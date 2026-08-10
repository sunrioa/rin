package host

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/sunrioa/rin/internal/jsonwire"
)

const (
	maxObservationBytes         = 1 << 20
	maxObservationPayloadBytes  = 256 << 10
	maxObservationFacts         = 256
	maxObservationResources     = 128
	maxObservationArtifacts     = 64
	maxObservationArtifactBytes = 64 << 20
	maxEffectAttributeBytes     = 16 << 10
	maxEffectAttributes         = 64
	maxEffectTags               = 32
	maxFactValueBytes           = 4 << 10
)

var mediaTypePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9!#$&^_.+-]*/[a-z0-9][a-z0-9!#$&^_.+-]*$`)

// Validate verifies an immutable schema reference.
func (ref SchemaRef) Validate(field string) error {
	if err := validateHostID(field+".id", ref.ID, true); err != nil {
		return err
	}
	if err := validateExactVersion(field+".version", ref.Version); err != nil {
		return err
	}
	if !lowerHexSHA256.MatchString(ref.SHA256) {
		return invalid(field+".sha256", "must be a lowercase SHA-256 digest")
	}
	return nil
}

// ValidateObservationEnvelope verifies the transport-safe shape of one
// observation. Use ValidateObservationPayload to check the payload schema.
func ValidateObservationEnvelope(observation ObservationEnvelope) error {
	for _, identifier := range []struct {
		field string
		value string
	}{
		{field: "observation_id", value: observation.ObservationID},
		{field: "host_id", value: observation.HostID},
		{field: "world_id", value: observation.WorldID},
		{field: "actor_id", value: observation.ActorID},
	} {
		if err := validateHostID(identifier.field, identifier.value, false); err != nil {
			return err
		}
	}
	if err := observation.Epoch.Validate("epoch"); err != nil {
		return err
	}
	if observation.WorldID != observation.Epoch.WorldID {
		return invalid("world_id", "must match epoch.world_id")
	}
	if observation.Sequence == 0 || observation.Sequence > maxInteroperableInteger {
		return invalid("sequence", "must be a positive JSON-safe integer")
	}
	if err := observation.ObservedAt.Validate("observed_at"); err != nil {
		return err
	}
	if err := observation.Schema.Validate("schema_ref"); err != nil {
		return err
	}
	if err := validateJSONObject("payload", observation.Payload, maxObservationPayloadBytes); err != nil {
		return err
	}
	if err := validateObservationFacts(observation.Facts, observation.Epoch); err != nil {
		return err
	}
	if err := validateObservationResources(observation.Resources, observation.Epoch); err != nil {
		return err
	}
	if err := validateObservationArtifacts(observation.Artifacts); err != nil {
		return err
	}
	if err := validateText("continuation_token", observation.ContinuationToken, 512, false); err != nil {
		return err
	}
	encoded, err := json.Marshal(observation)
	if err != nil {
		return invalid("observation", "cannot encode: "+err.Error())
	}
	if len(encoded) > maxObservationBytes {
		return invalid("observation", "must contain at most 1048576 bytes")
	}
	return nil
}

// ValidateObservationQuery verifies one bounded adapter query.
func ValidateObservationQuery(query ObservationQuery) error {
	for _, identifier := range []struct {
		field string
		value string
	}{
		{field: "query_id", value: query.QueryID},
		{field: "host_id", value: query.HostID},
		{field: "world_id", value: query.WorldID},
		{field: "actor_id", value: query.ActorID},
	} {
		if err := validateHostID(identifier.field, identifier.value, false); err != nil {
			return err
		}
	}
	if err := query.ExpectedEpoch.Validate("expected_epoch"); err != nil {
		return err
	}
	if query.WorldID != query.ExpectedEpoch.WorldID {
		return invalid("world_id", "must match expected_epoch.world_id")
	}
	if query.AfterSequence > maxInteroperableInteger {
		return invalid("after_sequence", "must be a JSON-safe integer")
	}
	if query.Limit == 0 || query.Limit > 256 {
		return invalid("limit", "must be between 1 and 256")
	}
	if len(query.Kinds) > 32 {
		return invalid("kinds", "must contain at most 32 values")
	}
	for index, kind := range query.Kinds {
		if err := validateHostID(fmt.Sprintf("kinds[%d]", index), kind, true); err != nil {
			return err
		}
		if index > 0 && query.Kinds[index-1] >= kind {
			return invalid("kinds", "must be sorted and contain no duplicates")
		}
	}
	return validateText("continuation_token", query.ContinuationToken, 512, false)
}

