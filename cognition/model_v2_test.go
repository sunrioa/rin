package cognition_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/provider"
)

func TestStructuredDecisionProviderReturnsGroundedAction(t *testing.T) {
	generation := &recordingGenerationProvider{responses: []provider.CompletionResponse{{
		Content: `{
          "kind":"action",
          "capability_id":"rin.navigation.move-to",
          "capability_version":"2.0.0",
          "arguments_json":"{\"distance\":2}",
          "target_handles":["target.0"],
          "inspect_capabilities":[],
          "inspect_skills":[],
          "summary":"Move closer to the player.",
          "memory_candidates":[{
            "content":"The player may want company.",
            "tags":["player.nearby"],
            "subject_handles":["target.0"],
            "confidence":0.6,
            "importance":0.4,
            "ttl_steps":20
          }]
        }`,
		Model: "test-model", Usage: provider.Usage{TotalTokens: 42},
	}}}
	input := modelV2Input(t)
	input.Task.Goal = "Follow the player. UNTRUSTED_CANARY_DO_NOT_EXECUTE"
	decision, err := (cognition.StructuredDecisionProvider{
		GenerationProvider: generation,
	}).Decide(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != cognition.ModelDecisionAction ||
		decision.Capability != input.Capabilities[0].Capability ||
		string(decision.Arguments) != `{"distance":2}` ||
		decision.ProviderModel != "test-model" || decision.Usage.TotalTokens != 42 {
		t.Fatalf("unexpected grounded decision: %+v", decision)
	}
	if len(decision.MemoryCandidates) != 1 || decision.MemoryCandidates[0].Confidence != 0.6 {
		t.Fatalf("memory candidates were lost: %+v", decision.MemoryCandidates)
	}
	refs, err := cognition.ResolveModelTargetHandles(input.Observation, decision.TargetHandles)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0] != input.Observation.Resources[0].Ref {
		t.Fatalf("target handle did not resolve to the observed HostRef: %+v", refs)
	}

	request := generation.lastRequest(t)
	if request.Schema == nil || !request.Schema.Strict || request.Schema.Name != "rin_v2_model_decision" {
		t.Fatalf("strict response schema was not requested: %+v", request.Schema)
	}
	if !strings.Contains(request.Messages[0].Content, "untrusted_context") {
		t.Fatal("system prompt does not separate untrusted game data")
	}
	var packet map[string]json.RawMessage
	if err := json.Unmarshal([]byte(request.Messages[1].Content), &packet); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(packet["contract"]), "UNTRUSTED_CANARY") ||
		!strings.Contains(string(packet["untrusted_context"]), "UNTRUSTED_CANARY") {
		t.Fatalf("narrative text crossed the trusted contract boundary: %s", request.Messages[1].Content)
	}
}

func TestStructuredDecisionProviderValidatesBeforeFirstRequest(t *testing.T) {
	generation := &recordingGenerationProvider{}
	for _, configured := range []cognition.StructuredDecisionProvider{
		{},
		{GenerationProvider: generation, MaxContextCharacters: 7_999},
		{GenerationProvider: generation, MaxOutputTokens: 8_193},
		{GenerationProvider: generation, Temperature: 2.1},
	} {
		if err := configured.Validate(); err == nil {
			t.Fatalf("invalid provider was accepted: %#v", configured)
		}
		if configured.Health(context.Background()).Available {
			t.Fatal("invalid provider reported healthy")
		}
	}
	valid := cognition.StructuredDecisionProvider{GenerationProvider: generation}
	if err := valid.Validate(); err != nil || !valid.Health(context.Background()).Available {
		t.Fatalf("valid provider failed startup validation: %v", err)
	}
}

func TestStructuredDecisionProviderRejectsInventedTargetsAndCapabilities(t *testing.T) {
	for _, test := range []struct {
		name     string
		response string
	}{
		{
			name: "invented target",
			response: modelActionResponse(
				"rin.navigation.move-to", "2.0.0", `{"distance":1}`, `["target.99"]`,
			),
		},
		{
			name: "invented capability",
			response: modelActionResponse(
				"rin.world.delete-all", "2.0.0", `{}`, `[]`,
			),
		},
		{
			name: "arguments exceed capability limit",
			response: modelActionResponse(
				"rin.navigation.move-to", "2.0.0", `{"padding":"`+strings.Repeat("x", 1_100)+`"}`, `[]`,
			),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			generation := &recordingGenerationProvider{responses: []provider.CompletionResponse{{Content: test.response}}}
			_, err := (cognition.StructuredDecisionProvider{
				GenerationProvider: generation,
			}).Decide(context.Background(), modelV2Input(t))
			if err == nil {
				t.Fatal("unsafe model output was accepted")
			}
		})
	}
}

