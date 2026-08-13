package taskstate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
)

type ControlClient interface {
	Info(context.Context) (controlplane.ClientInfo, error)
	GetActor(context.Context, string, string, string) (controlplane.ActorView, error)
	GetController(context.Context, controlplane.ActorControlTarget) (controlplane.ControllerLease, error)
	GetObservation(context.Context, controlplane.ActorControlTarget) (host.ObservationEnvelope, error)
	GetOperation(context.Context, string) (controlplane.OperationView, error)
	SubmitAction(context.Context, controlplane.SubmitActionInput) (controlplane.OperationView, error)
}

type PlanClient interface {
	CreatePlan(context.Context, Draft) (PlanState, error)
	GetPlan(context.Context, string) (PlanState, error)
	WaitPlan(context.Context, WaitInput) (PlanUpdate, error)
	RevisePlan(context.Context, ReviseInput) (PlanState, error)
	SetPlanStatus(context.Context, StatusInput) (PlanState, error)
	RequestTransition(context.Context, TransitionInput) (PlanState, error)
	SubmitStepAction(context.Context, SubmitStepActionInput) (controlplane.OperationView, error)
}

type SubmitStepActionInput struct {
	Action       controlplane.SubmitActionInput `json:"action"`
	ConditionIDs []string                       `json:"condition_ids,omitempty"`
}

type Coordinator struct {
	store   *Store
	control ControlClient
}

func NewCoordinator(store *Store, control ControlClient) (*Coordinator, error) {
	if store == nil || control == nil {
		return nil, invalid("coordinator", "requires store and control client")
	}
	return &Coordinator{store: store, control: control}, nil
}

func (coordinator *Coordinator) CreatePlan(ctx context.Context, draft Draft) (PlanState, error) {
	if err := coordinator.authorizeDraft(ctx, draft, controlplane.ScopeActorControl); err != nil {
		return PlanState{}, err
	}
	return coordinator.store.Create(ctx, draft)
}

func (coordinator *Coordinator) GetPlan(ctx context.Context, planID string) (PlanState, error) {
	state, err := coordinator.store.Get(ctx, planID)
	if err != nil {
		return PlanState{}, err
	}
	if err := coordinator.authorizeRead(ctx, state); err != nil {
		return PlanState{}, err
	}
	return state, nil
}

func (coordinator *Coordinator) WaitPlan(ctx context.Context, input WaitInput) (PlanUpdate, error) {
	state, err := coordinator.GetPlan(ctx, input.PlanID)
	if err != nil {
		return PlanUpdate{}, err
	}
	if input.AfterRevision > state.Revision {
		return PlanUpdate{}, ErrInvalid
	}
	return coordinator.store.Wait(ctx, input)
}

func (coordinator *Coordinator) RevisePlan(ctx context.Context, input ReviseInput) (PlanState, error) {
	current, err := coordinator.store.Get(ctx, input.PlanID)
	if err != nil {
		return PlanState{}, err
	}
	input.Draft.PlanID = current.PlanID
	input.Draft.TaskID = current.TaskID
	input.Draft.SessionID = current.SessionID
	input.Draft.HostID = current.HostID
	input.Draft.WorldID = current.WorldID
	input.Draft.ActorID = current.ActorID
	input.Draft.ControllerID = current.ControllerID
	input.Draft.ControllerSource = current.ControllerSource
	input.Draft.PlanningMode = current.PlanningMode
	input.Draft.MaxReplans = current.MaxReplans
	if err := coordinator.authorizeDraft(ctx, input.Draft, controlplane.ScopeActorControl); err != nil {
		return PlanState{}, err
	}
	gate := ReplanInput{
		Reason: input.Reason, ConsecutiveFailures: current.ConsecutiveFailures,
		ReplanCount: current.ReplanCount,
	}
	switch input.Reason {
	case ReplanGoalChanged, ReplanManualAuthorized:
		gate.PlayerAuthorized = true
	case ReplanFailureThresholdReached:
		gate.HasAuthoritativeProof = current.Status == PlanBlocked
	default:
		return PlanState{}, fmt.Errorf("%w: replan reason requires explicit Host evidence", ErrForbidden)
	}
	if !ShouldReplan(ReplanPolicy{
		FailureThreshold: 3, MaxReplans: current.MaxReplans,
	}, gate) {
		return PlanState{}, fmt.Errorf("%w: deterministic replan gate is not satisfied", ErrConflict)
	}
	return coordinator.store.Revise(ctx, input)
}