// ValidateObservationPayload binds an observation payload to one exact schema.
func ValidateObservationPayload(observation ObservationEnvelope, schema Schema) error {
	if err := ValidateObservationEnvelope(observation); err != nil {
		return err
	}
	if err := schema.Validate(); err != nil {
		return prefixValidation("payload_schema", err)
	}
	if observation.Schema.SHA256 != schema.SHA256 {
		return invalid("schema_ref.sha256", "does not match the supplied payload schema")
	}
	if err := schema.ValidateInstance(observation.Payload); err != nil {
		return prefixValidation("payload", err)
	}
	return nil
}

func validateObservationFacts(facts []ObservationFact, epoch Epoch) error {
	if len(facts) > maxObservationFacts {
		return invalid("facts", "must contain at most 256 values")
	}
	seen := make(map[string]struct{}, len(facts))
	for index, fact := range facts {
		field := fmt.Sprintf("facts[%d]", index)
		if err := validateHostID(field+".fact_id", fact.FactID, false); err != nil {
			return err
		}
		if _, duplicate := seen[fact.FactID]; duplicate {
			return invalid("facts", "must not contain duplicate fact_id values")
		}
		seen[fact.FactID] = struct{}{}
		if err := validateHostID(field+".kind", fact.Kind, true); err != nil {
			return err
		}
		if fact.Subject != nil {
			if err := validateRefAtEpoch(field+".subject", *fact.Subject, epoch); err != nil {
				return err
			}
		}
		if err := validateSortedTags(field+".tags", fact.Tags); err != nil {
			return err
		}
		if _, err := canonicalizeScalar(field+".value", fact.Value, maxFactValueBytes); err != nil {
			return err
		}
	}
	return nil
}

func validateObservationResources(resources []ObservationResource, epoch Epoch) error {
	if len(resources) > maxObservationResources {
		return invalid("resources", "must contain at most 128 values")
	}
	seen := make(map[string]struct{}, len(resources))
	for index, resource := range resources {
		field := fmt.Sprintf("resources[%d]", index)
		if err := validateRefAtEpoch(field+".ref", resource.Ref, epoch); err != nil {
			return err
		}
		key := resource.Ref.Namespace + "\x00" + resource.Ref.Type + "\x00" + resource.Ref.Key
		if _, duplicate := seen[key]; duplicate {
			return invalid("resources", "must not contain duplicate refs")
		}
		seen[key] = struct{}{}
		if err := validateHostID(field+".kind", resource.Kind, true); err != nil {
			return err
		}
		if err := validateSortedTags(field+".tags", resource.Tags); err != nil {
			return err
		}
		if !validOwnershipClass(resource.Ownership) {
			return invalid(field+".ownership", "is not supported")
		}
		if err := validateHostID(field+".scope", resource.Scope, true); err != nil {
			return err
		}
		if resource.Quantity > maxInteroperableInteger {
			return invalid(field+".quantity", "must be a JSON-safe integer")
		}
		if resource.Unit != "" {
			if err := validateHostID(field+".unit", resource.Unit, false); err != nil {
				return err
			}
		}
		if _, err := canonicalizeScalarObject(field+".attributes", resource.Attributes); err != nil {
			return err
		}
	}
	return nil
}

