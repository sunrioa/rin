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
	createdV2, err := service.SaveSkill(context.Background(), SkillSaveInput{
		SkillID: "learned.collect", Version: "v2",
		Description:  "Collect observed resources with recovery.",
		Instructions: "Use current targets, verify the outcome, and recover from failure.",
		Adapters:     []string{"minecraft"}, Capabilities: []string{"resource.harvest"},
	})
	if err != nil || createdV2.Skill.Version != "v2" {
		t.Fatalf("created v2 = %#v, %v", createdV2, err)
	}
	listed, err := service.ListSkills(context.Background(), SkillListInput{Limit: 128})
	if err != nil || len(listed.Skills) != 3 {
		t.Fatalf("listed = %#v, %v", listed, err)
	}
	if _, err := service.SaveSkill(context.Background(), SkillSaveInput{
		SkillID: "builtin.wait", Version: "v1", Description: "Overwrite.",
		Instructions: "Overwrite.",
	}); err == nil {
		t.Fatal("built-in skill was overwritten")
	}
	imported, err := service.ImportSkill(context.Background(), SkillImportInput{Document: `---
name: imported.travel
description: Travel with verified steps.
metadata:
  rin:
    version: v1
    adapters: [minecraft]
---

Observe, move, and verify arrival.
`})
	if err != nil || imported.Skill.Source != "learned" {
		t.Fatalf("imported = %#v, %v", imported, err)
	}
	if _, err := service.RemoveSkill(context.Background(), SkillRemoveInput{
		SkillID: imported.Skill.SkillID, Version: imported.Skill.Version,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetSkill(context.Background(), SkillGetInput{
		SkillID: imported.Skill.SkillID, Version: imported.Skill.Version,
	}); err != cognition.ErrProviderNotFound {
		t.Fatalf("removed skill lookup = %v", err)
	}
	if _, err := service.RemoveSkill(context.Background(), SkillRemoveInput{
		SkillID: "builtin.wait", Version: "v1",
	}); err == nil {
		t.Fatal("built-in skill was removed")
	}
	if _, err := service.ImportSkill(context.Background(), SkillImportInput{Document: `---
name: builtin.wait
description: Conflicting replacement.
metadata:
  rin:
    version: v1
---

Replace the built-in behavior.
`}); err == nil {
		t.Fatal("conflicting import was accepted")
	}
	if _, err := learned.DescribeSkill(context.Background(), "builtin.wait", "v1"); err != cognition.ErrProviderNotFound {
		t.Fatalf("conflicting import was not rolled back: %v", err)
	}
}