func (coordinator *Coordinator) SetPlanStatus(ctx context.Context, input StatusInput) (PlanState, error) {
	state, err := coordinator.store.Get(ctx, input.PlanID)
	if err != nil {
		return PlanState{}, err
	}
	if err := coordinator.authorizeState(ctx, state, controlplane.ScopeActorControl, true); err != nil {
		return PlanState{}, err
	}
	return coordinator.store.SetStatus(ctx, input)
}

func (coordinator *Coordinator) RequestTransition(
	ctx context.Context,
	input TransitionInput,
) (PlanState, error) {
	state, err := coordinator.store.Get(ctx, input.PlanID)
	if err != nil {
		return PlanState{}, err
	}
	if state.Revision != input.ExpectedRevision {
		return PlanState{}, ErrConflict
	}
	if err := coordinator.authorizeState(ctx, state, controlplane.ScopeActorControl, true); err != nil {
		return PlanState{}, err
	}
	var evidence PlanEvidence
	switch input.Kind {
	case EvidenceOperationOutcome:
		operation, err := coordinator.control.GetOperation(ctx, input.EvidenceID)
		if err != nil {
			return PlanState{}, mapControlError(err)
		}
		if !operation.Terminal || !operation.ExecutionConfirmed || operation.Outcome == nil ||
			operation.Outcome.Status != host.ActionSucceeded || operation.ActionRequest == nil ||
			operation.ActionRequest.PlanStep == nil ||
			operation.ActionRequest.PlanStep.PlanID != state.PlanID {
			return PlanState{}, fmt.Errorf("%w: operation is not confirmed plan evidence", ErrInvalid)
		}
		payload, _ := json.Marshal(operation.Outcome)
		digest := sha256.Sum256(payload)
		evidence = PlanEvidence{
			EvidenceID:  operation.OperationID + "." + input.ConditionID,
			ConditionID: input.ConditionID, Kind: input.Kind, OperationID: operation.OperationID,
			Epoch: operation.Outcome.Epoch, ObservationSequence: operation.Outcome.WorldSeq,
			Digest:               hex.EncodeToString(digest[:]),
			RecordedAtUnixMillis: operation.UpdatedAt,
		}
	case EvidenceObservationFact:
		target := controlplane.ActorControlTarget{
			HostID: state.HostID, WorldID: state.WorldID, ActorID: state.ActorID,
		}
		observation, err := coordinator.control.GetObservation(ctx, target)
		if err != nil {
			return PlanState{}, mapControlError(err)
		}
		var fact *host.ObservationFact
		for index := range observation.Facts {
			if observation.Facts[index].FactID == input.EvidenceID {
				fact = &observation.Facts[index]
				break
			}
		}
		if fact == nil {
			return PlanState{}, ErrNotFound
		}
		payload, _ := json.Marshal(fact)
		digest := sha256.Sum256(payload)
		evidence = PlanEvidence{
			EvidenceID:  observation.ObservationID + "." + fact.FactID,
			ConditionID: input.ConditionID, Kind: input.Kind,
			Epoch: observation.Epoch, ObservationSequence: observation.Sequence,
			Digest:               hex.EncodeToString(digest[:]),
			RecordedAtUnixMillis: timepointMillis(observation.ObservedAt),
		}
	default:
		return PlanState{}, fmt.Errorf("%w: MCP transition accepts only Host-owned evidence", ErrForbidden)
	}
	return coordinator.store.ApplyTrustedEvidence(
		ctx, state.PlanID, state.Revision, evidence, "Verified plan evidence applied.",
	)
}

func (coordinator *Coordinator) SubmitStepAction(
	ctx context.Context,
	input SubmitStepActionInput,
) (controlplane.OperationView, error) {
	request := input.Action.Request
	if request.PlanStep == nil {
		return controlplane.OperationView{}, invalid("plan_step_ref", "is required")
	}
	state, err := coordinator.store.Get(ctx, request.PlanStep.PlanID)
	if err != nil {
		return controlplane.OperationView{}, err
	}
	if err := coordinator.authorizeState(ctx, state, controlplane.ScopeActorExecute, true); err != nil {
		return controlplane.OperationView{}, err
	}
	if state.Revision != request.PlanStep.PlanRevision ||
		state.CurrentStepID != request.PlanStep.StepID || state.Status != PlanActive ||
		state.TaskID != request.TaskID || state.HostID != input.Action.HostID ||
		state.WorldID != input.Action.WorldID || state.ActorID != request.ActorID ||
		state.ControllerID != request.ControllerID {
		return controlplane.OperationView{}, ErrConflict
	}
	link := OperationLink{
		PlanID: state.PlanID, PlanRevision: state.Revision, StepID: state.CurrentStepID,
		ConditionIDs: append([]string(nil), input.ConditionIDs...),
	}
	if err := validateLinkConditions(state, link); err != nil {
		return controlplane.OperationView{}, err
	}
	operation, err := coordinator.control.SubmitAction(ctx, input.Action)
	if err != nil {
		return controlplane.OperationView{}, err
	}
	link.OperationID = operation.OperationID
	if err := coordinator.store.LinkOperation(ctx, link); err != nil {
		return operation, fmt.Errorf("link submitted operation: %w", err)
	}
	return operation, nil
}

