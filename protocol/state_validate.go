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
	if state.Tick < 0 {
		return &ValidationError{Field: "state.tick", Message: "must not be negative"}
	}
	if state.Revision == 0 {
		return &ValidationError{Field: "state.revision", Message: "must be greater than zero"}
	}
	if !hashPattern.MatchString(state.HeadHash) {
		return &ValidationError{Field: "state.head_hash", Message: "must be a lowercase SHA-256 hash"}
	}
	if len(state.Actors) == 0 || len(state.Actors) > 128 {
		return &ValidationError{Field: "state.actors", Message: "must contain 1-128 actors"}
	}
	for id, actor := range state.Actors {
		base := "state.actors." + id
		if id != actor.ID {
			return &ValidationError{Field: base, Message: "map key must match actor id"}
		}
		if err := validateActor(base, actor.ActorSeed); err != nil {
			return err
		}
		if actor.NextThinkTick < 0 {
			return &ValidationError{Field: base + ".next_think_tick", Message: "must not be negative"}
		}
		if len(actor.Memories) > 128 {
			return &ValidationError{Field: base + ".memories", Message: "must contain at most 128 values"}
		}
		memoryIDs := make(map[string]struct{}, len(actor.Memories))
		for index, memory := range actor.Memories {
			field := fmt.Sprintf("%s.memories[%d]", base, index)
			if err := validateMemory(field, memory); err != nil {
				return err
			}
			if _, exists := memoryIDs[memory.ID]; exists {
				return &ValidationError{Field: base + ".memories", Message: "memory ids must be unique"}
			}
			memoryIDs[memory.ID] = struct{}{}
		}
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
			for _, visibleActor := range fact.Visibility {
				if _, exists := state.Actors[visibleActor]; !exists {
					return &ValidationError{Field: field + ".visibility", Message: "references an unknown actor"}
				}
			}
		}
		if len(actor.RecentActions) > 32 {
			return &ValidationError{Field: base + ".recent_actions", Message: "must contain at most 32 values"}
		}
		for index, proposal := range actor.RecentActions {
			if err := validateProposal(fmt.Sprintf("%s.recent_actions[%d]", base, index), state, actor, proposal, memoryIDs); err != nil {
				return err
			}
			if proposal.Status != "accepted" {
				return &ValidationError{Field: fmt.Sprintf("%s.recent_actions[%d].status", base, index), Message: "must be accepted"}
			}
		}
	}
	if len(state.Proposals) > 64 {
		return &ValidationError{Field: "state.proposals", Message: "must contain at most 64 values"}
	}
	for id, proposal := range state.Proposals {
		if id != proposal.ID {
			return &ValidationError{Field: "state.proposals." + id, Message: "map key must match proposal id"}
		}
		actor, exists := state.Actors[proposal.ActorID]
		if !exists {
			return &ValidationError{Field: "state.proposals." + id + ".actor_id", Message: "references an unknown actor"}
		}
		memoryIDs := make(map[string]struct{}, len(actor.Memories))
		for _, memory := range actor.Memories {
			memoryIDs[memory.ID] = struct{}{}
		}
		if err := validateProposal("state.proposals."+id, state, actor, proposal, memoryIDs); err != nil {
			return err
		}
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
	}
	return nil
}

func validateMemory(field string, memory Memory) error {
	if err := validateID(field+".id", memory.ID); err != nil {
		return err
	}
	if err := validateID(field+".event_id", memory.EventID); err != nil {
		return err
	}
	if memory.Tick < 0 || memory.LastRecalledTick < 0 || memory.RecallCount < 0 || memory.RecallCount > 1_000_000 {
		return &ValidationError{Field: field, Message: "tick and recall values must not be negative"}
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
	if !hashPattern.MatchString(proposal.BasedOnHeadHash) {
		return &ValidationError{Field: field + ".based_on_head_hash", Message: "must be a lowercase SHA-256 hash"}
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
		if !found {
			return &ValidationError{Field: field + ".goal_id", Message: "references an unknown goal"}
		}
	}
	if proposal.Status != "pending" && proposal.Status != "accepted" && proposal.Status != "rejected" {
		return &ValidationError{Field: field + ".status", Message: "must be pending, accepted, or rejected"}
	}
	return nil
}
