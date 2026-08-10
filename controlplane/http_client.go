package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/internal/jsonwire"
)

const (
	defaultControlClientTimeout          = 30 * time.Second
	defaultControlClientMaxResponseBytes = int64(8 << 20)
)

// HTTPClient connects a thin external-control client to one Control Daemon.
type HTTPClient struct {
	baseURL *url.URL
	token   string
	client  *http.Client
}

// NewHTTPClient creates a loopback-only client for the Control Daemon.
func NewHTTPClient(baseURL, token string) (*HTTPClient, error) {
	parsed, err := validateControlBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	if len(token) < 32 {
		return nil, fmt.Errorf(
			"%w: token must contain at least 32 bytes",
			ErrInvalid,
		)
	}
	return &HTTPClient{
		baseURL: parsed,
		token:   token,
		client: &http.Client{
			Timeout: defaultControlClientTimeout,
			CheckRedirect: func(
				_ *http.Request,
				_ []*http.Request,
			) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// Info returns the Daemon's fixed principal and contract identity.
func (client *HTTPClient) Info(ctx context.Context) (ClientInfo, error) {
	var info ClientInfo
	if err := client.requestV2(ctx, http.MethodGet, "info", nil, &info); err != nil {
		return ClientInfo{}, err
	}
	if info.ContractVersion != ContractVersion {
		return ClientInfo{}, fmt.Errorf(
			"%w: unsupported Control contract %q",
			ErrInvalid,
			info.ContractVersion,
		)
	}
	if err := host.ValidatePrincipal(info.Principal); err != nil {
		return ClientInfo{}, fmt.Errorf(
			"%w: invalid daemon principal: %v",
			ErrInvalid,
			err,
		)
	}
	if !principalHasControlScope(info.Principal) {
		return ClientInfo{}, fmt.Errorf(
			"%w: daemon principal has no Control Plane scope",
			ErrInvalid,
		)
	}
	info.Principal = clonePrincipalValue(info.Principal)
	return info, nil
}

// ListWorlds returns worlds visible to the Daemon's fixed principal.
func (client *HTTPClient) ListWorlds(ctx context.Context) ([]WorldView, error) {
	var views []WorldView
	if err := client.requestV2(
		ctx,
		http.MethodPost,
		"worlds",
		emptyRequest{},
		&views,
	); err != nil {
		return nil, err
	}
	return views, nil
}

// ListActors returns visible actors in one Host-published world.
func (client *HTTPClient) ListActors(
	ctx context.Context,
	hostID, worldID string,
) ([]ActorView, error) {
	var views []ActorView
	if err := client.requestV2(
		ctx,
		http.MethodPost,
		"actors",
		worldTargetRequest{HostID: hostID, WorldID: worldID},
		&views,
	); err != nil {
		return nil, err
	}
	return views, nil
}

// GetActor returns one visible actor snapshot.
func (client *HTTPClient) GetActor(
	ctx context.Context,
	hostID, worldID, actorID string,
) (ActorView, error) {
	var view ActorView
	err := client.requestV2(
		ctx,
		http.MethodPost,
		"actor",
		actorTargetRequest{
			HostID: hostID, WorldID: worldID, ActorID: actorID,
		},
		&view,
	)
	return view, err
}

// WaitActor waits for a newer actor cursor for at most 25 seconds.
func (client *HTTPClient) WaitActor(
	ctx context.Context,
	input WaitActorInput,
) (ActorUpdate, error) {
	var update ActorUpdate
	err := client.requestV2(
		ctx,
		http.MethodPost,
		"wait-actor",
		input,
		&update,
	)
	return update, err
}

// ListActorOffers returns exact Host-published offers for one actor.
func (client *HTTPClient) ListActorOffers(
	ctx context.Context,
	hostID, worldID, actorID string,
) ([]host.ActionOffer, error) {
	var offers []host.ActionOffer
	if err := client.request(
		ctx,
		http.MethodPost,
		"offers",
		actorTargetRequest{
			HostID: hostID, WorldID: worldID, ActorID: actorID,
		},
		&offers,
	); err != nil {
		return nil, err
	}
	return offers, nil
}

// SendActorMessage queues plain conversation.
func (client *HTTPClient) SendActorMessage(
	ctx context.Context,
	input ActorTextInput,
) (OperationView, error) {
	return client.operation(ctx, "message", input)
}

// SendActorDirective queues a negotiable goal.
func (client *HTTPClient) SendActorDirective(
	ctx context.Context,
	input ActorTextInput,
) (OperationView, error) {
	return client.operation(ctx, "directive", input)
}

// SubmitActorUtterance queues dialogue from the current external controller.
func (client *HTTPClient) SubmitActorUtterance(
	ctx context.Context,
	input ActorUtteranceInput,
) (OperationView, error) {
	var operation OperationView
	err := client.request(
		ctx,
		http.MethodPost,
		"utterance",
		input,
		&operation,
	)
	return operation, err
}

// ExecuteActorOffer selects one exact Host-published Offer.
func (client *HTTPClient) ExecuteActorOffer(
	ctx context.Context,
	input ExecuteOfferInput,
) (OperationView, error) {
	var operation OperationView
	err := client.request(
		ctx,
		http.MethodPost,
		"execute-offer",
		input,
		&operation,
	)
	return operation, err
}

// GetOperation returns one operation visible to the fixed principal.
func (client *HTTPClient) GetOperation(
	ctx context.Context,
	operationID string,
) (OperationView, error) {
	return client.operationTargetV2(ctx, "operations/get", operationID)
}

// WaitOperation waits for a newer operation cursor or reportable terminal
// state for at most 25 seconds.
func (client *HTTPClient) WaitOperation(
	ctx context.Context,
	input WaitOperationInput,
) (OperationUpdate, error) {
	var update OperationUpdate
	err := client.requestV2(
		ctx,
		http.MethodPost,
		"operations/wait",
		input,
		&update,
	)
	return update, err
}

// CancelOperation requests cancellation without implying rollback.
func (client *HTTPClient) CancelOperation(
	ctx context.Context,
	operationID string,
) (OperationView, error) {
	return client.operationTargetV2(ctx, "operations/cancel", operationID)
}

// GetObservation returns the latest Host-published V2 observation.
func (client *HTTPClient) GetObservation(
	ctx context.Context,
	target ActorControlTarget,
) (host.ObservationEnvelope, error) {
	var observation host.ObservationEnvelope
	err := client.requestV2(
		ctx,
		http.MethodPost,
		"observe",
		target,
		&observation,
	)
	return observation, err
}

// ListCapabilities returns the exact V2 catalog published for one Actor.
func (client *HTTPClient) ListCapabilities(
	ctx context.Context,
	target ActorControlTarget,
) (host.CapabilitySnapshot, error) {
	var snapshot host.CapabilitySnapshot
	err := client.requestV2(
		ctx,
		http.MethodPost,
		"capabilities",
		target,
		&snapshot,
	)
	return snapshot, err
}

// DescribeCapability returns one exact V2 capability spec.
func (client *HTTPClient) DescribeCapability(
	ctx context.Context,
	input DescribeCapabilityInput,
) (host.CapabilitySpec, error) {
	var spec host.CapabilitySpec
	err := client.requestV2(
		ctx,
		http.MethodPost,
		"capability",
		input,
		&spec,
	)
	return spec, err
}

func (client *HTTPClient) AcquireController(
	ctx context.Context,
	input AcquireControllerInput,
) (ControllerLease, error) {
	var lease ControllerLease
	err := client.requestV2(
		ctx,
		http.MethodPost,
		"controllers/acquire",
		input,
		&lease,
	)
	return lease, err
}

func (client *HTTPClient) RenewController(
	ctx context.Context,
	input RenewControllerInput,
) (ControllerLease, error) {
	var lease ControllerLease
	err := client.requestV2(
		ctx,
		http.MethodPost,
		"controllers/renew",
		input,
		&lease,
	)
	return lease, err
}

func (client *HTTPClient) ReleaseController(
	ctx context.Context,
	input ReleaseControllerInput,
) error {
	var status statusResponse
	return client.requestV2(
		ctx,
		http.MethodPost,
		"controllers/release",
		input,
		&status,
	)
}

func (client *HTTPClient) GetController(
	ctx context.Context,
	target ActorControlTarget,
) (ControllerLease, error) {
	var lease ControllerLease
	err := client.requestV2(
		ctx,
		http.MethodPost,
		"controllers/get",
		target,
		&lease,
	)
	return lease, err
}

func (client *HTTPClient) SubmitAction(
	ctx context.Context,
	input SubmitActionInput,
) (OperationView, error) {
	var operation OperationView
	err := client.requestV2(
		ctx,
		http.MethodPost,
		"actions/submit",
		input,
		&operation,
	)
	return operation, err
}

func (client *HTTPClient) ConfirmAction(
	ctx context.Context,
	operationID string,
) (OperationView, error) {
	return client.operationTargetV2(ctx, "actions/confirm", operationID)
}

func (client *HTTPClient) SetEmergencyStop(
	ctx context.Context,
	input SetEmergencyStopInput,
) (ActorEmergencyStop, error) {
	var stop ActorEmergencyStop
	err := client.requestV2(
		ctx,
		http.MethodPost,
		"emergency-stop",
		input,
		&stop,
	)
	return stop, err
}

func (client *HTTPClient) operation(
	ctx context.Context,
	action string,
	input ActorTextInput,
) (OperationView, error) {
	var operation OperationView
	err := client.request(
		ctx,
		http.MethodPost,
		action,
		input,
		&operation,
	)
	return operation, err
}

func (client *HTTPClient) operationTarget(
	ctx context.Context,
	action, operationID string,
) (OperationView, error) {
	var operation OperationView
	err := client.request(
		ctx,
		http.MethodPost,
		action,
		operationTargetRequest{OperationID: operationID},
		&operation,
	)
	return operation, err
}

func (client *HTTPClient) operationTargetV2(
	ctx context.Context,
	action, operationID string,
) (OperationView, error) {
	var operation OperationView
	err := client.requestV2(
		ctx,
		http.MethodPost,
		action,
		operationTargetRequest{OperationID: operationID},
		&operation,
	)
	return operation, err
}

type statusResponse struct {
	Status string `json:"status"`
}

func (client *HTTPClient) requestV2(
	ctx context.Context,
	method, action string,
	input, output any,
) error {
	return client.requestAt(
		ctx,
		method,
		"/control/v2/"+action,
		input,
		output,
	)
}

func (client *HTTPClient) request(
	ctx context.Context,
	method, action string,
	input, output any,
) error {
	return client.requestAt(
		ctx,
		method,
		"/control/v1/client/"+action,
		input,
		output,
	)
}

func (client *HTTPClient) requestAt(
	ctx context.Context,
	method, path string,
	input, output any,
) error {
	var body io.Reader
	if input != nil {
		payload, err := json.Marshal(input)
		if err != nil {
			return fmt.Errorf("%w: encode request: %v", ErrInvalid, err)
		}
		body = bytes.NewReader(payload)
	}
	endpoint := *client.baseURL
	endpoint.Path = path
	request, err := http.NewRequestWithContext(
		ctx,
		method,
		endpoint.String(),
		body,
	)
	if err != nil {
		return fmt.Errorf("%w: create request: %v", ErrInvalid, err)
	}
	request.Header.Set("Authorization", "Bearer "+client.token)
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.client.Do(request)
	if err != nil {
		return fmt.Errorf("%w: Control Daemon request: %v", ErrUnavailable, err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(
		response.Body,
		defaultControlClientMaxResponseBytes+1,
	))
	if err != nil {
		return fmt.Errorf("%w: read Control Daemon response: %v", ErrUnavailable, err)
	}
	if int64(len(payload)) > defaultControlClientMaxResponseBytes {
		return fmt.Errorf("%w: Control Daemon response is too large", ErrUnavailable)
	}
	contentType, _, contentTypeErr := mime.ParseMediaType(
		response.Header.Get("Content-Type"),
	)
	if contentTypeErr != nil || contentType != "application/json" {
		return fmt.Errorf("%w: Control Daemon returned non-JSON", ErrUnavailable)
	}
	if err := jsonwire.Validate(payload); err != nil {
		return fmt.Errorf(
			"%w: invalid Control Daemon JSON: %v",
			ErrUnavailable,
			err,
		)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var remote errorResponse
		if err := decodeHTTPClientJSON(payload, &remote); err != nil {
			return fmt.Errorf(
				"%w: Control Daemon returned HTTP %d",
				ErrUnavailable,
				response.StatusCode,
			)
		}
		return controlClientStatusError(
			response.StatusCode,
			remote.Code,
			remote.Error,
		)
	}
	if err := decodeHTTPClientJSON(payload, output); err != nil {
		return fmt.Errorf(
			"%w: decode Control Daemon response: %v",
			ErrUnavailable,
			err,
		)
	}
	return nil
}

var _ ControlClient = (*HTTPClient)(nil)

func validateControlBaseURL(value string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return nil, fmt.Errorf("%w: invalid Control Daemon URL: %v", ErrInvalid, err)
	}
	if parsed.Scheme != "http" ||
		parsed.User != nil ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" ||
		(parsed.Path != "" && parsed.Path != "/") {
		return nil, fmt.Errorf(
			"%w: Control Daemon URL must be a plain loopback HTTP origin",
			ErrInvalid,
		)
	}
	hostName, port, err := net.SplitHostPort(parsed.Host)
	if err != nil || port == "" {
		return nil, fmt.Errorf(
			"%w: Control Daemon URL requires an explicit port",
			ErrInvalid,
		)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65_535 {
		return nil, fmt.Errorf(
			"%w: Control Daemon URL has an invalid port",
			ErrInvalid,
		)
	}
	if !strings.EqualFold(hostName, "localhost") {
		ip := net.ParseIP(hostName)
		if ip == nil || !ip.IsLoopback() {
			return nil, fmt.Errorf(
				"%w: Control Daemon URL must use a loopback host",
				ErrInvalid,
			)
		}
	}
	parsed.Path = ""
	return parsed, nil
}

func decodeHTTPClientJSON(payload []byte, target any) error {
	return decodeSingleJSON(bytes.NewReader(payload), target)
}

func controlClientStatusError(status int, code, message string) error {
	if message == "" {
		message = http.StatusText(status)
	}
	var target error
	switch code {
	case "invalid":
		target = ErrInvalid
	case "forbidden":
		target = ErrForbidden
	case "not_found":
		target = ErrNotFound
	case "lease_expired":
		target = ErrLeaseExpired
	case "unavailable":
		target = ErrUnavailable
	case "not_accepted":
		target = ErrNotAccepted
	case "lease_conflict":
		target = ErrLeaseConflict
	case "stale":
		target = ErrStale
	case "conflict":
		target = ErrConflict
	case "capacity":
		target = ErrCapacity
	case "internal":
		target = ErrUnavailable
	}
	if target == nil {
		switch status {
		case http.StatusBadRequest:
			target = ErrInvalid
		case http.StatusUnauthorized, http.StatusForbidden:
			target = ErrForbidden
		case http.StatusNotFound:
			target = ErrNotFound
		case http.StatusConflict:
			target = ErrConflict
		case http.StatusGone:
			target = ErrUnavailable
		case http.StatusTooManyRequests:
			target = ErrCapacity
		default:
			target = ErrUnavailable
		}
	}
	return fmt.Errorf("%w: %s", target, message)
}
