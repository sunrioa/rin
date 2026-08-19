package cognition

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/taskstate"
)

type recordingAssemblyMemory struct {
	MemoryProvider
	query MemoryQuery
}

func (provider *recordingAssemblyMemory) Retrieve(
	ctx context.Context,
	query MemoryQuery,
) ([]MemoryMatch, error) {
	provider.query = query
	return nil, nil
}

type recordingAssemblySkills struct {
	SkillProvider
	query SkillQuery
}

func (provider *recordingAssemblySkills) ListSkills(
	ctx context.Context,
	query SkillQuery,
) ([]SkillSummary, error) {
	provider.query = query
	return []SkillSummary{{SkillID: "skill.collect", Version: "v1"}}, nil
}

func TestAssembleOptionalModelContextBuildsStableProviderQueries(t *testing.T) {
	memory := &recordingAssemblyMemory{}
	skills := &recordingAssemblySkills{}
	runtime := &AgentRuntime{
		memory: memory, skills: skills,
		memoryBudget: MemoryBudget{MaxRecords: 6, MaxCharacters: 900},
		now:          func() time.Time { return time.UnixMilli(100) },
	}
	task := TaskSession{
		TaskID: "task.context", SessionID: "session.context", ActorID: "actor.context",
		AdapterID:    "adapter.context",
		ControllerID: "controller.context", Goal: "Collect oak logs near the shelter.",
		Tags: []string{"task.collect"}, CurrentPlanStepID: "step.gather-wood",
		PlanningMode: taskstate.PlanningRequired,
	}
	capabilities := []CapabilitySummary{
		{Capability: host.CapabilityRef{ID: "rin.navigation.move-to"}},
		{Capability: host.CapabilityRef{ID: "rin.resource.collect"}},
	}

	assembled, err := runtime.assembleOptionalModelContext(
		context.Background(), task, host.ObservationEnvelope{}, capabilities,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(assembled.warnings) != 0 || len(assembled.skills) != 1 {
		t.Fatalf("assembled optional context = %#v", assembled)
	}
	if !reflect.DeepEqual(memory.query.Terms, []string{"collect", "oak", "logs", "near"}) ||
		memory.query.Budget != runtime.memoryBudget ||
		!memory.query.Semantic || memory.query.SemanticText != task.Goal {
		t.Fatalf("memory query = %#v", memory.query)
	}
	if !reflect.DeepEqual(skills.query.Tags,
		[]string{"task.collect", "step.gather-wood"}) ||
		skills.query.Adapter != task.AdapterID ||
		!reflect.DeepEqual(skills.query.AvailableCapabilities,
			[]string{"rin.navigation.move-to", "rin.resource.collect"}) ||
		skills.query.Limit != 64 {
		t.Fatalf("skill query = %#v", skills.query)
	}
}
