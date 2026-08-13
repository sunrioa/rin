package cognition

import (
	"context"
	"errors"
	"testing"
)

func TestSkillCatalogMergesProvidersAndRejectsConflicts(t *testing.T) {
	first, err := NewLocalSkillProvider([]Skill{testCatalogSkill("skill.one", "builtin")})
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewLocalSkillProvider([]Skill{testCatalogSkill("skill.two", "installed")})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := NewSkillCatalog(first, second)
	if err != nil {
		t.Fatal(err)
	}
	result, err := catalog.ListSkills(context.Background(), SkillQuery{Limit: 8})
	if err != nil || len(result) != 2 || result[0].SkillID != "skill.one" ||
		result[1].SkillID != "skill.two" {
		t.Fatalf("catalog result = %#v, %v", result, err)
	}
	conflicting := testCatalogSkill("skill.one", "learned")
	conflicting.Instructions = "Different instructions."
	third, err := NewLocalSkillProvider([]Skill{conflicting})
	if err != nil {
		t.Fatal(err)
	}
	conflictCatalog, err := NewSkillCatalog(first, third)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conflictCatalog.ListSkills(
		context.Background(), SkillQuery{Limit: 8},
	); !errors.Is(err, ErrProviderConflict) {
		t.Fatalf("conflicting catalog error = %v", err)
	}
}

func testCatalogSkill(id, source string) Skill {
	return Skill{SkillSummary: SkillSummary{
		SkillID: id, Version: "v1", Summary: "A reusable skill.", Source: source,
	}, Instructions: "Observe, act, and verify the outcome."}
}