func validateObservationArtifacts(artifacts []ObservationArtifact) error {
	if len(artifacts) > maxObservationArtifacts {
		return invalid("artifacts", "must contain at most 64 values")
	}
	seen := make(map[string]struct{}, len(artifacts))
	for index, artifact := range artifacts {
		field := fmt.Sprintf("artifacts[%d]", index)
		if err := validateHostID(field+".artifact_id", artifact.ArtifactID, false); err != nil {
			return err
		}
		if _, duplicate := seen[artifact.ArtifactID]; duplicate {
			return invalid("artifacts", "must not contain duplicate artifact_id values")
		}
		seen[artifact.ArtifactID] = struct{}{}
		if err := validateHostID(field+".kind", artifact.Kind, true); err != nil {
			return err
		}
		if !mediaTypePattern.MatchString(artifact.MediaType) {
			return invalid(field+".media_type", "must be a lowercase media type without parameters")
		}
		if artifact.SizeBytes == 0 || artifact.SizeBytes > maxObservationArtifactBytes {
			return invalid(field+".size_bytes", "must be between 1 and 67108864")
		}
		if !lowerHexSHA256.MatchString(artifact.SHA256) {
			return invalid(field+".sha256", "must be a lowercase SHA-256 digest")
		}
	}
	return nil
}

// SealCapabilitySpec validates, normalizes, and digests a V2 capability.
func SealCapabilitySpec(spec CapabilitySpec) (CapabilitySpec, error) {
	sealed, _, _, _, err := prepareCapabilitySpec(spec)
	return sealed, err
}

func prepareCapabilitySpec(
	spec CapabilitySpec,
) (CapabilitySpec, *jsonschema.Schema, *jsonschema.Schema, *jsonschema.Schema, error) {
	normalized := cloneCapabilitySpec(spec)
	slices.Sort(normalized.RequiredScopes)
	if len(slices.Compact(normalized.RequiredScopes)) != len(spec.RequiredScopes) {
		return CapabilitySpec{}, nil, nil, nil,
			invalid("required_scopes", "must not contain duplicates")
	}
	input, err := normalized.Input.compiled()
	if err != nil {
		return CapabilitySpec{}, nil, nil, nil, prefixValidation("input", err)
	}
	output, err := normalized.Output.compiled()
	if err != nil {
		return CapabilitySpec{}, nil, nil, nil, prefixValidation("output", err)
	}
	effectSchema, err := normalized.EffectSchema.compiled()
	if err != nil {
		return CapabilitySpec{}, nil, nil, nil, prefixValidation("effect_schema", err)
	}
	if err := validateCapabilitySpecFields(normalized); err != nil {
		return CapabilitySpec{}, nil, nil, nil, err
	}
	digest, err := capabilitySpecDigest(normalized)
	if err != nil {
		return CapabilitySpec{}, nil, nil, nil, err
	}
	if spec.Digest != "" && spec.Digest != digest {
		return CapabilitySpec{}, nil, nil, nil, invalid("digest", "does not match capability spec")
	}
	normalized.Digest = digest
	return normalized, input, output, effectSchema, nil
}

// Validate verifies a sealed V2 capability spec.
func (spec CapabilitySpec) Validate() error {
	sealed, err := SealCapabilitySpec(spec)
	if err != nil {
		return err
	}
	if spec.Digest != sealed.Digest {
		return invalid("digest", "is required and must match capability spec")
	}
	if !slices.Equal(spec.RequiredScopes, sealed.RequiredScopes) {
		return invalid("required_scopes", "must be sorted")
	}
	return nil
}

