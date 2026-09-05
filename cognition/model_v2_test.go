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
	"github.com/sunrioa/rin/taskstate"
	"github.com/sunrioa/rin/timeline"
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
          }],
          "plan":null
        }`,
		Model: "test-model", Usage: provider.Usage{TotalTokens: 42},
	}}}
	input := modelV2Input(t)
	input.Task.ParentOperationID = "operation.parent.one"
	input.Task.Goal = "Follow the player. UNTRUSTED_CANARY_DO_NOT_EXECUTE"
	input.Task.Completion = cognition.TaskCompletionPolicy{Mode: cognition.CompletionEvidence, Conditions: []taskstate.PlanCondition{completionFact("goal.arrived")}}
	input.Task.Signals = []cognition.TaskSignal{{SignalContextRef: timeline.SignalContextRef{SignalID: "signal.context", Kind: "game.notice", Cursor: 1}, Summary: "UNTRUSTED_SIGNAL_CANARY", Epoch: input.Observation.Epoch, ObservationSequence: input.Observation.Sequence, ExpiresAtUnixMillis: 60001}}
	decision, err := (cognition.StructuredDecisionProvider{
		GenerationProvider: generation,
	}).Decide(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != cognition.ModelDecisionAction ||
		decision.Capability != input.Capabilities[0].Capability ||
		string(decision.Arguments) != `{"distance":2}` ||
		decision.ProviderModel != "test-model" || decision.Usage.TotalTokens != 42 ||
		!strings.HasPrefix(decision.ProviderRequestDigest, "sha256:") ||
		!strings.HasPrefix(decision.StablePrefixDigest, "sha256:") {
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
	if len(request.Messages) != 3 {
		t.Fatalf("model message count = %d, want 3", len(request.Messages))
	}
	if request.Schema == nil || !request.Schema.Strict || request.Schema.Name != "rin_v2_model_decision" {
		t.Fatalf("strict response schema was not requested: %+v", request.Schema)
	}
	if !strings.Contains(request.Messages[0].Content, "untrusted_context") {
		t.Fatal("system prompt does not separate untrusted game data")
	}
	if strings.Contains(request.Messages[1].Content, "UNTRUSTED_CANARY") {
		t.Fatal("dynamic goal leaked into the stable prefix")
	}
	var packet map[string]json.RawMessage
	if err := json.Unmarshal([]byte(request.Messages[2].Content), &packet); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(packet["contract"]), "UNTRUSTED_CANARY") ||
		!strings.Contains(string(packet["untrusted_context"]), "UNTRUSTED_CANARY") {
		t.Fatalf("narrative text crossed the trusted contract boundary: %s", request.Messages[2].Content)
	}
	var contract map[string]any
	if err := json.Unmarshal(packet["contract"], &contract); err != nil {
		t.Fatal(err)
	}
	if contract["parent_operation_id"] != input.Task.ParentOperationID {
		t.Fatalf("trusted macro parent is missing from model contract: %+v", contract)
	}
	if strings.Contains(string(packet["contract"]), "UNTRUSTED_SIGNAL_CANARY") || !strings.Contains(string(packet["untrusted_context"]), "UNTRUSTED_SIGNAL_CANARY") || !strings.Contains(string(packet["untrusted_context"]), "goal.arrived") {
		t.Fatalf("signal or completion policy missing from bounded narrative context: %s", packet["untrusted_context"])
	}
}

func TestStructuredDecisionProviderRecordsPreparedWireContext(t *testing.T) {
	response := provider.CompletionResponse{
		Content: modelActionResponse("rin.navigation.move-to", "2.0.0", `{}`, `["target.0"]`),
	}
	plain := &recordingGenerationProvider{responses: []provider.CompletionResponse{response}}
	plainDecision, err := (cognition.StructuredDecisionProvider{
		GenerationProvider: plain,
	}).Decide(context.Background(), modelV2Input(t))
	if err != nil {
		t.Fatal(err)
	}
	preparedBase := &recordingGenerationProvider{responses: []provider.CompletionResponse{response}}
	prepared := &preparingGenerationProvider{recordingGenerationProvider: preparedBase}
	preparedDecision, err := (cognition.StructuredDecisionProvider{
		GenerationProvider: prepared,
	}).Decide(context.Background(), modelV2Input(t))
	if err != nil {
		t.Fatal(err)
	}
	request := preparedBase.lastRequest(t)
	if !strings.Contains(request.Messages[0].Content, "prepared-provider-contract") {
		t.Fatalf("provider received unprepared messages: %#v", request.Messages)
	}
	if preparedDecision.ProviderRequestDigest == plainDecision.ProviderRequestDigest ||
		preparedDecision.StablePrefixDigest == plainDecision.StablePrefixDigest {
		t.Fatal("recorded digests ignored provider request preparation")
	}
}

func TestStructuredDecisionProviderRejectsInvalidPreparedRequest(t *testing.T) {
	base := &recordingGenerationProvider{responses: []provider.CompletionResponse{{
		Content: modelActionResponse("rin.navigation.move-to", "2.0.0", `{}`, `["target.0"]`),
	}}}
	generation := &preparingGenerationProvider{
		recordingGenerationProvider: base,
		dropMessages:                true,
	}
	_, err := (cognition.StructuredDecisionProvider{
		GenerationProvider: generation,
	}).Decide(context.Background(), modelV2Input(t))
	if err == nil || !strings.Contains(err.Error(), "invalid prepared request") {
		t.Fatalf("invalid prepared request error = %v", err)
	}
	if base.calls != 0 {
		t.Fatal("invalid prepared request reached the provider")
	}
}

func TestStructuredDecisionProviderKeepsStaticPrefixStable(t *testing.T) {
	response := provider.CompletionResponse{
		Content: modelActionResponse("rin.navigation.move-to", "2.0.0", `{}`, `["target.0"]`),
		Model:   "test-model",
	}
	generation := &recordingGenerationProvider{responses: []provider.CompletionResponse{
		response, response, response, response, response,
	}}
	decisionProvider := cognition.StructuredDecisionProvider{GenerationProvider: generation}
	addStaticValues := func(input *cognition.ModelInput, reverse bool) {
		capability := cognition.CapabilitySummary{
			Capability:  host.CapabilityRef{ID: "rin.world.wait", Version: "1.0.0"},
			Description: "Wait for the world to change.", Kind: host.CapabilityAtomic,
			Execution: host.ExecutionImmediate, Cancellation: host.CancellationUnsupported,
			RiskFloor: host.RiskLow, RequiredDurability: host.DurabilityAdvisory,
			MaxInputBytes: 128, MaxEffects: 1, SpecDigest: strings.Repeat("c", 64),
		}
		skill, err := cognition.SealSkill(cognition.Skill{
			SkillSummary: cognition.SkillSummary{
				SkillID: "skill.wait", Version: "v1", Summary: "Wait deliberately.", Source: "builtin",
			},
			Instructions: "Wait only when observation cannot support progress.",
		})
		if err != nil {
			t.Fatal(err)
		}
		if reverse {
			input.Capabilities = append([]cognition.CapabilitySummary{capability}, input.Capabilities...)
			input.Skills = append([]cognition.SkillSummary{skill.SkillSummary}, input.Skills...)
		} else {
			input.Capabilities = append(input.Capabilities, capability)
			input.Skills = append(input.Skills, skill.SkillSummary)
		}
	}

	first := modelV2Input(t)
	addStaticValues(&first, false)
	firstDecision, err := decisionProvider.Decide(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	second := modelV2Input(t)
	addStaticValues(&second, true)
	second.Task.Goal = "Move near the player without blocking them."
	second.Observation.ObservationID = "observation.2"
	second.Observation.Sequence = 2
	secondDecision, err := decisionProvider.Decide(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	firstRequest, secondRequest := generation.requests[0], generation.requests[1]
	if firstRequest.Messages[0] != secondRequest.Messages[0] ||
		firstRequest.Messages[1] != secondRequest.Messages[1] ||
		firstRequest.Messages[2] == secondRequest.Messages[2] ||
		firstDecision.StablePrefixDigest != secondDecision.StablePrefixDigest ||
		firstDecision.ProviderRequestDigest == secondDecision.ProviderRequestDigest {
		t.Fatalf("stable/dynamic split changed unexpectedly:\nfirst=%#v\nsecond=%#v", firstRequest.Messages, secondRequest.Messages)
	}

	changedPersona := modelV2Input(t)
	addStaticValues(&changedPersona, false)
	changedPersona.Persona.Identity = "Mira now speaks with a calmer voice."
	personaDecision, err := decisionProvider.Decide(context.Background(), changedPersona)
	if err != nil {
		t.Fatal(err)
	}
	changedCapability := modelV2Input(t)
	addStaticValues(&changedCapability, false)
	changedCapability.Capabilities[0].SpecDigest = strings.Repeat("d", 64)
	capabilityDecision, err := decisionProvider.Decide(context.Background(), changedCapability)
	if err != nil {
		t.Fatal(err)
	}
	changedSkill := modelV2Input(t)
	addStaticValues(&changedSkill, false)
	changedSkill.Skills[0].Digest = strings.Repeat("e", 64)
	skillDecision, err := decisionProvider.Decide(context.Background(), changedSkill)
	if err != nil {
		t.Fatal(err)
	}
	if personaDecision.StablePrefixDigest == firstDecision.StablePrefixDigest ||
		capabilityDecision.StablePrefixDigest == firstDecision.StablePrefixDigest ||
		skillDecision.StablePrefixDigest == firstDecision.StablePrefixDigest {
		t.Fatal("persona, capability schema, or skill change reused the old stable prefix digest")
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
      "memory_candidates":[],
      "plan":null
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
      "memory_candidates":[],
      "plan":null
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

func TestStructuredDecisionProviderAcceptsEmptyJSONObjectForNonAction(t *testing.T) {
	response := `{"kind":"complete","capability_id":"","capability_version":"","arguments_json":"{}","target_handles":[],"inspect_capabilities":[],"inspect_skills":[],"summary":"Done.","memory_candidates":[],"plan":null}`
	generation := &recordingGenerationProvider{responses: []provider.CompletionResponse{{Content: response}}}
	decision, err := (cognition.StructuredDecisionProvider{
		GenerationProvider: generation,
	}).Decide(context.Background(), modelV2Input(t))
	if err != nil {
		t.Fatal(err)
	}
	if decision.Kind != cognition.ModelDecisionComplete || len(decision.Arguments) != 0 {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestStructuredDecisionProviderRejectsNegativeCacheUsage(t *testing.T) {
	negative := -1
	generation := &recordingGenerationProvider{responses: []provider.CompletionResponse{{
		Content: modelActionResponse("rin.navigation.move-to", "2.0.0", `{}`, `[]`),
		Usage:   provider.Usage{PromptCacheHitTokens: &negative},
	}}}
	if _, err := (cognition.StructuredDecisionProvider{
		GenerationProvider: generation,
	}).Decide(context.Background(), modelV2Input(t)); err == nil {
		t.Fatal("negative cache usage was accepted")
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

type preparingGenerationProvider struct {
	*recordingGenerationProvider
	dropMessages bool
}

func (generation *preparingGenerationProvider) PrepareCompletionRequest(
	request provider.CompletionRequest,
) (provider.CompletionRequest, error) {
	if generation.dropMessages {
		request.Messages = nil
		return request, nil
	}
	request.Messages = append([]provider.Message(nil), request.Messages...)
	request.Messages[0].Content += "\nprepared-provider-contract"
	return request, nil
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
      "memory_candidates":[],
      "plan":null
    }`
}

func mustMarshalString(value string) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}
