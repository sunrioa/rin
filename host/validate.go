package host

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/sunrioa/rin/internal/jsonwire"
)

var (
	hostIDPattern  = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`)
	versionCore    = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z.-]+))?(?:\+([0-9A-Za-z.-]+))?$`)
	lowerHexSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

const maxInteroperableInteger = 9_007_199_254_740_991

// ValidateHostManifest verifies an engine-neutral host declaration.
func ValidateHostManifest(manifest HostManifest) error {
	if manifest.ContractVersion != ContractVersion {
		return invalid("contract_version", "must equal "+ContractVersion)
	}
	identifiers := []struct {
		field string
		value string
	}{
		{field: "adapter_id", value: manifest.AdapterID},
		{field: "engine_id", value: manifest.EngineID},
		{field: "runtime", value: manifest.Runtime},
		{field: "platform", value: manifest.Platform},
	}
	for _, identifier := range identifiers {
		if err := validateHostID(identifier.field, identifier.value, false); err != nil {
			return err
		}
	}
	if err := validateExactVersion("adapter_version", manifest.AdapterVersion); err != nil {
		return err
	}
	if err := validateText("engine_version", manifest.EngineVersion, 64, true); err != nil {
		return err
	}
	if !validAuthorityMode(manifest.Authority) {
		return invalid("authority", "is not supported")
	}
	if !validDeploymentMode(manifest.Deployment) {
		return invalid("deployment", "is not supported")
	}
	if !validControlMode(manifest.Control) {
		return invalid("control", "is not supported")
	}
	if manifest.Deployment == DeploymentComputerControl &&
		manifest.Control != ControlComputerControl {
		return invalid("control", "must be computer-control for computer-control deployment")
	}
	if manifest.Control == ControlComputerControl &&
		manifest.Deployment != DeploymentComputerControl {
		return invalid("deployment", "must be computer-control for computer-control control mode")
	}
	if err := validateUniqueClockModes(manifest.ClockModes); err != nil {
		return err
	}
	if err := validateUniqueDecisionModes(manifest.DecisionModes); err != nil {
		return err
	}
	if manifest.MaxConcurrentActors == 0 || manifest.MaxConcurrentActors > 1_000_000 {
		return invalid("max_concurrent_actors", "must be between 1 and 1000000")
	}
	if err := ValidateDurability(manifest.Durability); err != nil {
		return err
	}
	if manifest.Authority == AuthorityClientAdvisory &&
		manifest.Durability.Profile != DurabilityAdvisory {
		return invalid("durability.profile", "client-advisory hosts cannot claim world mutation durability")
	}
	return nil
}

// ValidateDurability rejects guarantees that exceed the selected profile.
func ValidateDurability(durability Durability) error {
	if !validDurabilityProfile(durability.Profile) {
		return invalid("durability.profile", "is not supported")
	}
	switch durability.Profile {
	case DurabilityAdvisory:
		if durability.DurableBeforeNetwork || durability.DurableOutbox ||
			durability.IdempotentApply || durability.AtomicApplyAndOutbox {
			return invalid("durability", "advisory profile must not claim stronger guarantees")
		}
	case DurabilityIdempotent:
		if !durability.StableIdentity || !durability.DurableBeforeNetwork ||
			!durability.DurableOutbox || !durability.IdempotentApply {
			return invalid("durability", "idempotent-action requires stable identity, durable pending work and outbox, and idempotent apply")
		}
		if durability.AtomicApplyAndOutbox {
			return invalid("durability.atomic_apply_and_outbox", "requires transactional-action profile")
		}
	case DurabilityTransactional:
		if !durability.StableIdentity || !durability.DurableBeforeNetwork ||
			!durability.DurableOutbox || !durability.AtomicApplyAndOutbox {
			return invalid("durability", "transactional-action requires stable identity, durable pending work and outbox, and atomic apply/outbox")
		}
	}
	return nil
}

