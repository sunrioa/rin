package experience_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sunrioa/rin/experience"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/provider"
)

func TestModelDraftGeneratorUsesPortableVerifiedEvidence(t *testing.T) {
	capability := host.CapabilityRef{ID: "resource.harvest", Version: "v1"}
	generation := &draftGenerationProvider{content: `{
  "description":"Collect reusable resources near (12, 64, -9)",
  "instructions":"Observe first. Never reuse sk-secret123456789. Harvest, verify inventory, and retry a nearby target."
}`}
	episode := experience.Episode{
		ContractVersion: experience.ContractVersion, EpisodeID: "episode.one",
		TaskID: "task.one", ControllerKind: experience.ControllerInternal,
		Goal: "Collect logs at 550e8400-e29b-41d4-a716-446655440000",
		Tags: []string{"collect"},
		Events: []experience.Event{{
			EventID: "event.one", Kind: "operation.succeeded",
			Evidence: experience.EvidenceRef{
				EventID: "event.one", Capability: &capability,
				OutcomeCode: "succeeded", ExecutionConfirmed: true,
			},
		}},
		VerifiedResult: &experience.VerifiedResult{Success: true, OutcomeCode: "succeeded"},
	}
	draft, err := (experience.ModelDraftGenerator{Provider: generation}).Generate(
		context.Background(),
		experience.DraftRequest{Episode: episode, SkillID: "learned.collect", Adapter: "minecraft"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(draft.Capabilities) != 1 || draft.Capabilities[0] != "resource.harvest" ||
		strings.Contains(draft.Description, "12, 64, -9") ||
		strings.Contains(draft.Instructions, "sk-secret") {
		t.Fatalf("draft = %#v", draft)
	}
	var input map[string]any
	if err := json.Unmarshal([]byte(generation.request.Messages[1].Content), &input); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(input)
	if strings.Contains(string(encoded), "550e8400") || strings.Contains(string(encoded), "operation.one") {
		t.Fatalf("portable input leaked local references: %s", encoded)
	}
}

func TestModelDraftGeneratorRejectsUnverifiedEpisode(t *testing.T) {
	_, err := (experience.ModelDraftGenerator{Provider: &draftGenerationProvider{}}).Generate(
		context.Background(),
		experience.DraftRequest{
			Episode: experience.Episode{ContractVersion: experience.ContractVersion},
			SkillID: "learned.collect", Adapter: "minecraft",
		},
	)
	if err == nil {
		t.Fatal("unverified episode generated a skill")
	}
}

type draftGenerationProvider struct {
	content string
	request provider.CompletionRequest
}

func (generation *draftGenerationProvider) Complete(
	_ context.Context,
	request provider.CompletionRequest,
) (provider.CompletionResponse, error) {
	generation.request = request
	return provider.CompletionResponse{
		Content: generation.content, Model: "test-model",
		Usage: provider.Usage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30},
	}, nil
}
