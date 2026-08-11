package controlplane

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/internal/jsonwire"
)

const defaultHTTPMaxBodyBytes int64 = 1 << 20

// HTTPOptions configures the loopback Host Control transport.
type HTTPOptions struct {
	Token           string
	MaxBodyBytes    int64
	ClientPrincipal *host.Principal
}

type hostHTTPHandler struct {
	service      *Service
	token        string
	maxBodyBytes int64
	client       *ClientService
	handler      http.Handler
}

type leaseRequest struct {
	HostID  string `json:"host_id"`
	LeaseID string `json:"lease_id"`
}

type publishRequest struct {
	HostID      string           `json:"host_id"`
	LeaseID     string           `json:"lease_id"`
	Publication WorldPublication `json:"publication"`
}

type pollRequest struct {
	HostID     string `json:"host_id"`
	LeaseID    string `json:"lease_id"`
	Limit      int    `json:"limit"`
	WaitMillis uint32 `json:"wait_millis"`
}

type acknowledgementRequest struct {
	HostID          string              `json:"host_id"`
	LeaseID         string              `json:"lease_id"`
	Acknowledgement HostAcknowledgement `json:"acknowledgement"`
}

type runRequest struct {
	HostID  string         `json:"host_id"`
	LeaseID string         `json:"lease_id"`
	Run     host.ActionRun `json:"run"`
}

type outcomeRequest struct {
	HostID  string             `json:"host_id"`
	LeaseID string             `json:"lease_id"`
	Outcome host.ActionOutcome `json:"outcome"`
	Output  json.RawMessage    `json:"output,omitempty"`
}

type gatewayResultRequest struct {
	HostID  string            `json:"host_id"`
	LeaseID string            `json:"lease_id"`
	Result  HostGatewayResult `json:"result"`
}

type emptyRequest struct{}

type worldTargetRequest struct {
	HostID  string `json:"host_id"`
	WorldID string `json:"world_id"`
}

type actorTargetRequest struct {
	HostID  string `json:"host_id"`
	WorldID string `json:"world_id"`
	ActorID string `json:"actor_id"`
}

type operationTargetRequest struct {
	OperationID string `json:"operation_id"`
}

type errorResponse struct {
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
}

