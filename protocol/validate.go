package protocol

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
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
	if err := validateID("binding.game_id", binding.GameID); err != nil {
		return err
	}
	if err := validateID("binding.content_id", binding.ContentID); err != nil {
		return err
	}
	if err := validateText("binding.content_version", binding.ContentVersion, 64, true); err != nil {
		return err
	}
	return validateText("binding.content_hash", binding.ContentHash, 128, true)
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
	outcomeReporting := HasFeature(request.Features, FeatureOutcomeReporting)
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
			if outcomeReporting && goal.Status == "active" && goal.Progress >= goal.TargetProgress {
				return &ValidationError{
					Field:   fmt.Sprintf("actors[%d].goals[%d].status", index, goalIndex),
					Message: "active status must match initial progress when outcome-reporting-v1 is enabled",
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
	if request.Tick < 0 {
		return &ValidationError{Field: "tick", Message: "must not be negative"}
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
	return nil
}

func validateAction(field string, action ActionSpec) error {
	if err := validateID(field+".id", action.ID); err != nil {
		return err
	}
	if err := validateID(field+".kind", action.Kind); err != nil {
		return err
	}
	if err := validateText(field+".description", action.Description, 300, true); err != nil {
		return err
	}
	if err := validateTags(field+".target_ids", action.TargetIDs, 32); err != nil {
		return err
	}
	if len(action.Parameters) > 32 {
		return &ValidationError{Field: field + ".parameters", Message: "must contain at most 32 values"}
	}
	for key, value := range action.Parameters {
		if err := validateID(field+".parameters key", key); err != nil {
			return err
		}
		if err := validateText(field+".parameters."+key, value, 500, false); err != nil {
			return err
		}
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
	if request.Tick < 0 {
		return &ValidationError{Field: "tick", Message: "must not be negative"}
	}
	if err := validateText("intent", request.Intent, 500, true); err != nil {
		return err
	}
	if err := validateTags("tags", request.Tags, 32); err != nil {
		return err
	}
	if len(request.CandidateActions) == 0 || len(request.CandidateActions) > 32 {
		return &ValidationError{Field: "candidate_actions", Message: "must contain 1-32 actions"}
	}
	seen := make(map[string]struct{}, len(request.CandidateActions))
	for index, action := range request.CandidateActions {
		if err := validateAction(fmt.Sprintf("candidate_actions[%d]", index), action); err != nil {
			return err
		}
		if _, exists := seen[action.ID]; exists {
			return &ValidationError{Field: "candidate_actions", Message: "action ids must be unique"}
		}
		seen[action.ID] = struct{}{}
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

func ValidateCommit(request CommitRequest) error {
	if err := validateVersion(request.ProtocolVersion); err != nil {
		return err
	}
	for field, value := range map[string]string{"session_id": request.SessionID, "request_id": request.RequestID, "proposal_id": request.ProposalID, "event_id": request.EventID} {
		if err := validateID(field, value); err != nil {
			return err
		}
	}
	if request.Tick < 0 {
		return &ValidationError{Field: "tick", Message: "must not be negative"}
	}
	if err := validateText("outcome", request.Outcome, 1000, request.Accepted); err != nil {
		return err
	}
	if err := validateTags("tags", request.Tags, 32); err != nil {
		return err
	}
	if len(request.Facts) > 64 || len(request.GoalUpdates) > 32 {
		return &ValidationError{Field: "commit", Message: "contains too many updates"}
	}
	for index, fact := range request.Facts {
		if err := validateRequestFact(fmt.Sprintf("facts[%d]", index), fact); err != nil {
			return err
		}
	}
	for index, update := range request.GoalUpdates {
		base := fmt.Sprintf("goal_updates[%d]", index)
		if err := validateID(base+".goal_id", update.GoalID); err != nil {
			return err
		}
		if update.ProgressDelta < -1000 || update.ProgressDelta > 1000 {
			return &ValidationError{Field: base + ".progress_delta", Message: "must be between -1000 and 1000"}
		}
		if update.Status != "" && update.Status != "active" && update.Status != "completed" && update.Status != "released" {
			return &ValidationError{Field: base + ".status", Message: "must be active, completed, or released"}
		}
	}
	return nil
}

func ValidateSessionRequest(request SessionRequest) error {
	if err := validateVersion(request.ProtocolVersion); err != nil {
		return err
	}
	return validateID("session_id", request.SessionID)
}

func ValidateDueAgents(request DueAgentsRequest) error {
	if err := validateVersion(request.ProtocolVersion); err != nil {
		return err
	}
	if err := validateID("session_id", request.SessionID); err != nil {
		return err
	}
	if request.Tick < 0 {
		return &ValidationError{Field: "tick", Message: "must not be negative"}
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
	if request.Snapshot.State.SessionID != request.SessionID {
		return &ValidationError{Field: "snapshot.state.session_id", Message: "must match session_id"}
	}
	return nil
}
