package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/policy"
)

const maxChildOperations = 1_024

// ActionHostSnapshot is trusted Host clock and timeline state used while
// binding or confirming an action.
type ActionHostSnapshot struct {
	Now            host.Timepoint `json:"now"`
	Epoch          host.Epoch     `json:"epoch"`
	ObservationSeq uint64         `json:"observation_sequence"`
}

// ActionBindingResult contains a Registry-sealed action and the exact Host
// state against which it was bound.
type ActionBindingResult struct {
	Action   host.BoundAction   `json:"bound_action"`
	Snapshot ActionHostSnapshot `json:"snapshot"`
}

// ActionHost is the trusted adapter port behind ActionGateway. Controllers and
// network clients never implement or directly invoke this interface.
type ActionHost interface {
	BindAction(
		context.Context,
		ActorControlTarget,
		host.ActionRequest,
	) (ActionBindingResult, error)
	SnapshotAction(
		context.Context,
		ActorControlTarget,
	) (ActionHostSnapshot, error)
}

// SubmitActionInput carries controller intent plus an optional macro parent.
type SubmitActionInput struct {
	HostID            string             `json:"host_id"`
	WorldID           string             `json:"world_id"`
	Request           host.ActionRequest `json:"action_request"`
	ParentOperationID string             `json:"parent_operation_id,omitempty"`
}

type actionFlight struct {
	fingerprint string
	done        chan struct{}
}

type actionSubmissionSnapshot struct {
	actor             ActorPublication
	controller        ControllerLease
	emergencyStopped  bool
	emergencyRevision uint64
}

// SubmitAction is the sole V2 mutation gateway. It asks the trusted Host to
// bind effects, evaluates those effects with Policy, and only queues an allow
// decision for Host execution.
func (service *Service) SubmitAction(
	ctx context.Context,
	principal host.Principal,
	input SubmitActionInput,
) (OperationView, error) {
	if err := validateSubmitActionInput(input); err != nil {
		return OperationView{}, err
	}
	if err := host.ValidatePrincipal(principal); err != nil {
		return OperationView{}, fmt.Errorf("%w: principal: %v", ErrInvalid, err)
	}
	principal = clonePrincipalValue(principal)
	if service.actionHost == nil || service.policyEngine == nil {
		return OperationView{}, fmt.Errorf("%w: V2 action gateway is not configured", ErrUnavailable)
	}
	requestDigest, err := host.ActionRequestDigest(input.Request)
	if err != nil {
		return OperationView{}, fmt.Errorf("%w: action_request: %v", ErrInvalid, err)
	}
	key := actionOperationRequestKey(principal.ID, input.Request.IdempotencyKey)
	fingerprint := actionSubmissionFingerprint(
		requestDigest,
		input.ParentOperationID,
		principal,
	)

	for {
		service.mu.Lock()
		if service.closed {
			service.mu.Unlock()
			return OperationView{}, ErrClosed
		}
		if existing, found, existingErr := service.idempotentActionLocked(
			key,
			fingerprint,
		); found || existingErr != nil {
			service.mu.Unlock()
			return existing, existingErr
		}
		if flight, exists := service.actionFlights[key]; exists {
			if flight.fingerprint != fingerprint {
				service.mu.Unlock()
				return OperationView{}, fmt.Errorf(
					"%w: idempotency_key was reused with different input",
					ErrConflict,
				)
			}
			done := flight.done
			service.mu.Unlock()
			select {
			case <-ctx.Done():
				return OperationView{}, ctx.Err()
			case <-done:
				continue
			}
		}
		snapshot, snapshotErr := service.prepareActionSubmissionLocked(
			principal,
			input,
		)
		if snapshotErr != nil {
			service.mu.Unlock()
			return OperationView{}, snapshotErr
		}
		flight := &actionFlight{fingerprint: fingerprint, done: make(chan struct{})}
		service.actionFlights[key] = flight
		service.mu.Unlock()

		view, submitErr := service.bindAuthorizeAndQueueAction(
			ctx,
			principal,
			input,
			requestDigest,
			key,
			fingerprint,
			snapshot,
		)
		service.finishActionFlight(key, flight)
		return view, submitErr
	}
}

