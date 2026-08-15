package host

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

type registeredCapabilitySpec struct {
	spec         CapabilitySpec
	input        *jsonschema.Schema
	output       *jsonschema.Schema
	effectSchema *jsonschema.Schema
}

type registeredBoundAction struct {
	fingerprint string
	capability  CapabilityRef
	epoch       Epoch
	validUntil  Timepoint
}

const maxActiveBindings = 4096

// CapabilitySnapshot is an immutable, deterministically ordered V2 catalog.
type CapabilitySnapshot struct {
	Revision uint64           `json:"revision"`
	Specs    []CapabilitySpec `json:"specs"`
}

// RegisterSpec validates, seals, and registers one exact V2 capability.
func (registry *Registry) RegisterSpec(spec CapabilitySpec) (CapabilitySpec, error) {
	sealed, input, output, effectSchema, err := prepareCapabilitySpec(spec)
	if err != nil {
		return CapabilitySpec{}, err
	}
	if durabilityRank(sealed.RequiredDurability) >
		durabilityRank(registry.manifest.Durability.Profile) {
		return CapabilitySpec{}, invalid(
			"required_durability",
			"exceeds host durability "+string(registry.manifest.Durability.Profile),
		)
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if current, exists := registry.specs[sealed.Capability]; exists {
		if current.spec.Digest == sealed.Digest {
			return cloneCapabilitySpec(current.spec), nil
		}
		return CapabilitySpec{}, invalid(
			"capability",
			"exact version is already registered with a different capability spec",
		)
	}
	registry.specs[sealed.Capability] = registeredCapabilitySpec{
		spec:         cloneCapabilitySpec(sealed),
		input:        input,
		output:       output,
		effectSchema: effectSchema,
	}
	registry.revision++
	return cloneCapabilitySpec(sealed), nil
}

// UnregisterSpec revokes one exact V2 capability version.
func (registry *Registry) UnregisterSpec(ref CapabilityRef) bool {
	if err := ref.Validate("capability"); err != nil {
		return false
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.specs[ref]; !exists {
		return false
	}
	delete(registry.specs, ref)
	for bindingID, binding := range registry.bindings {
		if binding.capability == ref {
			delete(registry.bindings, bindingID)
		}
	}
	registry.revision++
	return true
}

// ResolveSpec returns a defensive copy of one active V2 capability spec.
func (registry *Registry) ResolveSpec(ref CapabilityRef) (CapabilitySpec, bool) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	registered, exists := registry.specs[ref]
	if !exists {
		return CapabilitySpec{}, false
	}
	return cloneCapabilitySpec(registered.spec), true
}

// SnapshotSpecs returns all active V2 specs and the catalog revision.
func (registry *Registry) SnapshotSpecs() CapabilitySnapshot {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	specs := make([]CapabilitySpec, 0, len(registry.specs))
	for _, registered := range registry.specs {
		specs = append(specs, cloneCapabilitySpec(registered.spec))
	}
	slices.SortFunc(specs, func(left, right CapabilitySpec) int {
		if left.Capability.ID < right.Capability.ID {
			return -1
		}
		if left.Capability.ID > right.Capability.ID {
			return 1
		}
		if left.Capability.Version < right.Capability.Version {
			return -1
		}
		if left.Capability.Version > right.Capability.Version {
			return 1
		}
		return 0
	})
	return CapabilitySnapshot{Revision: registry.revision, Specs: specs}
}

// MaxObservationGap bounds how many newer published observations may sit
// between a controller's observed sequence and the Host's current observation
// before a request is treated as stale. Hosts publish frequently in a living
// world, so exact latest equality turned every submission stage into a race
// that real controllers could not reliably win; binding revalidation and
// authoritative execution remain the actual safety checks.
const MaxObservationGap uint64 = 8

func withinObservationGap(currentObservationSeq, requestObservationSeq uint64) bool {
	return currentObservationSeq >= requestObservationSeq &&
		currentObservationSeq-requestObservationSeq <= MaxObservationGap
}

// ValidateRequest checks controller intent against the active catalog and the
// current host timeline. It does not produce or authorize effects.
func (registry *Registry) ValidateRequest(
	request ActionRequest,
	currentEpoch Epoch,
	currentObservationSeq uint64,
) error {
	_, _, err := registry.validateRequest(request, currentEpoch, currentObservationSeq)
	return err
}

func (registry *Registry) validateRequest(
	request ActionRequest,
	currentEpoch Epoch,
	currentObservationSeq uint64,
) (registeredCapabilitySpec, json.RawMessage, error) {
	if err := ValidateActionRequest(request); err != nil {
		return registeredCapabilitySpec{}, nil, err
	}
	if err := currentEpoch.Validate("current_epoch"); err != nil {
		return registeredCapabilitySpec{}, nil, err
	}
	if currentObservationSeq == 0 || currentObservationSeq > maxInteroperableInteger {
		return registeredCapabilitySpec{}, nil,
			invalid("current_observation_sequence", "must be a positive JSON-safe integer")
	}
	if request.ExpectedEpoch != currentEpoch {
		return registeredCapabilitySpec{}, nil,
			invalid("expected_epoch", "request belongs to a stale host epoch")
	}
	if request.ObservationSeq > currentObservationSeq ||
		currentObservationSeq-request.ObservationSeq > MaxObservationGap {
		return registeredCapabilitySpec{}, nil,
			invalid("observation_sequence", "request does not match the current host observation")
	}
	registered, err := registry.lookupSpecForExecution(request.Capability, request.SpecDigest)
	if err != nil {
		return registeredCapabilitySpec{}, nil, err
	}
	if len(request.Arguments) > int(registered.spec.MaxInputBytes) {
		return registeredCapabilitySpec{}, nil,
			invalid("arguments", "exceeds capability input limit")
	}
	canonicalArguments, err := canonicalizeJSONObject(
		"arguments",
		request.Arguments,
		int(registered.spec.MaxInputBytes),
	)
	if err != nil {
		return registeredCapabilitySpec{}, nil, err
	}
	instance, err := decodeJSON(canonicalArguments)
	if err != nil {
		return registeredCapabilitySpec{}, nil, invalid("arguments", err.Error())
	}
	if err := registered.input.Validate(instance); err != nil {
		return registeredCapabilitySpec{}, nil, invalid("arguments", err.Error())
	}
	return registered, canonicalArguments, nil
}

// SealBinding creates an immutable Host binding from validated controller
// intent and an authoritative adapter's draft. The Registry never resolves
// targets or derives effects from controller fields.
func (registry *Registry) SealBinding(
	request ActionRequest,
	draft BindingDraft,
	now Timepoint,
	currentEpoch Epoch,
	currentObservationSeq uint64,
) (BoundAction, error) {
	registered, canonicalArguments, err := registry.validateRequest(
		request,
		currentEpoch,
		currentObservationSeq,
	)
	if err != nil {
		return BoundAction{}, err
	}
	if err := validateHostID("binding_id", draft.BindingID, false); err != nil {
		return BoundAction{}, err
	}
	if err := now.Validate("now"); err != nil {
		return BoundAction{}, err
	}
	if err := draft.ValidUntil.Validate("valid_until"); err != nil {
		return BoundAction{}, err
	}
	if len(request.Targets) > 0 && len(draft.ResolvedTargets) == 0 {
		return BoundAction{}, invalid("resolved_targets", "must resolve every targeted request to at least one HostRef")
	}
	if now.Clock != draft.ValidUntil.Clock || registered.spec.ExecutionBudget.Clock != now.Clock {
		return BoundAction{}, invalid(
			"valid_until.clock",
			"must match the current host clock and capability execution budget",
		)
	}
	budget := int64(registered.spec.ExecutionBudget.Value)
	if now.Value > maxInteroperableInteger-budget {
		return BoundAction{}, invalid("now.value", "cannot add execution budget safely")
	}
	if draft.ValidUntil.Value <= now.Value || draft.ValidUntil.Value > now.Value+budget {
		return BoundAction{}, invalid(
			"valid_until",
			"must be after now and within the capability execution budget",
		)
	}
	normalizedEffects, err := normalizeEffects(
		draft.Effects,
		request.ExpectedEpoch,
		registered.spec.MaxEffects,
		registered.spec.RiskFloor,
		registered.effectSchema,
	)
	if err != nil {
		return BoundAction{}, err
	}
	requestForDigest := request
	requestForDigest.Arguments = canonicalArguments
	requestDigest, err := ActionRequestDigest(requestForDigest)
	if err != nil {
		return BoundAction{}, err
	}
	effectDigest, err := EffectPreviewDigest(normalizedEffects)
	if err != nil {
		return BoundAction{}, err
	}
	action := BoundAction{
		BindingID:           draft.BindingID,
		RequestID:           request.RequestID,
		RequestDigest:       requestDigest,
		ControllerID:        request.ControllerID,
		ActorID:             request.ActorID,
		Capability:          request.Capability,
		SpecDigest:          request.SpecDigest,
		NormalizedArguments: canonicalArguments,
		RequestedTargets:    cloneRefs(request.Targets),
		ResolvedTargets:     cloneRefs(draft.ResolvedTargets),
		ExpectedEpoch:       request.ExpectedEpoch,
		ObservationSeq:      request.ObservationSeq,
		TaskID:              request.TaskID,
		PlanStep:            clonePlanStepRef(request.PlanStep),
		IdempotencyKey:      request.IdempotencyKey,
		Effects:             normalizedEffects,
		EffectDigest:        effectDigest,
		BoundAt:             now,
		ValidUntil:          draft.ValidUntil,
	}
	if err := ValidateBoundAction(action); err != nil {
		return BoundAction{}, err
	}
	if err := registry.rememberBoundAction(action, now); err != nil {
		return BoundAction{}, err
	}
	return cloneBoundAction(action), nil
}

// AuthorizeBoundAction performs the final local TOCTOU, schema, scope, and
// effect checks immediately before authority-thread dispatch.
func (registry *Registry) AuthorizeBoundAction(
	action BoundAction,
	now Timepoint,
	currentEpoch Epoch,
	currentObservationSeq uint64,
	principal Principal,
) error {
	if err := ValidateBoundAction(action); err != nil {
		return err
	}
	if err := now.Validate("now"); err != nil {
		return err
	}
	if now.Clock != action.ValidUntil.Clock {
		return invalid("valid_until.clock", "must match the current host clock")
	}
	if now.Value >= action.ValidUntil.Value {
		return invalid("valid_until", "bound action has expired")
	}
	if action.ExpectedEpoch != currentEpoch {
		return invalid("expected_epoch", "bound action belongs to a stale host epoch")
	}
	if !withinObservationGap(currentObservationSeq, action.ObservationSeq) {
		return invalid("observation_sequence", "bound action does not match the current host observation")
	}
	if err := registry.verifyBoundAction(action); err != nil {
		return err
	}
	registered, err := registry.lookupSpecForExecution(action.Capability, action.SpecDigest)
	if err != nil {
		return err
	}
	if len(action.NormalizedArguments) > int(registered.spec.MaxInputBytes) {
		return invalid("normalized_arguments", "exceeds capability input limit")
	}
	instance, err := decodeJSON(action.NormalizedArguments)
	if err != nil {
		return invalid("normalized_arguments", err.Error())
	}
	if err := registered.input.Validate(instance); err != nil {
		return invalid("normalized_arguments", err.Error())
	}
	normalizedEffects, err := normalizeEffects(
		action.Effects,
		action.ExpectedEpoch,
		registered.spec.MaxEffects,
		registered.spec.RiskFloor,
		registered.effectSchema,
	)
	if err != nil {
		return err
	}
	if !equalEffects(action.Effects, normalizedEffects) {
		return invalid("effect_preview", "must be normalized by the Host binding path")
	}
	return authorizeSpecPrincipal(registered.spec, principal)
}

// ValidateSpecOutput checks an action result against its active V2 spec.
func (registry *Registry) ValidateSpecOutput(
	ref CapabilityRef,
	digest string,
	document []byte,
) error {
	registered, err := registry.lookupSpecForExecution(ref, digest)
	if err != nil {
		return err
	}
	if len(document) > int(registered.spec.MaxOutputBytes) {
		return invalid("output", "exceeds capability output limit")
	}
	instance, err := decodeJSON(document)
	if err != nil {
		return invalid("output", err.Error())
	}
	if err := registered.output.Validate(instance); err != nil {
		return invalid("output", err.Error())
	}
	return nil
}

func (registry *Registry) lookupSpecForExecution(
	ref CapabilityRef,
	digest string,
) (registeredCapabilitySpec, error) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	registered, exists := registry.specs[ref]
	if !exists {
		return registeredCapabilitySpec{}, invalid(
			"capability",
			fmt.Sprintf("%s@%s is not registered", ref.ID, ref.Version),
		)
	}
	if registered.spec.Digest != digest {
		return registeredCapabilitySpec{}, invalid(
			"spec_digest",
			"does not match the active capability spec",
		)
	}
	return registered, nil
}

