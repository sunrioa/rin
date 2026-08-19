package cognition

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/taskstate"
)

// assembledModelContext contains optional provider data collected for one
// model turn. Provider failures are represented as warnings so a missing
// memory or skill provider does not change the task's decision path.
type assembledModelContext struct {
	memories []MemoryMatch
	skills   []SkillSummary
	warnings []TaskEvent
}

func (runtime *AgentRuntime) assembleOptionalModelContext(
	ctx context.Context,
	task TaskSession,
	observation host.ObservationEnvelope,
	capabilities []CapabilitySummary,
) (assembledModelContext, error) {
	assembled := assembledModelContext{warnings: make([]TaskEvent, 0, 2)}
	if runtime.memory != nil {
		memoryQuery := MemoryQuery{
			SessionID: task.SessionID, ActorID: task.ActorID, ControllerID: task.ControllerID,
			Terms: memoryTermsFromGoal(task.Goal), Now: observation.ObservedAt,
			Budget:       runtime.memoryBudget,
			Semantic:     task.PlanningMode == taskstate.PlanningRequired || task.PlanID != "",
			SemanticText: task.Goal,
		}
		var err error
		if traced, ok := runtime.memory.(TracedMemoryProvider); ok {
			var trace MemoryRetrievalTrace
			assembled.memories, trace, err = traced.RetrieveWithTrace(ctx, memoryQuery)
			if trace.SemanticUsed {
				assembled.warnings = append(assembled.warnings,
					runtime.memoryRetrievalEvent(task, trace, runtime.now().UnixMilli()),
				)
			}
		} else {
			assembled.memories, err = runtime.memory.Retrieve(ctx, memoryQuery)
		}
		if err != nil {
			if ctx.Err() != nil {
				return assembledModelContext{}, ctx.Err()
			}
			assembled.memories = nil
			assembled.warnings = append(assembled.warnings,
				runtime.warningEvent(task, "memory.degraded"),
			)
		}
	}
	if runtime.skills != nil {
		availableCapabilities := make([]string, 0, len(capabilities))
		for _, summary := range capabilities {
			availableCapabilities = append(availableCapabilities, summary.Capability.ID)
		}
		skillTags := append([]string(nil), task.Tags...)
		if task.CurrentPlanStepID != "" {
			found := false
			for _, tag := range skillTags {
				if tag == task.CurrentPlanStepID {
					found = true
					break
				}
			}
			if !found {
				skillTags = append(skillTags, task.CurrentPlanStepID)
			}
		}
		var err error
		assembled.skills, err = runtime.skills.ListSkills(ctx, SkillQuery{
			Tags: skillTags, AvailableCapabilities: availableCapabilities, Limit: 64,
		})
		if err != nil {
			if ctx.Err() != nil {
				return assembledModelContext{}, ctx.Err()
			}
			assembled.skills = nil
			assembled.warnings = append(assembled.warnings,
				runtime.warningEvent(task, "skills.degraded"),
			)
		}
	}
	return assembled, nil
}

func (runtime *AgentRuntime) memoryRetrievalEvent(
	task TaskSession,
	trace MemoryRetrievalTrace,
	at int64,
) TaskEvent {
	code := "memory.semantic"
	if trace.QueryCacheHit {
		code = "memory.semantic-cache-hit"
	}
	if trace.DegradedCode != "" {
		code = trace.DegradedCode
	}
	return TaskEvent{
		Kind: "memory.retrieval", Step: task.Step, Code: code,
		AtUnixMillis: at,
		Summary:      fmt.Sprintf("Semantic memory lookup completed in %d ms.", trace.RemoteLatencyMillis),
	}
}

func memoryTermsFromGoal(goal string) []string {
	stop := map[string]struct{}{
		"about": {}, "after": {}, "before": {}, "from": {}, "into": {},
		"nearby": {}, "that": {}, "the": {}, "then": {}, "this": {},
		"with": {}, "without": {},
	}
	result := make([]string, 0, 4)
	seen := make(map[string]struct{}, 4)
	for _, field := range strings.Fields(goal) {
		field = strings.ToLower(strings.TrimFunc(field, func(value rune) bool {
			return unicode.IsPunct(value) || unicode.IsSpace(value)
		}))
		if field == "" {
			continue
		}
		field = cropRunes(field, 100)
		if _, skipped := stop[field]; skipped {
			continue
		}
		if _, duplicate := seen[field]; duplicate {
			continue
		}
		seen[field] = struct{}{}
		result = append(result, field)
		if len(result) == 4 {
			break
		}
	}
	return result
}