func (service *Service) bindAuthorizeAndQueueAction(
	ctx context.Context,
	principal host.Principal,
	input SubmitActionInput,
	requestDigest, key, fingerprint string,
	submission actionSubmissionSnapshot,
) (OperationView, error) {
	target := ActorControlTarget{
		HostID: input.HostID, WorldID: input.WorldID, ActorID: input.Request.ActorID,
	}
	result, err := service.actionHost.BindAction(
		ctx,
		target,
		cloneActionRequest(input.Request),
	)
	if err != nil {
		return OperationView{}, err
	}
	if err := validateActionBindingResult(
		result,
		input.Request,
		requestDigest,
		submission.actor,
	); err != nil {
		return OperationView{}, fmt.Errorf("%w: Host binding: %v", ErrStale, err)
	}
	decision, err := service.policyEngine.Evaluate(
		result.Action,
		policy.Context{
			Now:              result.Snapshot.Now,
			CurrentEpoch:     result.Snapshot.Epoch,
			Principal:        clonePrincipalValue(principal),
			ServerID:         input.HostID,
			OwnerID:          submission.actor.OwnerPrincipalID,
			EmergencyStopped: submission.emergencyStopped,
		},
	)
	if err != nil {
		return OperationView{}, err
	}
	if err := policy.ValidateDecision(decision); err != nil {
		service.policyEngine.Finalize(decision.DecisionID, false)
		return OperationView{}, fmt.Errorf("%w: policy decision: %v", ErrInvalid, err)
	}

	service.mu.Lock()
	defer service.mu.Unlock()
	current, err := service.prepareActionSubmissionLocked(principal, input)
	if err != nil || !sameActionSubmissionSnapshot(submission, current) {
		service.policyEngine.Finalize(decision.DecisionID, false)
		if err != nil {
			return OperationView{}, err
		}
		return OperationView{}, ErrStale
	}
	if result.Snapshot.Epoch != current.actor.Epoch ||
		result.Snapshot.ObservationSeq != current.actor.ObservationSeq {
		service.policyEngine.Finalize(decision.DecisionID, false)
		return OperationView{}, ErrStale
	}
	if existing, found, existingErr := service.idempotentActionLocked(
		key,
		fingerprint,
	); found || existingErr != nil {
		service.policyEngine.Finalize(decision.DecisionID, false)
		return existing, existingErr
	}
	operationID, err := service.prepareOperationLocked()
	if err != nil {
		service.policyEngine.Finalize(decision.DecisionID, false)
		return OperationView{}, err
	}
	request := HostControlRequest{
		OperationID: operationID,
		RequestID:   input.Request.RequestID,
		Principal:   clonePrincipalValue(principal),
		HostID:      input.HostID,
		WorldID:     input.WorldID,
		ActorID:     input.Request.ActorID,
		Kind:        ControlAction,
		Binding: &ControlBinding{
			Epoch:             current.actor.Epoch,
			ObservationSeq:    current.actor.ObservationSeq,
			AuthorityRevision: effectiveAuthority(current.actor).Revision,
			ControllerLeaseID: current.controller.LeaseID,
		},
		ActionRequest:     actionRequestPointer(input.Request),
		BoundAction:       boundActionPointer(result.Action),
		PolicyDecision:    decisionPointer(decision),
		ParentOperationID: input.ParentOperationID,
		SubmittedAt:       service.now().UnixMilli(),
	}
	return service.storeActionOperationLocked(key, request, decision.Result)
}