func validateCapabilitySpecFields(spec CapabilitySpec) error {
	if err := spec.Capability.Validate("capability"); err != nil {
		return err
	}
	if err := validateText("description", spec.Description, 500, true); err != nil {
		return err
	}
	if spec.Kind != CapabilityAtomic && spec.Kind != CapabilityMacro {
		return invalid("kind", "is not supported")
	}
	if spec.Kind == CapabilityAtomic && spec.ProducesChildOperations {
		return invalid("produces_child_operations", "atomic capabilities cannot produce child operations")
	}
	if !validExecutionMode(spec.Execution) {
		return invalid("execution", "is not supported")
	}
	if !validCancellationMode(spec.Cancellation) {
		return invalid("cancellation", "is not supported")
	}
	if spec.Execution == ExecutionImmediate && spec.Cancellation != CancellationUnsupported {
		return invalid("cancellation", "immediate capabilities cannot be cancelled")
	}
	if !validRiskLevel(spec.RiskFloor) {
		return invalid("risk_floor", "is not supported")
	}
	if !validDurabilityProfile(spec.RequiredDurability) {
		return invalid("required_durability", "is not supported")
	}
	if err := spec.ExecutionBudget.Validate("execution_budget"); err != nil {
		return err
	}
	if spec.MaxInputBytes == 0 || spec.MaxInputBytes > maxInstanceBytes {
		return invalid("max_input_bytes", "must be between 1 and 1048576")
	}
	if spec.MaxOutputBytes == 0 || spec.MaxOutputBytes > maxInstanceBytes {
		return invalid("max_output_bytes", "must be between 1 and 1048576")
	}
	if spec.MaxEffects == 0 || spec.MaxEffects > 64 {
		return invalid("max_effects", "must be between 1 and 64")
	}
	if len(spec.RequiredScopes) > 32 {
		return invalid("required_scopes", "must contain at most 32 values")
	}
	for index, scope := range spec.RequiredScopes {
		if err := validateHostID(fmt.Sprintf("required_scopes[%d]", index), scope, true); err != nil {
			return err
		}
	}
	return nil
}

func capabilitySpecDigest(spec CapabilitySpec) (string, error) {
	payload := struct {
		Capability              CapabilityRef     `json:"capability"`
		Description             string            `json:"description"`
		InputSHA256             string            `json:"input_sha256"`
		OutputSHA256            string            `json:"output_sha256"`
		EffectSchemaSHA256      string            `json:"effect_schema_sha256"`
		Kind                    CapabilityKind    `json:"kind"`
		Execution               ExecutionMode     `json:"execution"`
		Cancellation            CancellationMode  `json:"cancellation"`
		RiskFloor               RiskLevel         `json:"risk_floor"`
		RequiredDurability      DurabilityProfile `json:"required_durability"`
		RequiredScopes          []string          `json:"required_scopes,omitempty"`
		ExecutionBudget         Duration          `json:"execution_budget"`
		MaxInputBytes           uint32            `json:"max_input_bytes"`
		MaxOutputBytes          uint32            `json:"max_output_bytes"`
		MaxEffects              uint32            `json:"max_effects"`
		ProducesChildOperations bool              `json:"produces_child_operations"`
	}{
		Capability:              spec.Capability,
		Description:             spec.Description,
		InputSHA256:             spec.Input.SHA256,
		OutputSHA256:            spec.Output.SHA256,
		EffectSchemaSHA256:      spec.EffectSchema.SHA256,
		Kind:                    spec.Kind,
		Execution:               spec.Execution,
		Cancellation:            spec.Cancellation,
		RiskFloor:               spec.RiskFloor,
		RequiredDurability:      spec.RequiredDurability,
		RequiredScopes:          spec.RequiredScopes,
		ExecutionBudget:         spec.ExecutionBudget,
		MaxInputBytes:           spec.MaxInputBytes,
		MaxOutputBytes:          spec.MaxOutputBytes,
		MaxEffects:              spec.MaxEffects,
		ProducesChildOperations: spec.ProducesChildOperations,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode capability spec digest: %w", err)
	}
	return sha256Hex(encoded), nil
}

// ValidateActionRequest verifies controller-authored intent. Effects, risk,
// ownership, and outcomes are deliberately absent from this type.
func ValidateActionRequest(request ActionRequest) error {
	for _, identifier := range []struct {
		field string
		value string
	}{
		{field: "request_id", value: request.RequestID},
		{field: "controller_id", value: request.ControllerID},
		{field: "actor_id", value: request.ActorID},
		{field: "idempotency_key", value: request.IdempotencyKey},
	} {
		if err := validateHostID(identifier.field, identifier.value, false); err != nil {
			return err
		}
	}
	if request.TaskID != "" {
		if err := validateHostID("task_id", request.TaskID, false); err != nil {
			return err
		}
	}
	if err := request.Capability.Validate("capability"); err != nil {
		return err
	}
	if !lowerHexSHA256.MatchString(request.SpecDigest) {
		return invalid("spec_digest", "must be a lowercase SHA-256 digest")
	}
	if err := request.ExpectedEpoch.Validate("expected_epoch"); err != nil {
		return err
	}
	if _, err := canonicalizeJSONObject("arguments", request.Arguments, maxInstanceBytes); err != nil {
		return err
	}
	if err := validateRefs("target_refs", request.Targets, request.ExpectedEpoch); err != nil {
		return err
	}
	if request.ObservationSeq == 0 || request.ObservationSeq > maxInteroperableInteger {
		return invalid("observation_sequence", "must be a positive JSON-safe integer")
	}
	return nil
}

