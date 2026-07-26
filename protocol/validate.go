package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/sunrioa/rin/host"
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,95}$`)

type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

func IsValidationError(err error) bool {
	var target *ValidationError
	return errors.As(err, &target)
}

func validateVersion(value string) error {
	if value != Version {
		return &ValidationError{Field: "protocol_version", Message: "must equal " + Version}
	}
	return nil
}

func validateID(field, value string) error {
	if !identifierPattern.MatchString(value) {
		return &ValidationError{Field: field, Message: "must be 1-96 safe identifier characters"}
	}
	return nil
}

// ValidateIdentifier applies the public protocol identifier grammar to a wire
// value such as an HTTP path parameter.
func ValidateIdentifier(field, value string) error {
	return validateID(field, value)
}

func validateText(field, value string, maximum int, required bool) error {
	if required && strings.TrimSpace(value) == "" {
		return &ValidationError{Field: field, Message: "is required"}
	}
	if !utf8.ValidString(value) {
		return &ValidationError{Field: field, Message: "must be valid UTF-8"}
	}
	if strings.ContainsRune(value, 0) {
		return &ValidationError{Field: field, Message: "must not contain NUL"}
	}
	if utf8.RuneCountInString(value) > maximum {
		return &ValidationError{Field: field, Message: fmt.Sprintf("must be at most %d characters", maximum)}
	}
	return nil
}

func validateTags(field string, values []string, maximum int) error {
	if len(values) > maximum {
		return &ValidationError{Field: field, Message: fmt.Sprintf("must contain at most %d values", maximum)}
	}
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		if err := validateID(fmt.Sprintf("%s[%d]", field, index), value); err != nil {
			return err
		}
		if _, exists := seen[value]; exists {
			return &ValidationError{Field: field, Message: "must not contain duplicates"}
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateFeatures(field string, values []string) error {
	if err := validateTags(field, values, len(supportedFeatures)); err != nil {
		return err
	}
	for index, value := range values {
		if !IsSupportedFeature(value) {
			return &ValidationError{Field: fmt.Sprintf("%s[%d]", field, index), Message: "is not supported"}
		}
	}
	return nil
}

func ValidateBinding(binding Binding) error {
	return validateBinding("binding", binding)
}

func validateBinding(field string, binding Binding) error {
	if err := validateID(field+".game_id", binding.GameID); err != nil {
		return err
	}
	if err := validateID(field+".content_id", binding.ContentID); err != nil {
		return err
	}
	if err := validateText(field+".content_version", binding.ContentVersion, 64, true); err != nil {
		return err
	}
	return validateText(field+".content_hash", binding.ContentHash, 128, true)
}

func validateGoal(field string, goal Goal) error {
	if err := validateID(field+".id", goal.ID); err != nil {
		return err
	}
	if err := validateText(field+".description", goal.Description, 300, true); err != nil {
		return err
	}
	if err := validateText(field+".motivation", goal.Motivation, 300, false); err != nil {
		return err
	}
	if goal.Priority < 1 || goal.Priority > 5 {
		return &ValidationError{Field: field + ".priority", Message: "must be between 1 and 5"}
	}
	if goal.TargetProgress < 1 || goal.TargetProgress > 1000 {
		return &ValidationError{Field: field + ".target_progress", Message: "must be between 1 and 1000"}
	}
	if goal.Progress < 0 || goal.Progress > goal.TargetProgress {
		return &ValidationError{Field: field + ".progress", Message: "must be between 0 and target_progress"}
	}
	if goal.UpdatedTick < 0 {
		return &ValidationError{Field: field + ".updated_tick", Message: "must not be negative"}
	}
	if goal.StatusUpdatedTick < 0 {
		return &ValidationError{Field: field + ".status_updated_tick", Message: "must not be negative"}
	}
	if goal.StatusSourceEventID != "" {
		if err := validateID(field+".status_source_event_id", goal.StatusSourceEventID); err != nil {
			return err
		}
	}
	if goal.Status != "active" && goal.Status != "completed" && goal.Status != "released" {
		return &ValidationError{Field: field + ".status", Message: "must be active, completed, or released"}
	}
	return validateTags(field+".preferred_actions", goal.PreferredActions, 16)
}

func validateActor(field string, actor ActorSeed) error {
	if err := validateID(field+".id", actor.ID); err != nil {
		return err
	}
	if err := validateID(field+".kind", actor.Kind); err != nil {
		return err
	}
	if err := validateText(field+".display_name", actor.DisplayName, 120, true); err != nil {
		return err
	}
	if err := validateTags(field+".traits", actor.Traits, 24); err != nil {
		return err
	}
	if actor.ThinkEveryTicks < 1 || actor.ThinkEveryTicks > 1_000_000 {
		return &ValidationError{Field: field + ".think_every_ticks", Message: "must be between 1 and 1000000"}
	}
	if len(actor.Boundaries) > 24 {
		return &ValidationError{Field: field + ".boundaries", Message: "must contain at most 24 values"}
	}
	boundaryIDs := make(map[string]struct{}, len(actor.Boundaries))
	for index, boundary := range actor.Boundaries {
		base := fmt.Sprintf("%s.boundaries[%d]", field, index)
		if err := validateID(base+".id", boundary.ID); err != nil {
			return err
		}
		if _, exists := boundaryIDs[boundary.ID]; exists {
			return &ValidationError{Field: field + ".boundaries", Message: "boundary ids must be unique"}
		}
		boundaryIDs[boundary.ID] = struct{}{}
		if err := validateText(base+".description", boundary.Description, 300, true); err != nil {
			return err
		}
		if err := validateTags(base+".trigger_tags", boundary.TriggerTags, 16); err != nil {
			return err
		}
		if boundary.Response != "refuse" && boundary.Response != "redirect" && boundary.Response != "wait" {
			return &ValidationError{Field: base + ".response", Message: "must be refuse, redirect, or wait"}
		}
	}
	if len(actor.Goals) > 32 {
		return &ValidationError{Field: field + ".goals", Message: "must contain at most 32 values"}
	}
	goalIDs := make(map[string]struct{}, len(actor.Goals))
	for index, goal := range actor.Goals {
		if err := validateGoal(fmt.Sprintf("%s.goals[%d]", field, index), goal); err != nil {
			return err
		}
		if _, exists := goalIDs[goal.ID]; exists {
			return &ValidationError{Field: field + ".goals", Message: "goal ids must be unique"}
		}
		goalIDs[goal.ID] = struct{}{}
	}
	if len(actor.Metadata) > 32 {
		return &ValidationError{Field: field + ".metadata", Message: "must contain at most 32 values"}
	}
	for key, value := range actor.Metadata {
		if err := validateID(field+".metadata key", key); err != nil {
			return err
		}
		if err := validateText(field+".metadata."+key, value, 500, false); err != nil {
			return err
		}
	}
	return nil
}

func ValidateCreateSession(request CreateSessionRequest) error {
	if err := validateVersion(request.ProtocolVersion); err != nil {
		return err
	}
	if err := validateJSONSafeSigned("seed", request.Seed); err != nil {
		return err
	}
	if err := validateID("request_id", request.RequestID); err != nil {
		return err
	}
	if err := validateID("session_id", request.SessionID); err != nil {
		return err
	}
	if err := ValidateBinding(request.Binding); err != nil {
		return err
	}
	if err := validateFeatures("features", request.Features); err != nil {
		return err
	}
	if len(request.Actors) == 0 || len(request.Actors) > 128 {
		return &ValidationError{Field: "actors", Message: "must contain 1-128 actors"}
	}
	seen := make(map[string]struct{}, len(request.Actors))
	for index, actor := range request.Actors {
		if err := validateActor(fmt.Sprintf("actors[%d]", index), actor); err != nil {
			return err
		}
		for goalIndex, goal := range actor.Goals {
			if goal.UpdatedTick != 0 ||
				goal.ProgressAccumulator != 0 ||
				goal.StatusExplicit ||
				goal.StatusUpdatedTick != 0 ||
				goal.StatusSourceEventID != "" {
				return &ValidationError{
					Field:   fmt.Sprintf("actors[%d].goals[%d]", index, goalIndex),
					Message: "server-owned occurrence metadata must be zero when creating a session",
				}
			}
			if goal.Status == "active" && goal.Progress >= goal.TargetProgress {
				return &ValidationError{
					Field:   fmt.Sprintf("actors[%d].goals[%d].status", index, goalIndex),
					Message: "active status must match initial progress",
				}
			}
		}
		if _, exists := seen[actor.ID]; exists {
			return &ValidationError{Field: "actors", Message: "actor ids must be unique"}
		}
		seen[actor.ID] = struct{}{}
	}
	return nil
}

func validateFact(field string, fact Fact) error {
	if err := validateID(field+".subject_id", fact.SubjectID); err != nil {
		return err
	}
	if err := validateID(field+".predicate", fact.Predicate); err != nil {
		return err
	}
	if err := validateText(field+".object", fact.Object, 500, true); err != nil {
		return err
	}
	if err := validateTags(field+".visibility", fact.Visibility, 32); err != nil {
		return err
	}
	if fact.Confidence < 0 || fact.Confidence > 100 {
		return &ValidationError{Field: field + ".confidence", Message: "must be between 0 and 100"}
	}
	if fact.ObservedTick < 0 {
		return &ValidationError{Field: field + ".observed_tick", Message: "must not be negative"}
	}
	if fact.SourceEventID != "" {
		if err := validateID(field+".source_event_id", fact.SourceEventID); err != nil {
			return err
		}
	}
	return nil
}

func validateRequestFact(field string, fact Fact) error {
	if err := validateFact(field, fact); err != nil {
		return err
	}
	if fact.ObservedTick != 0 {
		return &ValidationError{
			Field:   field + ".observed_tick",
			Message: "is server-owned and must be zero in requests",
		}
	}
	return nil
}

func ValidateObserve(request ObserveRequest) error {
	if err := validateVersion(request.ProtocolVersion); err != nil {
		return err
	}
	for field, value := range map[string]string{"session_id": request.SessionID, "request_id": request.RequestID, "event_id": request.EventID, "source": request.Source, "kind": request.Kind} {
		if err := validateID(field, value); err != nil {
			return err
		}
	}
	if err := validateJSONSafeTick("tick", request.Tick); err != nil {
		return err
	}
	if len(request.ObserverIDs) == 0 || len(request.ObserverIDs) > 128 {
		return &ValidationError{Field: "observer_ids", Message: "must contain 1-128 actors"}
	}
	if err := validateTags("observer_ids", request.ObserverIDs, 128); err != nil {
		return err
	}
	if err := validateText("summary", request.Summary, 1000, true); err != nil {
		return err
	}
	if err := validateText("quote", request.Quote, 500, false); err != nil {
		return err
	}
	if err := validateTags("tags", request.Tags, 32); err != nil {
		return err
	}
	if request.Importance < 1 || request.Importance > 5 {
		return &ValidationError{Field: "importance", Message: "must be between 1 and 5"}
	}
	if len(request.Facts) > 64 {
		return &ValidationError{Field: "facts", Message: "must contain at most 64 values"}
	}
	for index, fact := range request.Facts {
		if err := validateRequestFact(fmt.Sprintf("facts[%d]", index), fact); err != nil {
			return err
		}
	}
	if err := request.Epoch.Validate("epoch"); err != nil {
		return protocolHostError("", err)
	}
	if request.ObservationSeq == 0 ||
		request.ObservationSeq > uint64(MaxJSONSafeInteger) {
		return &ValidationError{Field: "observation_seq", Message: "must be a positive JSON-safe integer"}
	}
	if request.Payload != nil {
		if err := validateStructuredPayload("payload", *request.Payload); err != nil {
			return err
		}
	}
	if len(request.Artifacts) > 16 {
		return &ValidationError{Field: "artifacts", Message: "must contain at most 16 values"}
	}
	artifactIDs := make(map[string]struct{}, len(request.Artifacts))
	for index, artifact := range request.Artifacts {
		if err := validateArtifactRef(fmt.Sprintf("artifacts[%d]", index), artifact); err != nil {
			return err
		}
		if _, exists := artifactIDs[artifact.ID]; exists {
			return &ValidationError{Field: "artifacts", Message: "artifact ids must be unique"}
		}
		artifactIDs[artifact.ID] = struct{}{}
	}
	return nil
}

func ValidatePropose(request ProposeRequest) error {
	if err := validateVersion(request.ProtocolVersion); err != nil {
		return err
	}
	for field, value := range map[string]string{"session_id": request.SessionID, "request_id": request.RequestID, "actor_id": request.ActorID} {
		if err := validateID(field, value); err != nil {
			return err
		}
	}
	if err := validateJSONSafeTick("tick", request.Tick); err != nil {
		return err
	}
	if err := validateText("intent", request.Intent, 500, true); err != nil {
		return err
	}
	if err := validateTags("tags", request.Tags, 32); err != nil {
		return err
	}
	if err := validateDecisionWindow("decision_window", request.DecisionWindow); err != nil {
		return err
	}
	if !containsString(request.DecisionWindow.ActorIDs, request.ActorID) {
		return &ValidationError{Field: "actor_id", Message: "must belong to decision_window.actor_ids"}
	}
	if len(request.Offers) == 0 || len(request.Offers) > 32 {
		return &ValidationError{Field: "offers", Message: "must contain 1-32 action offers"}
	}
	seen := make(map[string]struct{}, len(request.Offers))
	for index, offer := range request.Offers {
		field := fmt.Sprintf("offers[%d]", index)
		if err := host.ValidateActionOffer(offer); err != nil {
			return protocolHostError(field, err)
		}
		if offer.ActorID != request.ActorID {
			return &ValidationError{Field: field + ".actor_id", Message: "must equal actor_id"}
		}
		if offer.DecisionWindowID != request.DecisionWindow.ID {
			return &ValidationError{Field: field + ".decision_window_id", Message: "must equal decision_window.id"}
		}
		if offer.ExpectedEpoch != request.DecisionWindow.Epoch {
			return &ValidationError{Field: field + ".expected_epoch", Message: "must equal decision_window.epoch"}
		}
		if offer.ObservationSeq != request.DecisionWindow.ObservationSeq {
			return &ValidationError{Field: field + ".observation_seq", Message: "must equal decision_window.observation_seq"}
		}
		if offer.Deadline.Clock != request.DecisionWindow.Deadline.Clock ||
			offer.Deadline.Value > request.DecisionWindow.Deadline.Value {
			return &ValidationError{Field: field + ".deadline", Message: "must not exceed decision_window.deadline"}
		}
		if _, exists := seen[offer.OfferID]; exists {
			return &ValidationError{Field: "offers", Message: "offer ids must be unique"}
		}
		seen[offer.OfferID] = struct{}{}
	}
	if request.DecisionWindow.Mode == host.DecisionSequential &&
		len(request.DecisionWindow.ActorIDs) != 1 {
		return &ValidationError{
			Field:   "decision_window.actor_ids",
			Message: "sequential windows must contain exactly one actor",
		}
	}
	if len(request.CandidateGoals) > 8 {
		return &ValidationError{Field: "candidate_goals", Message: "must contain at most 8 goals"}
	}
	goalIDs := make(map[string]struct{}, len(request.CandidateGoals))
	for index, goal := range request.CandidateGoals {
		field := fmt.Sprintf("candidate_goals[%d]", index)
		if err := validateGoal(field, goal); err != nil {
			return err
		}
		if goal.Progress != 0 ||
			goal.Status != "active" ||
			goal.UpdatedTick != 0 ||
			goal.ProgressAccumulator != 0 ||
			goal.StatusExplicit ||
			goal.StatusUpdatedTick != 0 ||
			goal.StatusSourceEventID != "" {
			return &ValidationError{Field: field, Message: "candidate goals must be active with zero progress and no state metadata"}
		}
		if _, exists := goalIDs[goal.ID]; exists {
			return &ValidationError{Field: "candidate_goals", Message: "goal ids must be unique"}
		}
		goalIDs[goal.ID] = struct{}{}
	}
	return nil
}

// ValidateReportAction validates one host-owned action lifecycle report.
func ValidateReportAction(request ReportActionRequest) error {
	if err := validateVersion(request.ProtocolVersion); err != nil {
		return err
	}
	for field, value := range map[string]string{
		"session_id": request.SessionID,
		"request_id": request.RequestID,
	} {
		if err := validateID(field, value); err != nil {
			return err
		}
	}
	if err := validateJSONSafeTick("tick", request.Tick); err != nil {
		return err
	}
	return validateActionReport("report", request.Report)
}

func validateActionReport(field string, report ActionReport) error {
	if err := validateID(field+".proposal_id", report.ProposalID); err != nil {
		return err
	}
	if err := validateID(field+".event_id", report.EventID); err != nil {
		return err
	}
	if report.Decision != ActionAccepted && report.Decision != ActionRejected {
		return &ValidationError{Field: field + ".decision", Message: "must be accepted or rejected"}
	}
	if err := validateText(field+".summary", report.Summary, 1000, true); err != nil {
		return err
	}
	if err := validateTags(field+".tags", report.Tags, 32); err != nil {
		return err
	}
	if len(report.Facts) > 64 || len(report.GoalUpdates) > 32 {
		return &ValidationError{Field: field, Message: "contains too many updates"}
	}
	if report.Decision == ActionRejected {
		if report.Invocation != nil || report.Run != nil || report.Outcome != nil {
			return &ValidationError{
				Field:   field + ".decision",
				Message: "rejected reports must not include invocation, run, or outcome",
			}
		}
		if len(report.Facts) > 0 || len(report.GoalUpdates) > 0 {
			return &ValidationError{
				Field:   field + ".decision",
				Message: "rejected reports must not include effect updates",
			}
		}
		return nil
	}
	if report.Invocation == nil {
		return &ValidationError{Field: field + ".invocation", Message: "is required for accepted reports"}
	}
	if err := host.ValidateActionInvocation(*report.Invocation); err != nil {
		return protocolHostError(field+".invocation", err)
	}
	if report.Run == nil {
		return &ValidationError{Field: field + ".run", Message: "is required for accepted reports"}
	}
	if err := host.ValidateActionRun(*report.Run); err != nil {
		return protocolHostError(field+".run", err)
	}
	if report.Invocation.OperationID != report.Run.OperationID {
		return &ValidationError{Field: field + ".run.operation_id", Message: "must equal invocation.operation_id"}
	}
	terminal := isTerminalActionStatus(report.Run.Status)
	if terminal && report.Outcome == nil {
		return &ValidationError{Field: field + ".outcome", Message: "is required for a terminal run"}
	}
	if !terminal && report.Outcome != nil {
		return &ValidationError{Field: field + ".outcome", Message: "must be absent until the run is terminal"}
	}
	if report.Outcome != nil {
		if err := host.ValidateActionOutcome(*report.Outcome); err != nil {
			return protocolHostError(field+".outcome", err)
		}
		if report.Outcome.OperationID != report.Run.OperationID {
			return &ValidationError{Field: field + ".outcome.operation_id", Message: "must equal run.operation_id"}
		}
		if report.Outcome.Status != report.Run.Status {
			return &ValidationError{Field: field + ".outcome.status", Message: "must equal run.status"}
		}
		if report.Outcome.Epoch != report.Invocation.ExpectedEpoch {
			return &ValidationError{Field: field + ".outcome.epoch", Message: "must equal invocation.expected_epoch"}
		}
	}
	if !terminal && (len(report.Facts) > 0 || len(report.GoalUpdates) > 0) {
		return &ValidationError{
			Field:   field + ".run.status",
			Message: "effect updates require a terminal run",
		}
	}
	for index, fact := range report.Facts {
		if err := validateRequestFact(fmt.Sprintf("%s.facts[%d]", field, index), fact); err != nil {
			return err
		}
	}
	goalIDs := make(map[string]struct{}, len(report.GoalUpdates))
	for index, update := range report.GoalUpdates {
		base := fmt.Sprintf("%s.goal_updates[%d]", field, index)
		if err := validateID(base+".goal_id", update.GoalID); err != nil {
			return err
		}
		if update.ProgressDelta < -1000 || update.ProgressDelta > 1000 {
			return &ValidationError{Field: base + ".progress_delta", Message: "must be between -1000 and 1000"}
		}
		if update.Status != "" && update.Status != "active" && update.Status != "completed" && update.Status != "released" {
			return &ValidationError{Field: base + ".status", Message: "must be active, completed, or released"}
		}
		if _, exists := goalIDs[update.GoalID]; exists {
			return &ValidationError{Field: field + ".goal_updates", Message: "goal ids must be unique"}
		}
		goalIDs[update.GoalID] = struct{}{}
	}
	return nil
}

func validateDecisionWindow(field string, window DecisionWindow) error {
	if err := validateID(field+".id", window.ID); err != nil {
		return err
	}
	if window.Mode != host.DecisionSequential &&
		window.Mode != host.DecisionSimultaneous &&
		window.Mode != host.DecisionAsynchronous {
		return &ValidationError{Field: field + ".mode", Message: "is not supported"}
	}
	if err := window.Epoch.Validate(field + ".epoch"); err != nil {
		return protocolHostError("", err)
	}
	if window.ObservationSeq == 0 ||
		window.ObservationSeq > uint64(MaxJSONSafeInteger) {
		return &ValidationError{Field: field + ".observation_seq", Message: "must be a positive JSON-safe integer"}
	}
	if err := window.OpenedAt.Validate(field + ".opened_at"); err != nil {
		return protocolHostError("", err)
	}
	if err := window.Deadline.Validate(field + ".deadline"); err != nil {
		return protocolHostError("", err)
	}
	if window.OpenedAt.Clock != window.Deadline.Clock {
		return &ValidationError{Field: field + ".deadline.clock", Message: "must equal opened_at.clock"}
	}
	if window.Deadline.Value <= window.OpenedAt.Value {
		return &ValidationError{Field: field + ".deadline.value", Message: "must be after opened_at.value"}
	}
	if len(window.ActorIDs) == 0 || len(window.ActorIDs) > 128 {
		return &ValidationError{Field: field + ".actor_ids", Message: "must contain 1-128 actors"}
	}
	return validateTags(field+".actor_ids", window.ActorIDs, 128)
}

func validateProtocolOffer(field string, offer ActionOffer) error {
	if err := host.ValidateActionOffer(offer); err != nil {
		return protocolHostError(field, err)
	}
	return nil
}

func validateStructuredPayload(field string, payload StructuredPayload) error {
	if err := (host.CapabilityRef{
		ID: payload.Schema.ID, Version: payload.Schema.Version,
	}).Validate(field + ".schema"); err != nil {
		return protocolHostError("", err)
	}
	if !hashPattern.MatchString(payload.Schema.Digest) {
		return &ValidationError{Field: field + ".schema.digest", Message: "must be a lowercase SHA-256 hash"}
	}
	if len(payload.Data) == 0 || len(payload.Data) > 256<<10 {
		return &ValidationError{Field: field + ".data", Message: "must contain 1-262144 bytes of JSON"}
	}
	decoder := json.NewDecoder(bytes.NewReader(payload.Data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return &ValidationError{Field: field + ".data", Message: "must be valid JSON"}
	}
	if _, err := decoder.Token(); err != io.EOF {
		return &ValidationError{Field: field + ".data", Message: "must contain exactly one JSON value"}
	}
	return nil
}

func validateArtifactRef(field string, artifact ArtifactRef) error {
	if err := validateID(field+".id", artifact.ID); err != nil {
		return err
	}
	mediaType, parameters, err := mime.ParseMediaType(artifact.MediaType)
	if err != nil || mediaType == "" || len(parameters) != 0 {
		return &ValidationError{Field: field + ".media_type", Message: "must be a canonical media type without parameters"}
	}
	if err := validateText(field+".uri", artifact.URI, 2048, true); err != nil {
		return err
	}
	parsed, err := url.ParseRequestURI(artifact.URI)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "rin-artifact") {
		return &ValidationError{Field: field + ".uri", Message: "must use https or rin-artifact scheme"}
	}
	if !hashPattern.MatchString(artifact.SHA256) {
		return &ValidationError{Field: field + ".sha256", Message: "must be a lowercase SHA-256 hash"}
	}
	if artifact.SizeBytes == 0 || artifact.SizeBytes > uint64(MaxJSONSafeInteger) {
		return &ValidationError{Field: field + ".size_bytes", Message: "must be a positive JSON-safe integer"}
	}
	return nil
}

// ValidateArtifactRef validates one immutable external artifact independently
// of an Observation request.
func ValidateArtifactRef(artifact ArtifactRef) error {
	return validateArtifactRef("artifact", artifact)
}

func protocolHostError(prefix string, err error) error {
	var validation *host.ValidationError
	if !errors.As(err, &validation) {
		return err
	}
	field := validation.Field
	if prefix != "" {
		field = prefix + "." + field
	}
	return &ValidationError{Field: field, Message: validation.Message}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func isTerminalActionStatus(status host.ActionRunStatus) bool {
	return status == host.ActionSucceeded ||
		status == host.ActionFailed ||
		status == host.ActionCancelled ||
		status == host.ActionInterrupted ||
		status == host.ActionStale
}

func ValidateSessionRequest(request SessionRequest) error {
	if err := validateVersion(request.ProtocolVersion); err != nil {
		return err
	}
	return validateID("session_id", request.SessionID)
}

func ValidateArchiveSession(request ArchiveSessionRequest) error {
	if err := validateVersion(request.ProtocolVersion); err != nil {
		return err
	}
	if err := validateID("session_id", request.SessionID); err != nil {
		return err
	}
	if err := validateID("request_id", request.RequestID); err != nil {
		return err
	}
	if err := validateBinding("expected_binding", request.ExpectedBinding); err != nil {
		return err
	}
	if request.ExpectedRevision == 0 ||
		request.ExpectedRevision > uint64(MaxJSONSafeInteger) {
		return &ValidationError{Field: "expected_revision", Message: "must be a positive JSON-safe integer"}
	}
	if !hashPattern.MatchString(request.ExpectedHeadHash) {
		return &ValidationError{Field: "expected_head_hash", Message: "must be a lowercase SHA-256 hash"}
	}
	return nil
}

func ValidateDeleteSession(request DeleteSessionRequest) error {
	if err := validateVersion(request.ProtocolVersion); err != nil {
		return err
	}
	if err := validateID("session_id", request.SessionID); err != nil {
		return err
	}
	if err := validateID("request_id", request.RequestID); err != nil {
		return err
	}
	if err := validateBinding("expected_binding", request.ExpectedBinding); err != nil {
		return err
	}
	if request.ExpectedRevision == 0 ||
		request.ExpectedRevision > uint64(MaxJSONSafeInteger) {
		return &ValidationError{Field: "expected_revision", Message: "must be a positive JSON-safe integer"}
	}
	if !hashPattern.MatchString(request.ExpectedHeadHash) {
		return &ValidationError{Field: "expected_head_hash", Message: "must be a lowercase SHA-256 hash"}
	}
	if err := validateID("archive_receipt_id", request.ArchiveReceiptID); err != nil {
		return err
	}
	if request.Confirmation != request.SessionID {
		return &ValidationError{Field: "confirmation", Message: "must exactly equal session_id"}
	}
	return nil
}

func ValidateDueAgents(request DueAgentsRequest) error {
	if err := validateVersion(request.ProtocolVersion); err != nil {
		return err
	}
	if err := validateID("session_id", request.SessionID); err != nil {
		return err
	}
	if err := validateJSONSafeTick("tick", request.Tick); err != nil {
		return err
	}
	if request.Limit < 1 || request.Limit > 128 {
		return &ValidationError{Field: "limit", Message: "must be between 1 and 128"}
	}
	return validateTags("region_ids", request.RegionIDs, 32)
}

func ValidateRestore(request RestoreRequest) error {
	if err := validateVersion(request.ProtocolVersion); err != nil {
		return err
	}
	if err := validateID("session_id", request.SessionID); err != nil {
		return err
	}
	if err := validateID("request_id", request.RequestID); err != nil {
		return err
	}
	if err := validateBinding("expected_binding", request.ExpectedBinding); err != nil {
		return err
	}
	if request.Snapshot.State.SessionID != request.SessionID {
		return &ValidationError{Field: "snapshot.state.session_id", Message: "must match session_id"}
	}
	return nil
}