// ConfirmAction approves and consumes the exact challenge attached to a
// pending operation. The original BoundAction is never rebound or replaced.
func (service *Service) ConfirmAction(
	ctx context.Context,
	approver host.Principal,
	operationID string,
) (OperationView, error) {
	if err := host.ValidatePrincipal(approver); err != nil {
		return OperationView{}, fmt.Errorf("%w: principal: %v", ErrInvalid, err)
	}
	if err := validateID("operation_id", operationID); err != nil {
		return OperationView{}, err
	}
	if service.actionHost == nil || service.policyEngine == nil {
		return OperationView{}, fmt.Errorf("%w: V2 action gateway is not configured", ErrUnavailable)
	}

	service.mu.Lock()
	operation, exists := service.operations[operationID]
	if !exists {
		service.mu.Unlock()
		return OperationView{}, ErrNotFound
	}
	if operation.status != OperationAwaitingConfirmation ||
		operation.request.ActionRequest == nil ||
		operation.request.BoundAction == nil ||
		operation.request.PolicyDecision == nil ||
		operation.request.PolicyDecision.Confirmation == nil {
		view := operationView(operation)
		complete := completeOperation(operation)
		service.mu.Unlock()
		if complete {
			return view, nil
		}
		return OperationView{}, ErrConflict
	}
	if _, busy := service.confirming[operationID]; busy {
		service.mu.Unlock()
		return OperationView{}, ErrConflict
	}
	if _, err := service.authorizeActorLocked(
		approver,
		operation.request.HostID,
		operation.request.WorldID,
		operation.request.ActorID,
		ScopeActorControl,
	); err != nil {
		service.mu.Unlock()
		return OperationView{}, err
	}
	input := SubmitActionInput{
		HostID:            operation.request.HostID,
		WorldID:           operation.request.WorldID,
		Request:           cloneActionRequest(*operation.request.ActionRequest),
		ParentOperationID: operation.request.ParentOperationID,
	}
	originalPrincipal := clonePrincipalValue(operation.request.Principal)
	current, err := service.prepareActionSubmissionLocked(originalPrincipal, input)
	if err != nil || current.emergencyStopped ||
		current.controller.LeaseID != controllerLeaseID(operation.request.Binding) {
		operation.status = OperationStale
		operation.updatedAt = service.now().UnixMilli()
		service.finalizeOperationPolicyLocked(operation, false)
		service.recordOperationTimelineLocked(operation)
		service.markOperationsDirtyLocked()
		service.notifyLocked()
		persistErr := service.persistOperationsLocked()
		view := operationView(operation)
		service.mu.Unlock()
		if persistErr != nil {
			return OperationView{}, persistErr
		}
		if err != nil {
			return view, err
		}
		return view, ErrStale
	}
	ownerID := current.actor.OwnerPrincipalID
	bound := cloneBoundAction(*operation.request.BoundAction)
	challenge := *operation.request.PolicyDecision.Confirmation
	service.confirming[operationID] = struct{}{}
	service.mu.Unlock()
	defer service.finishConfirmation(operationID)

	target := ActorControlTarget{
		HostID: input.HostID, WorldID: input.WorldID, ActorID: input.Request.ActorID,
	}
	snapshot, err := service.actionHost.SnapshotAction(ctx, target)
	if err != nil {
		return OperationView{}, err
	}
	if err := validateActionHostSnapshot(snapshot); err != nil {
		return OperationView{}, fmt.Errorf("%w: Host snapshot: %v", ErrInvalid, err)
	}
	if snapshot.Epoch != bound.ExpectedEpoch ||
		snapshot.ObservationSeq != bound.ObservationSeq {
		return service.stalePendingConfirmation(operationID)
	}
	if _, err := service.policyEngine.Approve(
		challenge.ChallengeID,
		approver,
		snapshot.Now,
	); err != nil {
		return OperationView{}, err
	}
	decision, err := service.policyEngine.Evaluate(
		bound,
		policy.Context{
			Now:            snapshot.Now,
			CurrentEpoch:   snapshot.Epoch,
			Principal:      originalPrincipal,
			ServerID:       input.HostID,
			OwnerID:        ownerID,
			ConfirmationID: challenge.ChallengeID,
		},
	)
	if err != nil {
		return OperationView{}, err
	}
	if err := policy.ValidateDecision(decision); err != nil {
		service.policyEngine.Finalize(decision.DecisionID, false)
		return OperationView{}, fmt.Errorf("%w: policy decision: %v", ErrInvalid, err)
	}
	if decision.Result != policy.Allow {
		return service.recordConfirmationDecision(operationID, decision)
	}

	service.mu.Lock()
	defer service.mu.Unlock()
	operation = service.operations[operationID]
	if operation == nil || operation.status != OperationAwaitingConfirmation {
		service.policyEngine.Finalize(decision.DecisionID, false)
		if operation == nil {
			return OperationView{}, ErrNotFound
		}
		return operationView(operation), nil
	}
	current, err = service.prepareActionSubmissionLocked(originalPrincipal, input)
	if err != nil || current.emergencyStopped ||
		current.actor.Epoch != snapshot.Epoch ||
		current.actor.ObservationSeq != snapshot.ObservationSeq ||
		current.controller.LeaseID != controllerLeaseID(operation.request.Binding) {
		service.policyEngine.Finalize(decision.DecisionID, false)
		operation.status = OperationStale
		operation.updatedAt = service.now().UnixMilli()
		service.recordOperationTimelineLocked(operation)
		service.markOperationsDirtyLocked()
		service.notifyLocked()
		persistErr := service.persistOperationsLocked()
		view := operationView(operation)
		if persistErr != nil {
			return OperationView{}, persistErr
		}
		if err != nil {
			return view, err
		}
		return view, ErrStale
	}
	operation.request.PolicyDecision = decisionPointer(decision)
	operation.status = OperationQueued
	operation.updatedAt = service.now().UnixMilli()
	service.recordOperationTimelineLocked(operation)
	service.markOperationsDirtyLocked()
	service.notifyLocked()
	if err := service.persistOperationsLocked(); err != nil {
		service.policyEngine.Finalize(decision.DecisionID, false)
		operation.status = OperationStale
		operation.updatedAt = service.now().UnixMilli()
		service.recordOperationTimelineLocked(operation)
		service.markOperationsDirtyLocked()
		service.notifyLocked()
		return OperationView{}, errors.Join(err, service.persistOperationsLocked())
	}
	return operationView(operation), nil
}

