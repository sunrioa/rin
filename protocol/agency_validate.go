package protocol

import "fmt"

func ValidateAgencyPolicy(field string, policy AgencyPolicy) error {
	if _, ok := initiativeRank(policy.Initiative); !ok {
		return &ValidationError{Field: field + ".initiative", Message: "must be passive, dialogue, or actions"}
	}
	if _, ok := obedienceRank(policy.Obedience); !ok {
		return &ValidationError{Field: field + ".obedience", Message: "must be obey, negotiate, or independent"}
	}
	if policy.MessageCooldownTicks < 0 || policy.MessageCooldownTicks > 1_000_000 {
		return &ValidationError{Field: field + ".message_cooldown_ticks", Message: "must be between 0 and 1000000"}
	}
	if policy.MaxConsecutiveTurns < 1 || policy.MaxConsecutiveTurns > 8 {
		return &ValidationError{Field: field + ".max_consecutive_turns", Message: "must be between 1 and 8"}
	}
	return nil
}

func ValidateSetActorAgency(request SetActorAgencyRequest) error {
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
	if len(request.Updates) == 0 || len(request.Updates) > 128 {
		return &ValidationError{Field: "updates", Message: "must contain 1-128 actor updates"}
	}
	seen := make(map[string]struct{}, len(request.Updates))
	for index, update := range request.Updates {
		field := fmt.Sprintf("updates[%d]", index)
		if err := validateID(field+".actor_id", update.ActorID); err != nil {
			return err
		}
		if err := ValidateAgencyPolicy(field+".policy", update.Policy); err != nil {
			return err
		}
		if _, exists := seen[update.ActorID]; exists {
			return &ValidationError{Field: "updates", Message: "actor ids must be unique"}
		}
		seen[update.ActorID] = struct{}{}
	}
	return nil
}

func validateAgencyTurn(field string, turn AgencyTurn, offerIDs map[string]struct{}) error {
	if turn.Kind != TurnResponsive &&
		turn.Kind != TurnProactiveDialogue &&
		turn.Kind != TurnProactiveAction {
		return &ValidationError{Field: field + ".kind", Message: "must be responsive, proactive-dialogue, or proactive-action"}
	}
	if err := ValidateAgencyPolicy(field+".host_ceiling", turn.HostCeiling); err != nil {
		return err
	}
	if err := ValidateAgencyPolicy(field+".server_policy", turn.ServerPolicy); err != nil {
		return err
	}
	if !turn.Directive && len(turn.DirectiveOfferIDs) != 0 {
		return &ValidationError{Field: field + ".directive_offer_ids", Message: "requires directive"}
	}
	if err := validateTags(field+".directive_offer_ids", turn.DirectiveOfferIDs, 32); err != nil {
		return err
	}
	if offerIDs != nil {
		for index, offerID := range turn.DirectiveOfferIDs {
			if _, exists := offerIDs[offerID]; !exists {
				return &ValidationError{
					Field:   fmt.Sprintf("%s.directive_offer_ids[%d]", field, index),
					Message: "must reference an offer in this request",
				}
			}
		}
	}
	return nil
}

func validateAgencyDecision(field string, decision AgencyDecision) error {
	turn := AgencyTurn{
		Kind:              decision.Kind,
		Directive:         decision.Directive,
		DirectiveOfferIDs: decision.DirectiveOfferIDs,
		HostCeiling:       decision.HostCeiling,
		ServerPolicy:      decision.ServerPolicy,
	}
	if err := validateAgencyTurn(field, turn, nil); err != nil {
		return err
	}
	if err := ValidateAgencyPolicy(field+".actor_policy", decision.ActorPolicy); err != nil {
		return err
	}
	if err := ValidateAgencyPolicy(field+".effective", decision.Effective); err != nil {
		return err
	}
	if expected := ResolveAgency(turn, decision.ActorPolicy).Effective; decision.Effective != expected {
		return &ValidationError{Field: field + ".effective", Message: "must be the restrictive policy intersection"}
	}
	return nil
}

func validateAgencyState(field string, agency AgencyState, state SessionState) error {
	if agency.UpdatedTick < 0 || agency.UpdatedTick > state.Tick {
		return &ValidationError{Field: field + ".updated_tick", Message: "must reference the current timeline"}
	}
	if agency.UpdatedRevision == 0 || agency.UpdatedRevision > state.Revision {
		return &ValidationError{Field: field + ".updated_revision", Message: "must reference an existing session revision"}
	}
	if agency.LastProactiveDialogueTick != nil &&
		(*agency.LastProactiveDialogueTick < 0 || *agency.LastProactiveDialogueTick > state.Tick) {
		return &ValidationError{Field: field + ".last_proactive_dialogue_tick", Message: "must reference the current timeline"}
	}
	if agency.ConsecutiveProactiveTurns < 0 || agency.ConsecutiveProactiveTurns > 8 {
		return &ValidationError{Field: field + ".consecutive_proactive_turns", Message: "must be between 0 and 8"}
	}
	return nil
}
