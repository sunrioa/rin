package protocol

import "fmt"

func validateJSONSafeSigned(field string, value int64) error {
	if value < -MaxJSONSafeInteger || value > MaxJSONSafeInteger {
		return &ValidationError{
			Field: field,
			Message: fmt.Sprintf(
				"must be an exact JSON integer between -%d and %d",
				MaxJSONSafeInteger,
				MaxJSONSafeInteger,
			),
		}
	}
	return nil
}

func validateJSONSafeUnsigned(field string, value uint64) error {
	if value > uint64(MaxJSONSafeInteger) {
		return &ValidationError{
			Field:   field,
			Message: fmt.Sprintf("must be an exact JSON integer at most %d", MaxJSONSafeInteger),
		}
	}
	return nil
}

func validateJSONSafeTick(field string, value int64) error {
	if value < 0 {
		return &ValidationError{Field: field, Message: "must not be negative"}
	}
	return validateJSONSafeSigned(field, value)
}

func validateSessionStateJSONIntegers(state SessionState) error {
	if err := validateJSONSafeSigned("state.seed", state.Seed); err != nil {
		return err
	}
	if err := validateJSONSafeTick("state.tick", state.Tick); err != nil {
		return err
	}
	if err := validateJSONSafeUnsigned("state.revision", state.Revision); err != nil {
		return err
	}
	if err := validateJSONSafeUnsigned("state.world_revision", state.WorldRevision); err != nil {
		return err
	}

	for actorID, actor := range state.Actors {
		base := "state.actors." + actorID
		if err := validateJSONSafeSigned(base+".think_every_ticks", actor.ThinkEveryTicks); err != nil {
			return err
		}
		if err := validateJSONSafeTick(base+".next_think_tick", actor.NextThinkTick); err != nil {
			return err
		}
		if actor.Activity != nil {
			if err := validateJSONSafeTick(base+".activity.updated_tick", actor.Activity.UpdatedTick); err != nil {
				return err
			}
			if err := validateJSONSafeUnsigned(base+".activity.updated_revision", actor.Activity.UpdatedRevision); err != nil {
				return err
			}
		}
		for index, goal := range actor.Goals {
			if err := validateGoalJSONIntegers(fmt.Sprintf("%s.goals[%d]", base, index), goal); err != nil {
				return err
			}
		}
		for index, memory := range actor.Memories {
			field := fmt.Sprintf("%s.memories[%d]", base, index)
			if err := validateJSONSafeTick(field+".tick", memory.Tick); err != nil {
				return err
			}
			if err := validateJSONSafeUnsigned(field+".created_revision", memory.CreatedRevision); err != nil {
				return err
			}
			if err := validateJSONSafeTick(field+".last_recalled_tick", memory.LastRecalledTick); err != nil {
				return err
			}
		}
		for index, summary := range actor.MemorySummaries {
			field := fmt.Sprintf("%s.memory_summaries[%d]", base, index)
			if err := validateJSONSafeTick(field+".start_tick", summary.StartTick); err != nil {
				return err
			}
			if err := validateJSONSafeTick(field+".end_tick", summary.EndTick); err != nil {
				return err
			}
			if err := validateJSONSafeUnsigned(field+".created_revision", summary.CreatedRevision); err != nil {
				return err
			}
			if err := validateJSONSafeTick(field+".last_recalled_tick", summary.LastRecalledTick); err != nil {
				return err
			}
		}
		for key, fact := range actor.Beliefs {
			if err := validateFactJSONIntegers(base+".beliefs."+key, fact); err != nil {
				return err
			}
		}
		for key, set := range actor.BeliefSets {
			for index, claim := range set.Claims {
				field := fmt.Sprintf("%s.belief_sets.%s.claims[%d]", base, key, index)
				if err := validateFactJSONIntegers(field+".fact", claim.Fact); err != nil {
					return err
				}
				if err := validateJSONSafeUnsigned(field+".observed_revision", claim.ObservedRevision); err != nil {
					return err
				}
			}
		}
		for index, proposal := range actor.RecentActions {
			if err := validateProposalJSONIntegers(fmt.Sprintf("%s.recent_actions[%d]", base, index), proposal); err != nil {
				return err
			}
		}
	}
	for proposalID, proposal := range state.Proposals {
		if err := validateProposalJSONIntegers("state.proposals."+proposalID, proposal); err != nil {
			return err
		}
	}
	for index, record := range state.Arbitrations {
		if err := validateArbitrationJSONIntegers(fmt.Sprintf("state.arbitrations[%d]", index), record); err != nil {
			return err
		}
	}
	for requestID, receipt := range state.Receipts {
		if err := validateJSONSafeUnsigned("state.receipts."+requestID+".revision", receipt.Revision); err != nil {
			return err
		}
	}
	return nil
}

func validateGoalJSONIntegers(field string, goal Goal) error {
	if err := validateJSONSafeTick(field+".updated_tick", goal.UpdatedTick); err != nil {
		return err
	}
	if err := validateJSONSafeSigned(field+".progress_accumulator", goal.ProgressAccumulator); err != nil {
		return err
	}
	return validateJSONSafeTick(field+".status_updated_tick", goal.StatusUpdatedTick)
}

func validateFactJSONIntegers(field string, fact Fact) error {
	return validateJSONSafeTick(field+".observed_tick", fact.ObservedTick)
}

func validateProposalJSONIntegers(field string, proposal ActionProposal) error {
	if err := validateJSONSafeTick(field+".tick", proposal.Tick); err != nil {
		return err
	}
	if err := validateJSONSafeUnsigned(field+".based_on_revision", proposal.BasedOnRevision); err != nil {
		return err
	}
	if err := validateJSONSafeUnsigned(field+".based_on_world_revision", proposal.BasedOnWorldRevision); err != nil {
		return err
	}
	if err := validateJSONSafeUnsigned(field+".created_revision", proposal.CreatedRevision); err != nil {
		return err
	}
	if err := validateJSONSafeTick(field+".outcome_tick", proposal.OutcomeTick); err != nil {
		return err
	}
	if proposal.ProposedGoal != nil {
		return validateGoalJSONIntegers(field+".proposed_goal", *proposal.ProposedGoal)
	}
	return nil
}

func validateArbitrationJSONIntegers(field string, record ArbitrationRecord) error {
	if err := validateJSONSafeTick(field+".tick", record.Tick); err != nil {
		return err
	}
	if err := validateJSONSafeUnsigned(field+".based_on_world_revision", record.BasedOnWorldRevision); err != nil {
		return err
	}
	return validateJSONSafeUnsigned(field+".created_revision", record.CreatedRevision)
}

func validateIdentifierHistoryJSONIntegers(history IdentifierHistory) error {
	const base = "identifier_history"
	for requestID, identity := range history.Requests {
		field := base + ".requests." + requestID
		if err := validateJSONSafeUnsigned(field+".result_revision", identity.ResultRevision); err != nil {
			return err
		}
		if identity.Proposal != nil {
			if err := validateProposalJSONIntegers(field+".proposal", *identity.Proposal); err != nil {
				return err
			}
		}
		if identity.Arbitration != nil {
			if err := validateArbitrationJSONIntegers(field+".arbitration", *identity.Arbitration); err != nil {
				return err
			}
		}
	}
	for eventID, identity := range history.Events {
		if err := validateJSONSafeUnsigned(base+".events."+eventID+".revision", identity.Revision); err != nil {
			return err
		}
	}
	return nil
}
