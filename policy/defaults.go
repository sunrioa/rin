package policy

import (
	"slices"

	"github.com/sunrioa/rin/host"
)

type effectDecision struct {
	result     Result
	ruleID     string
	reasonCode string
	summary    string
}

var safetyKernelKinds = map[string]string{
	"system.arbitrary-code": "policy.arbitrary_code_denied",
	"system.file-access":    "policy.file_access_denied",
	"system.native-call":    "policy.native_call_denied",
	"authority.forge":       "policy.authority_forgery_denied",
	"secret.exposure":       "policy.secret_exposure_denied",
}

var safetyKernelTags = map[string]string{
	"system.arbitrary-code": "policy.arbitrary_code_denied",
	"system.file-access":    "policy.file_access_denied",
	"system.native-call":    "policy.native_call_denied",
	"authority.forge":       "policy.authority_forgery_denied",
	"secret.exposure":       "policy.secret_exposure_denied",
}

func kernelDecision(config Config, effect host.Effect) (effectDecision, bool) {
	if reason, forbidden := safetyKernelKinds[effect.Kind]; forbidden {
		return effectDecision{
			result: Deny, ruleID: "kernel.forbidden-effect", reasonCode: reason,
			summary: "The effect is forbidden by the Rin safety kernel.",
		}, true
	}
	for _, tag := range effect.Tags {
		if reason, forbidden := safetyKernelTags[tag]; forbidden {
			return effectDecision{
				result: Deny, ruleID: "kernel.forbidden-effect", reasonCode: reason,
				summary: "The effect is forbidden by the Rin safety kernel.",
			}, true
		}
	}
	if effect.Ownership == host.OwnershipUnknown {
		return effectDecision{
			result: Deny, ruleID: "kernel.unknown-ownership",
			reasonCode: "policy.unknown_ownership",
			summary:    "The Host could not establish ownership for this effect.",
		}, true
	}
	if !slices.Contains(config.KnownEffectKinds, effect.Kind) {
		return effectDecision{
			result: Deny, ruleID: "kernel.unknown-effect",
			reasonCode: "policy.unknown_effect",
			summary:    "The effect kind is not registered in the active gameplay policy.",
		}, true
	}
	if !slices.Contains(config.KnownScopes, effect.Scope) {
		return effectDecision{
			result: Deny, ruleID: "kernel.unknown-scope",
			reasonCode: "policy.unknown_scope",
			summary:    "The effect scope is not registered in the active gameplay policy.",
		}, true
	}
	return effectDecision{}, false
}

