package protocol

import (
	"fmt"
	"regexp"
)

var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ValidateSessionState checks the structural invariants required for a safe
// snapshot restore. It does not verify StateHash; runtime.ValidateSnapshot does.
func ValidateSessionState(state SessionState) error {
	if err := validateVersion(state.ProtocolVersion); err != nil {
		return err
	}
	if err := validateID("state.session_id", state.SessionID); err != nil {
		return err
	}
	if err := ValidateBinding(state.Binding); err != nil {
		return err
	}
	if err := validateFeatures("state.features", state.Features); err != nil {
		return err
	}
	if state.Tick < 0 {
		return &ValidationError{Field: "state.tick", Message: "must not be negative"}
	}
	if state.Revision == 0 {
		return &ValidationError{Field: "state.revision", Message: "must be greater than zero"}
	}
	arbitration := HasFeature(state.Features, FeatureArbitration)
	if arbitration && state.WorldRevision == 0 {
		return &ValidationError{Field: "state.world_revision", Message: "must be greater than zero when arbitration is enabled"}
	}
	if !arbitration && state.WorldRevision != 0 {
		return &ValidationError{Field: "state.world_revision", Message: "must be zero when arbitration is disabled"}
	}
	if !hashPattern.MatchString(state.HeadHash) {
		return &ValidationError{Field: "state.head_hash", Message: "must be a lowercase SHA-256 hash"}
	}
	outcomeReporting := HasFeature(state.Features, FeatureOutcomeReporting)
	beliefConflicts := HasFeature(state.Features, FeatureBeliefConflicts)
	if len(state.Actors) == 0 || len(state.Actors) > 128 {
		return &ValidationError{Field: "state.actors", Message: "must contain 1-128 actors"}
	}
	actorMemoryIDs := make(map[string]map[string]struct{}, len(state.Actors))
	actorGoalIDs := make(map[string]map[string]struct{}, len(state.Actors))
	for id, actor := range state.Actors {
		base := "state.actors." + id
		if id != actor.ID {
			return &ValidationError{Field: base, Message: "map key must match actor id"}
		}
		if err := validateActor(base, actor.ActorSeed); err != nil {
			return err
		}
		goalIDs := make(map[string]struct{}, len(actor.Goals))
		for index, goal := range actor.Goals {
			goalIDs[goal.ID] = struct{}{}
			if !outcomeReporting &&
				(goal.UpdatedTick != 0 ||
					goal.ProgressAccumulator != 0 ||
					goal.StatusExplicit ||
					goal.StatusUpdatedTick != 0 ||
					goal.StatusSourceEventID != "") {
				return &ValidationError{
					Field:   fmt.Sprintf("%s.goals[%d]", base, index),
					Message: "outcome occurrence metadata requires outcome-reporting-v1",
				}
			}
			if goal.UpdatedTick > state.Tick {
				return &ValidationError{
					Field:   fmt.Sprintf("%s.goals[%d].updated_tick", base, index),
					Message: "must not exceed the session tick",
				}
			}
			if goal.StatusUpdatedTick > goal.UpdatedTick {
				return &ValidationError{
					Field:   fmt.Sprintf("%s.goals[%d].status_updated_tick", base, index),
					Message: "must not exceed updated_tick",
				}
			}
			if !goal.StatusExplicit &&
				(goal.StatusUpdatedTick != 0 || goal.StatusSourceEventID != "") {
				return &ValidationError{
					Field:   fmt.Sprintf("%s.goals[%d]", base, index),
					Message: "automatic status cannot carry explicit status metadata",
				}
			}
			if outcomeReporting {
				expected := goal.ProgressAccumulator
				if expected < 0 {
					expected = 0
				} else if expected > int64(goal.TargetProgress) {
					expected = int64(goal.TargetProgress)
				}
				if int64(goal.Progress) != expected {
					return &ValidationError{
						Field:   fmt.Sprintf("%s.goals[%d].progress", base, index),
						Message: "must be the bounded projection of progress_accumulator",
					}
				}
				expectedStatus := "active"
				if goal.Progress >= goal.TargetProgress {
					expectedStatus = "completed"
				}
				if !goal.StatusExplicit && goal.Status != expectedStatus {
					return &ValidationError{
						Field:   fmt.Sprintf("%s.goals[%d].status", base, index),
						Message: "automatic status must match bounded progress",
					}
				}
				if goal.StatusExplicit {
					if goal.StatusUpdatedTick != 0 && goal.StatusSourceEventID == "" {
						return &ValidationError{
							Field:   fmt.Sprintf("%s.goals[%d].status_source_event_id", base, index),
							Message: "is required for a timestamped explicit status",
						}
					}
					if goal.StatusSourceEventID == "" && goal.Status == "active" {
						return &ValidationError{
							Field:   fmt.Sprintf("%s.goals[%d].status", base, index),
							Message: "an initial explicit status must be completed or released",
						}
					}
				}
			}
		}
		actorGoalIDs[id] = goalIDs
		if actor.NextThinkTick < 0 {
			return &ValidationError{Field: base + ".next_think_tick", Message: "must not be negative"}
		}
		if actor.Activity != nil {
			if !HasFeature(state.Features, FeatureActorActivity) {
				return &ValidationError{Field: base + ".activity", Message: "requires actor-activity-v1"}
			}
			if err := validateActorActivity(base+".activity", *actor.Activity, state); err != nil {
				return err
			}
		}
		if len(actor.Memories) > 128 {
			return &ValidationError{Field: base + ".memories", Message: "must contain at most 128 values"}
		}
		memoryIDs := make(map[string]struct{}, len(actor.Memories)+len(actor.MemorySummaries))
		for index, memory := range actor.Memories {
			field := fmt.Sprintf("%s.memories[%d]", base, index)
			if err := validateMemory(field, memory, state.Revision, state.Tick); err != nil {
				return err
			}
			if _, exists := memoryIDs[memory.ID]; exists {
				return &ValidationError{Field: base + ".memories", Message: "memory ids must be unique"}
			}
			memoryIDs[memory.ID] = struct{}{}
		}
		if len(actor.MemorySummaries) > 32 {
			return &ValidationError{Field: base + ".memory_summaries", Message: "must contain at most 32 values"}
		}
		if len(actor.MemorySummaries) > 0 && !HasFeature(state.Features, FeatureMemoryArchive) {
			return &ValidationError{Field: base + ".memory_summaries", Message: "requires memory-archive-v1"}
		}
		for index, summary := range actor.MemorySummaries {
			field := fmt.Sprintf("%s.memory_summaries[%d]", base, index)
			if err := validateMemorySummary(field, summary, state.Revision, state.Tick); err != nil {
				return err
			}
			if _, exists := memoryIDs[summary.ID]; exists {
				return &ValidationError{Field: base + ".memory_summaries", Message: "memory and summary ids must be unique"}
			}
			memoryIDs[summary.ID] = struct{}{}
		}
		actorMemoryIDs[id] = memoryIDs
		if len(actor.Beliefs) > 256 {
			return &ValidationError{Field: base + ".beliefs", Message: "must contain at most 256 values"}
		}
		for key, fact := range actor.Beliefs {
			field := base + ".beliefs." + key
			if key != fact.SubjectID+":"+fact.Predicate {
				return &ValidationError{Field: field, Message: "map key must match subject and predicate"}
			}
			if err := validateFact(field, fact); err != nil {
				return err
			}
			if fact.ObservedTick > state.Tick {
				return &ValidationError{Field: field + ".observed_tick", Message: "must not exceed the session tick"}
			}
			if !outcomeReporting && fact.ObservedTick != 0 {
				return &ValidationError{Field: field + ".observed_tick", Message: "requires outcome-reporting-v1"}
			}
			if fact.SourceEventID == "" {
				return &ValidationError{Field: field + ".source_event_id", Message: "is required"}
			}
			for _, visibleActor := range fact.Visibility {
				if _, exists := state.Actors[visibleActor]; !exists {
					return &ValidationError{Field: field + ".visibility", Message: "references an unknown actor"}
				}
			}
		}
		if len(actor.BeliefSets) > 256 {
			return &ValidationError{Field: base + ".belief_sets", Message: "must contain at most 256 values"}
		}
		if len(actor.BeliefSets) > 0 && !beliefConflicts {
			return &ValidationError{Field: base + ".belief_sets", Message: "requires belief-conflicts-v1"}
		}
		for key, set := range actor.BeliefSets {
			field := base + ".belief_sets." + key
			if err := validateBeliefSet(field, key, set, state, outcomeReporting); err != nil {
				return err
			}
			selected, exists := actor.Beliefs[key]
			if !exists {
				return &ValidationError{Field: field, Message: "must have a selected compatibility belief"}
			}
			matched := false
			for _, claim := range set.Claims {
				if claim.Fact.SourceEventID == set.SelectedSourceEventID &&
					factsEquivalent(claim.Fact, selected) {
					matched = true
				}
			}
			if !matched {
				return &ValidationError{Field: field + ".selected_source_event_id", Message: "must select the projected belief"}
			}
		}
		if beliefConflicts {
			for key := range actor.Beliefs {
				if _, exists := actor.BeliefSets[key]; !exists {
					return &ValidationError{
						Field:   base + ".beliefs." + key,
						Message: "must have a belief set when belief-conflicts-v1 is enabled",
					}
				}
			}
		}
		if len(actor.RecentActions) > 32 {
			return &ValidationError{Field: base + ".recent_actions", Message: "must contain at most 32 values"}
		}
		recentActionIDs := make(map[string]struct{}, len(actor.RecentActions))
		recentOutcomeIDs := make(map[string]struct{}, len(actor.RecentActions))
		for index, proposal := range actor.RecentActions {
			field := fmt.Sprintf("%s.recent_actions[%d]", base, index)
			if err := validateProposal(field, state, actor, proposal, memoryIDs); err != nil {
				return err
			}
			if proposal.Status != "accepted" {
				return &ValidationError{Field: field + ".status", Message: "must be accepted"}
			}
			if _, exists := recentActionIDs[proposal.ID]; exists {
				return &ValidationError{Field: base + ".recent_actions", Message: "proposal ids must be unique"}
			}
			recentActionIDs[proposal.ID] = struct{}{}
			if proposal.OutcomeEventID != "" {
				if _, exists := recentOutcomeIDs[proposal.OutcomeEventID]; exists {
					return &ValidationError{Field: base + ".recent_actions", Message: "outcome event ids must be unique"}
				}
				recentOutcomeIDs[proposal.OutcomeEventID] = struct{}{}
			}
		}
	}
	if len(state.Proposals) > 64 {
		return &ValidationError{Field: "state.proposals", Message: "must contain at most 64 values"}
	}
	pendingGoalReservations := make(map[string]map[string]struct{}, len(state.Actors))
	for id, proposal := range state.Proposals {
		if id != proposal.ID {
			return &ValidationError{Field: "state.proposals." + id, Message: "map key must match proposal id"}
		}
		actor, exists := state.Actors[proposal.ActorID]
		if !exists {
			return &ValidationError{Field: "state.proposals." + id + ".actor_id", Message: "references an unknown actor"}
		}
		if err := validateProposal("state.proposals."+id, state, actor, proposal, actorMemoryIDs[proposal.ActorID]); err != nil {
			return err
		}
		if proposal.Status != "pending" || proposal.ProposedGoal == nil {
			continue
		}
		goalID := proposal.ProposedGoal.ID
		if _, exists := actorGoalIDs[proposal.ActorID][goalID]; exists {
			return &ValidationError{
				Field:   "state.proposals." + id + ".proposed_goal.id",
				Message: "is already part of actor goals",
			}
		}
		reservations := pendingGoalReservations[proposal.ActorID]
		if reservations == nil {
			reservations = make(map[string]struct{})
			pendingGoalReservations[proposal.ActorID] = reservations
		}
		if _, exists := reservations[goalID]; exists {
			return &ValidationError{
				Field:   "state.proposals." + id + ".proposed_goal.id",
				Message: "is already reserved by another pending proposal",
			}
		}
		reservations[goalID] = struct{}{}
	}
	for actorID, reservations := range pendingGoalReservations {
		if len(actorGoalIDs[actorID])+len(reservations) > 32 {
			return &ValidationError{
				Field:   "state.actors." + actorID + ".goals",
				Message: "goals and pending goal reservations must contain at most 32 values",
			}
		}
	}
	if len(state.Arbitrations) > 32 {
		return &ValidationError{Field: "state.arbitrations", Message: "must contain at most 32 values"}
	}
	if len(state.Arbitrations) > 0 && !HasFeature(state.Features, FeatureArbitration) {
		return &ValidationError{Field: "state.arbitrations", Message: "requires arbitration-v1"}
	}
	arbitrationIDs := make(map[string]struct{}, len(state.Arbitrations))
	for index, record := range state.Arbitrations {
		field := fmt.Sprintf("state.arbitrations[%d]", index)
		if err := validateArbitrationRecord(field, record, state); err != nil {
			return err
		}
		if _, exists := arbitrationIDs[record.ID]; exists {
			return &ValidationError{Field: "state.arbitrations", Message: "record ids must be unique"}
		}
		arbitrationIDs[record.ID] = struct{}{}
	}
	if len(state.Receipts) > 1024 {
		return &ValidationError{Field: "state.receipts", Message: "must contain at most 1024 values"}
	}
	for id, receipt := range state.Receipts {
		field := "state.receipts." + id
		if err := validateID(field, id); err != nil {
			return err
		}
		if err := validateID(field+".kind", receipt.Kind); err != nil {
			return err
		}
		if receipt.EntityID != "" {
			if err := validateID(field+".entity_id", receipt.EntityID); err != nil {
				return err
			}
		}
		if receipt.RequestHash != "" && !hashPattern.MatchString(receipt.RequestHash) {
			return &ValidationError{
				Field:   field + ".request_hash",
				Message: "must be a lowercase SHA-256 digest",
			}
		}
		if receipt.Revision > state.Revision {
			return &ValidationError{Field: field + ".revision", Message: "must not exceed the session revision"}
		}
	}
	return nil
}

