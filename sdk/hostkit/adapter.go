package hostkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
)

const maxAdapterOperations = 4_096

// AdapterTarget identifies one game-owned Actor without exposing an engine
// object to Rin or a controller.
type AdapterTarget struct {
	HostID  string
	WorldID string
	ActorID string
}

// AdapterSnapshot is the authoritative clock and timeline state used for
// binding and final execution checks.
type AdapterSnapshot struct {
	Now            host.Timepoint
	Epoch          host.Epoch
	ObservationSeq uint64
}

// AdapterBinding contains only immutable references resolved by the game.
// Effects are produced separately by Preview so an adapter cannot accidentally
// treat controller-authored fields as an authorization decision.
type AdapterBinding struct {
	BindingID       string
	ResolvedTargets []host.HostRef
	ValidUntil      host.Timepoint
}

// AdapterOperation is the exact, policy-approved operation given to the game
// executor. Principal scopes originate from the Host delivery, never a model.
type AdapterOperation struct {
	OperationID string
	Target      AdapterTarget
	Principal   host.Principal
	Action      host.BoundAction
}

// AdapterResult is one authoritative execution, cancellation, or verification
// result. Terminal runs require an Outcome; successful runs require Output.
type AdapterResult struct {
	Run     host.ActionRun
	Outcome *host.ActionOutcome
	Output  json.RawMessage
}

// AdapterPolicyFacts declares the standardized effect kinds and scopes an
// adapter can author. It is configuration input, not an authorization result.
type AdapterPolicyFacts struct {
	KnownEffectKinds []string
	KnownScopes      []string
}

// Adapter is the engine-neutral V2 boundary implemented by a game integration.
// Every method that can inspect or mutate game state is invoked through the
// configured AuthorityDispatcher.
type Adapter interface {
	Manifest() host.HostManifest
	Snapshot(context.Context, AdapterTarget) (AdapterSnapshot, error)
	Observe(context.Context, host.ObservationQuery) (host.ObservationEnvelope, error)
	ListCapabilities(context.Context) ([]host.CapabilitySpec, error)
	Bind(context.Context, AdapterTarget, host.ActionRequest) (AdapterBinding, error)
	Preview(
		context.Context,
		AdapterTarget,
		host.ActionRequest,
		AdapterBinding,
	) ([]host.Effect, error)
	Execute(context.Context, AdapterOperation) (AdapterResult, error)
	Cancel(context.Context, AdapterOperation) (AdapterResult, error)
	Verify(context.Context, AdapterOperation) (AdapterResult, error)
	PolicyFacts(context.Context, AdapterTarget) (AdapterPolicyFacts, error)
}

type adapterOperationState struct {
	operation AdapterOperation
	result    AdapterResult
}

// AdapterCoordinator seals adapter bindings with the Host Registry and owns
// the final authority-thread execution gate. It implements
// controlplane.ActionHost for embedded Control Plane deployments.
type AdapterCoordinator struct {
	adapter    Adapter
	dispatcher AuthorityDispatcher
	registry   *host.Registry
	manifest   host.HostManifest

	mu         sync.Mutex
	operations map[string]adapterOperationState
}

// NewAdapterCoordinator validates an adapter and seals its immutable V2
// capability catalog before the adapter can receive work.
func NewAdapterCoordinator(
	ctx context.Context,
	adapter Adapter,
	dispatcher AuthorityDispatcher,
) (*AdapterCoordinator, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}
	if adapter == nil || dispatcher == nil {
		return nil, errors.New("adapter and authority dispatcher are required")
	}
	manifest := cloneHostManifest(adapter.Manifest())
	registry, err := host.NewRegistry(manifest)
	if err != nil {
		return nil, fmt.Errorf("validate adapter manifest: %w", err)
	}
	var specs []host.CapabilitySpec
	if err := dispatcher.Dispatch(ctx, func(authorityContext context.Context) error {
		listed, listErr := adapter.ListCapabilities(authorityContext)
		if listErr != nil {
			return listErr
		}
		specs = cloneCapabilitySpecs(listed)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("list adapter capabilities: %w", err)
	}
	if len(specs) == 0 {
		return nil, errors.New("adapter must publish at least one capability")
	}
	if len(specs) > 512 {
		return nil, errors.New("adapter publishes more than 512 capabilities")
	}
	for index, spec := range specs {
		if _, err := registry.RegisterSpec(spec); err != nil {
			return nil, fmt.Errorf("register adapter capability %d: %w", index, err)
		}
	}
	return &AdapterCoordinator{
		adapter:    adapter,
		dispatcher: dispatcher,
		registry:   registry,
		manifest:   manifest,
		operations: make(map[string]adapterOperationState),
	}, nil
}