// NewHTTPHandler creates a token-authenticated Host Control handler.
func NewHTTPHandler(service *Service, options HTTPOptions) (http.Handler, error) {
	if service == nil {
		return nil, fmt.Errorf("%w: service is required", ErrInvalid)
	}
	if len(options.Token) < 32 {
		return nil, fmt.Errorf("%w: token must contain at least 32 bytes", ErrInvalid)
	}
	maxBodyBytes := options.MaxBodyBytes
	if maxBodyBytes <= 0 {
		maxBodyBytes = defaultHTTPMaxBodyBytes
	}
	if maxBodyBytes > 8<<20 {
		return nil, fmt.Errorf("%w: max body must not exceed 8 MiB", ErrInvalid)
	}
	var client *ClientService
	if options.ClientPrincipal != nil {
		if err := host.ValidatePrincipal(*options.ClientPrincipal); err != nil {
			return nil, fmt.Errorf(
				"%w: client principal: %v",
				ErrInvalid,
				err,
			)
		}
		if !principalHasControlScope(*options.ClientPrincipal) {
			return nil, fmt.Errorf(
				"%w: client principal has no Control Plane scope",
				ErrInvalid,
			)
		}
		principal := clonePrincipalValue(*options.ClientPrincipal)
		createdClient, err := NewClientService(service, principal)
		if err != nil {
			return nil, err
		}
		client = createdClient
	}
	server := &hostHTTPHandler{
		service:      service,
		token:        options.Token,
		maxBodyBytes: maxBodyBytes,
		client:       client,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", server.health)
	mux.HandleFunc("GET /control/v2/health", server.health)
	mux.HandleFunc("POST /control/v2/host/register", server.register)
	mux.HandleFunc("POST /control/v2/host/renew", server.renew)
	mux.HandleFunc("POST /control/v2/host/unregister", server.unregister)
	mux.HandleFunc("POST /control/v2/host/publish", server.publish)
	mux.HandleFunc("POST /control/v2/host/poll", server.poll)
	mux.HandleFunc("POST /control/v2/host/ack", server.acknowledge)
	mux.HandleFunc("POST /control/v2/host/run", server.reportRun)
	mux.HandleFunc("POST /control/v2/host/outcome", server.reportOutcome)
	mux.HandleFunc("POST /control/v2/host/gateway-result", server.reportGatewayResult)
	if client != nil {
		mux.HandleFunc("GET /control/v2/info", server.clientInfo)
		mux.HandleFunc("POST /control/v2/worlds", server.clientWorlds)
		mux.HandleFunc("POST /control/v2/actors", server.clientActors)
		mux.HandleFunc("POST /control/v2/actor", server.clientActor)
		mux.HandleFunc("POST /control/v2/wait-actor", server.clientWaitActor)
		mux.HandleFunc("POST /control/v2/observe", server.clientObservation)
		mux.HandleFunc("POST /control/v2/capabilities", server.clientCapabilities)
		mux.HandleFunc("POST /control/v2/capability", server.clientCapability)
		mux.HandleFunc("POST /control/v2/controllers/acquire", server.clientAcquireController)
		mux.HandleFunc("POST /control/v2/controllers/renew", server.clientRenewController)
		mux.HandleFunc("POST /control/v2/controllers/release", server.clientReleaseController)
		mux.HandleFunc("POST /control/v2/controllers/get", server.clientGetController)
		mux.HandleFunc("POST /control/v2/actions/submit", server.clientSubmitAction)
		mux.HandleFunc("POST /control/v2/actions/confirm", server.clientConfirmAction)
		mux.HandleFunc("POST /control/v2/operations/get", server.clientOperation)
		mux.HandleFunc("POST /control/v2/operations/wait", server.clientWaitOperation)
		mux.HandleFunc("POST /control/v2/operations/cancel", server.clientCancel)
		mux.HandleFunc("POST /control/v2/emergency-stop", server.clientEmergencyStop)
	}
	server.handler = server.secure(mux)
	return server, nil
}

func (server *hostHTTPHandler) ServeHTTP(
	response http.ResponseWriter,
	request *http.Request,
) {
	server.handler.ServeHTTP(response, request)
}

func (server *hostHTTPHandler) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		response.Header().Set("Cache-Control", "no-store")
		response.Header().Set("X-Content-Type-Options", "nosniff")
		if request.URL.Path != "/health" &&
			request.URL.Path != "/control/v2/health" {
			provided := strings.TrimPrefix(
				request.Header.Get("Authorization"), "Bearer ",
			)
			if len(provided) != len(server.token) ||
				subtle.ConstantTimeCompare(
					[]byte(provided), []byte(server.token),
				) != 1 {
				response.Header().Set("WWW-Authenticate", "Bearer")
				writeHTTPError(response, http.StatusUnauthorized, "unauthorized")
				return
			}
		}
		next.ServeHTTP(response, request)
	})
}

func (server *hostHTTPHandler) health(
	response http.ResponseWriter,
	_ *http.Request,
) {
	writeJSON(response, http.StatusOK, map[string]string{"status": "ok"})
}

func (server *hostHTTPHandler) register(
	response http.ResponseWriter,
	request *http.Request,
) {
	var input HostRegistration
	if err := server.decode(response, request, &input); err != nil {
		writeHTTPError(response, http.StatusBadRequest, err.Error())
		return
	}
	lease, err := server.service.RegisterHost(input)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, lease)
}

func (server *hostHTTPHandler) renew(
	response http.ResponseWriter,
	request *http.Request,
) {
	var input leaseRequest
	if err := server.decode(response, request, &input); err != nil {
		writeHTTPError(response, http.StatusBadRequest, err.Error())
		return
	}
	lease, err := server.service.RenewHost(input.HostID, input.LeaseID)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, lease)
}

func (server *hostHTTPHandler) unregister(
	response http.ResponseWriter,
	request *http.Request,
) {
	var input leaseRequest
	if err := server.decode(response, request, &input); err != nil {
		writeHTTPError(response, http.StatusBadRequest, err.Error())
		return
	}
	if err := server.service.UnregisterHost(input.HostID, input.LeaseID); err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]string{"status": "offline"})
}

func (server *hostHTTPHandler) publish(
	response http.ResponseWriter,
	request *http.Request,
) {
	var input publishRequest
	if err := server.decode(response, request, &input); err != nil {
		writeHTTPError(response, http.StatusBadRequest, err.Error())
		return
	}
	if err := server.service.PublishWorld(
		input.HostID, input.LeaseID, input.Publication,
	); err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]string{"status": "published"})
}

