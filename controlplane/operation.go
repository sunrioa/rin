package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/policy"
)

const (
	defaultMaxOperations = 1_024
	hardMaxOperations    = 65_536
	defaultOperationTTL  = 30 * time.Minute
	maxHostPollItems     = 64
)

type operationState struct {
	request                 HostControlRequest
	status                  OperationStatus
	attempts                uint32
	cancel                  bool
	ack                     *HostAcknowledgement
	run                     *host.ActionRun
	outcome                 *host.ActionOutcome
	outcomeDelivery         map[string]bool
	output                  json.RawMessage
	rejection               HostAcknowledgement
	idempotency             string
	createdAt               int64
	updatedAt               int64
	children                []string
	timeline                []operationTimelineEvent
	timelineTruncatedBefore uint64
}

type operationTimelineEvent struct {
	Sequence              uint64          `json:"sequence"`
	Kind                  string          `json:"kind"`
	Status                OperationStatus `json:"status"`
	ReasonCode            string          `json:"reason_code,omitempty"`
	Summary               string          `json:"summary,omitempty"`
	AtUnixMillis          int64           `json:"at_unix_millis"`
	Terminal              bool            `json:"terminal"`
	ExecutionConfirmed    bool            `json:"execution_confirmed"`
	ReconciliationPending bool            `json:"reconciliation_pending"`
	DeliveryAttempts      uint32          `json:"delivery_attempts,omitempty"`
	ProgressSequence      uint64          `json:"progress_sequence,omitempty"`
	Progress              uint32          `json:"progress,omitempty"`
	CancelRequested       bool            `json:"cancel_requested,omitempty"`
	OutcomeCode           string          `json:"outcome_code,omitempty"`
	PolicyDisposition     string          `json:"policy_disposition,omitempty"`
	PolicyReasonCode      string          `json:"policy_reason_code,omitempty"`
	PolicySummary         string          `json:"policy_summary,omitempty"`
	MatchedRuleIDs        []string        `json:"matched_rule_ids,omitempty"`
	ConfirmationPending   bool            `json:"confirmation_pending,omitempty"`
	EffectCount           uint32          `json:"effect_count,omitempty"`
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
		service.expireControllersLocked(service.now().UnixMilli())
		service.pruneOperationsLocked(service.now().UnixMilli())
		if err := service.persistOperationsLocked(); err != nil {
			service.mu.Unlock()
			return HostControlBatch{}, err
		}
		batch := service.collectHostWorkLocked(hostID, limit)
		if len(batch.GatewayRequests) != 0 || len(batch.Requests) != 0 ||
			len(batch.Cancellations) != 0 {
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
		service.finalizeOperationPolicyLocked(operation, false)
	}
	service.recordOperationTimelineLocked(operation)
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
	if operation.outcome != nil {
		return fmt.Errorf("%w: terminal result is already recorded", ErrConflict)
	}
	if operation.ack == nil || !operation.ack.Accepted {
		return ErrNotAccepted
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
	service.recordOperationTimelineLocked(operation)
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
			service.queueOutcomeDeliveryLocked(operation)
			if err := service.persistOperationsLocked(); err != nil {
				return err
			}
			return nil
		}
		return fmt.Errorf("%w: terminal result changed", ErrConflict)
	}
	if operation.ack == nil || !operation.ack.Accepted {
		return ErrNotAccepted
	}
	expected := operationStatusFromRun(outcome.Status)
	if terminalOperationStatus(operation.status) && operation.status != expected {
		return fmt.Errorf("%w: outcome conflicts with terminal run", ErrConflict)
	}
	if operation.request.Binding != nil &&
		outcome.Epoch != operation.request.Binding.Epoch {
		return fmt.Errorf("%w: outcome epoch does not match request binding", ErrInvalid)
	}
	if operation.request.Binding != nil &&
		outcome.WorldSeq < operation.request.Binding.ObservationSeq {
		return fmt.Errorf("%w: outcome world sequence predates request binding", ErrInvalid)
	}
	cloned := cloneOutcome(outcome)
	operation.outcome = &cloned
	service.queueOutcomeDeliveryLocked(operation)
	operation.output = append(json.RawMessage(nil), output...)
	operation.status = expected
	operation.updatedAt = service.now().UnixMilli()
	service.finalizeOperationPolicyLocked(
		operation,
		outcome.Status == host.ActionSucceeded,
	)
	service.recordOperationTimelineLocked(operation)
	service.markOperationsDirtyLocked()
	if err := service.persistOperationsLocked(); err != nil {
		return err
	}
	return nil
}