func TestStructuredDecisionProviderAllowsOneProgressiveInspection(t *testing.T) {
	response := `{
      "kind":"inspect",
      "capability_id":"",
      "capability_version":"",
      "arguments_json":"",
      "target_handles":[],
      "inspect_capabilities":[{"id":"rin.navigation.move-to","version":"2.0.0"}],
      "inspect_skills":[{"skill_id":"skill.follow","version":"v1"}],
      "summary":"Inspect movement constraints.",
      "memory_candidates":[]
    }`
	input := modelV2Input(t)
	generation := &recordingGenerationProvider{responses: []provider.CompletionResponse{{Content: response}}}
	decision, err := (cognition.StructuredDecisionProvider{
		GenerationProvider: generation,
	}).Decide(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != cognition.ModelDecisionInspect ||
		len(decision.InspectCapabilities) != 1 || len(decision.InspectSkills) != 1 {
		t.Fatalf("unexpected inspect decision: %+v", decision)
	}

	input.InspectionRound = 1
	generation = &recordingGenerationProvider{responses: []provider.CompletionResponse{{Content: response}}}
	if _, err := (cognition.StructuredDecisionProvider{
		GenerationProvider: generation,
	}).Decide(context.Background(), input); err == nil {
		t.Fatal("second inspection round was accepted")
	}
}

func TestStructuredDecisionProviderRejectsDuplicateInspectionSelectors(t *testing.T) {
	response := `{
      "kind":"inspect",
      "capability_id":"",
      "capability_version":"",
      "arguments_json":"",
      "target_handles":[],
      "inspect_capabilities":[
        {"id":"rin.navigation.move-to","version":"2.0.0"},
        {"id":"rin.navigation.move-to","version":"2.0.0"}
      ],
      "inspect_skills":[],
      "summary":"Inspect twice.",
      "memory_candidates":[]
    }`
	generation := &recordingGenerationProvider{responses: []provider.CompletionResponse{{Content: response}}}
	if _, err := (cognition.StructuredDecisionProvider{
		GenerationProvider: generation,
	}).Decide(context.Background(), modelV2Input(t)); err == nil {
		t.Fatal("duplicate inspection selector was accepted")
	}
}

func TestStructuredDecisionProviderRejectsMalformedStructuredOutput(t *testing.T) {
	responses := []string{
		`{"kind":"wait","kind":"action","capability_id":"","capability_version":"","arguments_json":"","target_handles":[],"inspect_capabilities":[],"inspect_skills":[],"summary":"Wait.","memory_candidates":[]}`,
		`{"kind":"wait","capability_id":"","capability_version":"","arguments_json":"","target_handles":[],"inspect_capabilities":[],"inspect_skills":[],"summary":"Wait."}`,
		`{"kind":"wait","capability_id":"","capability_version":"","arguments_json":"","target_handles":[],"inspect_capabilities":[],"inspect_skills":[],"summary":"Wait.","memory_candidates":[],"extra":true}`,
	}
	for _, response := range responses {
		generation := &recordingGenerationProvider{responses: []provider.CompletionResponse{{Content: response}}}
		if _, err := (cognition.StructuredDecisionProvider{
			GenerationProvider: generation,
		}).Decide(context.Background(), modelV2Input(t)); err == nil {
			t.Fatalf("malformed response was accepted: %s", response)
		}
	}
}

func TestStructuredDecisionProviderBoundsContextBeforeNetworkCall(t *testing.T) {
	generation := &recordingGenerationProvider{}
	input := modelV2Input(t)
	input.Observation.Payload = json.RawMessage(`{"text":"` + strings.Repeat("x", 9_000) + `"}`)
	_, err := (cognition.StructuredDecisionProvider{
		GenerationProvider: generation, MaxContextCharacters: 8_000,
	}).Decide(context.Background(), input)
	if !errors.Is(err, cognition.ErrProviderCapacity) {
		t.Fatalf("expected context capacity error, got %v", err)
	}
	if generation.calls != 0 {
		t.Fatal("oversized context reached the generation provider")
	}
}

func TestStructuredDecisionProviderHonorsCancellation(t *testing.T) {
	generation := &recordingGenerationProvider{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (cognition.StructuredDecisionProvider{
		GenerationProvider: generation,
	}).Decide(ctx, modelV2Input(t))
	if !errors.Is(err, context.Canceled) || generation.calls != 0 {
		t.Fatalf("cancellation was not honored before provider work: err=%v calls=%d", err, generation.calls)
	}
}

type recordingGenerationProvider struct {
	responses []provider.CompletionResponse
	err       error
	requests  []provider.CompletionRequest
	calls     int
}

func (generation *recordingGenerationProvider) Complete(
	ctx context.Context,
	request provider.CompletionRequest,
) (provider.CompletionResponse, error) {
	generation.calls++
	generation.requests = append(generation.requests, request)
	if err := ctx.Err(); err != nil {
		return provider.CompletionResponse{}, err
	}
	if generation.err != nil {
		return provider.CompletionResponse{}, generation.err
	}
	if len(generation.responses) == 0 {
		return provider.CompletionResponse{}, errors.New("no scripted response")
	}
	response := generation.responses[0]
	generation.responses = generation.responses[1:]
	return response, nil
}

func (generation *recordingGenerationProvider) lastRequest(t *testing.T) provider.CompletionRequest {
	t.Helper()
	if len(generation.requests) == 0 {
		t.Fatal("generation provider was not called")
	}
	return generation.requests[len(generation.requests)-1]
}

func modelV2Input(t *testing.T) cognition.ModelInput {
	t.Helper()
	epoch := host.Epoch{
		SessionID: "session.test", WorldID: "world.test", Host: 1, World: 1, Timeline: 1,
	}
	playerRef := host.HostRef{
		Namespace: "test.world", Type: "player", Key: "player-one", Epoch: epoch,
	}
	observation := host.ObservationEnvelope{
		ObservationID: "observation.1", HostID: "host.test", WorldID: "world.test",
		ActorID: "actor.mira", Epoch: epoch, Sequence: 1,
		ObservedAt: host.Timepoint{Clock: host.ClockStep, Value: 10},
		Schema: host.SchemaRef{
			ID: "rin.observation.actor", Version: "2.0.0", SHA256: strings.Repeat("a", 64),
		},
		Payload: json.RawMessage(`{"activity":"idle"}`),
		Facts: []host.ObservationFact{{
			FactID: "fact.player-nearby", Kind: "player.nearby", Subject: &playerRef,
			Tags: []string{"player.nearby"}, Value: json.RawMessage(`true`),
		}},
		Resources: []host.ObservationResource{{
			Ref: playerRef, Kind: "player.status", Tags: []string{"player.nearby"},
			Ownership: host.OwnershipPlayer, Scope: "world.local", Quantity: 1,
			Unit: "player", Attributes: json.RawMessage(`{"distance":2}`),
		}},
	}
	skill, err := cognition.SealSkill(cognition.Skill{
		SkillSummary: cognition.SkillSummary{
			SkillID: "skill.follow", Version: "v1", Summary: "Follow safely.",
			Triggers: []string{"task.follow"}, Source: "builtin",
		},
		Instructions: "Inspect the target and use bounded movement.",
	})
	if err != nil {
		t.Fatal(err)
	}
	return cognition.ModelInput{
		Task: cognition.ModelTaskContext{
			TaskID: "task.follow", SessionID: "session.test", ActorID: "actor.mira",
			ControllerID: "controller.internal", Goal: "Follow the nearby player.",
			Tags: []string{"task.follow"},
		},
		Persona: cognition.PersonaProfile{
			PersonaID: "persona.mira", Version: "v1", Identity: "Mira is a careful companion.",
		},
		Observation: observation,
		Capabilities: []cognition.CapabilitySummary{{
			Capability:  host.CapabilityRef{ID: "rin.navigation.move-to", Version: "2.0.0"},
			Description: "Move toward an observed target.", Kind: host.CapabilityAtomic,
			Execution: host.ExecutionLongRunning, Cancellation: host.CancellationCooperative,
			RiskFloor: host.RiskLow, RequiredDurability: host.DurabilityAdvisory,
			MaxInputBytes: 1_024, MaxEffects: 2, SpecDigest: strings.Repeat("b", 64),
		}},
		Skills: []cognition.SkillSummary{skill.SkillSummary},
	}
}

func modelActionResponse(capabilityID, version, arguments, targets string) string {
	return `{
      "kind":"action",
      "capability_id":"` + capabilityID + `",
      "capability_version":"` + version + `",
      "arguments_json":` + string(mustMarshalString(arguments)) + `,
      "target_handles":` + targets + `,
      "inspect_capabilities":[],
      "inspect_skills":[],
      "summary":"Act.",
      "memory_candidates":[]
    }`
}

func mustMarshalString(value string) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}