// ActionRequestDigest returns the canonical digest used by BoundAction.
func ActionRequestDigest(request ActionRequest) (string, error) {
	if err := ValidateActionRequest(request); err != nil {
		return "", err
	}
	arguments, err := canonicalizeJSONObject("arguments", request.Arguments, maxInstanceBytes)
	if err != nil {
		return "", err
	}
	payload := struct {
		RequestID      string          `json:"request_id"`
		ControllerID   string          `json:"controller_id"`
		ActorID        string          `json:"actor_id"`
		Capability     CapabilityRef   `json:"capability"`
		SpecDigest     string          `json:"spec_digest"`
		Arguments      json.RawMessage `json:"arguments"`
		Targets        []HostRef       `json:"target_refs,omitempty"`
		ExpectedEpoch  Epoch           `json:"expected_epoch"`
		ObservationSeq uint64          `json:"observation_sequence"`
		TaskID         string          `json:"task_id,omitempty"`
		IdempotencyKey string          `json:"idempotency_key"`
	}{
		RequestID:      request.RequestID,
		ControllerID:   request.ControllerID,
		ActorID:        request.ActorID,
		Capability:     request.Capability,
		SpecDigest:     request.SpecDigest,
		Arguments:      arguments,
		Targets:        cloneRefs(request.Targets),
		ExpectedEpoch:  request.ExpectedEpoch,
		ObservationSeq: request.ObservationSeq,
		TaskID:         request.TaskID,
		IdempotencyKey: request.IdempotencyKey,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode action request digest: %w", err)
	}
	return sha256Hex(encoded), nil
}

// ValidateEffect verifies a Host-authored effect independently of a
// capability's attribute schema.
func ValidateEffect(effect Effect, epoch Epoch) error {
	if err := validateHostID("effect_id", effect.EffectID, false); err != nil {
		return err
	}
	if err := validateHostID("kind", effect.Kind, true); err != nil {
		return err
	}
	if !validEffectOperation(effect.Operation) {
		return invalid("operation", "is not supported")
	}
	if effect.Subject != nil {
		if err := validateRefAtEpoch("subject_ref", *effect.Subject, epoch); err != nil {
			return err
		}
	}
	if effect.Target != nil {
		if err := validateRefAtEpoch("target_ref", *effect.Target, epoch); err != nil {
			return err
		}
	}
	if err := validateSortedTags("tags", effect.Tags); err != nil {
		return err
	}
	if !validOwnershipClass(effect.Ownership) {
		return invalid("ownership", "is not supported")
	}
	if err := validateHostID("scope", effect.Scope, true); err != nil {
		return err
	}
	if effect.Quantity > maxInteroperableInteger {
		return invalid("quantity", "must be a JSON-safe integer")
	}
	if effect.Unit != "" {
		if err := validateHostID("unit", effect.Unit, false); err != nil {
			return err
		}
	}
	if !validRiskLevel(effect.Risk) {
		return invalid("risk", "is not supported")
	}
	_, err := canonicalizeScalarObject("attributes", effect.Attributes)
	return err
}

// EffectPreviewDigest returns a deterministic digest for a normalized effect
// preview. Effects and tags must already be sorted and attributes canonical.
func EffectPreviewDigest(effects []Effect) (string, error) {
	if len(effects) == 0 || len(effects) > 64 {
		return "", invalid("effect_preview", "must contain between 1 and 64 values")
	}
	encoded, err := json.Marshal(effects)
	if err != nil {
		return "", fmt.Errorf("encode effect preview digest: %w", err)
	}
	if err := jsonwire.Validate(encoded); err != nil {
		return "", invalid("effect_preview", err.Error())
	}
	return sha256Hex(encoded), nil
}

