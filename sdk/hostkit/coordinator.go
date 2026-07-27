package hostkit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"

	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/protocol"
)

// Coordinator owns one restartable Host workflow slot.
type Coordinator struct {
	transport  RinTransport
	dispatcher AuthorityDispatcher
	store      HostStateStore
	identity   IdentityProvider
	registry   CapabilityRegistry
	executor   ActionExecutor
	mu         sync.Mutex
}

// NewCoordinator constructs a Coordinator from explicit engine-facing ports.
func NewCoordinator(
	transport RinTransport,
	dispatcher AuthorityDispatcher,
	store HostStateStore,
	identity IdentityProvider,
	registry CapabilityRegistry,
	executor ActionExecutor,
) (*Coordinator, error) {
	if transport == nil || dispatcher == nil || store == nil || identity == nil ||
		registry == nil || executor == nil {
		return nil, errors.New("all HostKit ports are required")
	}
	return &Coordinator{
		transport: transport, dispatcher: dispatcher, store: store,
		identity: identity, registry: registry, executor: executor,
	}, nil
}

// BeginDecision durably retains a validated request before any network call.
func (coordinator *Coordinator) BeginDecision(
	ctx context.Context,
	operationID string,
	request protocol.ProposeRequest,
) (PendingDecision, error) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if err := protocol.ValidatePropose(request); err != nil {
		return PendingDecision{}, err
	}
	if operationID == "" {
		var err error
		operationID, err = coordinator.newID(ctx, IDOperation)
		if err != nil {
			return PendingDecision{}, err
		}
	} else if err := protocol.ValidateIdentifier("operation_id", operationID); err != nil {
		return PendingDecision{}, err
	}
	identity, err := coordinator.current(ctx)
	if err != nil {
		return PendingDecision{}, err
	}
	if request.SessionID != identity.SessionID ||
		request.DecisionWindow.Epoch != identity.Epoch {
		return PendingDecision{}, ErrStaleEpoch
	}
	state, err := coordinator.load(ctx)
	if err != nil {
		return PendingDecision{}, err
	}
	if state.Pending != nil {
		return PendingDecision{}, ErrPendingExists
	}
	if len(state.Outbox) != 0 {
		return PendingDecision{}, ErrOutboxPending
	}
	next, err := state.Clone()
	if err != nil {
		return PendingDecision{}, err
	}
	pending := PendingDecision{OperationID: operationID, Request: request}
	next.Pending = &pending
	next.Revision++
	if err := next.Validate(); err != nil {
		return PendingDecision{}, err
	}
	if err := coordinator.store.CompareAndSwap(ctx, state.Revision, next); err != nil {
		return PendingDecision{}, err
	}
	return pending, nil
}

// PendingResult is one non-blocking Proposal Job poll result.
type PendingResult struct {
	Pending  PendingDecision
	Job      protocol.ProposalJob
	Proposal *protocol.ActionProposal
	Ready    bool
}