func (service *Service) recordConfirmationDecision(
	operationID string,
	decision policy.Decision,
) (OperationView, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	operation := service.operations[operationID]
	if operation == nil {
		return OperationView{}, ErrNotFound
	}
	if operation.status != OperationAwaitingConfirmation {
		return operationView(operation), nil
	}
	operation.request.PolicyDecision = decisionPointer(decision)
	operation.updatedAt = service.now().UnixMilli()
	switch decision.Result {
	case policy.Deny:
		operation.status = OperationRejected
		operation.rejection = HostAcknowledgement{
			OperationID: operationID,
			Accepted:    false,
			Code:        decision.ReasonCode,
			Message:     decision.HumanSummary,
		}
	case policy.RequireConfirmation:
		operation.status = OperationAwaitingConfirmation
	default:
		return OperationView{}, ErrConflict
	}
	service.recordOperationTimelineLocked(operation)
	service.markOperationsDirtyLocked()
	service.notifyLocked()
	if err := service.persistOperationsLocked(); err != nil {
		return OperationView{}, err
	}
	return operationView(operation), nil
}

func (service *Service) prepareActionSubmissionLocked(
	principal host.Principal,
	input SubmitActionInput,
) (actionSubmissionSnapshot, error) {
	actor, err := service.authorizeActorLocked(
		principal,
		input.HostID,
		input.WorldID,
		input.Request.ActorID,
		ScopeActorExecute,
	)
	if err != nil {
		return actionSubmissionSnapshot{}, err
	}
	if actor.Epoch != input.Request.ExpectedEpoch ||
		actor.ObservationSeq != input.Request.ObservationSeq {
		return actionSubmissionSnapshot{}, ErrStale
	}
	key := actorControlKey{
		hostID: input.HostID, worldID: input.WorldID, actorID: input.Request.ActorID,
	}
	service.expireControllerLocked(key, service.now().UnixMilli())
	controller, exists := service.controllers[key]
	if !exists {
		return actionSubmissionSnapshot{}, ErrLeaseExpired
	}
	if controller.ControllerID != input.Request.ControllerID ||
		controller.PrincipalID != principal.ID ||
		!controllerMatchesActor(controller, actor) {
		return actionSubmissionSnapshot{}, ErrForbidden
	}
	if err := service.validateActionParentLocked(
		input.ParentOperationID,
		principal,
		input.HostID,
		input.WorldID,
		actor,
		controller.LeaseID,
		input.Request.TaskID,
	); err != nil {
		return actionSubmissionSnapshot{}, err
	}
	stop := service.emergencyStops[key]
	return actionSubmissionSnapshot{
		actor:             actor,
		controller:        controller,
		emergencyStopped:  stop.Active,
		emergencyRevision: stop.Revision,
	}, nil
}