func (server *hostHTTPHandler) poll(
	response http.ResponseWriter,
	request *http.Request,
) {
	var input pollRequest
	if err := server.decode(response, request, &input); err != nil {
		writeHTTPError(response, http.StatusBadRequest, err.Error())
		return
	}
	if input.WaitMillis > 25_000 {
		writeHTTPError(
			response,
			http.StatusBadRequest,
			"wait_millis must not exceed 25000",
		)
		return
	}
	ctx, cancel := context.WithTimeout(
		request.Context(),
		time.Duration(input.WaitMillis)*time.Millisecond,
	)
	defer cancel()
	batch, err := server.service.PollHost(
		ctx,
		input.HostID,
		input.LeaseID,
		input.Limit,
	)
	if errors.Is(err, context.DeadlineExceeded) {
		writeJSON(response, http.StatusOK, HostControlBatch{
			GatewayRequests: []HostGatewayDelivery{},
			Requests:        []HostControlDelivery{},
			Cancellations:   []string{},
		})
		return
	}
	if errors.Is(err, context.Canceled) {
		return
	}
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, batch)
}

func (server *hostHTTPHandler) acknowledge(
	response http.ResponseWriter,
	request *http.Request,
) {
	var input acknowledgementRequest
	if err := server.decode(response, request, &input); err != nil {
		writeHTTPError(response, http.StatusBadRequest, err.Error())
		return
	}
	if err := server.service.AcknowledgeHost(
		input.HostID,
		input.LeaseID,
		input.Acknowledgement,
	); err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]string{"status": "acknowledged"})
}

func (server *hostHTTPHandler) reportRun(
	response http.ResponseWriter,
	request *http.Request,
) {
	var input runRequest
	if err := server.decode(response, request, &input); err != nil {
		writeHTTPError(response, http.StatusBadRequest, err.Error())
		return
	}
	if err := server.service.ReportHostRun(
		input.HostID,
		input.LeaseID,
		input.Run,
	); err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]string{"status": "recorded"})
}

func (server *hostHTTPHandler) reportOutcome(
	response http.ResponseWriter,
	request *http.Request,
) {
	var input outcomeRequest
	if err := server.decode(response, request, &input); err != nil {
		writeHTTPError(response, http.StatusBadRequest, err.Error())
		return
	}
	if err := server.service.ReportHostResult(
		input.HostID,
		input.LeaseID,
		input.Outcome,
		input.Output,
	); err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]string{"status": "recorded"})
}

func (server *hostHTTPHandler) reportGatewayResult(
	response http.ResponseWriter,
	request *http.Request,
) {
	var input gatewayResultRequest
	if err := server.decode(response, request, &input); err != nil {
		writeHTTPError(response, http.StatusBadRequest, err.Error())
		return
	}
	if err := server.service.ReportHostGatewayResult(
		input.HostID,
		input.LeaseID,
		input.Result,
	); err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]string{"status": "recorded"})
}

func (server *hostHTTPHandler) clientInfo(
	response http.ResponseWriter,
	request *http.Request,
) {
	info, err := server.client.Info(request.Context())
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, info)
}

func (server *hostHTTPHandler) clientWorlds(
	response http.ResponseWriter,
	request *http.Request,
) {
	var input emptyRequest
	if err := server.decode(response, request, &input); err != nil {
		writeHTTPError(response, http.StatusBadRequest, err.Error())
		return
	}
	views, err := server.client.ListWorlds(request.Context())
	if err != nil {
		writeServiceError(response, err)
		return
	}
	if views == nil {
		views = []WorldView{}
	}
	writeJSON(response, http.StatusOK, views)
}

func (server *hostHTTPHandler) clientActors(
	response http.ResponseWriter,
	request *http.Request,
) {
	var input worldTargetRequest
	if err := server.decode(response, request, &input); err != nil {
		writeHTTPError(response, http.StatusBadRequest, err.Error())
		return
	}
	views, err := server.client.ListActors(
		request.Context(),
		input.HostID,
		input.WorldID,
	)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	if views == nil {
		views = []ActorView{}
	}
	writeJSON(response, http.StatusOK, views)
}

