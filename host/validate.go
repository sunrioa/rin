package host

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/sunrioa/rin/internal/jsonwire"
)

var (
	hostIDPattern  = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`)
	versionCore    = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z.-]+))?(?:\+([0-9A-Za-z.-]+))?$`)
	lowerHexSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

const (
	maxHostIdentifierBytes  = 96
	maxInteroperableInteger = 9_007_199_254_740_991
)

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

// ValidatePrincipal verifies one trusted host authority identity.
func ValidatePrincipal(principal Principal) error {
	if err := validateHostID("principal.id", principal.ID, false); err != nil {
		return err
	}
	if len(principal.GrantedScopes) > 64 {
		return invalid("principal.granted_scopes", "must contain at most 64 values")
	}
	seen := make(map[string]struct{}, len(principal.GrantedScopes))
	for index, scope := range principal.GrantedScopes {
		field := fmt.Sprintf("principal.granted_scopes[%d]", index)
		if err := validateHostID(field, scope, true); err != nil {
			return err
		}
		if _, exists := seen[scope]; exists {
			return invalid("principal.granted_scopes", "must not contain duplicates")
		}
		seen[scope] = struct{}{}
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
	if len(value) == 0 ||
		len(value) > maxHostIdentifierBytes ||
		!hostIDPattern.MatchString(value) {
		return invalid(
			field,
			"must be a lowercase safe identifier of at most 96 bytes",
		)
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
		value == ActionInterrupted || value == ActionStale || value == ActionOutcomeUnknown
}
