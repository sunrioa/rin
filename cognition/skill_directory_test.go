package cognition

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDirectorySkillProviderLoadsFiltersAndSavesStandardDocuments(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "collect-resources")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	document := `---
name: collect-resources
description: Collect resources and verify each result.
metadata:
  rin:
    version: 1
    adapters: [minecraft]
    capabilities: [resource.harvest, world.observe]
    triggers: [task.collect]
---

# Collect resources

Observe, harvest, and verify the authoritative outcome.
`
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	provider, err := OpenDirectorySkillProvider(root, "installed", false)
	if err != nil {
		t.Fatal(err)
	}
	result, err := provider.ListSkills(context.Background(), SkillQuery{
		Adapter: "minecraft", AvailableCapabilities: []string{"world.observe", "resource.harvest"},
		Tags: []string{"task.collect"}, Limit: 8,
	})
	if err != nil || len(result) != 1 || result[0].Version != "v1" ||
		len(result[0].Capabilities) != 2 {
		t.Fatalf("directory skills = %#v, %v", result, err)
	}
	filtered, err := provider.ListSkills(context.Background(), SkillQuery{
		Adapter: "renpy", Tags: []string{"task.collect"}, Limit: 8,
	})
	if err != nil || len(filtered) != 0 {
		t.Fatalf("adapter filter = %#v, %v", filtered, err)
	}
	learned, err := OpenDirectorySkillProvider(filepath.Join(root, "learned"), "learned", true)
	if err != nil {
		t.Fatal(err)
	}
	skill, err := provider.DescribeSkill(context.Background(), "collect-resources", "v1")
	if err != nil {
		t.Fatal(err)
	}
	if err := learned.Save(context.Background(), skill); err != nil {
		t.Fatal(err)
	}
	saved, err := learned.DescribeSkill(context.Background(), "collect-resources", "v1")
	if err != nil || saved.Digest != skill.Digest || saved.Source != "learned" {
		t.Fatalf("saved skill = %#v, %v", saved, err)
	}
}

func TestDirectorySkillProviderRejectsMismatchedDirectory(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "wrong-name")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	document := "---\nname: actual-name\ndescription: A skill.\n---\n\nDo the work.\n"
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenDirectorySkillProvider(root, "installed", false); err == nil {
		t.Fatal("mismatched skill directory was accepted")
	}
}

func TestDirectorySkillProviderImportsAndRemovesOwnedDocument(t *testing.T) {
	root := t.TempDir()
	provider, err := OpenDirectorySkillProvider(root, "learned", true)
	if err != nil {
		t.Fatal(err)
	}
	document := []byte(`---
name: imported-survival
description: Prepare for a journey.
metadata:
  rin:
    version: v1
    adapters: [minecraft]
    capabilities: [recipe.lookup, crafting.craft]
---

Observe the inventory, prepare tools, and verify each outcome.
`)
	imported, err := provider.Import(context.Background(), document)
	if err != nil {
		t.Fatal(err)
	}
	if imported.SkillID != "imported-survival" || imported.Source != "learned" {
		t.Fatalf("imported skill = %#v", imported)
	}
	if err := provider.Remove(context.Background(), imported.SkillID, imported.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DescribeSkill(context.Background(), imported.SkillID, imported.Version); err != ErrProviderNotFound {
		t.Fatalf("removed skill lookup = %v", err)
	}
}

func TestDirectorySkillProviderRefusesRemovalWithUnmanagedFiles(t *testing.T) {
	root := t.TempDir()
	provider, err := OpenDirectorySkillProvider(root, "learned", true)
	if err != nil {
		t.Fatal(err)
	}
	skill := Skill{SkillSummary: SkillSummary{
		SkillID: "owned-skill", Version: "v1", Summary: "Owned skill.", Source: "learned",
	}, Instructions: "Perform the verified operation."}
	if err := provider.Save(context.Background(), skill); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "owned-skill", "notes.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := provider.Remove(context.Background(), "owned-skill", "v1"); err == nil {
		t.Fatal("skill directory with unmanaged content was removed")
	}
	if _, err := provider.DescribeSkill(context.Background(), "owned-skill", "v1"); err != nil {
		t.Fatalf("failed removal changed provider state: %v", err)
	}
}