// Validate verifies an Epoch and uses field as the error-path prefix.
func (epoch Epoch) Validate(field string) error {
	if err := validateHostID(field+".session_id", epoch.SessionID, false); err != nil {
		return err
	}
	if err := validateHostID(field+".world_id", epoch.WorldID, false); err != nil {
		return err
	}
	if epoch.Host == 0 || epoch.World == 0 || epoch.Timeline == 0 ||
		epoch.Host > maxInteroperableInteger ||
		epoch.World > maxInteroperableInteger ||
		epoch.Timeline > maxInteroperableInteger {
		return invalid(field, "host, world, and timeline generations must be positive JSON-safe integers")
	}
	return nil
}

// Validate verifies a host timepoint and uses field as the error-path prefix.
func (timepoint Timepoint) Validate(field string) error {
	if !validClockMode(timepoint.Clock) {
		return invalid(field+".clock", "is not supported")
	}
	if timepoint.Value < 0 || timepoint.Value > maxInteroperableInteger {
		return invalid(field+".value", "must be a non-negative JSON-safe integer")
	}
	return nil
}

// Validate verifies a positive host-clock duration.
func (duration Duration) Validate(field string) error {
	if !validClockMode(duration.Clock) {
		return invalid(field+".clock", "is not supported")
	}
	if duration.Value == 0 || duration.Value > maxInteroperableInteger {
		return invalid(field+".value", "must be a positive JSON-safe integer")
	}
	return nil
}

// Validate verifies an opaque host reference and its epoch.
func (ref HostRef) Validate(field string) error {
	if err := validateHostID(field+".namespace", ref.Namespace, true); err != nil {
		return err
	}
	if err := validateHostID(field+".type", ref.Type, false); err != nil {
		return err
	}
	if err := validateText(field+".key", ref.Key, 256, true); err != nil {
		return err
	}
	return ref.Epoch.Validate(field + ".epoch")
}

// SealDescriptor validates and normalizes a capability descriptor and computes
// its deterministic digest.
func SealDescriptor(descriptor CapabilityDescriptor) (CapabilityDescriptor, error) {
	sealed, _, _, err := prepareDescriptor(descriptor)
	return sealed, err
}

func prepareDescriptor(
	descriptor CapabilityDescriptor,
) (CapabilityDescriptor, *jsonschema.Schema, *jsonschema.Schema, error) {
	normalized := cloneDescriptor(descriptor)
	normalized.RequiredScopes = append([]string(nil), descriptor.RequiredScopes...)
	slices.Sort(normalized.RequiredScopes)
	compacted := slices.Compact(normalized.RequiredScopes)
	if len(compacted) != len(descriptor.RequiredScopes) {
		return CapabilityDescriptor{}, nil, nil,
			invalid("required_scopes", "must not contain duplicates")
	}
	normalized.RequiredScopes = compacted
	normalized.Input.Document = append(json.RawMessage(nil), descriptor.Input.Document...)
	normalized.Output.Document = append(json.RawMessage(nil), descriptor.Output.Document...)

	input, err := normalized.Input.compiled()
	if err != nil {
		return CapabilityDescriptor{}, nil, nil, prefixValidation("input", err)
	}
	output, err := normalized.Output.compiled()
	if err != nil {
		return CapabilityDescriptor{}, nil, nil, prefixValidation("output", err)
	}
	if err := validateDescriptorFields(normalized); err != nil {
		return CapabilityDescriptor{}, nil, nil, err
	}
	digest, err := descriptorDigest(normalized)
	if err != nil {
		return CapabilityDescriptor{}, nil, nil, err
	}
	if descriptor.Digest != "" && descriptor.Digest != digest {
		return CapabilityDescriptor{}, nil, nil,
			invalid("digest", "does not match descriptor")
	}
	normalized.Digest = digest
	return normalized, input, output, nil
}

// Validate verifies a sealed descriptor without changing it.
func (descriptor CapabilityDescriptor) Validate() error {
	sealed, err := SealDescriptor(descriptor)
	if err != nil {
		return err
	}
	if descriptor.Digest != sealed.Digest {
		return invalid("digest", "is required and must match descriptor")
	}
	if !slices.Equal(descriptor.RequiredScopes, sealed.RequiredScopes) {
		return invalid("required_scopes", "must be sorted")
	}
	return nil
}