// ValidateBoundAction verifies an immutable Host binding independently of the
// active Registry. Registry authorization performs the final TOCTOU checks.
func ValidateBoundAction(action BoundAction) error {
	for _, identifier := range []struct {
		field string
		value string
	}{
		{field: "binding_id", value: action.BindingID},
		{field: "request_id", value: action.RequestID},
		{field: "controller_id", value: action.ControllerID},
		{field: "actor_id", value: action.ActorID},
		{field: "idempotency_key", value: action.IdempotencyKey},
	} {
		if err := validateHostID(identifier.field, identifier.value, false); err != nil {
			return err
		}
	}
	if action.TaskID != "" {
		if err := validateHostID("task_id", action.TaskID, false); err != nil {
			return err
		}
	}
	if !lowerHexSHA256.MatchString(action.RequestDigest) {
		return invalid("request_digest", "must be a lowercase SHA-256 digest")
	}
	if !lowerHexSHA256.MatchString(action.SpecDigest) {
		return invalid("spec_digest", "must be a lowercase SHA-256 digest")
	}
	if !lowerHexSHA256.MatchString(action.EffectDigest) {
		return invalid("effect_digest", "must be a lowercase SHA-256 digest")
	}
	if err := action.Capability.Validate("capability"); err != nil {
		return err
	}
	if err := action.ExpectedEpoch.Validate("expected_epoch"); err != nil {
		return err
	}
	canonicalArguments, err := canonicalizeJSONObject("normalized_arguments", action.NormalizedArguments, maxInstanceBytes)
	if err != nil {
		return err
	}
	if !bytes.Equal(canonicalArguments, action.NormalizedArguments) {
		return invalid("normalized_arguments", "must be canonical JSON")
	}
	if err := validateRefs("requested_targets", action.RequestedTargets, action.ExpectedEpoch); err != nil {
		return err
	}
	if err := validateRefs("resolved_targets", action.ResolvedTargets, action.ExpectedEpoch); err != nil {
		return err
	}
	if action.ObservationSeq == 0 || action.ObservationSeq > maxInteroperableInteger {
		return invalid("observation_sequence", "must be a positive JSON-safe integer")
	}
	if err := action.BoundAt.Validate("bound_at"); err != nil {
		return err
	}
	if err := action.ValidUntil.Validate("valid_until"); err != nil {
		return err
	}
	if action.BoundAt.Clock != action.ValidUntil.Clock || action.ValidUntil.Value <= action.BoundAt.Value {
		return invalid("valid_until", "must use the binding clock and be after bound_at")
	}
	if len(action.Effects) == 0 || len(action.Effects) > 64 {
		return invalid("effect_preview", "must contain between 1 and 64 values")
	}
	previous := ""
	for index, effect := range action.Effects {
		if err := ValidateEffect(effect, action.ExpectedEpoch); err != nil {
			return prefixValidation(fmt.Sprintf("effect_preview[%d]", index), err)
		}
		if previous != "" && effect.EffectID <= previous {
			return invalid("effect_preview", "must be sorted by unique effect_id")
		}
		previous = effect.EffectID
		canonicalAttributes, err := canonicalizeScalarObject("attributes", effect.Attributes)
		if err != nil {
			return prefixValidation(fmt.Sprintf("effect_preview[%d]", index), err)
		}
		if !bytes.Equal(canonicalAttributes, effect.Attributes) {
			return invalid(fmt.Sprintf("effect_preview[%d].attributes", index), "must be canonical JSON")
		}
	}
	request := action.asRequest()
	digest, err := ActionRequestDigest(request)
	if err != nil {
		return prefixValidation("request", err)
	}
	if digest != action.RequestDigest {
		return invalid("request_digest", "does not match bound request fields")
	}
	effectDigest, err := EffectPreviewDigest(action.Effects)
	if err != nil {
		return err
	}
	if effectDigest != action.EffectDigest {
		return invalid("effect_digest", "does not match effect_preview")
	}
	return nil
}

