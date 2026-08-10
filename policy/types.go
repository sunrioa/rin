// Package policy authorizes Host-bound effects before a game adapter may
// execute them. It is deterministic and never calls a model or network
// provider.
package policy

import "github.com/sunrioa/rin/host"

// Result is the only possible policy outcome.
type Result string

const (
	Allow               Result = "allow"
	Deny                Result = "deny"
	RequireConfirmation Result = "require_confirmation"
)

// Profile supplies the default for known effects that match no explicit rule.
type Profile string

const (
	ProfileGuarded          Profile = "guarded"
	ProfileSurvival         Profile = "survival"
	ProfileOpen             Profile = "open"
	ProfilePrivilegedCustom Profile = "privileged-custom"
)

// Layer identifies one gameplay policy scope. The safety kernel is built into
// Engine and cannot be replaced by configuration.
type Layer string

const (
	LayerServer Layer = "server"
	LayerWorld  Layer = "world"
	LayerOwner  Layer = "owner"
	LayerActor  Layer = "actor"
	LayerTask   Layer = "task"
)

// Rule matches only trusted, standardized Effect fields. Adapter-specific
// Attributes and controller prose cannot influence authorization.
type Rule struct {
	RuleID       string                 `json:"rule_id"`
	Layer        Layer                  `json:"layer"`
	Priority     int32                  `json:"priority"`
	Result       Result                 `json:"result"`
	EffectKinds  []string               `json:"effect_kinds,omitempty"`
	Operations   []host.EffectOperation `json:"operations,omitempty"`
	Ownership    []host.OwnershipClass  `json:"ownership,omitempty"`
	Scopes       []string               `json:"scopes,omitempty"`
	TagsAll      []string               `json:"tags_all,omitempty"`
	TagsAny      []string               `json:"tags_any,omitempty"`
	RiskAtLeast  host.RiskLevel         `json:"risk_at_least,omitempty"`
	RiskAtMost   host.RiskLevel         `json:"risk_at_most,omitempty"`
	Reversible   *bool                  `json:"reversible,omitempty"`
	ReasonCode   string                 `json:"reason_code"`
	HumanSummary string                 `json:"human_summary"`
}

// Budget bounds matching actions in one policy scope and optional Host-clock
// window. A zero Window applies for the current epoch.
type Budget struct {
	BudgetID    string                 `json:"budget_id"`
	Layer       Layer                  `json:"layer"`
	EffectKinds []string               `json:"effect_kinds,omitempty"`
	Operations  []host.EffectOperation `json:"operations,omitempty"`
	TagsAny     []string               `json:"tags_any,omitempty"`
	MaxActions  uint32                 `json:"max_actions,omitempty"`
	MaxQuantity uint64                 `json:"max_quantity,omitempty"`
	Window      host.Duration          `json:"window,omitempty"`
}

// Config is one immutable gameplay policy revision.
type Config struct {
	Revision           uint64        `json:"revision"`
	Profile            Profile       `json:"profile"`
	KnownEffectKinds   []string      `json:"known_effect_kinds,omitempty"`
	KnownScopes        []string      `json:"known_scopes,omitempty"`
	Rules              []Rule        `json:"rules,omitempty"`
	Budgets            []Budget      `json:"budgets,omitempty"`
	ConfirmationTTL    host.Duration `json:"confirmation_ttl"`
	ConfirmationScopes []string      `json:"confirmation_scopes"`
}

// Context supplies trusted request metadata that is not part of an Effect.
type Context struct {
	Now              host.Timepoint `json:"now"`
	CurrentEpoch     host.Epoch     `json:"current_epoch"`
	Principal        host.Principal `json:"principal"`
	ServerID         string         `json:"server_id"`
	OwnerID          string         `json:"owner_id,omitempty"`
	EmergencyStopped bool           `json:"emergency_stopped"`
	ConfirmationID   string         `json:"confirmation_id,omitempty"`
}

// ConfirmationChallenge is bound to one controller, actor, principal, effect
// digest, epoch, policy revision, and expiry. It is single use.
type ConfirmationChallenge struct {
	ChallengeID    string         `json:"challenge_id"`
	ControllerID   string         `json:"controller_id"`
	ActorID        string         `json:"actor_id"`
	PrincipalID    string         `json:"principal_id"`
	EffectDigest   string         `json:"effect_digest"`
	Epoch          host.Epoch     `json:"epoch"`
	ExpiresAt      host.Timepoint `json:"expires_at"`
	PolicyRevision uint64         `json:"policy_revision"`
	SingleUse      bool           `json:"single_use"`
}

// EffectiveLimit explains one budget reservation made by an allow decision.
type EffectiveLimit struct {
	BudgetID          string `json:"budget_id"`
	UsageKey          string `json:"usage_key"`
	MaxActions        uint64 `json:"max_actions"`
	ActionsUsed       uint64 `json:"actions_used"`
	ActionsRemaining  uint64 `json:"actions_remaining"`
	MaxQuantity       uint64 `json:"max_quantity"`
	QuantityUsed      uint64 `json:"quantity_used"`
	QuantityRemaining uint64 `json:"quantity_remaining"`
}

// Decision is an auditable authorization result for one exact effect digest.
type Decision struct {
	DecisionID      string                 `json:"decision_id"`
	Result          Result                 `json:"result"`
	ControllerID    string                 `json:"controller_id"`
	ActorID         string                 `json:"actor_id"`
	PrincipalID     string                 `json:"principal_id"`
	EffectDigest    string                 `json:"effect_digest"`
	PolicyRevision  uint64                 `json:"policy_revision"`
	MatchedRuleIDs  []string               `json:"matched_rule_ids"`
	ReasonCode      string                 `json:"reason_code"`
	HumanSummary    string                 `json:"human_summary"`
	EffectiveLimits []EffectiveLimit       `json:"effective_limits,omitempty"`
	Confirmation    *ConfirmationChallenge `json:"confirmation_challenge,omitempty"`
	EvaluatedAt     host.Timepoint         `json:"evaluated_at"`
}