func validateMemory(field string, memory Memory, stateRevision uint64, stateTick int64) error {
	if err := validateID(field+".id", memory.ID); err != nil {
		return err
	}
	if err := validateID(field+".event_id", memory.EventID); err != nil {
		return err
	}
	if memory.Tick < 0 || memory.LastRecalledTick < 0 || memory.RecallCount < 0 || memory.RecallCount > 1_000_000 {
		return &ValidationError{Field: field, Message: "tick and recall values must not be negative"}
	}
	if memory.Tick > stateTick {
		return &ValidationError{Field: field + ".tick", Message: "must not exceed the session tick"}
	}
	if memory.LastRecalledTick > stateTick {
		return &ValidationError{Field: field + ".last_recalled_tick", Message: "must not exceed the session tick"}
	}
	if memory.CreatedRevision == 0 || memory.CreatedRevision > stateRevision {
		return &ValidationError{Field: field + ".created_revision", Message: "must reference an existing session revision"}
	}
	if err := validateText(field+".summary", memory.Summary, 1000, true); err != nil {
		return err
	}
	if err := validateText(field+".quote", memory.Quote, 500, false); err != nil {
		return err
	}
	if err := validateTags(field+".tags", memory.Tags, 32); err != nil {
		return err
	}
	if memory.Importance < 1 || memory.Importance > 5 {
		return &ValidationError{Field: field + ".importance", Message: "must be between 1 and 5"}
	}
	return nil
}