func validateDescriptorFields(descriptor CapabilityDescriptor) error {
	if err := descriptor.Capability.Validate("capability"); err != nil {
		return err
	}
	if err := validateText("description", descriptor.Description, 300, true); err != nil {
		return err
	}
	if !validEffectClass(descriptor.Effect) {
		return invalid("effect", "is not supported")
	}
	if !validExecutionMode(descriptor.Execution) {
		return invalid("execution", "is not supported")
	}
	if !validRiskLevel(descriptor.Risk) {
		return invalid("risk", "is not supported")
	}
	if !validDurabilityProfile(descriptor.RequiredDurability) {
		return invalid("required_durability", "is not supported")
	}
	if !validCancellationMode(descriptor.Cancellation) {
		return invalid("cancellation", "is not supported")
	}
	if descriptor.Execution == ExecutionImmediate &&
		descriptor.Cancellation != CancellationUnsupported {
		return invalid("cancellation", "immediate capabilities cannot be cancelled")
	}
	if descriptor.Effect == EffectRead {
		if descriptor.RequiredDurability != DurabilityAdvisory {
			return invalid("required_durability", "read capabilities must use advisory durability")
		}
		if descriptor.Reversible {
			return invalid("reversible", "read capabilities do not have effects to reverse")
		}
	}
	if err := descriptor.ExecutionBudget.Validate("execution_budget"); err != nil {
		return err
	}
	if descriptor.MaxInputBytes == 0 || descriptor.MaxInputBytes > 1<<20 {
		return invalid("max_input_bytes", "must be between 1 and 1048576")
	}
	if descriptor.MaxOutputBytes == 0 || descriptor.MaxOutputBytes > 1<<20 {
		return invalid("max_output_bytes", "must be between 1 and 1048576")
	}
	if len(descriptor.RequiredScopes) > 32 {
		return invalid("required_scopes", "must contain at most 32 values")
	}
	for index, scope := range descriptor.RequiredScopes {
		if err := validateHostID(fmt.Sprintf("required_scopes[%d]", index), scope, true); err != nil {
			return err
		}
	}
	return nil
}

// Validate verifies a namespaced capability ID and exact semantic version.
func (ref CapabilityRef) Validate(field string) error {
	if err := validateHostID(field+".id", ref.ID, true); err != nil {
		return err
	}
	if !strings.ContainsRune(ref.ID, '.') {
		return invalid(field+".id", "must be namespaced")
	}
	return validateExactVersion(field+".version", ref.Version)
}

// ValidateActionOffer verifies the host-independent shape of an offer.
func ValidateActionOffer(offer ActionOffer) error {
	if err := validateHostID("offer_id", offer.OfferID, false); err != nil {
		return err
	}
	if err := validateHostID("decision_window_id", offer.DecisionWindowID, false); err != nil {
		return err
	}
	if err := validateHostID("actor_id", offer.ActorID, false); err != nil {
		return err
	}
	if err := offer.Capability.Validate("capability"); err != nil {
		return err
	}
	if !lowerHexSHA256.MatchString(offer.DescriptorDigest) {
		return invalid("descriptor_digest", "must be a lowercase SHA-256 digest")
	}
	if err := validateText("description", offer.Description, 300, true); err != nil {
		return err
	}
	if err := validateJSONObject("arguments", offer.Arguments, 1<<20); err != nil {
		return err
	}
	if err := validateRefs("targets", offer.Targets, offer.ExpectedEpoch); err != nil {
		return err
	}
	if err := offer.ExpectedEpoch.Validate("expected_epoch"); err != nil {
		return err
	}
	if offer.ObservationSeq == 0 || offer.ObservationSeq > maxInteroperableInteger {
		return invalid("observation_seq", "must be a positive JSON-safe integer")
	}
	if err := offer.Deadline.Validate("deadline"); err != nil {
		return err
	}
	return nil
}

