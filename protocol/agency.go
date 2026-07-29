package protocol

const (
	InitiativePassive  = "passive"
	InitiativeDialogue = "dialogue"
	InitiativeActions  = "actions"

	ObedienceObey        = "obey"
	ObedienceNegotiate   = "negotiate"
	ObedienceIndependent = "independent"

	TurnResponsive        = "responsive"
	TurnProactiveDialogue = "proactive-dialogue"
	TurnProactiveAction   = "proactive-action"
)

type AgencyPolicy struct {
	Initiative           string `json:"initiative"`
	Obedience            string `json:"obedience"`
	MessageCooldownTicks int64  `json:"message_cooldown_ticks"`
	MaxConsecutiveTurns  int    `json:"max_consecutive_turns"`
}

type AgencyState struct {
	UpdatedTick               int64  `json:"updated_tick"`
	UpdatedRevision           uint64 `json:"updated_revision"`
	LastProactiveDialogueTick *int64 `json:"last_proactive_dialogue_tick,omitempty"`
	ConsecutiveProactiveTurns int    `json:"consecutive_proactive_turns"`
}

type AgencyTurn struct {
	Kind              string       `json:"kind"`
	Directive         bool         `json:"directive"`
	DirectiveOfferIDs []string     `json:"directive_offer_ids,omitempty"`
	HostCeiling       AgencyPolicy `json:"host_ceiling"`
	ServerPolicy      AgencyPolicy `json:"server_policy"`
}

type AgencyDecision struct {
	Kind              string       `json:"kind"`
	Directive         bool         `json:"directive"`
	DirectiveOfferIDs []string     `json:"directive_offer_ids,omitempty"`
	HostCeiling       AgencyPolicy `json:"host_ceiling"`
	ServerPolicy      AgencyPolicy `json:"server_policy"`
	ActorPolicy       AgencyPolicy `json:"actor_policy"`
	Effective         AgencyPolicy `json:"effective"`
}

type ActorAgencyUpdate struct {
	ActorID string       `json:"actor_id"`
	Policy  AgencyPolicy `json:"policy"`
}

type SetActorAgencyRequest struct {
	ProtocolVersion string              `json:"protocol_version"`
	SessionID       string              `json:"session_id"`
	RequestID       string              `json:"request_id"`
	Tick            int64               `json:"tick"`
	Updates         []ActorAgencyUpdate `json:"updates"`
}

func ResolveAgency(turn AgencyTurn, actor AgencyPolicy) AgencyDecision {
	return AgencyDecision{
		Kind:              turn.Kind,
		Directive:         turn.Directive,
		DirectiveOfferIDs: append([]string(nil), turn.DirectiveOfferIDs...),
		HostCeiling:       turn.HostCeiling,
		ServerPolicy:      turn.ServerPolicy,
		ActorPolicy:       actor,
		Effective: AgencyPolicy{
			Initiative: mostRestrictiveInitiative(
				turn.HostCeiling.Initiative,
				turn.ServerPolicy.Initiative,
				actor.Initiative,
			),
			Obedience: mostRestrictiveObedience(
				turn.HostCeiling.Obedience,
				turn.ServerPolicy.Obedience,
				actor.Obedience,
			),
			MessageCooldownTicks: maxInt64(
				turn.HostCeiling.MessageCooldownTicks,
				turn.ServerPolicy.MessageCooldownTicks,
				actor.MessageCooldownTicks,
			),
			MaxConsecutiveTurns: minInt(
				turn.HostCeiling.MaxConsecutiveTurns,
				turn.ServerPolicy.MaxConsecutiveTurns,
				actor.MaxConsecutiveTurns,
			),
		},
	}
}

func AgencyAllowsInitiative(policy AgencyPolicy, required string) bool {
	actualRank, actualOK := initiativeRank(policy.Initiative)
	requiredRank, requiredOK := initiativeRank(required)
	return actualOK && requiredOK && actualRank >= requiredRank
}

func mostRestrictiveInitiative(values ...string) string {
	result := InitiativeActions
	resultRank, _ := initiativeRank(result)
	for _, value := range values {
		rank, ok := initiativeRank(value)
		if !ok {
			return value
		}
		if rank < resultRank {
			result, resultRank = value, rank
		}
	}
	return result
}

func mostRestrictiveObedience(values ...string) string {
	result := ObedienceIndependent
	resultRank, _ := obedienceRank(result)
	for _, value := range values {
		rank, ok := obedienceRank(value)
		if !ok {
			return value
		}
		if rank < resultRank {
			result, resultRank = value, rank
		}
	}
	return result
}

func initiativeRank(value string) (int, bool) {
	switch value {
	case InitiativePassive:
		return 0, true
	case InitiativeDialogue:
		return 1, true
	case InitiativeActions:
		return 2, true
	default:
		return 0, false
	}
}

func obedienceRank(value string) (int, bool) {
	switch value {
	case ObedienceObey:
		return 0, true
	case ObedienceNegotiate:
		return 1, true
	case ObedienceIndependent:
		return 2, true
	default:
		return 0, false
	}
}

func maxInt64(values ...int64) int64 {
	result := values[0]
	for _, value := range values[1:] {
		if value > result {
			result = value
		}
	}
	return result
}

func minInt(values ...int) int {
	result := values[0]
	for _, value := range values[1:] {
		if value < result {
			result = value
		}
	}
	return result
}
