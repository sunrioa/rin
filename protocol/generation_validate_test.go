package protocol_test

import (
	"math"
	"strings"
	"testing"

	"github.com/sunrioa/rin/protocol"
)

func TestValidateGenerationBoundsProviderFreeContract(t *testing.T) {
	valid := protocol.GenerationRequest{
		ProtocolVersion: protocol.Version,
		RequestID:       "generation.scene.fixture",
		Kind:            "scene",
		ContextHash:     strings.Repeat("a", 64),
		Messages: []protocol.GenerationMessage{
			{Role: "system", Content: "Return JSON."},
			{Role: "user", Content: `{"scene":"rain"}`},
		},
		Temperature:    0.6,
		MaxTokens:      512,
		ResponseFormat: "json_object",
	}
	if err := protocol.ValidateGeneration(valid); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(*protocol.GenerationRequest)
		field  string
	}{
		{name: "kind", field: "kind", mutate: func(request *protocol.GenerationRequest) { request.Kind = "shell" }},
		{name: "context hash", field: "context_hash", mutate: func(request *protocol.GenerationRequest) { request.ContextHash = "not-a-hash" }},
		{name: "role", field: "messages[0].role", mutate: func(request *protocol.GenerationRequest) { request.Messages[0].Role = "tool" }},
		{name: "message limit", field: "messages[0].content", mutate: func(request *protocol.GenerationRequest) { request.Messages[0].Content = strings.Repeat("x", 32769) }},
		{name: "temperature", field: "temperature", mutate: func(request *protocol.GenerationRequest) { request.Temperature = math.NaN() }},
		{name: "tokens", field: "max_tokens", mutate: func(request *protocol.GenerationRequest) { request.MaxTokens = 8193 }},
		{name: "format", field: "response_format", mutate: func(request *protocol.GenerationRequest) { request.ResponseFormat = "text" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := valid
			request.Messages = append([]protocol.GenerationMessage(nil), valid.Messages...)
			test.mutate(&request)
			err := protocol.ValidateGeneration(request)
			validation, ok := err.(*protocol.ValidationError)
			if !ok || validation.Field != test.field {
				t.Fatalf("expected field %q, got %v", test.field, err)
			}
		})
	}
}