// ValidateActionInvocation verifies the host-independent shape of an invocation.
func ValidateActionInvocation(invocation ActionInvocation) error {
	if err := validateHostID("operation_id", invocation.OperationID, false); err != nil {
		return err
	}
	offer := ActionOffer{
		OfferID:          invocation.OfferID,
		DecisionWindowID: invocation.DecisionWindowID,
		ActorID:          invocation.ActorID,
		Capability:       invocation.Capability,
		DescriptorDigest: invocation.DescriptorDigest,
		Description:      "invocation",
		Arguments:        invocation.Arguments,
		Targets:          invocation.Targets,
		ExpectedEpoch:    invocation.ExpectedEpoch,
		ObservationSeq:   invocation.ObservationSeq,
		Deadline:         invocation.Deadline,
	}
	if err := ValidateActionOffer(offer); err != nil {
		return err
	}
	return nil
}

// ValidateActionRun verifies one action progress record.
func ValidateActionRun(run ActionRun) error {
	if err := validateHostID("operation_id", run.OperationID, false); err != nil {
		return err
	}
	if !validActionRunStatus(run.Status) {
		return invalid("status", "is not supported")
	}
	if run.ProgressSeq == 0 || run.ProgressSeq > maxInteroperableInteger {
		return invalid("progress_seq", "must be a positive JSON-safe integer")
	}
	if run.Progress > 100 {
		return invalid("progress", "must be between 0 and 100")
	}
	if run.Status == ActionQueued && run.Progress != 0 {
		return invalid("progress", "queued action must have zero progress")
	}
	if run.Status == ActionSucceeded && run.Progress != 100 {
		return invalid("progress", "succeeded action must have 100 progress")
	}
	if err := run.UpdatedAt.Validate("updated_at"); err != nil {
		return err
	}
	return validateText("message", run.Message, 500, false)
}

// CanTransitionActionRun reports whether an action status transition is legal.
func CanTransitionActionRun(from, to ActionRunStatus) bool {
	switch from {
	case ActionQueued:
		return to == ActionRunning || to == ActionFailed || to == ActionCancelled ||
			to == ActionStale || to == ActionOutcomeUnknown
	case ActionRunning:
		return to == ActionSucceeded || to == ActionFailed || to == ActionCancelled ||
			to == ActionInterrupted || to == ActionStale || to == ActionOutcomeUnknown
	case ActionOutcomeUnknown:
		return to == ActionSucceeded || to == ActionFailed || to == ActionCancelled ||
			to == ActionInterrupted || to == ActionStale
	default:
		return false
	}
}

// ValidateActionOutcome verifies one terminal host outcome.
func ValidateActionOutcome(outcome ActionOutcome) error {
	if err := validateHostID("operation_id", outcome.OperationID, false); err != nil {
		return err
	}
	if !isTerminalActionRunStatus(outcome.Status) {
		return invalid("status", "must be a terminal action status")
	}
	if outcome.Status == ActionSucceeded {
		if outcome.Code != "" {
			return invalid("code", "must be empty for succeeded outcome")
		}
	} else if err := validateHostID("code", outcome.Code, true); err != nil {
		return err
	}
	if err := validateText("summary", outcome.Summary, 1000, true); err != nil {
		return err
	}
	if err := outcome.Epoch.Validate("epoch"); err != nil {
		return err
	}
	if err := validateRefs("evidence", outcome.Evidence, outcome.Epoch); err != nil {
		return err
	}
	if outcome.WorldSeq == 0 || outcome.WorldSeq > maxInteroperableInteger {
		return invalid("world_seq", "must be a positive JSON-safe integer")
	}
	if err := outcome.OccurredAt.Validate("occurred_at"); err != nil {
		return err
	}
	return nil
}