func operationOutcomeEvidence(
	operation *operationState,
	outcome host.ActionOutcome,
) *OutcomeEvidence {
	if operation == nil || operation.request.ActionRequest == nil {
		return nil
	}
	request := operation.request.ActionRequest
	return &OutcomeEvidence{
		TaskID: request.TaskID, OperationID: operation.request.OperationID,
		HostID: operation.request.HostID, WorldID: operation.request.WorldID,
		ActorID: request.ActorID, ControllerID: request.ControllerID,
		Capability: request.Capability, ExpectedEpoch: request.ExpectedEpoch,
		ObservationSequence: request.ObservationSeq, PlanStep: clonePlanStepRef(request.PlanStep),
		Outcome: cloneOutcome(outcome),
	}
}

func clonePlanStepRef(ref *host.PlanStepRef) *host.PlanStepRef {
	if ref == nil {
		return nil
	}
	cloned := *ref
	return &cloned
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

// ListOperations returns recent operations visible to one principal. It is a
// read projection over the existing Operation Store, not a second event log.
func (service *Service) ListOperations(
	principal host.Principal,
	input ListOperationsInput,
) ([]OperationView, error) {
	if err := host.ValidatePrincipal(principal); err != nil {
		return nil, fmt.Errorf("%w: principal: %v", ErrInvalid, err)
	}
	for _, value := range []struct {
		field string
		value string
	}{
		{"host_id", input.HostID}, {"world_id", input.WorldID},
		{"actor_id", input.ActorID}, {"task_id", input.TaskID},
	} {
		if value.value != "" {
			if err := validateID(value.field, value.value); err != nil {
				return nil, err
			}
		}
	}
	if input.Status != "" && !validOperationStatus(input.Status) {
		return nil, invalid("status", "is not a known operation status")
	}
	if input.Limit == 0 {
		input.Limit = 100
	}
	if input.Limit > 500 {
		return nil, invalid("limit", "must not exceed 500")
	}

	service.mu.Lock()
	defer service.mu.Unlock()
	result := make([]OperationView, 0, min(len(service.operations), int(input.Limit)))
	for _, operation := range service.operations {
		if !hasScope(principal, ScopeHostAdmin) &&
			principal.ID != operation.request.Principal.ID {
			continue
		}
		service.refreshOperationHostLocked(operation)
		request := operation.request
		taskID := ""
		if request.ActionRequest != nil {
			taskID = request.ActionRequest.TaskID
		}
		if input.HostID != "" && request.HostID != input.HostID ||
			input.WorldID != "" && request.WorldID != input.WorldID ||
			input.ActorID != "" && request.ActorID != input.ActorID ||
			input.TaskID != "" && taskID != input.TaskID ||
			input.Status != "" && operation.status != input.Status {
			continue
		}
		result = append(result, operationView(operation))
	}
	if err := service.persistOperationsLocked(); err != nil {
		return nil, err
	}
	slices.SortFunc(result, func(left, right OperationView) int {
		if left.UpdatedAt != right.UpdatedAt {
			if left.UpdatedAt > right.UpdatedAt {
				return -1
			}
			return 1
		}
		return strings.Compare(left.OperationID, right.OperationID)
	})
	if len(result) > int(input.Limit) {
		result = result[:input.Limit]
	}
	return result, nil
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
		service.finalizeOperationPolicyLocked(operation, false)
		service.recordOperationTimelineLocked(operation)
		service.markOperationsDirtyLocked()
		if err := service.persistOperationsLocked(); err != nil {
			return OperationView{}, err
		}
		return operationView(operation), nil
	}
	if operation.status == OperationAwaitingConfirmation {
		operation.status = OperationCancelled
		operation.updatedAt = service.now().UnixMilli()
		service.finalizeOperationPolicyLocked(operation, false)
		service.recordOperationTimelineLocked(operation)
		service.markOperationsDirtyLocked()
		if err := service.persistOperationsLocked(); err != nil {
			return OperationView{}, err
		}
		return operationView(operation), nil
	}
	if !operation.cancel {
		operation.cancel = true
		operation.updatedAt = service.now().UnixMilli()
		service.recordOperationTimelineLocked(operation)
		service.markOperationsDirtyLocked()
	}
	if err := service.persistOperationsLocked(); err != nil {
		return OperationView{}, err
	}
	return operationView(operation), nil
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
		leftPriority := deliveryPriority(left.status)
		rightPriority := deliveryPriority(right.status)
		if leftPriority != rightPriority {
			return leftPriority - rightPriority
		}
		if left.createdAt < right.createdAt {
			return -1
		}
		if left.createdAt > right.createdAt {
			return 1
		}
		return compare(left.request.OperationID, right.request.OperationID)
	})

	batch := HostControlBatch{
		GatewayRequests: make([]HostGatewayDelivery, 0),
		Requests:        make([]HostControlDelivery, 0),
		Cancellations:   make([]string, 0),
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
	batch.GatewayRequests = service.collectHostGatewayWorkLocked(
		hostID,
		limit-len(batch.Cancellations),
	)
	for _, operation := range operations {
		if len(batch.GatewayRequests)+len(batch.Requests)+
			len(batch.Cancellations) >= limit {
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
		if operation.ack == nil {
			operation.status = OperationDelivered
		}
		operation.updatedAt = now
		service.recordOperationTimelineLocked(operation)
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

func deliveryPriority(status OperationStatus) int {
	switch status {
	case OperationQueued:
		return 0
	case OperationDelivered:
		return 1
	case OperationAccepted:
		return 2
	default:
		return 3
	}
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
				service.finalizeOperationPolicyLocked(operation, true)
				service.recordOperationTimelineLocked(operation)
				changed = true
			}
		} else {
			if operation.status != OperationStale {
				operation.status = OperationStale
				operation.updatedAt = now
				service.finalizeOperationPolicyLocked(operation, false)
				service.recordOperationTimelineLocked(operation)
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
		if !completeOperation(operation) || hasPendingOutcome(operation) || operation.updatedAt > cutoff {
			continue
		}
		if len(operation.children) != 0 {
			continue
		}
		delete(service.operations, operationID)
		delete(service.requests, operation.idempotency)
		if parent := service.operations[operation.request.ParentOperationID]; parent != nil {
			parent.children = slices.DeleteFunc(
				parent.children,
				func(childID string) bool { return childID == operationID },
			)
			parent.updatedAt = now
		}
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
		service.finalizeOperationPolicyLocked(operation, true)
	} else {
		operation.status = OperationStale
		service.finalizeOperationPolicyLocked(operation, false)
	}
	operation.updatedAt = now
	service.recordOperationTimelineLocked(operation)
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
		ControllerLeaseID:     controllerLeaseID(operation.request.Binding),
		ParentOperationID:     operation.request.ParentOperationID,
		ChildOperationIDs:     append([]string(nil), operation.children...),
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
	if operation.request.ActionRequest != nil {
		cloned := cloneActionRequest(*operation.request.ActionRequest)
		view.ActionRequest = &cloned
	}
	if operation.request.BoundAction != nil {
		cloned := cloneBoundAction(*operation.request.BoundAction)
		view.BoundAction = &cloned
	}
	if operation.request.PolicyDecision != nil {
		cloned := policy.CloneDecision(*operation.request.PolicyDecision)
		view.PolicyDecision = &cloned
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
		"op2:%s:%d:%t:%d:%s:%d:%t:%d",
		operation.status,
		operation.attempts,
		operation.cancel,
		operation.updatedAt,
		runStatus,
		progressSequence,
		operation.outcome != nil,
		len(operation.children),
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

func cloneControlRequest(value HostControlRequest) HostControlRequest {
	cloned := value
	cloned.Principal = clonePrincipalValue(value.Principal)
	if value.Binding != nil {
		binding := *value.Binding
		cloned.Binding = &binding
	}
	if value.ActionRequest != nil {
		action := cloneActionRequest(*value.ActionRequest)
		cloned.ActionRequest = &action
	}
	if value.BoundAction != nil {
		action := cloneBoundAction(*value.BoundAction)
		cloned.BoundAction = &action
	}
	if value.PolicyDecision != nil {
		decision := policy.CloneDecision(*value.PolicyDecision)
		cloned.PolicyDecision = &decision
	}
	return cloned
}

func controllerLeaseID(binding *ControlBinding) string {
	if binding == nil {
		return ""
	}
	return binding.ControllerLeaseID
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