// Manifest returns the validated immutable adapter manifest.
func (coordinator *AdapterCoordinator) Manifest() host.HostManifest {
	return cloneHostManifest(coordinator.manifest)
}

// Capabilities returns the sealed, deterministically ordered V2 catalog.
func (coordinator *AdapterCoordinator) Capabilities() host.CapabilitySnapshot {
	return coordinator.registry.SnapshotSpecs()
}

// Observe invokes the adapter on its authority thread and verifies that the
// response belongs to the exact requested Host, World, Actor, and Epoch.
func (coordinator *AdapterCoordinator) Observe(
	ctx context.Context,
	query host.ObservationQuery,
) (host.ObservationEnvelope, error) {
	if err := requireContext(ctx); err != nil {
		return host.ObservationEnvelope{}, err
	}
	if err := host.ValidateObservationQuery(query); err != nil {
		return host.ObservationEnvelope{}, err
	}
	var observation host.ObservationEnvelope
	err := coordinator.dispatcher.Dispatch(
		ctx,
		func(authorityContext context.Context) error {
			value, observeErr := coordinator.adapter.Observe(authorityContext, query)
			if observeErr != nil {
				return observeErr
			}
			observation = cloneObservation(value)
			return nil
		},
	)
	if err != nil {
		return host.ObservationEnvelope{}, fmt.Errorf("observe adapter: %w", err)
	}
	if err := host.ValidateObservationEnvelope(observation); err != nil {
		return host.ObservationEnvelope{}, fmt.Errorf("validate adapter observation: %w", err)
	}
	if observation.HostID != query.HostID || observation.WorldID != query.WorldID ||
		observation.ActorID != query.ActorID || observation.Epoch != query.ExpectedEpoch {
		return host.ObservationEnvelope{}, errors.New("adapter observation does not match query authority")
	}
	if observation.Sequence < query.AfterSequence {
		return host.ObservationEnvelope{}, errors.New("adapter observation sequence moved backwards")
	}
	return observation, nil
}

// BindAction implements controlplane.ActionHost. Snapshot, target resolution,
// effect preview, and Registry sealing all happen in one authority dispatch.
func (coordinator *AdapterCoordinator) BindAction(
	ctx context.Context,
	target controlplane.ActorControlTarget,
	request host.ActionRequest,
) (controlplane.ActionBindingResult, error) {
	if err := requireContext(ctx); err != nil {
		return controlplane.ActionBindingResult{}, err
	}
	adapterTarget, err := makeAdapterTarget(target, request.ActorID)
	if err != nil {
		return controlplane.ActionBindingResult{}, err
	}
	var result controlplane.ActionBindingResult
	err = coordinator.dispatcher.Dispatch(
		ctx,
		func(authorityContext context.Context) error {
			snapshot, snapshotErr := coordinator.adapter.Snapshot(
				authorityContext,
				adapterTarget,
			)
			if snapshotErr != nil {
				return snapshotErr
			}
			if err := validateAdapterSnapshot(snapshot, adapterTarget); err != nil {
				return err
			}
			binding, bindErr := coordinator.adapter.Bind(
				authorityContext,
				adapterTarget,
				cloneActionRequestV2(request),
			)
			if bindErr != nil {
				return bindErr
			}
			effects, previewErr := coordinator.adapter.Preview(
				authorityContext,
				adapterTarget,
				cloneActionRequestV2(request),
				cloneAdapterBinding(binding),
			)
			if previewErr != nil {
				return previewErr
			}
			action, sealErr := coordinator.registry.SealBinding(
				request,
				host.BindingDraft{
					BindingID:       binding.BindingID,
					ResolvedTargets: cloneHostRefs(binding.ResolvedTargets),
					Effects:         cloneEffectsV2(effects),
					ValidUntil:      binding.ValidUntil,
				},
				snapshot.Now,
				snapshot.Epoch,
				snapshot.ObservationSeq,
			)
			if sealErr != nil {
				return sealErr
			}
			result = controlplane.ActionBindingResult{
				Action: action,
				Snapshot: controlplane.ActionHostSnapshot{
					Now:            snapshot.Now,
					Epoch:          snapshot.Epoch,
					ObservationSeq: snapshot.ObservationSeq,
				},
			}
			return nil
		},
	)
	if err != nil {
		return controlplane.ActionBindingResult{}, fmt.Errorf("bind adapter action: %w", err)
	}
	return result, nil
}

