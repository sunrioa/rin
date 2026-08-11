// Package conformance provides a game-neutral Adapter contract suite. Game
// integrations supply scenarios; the suite owns the Rin binding, policy, and
// operation checks so adapters cannot replace them with game-specific rules.
package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/policy"
	"github.com/sunrioa/rin/sdk/hostkit"
)

// RequestBuilder constructs one controller-authored request from the current
// sealed catalog and Host snapshot.
type RequestBuilder func(
	host.CapabilitySnapshot,
	controlplane.ActionHostSnapshot,
	string,
	string,
) (host.ActionRequest, error)

// Scenario supplies only game-specific setup and controller intent. Every
// authorization and lifecycle assertion remains inside Run.
type Scenario struct {
	Adapter            hostkit.Adapter
	Target             hostkit.AdapterTarget
	Principal          host.Principal
	BuildAction        RequestBuilder
	BuildCancellable   RequestBuilder
	AdvanceObservation func()
	RestartHost        func()
	StateDigest        func() string
}

// Report records the portable behavior exercised by one conformance run.
type Report struct {
	CapabilityCount   int
	EffectCount       int
	IdempotentReplay  bool
	StaleRejected     bool
	RestartRejected   bool
	CancellationWorks bool
}

// Run executes the common Observe, Bind, Preview, Policy, Execute, Verify,
// cancellation, stale-state, restart, and idempotency contract.
func Run(ctx context.Context, scenario Scenario) (Report, error) {
	if ctx == nil {
		return Report{}, errors.New("conformance context is required")
	}
	if scenario.Adapter == nil || scenario.BuildAction == nil ||
		scenario.BuildCancellable == nil || scenario.AdvanceObservation == nil ||
		scenario.RestartHost == nil || scenario.StateDigest == nil {
		return Report{}, errors.New("conformance scenario is incomplete")
	}
	if err := host.ValidatePrincipal(scenario.Principal); err != nil {
		return Report{}, fmt.Errorf("validate conformance principal: %w", err)
	}
	dispatcher := &inlineDispatcher{}
	coordinator, err := hostkit.NewAdapterCoordinator(
		ctx,
		scenario.Adapter,
		dispatcher,
	)
	if err != nil {
		return Report{}, err
	}
	catalog := coordinator.Capabilities()
	if catalog.Revision == 0 || len(catalog.Specs) == 0 {
		return Report{}, errors.New("adapter returned an empty sealed catalog")
	}
	target := controlplane.ActorControlTarget{
		HostID: scenario.Target.HostID, WorldID: scenario.Target.WorldID,
		ActorID: scenario.Target.ActorID,
	}
	snapshot, err := coordinator.SnapshotAction(ctx, target)
	if err != nil {
		return Report{}, err
	}
	observation, err := coordinator.Observe(ctx, host.ObservationQuery{
		QueryID:       "query.conformance.initial",
		HostID:        target.HostID,
		WorldID:       target.WorldID,
		ActorID:       target.ActorID,
		ExpectedEpoch: snapshot.Epoch,
		Limit:         128,
	})
	if err != nil {
		return Report{}, err
	}
	if observation.Sequence != snapshot.ObservationSeq {
		return Report{}, errors.New("observation and execution snapshot sequences differ")
	}
	facts, err := coordinator.PolicyFacts(ctx, scenario.Target)
	if err != nil {
		return Report{}, err
	}

	report := Report{CapabilityCount: len(catalog.Specs)}
	request, err := scenario.BuildAction(
		catalog,
		snapshot,
		"request.conformance.execute",
		"action.conformance.execute",
	)
	if err != nil {
		return Report{}, fmt.Errorf("build conformance action: %w", err)
	}
	binding, delivery, err := prepareDelivery(
		ctx,
		coordinator,
		target,
		scenario.Principal,
		facts,
		request,
		"operation.conformance.execute",
	)
	if err != nil {
		return Report{}, err
	}
	report.EffectCount = len(binding.Action.Effects)
	before := scenario.StateDigest()
	result, err := coordinator.ExecuteDelivery(ctx, delivery)
	if err != nil {
		return Report{}, err
	}
	if result.Run.Status != host.ActionSucceeded || result.Outcome == nil {
		return Report{}, fmt.Errorf("conformance action did not succeed: %#v", result)
	}
	after := scenario.StateDigest()
	if before == after {
		return Report{}, errors.New("conformance action produced no observable game change")
	}
	replayed, err := coordinator.ExecuteDelivery(ctx, delivery)
	if err != nil {
		return Report{}, fmt.Errorf("replay conformance action: %w", err)
	}
	if !sameJSON(result, replayed) || scenario.StateDigest() != after {
		return Report{}, errors.New("Operation replay changed result or game state")
	}
	report.IdempotentReplay = true
	coordinator.ForgetOperation(delivery.Request.OperationID)

	current, err := coordinator.SnapshotAction(ctx, target)
	if err != nil {
		return Report{}, err
	}
	staleRequest, err := scenario.BuildAction(
		catalog,
		current,
		"request.conformance.stale",
		"action.conformance.stale",
	)
	if err != nil {
		return Report{}, err
	}
	_, staleDelivery, err := prepareDelivery(
		ctx,
		coordinator,
		target,
		scenario.Principal,
		facts,
		staleRequest,
		"operation.conformance.stale",
	)
	if err != nil {
		return Report{}, err
	}
	staleState := scenario.StateDigest()
	scenario.AdvanceObservation()
	if _, err := coordinator.ExecuteDelivery(ctx, staleDelivery); err == nil {
		return Report{}, errors.New("adapter accepted a stale observation binding")
	}
	if scenario.StateDigest() == staleState {
		return Report{}, errors.New("AdvanceObservation did not advance adapter state")
	}
	report.StaleRejected = true

	current, err = coordinator.SnapshotAction(ctx, target)
	if err != nil {
		return Report{}, err
	}
	restartRequest, err := scenario.BuildAction(
		catalog,
		current,
		"request.conformance.restart",
		"action.conformance.restart",
	)
	if err != nil {
		return Report{}, err
	}
	_, restartDelivery, err := prepareDelivery(
		ctx,
		coordinator,
		target,
		scenario.Principal,
		facts,
		restartRequest,
		"operation.conformance.restart",
	)
	if err != nil {
		return Report{}, err
	}
	scenario.RestartHost()
	if _, err := coordinator.ExecuteDelivery(ctx, restartDelivery); err == nil {
		return Report{}, errors.New("adapter accepted an action from a previous Host epoch")
	}
	report.RestartRejected = true

	current, err = coordinator.SnapshotAction(ctx, target)
	if err != nil {
		return Report{}, err
	}
	cancelRequest, err := scenario.BuildCancellable(
		catalog,
		current,
		"request.conformance.cancel",
		"action.conformance.cancel",
	)
	if err != nil {
		return Report{}, err
	}
	_, cancelDelivery, err := prepareDelivery(
		ctx,
		coordinator,
		target,
		scenario.Principal,
		facts,
		cancelRequest,
		"operation.conformance.cancel",
	)
	if err != nil {
		return Report{}, err
	}
	running, err := coordinator.ExecuteDelivery(ctx, cancelDelivery)
	if err != nil {
		return Report{}, err
	}
	if running.Run.Status != host.ActionRunning || running.Outcome != nil {
		return Report{}, fmt.Errorf("cancellable action did not remain running: %#v", running)
	}
	verified, err := coordinator.VerifyOperation(
		ctx,
		cancelDelivery.Request.OperationID,
	)
	if err != nil {
		return Report{}, err
	}
	if verified.Run.Status != host.ActionRunning ||
		verified.Run.ProgressSeq < running.Run.ProgressSeq {
		return Report{}, errors.New("adapter verification returned invalid progress")
	}
	cancelled, err := coordinator.CancelOperation(
		ctx,
		cancelDelivery.Request.OperationID,
	)
	if err != nil {
		return Report{}, err
	}
	if cancelled.Run.Status != host.ActionCancelled || cancelled.Outcome == nil {
		return Report{}, fmt.Errorf("adapter cancellation is not authoritative: %#v", cancelled)
	}
	report.CancellationWorks = true
	coordinator.ForgetOperation(cancelDelivery.Request.OperationID)

	if !dispatcher.used {
		return Report{}, errors.New("adapter conformance never used the authority dispatcher")
	}
	return report, nil
}