func (service *Service) validateActionParentLocked(
	parentID string,
	principal host.Principal,
	hostID, worldID string,
	actor ActorPublication,
	controllerLeaseID, taskID string,
) error {
	if parentID == "" {
		return nil
	}
	parent, exists := service.operations[parentID]
	if !exists {
		return ErrNotFound
	}
	if parent.request.Kind != ControlAction || completeOperation(parent) ||
		parent.request.Principal.ID != principal.ID ||
		parent.request.HostID != hostID || parent.request.WorldID != worldID ||
		parent.request.ActorID != actor.ActorID ||
		controllerLeaseID != controllerLeaseIDFromRequest(parent.request) {
		return ErrConflict
	}
	if parent.status != OperationAccepted && parent.status != OperationRunning {
		return ErrConflict
	}
	if parent.request.ActionRequest == nil || parent.request.BoundAction == nil ||
		taskID == "" || parent.request.ActionRequest.TaskID != taskID ||
		parent.request.BoundAction.TaskID != taskID {
		return ErrConflict
	}
	if !actorPublishesChildProducingMacro(actor, *parent.request.BoundAction) {
		return ErrConflict
	}
	if len(parent.children) >= maxChildOperations {
		return ErrCapacity
	}
	return nil
}

func actorPublishesChildProducingMacro(
	actor ActorPublication,
	parent host.BoundAction,
) bool {
	if actor.Capabilities == nil {
		return false
	}
	for _, spec := range actor.Capabilities.Specs {
		if spec.Capability == parent.Capability && spec.Digest == parent.SpecDigest {
			return spec.Kind == host.CapabilityMacro && spec.ProducesChildOperations
		}
	}
	return false
}

func (service *Service) storeActionOperationLocked(
	key string,
	request HostControlRequest,
	result policy.Result,
) (OperationView, error) {
	status := OperationQueued
	switch result {
	case policy.Allow:
	case policy.RequireConfirmation:
		status = OperationAwaitingConfirmation
	case policy.Deny:
		status = OperationRejected
	default:
		return OperationView{}, ErrInvalid
	}
	operation := &operationState{
		request:     cloneControlRequest(request),
		status:      status,
		idempotency: key,
		createdAt:   request.SubmittedAt,
		updatedAt:   request.SubmittedAt,
	}
	if result == policy.Deny && request.PolicyDecision != nil {
		operation.rejection = HostAcknowledgement{
			OperationID: request.OperationID,
			Accepted:    false,
			Code:        request.PolicyDecision.ReasonCode,
			Message:     request.PolicyDecision.HumanSummary,
		}
	}
	var parent *operationState
	var parentUpdatedAt int64
	if request.ParentOperationID != "" {
		parent = service.operations[request.ParentOperationID]
		if parent == nil || len(parent.children) >= maxChildOperations {
			service.finalizeOperationPolicyLocked(operation, false)
			return OperationView{}, ErrConflict
		}
		parentUpdatedAt = parent.updatedAt
		parent.children = append(parent.children, request.OperationID)
		parent.updatedAt = request.SubmittedAt
	}
	service.operations[request.OperationID] = operation
	service.requests[key] = request.OperationID
	service.recordOperationTimelineLocked(operation)
	service.markOperationsDirtyLocked()
	if err := service.persistOperationsWithLimitLocked(maxQueuedStateBytes); err != nil {
		delete(service.operations, request.OperationID)
		delete(service.requests, key)
		if parent != nil {
			parent.children = parent.children[:len(parent.children)-1]
			parent.updatedAt = parentUpdatedAt
		}
		service.finalizeOperationPolicyLocked(operation, false)
		service.operationDirty = true
		return OperationView{}, errors.Join(err, service.persistOperationsLocked())
	}
	service.notifyLocked()
	return operationView(operation), nil
}

func (service *Service) idempotentActionLocked(
	key, fingerprint string,
) (OperationView, bool, error) {
	operationID, exists := service.requests[key]
	if !exists {
		return OperationView{}, false, nil
	}
	operation := service.operations[operationID]
	if operation == nil || operation.request.Kind != ControlAction ||
		actionRequestFingerprint(operation.request) != fingerprint {
		return OperationView{}, true, fmt.Errorf(
			"%w: idempotency_key was reused with different input",
			ErrConflict,
		)
	}
	service.refreshOperationHostLocked(operation)
	if err := service.persistOperationsLocked(); err != nil {
		return OperationView{}, true, err
	}
	return operationView(operation), true, nil
}

