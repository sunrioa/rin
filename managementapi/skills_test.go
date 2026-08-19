package managementapi

import (
	"context"
	"testing"

	"github.com/sunrioa/rin/cognition"
)

func TestServiceManagesLearnedSkillsIndependentlyOfControlScopes(t *testing.T) {
	builtin, err := cognition.NewLocalSkillProvider([]cognition.Skill{{
		SkillSummary: cognition.SkillSummary{
			SkillID: "builtin.wait", Version: "v1", Summary: "Wait safely.",
			Source: "builtin",
		},
		Instructions: "Wait for a current observation.",
	}})
	if err != nil {
		t.Fatal(err)
	}
	learned, err := cognition.OpenDirectorySkillProvider(t.TempDir(), "learned", true)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := cognition.NewSkillCatalog(builtin, learned)
	if err != nil {
		t.Fatal(err)
	}
	personas, err := cognition.RestoreLocalPersonaProvider(cognition.DefaultPersonaSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	memory, err := cognition.NewLocalMemoryProvider(cognition.LocalMemoryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(personas, memory)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ConfigureSkills(catalog, learned); err != nil {
		t.Fatal(err)
	}
	created, err := service.SaveSkill(context.Background(), SkillSaveInput{
		SkillID: "learned.collect", Description: "Collect observed resources.",
		Instructions: "Use only current targets and verify the outcome.",
		Adapters:     []string{"minecraft"}, Capabilities: []string{"resource.harvest"},
	})
	if err != nil || created.Skill.Source != "learned" {
		t.Fatalf("created = %#v, %v", created, err)
	}
	listed, err := service.ListSkills(context.Background(), SkillListInput{Limit: 128})
	if err != nil || len(listed.Skills) != 2 {
		t.Fatalf("listed = %#v, %v", listed, err)
	}
	if _, err := service.SaveSkill(context.Background(), SkillSaveInput{
		SkillID: "builtin.wait", Version: "v1", Description: "Overwrite.",
		Instructions: "Overwrite.",
	}); err == nil {
		t.Fatal("built-in skill was overwritten")
	}
}
