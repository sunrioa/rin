package mcpbridge

import (
	"context"
	"slices"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/skillapi"
)

func TestGatewayExposesSharedSkillCatalogByScope(t *testing.T) {
	service := controlplane.New(controlplane.Options{})
	defer service.Close()
	principal := host.Principal{
		ID: "player.one",
		GrantedScopes: []string{
			controlplane.ScopeActorRead,
			skillapi.ScopeSkillRead,
			skillapi.ScopeSkillWrite,
		},
	}
	controlClient, err := controlplane.NewClientService(service, principal)
	if err != nil {
		t.Fatal(err)
	}
	skills := &testSkillClient{skill: testSkill(t)}
	gateway, err := NewClientWithSkills(controlClient, skills, principal)
	if err != nil {
		t.Fatal(err)
	}
	session := connectGateway(t, gateway)
	listedTools, err := session.ListTools(testContext(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := toolNames(listedTools.Tools)
	for _, name := range []string{
		"list_skills", "get_skill", "save_experience_as_skill", "reload_skills",
	} {
		if !slices.Contains(names, name) {
			t.Fatalf("tool %q is absent from %#v", name, names)
		}
	}

	var listed ListSkillsOutput
	callTool(t, session, "list_skills", map[string]any{
		"adapter":                "minecraft",
		"available_capabilities": []string{"resource.harvest"},
	}, &listed)
	if len(listed.Skills) != 1 || listed.Skills[0].SkillID != "collect.logs" {
		t.Fatalf("list_skills = %#v", listed)
	}
	var loaded GetSkillOutput
	callTool(t, session, "get_skill", map[string]any{
		"skill_id": "collect.logs", "version": "v1",
	}, &loaded)
	if loaded.Skill.Digest != skills.skill.Digest {
		t.Fatalf("get_skill = %#v", loaded)
	}
	callTool(t, session, "save_experience_as_skill", map[string]any{
		"skill_id":     "collect.stone",
		"description":  "Collect nearby stone",
		"instructions": "Observe and use the published harvesting capability.",
	}, &loaded)
	if loaded.Skill.SkillID != "collect.stone" || skills.saved != 1 {
		t.Fatalf("save_experience_as_skill = %#v, saved = %d", loaded, skills.saved)
	}
	var reloaded ReloadSkillsOutput
	callTool(t, session, "reload_skills", map[string]any{}, &reloaded)
	if !reloaded.Reloaded || skills.reloaded != 1 {
		t.Fatalf("reload_skills = %#v, calls = %d", reloaded, skills.reloaded)
	}
}

func connectGateway(t *testing.T, gateway *Gateway) *mcp.ClientSession {
	t.Helper()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := gateway.Server().Connect(testContext(t), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcp.NewClient(
		&mcp.Implementation{Name: "rin-skill-test", Version: "1.0.0"}, nil,
	)
	session, err := client.Connect(testContext(t), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

type testSkillClient struct {
	skill    cognition.Skill
	saved    int
	reloaded int
}

func (client *testSkillClient) List(
	context.Context,
	skillapi.ListInput,
) (skillapi.ListOutput, error) {
	return skillapi.ListOutput{Skills: []cognition.SkillSummary{client.skill.SkillSummary}}, nil
}

func (client *testSkillClient) Get(
	context.Context,
	skillapi.GetInput,
) (skillapi.GetOutput, error) {
	return skillapi.GetOutput{Skill: client.skill}, nil
}

func (client *testSkillClient) Save(
	_ context.Context,
	input skillapi.SaveInput,
) (skillapi.GetOutput, error) {
	client.saved++
	version := input.Version
	if version == "" {
		version = "v1"
	}
	skill, err := cognition.SealSkill(cognition.Skill{
		SkillSummary: cognition.SkillSummary{
			SkillID: input.SkillID, Version: version, Summary: input.Description,
			Source: "learned", Triggers: input.Triggers, Adapters: input.Adapters,
			Capabilities: input.Capabilities,
		},
		Instructions: input.Instructions,
	})
	return skillapi.GetOutput{Skill: skill}, err
}

func (client *testSkillClient) Reload(context.Context) (skillapi.ReloadOutput, error) {
	client.reloaded++
	return skillapi.ReloadOutput{Reloaded: true}, nil
}

func testSkill(t *testing.T) cognition.Skill {
	t.Helper()
	skill, err := cognition.SealSkill(cognition.Skill{
		SkillSummary: cognition.SkillSummary{
			SkillID: "collect.logs", Version: "v1", Summary: "Collect nearby logs",
			Source: "installed", Adapters: []string{"minecraft"},
			Capabilities: []string{"resource.harvest"},
		},
		Instructions: "Observe the world and use current published capabilities.",
	})
	if err != nil {
		t.Fatal(err)
	}
	return skill
}
