package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/sunrioa/rin/host"
)

const (
	defaultMaxOperations = 1_024
	hardMaxOperations    = 65_536
	defaultOperationTTL  = 30 * time.Minute
	maxControlTextBytes  = 4 << 10
	maxUtteranceRunes    = 300
	maxHostPollItems     = 64
)

type operationState struct {
	request     HostControlRequest
	status      OperationStatus
	attempts    uint32
	cancel      bool
	ack         *HostAcknowledgement
	run         *host.ActionRun
	outcome     *host.ActionOutcome
	output      json.RawMessage
	rejection   HostAcknowledgement
	idempotency string
	createdAt   int64
	updatedAt   int64
}

// SendActorMessage queues plain conversation without directly authorizing a
// world mutation.
func (service *Service) SendActorMessage(
	principal host.Principal,
	input ActorTextInput,
) (OperationView, error) {
	return service.submitText(principal, input, ControlMessage)
}

// SendActorDirective queues a negotiable goal that the Actor may refuse.
func (service *Service) SendActorDirective(
	principal host.Principal,
	input ActorTextInput,
) (OperationView, error) {
	return service.submitText(principal, input, ControlDirective)
}

// SubmitActorUtterance queues player-visible dialogue authored by the current
// externally bound controller.
func (service *Service) SubmitActorUtterance(
	principal host.Principal,
	input ActorUtteranceInput,
) (OperationView, error) {
	if err := validateActorUtteranceInput(input); err != nil {
		return OperationView{}, err
	}
	if err := host.ValidatePrincipal(principal); err != nil {
		return OperationView{}, fmt.Errorf("%w: principal: %v", ErrInvalid, err)
	}

	service.mu.Lock()
	defer service.mu.Unlock()
	key := operationRequestKey(principal.ID, input.RequestID)
	if existing, found, err := service.idempotentOperationLocked(
		key,
		principal,
		input.HostID,
		input.WorldID,
		input.ActorID,
		ControlUtterance,
		input.Text,
		"",
		input.TurnID,
	); found || err != nil {
		return existing, err
	}
	actor, err := service.authorizeActorLocked(
		principal,
		input.HostID,
		input.WorldID,
		input.ActorID,
		ScopeActorSpeak,
	)
	if err != nil {
		if persistErr := service.persistOperationsLocked(); persistErr != nil {
			return OperationView{}, persistErr
		}
		return OperationView{}, err
	}
	if !authorityAllowsExternal(actor, principal.ID) {
		return OperationView{}, ErrForbidden
	}
	operationID, err := service.prepareOperationLocked()
	if err != nil {
		return OperationView{}, err
	}
	request := HostControlRequest{
		OperationID: operationID,
		RequestID:   input.RequestID,
		Principal:   clonePrincipalValue(principal),
		HostID:      input.HostID,
		WorldID:     input.WorldID,
		ActorID:     input.ActorID,
		Kind:        ControlUtterance,
		TurnID:      input.TurnID,
		Text:        input.Text,
		Binding:     bindingFromActor(actor),
		SubmittedAt: service.now().UnixMilli(),
	}
	return service.queueOperationLocked(key, request)
}

// ExecuteActorOffer queues one exact Host-published Offer.
func (service *Service) ExecuteActorOffer(
	principal host.Principal,
	input ExecuteOfferInput,
) (OperationView, error) {
	if err := validateExecuteOfferInput(input); err != nil {
		return OperationView{}, err
	}
	if err := host.ValidatePrincipal(principal); err != nil {
		return OperationView{}, fmt.Errorf("%w: principal: %v", ErrInvalid, err)
	}

	service.mu.Lock()
	defer service.mu.Unlock()
	key := operationRequestKey(principal.ID, input.RequestID)
	if existing, found, err := service.idempotentOperationLocked(
		key,
		principal,
		input.HostID,
		input.WorldID,
		input.ActorID,
		ControlOffer,
		"",
		input.OfferID,
		input.TurnID,
	); found || err != nil {
		return existing, err
	}
	actor, err := service.authorizeActorLocked(
		principal,
		input.HostID,
		input.WorldID,
		input.ActorID,
		ScopeActorExecute,
	)
	if err != nil {
		if persistErr := service.persistOperationsLocked(); persistErr != nil {
			return OperationView{}, persistErr
		}
		return OperationView{}, err
	}
	if !authorityAllowsExternal(actor, principal.ID) {
		return OperationView{}, ErrForbidden
	}
	var selected *host.ActionOffer
	for index := range actor.Offers {
		if actor.Offers[index].OfferID == input.OfferID {
			selected = &actor.Offers[index]
			break
		}
	}
	if selected == nil {
		return OperationView{}, ErrNotFound
	}
	operationID, err := service.prepareOperationLocked()
	if err != nil {
		return OperationView{}, err
	}
	offer := cloneOffer(*selected)
	request := HostControlRequest{
		OperationID: operationID,
		RequestID:   input.RequestID,
		Principal:   clonePrincipalValue(principal),
		HostID:      input.HostID,
		WorldID:     input.WorldID,
		ActorID:     input.ActorID,
		Kind:        ControlOffer,
		TurnID:      input.TurnID,
		Binding:     bindingFromActor(actor),
		Offer:       &offer,
		SubmittedAt: service.now().UnixMilli(),
	}
	return service.queueOperationLocked(key, request)
}