// ResumePendingWork drains exact reports, submits an idempotent Proposal Job
// when needed, and performs one bounded poll without an internal wait loop.
// Call it again after queued or running results.
func (coordinator *Coordinator) ResumePendingWork(
	ctx context.Context,
) (PendingResult, error) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if _, err := coordinator.drainOutbox(ctx); err != nil {
		return PendingResult{}, err
	}
	state, err := coordinator.load(ctx)
	if err != nil {
		return PendingResult{}, err
	}
	if state.Pending == nil {
		return PendingResult{}, ErrPendingMissing
	}
	pending := *state.Pending
	identity, err := coordinator.current(ctx)
	if err != nil {
		return PendingResult{}, err
	}
	if pending.Request.SessionID != identity.SessionID ||
		pending.Request.DecisionWindow.Epoch != identity.Epoch {
		if err := coordinator.clearPending(ctx, state); err != nil {
			return PendingResult{}, err
		}
		return PendingResult{}, ErrStaleEpoch
	}
	if pending.JobID == "" {
		submission, submitErr := coordinator.transport.SubmitProposal(ctx, pending.Request)
		if submitErr != nil {
			return PendingResult{}, submitErr
		}
		if submission.ProtocolVersion != protocol.Version ||
			!validJobStatus(submission.Status) {
			return PendingResult{}, errors.New("Rin returned an invalid Proposal Job submission")
		}
		if err := protocol.ValidateIdentifier("job_id", submission.JobID); err != nil {
			return PendingResult{}, err
		}
		next, cloneErr := state.Clone()
		if cloneErr != nil {
			return PendingResult{}, cloneErr
		}
		next.Pending.JobID = submission.JobID
		next.Revision++
		if err := next.Validate(); err != nil {
			return PendingResult{}, err
		}
		if err := coordinator.store.CompareAndSwap(
			ctx, state.Revision, next,
		); err != nil {
			return PendingResult{}, err
		}
		state = next
		pending = *next.Pending
	}
	job, err := coordinator.transport.PollProposal(ctx, pending.JobID)
	if err != nil {
		return PendingResult{}, err
	}
	if err := validateJob(pending, job); err != nil {
		return PendingResult{}, err
	}
	result := PendingResult{Pending: pending, Job: job}
	switch job.Status {
	case "queued", "running":
		return result, nil
	case "succeeded":
		result.Proposal = job.Proposal
		result.Ready = true
		return result, nil
	case "failed", "stale", "canceled":
		if job.Error == nil {
			return PendingResult{}, errors.New("terminal Proposal Job has no error")
		}
		return PendingResult{}, fmt.Errorf(
			"Proposal Job %s: %s: %s", job.Status, job.Error.Code, job.Error.Message)
	default:
		return PendingResult{}, fmt.Errorf("unknown Proposal Job status %q", job.Status)
	}
}

// DispatchRequest adds host-authored report metadata to a selected Proposal.
type DispatchRequest struct {
	Proposal           protocol.ActionProposal
	InvocationDeadline host.Timepoint
	Summary            string
	Tags               []string
	Facts              []protocol.Fact
	GoalUpdates        []protocol.GoalUpdate
}

// DispatchAndEnqueue validates the selected offer, repeats authorization on
// the authority thread, invokes the game executor, and persists the exact
// lifecycle report before it can be sent. Long-running executors may return a
// queued or running record and later call RecordTransitionAndEnqueue.
func (coordinator *Coordinator) DispatchAndEnqueue(
	ctx context.Context,
	request DispatchRequest,
) (ActionRecord, error) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	state, err := coordinator.load(ctx)
	if err != nil {
		return ActionRecord{}, err
	}
	if state.Pending == nil {
		return ActionRecord{}, ErrPendingMissing
	}
	pending := *state.Pending
	if pending.JobID == "" {
		return ActionRecord{}, errors.New("Pending Decision has no accepted Proposal Job")
	}
	if err := validateProposalForPending(pending, request.Proposal); err != nil {
		return ActionRecord{}, err
	}
	identity, err := coordinator.current(ctx)
	if err != nil {
		return ActionRecord{}, err
	}
	if identity.Epoch != request.Proposal.DecisionWindow.Epoch {
		return ActionRecord{}, ErrStaleEpoch
	}
	invocation, err := coordinator.registry.NewInvocation(
		request.Proposal.Action, pending.OperationID, identity.Now,
		request.InvocationDeadline, identity.Epoch,
	)
	if err != nil {
		return ActionRecord{}, err
	}
	reportRequestID, err := coordinator.newID(ctx, IDRequest)
	if err != nil {
		return ActionRecord{}, err
	}
	eventID, err := coordinator.newID(ctx, IDEvent)
	if err != nil {
		return ActionRecord{}, err
	}
	outboxID, err := coordinator.newID(ctx, IDOutbox)
	if err != nil {
		return ActionRecord{}, err
	}
	var persisted ActionRecord
	commitErr := coordinator.store.CommitEffect(
		ctx,
		state.Revision,
		func(effectContext context.Context) (WorkflowState, error) {
			current, currentErr := coordinator.current(effectContext)
			if currentErr != nil {
				return WorkflowState{}, currentErr
			}
			if current.Epoch != invocation.ExpectedEpoch {
				return WorkflowState{}, ErrStaleEpoch
			}
			var run host.ActionRun
			var outcome *host.ActionOutcome
			dispatchErr := coordinator.dispatcher.Dispatch(
				effectContext,
				func(authorityContext context.Context) error {
					authorityIdentity, identityErr := coordinator.current(authorityContext)
					if identityErr != nil {
						return identityErr
					}
					if err := coordinator.registry.AuthorizeInvocation(
						invocation, authorityIdentity.Now, authorityIdentity.Epoch,
					); err != nil {
						return err
					}
					run, outcome, identityErr = coordinator.executor.Execute(
						authorityContext, invocation)
					return identityErr
				},
			)
			if dispatchErr != nil {
				return WorkflowState{}, dispatchErr
			}
			record, report, buildErr := buildActionReport(
				pending, request, invocation, run, outcome,
				current, reportRequestID, eventID,
			)
			if buildErr != nil {
				return WorkflowState{}, buildErr
			}
			next, cloneErr := state.Clone()
			if cloneErr != nil {
				return WorkflowState{}, cloneErr
			}
			next.Pending = nil
			next.Actions = append(next.Actions, record)
			next.Outbox = append(next.Outbox, OutboxEntry{ID: outboxID, Request: report})
			next.Revision++
			if err := next.Validate(); err != nil {
				return WorkflowState{}, err
			}
			persisted = record
			return next, nil
		},
	)
	if commitErr != nil {
		return ActionRecord{}, commitErr
	}
	return persisted, nil
}

