package policy

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"slices"
	"sync"

	"github.com/sunrioa/rin/host"
)

const (
	maxChallenges     = 1024
	maxAuthorizations = 4096
)

// Engine evaluates immutable policy revisions and owns short-lived
// confirmation challenges and budget reservations.
type Engine struct {
	mu sync.Mutex

	config       Config
	configDigest string

	challenges     map[string]*challengeState
	challengeKeys  map[string]string
	usage          map[string]budgetUsage
	reservations   map[string]reservation
	authorizations map[string]*authorizationState
	decisionKeys   map[string]string
}

type challengeState struct {
	challenge  ConfirmationChallenge
	approved   bool
	key        string
	clockScope string
}

type budgetUsage struct {
	actions  uint64
	quantity uint64
}

type budgetDelta struct {
	key      string
	actions  uint64
	quantity uint64
}

type reservation struct {
	deltas []budgetDelta
}

type authorizationState struct {
	decision       Decision
	confirmationID string
	finalized      bool
}

// New creates an effect authorization engine with one sealed policy revision.
func New(config Config) (*Engine, error) {
	sealed, err := SealConfig(config)
	if err != nil {
		return nil, err
	}
	digest, err := configStateDigest(sealed)
	if err != nil {
		return nil, err
	}
	return &Engine{
		config:         sealed,
		configDigest:   digest,
		challenges:     make(map[string]*challengeState),
		challengeKeys:  make(map[string]string),
		usage:          make(map[string]budgetUsage),
		reservations:   make(map[string]reservation),
		authorizations: make(map[string]*authorizationState),
		decisionKeys:   make(map[string]string),
	}, nil
}

// Config returns a defensive copy of the active policy revision.
func (engine *Engine) Config() Config {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	return cloneConfig(engine.config)
}