func descriptorDigest(descriptor CapabilityDescriptor) (string, error) {
	payload := struct {
		Capability         CapabilityRef     `json:"capability"`
		Description        string            `json:"description"`
		InputSHA256        string            `json:"input_sha256"`
		OutputSHA256       string            `json:"output_sha256"`
		Effect             EffectClass       `json:"effect"`
		Execution          ExecutionMode     `json:"execution"`
		Risk               RiskLevel         `json:"risk"`
		RequiredDurability DurabilityProfile `json:"required_durability"`
		RequiredScopes     []string          `json:"required_scopes,omitempty"`
		ExecutionBudget    Duration          `json:"execution_budget"`
		MaxInputBytes      uint32            `json:"max_input_bytes"`
		MaxOutputBytes     uint32            `json:"max_output_bytes"`
		Cancellation       CancellationMode  `json:"cancellation"`
		Reversible         bool              `json:"reversible"`
	}{
		Capability:         descriptor.Capability,
		Description:        descriptor.Description,
		InputSHA256:        descriptor.Input.SHA256,
		OutputSHA256:       descriptor.Output.SHA256,
		Effect:             descriptor.Effect,
		Execution:          descriptor.Execution,
		Risk:               descriptor.Risk,
		RequiredDurability: descriptor.RequiredDurability,
		RequiredScopes:     descriptor.RequiredScopes,
		ExecutionBudget:    descriptor.ExecutionBudget,
		MaxInputBytes:      descriptor.MaxInputBytes,
		MaxOutputBytes:     descriptor.MaxOutputBytes,
		Cancellation:       descriptor.Cancellation,
		Reversible:         descriptor.Reversible,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode descriptor digest: %w", err)
	}
	return sha256Hex(encoded), nil
}