func (action BoundAction) asRequest() ActionRequest {
	return ActionRequest{
		RequestID:      action.RequestID,
		ControllerID:   action.ControllerID,
		ActorID:        action.ActorID,
		Capability:     action.Capability,
		SpecDigest:     action.SpecDigest,
		Arguments:      append(json.RawMessage(nil), action.NormalizedArguments...),
		Targets:        cloneRefs(action.RequestedTargets),
		ExpectedEpoch:  action.ExpectedEpoch,
		ObservationSeq: action.ObservationSeq,
		TaskID:         action.TaskID,
		IdempotencyKey: action.IdempotencyKey,
	}
}

func normalizeEffects(
	effects []Effect,
	epoch Epoch,
	maximum uint32,
	riskFloor RiskLevel,
	attributes *jsonschema.Schema,
) ([]Effect, error) {
	if len(effects) == 0 || len(effects) > int(maximum) {
		return nil, invalid("effect_preview", fmt.Sprintf("must contain between 1 and %d values", maximum))
	}
	normalized := make([]Effect, len(effects))
	for index, effect := range effects {
		normalized[index] = cloneEffect(effect)
		slices.Sort(normalized[index].Tags)
		if len(slices.Compact(normalized[index].Tags)) != len(effect.Tags) {
			return nil, invalid(fmt.Sprintf("effect_preview[%d].tags", index), "must not contain duplicates")
		}
		canonicalAttributes, err := canonicalizeScalarObject("attributes", effect.Attributes)
		if err != nil {
			return nil, prefixValidation(fmt.Sprintf("effect_preview[%d]", index), err)
		}
		normalized[index].Attributes = canonicalAttributes
		if err := ValidateEffect(normalized[index], epoch); err != nil {
			return nil, prefixValidation(fmt.Sprintf("effect_preview[%d]", index), err)
		}
		if riskRank(normalized[index].Risk) < riskRank(riskFloor) {
			return nil, invalid(fmt.Sprintf("effect_preview[%d].risk", index), "must not be below capability risk_floor")
		}
		instance, err := decodeJSON(normalized[index].Attributes)
		if err != nil {
			return nil, invalid(fmt.Sprintf("effect_preview[%d].attributes", index), err.Error())
		}
		if err := attributes.Validate(instance); err != nil {
			return nil, invalid(fmt.Sprintf("effect_preview[%d].attributes", index), err.Error())
		}
	}
	slices.SortFunc(normalized, func(left, right Effect) int {
		return strings.Compare(left.EffectID, right.EffectID)
	})
	for index := 1; index < len(normalized); index++ {
		if normalized[index-1].EffectID == normalized[index].EffectID {
			return nil, invalid("effect_preview", "must not contain duplicate effect_id values")
		}
	}
	return normalized, nil
}

func canonicalizeJSONObject(field string, raw json.RawMessage, maximum int) (json.RawMessage, error) {
	if err := validateJSONObject(field, raw, maximum); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		return nil, invalid(field, "must be valid JSON: "+err.Error())
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, invalid(field, "cannot canonicalize: "+err.Error())
	}
	return canonical, nil
}

func canonicalizeScalar(field string, raw json.RawMessage, maximum int) (json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > maximum {
		return nil, invalid(field, "must be a bounded JSON scalar")
	}
	if err := jsonwire.Validate(raw); err != nil {
		return nil, invalid(field, err.Error())
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, invalid(field, "must be valid JSON: "+err.Error())
	}
	switch typed := value.(type) {
	case bool:
	case json.Number:
		if len(typed.String()) > 64 {
			return nil, invalid(field, "number representation is too long")
		}
		number, err := strconv.ParseFloat(typed.String(), 64)
		if err != nil || math.IsInf(number, 0) || math.Abs(number) > maxInteroperableInteger {
			return nil, invalid(field, "number must be finite and interoperable")
		}
	case string:
		if err := validateText(field, typed, 1024, false); err != nil {
			return nil, err
		}
	default:
		return nil, invalid(field, "must be a string, number, or boolean")
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, invalid(field, "cannot canonicalize: "+err.Error())
	}
	return canonical, nil
}