func validateMemorySummary(field string, summary MemorySummary, stateRevision uint64, stateTick int64) error {
	if err := validateID(field+".id", summary.ID); err != nil {
		return err
	}
	if summary.Level < 1 || summary.Level > 16 {
		return &ValidationError{Field: field + ".level", Message: "must be between 1 and 16"}
	}
	if err := validateText(field+".summary", summary.Summary, 1000, true); err != nil {
		return err
	}
	if err := validateTags(field+".tags", summary.Tags, 32); err != nil {
		return err
	}
	if err := validateTags(field+".source_memory_ids", summary.SourceMemoryIDs, 64); err != nil {
		return err
	}
	if err := validateTags(field+".source_event_ids", summary.SourceEventIDs, 64); err != nil {
		return err
	}
	if len(summary.SourceMemoryIDs) == 0 || len(summary.SourceEventIDs) == 0 {
		return &ValidationError{Field: field, Message: "must retain source memory and event ids"}
	}
	if summary.StartTick < 0 || summary.EndTick < summary.StartTick || summary.LastRecalledTick < 0 {
		return &ValidationError{Field: field, Message: "contains an invalid tick range"}
	}
	if summary.EndTick > stateTick {
		return &ValidationError{Field: field + ".end_tick", Message: "must not exceed the session tick"}
	}
	if summary.LastRecalledTick > stateTick {
		return &ValidationError{Field: field + ".last_recalled_tick", Message: "must not exceed the session tick"}
	}
	if summary.Importance < 1 || summary.Importance > 5 {
		return &ValidationError{Field: field + ".importance", Message: "must be between 1 and 5"}
	}
	if err := validateID(field+".reason", summary.Reason); err != nil {
		return err
	}
	if summary.CreatedRevision == 0 || summary.CreatedRevision > stateRevision {
		return &ValidationError{Field: field + ".created_revision", Message: "must reference an existing session revision"}
	}
	if summary.RecallCount < 0 || summary.RecallCount > 1_000_000 {
		return &ValidationError{Field: field + ".recall_count", Message: "must be between 0 and 1000000"}
	}
	return nil
}