// TransitionRequest records a later long-running action callback.
type TransitionRequest struct {
	OperationID string
	Run         host.ActionRun
	Outcome     *host.ActionOutcome
	Summary     string
	Tags        []string
	Facts       []protocol.Fact
	GoalUpdates []protocol.GoalUpdate
}

// RecordTransitionAndEnqueue persists one monotonic ActionRun transition and
// its exact Action Report.
func (coordinator *Coordinator) RecordTransitionAndEnqueue(
	ctx context.Context,
	request TransitionRequest,
) (ActionRecord, error) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	state, err := coordinator.load(ctx)
	if err != nil {
		return ActionRecord{}, err
	}
	index := actionIndex(state.Actions, request.OperationID)
	if index < 0 {
		return ActionRecord{}, fmt.Errorf("operation %q is not retained", request.OperationID)
	}
	previous := state.Actions[index]
	if !host.CanTransitionActionRun(previous.Run.Status, request.Run.Status) {
		return ActionRecord{}, fmt.Errorf(
			"illegal action transition %s -> %s", previous.Run.Status, request.Run.Status)
	}
	if request.Run.ProgressSeq <= previous.Run.ProgressSeq ||
		request.Run.Progress < previous.Run.Progress ||
		request.Run.UpdatedAt.Clock != previous.Run.UpdatedAt.Clock ||
		request.Run.UpdatedAt.Value < previous.Run.UpdatedAt.Value {
		return ActionRecord{}, errors.New("ActionRun progress must be monotonic")
	}
	identity, err := coordinator.current(ctx)
	if err != nil {
		return ActionRecord{}, err
	}
	if identity.Epoch != previous.Invocation.ExpectedEpoch &&
		previous.Run.Status != host.ActionOutcomeUnknown {
		return ActionRecord{}, ErrStaleEpoch
	}
	reportRequestID, err := coordinator.newID(ctx, IDRequest)
	if err != nil {
		return ActionRecord{}, err
	}
	eventID, err := coordinator.newID(ctx, IDEvent)
	if err != nil {
		return ActionRecord{}, err
	}
	outboxID, err := coordinator.newID(ctx, IDOutbox)
	if err != nil {
		return ActionRecord{}, err
	}
	updated := previous
	updated.Run = request.Run
	updated.Outcome = request.Outcome
	report := protocol.ReportActionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       previous.Invocation.ExpectedEpoch.SessionID,
		RequestID:       reportRequestID,
		Tick:            identity.Tick,
		Report: protocol.ActionReport{
			ProposalID: previous.ProposalID, EventID: eventID,
			Decision:   protocol.ActionAccepted,
			Invocation: &updated.Invocation, Run: &updated.Run, Outcome: updated.Outcome,
			Summary: request.Summary, Tags: request.Tags,
			Facts: request.Facts, GoalUpdates: request.GoalUpdates,
		},
	}
	if err := validateRecordAndReport(previous.Run.Status, updated, report); err != nil {
		return ActionRecord{}, err
	}
	next, err := state.Clone()
	if err != nil {
		return ActionRecord{}, err
	}
	next.Actions[index] = updated
	next.Outbox = append(next.Outbox, OutboxEntry{ID: outboxID, Request: report})
	next.Revision++
	if err := next.Validate(); err != nil {
		return ActionRecord{}, err
	}
	if err := coordinator.store.CompareAndSwap(ctx, state.Revision, next); err != nil {
		return ActionRecord{}, err
	}
	return updated, nil
}

