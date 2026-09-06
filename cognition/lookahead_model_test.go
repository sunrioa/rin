package cognition_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/provider"
)

func lookaheadModelInput(t *testing.T) cognition.LookaheadInput {
	t.Helper()
	input := modelV2Input(t)
	input.Observation.Facts = []host.ObservationFact{{FactID: "next.allowed", Kind: "world.condition", Value: json.RawMessage("false")}}
	targets, err := cognition.ResolveModelTargetHandles(input.Observation, []string{"target.0"})
	if err != nil {
		t.Fatal(err)
	}
	return cognition.LookaheadInput{Context: input, OperationID: "operation.current", Action: host.ActionRequest{
		RequestID: "request.current", IdempotencyKey: "request.current", TaskID: input.Task.TaskID,
		ControllerID: input.Task.ControllerID, ActorID: input.Task.ActorID,
		Capability: input.Capabilities[0].Capability, SpecDigest: input.Capabilities[0].SpecDigest,
		Arguments: json.RawMessage(`{"distance":2}`), Targets: targets, ExpectedEpoch: input.Observation.Epoch, ObservationSeq: input.Observation.Sequence,
	}}
}

func lookaheadModelOutput() map[string]any {
	return map[string]any{"kind": "action", "plan_step_id": "", "capability_id": "rin.navigation.move-to", "capability_version": "2.0.0",
		"arguments_json": `{"distance":3}`, "target_handles": []string{"target.0"},
		"preconditions": []map[string]string{{"fact_id": "next.allowed", "fact_value_json": "true"}},
		"summary":       "Continue after the Host confirms success and the expected condition."}
}

func TestLookaheadProviderUsesConditionalContractAndPreparedTokenReservation(t *testing.T) {
	output, _ := json.Marshal(lookaheadModelOutput())
	generation := &recordingGenerationProvider{responses: []provider.CompletionResponse{{Content: string(output), Model: "preview-model", Usage: provider.Usage{PromptTokens: 20, CompletionTokens: 7, TotalTokens: 27}}}}
	prepared := &preparingGenerationProvider{recordingGenerationProvider: generation}
	model := cognition.StructuredDecisionProvider{GenerationProvider: prepared, MaxOutputTokens: 4096}
	input := lookaheadModelInput(t)
	input.Context.Task.Goal = "UNTRUSTED_LOOKAHEAD_GOAL: predict the next appropriate movement."
	reserve, err := model.LookaheadTokenReservation(input)
	if err != nil || reserve < 2048 || generation.calls != 0 {
		t.Fatalf("reservation invoked the provider or lost the prompt allowance: %d %v", reserve, err)
	}
	draft, err := model.Lookahead(context.Background(), input)
	if err != nil || draft.Kind != "action" || len(draft.Preconditions) != 1 || !draft.UsageKnown || draft.Usage.TotalTokens != 27 || draft.ProviderModel != "preview-model" {
		t.Fatalf("invalid draft: %#v %v", draft, err)
	}
	request := generation.lastRequest(t)
	if request.Schema == nil || request.Schema.Name != "rin_next_step_draft" || request.MaxTokens != 2048 || !strings.Contains(request.Messages[0].Content, "NOT succeeded") || !strings.Contains(request.Messages[0].Content, "prepared-provider-contract") {
		t.Fatalf("conditional/prepared request missing: %#v", request)
	}
	schema, err := host.NewSchema(request.Schema.Schema)
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.ValidateInstance(output); err != nil {
		t.Fatal(err)
	}
	var packet map[string]json.RawMessage
	if err := json.Unmarshal([]byte(request.Messages[2].Content), &packet); err != nil {
		t.Fatal(err)
	}
	if len(packet["contract"]) == 0 || len(packet["current_action"]) == 0 || string(packet["current_operation_id"]) != `"operation.current"` ||
		strings.Contains(string(packet["contract"]), "UNTRUSTED_LOOKAHEAD_GOAL") || !strings.Contains(string(packet["untrusted_context"]), "UNTRUSTED_LOOKAHEAD_GOAL") ||
		!strings.Contains(string(packet["untrusted_context"]), `"value":false`) {
		t.Fatalf("current facts or trust boundary changed in preview: %s", request.Messages[2].Content)
	}
}

