package controlplane

import (
	"context"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/sunrioa/rin/host"
)

const (
	maxPendingHostGatewayRequests = 1_024
	maxHostGatewayWait            = 25 * time.Second
)

// pollingActionHost bridges ActionGateway to an authoritative Host connected
// through the existing long-poll transport. It never binds effects locally.
type pollingActionHost struct {
	service *Service
}

func (adapter *pollingActionHost) BindAction(
	ctx context.Context,
	target ActorControlTarget,
	request host.ActionRequest,
) (ActionBindingResult, error) {
	result, err := adapter.exchange(ctx, HostGatewayRequest{
		Kind:          HostGatewayBind,
		Target:        target,
		ActionRequest: actionRequestPointer(request),
	})
	if err != nil {
		return ActionBindingResult{}, err
	}
	if result.Binding == nil {
		return ActionBindingResult{}, fmt.Errorf(
			"%w: Host returned no action binding",
			ErrUnavailable,
		)
	}
	return cloneActionBindingResult(*result.Binding), nil
}

func (adapter *pollingActionHost) SnapshotAction(
	ctx context.Context,
	target ActorControlTarget,
) (ActionHostSnapshot, error) {
	result, err := adapter.exchange(ctx, HostGatewayRequest{
		Kind:   HostGatewaySnapshot,
		Target: target,
	})
	if err != nil {
		return ActionHostSnapshot{}, err
	}
	if result.Snapshot == nil {
		return ActionHostSnapshot{}, fmt.Errorf(
			"%w: Host returned no action snapshot",
			ErrUnavailable,
		)
	}
	return *result.Snapshot, nil
}

func (adapter *pollingActionHost) exchange(
	ctx context.Context,
	request HostGatewayRequest,
) (HostGatewayResult, error) {
	if adapter == nil || adapter.service == nil {
		return HostGatewayResult{}, ErrUnavailable
	}
	waitCtx, cancel := context.WithTimeout(ctx, maxHostGatewayWait)
	defer cancel()
	state, err := adapter.service.enqueueHostGateway(request)
	if err != nil {
		return HostGatewayResult{}, err
	}
	select {
	case response := <-state.result:
		return cloneHostGatewayResult(response.result), response.err
	case <-waitCtx.Done():
		adapter.service.cancelHostGateway(
			state.request.GatewayRequestID,
			state,
		)
		if ctx.Err() != nil {
			return HostGatewayResult{}, ctx.Err()
		}
		return HostGatewayResult{}, fmt.Errorf(
			"%w: Host gateway response timed out",
			ErrUnavailable,
		)
	}
}

func (service *Service) enqueueHostGateway(
	request HostGatewayRequest,
) (*hostGatewayState, error) {
	if err := validateHostGatewayRequest(request, false); err != nil {
		return nil, err
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.closed {
		return nil, ErrClosed
	}
	current, world, err := service.findWorldLocked(
		request.Target.HostID,
		request.Target.WorldID,
	)
	if err != nil {
		return nil, err
	}
	if current.lease.ExpiresAtUnixMillis <= service.now().UnixMilli() {
		return nil, ErrUnavailable
	}
	foundActor := false
	for _, actor := range world.Actors {
		if actor.ActorID == request.Target.ActorID {
			foundActor = true
			break
		}
	}
	if !foundActor {
		return nil, ErrNotFound
	}
	if len(service.hostGateway) >= maxPendingHostGatewayRequests {
		return nil, ErrCapacity
	}
	requestID, err := service.newID("gateway")
	if err != nil {
		return nil, err
	}
	request.GatewayRequestID = requestID
	request.SubmittedAt = service.now().UnixMilli()
	state := &hostGatewayState{
		request:  cloneHostGatewayRequest(request),
		result:   make(chan hostGatewayResponse, 1),
		attempts: 0,
	}
	service.hostGateway[requestID] = state
	service.notifyLocked()
	return state, nil
}

// ReportHostGatewayResult completes one bind or snapshot request previously
// delivered by PollHost. A late result for a cancelled request is rejected.
func (service *Service) ReportHostGatewayResult(
	hostID, leaseID string,
	result HostGatewayResult,
) error {
	service.mu.Lock()
	defer service.mu.Unlock()
	if _, err := service.requireLeaseLocked(hostID, leaseID); err != nil {
		return err
	}
	state, exists := service.hostGateway[result.GatewayRequestID]
	if !exists {
		return ErrNotFound
	}
	if state.request.Target.HostID != hostID {
		return ErrForbidden
	}
	if err := validateHostGatewayResult(result, state.request.Kind); err != nil {
		return err
	}
	delete(service.hostGateway, result.GatewayRequestID)
	response := hostGatewayResponse{result: cloneHostGatewayResult(result)}
	if result.ErrorCode != "" {
		response.err = hostGatewayError(result.ErrorCode, result.ErrorMessage)
	}
	state.result <- response
	service.notifyLocked()
	return nil
}

func (service *Service) cancelHostGateway(
	requestID string,
	state *hostGatewayState,
) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.hostGateway[requestID] != state {
		return
	}
	delete(service.hostGateway, requestID)
	service.notifyLocked()
}