func validateBeliefSet(
	field string,
	key string,
	set BeliefSet,
	state SessionState,
	outcomeReporting bool,
) error {
	if err := validateID(field+".subject_id", set.SubjectID); err != nil {
		return err
	}
	if err := validateID(field+".predicate", set.Predicate); err != nil {
		return err
	}
	if key != set.SubjectID+":"+set.Predicate {
		return &ValidationError{Field: field, Message: "map key must match subject and predicate"}
	}
	if len(set.Claims) == 0 || len(set.Claims) > 8 {
		return &ValidationError{Field: field + ".claims", Message: "must contain 1-8 claims"}
	}
	if err := validateID(field+".selected_source_event_id", set.SelectedSourceEventID); err != nil {
		return err
	}
	sources := make(map[string]struct{}, len(set.Claims))
	objects := make(map[string]struct{}, len(set.Claims))
	selectedExists := false
	for index, claim := range set.Claims {
		claimField := fmt.Sprintf("%s.claims[%d]", field, index)
		if err := validateFact(claimField+".fact", claim.Fact); err != nil {
			return err
		}
		if claim.Fact.ObservedTick > state.Tick {
			return &ValidationError{Field: claimField + ".fact.observed_tick", Message: "must not exceed the session tick"}
		}
		if !outcomeReporting && claim.Fact.ObservedTick != 0 {
			return &ValidationError{Field: claimField + ".fact.observed_tick", Message: "requires outcome-reporting-v1"}
		}
		if claim.Fact.SubjectID != set.SubjectID || claim.Fact.Predicate != set.Predicate {
			return &ValidationError{Field: claimField + ".fact", Message: "must match its belief set"}
		}
		if claim.Fact.SourceEventID == "" {
			return &ValidationError{Field: claimField + ".fact.source_event_id", Message: "is required"}
		}
		for _, visibleActor := range claim.Fact.Visibility {
			if _, exists := state.Actors[visibleActor]; !exists {
				return &ValidationError{Field: claimField + ".fact.visibility", Message: "references an unknown actor"}
			}
		}
		if _, exists := sources[claim.Fact.SourceEventID]; exists {
			return &ValidationError{Field: field + ".claims", Message: "source event ids must be unique"}
		}
		sources[claim.Fact.SourceEventID] = struct{}{}
		objects[claim.Fact.Object] = struct{}{}
		if claim.ObservedRevision == 0 || claim.ObservedRevision > state.Revision {
			return &ValidationError{Field: claimField + ".observed_revision", Message: "must reference an existing session revision"}
		}
		selectedExists = selectedExists || claim.Fact.SourceEventID == set.SelectedSourceEventID
	}
	if !selectedExists {
		return &ValidationError{Field: field + ".selected_source_event_id", Message: "references an unknown claim"}
	}
	if set.Conflicted != (len(objects) > 1) {
		return &ValidationError{Field: field + ".conflicted", Message: "must reflect distinct claim objects"}
	}
	return nil
}

