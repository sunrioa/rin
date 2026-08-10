package cognition_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/sunrioa/rin/cognition"
)

func TestLocalSkillProviderListsSummariesAndExpandsOneSkill(t *testing.T) {
	provider, err := cognition.NewLocalSkillProvider([]cognition.Skill{
		{
			SkillSummary: cognition.SkillSummary{
				SkillID: "skill.collect", Version: "v1", Summary: "Collect safely.",
				Triggers: []string{"task.collect"}, Source: "builtin",
			},
			Instructions: "Inspect nearby resources before choosing a capability.",
		},
		{
			SkillSummary: cognition.SkillSummary{
				SkillID: "skill.general", Version: "v1", Summary: "General guidance.", Source: "builtin",
			},
			Instructions: "Prefer reversible actions.",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	summaries, err := provider.ListSkills(context.Background(), cognition.SkillQuery{
		Tags: []string{"task.collect"}, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 2 || summaries[0].SkillID != "skill.collect" ||
		summaries[1].SkillID != "skill.general" {
		t.Fatalf("unexpected progressive skill list: %+v", summaries)
	}
	if summaries[0].Digest == "" {
		t.Fatal("sealed skill summary has no digest")
	}

	described, err := provider.DescribeSkill(
		context.Background(), summaries[0].SkillID, summaries[0].Version,
	)
	if err != nil {
		t.Fatal(err)
	}
	if described.Instructions == "" || described.Digest != summaries[0].Digest {
		t.Fatalf("unexpected described skill: %+v", described)
	}
}

func TestLocalSkillProviderFiltersUnrelatedTriggeredSkills(t *testing.T) {
	provider, err := cognition.NewLocalSkillProvider([]cognition.Skill{
		{
			SkillSummary: cognition.SkillSummary{
				SkillID: "skill.build", Version: "v1", Summary: "Build.",
				Triggers: []string{"task.build"}, Source: "builtin",
			},
			Instructions: "Build within the selected region.",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	summaries, err := provider.ListSkills(context.Background(), cognition.SkillQuery{
		Tags: []string{"task.collect"}, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 0 {
		t.Fatalf("unrelated skill was exposed: %+v", summaries)
	}
}

func TestSkillProviderSealsCopiesAndRejectsTampering(t *testing.T) {
	input := cognition.Skill{
		SkillSummary: cognition.SkillSummary{
			SkillID: "skill.collect", Version: "v1", Summary: "Collect safely.",
			Triggers: []string{"task.collect"}, Source: "builtin",
		},
		Instructions: "Inspect first.",
	}
	sealed, err := cognition.SealSkill(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Triggers[0] = "mutated-input"
	provider, err := cognition.NewLocalSkillProvider([]cognition.Skill{sealed})
	if err != nil {
		t.Fatal(err)
	}
	described, err := provider.DescribeSkill(context.Background(), "skill.collect", "v1")
	if err != nil {
		t.Fatal(err)
	}
	described.Triggers[0] = "mutated-output"
	reloaded, err := provider.DescribeSkill(context.Background(), "skill.collect", "v1")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reloaded.Triggers, []string{"task.collect"}) {
		t.Fatalf("provider state was mutated: %+v", reloaded)
	}

	sealed.Instructions = "Tampered instructions."
	if _, err := cognition.NewLocalSkillProvider([]cognition.Skill{sealed}); err == nil {
		t.Fatal("expected digest mismatch for tampered skill")
	}
}

func TestSkillProviderHonorsCancellationAndMissingSkills(t *testing.T) {
	provider, err := cognition.NewLocalSkillProvider(nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.DescribeSkill(context.Background(), "skill.missing", "v1")
	if !errors.Is(err, cognition.ErrProviderNotFound) {
		t.Fatalf("expected missing skill, got %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = provider.ListSkills(ctx, cognition.SkillQuery{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled list, got %v", err)
	}
}