func (service *Service) stalePendingConfirmation(
	operationID string,
) (OperationView, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	operation := service.operations[operationID]
	if operation == nil {
		return OperationView{}, ErrNotFound
	}
	if operation.status == OperationAwaitingConfirmation {
		operation.status = OperationStale
		operation.updatedAt = service.now().UnixMilli()
		service.recordOperationTimelineLocked(operation)
		service.markOperationsDirtyLocked()
		service.notifyLocked()
		if err := service.persistOperationsLocked(); err != nil {
			return OperationView{}, err
		}
	}
	return operationView(operation), ErrStale
}

func (service *Service) finishActionFlight(key string, flight *actionFlight) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.actionFlights[key] != flight {
		return
	}
	delete(service.actionFlights, key)
	close(flight.done)
}

func (service *Service) finishConfirmation(operationID string) {
	service.mu.Lock()
	operation := service.operations[operationID]
	delete(service.confirming, operationID)
	if operation != nil && operation.status != OperationAwaitingConfirmation {
		service.discardOperationConfirmationLocked(operation)
	}
	service.mu.Unlock()
}

func (service *Service) finalizeOperationPolicyLocked(
	operation *operationState,
	committed bool,
) {
	if service.policyEngine == nil || operation == nil ||
		operation.request.PolicyDecision == nil {
		return
	}
	decision := operation.request.PolicyDecision
	if decision.Result == policy.Allow {
		service.policyEngine.Finalize(decision.DecisionID, committed)
	} else if !committed {
		service.discardOperationConfirmationLocked(operation)
	}
}

func (service *Service) discardOperationConfirmationLocked(operation *operationState) {
	if service.policyEngine == nil || operation == nil ||
		operation.request.PolicyDecision == nil ||
		operation.request.PolicyDecision.Result != policy.RequireConfirmation ||
		operation.request.PolicyDecision.Confirmation == nil {
		return
	}
	if _, confirming := service.confirming[operation.request.OperationID]; confirming {
		return
	}
	service.policyEngine.DiscardConfirmation(
		*operation.request.PolicyDecision.Confirmation,
	)
}

// ValidateActionDelivery verifies that a Host queue item contains one exact,
// allowed V2 ActionRequest/BoundAction/PolicyDecision tuple. The Host must still
// call Registry.AuthorizeBoundAction immediately before authority-thread
// execution.
func ValidateActionDelivery(request HostControlRequest) error {
	if request.Kind != ControlAction {
		return errors.New("control request is not a V2 action")
	}
	if err := validateStoredRequest(request); err != nil {
		return err
	}
	if request.PolicyDecision == nil || request.PolicyDecision.Result != policy.Allow {
		return errors.New("V2 action delivery requires an allow policy decision")
	}
	return nil
}

func validateSubmitActionInput(input SubmitActionInput) error {
	if err := validateActorControlTarget(ActorControlTarget{
		HostID: input.HostID, WorldID: input.WorldID, ActorID: input.Request.ActorID,
	}); err != nil {
		return err
	}
	if err := host.ValidateActionRequest(input.Request); err != nil {
		return invalid("action_request", err.Error())
	}
	if input.Request.ExpectedEpoch.WorldID != input.WorldID {
		return invalid("action_request.expected_epoch.world_id", "must equal world_id")
	}
	if input.ParentOperationID != "" {
		return validateID("parent_operation_id", input.ParentOperationID)
	}
	return nil
}

func validateActionBindingResult(
	result ActionBindingResult,
	request host.ActionRequest,
	requestDigest string,
	actor ActorPublication,
) error {
	if err := validateActionHostSnapshot(result.Snapshot); err != nil {
		return err
	}
	if err := host.ValidateBoundAction(result.Action); err != nil {
		return err
	}
	if result.Snapshot.Epoch != actor.Epoch ||
		result.Snapshot.ObservationSeq != actor.ObservationSeq {
		return errors.New("Host snapshot does not match the published Actor")
	}
	action := result.Action
	if action.RequestDigest != requestDigest || action.RequestID != request.RequestID ||
		action.ControllerID != request.ControllerID || action.ActorID != request.ActorID ||
		action.Capability != request.Capability || action.SpecDigest != request.SpecDigest ||
		action.ExpectedEpoch != request.ExpectedEpoch ||
		action.ObservationSeq != request.ObservationSeq ||
		action.TaskID != request.TaskID || action.IdempotencyKey != request.IdempotencyKey ||
		!slices.Equal(action.RequestedTargets, request.Targets) {
		return errors.New("BoundAction does not match ActionRequest")
	}
	if action.BoundAt != result.Snapshot.Now {
		return errors.New("BoundAction bound_at does not match Host snapshot")
	}
	return nil
}