func profileDecision(profile Profile, effect host.Effect) effectDecision {
	switch profile {
	case ProfileGuarded:
		if (effect.Operation == host.EffectOperationRead ||
			effect.Operation == host.EffectOperationCommunicate) &&
			riskRank(effect.Risk) <= riskRank(host.RiskModerate) {
			return effectDecision{
				result: Allow, ruleID: "profile.guarded.safe-interaction",
				reasonCode: "policy.profile_allow",
				summary:    "The guarded profile allows this bounded interaction.",
			}
		}
		if riskRank(effect.Risk) >= riskRank(host.RiskHigh) {
			return effectDecision{
				result: Deny, ruleID: "profile.guarded.high-risk",
				reasonCode: "policy.profile_deny",
				summary:    "The guarded profile denies high-risk effects by default.",
			}
		}
		if effect.Reversible && (effect.Ownership == host.OwnershipActor ||
			effect.Ownership == host.OwnershipController ||
			effect.Ownership == host.OwnershipUnowned) && effect.Risk == host.RiskLow {
			return effectDecision{
				result: Allow, ruleID: "profile.guarded.low-risk",
				reasonCode: "policy.profile_allow",
				summary:    "The guarded profile allows this reversible low-risk effect.",
			}
		}
		return effectDecision{
			result: RequireConfirmation, ruleID: "profile.guarded.confirm",
			reasonCode: "policy.profile_confirmation",
			summary:    "The guarded profile requires confirmation for this effect.",
		}
	case ProfileSurvival:
		if effect.Risk == host.RiskCritical {
			return effectDecision{
				result: Deny, ruleID: "profile.survival.critical-risk",
				reasonCode: "policy.profile_deny",
				summary:    "The survival profile denies critical-risk effects by default.",
			}
		}
		if effect.Risk == host.RiskHigh || effect.Ownership == host.OwnershipPlayer ||
			effect.Ownership == host.OwnershipShared || effect.Ownership == host.OwnershipSystem {
			return effectDecision{
				result: RequireConfirmation, ruleID: "profile.survival.confirm",
				reasonCode: "policy.profile_confirmation",
				summary:    "The survival profile requires confirmation for protected or high-risk effects.",
			}
		}
		return effectDecision{
			result: Allow, ruleID: "profile.survival.allow",
			reasonCode: "policy.profile_allow",
			summary:    "The survival profile allows this registered gameplay effect.",
		}
	case ProfileOpen, ProfilePrivilegedCustom:
		if effect.Risk == host.RiskCritical {
			return effectDecision{
				result: RequireConfirmation, ruleID: "profile.open.critical-confirm",
				reasonCode: "policy.profile_confirmation",
				summary:    "The open profile still requires confirmation for critical-risk effects.",
			}
		}
		return effectDecision{
			result: Allow, ruleID: "profile.open.allow",
			reasonCode: "policy.profile_allow",
			summary:    "The open profile allows this registered effect.",
		}
	default:
		return effectDecision{
			result: Deny, ruleID: "kernel.invalid-profile",
			reasonCode: "policy.invalid_profile",
			summary:    "The active gameplay profile is invalid.",
		}
	}
}

func ruleMatches(rule Rule, effect host.Effect, action host.BoundAction, context Context) bool {
	if rule.Layer == LayerOwner && context.OwnerID == "" {
		return false
	}
	if rule.Layer == LayerTask && action.TaskID == "" {
		return false
	}
	if len(rule.EffectKinds) > 0 && !slices.Contains(rule.EffectKinds, effect.Kind) {
		return false
	}
	if len(rule.Operations) > 0 && !slices.Contains(rule.Operations, effect.Operation) {
		return false
	}
	if len(rule.Ownership) > 0 && !slices.Contains(rule.Ownership, effect.Ownership) {
		return false
	}
	if len(rule.Scopes) > 0 && !slices.Contains(rule.Scopes, effect.Scope) {
		return false
	}
	if rule.RiskAtLeast != "" && riskRank(effect.Risk) < riskRank(rule.RiskAtLeast) {
		return false
	}
	if rule.RiskAtMost != "" && riskRank(effect.Risk) > riskRank(rule.RiskAtMost) {
		return false
	}
	if rule.Reversible != nil && effect.Reversible != *rule.Reversible {
		return false
	}
	for _, tag := range rule.TagsAll {
		if !slices.Contains(effect.Tags, tag) {
			return false
		}
	}
	if len(rule.TagsAny) > 0 {
		matched := false
		for _, tag := range rule.TagsAny {
			if slices.Contains(effect.Tags, tag) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func budgetMatches(budget Budget, effect host.Effect, action host.BoundAction, context Context) bool {
	if budget.Layer == LayerOwner && context.OwnerID == "" {
		return false
	}
	if budget.Layer == LayerTask && action.TaskID == "" {
		return false
	}
	if len(budget.EffectKinds) > 0 && !slices.Contains(budget.EffectKinds, effect.Kind) {
		return false
	}
	if len(budget.Operations) > 0 && !slices.Contains(budget.Operations, effect.Operation) {
		return false
	}
	if len(budget.TagsAny) == 0 {
		return true
	}
	for _, tag := range budget.TagsAny {
		if slices.Contains(effect.Tags, tag) {
			return true
		}
	}
	return false
}
