package cognition

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/policy"
	"github.com/sunrioa/rin/timeline"
)

func modelContextTimelineFields(input ModelInput) (
	[]timeline.MemoryContextRef,
	[]timeline.SkillContextRef,
) {
	memories := make([]timeline.MemoryContextRef, 0, len(input.Memories))
	for index, match := range input.Memories {
		memories = append(memories, timeline.MemoryContextRef{
			MemoryID: match.Record.MemoryID,
			Domain:   string(match.Record.Namespace.Domain),
			Source:   string(match.Record.Provenance.Source),
			Rank:     uint32(index + 1),
			Digest:   memoryContextDigest(match.Record),
		})
	}
	skills := make([]timeline.SkillContextRef, 0, len(input.Skills))
	for _, skill := range input.Skills {
		skills = append(skills, timeline.SkillContextRef{
			SkillID: skill.SkillID, Version: skill.Version, Digest: skill.Digest,
		})
	}
	return memories, skills
}

func measuredModelUsage(decision ModelDecision, latency uint64) *timeline.ModelUsage {
	usage := &timeline.ModelUsage{Model: decision.ProviderModel, LatencyMillis: uint64Pointer(latency)}
	if decision.Usage.PromptTokens != 0 || decision.Usage.CompletionTokens != 0 ||
		decision.Usage.TotalTokens != 0 {
		prompt := uint64(decision.Usage.PromptTokens)
		completion := uint64(decision.Usage.CompletionTokens)
		total := uint64(decision.Usage.TotalTokens)
		usage.PromptTokens = &prompt
		usage.CompletionTokens = &completion
		usage.TotalTokens = &total
	}
	usage.CacheHitTokens = optionalIntToUint64(decision.Usage.PromptCacheHitTokens)
	usage.CacheMissTokens = optionalIntToUint64(decision.Usage.PromptCacheMissTokens)
	usage.CacheWriteTokens = optionalIntToUint64(decision.Usage.CacheWriteTokens)
	return usage
}

func optionalIntToUint64(value *int) *uint64 {
	if value == nil {
		return nil
	}
	converted := uint64(*value)
	return &converted
}

func operationTimelineEvent(
	task TaskSession,
	kind string,
	view controlplane.OperationView,
	at int64,
) TaskEvent {
	code := string(view.Status)
	summary := view.RejectionMessage
	if view.Outcome != nil {
		code = string(view.Outcome.Status)
		summary = view.Outcome.Summary
	} else if summary == "" && view.PolicyDecision != nil {
		summary = view.PolicyDecision.HumanSummary
	}
	event := TaskEvent{
		Kind: kind, Step: task.Step, Code: code, Summary: summary,
		OperationID: view.OperationID, AtUnixMillis: at,
		Operation: operationTimelineSummary(view), Policy: policyTimelineSummary(view),
	}
	if view.ActionRequest != nil {
		capability := view.ActionRequest.Capability
		event.Capability = &capability
		event.ObservationSequence = view.ActionRequest.ObservationSeq
		epoch := view.ActionRequest.ExpectedEpoch
		event.Epoch = &epoch
		if view.ActionRequest.PlanStep != nil {
			event.PlanID = view.ActionRequest.PlanStep.PlanID
			event.PlanRevision = view.ActionRequest.PlanStep.PlanRevision
			event.PlanStepID = view.ActionRequest.PlanStep.StepID
		}
	}
	return event
}

func operationTimelineSummary(view controlplane.OperationView) *timeline.OperationSummary {
	summary := &timeline.OperationSummary{
		OperationID: view.OperationID, Status: string(view.Status), Terminal: view.Terminal,
		ExecutionConfirmed:    view.ExecutionConfirmed,
		ReconciliationPending: view.ReconciliationPending,
		DeliveryAttempts:      view.DeliveryAttempts, CancelRequested: view.CancelRequested,
	}
	if view.Run != nil {
		summary.ProgressSequence = view.Run.ProgressSeq
		summary.Progress = view.Run.Progress
	}
	if view.Outcome != nil {
		summary.OutcomeCode = string(view.Outcome.Status)
	}
	return summary
}

func policyTimelineSummary(view controlplane.OperationView) *timeline.PolicySummary {
	decision := view.PolicyDecision
	if decision == nil {
		return nil
	}
	effectCount := uint32(0)
	if view.BoundAction != nil {
		effectCount = uint32(len(view.BoundAction.Effects))
	}
	return &timeline.PolicySummary{
		Disposition: string(decision.Result), ReasonCode: decision.ReasonCode,
		HumanSummary:   decision.HumanSummary,
		MatchedRuleIDs: append([]string(nil), decision.MatchedRuleIDs...),
		ConfirmationPending: decision.Result == policy.RequireConfirmation &&
			view.Status == controlplane.OperationAwaitingConfirmation,
		EffectCount: effectCount,
	}
}

func memoryContextDigest(record MemoryRecord) string {
	// Include a private provenance identifier so a public digest is not a
	// dictionary oracle for short, low-entropy memory text.
	payload, _ := json.Marshal(struct {
		MemoryID string `json:"memory_id"`
		Session  string `json:"session_id"`
		Actor    string `json:"actor_id"`
		Domain   string `json:"domain"`
		Source   string `json:"source"`
		SourceID string `json:"source_id"`
		Content  string `json:"content"`
	}{
		MemoryID: record.MemoryID,
		Session:  record.Namespace.SessionID,
		Actor:    record.Namespace.ActorID,
		Domain:   string(record.Namespace.Domain),
		Source:   string(record.Provenance.Source),
		SourceID: record.Provenance.SourceID,
		Content:  record.Content,
	})
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func uint64Pointer(value uint64) *uint64 {
	result := value
	return &result
}

func actionCapabilityPointer(ref host.CapabilityRef) *host.CapabilityRef {
	result := ref
	return &result
}