// DrainOutbox reports retained entries in order and removes acknowledged ones.
func (coordinator *Coordinator) DrainOutbox(ctx context.Context) (int, error) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	return coordinator.drainOutbox(ctx)
}

func (coordinator *Coordinator) drainOutbox(ctx context.Context) (int, error) {
	acknowledged := 0
	for {
		state, err := coordinator.load(ctx)
		if err != nil {
			return acknowledged, err
		}
		if len(state.Outbox) == 0 {
			return acknowledged, nil
		}
		entry := state.Outbox[0]
		result, err := coordinator.transport.ReportAction(ctx, entry.Request)
		if err != nil {
			return acknowledged, err
		}
		if result.SessionID != entry.Request.SessionID {
			return acknowledged, errors.New("Rin acknowledged an Outbox report for another Session")
		}
		next, err := state.Clone()
		if err != nil {
			return acknowledged, err
		}
		next.Outbox = append([]OutboxEntry(nil), next.Outbox[1:]...)
		next.Revision++
		if err := next.Validate(); err != nil {
			return acknowledged, err
		}
		if err := coordinator.store.CompareAndSwap(ctx, state.Revision, next); err != nil {
			return acknowledged, err
		}
		acknowledged++
	}
}

// ReconcileEpoch removes a stale Pending Decision and cooperatively cancels
// every non-terminal action from an older epoch. Cancellation reports enter the
// same exact-retry Outbox as normal transitions.
func (coordinator *Coordinator) ReconcileEpoch(
	ctx context.Context,
) (int, error) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	state, err := coordinator.load(ctx)
	if err != nil {
		return 0, err
	}
	identity, err := coordinator.current(ctx)
	if err != nil {
		return 0, err
	}
	next, err := state.Clone()
	if err != nil {
		return 0, err
	}
	changed := false
	if next.Pending != nil && next.Pending.Request.DecisionWindow.Epoch != identity.Epoch {
		next.Pending = nil
		changed = true
	}
	type cancellationIDs struct{ request, event, outbox string }
	ids := make(map[string]cancellationIDs)
	for _, action := range next.Actions {
		if terminal(action.Run.Status) ||
			action.Run.Status == host.ActionOutcomeUnknown ||
			action.Invocation.ExpectedEpoch == identity.Epoch {
			continue
		}
		requestID, idErr := coordinator.newID(ctx, IDRequest)
		if idErr != nil {
			return 0, idErr
		}
		eventID, idErr := coordinator.newID(ctx, IDEvent)
		if idErr != nil {
			return 0, idErr
		}
		outboxID, idErr := coordinator.newID(ctx, IDOutbox)
		if idErr != nil {
			return 0, idErr
		}
		ids[action.Invocation.OperationID] = cancellationIDs{
			request: requestID, event: eventID, outbox: outboxID,
		}
	}
	if len(ids) == 0 {
		if !changed {
			return 0, nil
		}
		next.Revision++
		if err := next.Validate(); err != nil {
			return 0, err
		}
		return 0, coordinator.store.CompareAndSwap(ctx, state.Revision, next)
	}
	cancelled := 0
	err = coordinator.store.CommitEffect(
		ctx, state.Revision,
		func(effectContext context.Context) (WorkflowState, error) {
			for index := range next.Actions {
				action := &next.Actions[index]
				actionIDs, exists := ids[action.Invocation.OperationID]
				if !exists {
					continue
				}
				descriptor, registered := coordinator.registry.Resolve(
					action.Invocation.Capability)
				var run host.ActionRun
				var outcome *host.ActionOutcome
				if !registered ||
					descriptor.Cancellation == host.CancellationUnsupported {
					run = host.ActionRun{
						OperationID: action.Invocation.OperationID,
						Status:      host.ActionOutcomeUnknown,
						ProgressSeq: action.Run.ProgressSeq + 1,
						Progress:    action.Run.Progress,
						UpdatedAt:   identity.Now,
						Message:     "Host epoch changed and cancellation is unsupported.",
					}
				} else {
					var cancelledOutcome host.ActionOutcome
					dispatchErr := coordinator.dispatcher.Dispatch(
						effectContext,
						func(authorityContext context.Context) error {
							var cancelErr error
							run, cancelledOutcome, cancelErr = coordinator.executor.Cancel(
								authorityContext, action.Invocation, identity)
							return cancelErr
						},
					)
					if dispatchErr != nil {
						return WorkflowState{}, dispatchErr
					}
					outcome = &cancelledOutcome
				}
				previousStatus := action.Run.Status
				action.Run, action.Outcome = run, outcome
				summary := run.Message
				if outcome != nil {
					summary = outcome.Summary
				}
				report := protocol.ReportActionRequest{
					ProtocolVersion: protocol.Version,
					SessionID:       action.Invocation.ExpectedEpoch.SessionID,
					RequestID:       actionIDs.request,
					Tick:            identity.Tick,
					Report: protocol.ActionReport{
						ProposalID: action.ProposalID, EventID: actionIDs.event,
						Decision:   protocol.ActionAccepted,
						Invocation: &action.Invocation, Run: &action.Run,
						Outcome: action.Outcome, Summary: summary,
					},
				}
				if err := validateRecordAndReport(previousStatus, *action, report); err != nil {
					return WorkflowState{}, err
				}
				next.Outbox = append(next.Outbox, OutboxEntry{
					ID: actionIDs.outbox, Request: report,
				})
				cancelled++
			}
			next.Revision++
			if err := next.Validate(); err != nil {
				return WorkflowState{}, err
			}
			return next, nil
		},
	)
	if err != nil {
		return 0, err
	}
	return cancelled, nil
}

