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
	Token        string
	MaxBodyBytes int64
}

type hostHTTPHandler struct {
	service      *Service
	token        string
	maxBodyBytes int64
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
}

type errorResponse struct {
	Error string `json:"error"`
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
	server := &hostHTTPHandler{
		service:      service,
		token:        options.Token,
		maxBodyBytes: maxBodyBytes,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", server.health)
	mux.HandleFunc("POST /control/v1/register", server.register)
	mux.HandleFunc("POST /control/v1/renew", server.renew)
	mux.HandleFunc("POST /control/v1/unregister", server.unregister)
	mux.HandleFunc("POST /control/v1/publish", server.publish)
	mux.HandleFunc("POST /control/v1/poll", server.poll)
	mux.HandleFunc("POST /control/v1/ack", server.acknowledge)
	mux.HandleFunc("POST /control/v1/run", server.reportRun)
	mux.HandleFunc("POST /control/v1/outcome", server.reportOutcome)
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
		if request.URL.Path != "/health" {
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
			Requests:      []HostControlDelivery{},
			Cancellations: []string{},
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
	if err := server.service.ReportHostOutcome(
		input.HostID,
		input.LeaseID,
		input.Outcome,
	); err != nil {
		writeServiceError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]string{"status": "recorded"})
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

func writeServiceError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalid):
		writeHTTPError(response, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrForbidden):
		writeHTTPError(response, http.StatusForbidden, err.Error())
	case errors.Is(err, ErrNotFound):
		writeHTTPError(response, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrLeaseExpired), errors.Is(err, ErrUnavailable):
		writeHTTPError(response, http.StatusGone, err.Error())
	case errors.Is(err, ErrLeaseConflict), errors.Is(err, ErrStale):
		writeHTTPError(response, http.StatusConflict, err.Error())
	case errors.Is(err, ErrConflict):
		writeHTTPError(response, http.StatusConflict, err.Error())
	case errors.Is(err, ErrCapacity):
		writeHTTPError(response, http.StatusTooManyRequests, err.Error())
	default:
		writeHTTPError(response, http.StatusInternalServerError, "internal error")
	}
}

func writeHTTPError(response http.ResponseWriter, status int, message string) {
	writeJSON(response, status, errorResponse{Error: message})
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