// Update atomically installs a newer policy revision. Unused confirmations
// and cached authorizations are invalidated; existing budget usage remains.
func (engine *Engine) Update(config Config) error {
	sealed, err := SealConfig(config)
	if err != nil {
		return err
	}
	digest, err := configStateDigest(sealed)
	if err != nil {
		return err
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if sealed.Revision <= engine.config.Revision {
		return errors.New("policy revision must increase")
	}
	engine.config = sealed
	engine.configDigest = digest
	clear(engine.challenges)
	clear(engine.challengeKeys)
	clear(engine.authorizations)
	return nil
}

// Evaluate authorizes one exact Host-bound effect preview. Policy denials are
// returned as Decision values; malformed trusted inputs return errors.
func (engine *Engine) Evaluate(action host.BoundAction, context Context) (Decision, error) {
	if err := host.ValidateBoundAction(action); err != nil {
		return Decision{}, fmt.Errorf("validate bound action: %w", err)
	}
	if err := validateContext(context); err != nil {
		return Decision{}, err
	}
	decisionID, err := newID("decision")
	if err != nil {
		return Decision{}, err
	}

	engine.mu.Lock()
	defer engine.mu.Unlock()
	engine.pruneChallenges(action, context)
	config := engine.config
	base := Decision{
		DecisionID:     decisionID,
		ControllerID:   action.ControllerID,
		ActorID:        action.ActorID,
		PrincipalID:    context.Principal.ID,
		EffectDigest:   action.EffectDigest,
		PolicyRevision: config.Revision,
		EvaluatedAt:    context.Now,
	}

	if context.EmergencyStopped {
		return denyDecision(
			base,
			"kernel.emergency-stop",
			"policy.emergency_stop",
			"The emergency stop is active for this Host.",
		), nil
	}
	if context.CurrentEpoch != action.ExpectedEpoch {
		return denyDecision(
			base,
			"kernel.stale-epoch",
			"policy.stale_epoch",
			"The action belongs to a stale Host epoch.",
		), nil
	}
	if context.Now.Clock != action.ValidUntil.Clock || context.Now.Value >= action.ValidUntil.Value {
		return denyDecision(
			base,
			"kernel.expired-binding",
			"policy.expired_binding",
			"The Host binding is expired or uses a different clock.",
		), nil
	}

	authorizationKey := makeAuthorizationKey(action, context, config.Revision)
	if existing, found := engine.authorizations[authorizationKey]; found {
		if existing.confirmationID == context.ConfirmationID {
			return cloneDecision(existing.decision), nil
		}
		return denyDecision(
			base,
			"kernel.invalid-confirmation",
			"policy.invalid_confirmation",
			"The confirmation does not match the existing authorization for this binding.",
		), nil
	}

	result, matched, reason, summary := evaluateGameplay(config, action, context)
	base.Result = result
	base.MatchedRuleIDs = matched
	base.ReasonCode = reason
	base.HumanSummary = summary
	if result == Deny {
		return base, nil
	}
	if result == RequireConfirmation {
		confirmed, rejection := engine.consumeConfirmation(action, context, config)
		if rejection != nil {
			rejection.DecisionID = decisionID
			rejection.EvaluatedAt = context.Now
			return *rejection, nil
		}
		if !confirmed {
			ttl, supported := confirmationTTL(config.ConfirmationTTL, context.Now.Clock)
			if !supported {
				return denyDecision(
					base,
					"kernel.confirmation-clock-disabled",
					"policy.confirmation_clock_disabled",
					"Gameplay confirmation is disabled for this Host clock.",
				), nil
			}
			challenge, err := engine.issueConfirmation(action, context, config, ttl)
			if err != nil {
				return Decision{}, err
			}
			base.Confirmation = &challenge
			return base, nil
		}
		base.Result = Allow
		base.MatchedRuleIDs = appendUnique(base.MatchedRuleIDs, "confirmation.consumed")
		base.ReasonCode = "policy.confirmed"
		base.HumanSummary = "The approved single-use confirmation matches this exact effect preview."
	} else if context.ConfirmationID != "" {
		return denyDecision(
			base,
			"kernel.unexpected-confirmation",
			"policy.confirmation_not_required",
			"A confirmation cannot be attached to an action that does not require one.",
		), nil
	}

	limits, deltas, budgetDecision, err := engine.reserveBudgets(action, context, config, base)
	if err != nil {
		return Decision{}, err
	}
	if budgetDecision != nil {
		return *budgetDecision, nil
	}
	base.EffectiveLimits = limits
	if len(engine.authorizations) >= maxAuthorizations {
		engine.removeFinalizedAuthorizations()
	}
	if len(engine.authorizations) >= maxAuthorizations {
		engine.rollbackDeltas(deltas)
		return denyDecision(
			base,
			"kernel.authorization-capacity",
			"policy.authorization_capacity",
			"The policy authorization capacity is temporarily exhausted.",
		), nil
	}
	engine.authorizations[authorizationKey] = &authorizationState{
		decision:       cloneDecision(base),
		confirmationID: context.ConfirmationID,
	}
	engine.decisionKeys[base.DecisionID] = authorizationKey
	if len(deltas) > 0 {
		engine.reservations[base.DecisionID] = reservation{deltas: deltas}
	}
	return cloneDecision(base), nil
}

// Approve marks one challenge as approved by a trusted principal. Approval
// does not authorize execution until Evaluate consumes the exact challenge.
func (engine *Engine) Approve(
	challengeID string,
	approver host.Principal,
	now host.Timepoint,
) (ConfirmationChallenge, error) {
	if err := validateID("challenge_id", challengeID, false); err != nil {
		return ConfirmationChallenge{}, err
	}
	if err := host.ValidatePrincipal(approver); err != nil {
		return ConfirmationChallenge{}, err
	}
	if err := now.Validate("now"); err != nil {
		return ConfirmationChallenge{}, err
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	state, exists := engine.challenges[challengeID]
	if !exists {
		return ConfirmationChallenge{}, errors.New("confirmation challenge is missing or expired")
	}
	if state.challenge.ExpiresAt.Clock != now.Clock {
		return ConfirmationChallenge{}, errors.New(
			"confirmation approval clock does not match the challenge",
		)
	}
	if now.Value >= state.challenge.ExpiresAt.Value {
		delete(engine.challenges, challengeID)
		delete(engine.challengeKeys, state.key)
		return ConfirmationChallenge{}, errors.New(
			"confirmation challenge is missing or expired",
		)
	}
	if state.challenge.PolicyRevision != engine.config.Revision {
		return ConfirmationChallenge{}, errors.New("confirmation challenge belongs to an old policy revision")
	}
	if !principalHasAnyScope(approver, engine.config.ConfirmationScopes) {
		return ConfirmationChallenge{}, errors.New("principal cannot approve gameplay confirmations")
	}
	state.approved = true
	return state.challenge, nil
}

// Finalize commits or rolls back the budget reservation attached to a policy
// decision. A rollback permits a fresh evaluation of the same binding.
func (engine *Engine) Finalize(decisionID string, committed bool) bool {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	key, exists := engine.decisionKeys[decisionID]
	if !exists {
		return false
	}
	if reserved, exists := engine.reservations[decisionID]; exists {
		if !committed {
			engine.rollbackDeltas(reserved.deltas)
		}
		delete(engine.reservations, decisionID)
	}
	state, exists := engine.authorizations[key]
	if !exists {
		delete(engine.decisionKeys, decisionID)
		return true
	}
	if committed {
		state.finalized = true
	} else {
		delete(engine.authorizations, key)
	}
	delete(engine.decisionKeys, decisionID)
	return true
}

// DiscardConfirmation removes one exact challenge when its owning operation is
// abandoned before approval. A different or already-consumed challenge is not
// affected.
func (engine *Engine) DiscardConfirmation(challenge ConfirmationChallenge) bool {
	if err := validateConfirmationChallenge(challenge); err != nil {
		return false
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	state, exists := engine.challenges[challenge.ChallengeID]
	if !exists || state.challenge != challenge {
		return false
	}
	delete(engine.challenges, challenge.ChallengeID)
	delete(engine.challengeKeys, state.key)
	return true
}

func evaluateGameplay(
	config Config,
	action host.BoundAction,
	context Context,
) (Result, []string, string, string) {
	result := Allow
	reason := "policy.allowed"
	summary := "All Host-bound effects are allowed by the active gameplay policy."
	selectedReason := false
	matchedIDs := make([]string, 0, len(action.Effects))
	for _, effect := range action.Effects {
		if kernel, denied := kernelDecision(config, effect); denied {
			matchedIDs = appendUnique(matchedIDs, kernel.ruleID)
			if !selectedReason || resultRank(kernel.result) > resultRank(result) {
				result, reason, summary = kernel.result, kernel.reasonCode, kernel.summary
				selectedReason = true
			}
			continue
		}
		matchedRule := false
		for _, rule := range config.Rules {
			if !ruleMatches(rule, effect, action, context) {
				continue
			}
			matchedRule = true
			matchedIDs = appendUnique(matchedIDs, rule.RuleID)
			if !selectedReason || resultRank(rule.Result) > resultRank(result) {
				result, reason, summary = rule.Result, rule.ReasonCode, rule.HumanSummary
				selectedReason = true
			}
		}
		if !matchedRule {
			fallback := profileDecision(config.Profile, effect)
			matchedIDs = appendUnique(matchedIDs, fallback.ruleID)
			if !selectedReason || resultRank(fallback.result) > resultRank(result) {
				result, reason, summary = fallback.result, fallback.reasonCode, fallback.summary
				selectedReason = true
			}
		}
	}
	return result, matchedIDs, reason, summary
}

func (engine *Engine) issueConfirmation(
	action host.BoundAction,
	context Context,
	config Config,
	ttl uint64,
) (ConfirmationChallenge, error) {
	key := makeChallengeKey(action, context, config.Revision)
	if existingID, exists := engine.challengeKeys[key]; exists {
		if existing, found := engine.challenges[existingID]; found {
			return existing.challenge, nil
		}
		delete(engine.challengeKeys, key)
	}
	if len(engine.challenges) >= maxChallenges {
		return ConfirmationChallenge{}, errors.New("confirmation challenge capacity is exhausted")
	}
	id, err := newID("confirmation")
	if err != nil {
		return ConfirmationChallenge{}, err
	}
	expiresAt := action.ValidUntil.Value
	if context.Now.Value <= maxJSONSafeInteger-int64(ttl) {
		configuredExpiry := context.Now.Value + int64(ttl)
		if configuredExpiry < expiresAt {
			expiresAt = configuredExpiry
		}
	}
	challenge := ConfirmationChallenge{
		ChallengeID:    id,
		ControllerID:   action.ControllerID,
		ActorID:        action.ActorID,
		PrincipalID:    context.Principal.ID,
		EffectDigest:   action.EffectDigest,
		Epoch:          action.ExpectedEpoch,
		ExpiresAt:      host.Timepoint{Clock: context.Now.Clock, Value: expiresAt},
		PolicyRevision: config.Revision,
		SingleUse:      true,
	}
	engine.challenges[id] = &challengeState{
		challenge:  challenge,
		key:        key,
		clockScope: challengeClockScope(action, context),
	}
	engine.challengeKeys[key] = id
	return challenge, nil
}

func confirmationTTL(value ConfirmationDurations, clock host.ClockMode) (uint64, bool) {
	var ttl uint64
	switch clock {
	case host.ClockEvent:
		ttl = value.Event
	case host.ClockStep:
		ttl = value.Step
	case host.ClockRealtime:
		ttl = value.Realtime
	default:
		return 0, false
	}
	return ttl, ttl != 0
}

func (engine *Engine) consumeConfirmation(
	action host.BoundAction,
	context Context,
	config Config,
) (bool, *Decision) {
	if context.ConfirmationID == "" {
		return false, nil
	}
	base := Decision{
		Result:         Deny,
		ControllerID:   action.ControllerID,
		ActorID:        action.ActorID,
		PrincipalID:    context.Principal.ID,
		EffectDigest:   action.EffectDigest,
		PolicyRevision: config.Revision,
		MatchedRuleIDs: []string{"kernel.invalid-confirmation"},
		ReasonCode:     "policy.invalid_confirmation",
		HumanSummary:   "The confirmation is missing, unapproved, expired, consumed, or bound to another action.",
	}
	state, exists := engine.challenges[context.ConfirmationID]
	if !exists || !state.approved {
		return false, &base
	}
	challenge := state.challenge
	key := makeChallengeKey(action, context, config.Revision)
	if state.key != key || challenge.ControllerID != action.ControllerID ||
		challenge.ActorID != action.ActorID ||
		challenge.PrincipalID != context.Principal.ID || challenge.EffectDigest != action.EffectDigest ||
		challenge.Epoch != action.ExpectedEpoch || challenge.PolicyRevision != config.Revision ||
		challenge.ExpiresAt.Clock != context.Now.Clock || context.Now.Value >= challenge.ExpiresAt.Value {
		return false, &base
	}
	delete(engine.challenges, challenge.ChallengeID)
	delete(engine.challengeKeys, state.key)
	return true, nil
}

func (engine *Engine) pruneChallenges(action host.BoundAction, context Context) {
	scope := challengeClockScope(action, context)
	for id, state := range engine.challenges {
		if state.clockScope == scope &&
			context.Now.Value >= state.challenge.ExpiresAt.Value {
			delete(engine.challenges, id)
			delete(engine.challengeKeys, state.key)
		}
	}
}

func (engine *Engine) reserveBudgets(
	action host.BoundAction,
	context Context,
	config Config,
	base Decision,
) ([]EffectiveLimit, []budgetDelta, *Decision, error) {
	limits := make([]EffectiveLimit, 0, len(config.Budgets))
	deltas := make([]budgetDelta, 0, len(config.Budgets))
	for _, budget := range config.Budgets {
		matched := false
		var quantity uint64
		for _, effect := range action.Effects {
			if !budgetMatches(budget, effect, action, context) {
				continue
			}
			matched = true
			if math.MaxUint64-quantity < effect.Quantity {
				return nil, nil, nil, errors.New("effect quantity overflow")
			}
			quantity += effect.Quantity
		}
		if !matched {
			continue
		}
		key, publicKey, err := makeBudgetKey(budget, action, context)
		if err != nil {
			engine.rollbackDeltas(deltas)
			return nil, nil, nil, err
		}
		current := engine.usage[key]
		if current.actions == math.MaxUint64 || math.MaxUint64-current.quantity < quantity {
			engine.rollbackDeltas(deltas)
			return nil, nil, nil, errors.New("policy budget usage overflow")
		}
		next := budgetUsage{actions: current.actions + 1, quantity: current.quantity + quantity}
		if budget.MaxActions > 0 && next.actions > uint64(budget.MaxActions) ||
			budget.MaxQuantity > 0 && next.quantity > budget.MaxQuantity {
			engine.rollbackDeltas(deltas)
			denied := denyDecision(
				base,
				budget.BudgetID,
				"policy.budget_exceeded",
				"The action would exceed an active gameplay budget.",
			)
			return nil, nil, &denied, nil
		}
		engine.usage[key] = next
		deltas = append(deltas, budgetDelta{key: key, actions: 1, quantity: quantity})
		limit := EffectiveLimit{
			BudgetID:     budget.BudgetID,
			UsageKey:     publicKey,
			MaxActions:   uint64(budget.MaxActions),
			ActionsUsed:  next.actions,
			MaxQuantity:  budget.MaxQuantity,
			QuantityUsed: next.quantity,
		}
		if budget.MaxActions > 0 {
			limit.ActionsRemaining = uint64(budget.MaxActions) - next.actions
		}
		if budget.MaxQuantity > 0 {
			limit.QuantityRemaining = budget.MaxQuantity - next.quantity
		}
		limits = append(limits, limit)
	}
	return limits, deltas, nil, nil
}

func makeBudgetKey(budget Budget, action host.BoundAction, context Context) (string, string, error) {
	scope := context.ServerID
	switch budget.Layer {
	case LayerWorld:
		scope = action.ExpectedEpoch.WorldID
	case LayerOwner:
		scope = context.OwnerID
	case LayerActor:
		scope = action.ActorID
	case LayerTask:
		scope = action.TaskID
	}
	bucket := "epoch"
	if budget.Window.Value > 0 {
		if budget.Window.Clock != context.Now.Clock {
			return "", "", errors.New("budget window clock does not match the Host clock")
		}
		bucket = fmt.Sprintf("%d", uint64(context.Now.Value)/budget.Window.Value)
	}
	key := fmt.Sprintf(
		"%s\x00%s\x00%s\x00%s\x00%d\x00%d\x00%d\x00%s",
		budget.BudgetID,
		scope,
		action.ExpectedEpoch.SessionID,
		action.ExpectedEpoch.WorldID,
		action.ExpectedEpoch.Host,
		action.ExpectedEpoch.World,
		action.ExpectedEpoch.Timeline,
		bucket,
	)
	digest := sha256.Sum256([]byte(key))
	return key, budget.BudgetID + "." + hex.EncodeToString(digest[:8]), nil
}

func (engine *Engine) rollbackDeltas(deltas []budgetDelta) {
	for index := len(deltas) - 1; index >= 0; index-- {
		delta := deltas[index]
		current := engine.usage[delta.key]
		if current.actions < delta.actions || current.quantity < delta.quantity {
			delete(engine.usage, delta.key)
			continue
		}
		current.actions -= delta.actions
		current.quantity -= delta.quantity
		if current.actions == 0 && current.quantity == 0 {
			delete(engine.usage, delta.key)
		} else {
			engine.usage[delta.key] = current
		}
	}
}

func (engine *Engine) removeFinalizedAuthorizations() {
	for key, state := range engine.authorizations {
		if state.finalized {
			delete(engine.authorizations, key)
		}
	}
}

func validateContext(context Context) error {
	if err := context.Now.Validate("now"); err != nil {
		return err
	}
	if err := context.CurrentEpoch.Validate("current_epoch"); err != nil {
		return err
	}
	if err := host.ValidatePrincipal(context.Principal); err != nil {
		return err
	}
	if err := validateID("server_id", context.ServerID, false); err != nil {
		return err
	}
	if context.OwnerID != "" {
		if err := validateID("owner_id", context.OwnerID, false); err != nil {
			return err
		}
	}
	if context.ConfirmationID != "" {
		if err := validateID("confirmation_id", context.ConfirmationID, false); err != nil {
			return err
		}
	}
	return nil
}

func principalHasAnyScope(principal host.Principal, scopes []string) bool {
	for _, granted := range principal.GrantedScopes {
		if slices.Contains(scopes, granted) {
			return true
		}
	}
	return false
}

func denyDecision(base Decision, ruleID, reason, summary string) Decision {
	base.Result = Deny
	base.MatchedRuleIDs = appendUnique(base.MatchedRuleIDs, ruleID)
	base.ReasonCode = reason
	base.HumanSummary = summary
	base.Confirmation = nil
	base.EffectiveLimits = nil
	return base
}

func appendUnique(values []string, value string) []string {
	if slices.Contains(values, value) {
		return values
	}
	return append(values, value)
}

func makeAuthorizationKey(action host.BoundAction, context Context, revision uint64) string {
	return fmt.Sprintf(
		"%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%d\x00%d\x00%d\x00%d",
		action.BindingID,
		action.RequestDigest,
		action.EffectDigest,
		context.Principal.ID,
		context.ServerID,
		context.OwnerID,
		action.ExpectedEpoch.SessionID,
		action.ExpectedEpoch.Host,
		action.ExpectedEpoch.World,
		action.ExpectedEpoch.Timeline,
		revision,
	)
}

func makeChallengeKey(action host.BoundAction, context Context, revision uint64) string {
	return makeAuthorizationKey(action, context, revision)
}

func challengeClockScope(action host.BoundAction, context Context) string {
	return fmt.Sprintf(
		"%s\x00%s\x00%s",
		context.ServerID,
		action.ExpectedEpoch.WorldID,
		context.Now.Clock,
	)
}

func cloneDecision(decision Decision) Decision {
	cloned := decision
	cloned.MatchedRuleIDs = append([]string(nil), decision.MatchedRuleIDs...)
	cloned.EffectiveLimits = append([]EffectiveLimit(nil), decision.EffectiveLimits...)
	if decision.Confirmation != nil {
		challenge := *decision.Confirmation
		cloned.Confirmation = &challenge
	}
	return cloned
}

func newID(prefix string) (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate %s id: %w", prefix, err)
	}
	return prefix + "." + hex.EncodeToString(random[:]), nil
}