func (coordinator *Coordinator) load(ctx context.Context) (WorkflowState, error) {
	state, err := coordinator.store.Load(ctx)
	if err != nil {
		return WorkflowState{}, err
	}
	if err := state.Validate(); err != nil {
		return WorkflowState{}, err
	}
	return state, nil
}

func (coordinator *Coordinator) current(ctx context.Context) (HostIdentity, error) {
	identity, err := coordinator.identity.Current(ctx)
	if err != nil {
		return HostIdentity{}, err
	}
	if identity.SessionID != identity.Epoch.SessionID {
		return HostIdentity{}, errors.New("Host identity Session and Epoch do not match")
	}
	if err := identity.Epoch.Validate("identity.epoch"); err != nil {
		return HostIdentity{}, err
	}
	if err := identity.Now.Validate("identity.now"); err != nil {
		return HostIdentity{}, err
	}
	if identity.ObservationSeq == 0 {
		return HostIdentity{}, errors.New("Host identity observation sequence must be positive")
	}
	return identity, nil
}

func (coordinator *Coordinator) newID(ctx context.Context, kind IDKind) (string, error) {
	value, err := coordinator.identity.NewID(ctx, kind)
	if err != nil {
		return "", err
	}
	if err := protocol.ValidateIdentifier(string(kind)+"_id", value); err != nil {
		return "", err
	}
	return value, nil
}

func (coordinator *Coordinator) clearPending(
	ctx context.Context,
	state WorkflowState,
) error {
	next, err := state.Clone()
	if err != nil {
		return err
	}
	next.Pending = nil
	next.Revision++
	if err := next.Validate(); err != nil {
		return err
	}
	return coordinator.store.CompareAndSwap(ctx, state.Revision, next)
}

func validateJob(pending PendingDecision, job protocol.ProposalJob) error {
	if job.ProtocolVersion != protocol.Version || job.JobID != pending.JobID ||
		job.SessionID != pending.Request.SessionID ||
		job.RequestID != pending.Request.RequestID {
		return errors.New("Proposal Job does not match the Pending Decision")
	}
	if job.Status == "succeeded" {
		if job.Proposal == nil {
			return errors.New("successful Proposal Job has no Proposal")
		}
		return validateProposalForPending(pending, *job.Proposal)
	}
	return nil
}