// factsEquivalent compares the JSON contract fields while treating nil and
// empty omitempty slices as the same projected value.
func factsEquivalent(left Fact, right Fact) bool {
	if left.SubjectID != right.SubjectID ||
		left.Predicate != right.Predicate ||
		left.Object != right.Object ||
		left.Confidence != right.Confidence ||
		left.SourceEventID != right.SourceEventID ||
		left.ObservedTick != right.ObservedTick ||
		len(left.Visibility) != len(right.Visibility) {
		return false
	}
	for index := range left.Visibility {
		if left.Visibility[index] != right.Visibility[index] {
			return false
		}
	}
	return true
}

func validateActorActivity(field string, activity ActorActivity, state SessionState) error {
	if activity.RegionID != "" {
		if err := validateID(field+".region_id", activity.RegionID); err != nil {
			return err
		}
	}
	if activity.State != "awake" && activity.State != "dormant" {
		return &ValidationError{Field: field + ".state", Message: "must be awake or dormant"}
	}
	if err := validateText(field+".reason", activity.Reason, 300, false); err != nil {
		return err
	}
	if activity.UpdatedTick < 0 || activity.UpdatedTick > state.Tick {
		return &ValidationError{Field: field + ".updated_tick", Message: "must reference the current timeline"}
	}
	if activity.UpdatedRevision == 0 || activity.UpdatedRevision > state.Revision {
		return &ValidationError{Field: field + ".updated_revision", Message: "must reference an existing session revision"}
	}
	return nil
}