func validateUniqueClockModes(values []ClockMode) error {
	if len(values) == 0 || len(values) > 3 {
		return invalid("clock_modes", "must contain 1-3 values")
	}
	seen := make(map[ClockMode]struct{}, len(values))
	for index, value := range values {
		if value != ClockEvent && value != ClockStep && value != ClockRealtime {
			return invalid(fmt.Sprintf("clock_modes[%d]", index), "is not supported")
		}
		if _, exists := seen[value]; exists {
			return invalid("clock_modes", "must not contain duplicates")
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateUniqueDecisionModes(values []DecisionMode) error {
	if len(values) == 0 || len(values) > 3 {
		return invalid("decision_modes", "must contain 1-3 values")
	}
	seen := make(map[DecisionMode]struct{}, len(values))
	for index, value := range values {
		if value != DecisionSequential && value != DecisionSimultaneous &&
			value != DecisionAsynchronous {
			return invalid(fmt.Sprintf("decision_modes[%d]", index), "is not supported")
		}
		if _, exists := seen[value]; exists {
			return invalid("decision_modes", "must not contain duplicates")
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateRefs(field string, refs []HostRef, epoch Epoch) error {
	if len(refs) > 32 {
		return invalid(field, "must contain at most 32 values")
	}
	seen := make(map[string]struct{}, len(refs))
	for index, ref := range refs {
		itemField := fmt.Sprintf("%s[%d]", field, index)
		if err := ref.Validate(itemField); err != nil {
			return err
		}
		if ref.Epoch != epoch {
			return invalid(itemField+".epoch", "must equal action epoch")
		}
		key := ref.Namespace + "\x00" + ref.Type + "\x00" + ref.Key
		if _, exists := seen[key]; exists {
			return invalid(field, "must not contain duplicates")
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateJSONObject(field string, raw json.RawMessage, maximum int) error {
	if len(raw) == 0 || len(raw) > maximum {
		return invalid(field, "must be a bounded JSON object")
	}
	if err := jsonwire.Validate(raw); err != nil {
		return invalid(field, err.Error())
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return invalid(field, "must be valid JSON: "+err.Error())
	}
	if err := requireJSONEOF(decoder); err != nil {
		return invalid(field, err.Error())
	}
	if _, ok := value.(map[string]any); !ok {
		return invalid(field, "must be a JSON object")
	}
	return nil
}

func validateExactVersion(field, value string) error {
	match := versionCore.FindStringSubmatch(value)
	if match == nil {
		return invalid(field, "must be an exact SemVer version")
	}
	if err := validateVersionIdentifiers(match[4], true); err != nil {
		return invalid(field, err.Error())
	}
	if err := validateVersionIdentifiers(match[5], false); err != nil {
		return invalid(field, err.Error())
	}
	return nil
}

func validateVersionIdentifiers(value string, prerelease bool) error {
	if value == "" {
		return nil
	}
	for _, identifier := range strings.Split(value, ".") {
		if identifier == "" {
			return errors.New("version identifiers must not be empty")
		}
		if prerelease && len(identifier) > 1 && identifier[0] == '0' {
			if _, err := strconv.ParseUint(identifier, 10, 64); err == nil {
				return errors.New("numeric prerelease identifiers must not contain leading zeroes")
			}
		}
	}
	return nil
}

func validateHostID(field, value string, namespaced bool) error {
	if len(value) == 0 || len(value) > 128 || !hostIDPattern.MatchString(value) {
		return invalid(field, "must be a lowercase safe identifier of at most 128 bytes")
	}
	if namespaced && !strings.ContainsRune(value, '.') {
		return invalid(field, "must be namespaced")
	}
	return nil
}

func validateText(field, value string, maximum int, required bool) error {
	if required && strings.TrimSpace(value) == "" {
		return invalid(field, "is required")
	}
	if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return invalid(field, "must be valid UTF-8 without NUL")
	}
	if utf8.RuneCountInString(value) > maximum {
		return invalid(field, fmt.Sprintf("must contain at most %d characters", maximum))
	}
	return nil
}

func prefixValidation(prefix string, err error) error {
	var validation *ValidationError
	if !errors.As(err, &validation) {
		return err
	}
	return invalid(prefix+"."+validation.Field, validation.Message)
}

func cloneDescriptor(descriptor CapabilityDescriptor) CapabilityDescriptor {
	copyDescriptor := descriptor
	copyDescriptor.Input.Document = append(json.RawMessage(nil), descriptor.Input.Document...)
	copyDescriptor.Output.Document = append(json.RawMessage(nil), descriptor.Output.Document...)
	copyDescriptor.RequiredScopes = append([]string(nil), descriptor.RequiredScopes...)
	return copyDescriptor
}

func validAuthorityMode(value AuthorityMode) bool {
	return value == AuthorityStandalone || value == AuthorityServer ||
		value == AuthorityClientAdvisory
}

func validDeploymentMode(value DeploymentMode) bool {
	return value == DeploymentLoopbackSidecar || value == DeploymentDedicatedServer ||
		value == DeploymentRemoteHTTPS || value == DeploymentEmbeddedOffline ||
		value == DeploymentComputerControl
}

func validControlMode(value ControlMode) bool {
	return value == ControlSemantic || value == ControlAccessibility ||
		value == ControlComputerControl
}

func validClockMode(value ClockMode) bool {
	return value == ClockEvent || value == ClockStep || value == ClockRealtime
}

func validDurabilityProfile(value DurabilityProfile) bool {
	return value == DurabilityAdvisory || value == DurabilityIdempotent ||
		value == DurabilityTransactional
}

func validEffectClass(value EffectClass) bool {
	return value == EffectRead || value == EffectAdvisory || value == EffectWorldMutation
}

func validExecutionMode(value ExecutionMode) bool {
	return value == ExecutionImmediate || value == ExecutionQueued ||
		value == ExecutionLongRunning
}

func validRiskLevel(value RiskLevel) bool {
	return value == RiskLow || value == RiskModerate || value == RiskHigh ||
		value == RiskCritical
}

func validCancellationMode(value CancellationMode) bool {
	return value == CancellationUnsupported || value == CancellationCooperative ||
		value == CancellationPreemptive
}

func validActionRunStatus(value ActionRunStatus) bool {
	return value == ActionQueued || value == ActionRunning || value == ActionSucceeded ||
		value == ActionFailed || value == ActionCancelled || value == ActionInterrupted ||
		value == ActionStale || value == ActionOutcomeUnknown
}

func isTerminalActionRunStatus(value ActionRunStatus) bool {
	return value == ActionSucceeded || value == ActionFailed || value == ActionCancelled ||
		value == ActionInterrupted || value == ActionStale
}
