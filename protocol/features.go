package protocol

import "sort"

const (
	FeatureMemoryArchive   = "memory-archive-v1"
	FeatureBeliefConflicts = "belief-conflicts-v1"
	FeatureGoalCandidates  = "goal-candidates-v1"
	FeatureActorActivity   = "actor-activity-v1"
	FeatureArbitration     = "arbitration-v1"
	// FeatureOutcomeReporting opts a session into game-authoritative outcome
	// reports, late occurrence-time merging, and durable outcome metadata.
	// Sessions created before this feature retain their historical reducer
	// semantics when old event logs are replayed.
	FeatureOutcomeReporting = "outcome-reporting-v1"
)

var supportedFeatures = map[string]struct{}{
	FeatureMemoryArchive:    {},
	FeatureBeliefConflicts:  {},
	FeatureGoalCandidates:   {},
	FeatureActorActivity:    {},
	FeatureArbitration:      {},
	FeatureOutcomeReporting: {},
}

func SupportedFeatures() []string {
	result := make([]string, 0, len(supportedFeatures))
	for feature := range supportedFeatures {
		result = append(result, feature)
	}
	sort.Strings(result)
	return result
}

func IsSupportedFeature(feature string) bool {
	_, exists := supportedFeatures[feature]
	return exists
}

func HasFeature(features []string, wanted string) bool {
	for _, feature := range features {
		if feature == wanted {
			return true
		}
	}
	return false
}