func (server *hostHTTPHandler) clientActor(
	response http.ResponseWriter,
	request *http.Request,
) {
	var input actorTargetRequest
	if err := server.decode(response, request, &input); err != nil {
		writeHTTPError(response, http.StatusBadRequest, err.Error())
		return
	}
	view, err := server.client.GetActor(
		request.Context(),
		input.HostID,
		input.WorldID,
		input.ActorID,
	)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, view)
}

func (server *hostHTTPHandler) clientWaitActor(
	response http.ResponseWriter,
	request *http.Request,
) {
	var input WaitActorInput
	if err := server.decode(response, request, &input); err != nil {
		writeHTTPError(response, http.StatusBadRequest, err.Error())
		return
	}
	update, err := server.client.WaitActor(
		request.Context(),
		input,
	)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, update)
}

func (server *hostHTTPHandler) clientObservation(
	response http.ResponseWriter,
	request *http.Request,
) {
	var input ActorControlTarget
	if err := server.decode(response, request, &input); err != nil {
		writeHTTPError(response, http.StatusBadRequest, err.Error())
		return
	}
	observation, err := server.client.GetObservation(request.Context(), input)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, observation)
}

func (server *hostHTTPHandler) clientCapabilities(
	response http.ResponseWriter,
	request *http.Request,
) {
	var input ActorControlTarget
	if err := server.decode(response, request, &input); err != nil {
		writeHTTPError(response, http.StatusBadRequest, err.Error())
		return
	}
	snapshot, err := server.client.ListCapabilities(request.Context(), input)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	if snapshot.Specs == nil {
		snapshot.Specs = []host.CapabilitySpec{}
	}
	writeJSON(response, http.StatusOK, snapshot)
}

func (server *hostHTTPHandler) clientCapability(
	response http.ResponseWriter,
	request *http.Request,
) {
	var input DescribeCapabilityInput
	if err := server.decode(response, request, &input); err != nil {
		writeHTTPError(response, http.StatusBadRequest, err.Error())
		return
	}
	spec, err := server.client.DescribeCapability(request.Context(), input)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, spec)
}

func (server *hostHTTPHandler) clientAcquireController(
	response http.ResponseWriter,
	request *http.Request,
) {
	var input AcquireControllerInput
	if err := server.decode(response, request, &input); err != nil {
		writeHTTPError(response, http.StatusBadRequest, err.Error())
		return
	}
	lease, err := server.client.AcquireController(request.Context(), input)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, lease)
}

func (server *hostHTTPHandler) clientRenewController(
	response http.ResponseWriter,
	request *http.Request,
) {
	var input RenewControllerInput
	if err := server.decode(response, request, &input); err != nil {
		writeHTTPError(response, http.StatusBadRequest, err.Error())
		return
	}
	lease, err := server.client.RenewController(request.Context(), input)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, lease)
}

func (server *hostHTTPHandler) clientReleaseController(
	response http.ResponseWriter,
	request *http.Request,
) {
	var input ReleaseControllerInput
	if err := server.decode(response, request, &input); err != nil {
		writeHTTPError(response, http.StatusBadRequest, err.Error())
		return
	}
	if err := server.client.ReleaseController(request.Context(), input); err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]string{"status": "released"})
}

func (server *hostHTTPHandler) clientGetController(
	response http.ResponseWriter,
	request *http.Request,
) {
	var input ActorControlTarget
	if err := server.decode(response, request, &input); err != nil {
		writeHTTPError(response, http.StatusBadRequest, err.Error())
		return
	}
	lease, err := server.client.GetController(request.Context(), input)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, lease)
}

func (server *hostHTTPHandler) clientSubmitAction(
	response http.ResponseWriter,
	request *http.Request,
) {
	var input SubmitActionInput
	if err := server.decode(response, request, &input); err != nil {
		writeHTTPError(response, http.StatusBadRequest, err.Error())
		return
	}
	operation, err := server.client.SubmitAction(request.Context(), input)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, operation)
}

func (server *hostHTTPHandler) clientConfirmAction(
	response http.ResponseWriter,
	request *http.Request,
) {
	var input operationTargetRequest
	if err := server.decode(response, request, &input); err != nil {
		writeHTTPError(response, http.StatusBadRequest, err.Error())
		return
	}
	operation, err := server.client.ConfirmAction(
		request.Context(),
		input.OperationID,
	)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, operation)
}