func validateProposalForPending(
	pending PendingDecision,
	proposal protocol.ActionProposal,
) error {
	if proposal.SessionID != pending.Request.SessionID ||
		proposal.RequestID != pending.Request.RequestID ||
		proposal.ActorID != pending.Request.ActorID ||
		proposal.DecisionWindow.ID != pending.Request.DecisionWindow.ID ||
		proposal.DecisionWindow.Epoch != pending.Request.DecisionWindow.Epoch {
		return errors.New("Proposal does not match the Pending Decision")
	}
	if err := protocol.ValidateIdentifier("proposal_id", proposal.ID); err != nil {
		return err
	}
	offered := false
	for _, candidate := range pending.Request.Offers {
		if actionOffersEqual(candidate, proposal.Action) {
			offered = true
			break
		}
	}
	if !offered {
		return errors.New("Proposal selected an action that the Host did not offer")
	}
	if err := host.ValidateActionOffer(proposal.Action); err != nil {
		return err
	}
	return nil
}

func buildActionReport(
	pending PendingDecision,
	request DispatchRequest,
	invocation host.ActionInvocation,
	run host.ActionRun,
	outcome *host.ActionOutcome,
	identity HostIdentity,
	reportRequestID string,
	eventID string,
) (ActionRecord, protocol.ReportActionRequest, error) {
	record := ActionRecord{
		ProposalID: request.Proposal.ID, ProposalRequestID: pending.Request.RequestID,
		Invocation: invocation, Run: run, Outcome: outcome,
	}
	report := protocol.ReportActionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       pending.Request.SessionID,
		RequestID:       reportRequestID,
		Tick:            identity.Tick,
		Report: protocol.ActionReport{
			ProposalID: request.Proposal.ID, EventID: eventID,
			Decision:   protocol.ActionAccepted,
			Invocation: &record.Invocation, Run: &record.Run, Outcome: record.Outcome,
			Summary: request.Summary, Tags: request.Tags,
			Facts: request.Facts, GoalUpdates: request.GoalUpdates,
		},
	}
	return record, report, validateRecordAndReport("", record, report)
}

func validateRecordAndReport(
	previous host.ActionRunStatus,
	record ActionRecord,
	report protocol.ReportActionRequest,
) error {
	if record.Invocation.OperationID != record.Run.OperationID {
		return errors.New("executor returned a Run for another operation")
	}
	if previous != "" && !host.CanTransitionActionRun(previous, record.Run.Status) {
		return fmt.Errorf("illegal action transition %s -> %s", previous, record.Run.Status)
	}
	if previous != "" && record.Run.ProgressSeq == 0 {
		return errors.New("action transition has no progress sequence")
	}
	if terminal(record.Run.Status) != (record.Outcome != nil) {
		return errors.New("terminal Runs require an Outcome and non-terminal Runs forbid one")
	}
	if record.Outcome != nil &&
		(record.Outcome.OperationID != record.Invocation.OperationID ||
			record.Outcome.Status != record.Run.Status) {
		return errors.New("Run and Outcome identities or statuses do not match")
	}
	if record.Run.Status == host.ActionSucceeded &&
		(record.Outcome.OccurredAt.Clock != record.Invocation.Deadline.Clock ||
			record.Outcome.OccurredAt.Value > record.Invocation.Deadline.Value) {
		return errors.New("successful action completed after its invocation deadline")
	}
	return protocol.ValidateReportAction(report)
}

func validJobStatus(status string) bool {
	switch status {
	case "queued", "running", "succeeded", "failed", "stale", "canceled":
		return true
	default:
		return false
	}
}

func actionOffersEqual(left, right host.ActionOffer) bool {
	return left.OfferID == right.OfferID &&
		left.DecisionWindowID == right.DecisionWindowID &&
		left.ActorID == right.ActorID &&
		left.Capability == right.Capability &&
		left.DescriptorDigest == right.DescriptorDigest &&
		left.Description == right.Description &&
		bytes.Equal(left.Arguments, right.Arguments) &&
		reflect.DeepEqual(left.Targets, right.Targets) &&
		left.ExpectedEpoch == right.ExpectedEpoch &&
		left.ObservationSeq == right.ObservationSeq &&
		left.Deadline == right.Deadline
}

func actionIndex(actions []ActionRecord, operationID string) int {
	for index := range actions {
		if actions[index].Invocation.OperationID == operationID {
			return index
		}
	}
	return -1
}
