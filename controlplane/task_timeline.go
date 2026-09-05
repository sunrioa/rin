package controlplane

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/policy"
	"github.com/sunrioa/rin/timeline"
)

const maxOperationTimelineEvents = 64

// GetTaskTimeline returns only operation events submitted by the bound
// principal. Host administrators may inspect all principals for diagnostics.
func (service *Service) GetTaskTimeline(
	principal host.Principal,
	query timeline.Query,
) (timeline.Page, error) {
	if err := host.ValidatePrincipal(principal); err != nil {
		return timeline.Page{}, fmt.Errorf("%w: principal: %v", ErrInvalid, err)
	}
	query, _, err := timeline.NormalizeQuery(query)
	if err != nil {
		return timeline.Page{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.closed {
		return timeline.Page{}, ErrUnavailable
	}
	return service.taskTimelineLocked(principal, query)
}

// WaitTaskTimeline performs bounded long polling. Changed=false means no new
// timeline evidence and must never be reported as action completion.
func (service *Service) WaitTaskTimeline(
	ctx context.Context,
	principal host.Principal,
	input timeline.WaitInput,
) (timeline.Update, error) {
	if err := host.ValidatePrincipal(principal); err != nil {
		return timeline.Update{}, fmt.Errorf("%w: principal: %v", ErrInvalid, err)
	}
	input, after, err := timeline.NormalizeWait(input)
	if err != nil {
		return timeline.Update{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	timer := time.NewTimer(time.Duration(input.WaitMillis) * time.Millisecond)
	defer timer.Stop()
	for {
		service.mu.Lock()
		if service.closed {
			service.mu.Unlock()
			return timeline.Update{}, ErrUnavailable
		}
		page, pageErr := service.taskTimelineLocked(principal, input.Query())
		changed := service.changed
		service.mu.Unlock()
		if pageErr != nil {
			return timeline.Update{}, pageErr
		}
		latest, err := timeline.ParseCursor(page.NextCursor)
		if err != nil {
			return timeline.Update{}, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		if latest > after || input.WaitMillis == 0 {
			return timeline.Update{Timeline: page, Changed: latest > after}, nil
		}
		select {
		case <-ctx.Done():
			return timeline.Update{}, ctx.Err()
		case <-timer.C:
			service.mu.Lock()
			page, pageErr = service.taskTimelineLocked(principal, input.Query())
			service.mu.Unlock()
			if pageErr != nil {
				return timeline.Update{}, pageErr
			}
			latest, err = timeline.ParseCursor(page.NextCursor)
			if err != nil {
				return timeline.Update{}, fmt.Errorf("%w: %v", ErrInvalid, err)
			}
			return timeline.Update{Timeline: page, Changed: latest > after}, nil
		case <-changed:
		}
	}
}

func (service *Service) taskTimelineLocked(
	principal host.Principal,
	query timeline.Query,
) (timeline.Page, error) {
	visibleOperations := make([]*operationState, 0)
	for _, operation := range service.operations {
		request := operation.request.ActionRequest
		if request == nil || request.TaskID != query.TaskID {
			continue
		}
		if !hasScope(principal, ScopeHostAdmin) && operation.request.Principal.ID != principal.ID {
			continue
		}
		visibleOperations = append(visibleOperations, operation)
	}
	if len(visibleOperations) == 0 {
		return timeline.Page{}, ErrNotFound
	}
	for _, operation := range visibleOperations {
		service.refreshOperationHostLocked(operation)
	}
	if err := service.persistOperationsLocked(); err != nil {
		return timeline.Page{}, err
	}
	records := make([]timeline.Record, 0)
	latest := uint64(0)
	truncatedBefore := uint64(0)
	status := ""
	for _, operation := range visibleOperations {
		for _, item := range operation.timeline {
			records = append(records, timeline.Record{
				Sequence: item.Sequence,
				Event:    service.projectOperationTimelineEvent(operation, item),
			})
			if item.Sequence > latest {
				latest = item.Sequence
				status = string(item.Status)
			}
		}
		if operation.timelineTruncatedBefore > truncatedBefore {
			truncatedBefore = operation.timelineTruncatedBefore
		}
	}
	return timeline.BuildPage(timeline.Snapshot{
		TaskID: query.TaskID, Status: status, LatestSequence: latest,
		TruncatedBefore: truncatedBefore, Records: records,
	}, query)
}

func (service *Service) projectOperationTimelineEvent(
	operation *operationState,
	item operationTimelineEvent,
) timeline.Event {
	request := operation.request.ActionRequest
	event := timeline.Event{
		EventID:              operation.request.OperationID + ".event." + strconv.FormatUint(item.Sequence, 36),
		OccurredAtUnixMillis: item.AtUnixMillis,
		TaskID:               request.TaskID, HostID: operation.request.HostID,
		WorldID: operation.request.WorldID, ActorID: operation.request.ActorID,
		EventKind: item.Kind, PublicSummary: item.Summary, ReasonCode: item.ReasonCode,
		ObservationSequence: request.ObservationSeq,
		Operation: &timeline.OperationSummary{
			OperationID: operation.request.OperationID, Status: string(item.Status),
			Terminal: item.Terminal, ExecutionConfirmed: item.ExecutionConfirmed,
			ReconciliationPending: item.ReconciliationPending,
			OutcomeCode:           item.OutcomeCode, DeliveryAttempts: item.DeliveryAttempts,
			ProgressSequence: item.ProgressSequence, Progress: item.Progress,
			CancelRequested: item.CancelRequested,
		},
	}
	if operation.request.Binding != nil {
		epoch := operation.request.Binding.Epoch
		event.Epoch = &epoch
	}
	capability := request.Capability
	event.Capability = &capability
	if request.PlanStep != nil {
		event.PlanID = request.PlanStep.PlanID
		event.PlanRevision = request.PlanStep.PlanRevision
		event.PlanStepID = request.PlanStep.StepID
	}
	if operation.request.BoundAction != nil {
		event.ControllerID = operation.request.BoundAction.ControllerID
	}
	if item.PolicyDisposition != "" {
		event.Policy = &timeline.PolicySummary{
			Disposition: item.PolicyDisposition, ReasonCode: item.PolicyReasonCode,
			HumanSummary:        item.PolicySummary,
			MatchedRuleIDs:      append([]string(nil), item.MatchedRuleIDs...),
			ConfirmationPending: item.ConfirmationPending,
			EffectCount:         item.EffectCount,
		}
	}
	return event
}

func (service *Service) recordOperationTimelineLocked(operation *operationState) {
	if operation == nil {
		return
	}
	operation.persistenceRevision++
	service.recordOperationChangeLocked(operation.request.OperationID)
	if operation.request.ActionRequest == nil ||
		operation.request.ActionRequest.TaskID == "" {
		return
	}
	item := operationTimelineSnapshot(operation)
	if len(operation.timeline) != 0 && operationTimelineEventsEqual(
		operation.timeline[len(operation.timeline)-1], item,
	) {
		return
	}
	service.operationTimelineSequence++
	item.Sequence = service.operationTimelineSequence
	if len(operation.timeline) == maxOperationTimelineEvents {
		operation.timelineTruncatedBefore = operation.timeline[0].Sequence
		copy(operation.timeline, operation.timeline[1:])
		operation.timeline = operation.timeline[:maxOperationTimelineEvents-1]
	}
	operation.timeline = append(operation.timeline, item)
}

func operationTimelineSnapshot(operation *operationState) operationTimelineEvent {
	item := operationTimelineEvent{
		Kind: operationTimelineKind(operation), Status: operation.status,
		AtUnixMillis: operation.updatedAt, Terminal: settledOperation(operation),
		ExecutionConfirmed:    operation.status == OperationSucceeded && operation.outcome != nil,
		ReconciliationPending: reconciliationPending(operation),
		DeliveryAttempts:      operation.attempts, CancelRequested: operation.cancel,
	}
	if operation.run != nil {
		item.ProgressSequence = operation.run.ProgressSeq
		item.Progress = operation.run.Progress
	}
	if operation.outcome != nil {
		item.OutcomeCode = string(operation.outcome.Status)
		item.ReasonCode = string(operation.outcome.Status)
		item.Summary = operation.outcome.Summary
	} else if operation.rejection.Code != "" || operation.rejection.Message != "" {
		item.ReasonCode = operation.rejection.Code
		item.Summary = operation.rejection.Message
	} else {
		item.ReasonCode = string(operation.status)
	}
	if decision := operation.request.PolicyDecision; decision != nil {
		item.PolicyDisposition = string(decision.Result)
		item.PolicyReasonCode = decision.ReasonCode
		item.PolicySummary = decision.HumanSummary
		item.MatchedRuleIDs = append([]string(nil), decision.MatchedRuleIDs...)
		item.ConfirmationPending = decision.Result == policy.RequireConfirmation &&
			operation.status == OperationAwaitingConfirmation
		if operation.request.BoundAction != nil {
			item.EffectCount = uint32(len(operation.request.BoundAction.Effects))
		}
		if item.Summary == "" {
			item.Summary = decision.HumanSummary
		}
	}
	return item
}

func operationTimelineKind(operation *operationState) string {
	if operation.cancel && !completeOperation(operation) {
		return "operation.cancel-requested"
	}
	if operation.run != nil && operation.status == OperationRunning {
		return "operation.progress"
	}
	return "operation." + string(operation.status)
}

func operationTimelineEventsEqual(left, right operationTimelineEvent) bool {
	left.Sequence = 0
	right.Sequence = 0
	return left.Kind == right.Kind && left.Status == right.Status &&
		left.ReasonCode == right.ReasonCode && left.Summary == right.Summary &&
		left.AtUnixMillis == right.AtUnixMillis && left.Terminal == right.Terminal &&
		left.ExecutionConfirmed == right.ExecutionConfirmed &&
		left.ReconciliationPending == right.ReconciliationPending &&
		left.DeliveryAttempts == right.DeliveryAttempts &&
		left.ProgressSequence == right.ProgressSequence && left.Progress == right.Progress &&
		left.CancelRequested == right.CancelRequested && left.OutcomeCode == right.OutcomeCode &&
		left.PolicyDisposition == right.PolicyDisposition &&
		left.PolicyReasonCode == right.PolicyReasonCode && left.PolicySummary == right.PolicySummary &&
		slices.Equal(left.MatchedRuleIDs, right.MatchedRuleIDs) &&
		left.ConfirmationPending == right.ConfirmationPending && left.EffectCount == right.EffectCount
}