// SnapshotAction implements controlplane.ActionHost using the same authority
// dispatch as binding and execution.
func (coordinator *AdapterCoordinator) SnapshotAction(
	ctx context.Context,
	target controlplane.ActorControlTarget,
) (controlplane.ActionHostSnapshot, error) {
	if err := requireContext(ctx); err != nil {
		return controlplane.ActionHostSnapshot{}, err
	}
	adapterTarget, err := makeAdapterTarget(target, target.ActorID)
	if err != nil {
		return controlplane.ActionHostSnapshot{}, err
	}
	var snapshot AdapterSnapshot
	err = coordinator.dispatcher.Dispatch(
		ctx,
		func(authorityContext context.Context) error {
			value, snapshotErr := coordinator.adapter.Snapshot(
				authorityContext,
				adapterTarget,
			)
			if snapshotErr != nil {
				return snapshotErr
			}
			snapshot = value
			return nil
		},
	)
	if err != nil {
		return controlplane.ActionHostSnapshot{}, fmt.Errorf("snapshot adapter: %w", err)
	}
	if err := validateAdapterSnapshot(snapshot, adapterTarget); err != nil {
		return controlplane.ActionHostSnapshot{}, err
	}
	return controlplane.ActionHostSnapshot{
		Now:            snapshot.Now,
		Epoch:          snapshot.Epoch,
		ObservationSeq: snapshot.ObservationSeq,
	}, nil
}

// ExecuteDelivery validates one complete Control Plane delivery, repeats the
// local TOCTOU gate, and invokes the adapter exactly once per Operation ID.
func (coordinator *AdapterCoordinator) ExecuteDelivery(
	ctx context.Context,
	delivery controlplane.HostControlDelivery,
) (AdapterResult, error) {
	if err := requireContext(ctx); err != nil {
		return AdapterResult{}, err
	}
	if err := controlplane.ValidateActionDelivery(delivery.Request); err != nil {
		return AdapterResult{}, fmt.Errorf("validate action delivery: %w", err)
	}
	request := delivery.Request
	operation := AdapterOperation{
		OperationID: request.OperationID,
		Target: AdapterTarget{
			HostID:  request.HostID,
			WorldID: request.WorldID,
			ActorID: request.ActorID,
		},
		Principal: clonePrincipalV2(request.Principal),
		Action:    cloneBoundActionV2(*request.BoundAction),
	}

	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if current, exists := coordinator.operations[operation.OperationID]; exists {
		if !sameAdapterOperation(current.operation, operation) {
			return AdapterResult{}, errors.New("operation ID was reused for a different bound action")
		}
		return cloneAdapterResult(current.result), nil
	}
	if len(coordinator.operations) >= maxAdapterOperations {
		return AdapterResult{}, errors.New("adapter operation cache is full")
	}

	var result AdapterResult
	err := coordinator.dispatcher.Dispatch(
		ctx,
		func(authorityContext context.Context) error {
			snapshot, snapshotErr := coordinator.adapter.Snapshot(
				authorityContext,
				operation.Target,
			)
			if snapshotErr != nil {
				return snapshotErr
			}
			if err := validateAdapterSnapshot(snapshot, operation.Target); err != nil {
				return err
			}
			if err := coordinator.registry.AuthorizeBoundAction(
				operation.Action,
				snapshot.Now,
				snapshot.Epoch,
				snapshot.ObservationSeq,
				operation.Principal,
			); err != nil {
				return err
			}
			value, executeErr := coordinator.adapter.Execute(
				authorityContext,
				cloneAdapterOperation(operation),
			)
			if executeErr != nil {
				return executeErr
			}
			result = cloneAdapterResult(value)
			return coordinator.validateResult(operation, result)
		},
	)
	if err != nil {
		return AdapterResult{}, fmt.Errorf("execute adapter action: %w", err)
	}
	coordinator.operations[operation.OperationID] = adapterOperationState{
		operation: cloneAdapterOperation(operation),
		result:    cloneAdapterResult(result),
	}
	return cloneAdapterResult(result), nil
}

// VerifyOperation asks the adapter for newer authoritative progress without
// executing the BoundAction again.
func (coordinator *AdapterCoordinator) VerifyOperation(
	ctx context.Context,
	operationID string,
) (AdapterResult, error) {
	return coordinator.updateOperation(ctx, operationID, coordinator.adapter.Verify)
}

