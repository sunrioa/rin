package policy

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/sunrioa/rin/host"
)

const maxJSONSafeInteger = 9_007_199_254_740_991

var safeIDPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`)
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// SealConfig validates and deterministically normalizes one policy revision.
func SealConfig(config Config) (Config, error) {
	sealed := cloneConfig(config)
	if err := normalizeStrings("known_effect_kinds", sealed.KnownEffectKinds, true); err != nil {
		return Config{}, err
	}
	if err := normalizeStrings("known_scopes", sealed.KnownScopes, true); err != nil {
		return Config{}, err
	}
	if err := normalizeStrings("confirmation_scopes", sealed.ConfirmationScopes, true); err != nil {
		return Config{}, err
	}
	for index := range sealed.Rules {
		if err := normalizeRule(&sealed.Rules[index], index); err != nil {
			return Config{}, err
		}
	}
	for index := range sealed.Budgets {
		if err := normalizeBudget(&sealed.Budgets[index], index); err != nil {
			return Config{}, err
		}
	}
	slices.SortFunc(sealed.Rules, func(left, right Rule) int {
		if layerRank(left.Layer) != layerRank(right.Layer) {
			return layerRank(left.Layer) - layerRank(right.Layer)
		}
		if left.Priority != right.Priority {
			if left.Priority > right.Priority {
				return -1
			}
			return 1
		}
		return strings.Compare(left.RuleID, right.RuleID)
	})
	slices.SortFunc(sealed.Budgets, func(left, right Budget) int {
		if layerRank(left.Layer) != layerRank(right.Layer) {
			return layerRank(left.Layer) - layerRank(right.Layer)
		}
		return strings.Compare(left.BudgetID, right.BudgetID)
	})
	if err := validateConfig(sealed); err != nil {
		return Config{}, err
	}
	return sealed, nil
}

// Validate verifies a normalized policy configuration.
func (config Config) Validate() error {
	sealed, err := SealConfig(config)
	if err != nil {
		return err
	}
	if !slices.Equal(config.KnownEffectKinds, sealed.KnownEffectKinds) ||
		!slices.Equal(config.KnownScopes, sealed.KnownScopes) ||
		!slices.Equal(config.ConfirmationScopes, sealed.ConfirmationScopes) ||
		!slices.EqualFunc(config.Rules, sealed.Rules, equalRule) ||
		!slices.EqualFunc(config.Budgets, sealed.Budgets, equalBudget) {
		return errors.New("policy config must be normalized with SealConfig")
	}
	return nil
}

// ValidateDecision verifies one persisted or transported policy decision.
func ValidateDecision(decision Decision) error {
	identifiers := []struct{ field, value string }{
		{field: "decision_id", value: decision.DecisionID},
		{field: "controller_id", value: decision.ControllerID},
		{field: "actor_id", value: decision.ActorID},
		{field: "principal_id", value: decision.PrincipalID},
	}
	for _, identifier := range identifiers {
		if err := validateID(identifier.field, identifier.value, false); err != nil {
			return err
		}
	}
	if !validResult(decision.Result) {
		return errors.New("decision result is not supported")
	}
	if !digestPattern.MatchString(decision.EffectDigest) {
		return errors.New("effect_digest must be a lowercase SHA-256 digest")
	}
	if decision.PolicyRevision == 0 || decision.PolicyRevision > maxJSONSafeInteger {
		return errors.New("policy_revision must be a positive JSON-safe integer")
	}
	if len(decision.MatchedRuleIDs) == 0 || len(decision.MatchedRuleIDs) > 1_024 {
		return errors.New("matched_rule_ids must contain between 1 and 1024 values")
	}
	seenRules := make(map[string]struct{}, len(decision.MatchedRuleIDs))
	for index, ruleID := range decision.MatchedRuleIDs {
		if err := validateID(fmt.Sprintf("matched_rule_ids[%d]", index), ruleID, true); err != nil {
			return err
		}
		if _, duplicate := seenRules[ruleID]; duplicate {
			return errors.New("matched_rule_ids must not contain duplicates")
		}
		seenRules[ruleID] = struct{}{}
	}
	if err := validateID("reason_code", decision.ReasonCode, true); err != nil {
		return err
	}
	if err := validateText("human_summary", decision.HumanSummary, 500, true); err != nil {
		return err
	}
	if err := decision.EvaluatedAt.Validate("evaluated_at"); err != nil {
		return err
	}
	if len(decision.EffectiveLimits) > 128 {
		return errors.New("effective_limits must contain at most 128 values")
	}
	seenLimits := make(map[string]struct{}, len(decision.EffectiveLimits))
	for index, limit := range decision.EffectiveLimits {
		field := fmt.Sprintf("effective_limits[%d]", index)
		if err := validateID(field+".budget_id", limit.BudgetID, true); err != nil {
			return err
		}
		if err := validateID(field+".usage_key", limit.UsageKey, true); err != nil {
			return err
		}
		if _, duplicate := seenLimits[limit.BudgetID]; duplicate {
			return errors.New("effective_limits must not contain duplicate budget_id values")
		}
		seenLimits[limit.BudgetID] = struct{}{}
		counters := []uint64{
			limit.MaxActions,
			limit.ActionsUsed,
			limit.ActionsRemaining,
			limit.MaxQuantity,
			limit.QuantityUsed,
			limit.QuantityRemaining,
		}
		for _, counter := range counters {
			if counter > maxJSONSafeInteger {
				return fmt.Errorf("%s counters must be JSON-safe integers", field)
			}
		}
		if limit.MaxActions == 0 {
			if limit.ActionsRemaining != 0 {
				return fmt.Errorf("%s unbounded action counter has remaining capacity", field)
			}
		} else if limit.ActionsUsed > limit.MaxActions ||
			limit.ActionsRemaining != limit.MaxActions-limit.ActionsUsed {
			return fmt.Errorf("%s action counters are inconsistent", field)
		}
		if limit.MaxQuantity == 0 {
			if limit.QuantityRemaining != 0 {
				return fmt.Errorf("%s unbounded quantity counter has remaining capacity", field)
			}
		} else if limit.QuantityUsed > limit.MaxQuantity ||
			limit.QuantityRemaining != limit.MaxQuantity-limit.QuantityUsed {
			return fmt.Errorf("%s quantity counters are inconsistent", field)
		}
	}
	if decision.Result != Allow && len(decision.EffectiveLimits) != 0 {
		return errors.New("only allow decisions may contain effective_limits")
	}
	if decision.Result == RequireConfirmation {
		if decision.Confirmation == nil {
			return errors.New("confirmation decision requires a challenge")
		}
		if err := validateConfirmationChallenge(*decision.Confirmation); err != nil {
			return err
		}
		challenge := decision.Confirmation
		if challenge.ControllerID != decision.ControllerID ||
			challenge.ActorID != decision.ActorID ||
			challenge.PrincipalID != decision.PrincipalID ||
			challenge.EffectDigest != decision.EffectDigest ||
			challenge.PolicyRevision != decision.PolicyRevision {
			return errors.New("confirmation challenge does not match decision")
		}
	} else if decision.Confirmation != nil {
		return errors.New("only require_confirmation decisions may contain a challenge")
	}
	return nil
}

func validateConfirmationChallenge(challenge ConfirmationChallenge) error {
	identifiers := []struct{ field, value string }{
		{field: "confirmation.challenge_id", value: challenge.ChallengeID},
		{field: "confirmation.controller_id", value: challenge.ControllerID},
		{field: "confirmation.actor_id", value: challenge.ActorID},
		{field: "confirmation.principal_id", value: challenge.PrincipalID},
	}
	for _, identifier := range identifiers {
		if err := validateID(identifier.field, identifier.value, false); err != nil {
			return err
		}
	}
	if !digestPattern.MatchString(challenge.EffectDigest) {
		return errors.New("confirmation.effect_digest must be a lowercase SHA-256 digest")
	}
	if err := challenge.Epoch.Validate("confirmation.epoch"); err != nil {
		return err
	}
	if err := challenge.ExpiresAt.Validate("confirmation.expires_at"); err != nil {
		return err
	}
	if challenge.PolicyRevision == 0 || challenge.PolicyRevision > maxJSONSafeInteger {
		return errors.New("confirmation.policy_revision is invalid")
	}
	if !challenge.SingleUse {
		return errors.New("confirmation challenge must be single-use")
	}
	return nil
}

// CloneDecision returns a defensive copy suitable for transport or storage.
func CloneDecision(decision Decision) Decision {
	return cloneDecision(decision)
}

func validateConfig(config Config) error {
	if config.Revision == 0 || config.Revision > maxJSONSafeInteger {
		return errors.New("revision must be a positive JSON-safe integer")
	}
	if !validProfile(config.Profile) {
		return fmt.Errorf("unsupported profile %q", config.Profile)
	}
	if len(config.KnownEffectKinds) > 512 {
		return errors.New("known_effect_kinds must contain at most 512 values")
	}
	if len(config.KnownScopes) > 512 {
		return errors.New("known_scopes must contain at most 512 values")
	}
	if len(config.Rules) > 512 {
		return errors.New("rules must contain at most 512 values")
	}
	if len(config.Budgets) > 128 {
		return errors.New("budgets must contain at most 128 values")
	}
	if err := config.ConfirmationTTL.Validate("confirmation_ttl"); err != nil {
		return err
	}
	if len(config.ConfirmationScopes) == 0 || len(config.ConfirmationScopes) > 16 {
		return errors.New("confirmation_scopes must contain between 1 and 16 values")
	}
	ruleIDs := make(map[string]struct{}, len(config.Rules))
	for _, rule := range config.Rules {
		if _, duplicate := ruleIDs[rule.RuleID]; duplicate {
			return fmt.Errorf("duplicate rule_id %q", rule.RuleID)
		}
		ruleIDs[rule.RuleID] = struct{}{}
	}
	budgetIDs := make(map[string]struct{}, len(config.Budgets))
	for _, budget := range config.Budgets {
		if _, duplicate := budgetIDs[budget.BudgetID]; duplicate {
			return fmt.Errorf("duplicate budget_id %q", budget.BudgetID)
		}
		budgetIDs[budget.BudgetID] = struct{}{}
	}
	return nil
}

func normalizeRule(rule *Rule, index int) error {
	field := fmt.Sprintf("rules[%d]", index)
	if err := validateID(field+".rule_id", rule.RuleID, true); err != nil {
		return err
	}
	if !validLayer(rule.Layer) {
		return fmt.Errorf("%s.layer is not supported", field)
	}
	if rule.Priority < -10_000 || rule.Priority > 10_000 {
		return fmt.Errorf("%s.priority must be between -10000 and 10000", field)
	}
	if !validResult(rule.Result) {
		return fmt.Errorf("%s.result is not supported", field)
	}
	if err := normalizeStrings(field+".effect_kinds", rule.EffectKinds, true); err != nil {
		return err
	}
	if err := normalizeOperations(field+".operations", rule.Operations); err != nil {
		return err
	}
	if err := normalizeOwnership(field+".ownership", rule.Ownership); err != nil {
		return err
	}
	if err := normalizeStrings(field+".scopes", rule.Scopes, true); err != nil {
		return err
	}
	if err := normalizeStrings(field+".tags_all", rule.TagsAll, true); err != nil {
		return err
	}
	if err := normalizeStrings(field+".tags_any", rule.TagsAny, true); err != nil {
		return err
	}
	if len(rule.EffectKinds) > 64 || len(rule.Operations) > 8 ||
		len(rule.Ownership) > 7 || len(rule.Scopes) > 64 ||
		len(rule.TagsAll) > 32 || len(rule.TagsAny) > 32 {
		return fmt.Errorf("%s contains too many match values", field)
	}
	if rule.RiskAtLeast != "" && !validRisk(rule.RiskAtLeast) {
		return fmt.Errorf("%s.risk_at_least is not supported", field)
	}
	if rule.RiskAtMost != "" && !validRisk(rule.RiskAtMost) {
		return fmt.Errorf("%s.risk_at_most is not supported", field)
	}
	if rule.RiskAtLeast != "" && rule.RiskAtMost != "" &&
		riskRank(rule.RiskAtLeast) > riskRank(rule.RiskAtMost) {
		return fmt.Errorf("%s risk range is empty", field)
	}
	if err := validateID(field+".reason_code", rule.ReasonCode, true); err != nil {
		return err
	}
	if err := validateText(field+".human_summary", rule.HumanSummary, 300, true); err != nil {
		return err
	}
	return nil
}

func normalizeBudget(budget *Budget, index int) error {
	field := fmt.Sprintf("budgets[%d]", index)
	if err := validateID(field+".budget_id", budget.BudgetID, true); err != nil {
		return err
	}
	if !validLayer(budget.Layer) {
		return fmt.Errorf("%s.layer is not supported", field)
	}
	if err := normalizeStrings(field+".effect_kinds", budget.EffectKinds, true); err != nil {
		return err
	}
	if err := normalizeOperations(field+".operations", budget.Operations); err != nil {
		return err
	}
	if err := normalizeStrings(field+".tags_any", budget.TagsAny, true); err != nil {
		return err
	}
	if len(budget.EffectKinds) > 64 || len(budget.Operations) > 8 || len(budget.TagsAny) > 32 {
		return fmt.Errorf("%s contains too many match values", field)
	}
	if budget.MaxActions == 0 && budget.MaxQuantity == 0 {
		return fmt.Errorf("%s must set max_actions or max_quantity", field)
	}
	if budget.MaxQuantity > maxJSONSafeInteger {
		return fmt.Errorf("%s.max_quantity must be a JSON-safe integer", field)
	}
	if budget.Window.Value == 0 {
		if budget.Window.Clock != "" {
			return fmt.Errorf("%s.window.clock must be empty when no window is configured", field)
		}
	} else if err := budget.Window.Validate(field + ".window"); err != nil {
		return err
	}
	return nil
}

func normalizeStrings(field string, values []string, namespaced bool) error {
	slices.Sort(values)
	for index, value := range values {
		if err := validateID(fmt.Sprintf("%s[%d]", field, index), value, namespaced); err != nil {
			return err
		}
		if index > 0 && values[index-1] == value {
			return fmt.Errorf("%s must not contain duplicates", field)
		}
	}
	return nil
}

func normalizeOperations(field string, values []host.EffectOperation) error {
	slices.Sort(values)
	for index, value := range values {
		if !validOperation(value) {
			return fmt.Errorf("%s[%d] is not supported", field, index)
		}
		if index > 0 && values[index-1] == value {
			return fmt.Errorf("%s must not contain duplicates", field)
		}
	}
	return nil
}

func normalizeOwnership(field string, values []host.OwnershipClass) error {
	slices.Sort(values)
	for index, value := range values {
		if !validOwnership(value) {
			return fmt.Errorf("%s[%d] is not supported", field, index)
		}
		if index > 0 && values[index-1] == value {
			return fmt.Errorf("%s must not contain duplicates", field)
		}
	}
	return nil
}

func validateID(field, value string, namespaced bool) error {
	if len(value) == 0 || len(value) > 96 || !safeIDPattern.MatchString(value) {
		return fmt.Errorf("%s must be a lowercase safe identifier of at most 96 bytes", field)
	}
	if namespaced && !strings.ContainsRune(value, '.') {
		return fmt.Errorf("%s must be namespaced", field)
	}
	return nil
}

func validateText(field, value string, maximum int, required bool) error {
	if required && strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", field)
	}
	if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return fmt.Errorf("%s must be valid UTF-8 without NUL", field)
	}
	if utf8.RuneCountInString(value) > maximum {
		return fmt.Errorf("%s must contain at most %d characters", field, maximum)
	}
	return nil
}

func cloneConfig(config Config) Config {
	cloned := config
	cloned.KnownEffectKinds = append([]string(nil), config.KnownEffectKinds...)
	cloned.KnownScopes = append([]string(nil), config.KnownScopes...)
	cloned.ConfirmationScopes = append([]string(nil), config.ConfirmationScopes...)
	cloned.Rules = make([]Rule, len(config.Rules))
	for index, rule := range config.Rules {
		cloned.Rules[index] = cloneRule(rule)
	}
	cloned.Budgets = make([]Budget, len(config.Budgets))
	for index, budget := range config.Budgets {
		cloned.Budgets[index] = cloneBudget(budget)
	}
	return cloned
}

func cloneRule(rule Rule) Rule {
	cloned := rule
	cloned.EffectKinds = append([]string(nil), rule.EffectKinds...)
	cloned.Operations = append([]host.EffectOperation(nil), rule.Operations...)
	cloned.Ownership = append([]host.OwnershipClass(nil), rule.Ownership...)
	cloned.Scopes = append([]string(nil), rule.Scopes...)
	cloned.TagsAll = append([]string(nil), rule.TagsAll...)
	cloned.TagsAny = append([]string(nil), rule.TagsAny...)
	if rule.Reversible != nil {
		value := *rule.Reversible
		cloned.Reversible = &value
	}
	return cloned
}

func cloneBudget(budget Budget) Budget {
	cloned := budget
	cloned.EffectKinds = append([]string(nil), budget.EffectKinds...)
	cloned.Operations = append([]host.EffectOperation(nil), budget.Operations...)
	cloned.TagsAny = append([]string(nil), budget.TagsAny...)
	return cloned
}

func equalRule(left, right Rule) bool {
	return left.RuleID == right.RuleID && left.Layer == right.Layer &&
		left.Priority == right.Priority && left.Result == right.Result &&
		slices.Equal(left.EffectKinds, right.EffectKinds) &&
		slices.Equal(left.Operations, right.Operations) &&
		slices.Equal(left.Ownership, right.Ownership) &&
		slices.Equal(left.Scopes, right.Scopes) &&
		slices.Equal(left.TagsAll, right.TagsAll) &&
		slices.Equal(left.TagsAny, right.TagsAny) &&
		left.RiskAtLeast == right.RiskAtLeast && left.RiskAtMost == right.RiskAtMost &&
		equalBoolPointer(left.Reversible, right.Reversible) &&
		left.ReasonCode == right.ReasonCode && left.HumanSummary == right.HumanSummary
}

func equalBudget(left, right Budget) bool {
	return left.BudgetID == right.BudgetID && left.Layer == right.Layer &&
		slices.Equal(left.EffectKinds, right.EffectKinds) &&
		slices.Equal(left.Operations, right.Operations) &&
		slices.Equal(left.TagsAny, right.TagsAny) &&
		left.MaxActions == right.MaxActions && left.MaxQuantity == right.MaxQuantity &&
		left.Window == right.Window
}

func equalBoolPointer(left, right *bool) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func validProfile(value Profile) bool {
	return value == ProfileGuarded || value == ProfileSurvival ||
		value == ProfileOpen || value == ProfilePrivilegedCustom
}

func validLayer(value Layer) bool {
	return value == LayerServer || value == LayerWorld || value == LayerOwner ||
		value == LayerActor || value == LayerTask
}

func layerRank(value Layer) int {
	switch value {
	case LayerServer:
		return 0
	case LayerWorld:
		return 1
	case LayerOwner:
		return 2
	case LayerActor:
		return 3
	case LayerTask:
		return 4
	default:
		return 5
	}
}

func validResult(value Result) bool {
	return value == Allow || value == Deny || value == RequireConfirmation
}

func resultRank(value Result) int {
	switch value {
	case Allow:
		return 0
	case RequireConfirmation:
		return 1
	case Deny:
		return 2
	default:
		return 3
	}
}

func validRisk(value host.RiskLevel) bool {
	return value == host.RiskLow || value == host.RiskModerate ||
		value == host.RiskHigh || value == host.RiskCritical
}

func riskRank(value host.RiskLevel) int {
	switch value {
	case host.RiskLow:
		return 0
	case host.RiskModerate:
		return 1
	case host.RiskHigh:
		return 2
	case host.RiskCritical:
		return 3
	default:
		return -1
	}
}

func validOperation(value host.EffectOperation) bool {
	return value == host.EffectOperationRead || value == host.EffectOperationCreate ||
		value == host.EffectOperationUpdate || value == host.EffectOperationDelete ||
		value == host.EffectOperationTransfer || value == host.EffectOperationConsume ||
		value == host.EffectOperationExecute || value == host.EffectOperationCommunicate
}

func validOwnership(value host.OwnershipClass) bool {
	return value == host.OwnershipUnknown || value == host.OwnershipSystem ||
		value == host.OwnershipActor || value == host.OwnershipController ||
		value == host.OwnershipPlayer || value == host.OwnershipShared ||
		value == host.OwnershipUnowned
}