type inlineDispatcher struct {
	used bool
}

func (dispatcher *inlineDispatcher) Dispatch(
	ctx context.Context,
	work func(context.Context) error,
) error {
	dispatcher.used = true
	return work(ctx)
}

func prepareDelivery(
	ctx context.Context,
	coordinator *hostkit.AdapterCoordinator,
	target controlplane.ActorControlTarget,
	principal host.Principal,
	facts hostkit.AdapterPolicyFacts,
	request host.ActionRequest,
	operationID string,
) (controlplane.ActionBindingResult, controlplane.HostControlDelivery, error) {
	binding, err := coordinator.BindAction(ctx, target, request)
	if err != nil {
		return controlplane.ActionBindingResult{}, controlplane.HostControlDelivery{}, err
	}
	for _, effect := range binding.Action.Effects {
		if !slices.Contains(facts.KnownEffectKinds, effect.Kind) ||
			!slices.Contains(facts.KnownScopes, effect.Scope) {
			return controlplane.ActionBindingResult{}, controlplane.HostControlDelivery{},
				errors.New("effect preview is absent from adapter PolicyFacts")
		}
	}
	engine, err := policy.New(policy.Config{
		Revision:           1,
		Profile:            policy.ProfileOpen,
		KnownEffectKinds:   append([]string(nil), facts.KnownEffectKinds...),
		KnownScopes:        append([]string(nil), facts.KnownScopes...),
		ConfirmationTTL:    confirmationDurations(binding.Snapshot.Now.Clock, 10),
		ConfirmationScopes: []string{"rin.policy.confirm"},
	})
	if err != nil {
		return controlplane.ActionBindingResult{}, controlplane.HostControlDelivery{}, err
	}
	decision, err := engine.Evaluate(binding.Action, policy.Context{
		Now:          binding.Snapshot.Now,
		CurrentEpoch: binding.Snapshot.Epoch,
		Principal:    principal,
		ServerID:     target.HostID,
	})
	if err != nil {
		return controlplane.ActionBindingResult{}, controlplane.HostControlDelivery{}, err
	}
	if decision.Result != policy.Allow {
		return controlplane.ActionBindingResult{}, controlplane.HostControlDelivery{},
			fmt.Errorf("conformance action was not allowed: %s", decision.ReasonCode)
	}
	delivery := controlplane.HostControlDelivery{
		DeliveryAttempt: 1,
		Request: controlplane.HostControlRequest{
			OperationID: operationID,
			RequestID:   request.RequestID,
			Principal:   principal,
			HostID:      target.HostID,
			WorldID:     target.WorldID,
			ActorID:     target.ActorID,
			Kind:        controlplane.ControlAction,
			Binding: &controlplane.ControlBinding{
				Epoch:             binding.Snapshot.Epoch,
				ObservationSeq:    binding.Snapshot.ObservationSeq,
				AuthorityRevision: 1,
				ControllerLeaseID: "lease.conformance.one",
			},
			ActionRequest:  actionRequestPointer(request),
			BoundAction:    boundActionPointer(binding.Action),
			PolicyDecision: decisionPointer(decision),
			SubmittedAt:    1,
		},
	}
	return binding, delivery, nil
}

func confirmationDurations(
	clock host.ClockMode,
	value uint64,
) policy.ConfirmationDurations {
	result := policy.ConfirmationDurations{}
	switch clock {
	case host.ClockEvent:
		result.Event = value
	case host.ClockStep:
		result.Step = value
	case host.ClockRealtime:
		result.Realtime = value
	}
	return result
}

func actionRequestPointer(value host.ActionRequest) *host.ActionRequest {
	value.Arguments = append(json.RawMessage(nil), value.Arguments...)
	value.Targets = append([]host.HostRef(nil), value.Targets...)
	return &value
}

func boundActionPointer(value host.BoundAction) *host.BoundAction {
	payload, _ := json.Marshal(value)
	var cloned host.BoundAction
	_ = json.Unmarshal(payload, &cloned)
	return &cloned
}

func decisionPointer(value policy.Decision) *policy.Decision {
	payload, _ := json.Marshal(value)
	var cloned policy.Decision
	_ = json.Unmarshal(payload, &cloned)
	return &cloned
}

func sameJSON(left, right any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}