func TestLookaheadProviderRejectsUngroundedOrAuthoritativeOutputButKeepsUsage(t *testing.T) {
	cases := map[string]func(map[string]any){
		"complete":      func(output map[string]any) { output["kind"] = "complete" },
		"plan":          func(output map[string]any) { output["plan"] = map[string]any{"phase": "invented"} },
		"memory":        func(output map[string]any) { output["memory_candidates"] = []any{} },
		"unseen-target": func(output map[string]any) { output["target_handles"] = []string{"target.unknown"} },
		"capability":    func(output map[string]any) { output["capability_id"] = "invented.capability" },
		"unpublished-fact": func(output map[string]any) {
			output["preconditions"] = []map[string]string{{"fact_id": "invented.fact", "fact_value_json": "true"}}
		},
		"object-fact": func(output map[string]any) {
			output["preconditions"] = []map[string]string{{"fact_id": "next.allowed", "fact_value_json": `{"invented":true}`}}
		},
		"duplicate-fact": func(output map[string]any) {
			output["preconditions"] = []map[string]string{{"fact_id": "next.allowed", "fact_value_json": "true"}, {"fact_id": "next.allowed", "fact_value_json": "false"}}
		},
		"null-field":        func(output map[string]any) { output["preconditions"] = nil },
		"missing-field":     func(output map[string]any) { delete(output, "plan_step_id") },
		"unknown-plan-step": func(output map[string]any) { output["plan_step_id"] = "step.unknown" },
		"none-with-action":  func(output map[string]any) { output["kind"] = "none" },
		"oversized-summary": func(output map[string]any) { output["summary"] = strings.Repeat("中", 167) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			output := lookaheadModelOutput()
			mutate(output)
			payload, _ := json.Marshal(output)
			generation := &recordingGenerationProvider{responses: []provider.CompletionResponse{{Content: string(payload), Usage: provider.Usage{TotalTokens: 37}}}}
			draft, err := (cognition.StructuredDecisionProvider{GenerationProvider: generation}).Lookahead(context.Background(), lookaheadModelInput(t))
			if err == nil || !draft.UsageKnown || draft.Usage.TotalTokens != 37 {
				t.Fatalf("invalid prediction accepted or paid usage lost: %#v %v", draft, err)
			}
		})
	}
}

func TestLookaheadProviderRejectsForeignActionBeforeCallingModel(t *testing.T) {
	for _, mutate := range []func(*cognition.LookaheadInput){
		func(input *cognition.LookaheadInput) { input.Action.TaskID = "another.task" },
		func(input *cognition.LookaheadInput) { input.Action.ExpectedEpoch.Timeline++ },
	} {
		generation := &recordingGenerationProvider{}
		input := lookaheadModelInput(t)
		mutate(&input)
		_, err := (cognition.StructuredDecisionProvider{GenerationProvider: generation}).Lookahead(context.Background(), input)
		if err == nil || generation.calls != 0 {
			t.Fatalf("foreign in-flight action reached the model: %v", err)
		}
	}
}

func TestLookaheadProviderDoesNotTreatMissingUsageAsFreeInference(t *testing.T) {
	output, _ := json.Marshal(lookaheadModelOutput())
	generation := &recordingGenerationProvider{responses: []provider.CompletionResponse{{Content: string(output)}}}
	draft, err := (cognition.StructuredDecisionProvider{GenerationProvider: generation}).Lookahead(context.Background(), lookaheadModelInput(t))
	if err != nil || draft.Kind != "action" || draft.UsageKnown {
		t.Fatalf("missing usage was treated as a known zero-token call: %#v %v", draft, err)
	}
}