func (coordinator *Coordinator) authorizeRead(ctx context.Context, state PlanState) error {
	info, err := coordinator.control.Info(ctx)
	if err != nil {
		return err
	}
	if !principalHasScope(info.Principal, controlplane.ScopeActorRead) &&
		!principalHasScope(info.Principal, controlplane.ScopeHostAdmin) {
		return ErrForbidden
	}
	_, err = coordinator.control.GetActor(ctx, state.HostID, state.WorldID, state.ActorID)
	return mapControlError(err)
}

func (coordinator *Coordinator) authorizeDraft(
	ctx context.Context,
	draft Draft,
	scope string,
) error {
	state, err := NewPlan(draft, 0)
	if err != nil {
		return err
	}
	return coordinator.authorizeState(ctx, state, scope, true)
}

func (coordinator *Coordinator) authorizeState(
	ctx context.Context,
	state PlanState,
	scope string,
	requireController bool,
) error {
	info, err := coordinator.control.Info(ctx)
	if err != nil {
		return err
	}
	admin := principalHasScope(info.Principal, controlplane.ScopeHostAdmin)
	if !admin && !principalHasScope(info.Principal, scope) {
		return ErrForbidden
	}
	actor, err := coordinator.control.GetActor(ctx, state.HostID, state.WorldID, state.ActorID)
	if err != nil {
		return mapControlError(err)
	}
	if actor.Epoch != state.BasedOnEpoch || actor.ObservationSeq < state.BasedOnObservationSequence {
		return fmt.Errorf("%w: plan observation is stale", ErrConflict)
	}
	if !requireController {
		return nil
	}
	target := controlplane.ActorControlTarget{
		HostID: state.HostID, WorldID: state.WorldID, ActorID: state.ActorID,
	}
	lease, err := coordinator.control.GetController(ctx, target)
	if err != nil {
		return mapControlError(err)
	}
	if lease.ControllerID != state.ControllerID || lease.Epoch != state.BasedOnEpoch ||
		(!admin && lease.PrincipalID != info.Principal.ID) ||
		(state.ControllerSource == ControllerExternal && lease.Source != controlplane.DecisionExternal) ||
		(state.ControllerSource == ControllerInternal && lease.Source != controlplane.DecisionInternal) {
		return ErrForbidden
	}
	return nil
}

func principalHasScope(principal host.Principal, scope string) bool {
	for _, granted := range principal.GrantedScopes {
		if granted == scope {
			return true
		}
	}
	return false
}

func mapControlError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, controlplane.ErrForbidden) || errors.Is(err, controlplane.ErrNotFound) ||
		errors.Is(err, controlplane.ErrLeaseExpired) {
		return ErrForbidden
	}
	if errors.Is(err, controlplane.ErrStale) || errors.Is(err, controlplane.ErrLeaseConflict) {
		return ErrConflict
	}
	return err
}

func timepointMillis(point host.Timepoint) int64 {
	if point.Clock == host.ClockRealtime {
		return point.Value
	}
	return time.Now().UnixMilli()
}

var _ PlanClient = (*Coordinator)(nil)

// OutcomeSink reconciles and applies only action requests carrying PlanStepRef.
type OutcomeSink struct {
	store *Store
}

func NewOutcomeSink(store *Store) (*OutcomeSink, error) {
	if store == nil {
		return nil, invalid("outcome_sink", "requires a store")
	}
	return &OutcomeSink{store: store}, nil
}

func (sink *OutcomeSink) RecordOutcome(
	ctx context.Context,
	evidence controlplane.OutcomeEvidence,
) error {
	if evidence.PlanStep == nil {
		return nil
	}
	link := OperationLink{
		OperationID: evidence.OperationID, PlanID: evidence.PlanStep.PlanID,
		PlanRevision: evidence.PlanStep.PlanRevision, StepID: evidence.PlanStep.StepID,
	}
	if err := sink.store.ReconcileOperationLink(ctx, link); err != nil {
		return err
	}
	_, _, err := sink.store.ApplyOperationResult(ctx, OperationResult{
		OperationID: evidence.OperationID, ExecutionConfirmed: true, Outcome: evidence.Outcome,
	})
	return err
}

var _ controlplane.OutcomeSink = (*OutcomeSink)(nil)