func (service *Service) collectHostGatewayWorkLocked(
	hostID string,
	limit int,
) []HostGatewayDelivery {
	states := make([]*hostGatewayState, 0)
	for _, state := range service.hostGateway {
		if state.request.Target.HostID == hostID {
			states = append(states, state)
		}
	}
	slices.SortFunc(states, func(left, right *hostGatewayState) int {
		if left.request.SubmittedAt < right.request.SubmittedAt {
			return -1
		}
		if left.request.SubmittedAt > right.request.SubmittedAt {
			return 1
		}
		return compare(
			left.request.GatewayRequestID,
			right.request.GatewayRequestID,
		)
	})
	if len(states) > limit {
		states = states[:limit]
	}
	deliveries := make([]HostGatewayDelivery, 0, len(states))
	for _, state := range states {
		if state.attempts < math.MaxUint32 {
			state.attempts++
		}
		deliveries = append(deliveries, HostGatewayDelivery{
			Request:         cloneHostGatewayRequest(state.request),
			DeliveryAttempt: state.attempts,
		})
	}
	return deliveries
}

func (service *Service) failHostGatewayLocked(hostID string, err error) {
	for requestID, state := range service.hostGateway {
		if state.request.Target.HostID != hostID {
			continue
		}
		delete(service.hostGateway, requestID)
		state.result <- hostGatewayResponse{err: err}
	}
}

func (service *Service) failAllHostGatewayLocked(err error) {
	for requestID, state := range service.hostGateway {
		delete(service.hostGateway, requestID)
		state.result <- hostGatewayResponse{err: err}
	}
}

func validateHostGatewayRequest(
	request HostGatewayRequest,
	requireTransportFields bool,
) error {
	if requireTransportFields {
		if err := validateID("gateway_request_id", request.GatewayRequestID); err != nil {
			return err
		}
		if request.SubmittedAt <= 0 || request.SubmittedAt > maxJSONSafeInteger {
			return invalid(
				"submitted_at_unix_millis",
				"must be a positive JSON-safe integer",
			)
		}
	} else if request.GatewayRequestID != "" || request.SubmittedAt != 0 {
		return invalid("gateway_request", "transport fields must be empty")
	}
	if err := validateActorControlTarget(request.Target); err != nil {
		return err
	}
	switch request.Kind {
	case HostGatewayBind:
		if request.ActionRequest == nil {
			return invalid("action_request", "is required for bind")
		}
		if err := host.ValidateActionRequest(*request.ActionRequest); err != nil {
			return invalid("action_request", err.Error())
		}
		if request.ActionRequest.ActorID != request.Target.ActorID ||
			request.ActionRequest.ExpectedEpoch.WorldID != request.Target.WorldID {
			return invalid("action_request", "does not match target")
		}
	case HostGatewaySnapshot:
		if request.ActionRequest != nil {
			return invalid("action_request", "must be empty for snapshot")
		}
	default:
		return invalid("kind", "must be bind or snapshot")
	}
	return nil
}

func validateHostGatewayResult(
	result HostGatewayResult,
	kind HostGatewayKind,
) error {
	if err := validateID("gateway_request_id", result.GatewayRequestID); err != nil {
		return err
	}
	if result.ErrorCode != "" {
		if result.Binding != nil || result.Snapshot != nil {
			return invalid("gateway_result", "error cannot include a successful result")
		}
		if err := validateText("error_message", result.ErrorMessage, 500, true); err != nil {
			return err
		}
		switch result.ErrorCode {
		case "invalid", "stale", "unavailable", "forbidden", "conflict", "internal":
			return nil
		default:
			return invalid("error_code", "is not supported")
		}
	}
	if result.ErrorMessage != "" {
		return invalid("error_message", "requires error_code")
	}
	switch kind {
	case HostGatewayBind:
		if result.Binding == nil || result.Snapshot != nil {
			return invalid("gateway_result", "bind requires only binding")
		}
		if err := validateActionHostSnapshot(result.Binding.Snapshot); err != nil {
			return invalid("binding.snapshot", err.Error())
		}
		if err := host.ValidateBoundAction(result.Binding.Action); err != nil {
			return invalid("binding.bound_action", err.Error())
		}
	case HostGatewaySnapshot:
		if result.Snapshot == nil || result.Binding != nil {
			return invalid("gateway_result", "snapshot requires only snapshot")
		}
		if err := validateActionHostSnapshot(*result.Snapshot); err != nil {
			return invalid("snapshot", err.Error())
		}
	default:
		return invalid("kind", "is not supported")
	}
	return nil
}

func hostGatewayError(code, message string) error {
	var target error
	switch code {
	case "invalid":
		target = ErrInvalid
	case "stale":
		target = ErrStale
	case "unavailable", "internal":
		target = ErrUnavailable
	case "forbidden":
		target = ErrForbidden
	case "conflict":
		target = ErrConflict
	default:
		target = ErrUnavailable
	}
	return fmt.Errorf("%w: Host gateway %s: %s", target, code, message)
}

func cloneHostGatewayRequest(value HostGatewayRequest) HostGatewayRequest {
	if value.ActionRequest != nil {
		request := cloneActionRequest(*value.ActionRequest)
		value.ActionRequest = &request
	}
	return value
}

func cloneActionBindingResult(value ActionBindingResult) ActionBindingResult {
	value.Action = cloneBoundAction(value.Action)
	return value
}

func cloneHostGatewayResult(value HostGatewayResult) HostGatewayResult {
	if value.Binding != nil {
		binding := cloneActionBindingResult(*value.Binding)
		value.Binding = &binding
	}
	if value.Snapshot != nil {
		snapshot := *value.Snapshot
		value.Snapshot = &snapshot
	}
	return value
}

var _ ActionHost = (*pollingActionHost)(nil)