func validateArbitrationRecord(field string, record ArbitrationRecord, state SessionState) error {
	if err := validateID(field+".id", record.ID); err != nil {
		return err
	}
	if err := validateID(field+".request_id", record.RequestID); err != nil {
		return err
	}
	if record.Tick < 0 {
		return &ValidationError{Field: field + ".tick", Message: "must not be negative"}
	}
	if record.BasedOnWorldRevision == 0 || record.BasedOnWorldRevision > state.WorldRevision {
		return &ValidationError{Field: field + ".based_on_world_revision", Message: "must reference an existing world revision"}
	}
	if record.CreatedRevision == 0 || record.CreatedRevision > state.Revision {
		return &ValidationError{Field: field + ".created_revision", Message: "must reference an existing session revision"}
	}
	if len(record.Decisions) == 0 || len(record.Decisions) > 64 {
		return &ValidationError{Field: field + ".decisions", Message: "must contain 1-64 decisions"}
	}
	proposalIDs := make(map[string]struct{}, len(record.Decisions))
	actorIDs := make(map[string]struct{}, len(record.Decisions))
	statuses := make(map[string]string, len(record.Decisions))
	for index, decision := range record.Decisions {
		base := fmt.Sprintf("%s.decisions[%d]", field, index)
		if err := validateID(base+".proposal_id", decision.ProposalID); err != nil {
			return err
		}
		if err := validateID(base+".actor_id", decision.ActorID); err != nil {
			return err
		}
		if _, exists := state.Actors[decision.ActorID]; !exists {
			return &ValidationError{Field: base + ".actor_id", Message: "references an unknown actor"}
		}
		if decision.Status != "selected" && decision.Status != "deferred" {
			return &ValidationError{Field: base + ".status", Message: "must be selected or deferred"}
		}
		if err := validateText(base+".reason", decision.Reason, 300, true); err != nil {
			return err
		}
		if err := validateTags(base+".conflicting_proposal_ids", decision.ConflictingProposalIDs, 64); err != nil {
			return err
		}
		if _, exists := proposalIDs[decision.ProposalID]; exists {
			return &ValidationError{Field: field + ".decisions", Message: "proposal ids must be unique"}
		}
		proposalIDs[decision.ProposalID] = struct{}{}
		if _, exists := actorIDs[decision.ActorID]; exists {
			return &ValidationError{Field: field + ".decisions", Message: "actor ids must be unique"}
		}
		actorIDs[decision.ActorID] = struct{}{}
		statuses[decision.ProposalID] = decision.Status
		if decision.Status == "selected" && len(decision.ConflictingProposalIDs) != 0 {
			return &ValidationError{Field: base + ".conflicting_proposal_ids", Message: "selected decisions cannot have conflicts"}
		}
		if decision.Status == "deferred" && len(decision.ConflictingProposalIDs) == 0 {
			return &ValidationError{Field: base + ".conflicting_proposal_ids", Message: "deferred decisions must identify a conflict"}
		}
	}
	for index, decision := range record.Decisions {
		base := fmt.Sprintf("%s.decisions[%d].conflicting_proposal_ids", field, index)
		for conflictIndex, proposalID := range decision.ConflictingProposalIDs {
			if proposalID == decision.ProposalID {
				return &ValidationError{
					Field:   fmt.Sprintf("%s[%d]", base, conflictIndex),
					Message: "cannot reference the decision itself",
				}
			}
			status, exists := statuses[proposalID]
			if !exists {
				return &ValidationError{
					Field:   fmt.Sprintf("%s[%d]", base, conflictIndex),
					Message: "references an unknown decision",
				}
			}
			if status != "selected" {
				return &ValidationError{
					Field:   fmt.Sprintf("%s[%d]", base, conflictIndex),
					Message: "must reference a selected decision",
				}
			}
		}
	}
	return nil
}