func canonicalizeScalarObject(field string, raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > maxEffectAttributeBytes {
		return nil, invalid(field, "must be a bounded JSON object")
	}
	if err := jsonwire.Validate(raw); err != nil {
		return nil, invalid(field, err.Error())
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value map[string]json.RawMessage
	if err := decoder.Decode(&value); err != nil {
		return nil, invalid(field, "must be a JSON object")
	}
	if len(value) > maxEffectAttributes {
		return nil, invalid(field, "must contain at most 64 properties")
	}
	canonical := make(map[string]json.RawMessage, len(value))
	for name, item := range value {
		if err := validateHostID(field+"."+name, name, false); err != nil {
			return nil, err
		}
		encoded, err := canonicalizeScalar(field+"."+name, item, maxFactValueBytes)
		if err != nil {
			return nil, err
		}
		canonical[name] = encoded
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return nil, invalid(field, "cannot canonicalize: "+err.Error())
	}
	return encoded, nil
}

func validateSortedTags(field string, tags []string) error {
	if len(tags) > maxEffectTags {
		return invalid(field, "must contain at most 32 values")
	}
	for index, tag := range tags {
		if err := validateHostID(fmt.Sprintf("%s[%d]", field, index), tag, true); err != nil {
			return err
		}
		if index > 0 && tags[index-1] >= tag {
			return invalid(field, "must be sorted and contain no duplicates")
		}
	}
	return nil
}

func validateRefAtEpoch(field string, ref HostRef, epoch Epoch) error {
	if err := ref.Validate(field); err != nil {
		return err
	}
	if ref.Epoch != epoch {
		return invalid(field+".epoch", "must equal the enclosing epoch")
	}
	return nil
}

func cloneCapabilitySpec(spec CapabilitySpec) CapabilitySpec {
	copySpec := spec
	copySpec.Input.Document = append(json.RawMessage(nil), spec.Input.Document...)
	copySpec.Output.Document = append(json.RawMessage(nil), spec.Output.Document...)
	copySpec.EffectSchema.Document = append(json.RawMessage(nil), spec.EffectSchema.Document...)
	copySpec.RequiredScopes = append([]string(nil), spec.RequiredScopes...)
	return copySpec
}

func cloneEffect(effect Effect) Effect {
	copyEffect := effect
	copyEffect.Subject = cloneRefPointer(effect.Subject)
	copyEffect.Target = cloneRefPointer(effect.Target)
	copyEffect.Tags = append([]string(nil), effect.Tags...)
	copyEffect.Attributes = append(json.RawMessage(nil), effect.Attributes...)
	return copyEffect
}

func cloneEffects(effects []Effect) []Effect {
	cloned := make([]Effect, len(effects))
	for index, effect := range effects {
		cloned[index] = cloneEffect(effect)
	}
	return cloned
}

func cloneRefPointer(ref *HostRef) *HostRef {
	if ref == nil {
		return nil
	}
	copyRef := *ref
	return &copyRef
}

func validOwnershipClass(value OwnershipClass) bool {
	return value == OwnershipUnknown || value == OwnershipSystem ||
		value == OwnershipActor || value == OwnershipController ||
		value == OwnershipPlayer || value == OwnershipShared ||
		value == OwnershipUnowned
}

func validEffectOperation(value EffectOperation) bool {
	return value == EffectOperationRead || value == EffectOperationCreate ||
		value == EffectOperationUpdate || value == EffectOperationDelete ||
		value == EffectOperationTransfer || value == EffectOperationConsume ||
		value == EffectOperationExecute || value == EffectOperationCommunicate
}

func riskRank(value RiskLevel) int {
	switch value {
	case RiskLow:
		return 0
	case RiskModerate:
		return 1
	case RiskHigh:
		return 2
	case RiskCritical:
		return 3
	default:
		return -1
	}
}