func authorizeSpecPrincipal(spec CapabilitySpec, principal Principal) error {
	if err := ValidatePrincipal(principal); err != nil {
		return err
	}
	granted := make(map[string]struct{}, len(principal.GrantedScopes))
	for _, scope := range principal.GrantedScopes {
		granted[scope] = struct{}{}
	}
	for _, required := range spec.RequiredScopes {
		if _, exists := granted[required]; !exists {
			return invalid(
				"principal.granted_scopes",
				fmt.Sprintf("principal %q is missing required scope %q", principal.ID, required),
			)
		}
	}
	return nil
}

func cloneBoundAction(action BoundAction) BoundAction {
	copyAction := action
	copyAction.NormalizedArguments = append(json.RawMessage(nil), action.NormalizedArguments...)
	copyAction.RequestedTargets = cloneRefs(action.RequestedTargets)
	copyAction.ResolvedTargets = cloneRefs(action.ResolvedTargets)
	copyAction.Effects = cloneEffects(action.Effects)
	copyAction.PlanStep = clonePlanStepRef(action.PlanStep)
	return copyAction
}

func equalEffects(left, right []Effect) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func (registry *Registry) rememberBoundAction(action BoundAction, now Timepoint) error {
	fingerprint, err := boundActionFingerprint(action)
	if err != nil {
		return err
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	active, exists := registry.specs[action.Capability]
	if !exists || active.spec.Digest != action.SpecDigest {
		return invalid("capability", "was revoked or changed while the Host binding was created")
	}
	for bindingID, binding := range registry.bindings {
		sameWorld := binding.epoch.SessionID == action.ExpectedEpoch.SessionID &&
			binding.epoch.WorldID == action.ExpectedEpoch.WorldID
		if (sameWorld && binding.epoch != action.ExpectedEpoch) ||
			(binding.validUntil.Clock == now.Clock && binding.validUntil.Value <= now.Value) {
			delete(registry.bindings, bindingID)
		}
	}
	if current, exists := registry.bindings[action.BindingID]; exists {
		if current.fingerprint == fingerprint {
			return nil
		}
		return invalid("binding_id", "is already bound to different action data")
	}
	if len(registry.bindings) >= maxActiveBindings {
		return invalid("binding_id", "active Host binding limit reached")
	}
	registry.bindings[action.BindingID] = registeredBoundAction{
		fingerprint: fingerprint,
		capability:  action.Capability,
		epoch:       action.ExpectedEpoch,
		validUntil:  action.ValidUntil,
	}
	return nil
}

func (registry *Registry) verifyBoundAction(action BoundAction) error {
	fingerprint, err := boundActionFingerprint(action)
	if err != nil {
		return err
	}
	registry.mu.RLock()
	registered, exists := registry.bindings[action.BindingID]
	registry.mu.RUnlock()
	if !exists {
		return invalid("binding_id", "was not issued by this Host registry")
	}
	if registered.fingerprint != fingerprint {
		return invalid("binding_id", "Host binding data was modified")
	}
	return nil
}

func boundActionFingerprint(action BoundAction) (string, error) {
	encoded, err := json.Marshal(action)
	if err != nil {
		return "", fmt.Errorf("encode bound action fingerprint: %w", err)
	}
	return sha256Hex(encoded), nil
}
