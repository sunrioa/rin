package compat_test

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/extension"
	"github.com/sunrioa/rin/provider"
	rinruntime "github.com/sunrioa/rin/runtime"
)

var (
	_ rinruntime.DecisionProvider           = cognition.Deterministic{}
	_ provider.StructuredGenerationProvider = (*structuredProviderFixture)(nil)
	_ extension.MemoryIndex                 = (*memoryIndexPortFixture)(nil)
	_ extension.SpeechProvider              = (*speechProviderPortFixture)(nil)
	_ extension.TelemetrySink               = (*telemetrySinkPortFixture)(nil)
)

type structuredProviderFixture struct{}

func (*structuredProviderFixture) Complete(
	context.Context,
	provider.CompletionRequest,
) (provider.CompletionResponse, error) {
	return provider.CompletionResponse{}, nil
}

type memoryIndexPortFixture struct{}

func (*memoryIndexPortFixture) ReplaceSession(
	context.Context,
	string,
	[]extension.MemoryDocument,
) error {
	return nil
}

func (*memoryIndexPortFixture) Search(
	context.Context,
	extension.MemoryQuery,
) ([]extension.MemoryMatch, error) {
	return nil, nil
}

func (*memoryIndexPortFixture) DeleteSession(context.Context, string) error {
	return nil
}

type speechProviderPortFixture struct{}

func (*speechProviderPortFixture) Synthesize(
	context.Context,
	extension.SpeechRequest,
) (extension.AudioArtifactRef, error) {
	return extension.AudioArtifactRef{}, nil
}

type telemetrySinkPortFixture struct{}

func (*telemetrySinkPortFixture) Record(
	context.Context,
	extension.TelemetryEvent,
) error {
	return nil
}

func TestDecisionDraftIsStructuredAndHasNoPresentationText(t *testing.T) {
	draftType := reflect.TypeOf(rinruntime.DecisionDraft{})
	expected := []string{
		"OfferID",
		"Stance",
		"PolicySource",
		"RecalledMemoryIDs",
		"GoalID",
		"BoundaryID",
	}
	if draftType.NumField() != len(expected) {
		t.Fatalf("DecisionDraft fields changed: %v", fieldNames(draftType))
	}
	for index, name := range expected {
		if draftType.Field(index).Name != name {
			t.Fatalf("DecisionDraft fields changed: %v", fieldNames(draftType))
		}
	}
}

func TestOptionalTelemetryPortCannotCarryContent(t *testing.T) {
	eventType := reflect.TypeOf(extension.TelemetryEvent{})
	for _, forbidden := range []string{
		"Text", "Summary", "Rationale", "Prompt", "Messages", "Audio",
		"Credential", "Token", "Payload", "Attributes",
	} {
		if _, exists := eventType.FieldByName(forbidden); exists {
			t.Fatalf("TelemetryEvent exposes content field %s", forbidden)
		}
	}
}

func TestObsoletePublicGoPortNamesAreRemoved(t *testing.T) {
	files := []string{"../runtime/runtime.go", "../provider/provider.go"}
	for _, path := range files {
		payload, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		source := string(payload)
		for _, obsolete := range []string{
			"type Policy" + "Context ",
			"type Proposal" + "Draft ",
			"type Policy " + "interface",
			"type Client " + "interface",
		} {
			if strings.Contains(source, obsolete) {
				t.Fatalf("%s retains obsolete public declaration %q", path, obsolete)
			}
		}
	}
}

func fieldNames(value reflect.Type) []string {
	names := make([]string, value.NumField())
	for index := range names {
		names[index] = value.Field(index).Name
	}
	return names
}