func (service *Service) submitText(
	principal host.Principal,
	input ActorTextInput,
	kind ControlKind,
) (OperationView, error) {
	if err := validateActorTextInput(input); err != nil {
		return OperationView{}, err
	}
	if err := host.ValidatePrincipal(principal); err != nil {
		return OperationView{}, fmt.Errorf("%w: principal: %v", ErrInvalid, err)
	}
	requiredScope := ScopeActorConverse
	if kind == ControlDirective {
		requiredScope = ScopeActorDirect
	}

	service.mu.Lock()
	defer service.mu.Unlock()
	key := operationRequestKey(principal.ID, input.RequestID)
	if existing, found, err := service.idempotentOperationLocked(
		key,
		principal,
		input.HostID,
		input.WorldID,
		input.ActorID,
		kind,
		input.Text,
		"",
		"",
	); found || err != nil {
		return existing, err
	}
	actor, err := service.authorizeActorLocked(
		principal,
		input.HostID,
		input.WorldID,
		input.ActorID,
		requiredScope,
	)
	if err != nil {
		if persistErr := service.persistOperationsLocked(); persistErr != nil {
			return OperationView{}, persistErr
		}
		return OperationView{}, err
	}
	operationID, err := service.prepareOperationLocked()
	if err != nil {
		return OperationView{}, err
	}
	request := HostControlRequest{
		OperationID: operationID,
		RequestID:   input.RequestID,
		Principal:   clonePrincipalValue(principal),
		HostID:      input.HostID,
		WorldID:     input.WorldID,
		ActorID:     input.ActorID,
		Kind:        kind,
		Text:        input.Text,
		Binding:     bindingFromActor(actor),
		SubmittedAt: service.now().UnixMilli(),
	}
	return service.queueOperationLocked(key, request)
}

// PollHost waits for bounded new work or cancellation requests. Redelivery is
// intentional until the Host acknowledges a stable Operation ID.
func (service *Service) PollHost(
	ctx context.Context,
	hostID, leaseID string,
	limit int,
) (HostControlBatch, error) {
	if limit <= 0 || limit > maxHostPollItems {
		return HostControlBatch{}, invalid("limit", "must be between 1 and 64")
	}
	for {
		service.mu.Lock()
		current, err := service.requireLeaseLocked(hostID, leaseID)
		if err != nil {
			service.mu.Unlock()
			return HostControlBatch{}, err
		}
		if err := service.persistOperationsLocked(); err != nil {
			service.mu.Unlock()
			return HostControlBatch{}, err
		}
		batch := service.collectHostWorkLocked(hostID, limit)
		if len(batch.Requests) != 0 || len(batch.Cancellations) != 0 {
			if err := service.persistOperationsLocked(); err != nil {
				service.mu.Unlock()
				return HostControlBatch{}, err
			}
			service.mu.Unlock()
			return batch, nil
		}
		changed := service.changed
		leaseRemaining := time.Duration(
			current.lease.ExpiresAtUnixMillis-service.now().UnixMilli(),
		) * time.Millisecond
		service.mu.Unlock()

		timer := time.NewTimer(leaseRemaining)
		select {
		case <-ctx.Done():
			stopTimer(timer)
			return HostControlBatch{}, ctx.Err()
		case <-changed:
			stopTimer(timer)
		case <-timer.C:
		}
	}
}