func (server *hostHTTPHandler) clientEmergencyStop(
	response http.ResponseWriter,
	request *http.Request,
) {
	var input SetEmergencyStopInput
	if err := server.decode(response, request, &input); err != nil {
		writeHTTPError(response, http.StatusBadRequest, err.Error())
		return
	}
	stop, err := server.client.SetEmergencyStop(request.Context(), input)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, stop)
}

func (server *hostHTTPHandler) clientOperation(
	response http.ResponseWriter,
	request *http.Request,
) {
	var input operationTargetRequest
	if err := server.decode(response, request, &input); err != nil {
		writeHTTPError(response, http.StatusBadRequest, err.Error())
		return
	}
	operation, err := server.client.GetOperation(
		request.Context(),
		input.OperationID,
	)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, operation)
}

func (server *hostHTTPHandler) clientWaitOperation(
	response http.ResponseWriter,
	request *http.Request,
) {
	var input WaitOperationInput
	if err := server.decode(response, request, &input); err != nil {
		writeHTTPError(response, http.StatusBadRequest, err.Error())
		return
	}
	update, err := server.client.WaitOperation(
		request.Context(),
		input,
	)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, update)
}

func (server *hostHTTPHandler) clientCancel(
	response http.ResponseWriter,
	request *http.Request,
) {
	var input operationTargetRequest
	if err := server.decode(response, request, &input); err != nil {
		writeHTTPError(response, http.StatusBadRequest, err.Error())
		return
	}
	operation, err := server.client.CancelOperation(
		request.Context(),
		input.OperationID,
	)
	if err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, operation)
}

func (server *hostHTTPHandler) decode(
	response http.ResponseWriter,
	request *http.Request,
	target any,
) error {
	contentType, _, err := mime.ParseMediaType(
		request.Header.Get("Content-Type"),
	)
	if err != nil || contentType != "application/json" {
		return errors.New("content type must be application/json")
	}
	request.Body = http.MaxBytesReader(
		response, request.Body, server.maxBodyBytes,
	)
	payload, err := io.ReadAll(request.Body)
	if err != nil {
		return errors.New("request body exceeds the configured limit")
	}
	if err := jsonwire.Validate(payload); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid request: %w", err)
	}
	return nil
}

func principalHasControlScope(principal host.Principal) bool {
	for _, scope := range []string{
		ScopeActorRead,
		ScopeActorControl,
		ScopeActorExecute,
		ScopeOperationCancel,
		ScopeHostAdmin,
	} {
		if hasScope(principal, scope) {
			return true
		}
	}
	return false
}

func writeServiceError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalid):
		writeHTTPErrorCode(response, http.StatusBadRequest, "invalid", err.Error())
	case errors.Is(err, ErrForbidden):
		writeHTTPErrorCode(response, http.StatusForbidden, "forbidden", err.Error())
	case errors.Is(err, ErrNotFound):
		writeHTTPErrorCode(response, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, ErrLeaseExpired):
		writeHTTPErrorCode(response, http.StatusGone, "lease_expired", err.Error())
	case errors.Is(err, ErrUnavailable):
		writeHTTPErrorCode(response, http.StatusGone, "unavailable", err.Error())
	case errors.Is(err, ErrNotAccepted):
		writeHTTPErrorCode(response, http.StatusConflict, "not_accepted", err.Error())
	case errors.Is(err, ErrLeaseConflict):
		writeHTTPErrorCode(response, http.StatusConflict, "lease_conflict", err.Error())
	case errors.Is(err, ErrStale):
		writeHTTPErrorCode(response, http.StatusConflict, "stale", err.Error())
	case errors.Is(err, ErrConflict):
		writeHTTPErrorCode(response, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, ErrCapacity):
		writeHTTPErrorCode(response, http.StatusTooManyRequests, "capacity", err.Error())
	default:
		writeHTTPErrorCode(response, http.StatusInternalServerError, "internal", "internal error")
	}
}

func writeHTTPError(response http.ResponseWriter, status int, message string) {
	writeHTTPErrorCode(response, status, "", message)
}

func writeHTTPErrorCode(
	response http.ResponseWriter,
	status int,
	code, message string,
) {
	writeJSON(response, status, errorResponse{Error: message, Code: code})
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	payload, err := json.Marshal(value)
	if err != nil {
		http.Error(response, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_, _ = response.Write(append(payload, '\n'))
}