func validateProposal(field string, state SessionState, actor ActorState, proposal ActionProposal, memoryIDs map[string]struct{}) error {
	for suffix, value := range map[string]string{
		".id": proposal.ID, ".session_id": proposal.SessionID, ".request_id": proposal.RequestID, ".actor_id": proposal.ActorID,
	} {
		if err := validateID(field+suffix, value); err != nil {
			return err
		}
	}
	if proposal.SessionID != state.SessionID || proposal.ActorID != actor.ID {
		return &ValidationError{Field: field, Message: "proposal session and actor must match its container"}
	}
	if proposal.Tick < 0 {
		return &ValidationError{Field: field + ".tick", Message: "must not be negative"}
	}
	if proposal.CreatedRevision == 0 || proposal.CreatedRevision > state.Revision {
		return &ValidationError{Field: field + ".created_revision", Message: "must reference an existing session revision"}
	}
	if proposal.BasedOnRevision == 0 {
		if proposal.CreatedRevision != 1 || proposal.BasedOnHeadHash != "" {
			return &ValidationError{
				Field:   field + ".based_on_revision",
				Message: "zero is only valid for a fresh restore generation",
			}
		}
	} else {
		if proposal.BasedOnRevision >= proposal.CreatedRevision {
			return &ValidationError{
				Field:   field + ".based_on_revision",
				Message: "must precede the proposal creation revision",
			}
		}
		if !hashPattern.MatchString(proposal.BasedOnHeadHash) {
			return &ValidationError{Field: field + ".based_on_head_hash", Message: "must be a lowercase SHA-256 hash"}
		}
	}
	outcomeReporting := HasFeature(state.Features, FeatureOutcomeReporting)
	if !outcomeReporting && (proposal.OutcomeEventID != "" || proposal.OutcomeTick != 0) {
		return &ValidationError{Field: field, Message: "outcome occurrence metadata requires outcome-reporting-v1"}
	}
	if proposal.OutcomeEventID == "" {
		if proposal.OutcomeTick != 0 {
			return &ValidationError{Field: field + ".outcome_tick", Message: "requires outcome_event_id"}
		}
		if outcomeReporting && proposal.Status != "pending" {
			return &ValidationError{Field: field + ".outcome_event_id", Message: "resolved proposals require outcome metadata"}
		}
	} else {
		if err := validateID(field+".outcome_event_id", proposal.OutcomeEventID); err != nil {
			return err
		}
		if proposal.Status == "pending" {
			return &ValidationError{Field: field + ".outcome_event_id", Message: "pending proposals cannot have an outcome"}
		}
		if proposal.OutcomeTick < proposal.Tick || proposal.OutcomeTick > state.Tick {
			return &ValidationError{Field: field + ".outcome_tick", Message: "must be between proposal tick and session tick"}
		}
	}
	if HasFeature(state.Features, FeatureArbitration) {
		if proposal.BasedOnWorldRevision == 0 || proposal.BasedOnWorldRevision > state.WorldRevision {
			return &ValidationError{Field: field + ".based_on_world_revision", Message: "must reference an existing world revision"}
		}
	} else if proposal.BasedOnWorldRevision != 0 {
		return &ValidationError{Field: field + ".based_on_world_revision", Message: "must be zero when arbitration is disabled"}
	}
	if err := validateAction(field+".action", proposal.Action); err != nil {
		return err
	}
	if proposal.Stance != "engage" && proposal.Stance != "partial" && proposal.Stance != "redirect" && proposal.Stance != "refuse" && proposal.Stance != "wait" {
		return &ValidationError{Field: field + ".stance", Message: "is unsupported"}
	}
	if err := validateText(field+".summary", proposal.Summary, 500, true); err != nil {
		return err
	}
	if err := validateText(field+".rationale", proposal.Rationale, 500, true); err != nil {
		return err
	}
	if proposal.PolicySource != "" {
		if err := validateID(field+".policy_source", proposal.PolicySource); err != nil {
			return err
		}
	}
	if err := validateTags(field+".recalled_memory_ids", proposal.RecalledMemoryIDs, 8); err != nil {
		return err
	}
	for _, id := range proposal.RecalledMemoryIDs {
		if _, exists := memoryIDs[id]; !exists {
			return &ValidationError{Field: field + ".recalled_memory_ids", Message: "references an unknown memory"}
		}
	}
	if proposal.GoalID != "" {
		if err := validateID(field+".goal_id", proposal.GoalID); err != nil {
			return err
		}
		found := false
		for _, goal := range actor.Goals {
			found = found || goal.ID == proposal.GoalID
		}
		if proposal.ProposedGoal != nil {
			if !HasFeature(state.Features, FeatureGoalCandidates) {
				return &ValidationError{Field: field + ".proposed_goal", Message: "requires goal-candidates-v1"}
			}
			if err := validateGoal(field+".proposed_goal", *proposal.ProposedGoal); err != nil {
				return err
			}
			if proposal.ProposedGoal.ID != proposal.GoalID ||
				proposal.ProposedGoal.Progress != 0 ||
				proposal.ProposedGoal.Status != "active" ||
				proposal.ProposedGoal.UpdatedTick != 0 ||
				proposal.ProposedGoal.ProgressAccumulator != 0 ||
				proposal.ProposedGoal.StatusExplicit ||
				proposal.ProposedGoal.StatusUpdatedTick != 0 ||
				proposal.ProposedGoal.StatusSourceEventID != "" {
				return &ValidationError{Field: field + ".proposed_goal", Message: "must match an active zero-progress goal_id without state metadata"}
			}
			if proposal.Status == "accepted" && !found {
				return &ValidationError{
					Field:   field + ".proposed_goal.id",
					Message: "accepted proposed goal must be present in actor goals",
				}
			}
			found = true
		}
		if !found {
			return &ValidationError{Field: field + ".goal_id", Message: "references an unknown goal"}
		}
	} else if proposal.ProposedGoal != nil {
		return &ValidationError{Field: field + ".proposed_goal", Message: "requires goal_id"}
	}
	if proposal.Status != "pending" && proposal.Status != "accepted" && proposal.Status != "rejected" {
		return &ValidationError{Field: field + ".status", Message: "must be pending, accepted, or rejected"}
	}
	return nil
}