// AcknowledgeHost records whether the Host accepted a delivered request.
func (service *Service) AcknowledgeHost(
	hostID, leaseID string,
	acknowledgement HostAcknowledgement,
) error {
	if err := validateAcknowledgement(acknowledgement); err != nil {
		return err
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if err := service.persistOperationsLocked(); err != nil {
		return err
	}
	if _, err := service.requireLeaseLocked(hostID, leaseID); err != nil {
		return err
	}
	operation, err := service.hostOperationLocked(
		hostID,
		acknowledgement.OperationID,
	)
	if err != nil {
		return err
	}
	if operation.ack != nil {
		if reflect.DeepEqual(*operation.ack, acknowledgement) {
			return service.persistOperationsLocked()
		}
		return fmt.Errorf("%w: acknowledgement changed", ErrConflict)
	}
	if operation.status != OperationDelivered {
		return fmt.Errorf("%w: operation was not delivered", ErrConflict)
	}
	cloned := acknowledgement
	operation.ack = &cloned
	operation.updatedAt = service.now().UnixMilli()
	if acknowledgement.Accepted {
		operation.status = OperationAccepted
	} else {
		operation.status = OperationRejected
		operation.rejection = acknowledgement
	}
	service.markOperationsDirtyLocked()
	return service.persistOperationsLocked()
}

// ReportHostRun records monotonic Host progress for an accepted request.
func (service *Service) ReportHostRun(
	hostID, leaseID string,
	run host.ActionRun,
) error {
	if err := host.ValidateActionRun(run); err != nil {
		return fmt.Errorf("%w: run: %v", ErrInvalid, err)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if err := service.persistOperationsLocked(); err != nil {
		return err
	}
	if _, err := service.requireLeaseLocked(hostID, leaseID); err != nil {
		return err
	}
	operation, err := service.hostOperationLocked(hostID, run.OperationID)
	if err != nil {
		return err
	}
	if operation.ack == nil || !operation.ack.Accepted {
		return fmt.Errorf("%w: operation was not accepted", ErrConflict)
	}
	if operation.run != nil {
		if run.ProgressSeq < operation.run.ProgressSeq {
			return fmt.Errorf("%w: progress sequence moved backwards", ErrStale)
		}
		if run.ProgressSeq == operation.run.ProgressSeq {
			if reflect.DeepEqual(*operation.run, run) {
				return service.persistOperationsLocked()
			}
			return fmt.Errorf("%w: progress changed without a new sequence", ErrStale)
		}
		if run.Progress < operation.run.Progress {
			return fmt.Errorf("%w: action progress moved backwards", ErrStale)
		}
		if run.UpdatedAt.Clock != operation.run.UpdatedAt.Clock ||
			run.UpdatedAt.Value < operation.run.UpdatedAt.Value {
			return fmt.Errorf("%w: action timepoint moved backwards", ErrStale)
		}
		if run.Status != operation.run.Status &&
			!host.CanTransitionActionRun(operation.run.Status, run.Status) {
			return fmt.Errorf(
				"%w: action run cannot transition from %s to %s",
				ErrConflict,
				operation.run.Status,
				run.Status,
			)
		}
	} else if !validInitialRunStatus(run.Status) {
		return fmt.Errorf("%w: initial action run status %s", ErrConflict, run.Status)
	}
	cloned := cloneRun(run)
	operation.run = &cloned
	operation.status = operationStatusFromRun(run.Status)
	operation.updatedAt = service.now().UnixMilli()
	service.markOperationCheckpointDirtyLocked()
	return service.persistOperationsLocked()
}

// ReportHostOutcome persists one authoritative terminal effect.
func (service *Service) ReportHostOutcome(
	hostID, leaseID string,
	outcome host.ActionOutcome,
) error {
	return service.ReportHostResult(hostID, leaseID, outcome, nil)
}

// ReportHostResult persists one authoritative terminal effect and an optional
// bounded structured output supplied by the Host.
func (service *Service) ReportHostResult(
	hostID, leaseID string,
	outcome host.ActionOutcome,
	output json.RawMessage,
) error {
	if err := host.ValidateActionOutcome(outcome); err != nil {
		return fmt.Errorf("%w: outcome: %v", ErrInvalid, err)
	}
	if err := validateOperationOutput(output); err != nil {
		return err
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if err := service.persistOperationsLocked(); err != nil {
		return err
	}
	if _, err := service.requireLeaseLocked(hostID, leaseID); err != nil {
		return err
	}
	operation, err := service.hostOperationLocked(hostID, outcome.OperationID)
	if err != nil {
		return err
	}
	if operation.outcome != nil {
		if reflect.DeepEqual(*operation.outcome, outcome) &&
			bytes.Equal(operation.output, output) {
			return service.persistOperationsLocked()
		}
		return fmt.Errorf("%w: terminal result changed", ErrConflict)
	}
	if operation.ack == nil || !operation.ack.Accepted {
		return fmt.Errorf("%w: operation was not accepted", ErrConflict)
	}
	expected := operationStatusFromRun(outcome.Status)
	if terminalOperationStatus(operation.status) && operation.status != expected {
		return fmt.Errorf("%w: outcome conflicts with terminal run", ErrConflict)
	}
	if operation.request.Kind == ControlOffer &&
		outcome.Epoch.WorldID != operation.request.WorldID {
		return fmt.Errorf("%w: outcome epoch belongs to another world", ErrInvalid)
	}
	cloned := cloneOutcome(outcome)
	operation.outcome = &cloned
	operation.output = append(json.RawMessage(nil), output...)
	operation.status = expected
	operation.updatedAt = service.now().UnixMilli()
	service.markOperationsDirtyLocked()
	return service.persistOperationsLocked()
}

// GetOperation returns one operation to its submitting principal.
func (service *Service) GetOperation(
	principal host.Principal,
	operationID string,
) (OperationView, error) {
	if err := host.ValidatePrincipal(principal); err != nil {
		return OperationView{}, fmt.Errorf("%w: principal: %v", ErrInvalid, err)
	}
	if err := validateID("operation_id", operationID); err != nil {
		return OperationView{}, err
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.getOperationLocked(principal, operationID)
}

// WaitOperation waits for a newer operation cursor or a settled terminal
// state. It does not interpret queueing, delivery, or progress as execution.
func (service *Service) WaitOperation(
	ctx context.Context,
	principal host.Principal,
	input WaitOperationInput,
) (OperationUpdate, error) {
	if err := host.ValidatePrincipal(principal); err != nil {
		return OperationUpdate{}, fmt.Errorf(
			"%w: principal: %v", ErrInvalid, err,
		)
	}
	if err := validateID("operation_id", input.OperationID); err != nil {
		return OperationUpdate{}, err
	}
	if len(input.AfterCursor) > 256 || !utf8.ValidString(input.AfterCursor) {
		return OperationUpdate{}, invalid(
			"after_cursor", "must be valid UTF-8 of at most 256 bytes",
		)
	}
	if input.WaitMillis > 25_000 {
		return OperationUpdate{}, invalid(
			"wait_millis", "must not exceed 25000",
		)
	}
	timer := time.NewTimer(time.Duration(input.WaitMillis) * time.Millisecond)
	defer timer.Stop()
	for {
		service.mu.Lock()
		if service.closed {
			service.mu.Unlock()
			return OperationUpdate{}, ErrUnavailable
		}
		view, err := service.getOperationLocked(principal, input.OperationID)
		changed := service.changed
		service.mu.Unlock()
		if err != nil {
			return OperationUpdate{}, err
		}
		cursorChanged := view.Cursor != input.AfterCursor
		if view.Terminal || cursorChanged || input.WaitMillis == 0 {
			return OperationUpdate{
				Operation: view,
				Changed:   cursorChanged,
			}, nil
		}
		select {
		case <-ctx.Done():
			return OperationUpdate{}, ctx.Err()
		case <-timer.C:
			service.mu.Lock()
			view, err = service.getOperationLocked(
				principal, input.OperationID,
			)
			service.mu.Unlock()
			if err != nil {
				return OperationUpdate{}, err
			}
			return OperationUpdate{
				Operation: view,
				Changed:   view.Cursor != input.AfterCursor,
			}, nil
		case <-changed:
		}
	}
}

func (service *Service) getOperationLocked(
	principal host.Principal,
	operationID string,
) (OperationView, error) {
	operation, exists := service.operations[operationID]
	if !exists {
		return OperationView{}, ErrNotFound
	}
	service.refreshOperationHostLocked(operation)
	if err := service.persistOperationsLocked(); err != nil {
		return OperationView{}, err
	}
	if !hasScope(principal, ScopeHostAdmin) &&
		principal.ID != operation.request.Principal.ID {
		return OperationView{}, ErrForbidden
	}
	return operationView(operation), nil
}

// CancelOperation requests cancellation. Work that has never been delivered is
// cancelled locally; delivered work remains pending until the Host reconciles.
func (service *Service) CancelOperation(
	principal host.Principal,
	operationID string,
) (OperationView, error) {
	if err := host.ValidatePrincipal(principal); err != nil {
		return OperationView{}, fmt.Errorf("%w: principal: %v", ErrInvalid, err)
	}
	if err := validateID("operation_id", operationID); err != nil {
		return OperationView{}, err
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	operation, exists := service.operations[operationID]
	if !exists {
		return OperationView{}, ErrNotFound
	}
	service.refreshOperationHostLocked(operation)
	if err := service.persistOperationsLocked(); err != nil {
		return OperationView{}, err
	}
	if !hasScope(principal, ScopeHostAdmin) {
		if principal.ID != operation.request.Principal.ID ||
			!hasScope(principal, ScopeOperationCancel) {
			return OperationView{}, ErrForbidden
		}
	}
	if completeOperation(operation) {
		return operationView(operation), nil
	}
	if operation.status == OperationQueued {
		operation.status = OperationCancelled
		operation.updatedAt = service.now().UnixMilli()
		service.markOperationsDirtyLocked()
		if err := service.persistOperationsLocked(); err != nil {
			return OperationView{}, err
		}
		return operationView(operation), nil
	}
	if !operation.cancel {
		operation.cancel = true
		operation.updatedAt = service.now().UnixMilli()
		service.markOperationsDirtyLocked()
	}
	if err := service.persistOperationsLocked(); err != nil {
		return OperationView{}, err
	}
	return operationView(operation), nil
}

func (service *Service) idempotentOperationLocked(
	key string,
	principal host.Principal,
	hostID, worldID, actorID string,
	kind ControlKind,
	text, offerID, turnID string,
) (OperationView, bool, error) {
	operationID, exists := service.requests[key]
	if !exists {
		return OperationView{}, false, nil
	}
	operation := service.operations[operationID]
	canonicalPrincipal := clonePrincipalValue(principal)
	same := operation != nil &&
		operation.request.Principal.ID == principal.ID &&
		slices.Equal(
			operation.request.Principal.GrantedScopes,
			canonicalPrincipal.GrantedScopes,
		) &&
		operation.request.HostID == hostID &&
		operation.request.WorldID == worldID &&
		operation.request.ActorID == actorID &&
		operation.request.Kind == kind &&
		operation.request.Text == text &&
		operation.request.TurnID == turnID
	if same && kind == ControlOffer {
		same = operation.request.Offer != nil &&
			operation.request.Offer.OfferID == offerID
	}
	if !same {
		return OperationView{}, true,
			fmt.Errorf("%w: request_id was reused with different input", ErrConflict)
	}
	service.refreshOperationHostLocked(operation)
	if err := service.persistOperationsLocked(); err != nil {
		return OperationView{}, true, err
	}
	return operationView(operation), true, nil
}

func (service *Service) authorizeActorLocked(
	principal host.Principal,
	hostID, worldID, actorID, requiredScope string,
) (ActorPublication, error) {
	current, world, err := service.findWorldLocked(hostID, worldID)
	if err != nil {
		return ActorPublication{}, err
	}
	if current.lease.ExpiresAtUnixMillis <= service.now().UnixMilli() {
		service.expireHostOperationsLocked(hostID, service.now().UnixMilli())
		return ActorPublication{}, ErrUnavailable
	}
	for _, actor := range world.Actors {
		if actor.ActorID != actorID {
			continue
		}
		if !canAccessActor(principal, actor, requiredScope) {
			return ActorPublication{}, ErrForbidden
		}
		return actor, nil
	}
	return ActorPublication{}, ErrNotFound
}

func (service *Service) prepareOperationLocked() (string, error) {
	now := service.now().UnixMilli()
	service.pruneOperationsLocked(now)
	if err := service.persistOperationsLocked(); err != nil {
		return "", err
	}
	if len(service.operations) >= service.maxOperations {
		return "", ErrCapacity
	}
	return service.newID("operation")
}

func (service *Service) queueOperationLocked(
	key string,
	request HostControlRequest,
) (OperationView, error) {
	operation := &operationState{
		request:     cloneControlRequest(request),
		status:      OperationQueued,
		idempotency: key,
		createdAt:   request.SubmittedAt,
		updatedAt:   request.SubmittedAt,
	}
	service.operations[request.OperationID] = operation
	service.requests[key] = request.OperationID
	service.markOperationsDirtyLocked()
	if err := service.persistOperationsWithLimitLocked(
		maxQueuedStateBytes,
	); err != nil {
		if errors.Is(err, ErrCapacity) {
			delete(service.operations, request.OperationID)
			delete(service.requests, key)
			service.operationDirty = true
			return OperationView{}, errors.Join(
				err,
				service.persistOperationsLocked(),
			)
		}
		return OperationView{}, err
	}
	return operationView(operation), nil
}

func (service *Service) collectHostWorkLocked(
	hostID string,
	limit int,
) HostControlBatch {
	operations := make([]*operationState, 0)
	for _, operation := range service.operations {
		if operation.request.HostID == hostID {
			operations = append(operations, operation)
		}
	}
	slices.SortFunc(operations, func(left, right *operationState) int {
		if left.createdAt < right.createdAt {
			return -1
		}
		if left.createdAt > right.createdAt {
			return 1
		}
		return compare(left.request.OperationID, right.request.OperationID)
	})

	batch := HostControlBatch{
		Requests:      make([]HostControlDelivery, 0),
		Cancellations: make([]string, 0),
	}
	now := service.now().UnixMilli()
	deliveryChanged := false
	for _, operation := range operations {
		if len(batch.Cancellations) >= limit {
			break
		}
		if operation.cancel && !completeOperation(operation) {
			batch.Cancellations = append(
				batch.Cancellations,
				operation.request.OperationID,
			)
		}
	}
	for _, operation := range operations {
		if len(batch.Requests)+len(batch.Cancellations) >= limit {
			break
		}
		if operation.cancel {
			continue
		}
		if operation.status != OperationQueued &&
			operation.status != OperationDelivered &&
			operation.status != OperationAccepted {
			continue
		}
		if operation.attempts < math.MaxUint32 {
			operation.attempts++
		}
		operation.status = OperationDelivered
		operation.updatedAt = now
		deliveryChanged = true
		batch.Requests = append(batch.Requests, HostControlDelivery{
			Request:         cloneControlRequest(operation.request),
			DeliveryAttempt: operation.attempts,
		})
	}
	if deliveryChanged {
		service.markOperationCheckpointDirtyLocked()
	}
	return batch
}

func (service *Service) hostOperationLocked(
	hostID, operationID string,
) (*operationState, error) {
	operation, exists := service.operations[operationID]
	if !exists {
		return nil, ErrNotFound
	}
	if operation.request.HostID != hostID {
		return nil, ErrForbidden
	}
	return operation, nil
}

func (service *Service) refreshOperationHostLocked(operation *operationState) {
	now := service.now().UnixMilli()
	if service.expireOperationByTTLLocked(operation, now) {
		service.markOperationsDirtyLocked()
		return
	}
	current, exists := service.hosts[operation.request.HostID]
	if exists &&
		current.lease.ExpiresAtUnixMillis <= now {
		service.expireHostOperationsLocked(
			operation.request.HostID,
			now,
		)
	}
}

func (service *Service) expireHostOperationsLocked(hostID string, now int64) {
	changed := false
	for _, operation := range service.operations {
		if operation.request.HostID != hostID || completeOperation(operation) {
			continue
		}
		if operation.ack != nil && operation.ack.Accepted {
			if operation.status != OperationOutcomeUnknown {
				operation.status = OperationOutcomeUnknown
				operation.updatedAt = now
				changed = true
			}
		} else {
			if operation.status != OperationStale {
				operation.status = OperationStale
				operation.updatedAt = now
				changed = true
			}
		}
	}
	if changed {
		service.markOperationsDirtyLocked()
	}
}

func (service *Service) pruneOperationsLocked(now int64) {
	cutoff := now - service.operationTTL.Milliseconds()
	changed := false
	for _, operation := range service.operations {
		changed = service.expireOperationByTTLLocked(operation, now) || changed
	}
	for operationID, operation := range service.operations {
		if !completeOperation(operation) || operation.updatedAt > cutoff {
			continue
		}
		delete(service.operations, operationID)
		delete(service.requests, operation.idempotency)
		changed = true
	}
	if changed {
		service.markOperationsDirtyLocked()
	}
}

func (service *Service) expireOperationByTTLLocked(
	operation *operationState,
	now int64,
) bool {
	if completeOperation(operation) ||
		operation.updatedAt > now-service.operationTTL.Milliseconds() {
		return false
	}
	if operation.ack != nil && operation.ack.Accepted {
		operation.status = OperationOutcomeUnknown
	} else {
		operation.status = OperationStale
	}
	operation.updatedAt = now
	return true
}

func (service *Service) notifyLocked() {
	close(service.changed)
	service.changed = make(chan struct{})
}

func operationView(operation *operationState) OperationView {
	view := OperationView{
		OperationID:           operation.request.OperationID,
		RequestID:             operation.request.RequestID,
		HostID:                operation.request.HostID,
		WorldID:               operation.request.WorldID,
		ActorID:               operation.request.ActorID,
		Kind:                  operation.request.Kind,
		TurnID:                operation.request.TurnID,
		Status:                operation.status,
		Cursor:                operationCursor(operation),
		Terminal:              settledOperation(operation),
		ReconciliationPending: reconciliationPending(operation),
		ExecutionConfirmed: operation.status == OperationSucceeded &&
			operation.outcome != nil,
		CancelRequested:  operation.cancel,
		DeliveryAttempts: operation.attempts,
		RejectionCode:    operation.rejection.Code,
		RejectionMessage: operation.rejection.Message,
		CreatedAt:        operation.createdAt,
		UpdatedAt:        operation.updatedAt,
	}
	if operation.run != nil {
		cloned := cloneRun(*operation.run)
		view.Run = &cloned
	}
	if operation.outcome != nil {
		cloned := cloneOutcome(*operation.outcome)
		view.Outcome = &cloned
	}
	view.Output = operationOutputView(operation.output)
	return view
}

func operationCursor(operation *operationState) string {
	runStatus := host.ActionRunStatus("")
	progressSequence := uint64(0)
	if operation.run != nil {
		runStatus = operation.run.Status
		progressSequence = operation.run.ProgressSeq
	}
	return fmt.Sprintf(
		"op1:%s:%d:%t:%d:%s:%d:%t",
		operation.status,
		operation.attempts,
		operation.cancel,
		operation.updatedAt,
		runStatus,
		progressSequence,
		operation.outcome != nil,
	)
}

func settledOperation(operation *operationState) bool {
	return completeOperation(operation) && !reconciliationPending(operation)
}

func reconciliationPending(operation *operationState) bool {
	return operation.status == OperationOutcomeUnknown &&
		operation.outcome == nil
}

func operationOutputView(output json.RawMessage) map[string]any {
	if len(output) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		return nil
	}
	return value
}

func bindingFromActor(actor ActorPublication) *ControlBinding {
	return &ControlBinding{
		Epoch:             actor.Epoch,
		ObservationSeq:    actor.ObservationSeq,
		AuthorityRevision: effectiveAuthority(actor).Revision,
	}
}

func cloneControlRequest(value HostControlRequest) HostControlRequest {
	cloned := value
	cloned.Principal = clonePrincipalValue(value.Principal)
	if value.Binding != nil {
		binding := *value.Binding
		cloned.Binding = &binding
	}
	if value.Offer != nil {
		offer := cloneOffer(*value.Offer)
		cloned.Offer = &offer
	}
	if value.Invocation != nil {
		invocation := *value.Invocation
		invocation.Arguments = append([]byte(nil), value.Invocation.Arguments...)
		invocation.Targets = append([]host.HostRef(nil), value.Invocation.Targets...)
		cloned.Invocation = &invocation
	}
	return cloned
}

func cloneOffer(value host.ActionOffer) host.ActionOffer {
	value.Arguments = append(json.RawMessage(nil), value.Arguments...)
	value.Targets = append([]host.HostRef(nil), value.Targets...)
	return value
}

func clonePrincipalValue(value host.Principal) host.Principal {
	value.GrantedScopes = append([]string(nil), value.GrantedScopes...)
	slices.Sort(value.GrantedScopes)
	return value
}

func cloneRun(value host.ActionRun) host.ActionRun {
	return value
}

func cloneOutcome(value host.ActionOutcome) host.ActionOutcome {
	value.Evidence = append([]host.HostRef(nil), value.Evidence...)
	return value
}

func validateActorTextInput(input ActorTextInput) error {
	if err := validateControlTarget(
		input.RequestID,
		input.HostID,
		input.WorldID,
		input.ActorID,
	); err != nil {
		return err
	}
	return validateText("text", input.Text, maxControlTextBytes, true)
}

func validateActorUtteranceInput(input ActorUtteranceInput) error {
	if err := validateControlTarget(
		input.RequestID,
		input.HostID,
		input.WorldID,
		input.ActorID,
	); err != nil {
		return err
	}
	if err := validateID("turn_id", input.TurnID); err != nil {
		return err
	}
	if err := validateText(
		"text",
		input.Text,
		maxControlTextBytes,
		true,
	); err != nil {
		return err
	}
	if utf8.RuneCountInString(input.Text) > maxUtteranceRunes {
		return invalid("text", "must contain at most 300 Unicode code points")
	}
	return nil
}

func validateExecuteOfferInput(input ExecuteOfferInput) error {
	if err := validateControlTarget(
		input.RequestID,
		input.HostID,
		input.WorldID,
		input.ActorID,
	); err != nil {
		return err
	}
	if err := validateID("offer_id", input.OfferID); err != nil {
		return err
	}
	if input.TurnID != "" {
		return validateID("turn_id", input.TurnID)
	}
	return nil
}

func validateControlTarget(
	requestID, hostID, worldID, actorID string,
) error {
	for _, value := range []struct {
		field string
		value string
	}{
		{"request_id", requestID},
		{"host_id", hostID},
		{"world_id", worldID},
		{"actor_id", actorID},
	} {
		if err := validateID(value.field, value.value); err != nil {
			return err
		}
	}
	return nil
}

func validateAcknowledgement(value HostAcknowledgement) error {
	if err := validateID("operation_id", value.OperationID); err != nil {
		return err
	}
	if value.Accepted {
		if value.Code != "" {
			return invalid("code", "must be empty when accepted")
		}
	} else if err := validateID("code", value.Code); err != nil {
		return err
	}
	return validateText("message", value.Message, 500, false)
}

func operationRequestKey(principalID, requestID string) string {
	return principalID + "\x00" + requestID
}

func validInitialRunStatus(status host.ActionRunStatus) bool {
	return status == host.ActionQueued ||
		status == host.ActionRunning ||
		status == host.ActionSucceeded ||
		status == host.ActionFailed ||
		status == host.ActionCancelled ||
		status == host.ActionInterrupted ||
		status == host.ActionStale ||
		status == host.ActionOutcomeUnknown
}

func operationStatusFromRun(status host.ActionRunStatus) OperationStatus {
	switch status {
	case host.ActionQueued:
		return OperationAccepted
	case host.ActionRunning:
		return OperationRunning
	case host.ActionSucceeded:
		return OperationSucceeded
	case host.ActionFailed:
		return OperationFailed
	case host.ActionCancelled:
		return OperationCancelled
	case host.ActionInterrupted:
		return OperationInterrupted
	case host.ActionStale:
		return OperationStale
	case host.ActionOutcomeUnknown:
		return OperationOutcomeUnknown
	default:
		panic("validated ActionRunStatus was not mapped")
	}
}

func terminalOperationStatus(status OperationStatus) bool {
	return status == OperationSucceeded ||
		status == OperationFailed ||
		status == OperationCancelled ||
		status == OperationInterrupted ||
		status == OperationStale ||
		status == OperationRejected
}

func completeOperation(operation *operationState) bool {
	// An unresolved outcome is no longer delivered or cancellable. It remains
	// reconcilable until retention pruning removes it.
	if operation.status == OperationOutcomeUnknown {
		return true
	}
	if operation.status == OperationRejected {
		return true
	}
	if operation.status == OperationStale &&
		(operation.ack == nil || !operation.ack.Accepted) {
		return true
	}
	if operation.status == OperationCancelled &&
		operation.attempts == 0 &&
		operation.outcome == nil {
		return true
	}
	return operation.outcome != nil
}

func stopTimer(timer *time.Timer) {
	if timer.Stop() {
		return
	}
	select {
	case <-timer.C:
	default:
	}
}