// CancelOperation requests adapter cancellation for a previously accepted
// operation. Unsupported cancellation remains an explicit error.
func (coordinator *AdapterCoordinator) CancelOperation(
	ctx context.Context,
	operationID string,
) (AdapterResult, error) {
	if err := requireContext(ctx); err != nil {
		return AdapterResult{}, err
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	state, exists := coordinator.operations[operationID]
	if !exists {
		return AdapterResult{}, errors.New("adapter operation is not known")
	}
	spec, exists := coordinator.registry.ResolveSpec(state.operation.Action.Capability)
	if !exists || spec.Digest != state.operation.Action.SpecDigest {
		return AdapterResult{}, errors.New("adapter operation capability was revoked")
	}
	if spec.Cancellation == host.CancellationUnsupported {
		return AdapterResult{}, errors.New("adapter operation does not support cancellation")
	}
	var result AdapterResult
	err := coordinator.dispatcher.Dispatch(
		ctx,
		func(authorityContext context.Context) error {
			value, cancelErr := coordinator.adapter.Cancel(
				authorityContext,
				cloneAdapterOperation(state.operation),
			)
			if cancelErr != nil {
				return cancelErr
			}
			result = cloneAdapterResult(value)
			return coordinator.validateResult(state.operation, result)
		},
	)
	if err != nil {
		return AdapterResult{}, fmt.Errorf("cancel adapter action: %w", err)
	}
	if !validAdapterTransition(state.result.Run, result.Run) {
		return AdapterResult{}, errors.New("adapter cancellation progress moved backwards")
	}
	state.result = cloneAdapterResult(result)
	coordinator.operations[operationID] = state
	return cloneAdapterResult(result), nil
}

// ForgetOperation releases one terminal cache entry after its authoritative
// result has been durably acknowledged by the Control Plane.
func (coordinator *AdapterCoordinator) ForgetOperation(operationID string) bool {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	state, exists := coordinator.operations[operationID]
	if !exists || !terminalAdapterStatus(state.result.Run.Status) {
		return false
	}
	delete(coordinator.operations, operationID)
	return true
}

// PolicyFacts returns adapter-authored policy vocabulary after normalization.
func (coordinator *AdapterCoordinator) PolicyFacts(
	ctx context.Context,
	target AdapterTarget,
) (AdapterPolicyFacts, error) {
	if err := requireContext(ctx); err != nil {
		return AdapterPolicyFacts{}, err
	}
	var facts AdapterPolicyFacts
	err := coordinator.dispatcher.Dispatch(
		ctx,
		func(authorityContext context.Context) error {
			value, factsErr := coordinator.adapter.PolicyFacts(authorityContext, target)
			if factsErr != nil {
				return factsErr
			}
			facts = value
			return nil
		},
	)
	if err != nil {
		return AdapterPolicyFacts{}, fmt.Errorf("read adapter policy facts: %w", err)
	}
	if err := normalizePolicyFacts(&facts); err != nil {
		return AdapterPolicyFacts{}, err
	}
	return facts, nil
}

func (coordinator *AdapterCoordinator) updateOperation(
	ctx context.Context,
	operationID string,
	update func(context.Context, AdapterOperation) (AdapterResult, error),
) (AdapterResult, error) {
	if err := requireContext(ctx); err != nil {
		return AdapterResult{}, err
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	state, exists := coordinator.operations[operationID]
	if !exists {
		return AdapterResult{}, errors.New("adapter operation is not known")
	}
	var result AdapterResult
	err := coordinator.dispatcher.Dispatch(
		ctx,
		func(authorityContext context.Context) error {
			value, updateErr := update(
				authorityContext,
				cloneAdapterOperation(state.operation),
			)
			if updateErr != nil {
				return updateErr
			}
			result = cloneAdapterResult(value)
			return coordinator.validateResult(state.operation, result)
		},
	)
	if err != nil {
		return AdapterResult{}, fmt.Errorf("verify adapter action: %w", err)
	}
	if !validAdapterTransition(state.result.Run, result.Run) {
		return AdapterResult{}, errors.New("adapter operation progress moved backwards")
	}
	state.result = cloneAdapterResult(result)
	coordinator.operations[operationID] = state
	return cloneAdapterResult(result), nil
}

func (coordinator *AdapterCoordinator) validateResult(
	operation AdapterOperation,
	result AdapterResult,
) error {
	if err := host.ValidateActionRun(result.Run); err != nil {
		return fmt.Errorf("validate adapter run: %w", err)
	}
	if result.Run.OperationID != operation.OperationID {
		return errors.New("adapter run operation ID does not match delivery")
	}
	terminal := terminalAdapterStatus(result.Run.Status)
	if terminal != (result.Outcome != nil) {
		return errors.New("terminal adapter run and Outcome presence do not match")
	}
	if result.Outcome != nil {
		if err := host.ValidateActionOutcome(*result.Outcome); err != nil {
			return fmt.Errorf("validate adapter outcome: %w", err)
		}
		if result.Outcome.OperationID != operation.OperationID ||
			result.Outcome.Status != result.Run.Status {
			return errors.New("adapter Outcome does not match run")
		}
		if result.Outcome.Epoch != operation.Action.ExpectedEpoch ||
			result.Outcome.WorldSeq < operation.Action.ObservationSeq {
			return errors.New("adapter Outcome does not match bound timeline")
		}
	}
	if !terminal && len(result.Output) != 0 {
		return errors.New("non-terminal adapter result contains Output")
	}
	if result.Run.Status == host.ActionSucceeded && len(result.Output) == 0 {
		return errors.New("successful adapter result has no structured Output")
	}
	if len(result.Output) != 0 {
		if err := coordinator.registry.ValidateSpecOutput(
			operation.Action.Capability,
			operation.Action.SpecDigest,
			result.Output,
		); err != nil {
			return fmt.Errorf("validate adapter Output: %w", err)
		}
	}
	return nil
}

func makeAdapterTarget(
	target controlplane.ActorControlTarget,
	actorID string,
) (AdapterTarget, error) {
	if target.HostID == "" || target.WorldID == "" || target.ActorID == "" {
		return AdapterTarget{}, errors.New("adapter target identifiers are required")
	}
	if target.ActorID != actorID {
		return AdapterTarget{}, errors.New("adapter target does not match action actor")
	}
	return AdapterTarget{
		HostID: target.HostID, WorldID: target.WorldID, ActorID: target.ActorID,
	}, nil
}

func validateAdapterSnapshot(snapshot AdapterSnapshot, target AdapterTarget) error {
	if err := snapshot.Now.Validate("snapshot.now"); err != nil {
		return err
	}
	if err := snapshot.Epoch.Validate("snapshot.epoch"); err != nil {
		return err
	}
	if snapshot.Epoch.WorldID != target.WorldID {
		return errors.New("adapter snapshot belongs to another world")
	}
	if snapshot.ObservationSeq == 0 {
		return errors.New("adapter snapshot observation sequence is required")
	}
	return nil
}

func normalizePolicyFacts(facts *AdapterPolicyFacts) error {
	if len(facts.KnownEffectKinds) == 0 || len(facts.KnownEffectKinds) > 512 {
		return errors.New("adapter policy facts require 1 to 512 effect kinds")
	}
	if len(facts.KnownScopes) == 0 || len(facts.KnownScopes) > 512 {
		return errors.New("adapter policy facts require 1 to 512 scopes")
	}
	slices.Sort(facts.KnownEffectKinds)
	slices.Sort(facts.KnownScopes)
	if len(slices.Compact(facts.KnownEffectKinds)) != len(facts.KnownEffectKinds) ||
		len(slices.Compact(facts.KnownScopes)) != len(facts.KnownScopes) {
		return errors.New("adapter policy facts contain duplicates")
	}
	for _, value := range append(
		append([]string(nil), facts.KnownEffectKinds...),
		facts.KnownScopes...,
	) {
		if value == "" || len(value) > 128 {
			return errors.New("adapter policy fact identifiers must contain 1 to 128 bytes")
		}
	}
	return nil
}

func terminalAdapterStatus(status host.ActionRunStatus) bool {
	return status == host.ActionSucceeded || status == host.ActionFailed ||
		status == host.ActionCancelled || status == host.ActionInterrupted ||
		status == host.ActionStale || status == host.ActionOutcomeUnknown
}

func validAdapterTransition(previous, next host.ActionRun) bool {
	if next.ProgressSeq < previous.ProgressSeq || next.Progress < previous.Progress ||
		next.UpdatedAt.Clock != previous.UpdatedAt.Clock ||
		next.UpdatedAt.Value < previous.UpdatedAt.Value {
		return false
	}
	if next.ProgressSeq == previous.ProgressSeq {
		return sameAdapterRun(previous, next)
	}
	return next.Status == previous.Status ||
		host.CanTransitionActionRun(previous.Status, next.Status)
}

func sameAdapterRun(left, right host.ActionRun) bool {
	return left == right
}

func sameAdapterOperation(left, right AdapterOperation) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}

func cloneAdapterBinding(value AdapterBinding) AdapterBinding {
	value.ResolvedTargets = cloneHostRefs(value.ResolvedTargets)
	return value
}

func cloneAdapterOperation(value AdapterOperation) AdapterOperation {
	value.Principal = clonePrincipalV2(value.Principal)
	value.Action = cloneBoundActionV2(value.Action)
	return value
}

func cloneAdapterResult(value AdapterResult) AdapterResult {
	value.Output = append(json.RawMessage(nil), value.Output...)
	if value.Outcome != nil {
		outcome := *value.Outcome
		outcome.Evidence = cloneHostRefs(value.Outcome.Evidence)
		value.Outcome = &outcome
	}
	return value
}

func cloneActionRequestV2(value host.ActionRequest) host.ActionRequest {
	value.Arguments = append(json.RawMessage(nil), value.Arguments...)
	value.Targets = cloneHostRefs(value.Targets)
	return value
}

func cloneBoundActionV2(value host.BoundAction) host.BoundAction {
	value.NormalizedArguments = append(json.RawMessage(nil), value.NormalizedArguments...)
	value.RequestedTargets = cloneHostRefs(value.RequestedTargets)
	value.ResolvedTargets = cloneHostRefs(value.ResolvedTargets)
	value.Effects = cloneEffectsV2(value.Effects)
	return value
}

func cloneEffectsV2(values []host.Effect) []host.Effect {
	cloned := make([]host.Effect, len(values))
	for index, value := range values {
		cloned[index] = value
		cloned[index].Tags = append([]string(nil), value.Tags...)
		cloned[index].Attributes = append(json.RawMessage(nil), value.Attributes...)
		if value.Subject != nil {
			subject := *value.Subject
			cloned[index].Subject = &subject
		}
		if value.Target != nil {
			target := *value.Target
			cloned[index].Target = &target
		}
	}
	return cloned
}

func cloneHostRefs(values []host.HostRef) []host.HostRef {
	return append([]host.HostRef(nil), values...)
}

func clonePrincipalV2(value host.Principal) host.Principal {
	value.GrantedScopes = append([]string(nil), value.GrantedScopes...)
	return value
}

func cloneCapabilitySpecs(values []host.CapabilitySpec) []host.CapabilitySpec {
	cloned := make([]host.CapabilitySpec, len(values))
	for index, value := range values {
		cloned[index] = value
		cloned[index].RequiredScopes = append([]string(nil), value.RequiredScopes...)
		cloned[index].Input.Document = append(json.RawMessage(nil), value.Input.Document...)
		cloned[index].Output.Document = append(json.RawMessage(nil), value.Output.Document...)
		cloned[index].EffectSchema.Document = append(
			json.RawMessage(nil), value.EffectSchema.Document...,
		)
	}
	return cloned
}

func cloneHostManifest(value host.HostManifest) host.HostManifest {
	value.ClockModes = append([]host.ClockMode(nil), value.ClockModes...)
	value.DecisionModes = append([]host.DecisionMode(nil), value.DecisionModes...)
	return value
}

func cloneObservation(value host.ObservationEnvelope) host.ObservationEnvelope {
	value.Payload = append(json.RawMessage(nil), value.Payload...)
	value.Facts = append([]host.ObservationFact(nil), value.Facts...)
	for index := range value.Facts {
		value.Facts[index].Tags = append([]string(nil), value.Facts[index].Tags...)
		value.Facts[index].Value = append(json.RawMessage(nil), value.Facts[index].Value...)
		if value.Facts[index].Subject != nil {
			subject := *value.Facts[index].Subject
			value.Facts[index].Subject = &subject
		}
	}
	value.Resources = append([]host.ObservationResource(nil), value.Resources...)
	for index := range value.Resources {
		value.Resources[index].Tags = append([]string(nil), value.Resources[index].Tags...)
		value.Resources[index].Attributes = append(
			json.RawMessage(nil), value.Resources[index].Attributes...,
		)
	}
	value.Artifacts = append([]host.ObservationArtifact(nil), value.Artifacts...)
	return value
}
