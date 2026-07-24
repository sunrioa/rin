package main

import (
	"bytes"
	"encoding/json"
	"fmt"
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
	if err := fileStore.Close(); err != nil {
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

func TestInspectTimelineReadsOnlyRequestedTail(t *testing.T) {
	counted := &inspectRangeCountingStore{Memory: store.NewMemory()}
	engine, err := rintime.Open(counted, policy.Deterministic{})
	if err != nil {
		t.Fatal(err)
	}
	const sessionID = "session.inspect-tail"
	_, err = engine.CreateSession(protocol.CreateSessionRequest{
		ProtocolVersion: protocol.Version,
		RequestID:       "create.inspect-tail",
		SessionID:       sessionID,
		Binding: protocol.Binding{
			GameID: "game.inspect", ContentID: "base", ContentVersion: "1", ContentHash: "hash",
		},
		Actors: []protocol.ActorSeed{{
			ID: "npc.inspect", Kind: "npc", DisplayName: "Inspector",
			ThinkEveryTicks: 1, Enabled: true,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for revision := 2; revision <= 20; revision++ {
		_, err := engine.Observe(protocol.ObserveRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       sessionID,
			RequestID:       fmt.Sprintf("observe.inspect-tail.%d", revision),
			EventID:         fmt.Sprintf("event.inspect-tail.%d", revision),
			Tick:            int64(revision),
			ObserverIDs:     []string{"npc.inspect"},
			Source:          "game",
			Kind:            "world",
			Summary:         "A bounded inspect event.",
			Importance:      2,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	counted.ranges = nil
	entries, err := inspectTimeline(engine, sessionID, 20, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 ||
		entries[0].Sequence != 18 ||
		entries[2].Sequence != 20 {
		t.Fatalf("inspect tail = %+v", entries)
	}
	if len(counted.ranges) != 2 {
		t.Fatalf("inspect range calls = %+v", counted.ranges)
	}
	page := counted.ranges[len(counted.ranges)-1]
	if page.after != 17 || page.through != 20 || page.limit != 3 {
		t.Fatalf("inspect did not seek directly to target tail: %+v", page)
	}
}

type inspectRangeCall struct {
	after   uint64
	through uint64
	limit   int
}

type inspectRangeCountingStore struct {
	*store.Memory
	ranges []inspectRangeCall
}

func (s *inspectRangeCountingStore) LoadRange(
	sessionID string,
	afterRevision uint64,
	throughRevision uint64,
	limit int,
) (rintime.EventPage, error) {
	s.ranges = append(s.ranges, inspectRangeCall{
		after: afterRevision, through: throughRevision, limit: limit,
	})
	return s.Memory.LoadRange(
		sessionID,
		afterRevision,
		throughRevision,
		limit,
	)
}
