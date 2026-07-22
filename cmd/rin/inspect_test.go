package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sunrioa/rin/policy"
	"github.com/sunrioa/rin/protocol"
	rintime "github.com/sunrioa/rin/runtime"
	"github.com/sunrioa/rin/store"
)

func TestRunInspectPrintsVerifiedRedactedSummary(t *testing.T) {
	directory := t.TempDir()
	fileStore, err := store.OpenFile(directory)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := rintime.Open(fileStore, policy.Deterministic{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = engine.CreateSession(protocol.CreateSessionRequest{
		ProtocolVersion: protocol.Version, RequestID: "create.inspect", SessionID: "session.inspect",
		Binding: protocol.Binding{GameID: "game.inspect", ContentID: "base", ContentVersion: "1", ContentHash: "hash"},
		Actors: []protocol.ActorSeed{{
			ID: "npc.inspect", Kind: "npc", DisplayName: "Inspector",
			ThinkEveryTicks: 1, Enabled: true,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = engine.Observe(protocol.ObserveRequest{
		ProtocolVersion: protocol.Version, SessionID: "session.inspect", RequestID: "observe.inspect",
		EventID: "event.inspect", Tick: 1, ObserverIDs: []string{"npc.inspect"}, Source: "game",
		Kind: "dialogue", Summary: "PRIVATE_SUMMARY", Quote: "PRIVATE_QUOTE", Importance: 3,
	})
	if err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := runInspect([]string{
		"-data", directory, "-session", "session.inspect", "-revision", "1", "-timeline-limit", "10",
	}, &output); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "PRIVATE_SUMMARY") || strings.Contains(output.String(), "PRIVATE_QUOTE") {
		t.Fatalf("inspect output leaked story text: %s", output.String())
	}
	var result inspectOutput
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.SessionID != "session.inspect" || result.Revision != 1 || result.ActorCount != 1 || len(result.Timeline) != 1 {
		t.Fatalf("unexpected inspect output: %+v", result)
	}
}