func validateActionHostSnapshot(snapshot ActionHostSnapshot) error {
	if err := snapshot.Now.Validate("snapshot.now"); err != nil {
		return err
	}
	if err := snapshot.Epoch.Validate("snapshot.epoch"); err != nil {
		return err
	}
	if snapshot.ObservationSeq == 0 || snapshot.ObservationSeq > maxJSONSafeInteger {
		return errors.New("snapshot observation_sequence is invalid")
	}
	return nil
}

func sameActionSubmissionSnapshot(left, right actionSubmissionSnapshot) bool {
	return left.actor.Epoch == right.actor.Epoch &&
		left.actor.ObservationSeq == right.actor.ObservationSeq &&
		left.actor.OwnerPrincipalID == right.actor.OwnerPrincipalID &&
		left.controller == right.controller &&
		left.emergencyStopped == right.emergencyStopped &&
		left.emergencyRevision == right.emergencyRevision
}

func actionSubmissionFingerprint(
	requestDigest, parentOperationID string,
	principal host.Principal,
) string {
	return strings.Join([]string{
		requestDigest,
		parentOperationID,
		principal.ID,
		strings.Join(principal.GrantedScopes, ","),
	}, "\x00")
}

func actionRequestFingerprint(request HostControlRequest) string {
	if request.ActionRequest == nil {
		return ""
	}
	digest, err := host.ActionRequestDigest(*request.ActionRequest)
	if err != nil {
		return ""
	}
	return actionSubmissionFingerprint(
		digest,
		request.ParentOperationID,
		request.Principal,
	)
}

func actionOperationRequestKey(principalID, idempotencyKey string) string {
	return principalID + "\x00action\x00" + idempotencyKey
}

func operationIdempotencyKey(request HostControlRequest) string {
	if request.ActionRequest == nil {
		return ""
	}
	return actionOperationRequestKey(
		request.Principal.ID,
		request.ActionRequest.IdempotencyKey,
	)
}

func controllerLeaseIDFromRequest(request HostControlRequest) string {
	return controllerLeaseID(request.Binding)
}

func cloneActionRequest(value host.ActionRequest) host.ActionRequest {
	value.Arguments = append(json.RawMessage(nil), value.Arguments...)
	value.Targets = append([]host.HostRef(nil), value.Targets...)
	return value
}

func cloneBoundAction(value host.BoundAction) host.BoundAction {
	value.NormalizedArguments = append(json.RawMessage(nil), value.NormalizedArguments...)
	value.RequestedTargets = append([]host.HostRef(nil), value.RequestedTargets...)
	value.ResolvedTargets = append([]host.HostRef(nil), value.ResolvedTargets...)
	effects := value.Effects
	value.Effects = make([]host.Effect, len(effects))
	for index, effect := range effects {
		cloned := effect
		cloned.Tags = append([]string(nil), effect.Tags...)
		cloned.Attributes = append(json.RawMessage(nil), effect.Attributes...)
		if effect.Subject != nil {
			subject := *effect.Subject
			cloned.Subject = &subject
		}
		if effect.Target != nil {
			target := *effect.Target
			cloned.Target = &target
		}
		value.Effects[index] = cloned
	}
	return value
}

func actionRequestPointer(value host.ActionRequest) *host.ActionRequest {
	cloned := cloneActionRequest(value)
	return &cloned
}

func boundActionPointer(value host.BoundAction) *host.BoundAction {
	cloned := cloneBoundAction(value)
	return &cloned
}

func decisionPointer(value policy.Decision) *policy.Decision {
	cloned := policy.CloneDecision(value)
	return &cloned
}
