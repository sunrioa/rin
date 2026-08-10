package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/sunrioa/rin/cognition"
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
	engine, err := rintime.Open(fileStore, cognition.Deterministic{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = engine.CreateSession(protocol.CreateSessionRequest{
		ProtocolVersion: protocol.Version, RequestID: "create.inspect", SessionID: "session.inspect",
		Binding:  protocol.Binding{GameID: "game.inspect", ContentID: "base", ContentVersion: "1", ContentHash: "hash"},
		Features: protocol.RecommendedFeatures(),
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
		Epoch: protocol.Epoch{
			SessionID: "session.inspect", WorldID: "world.inspect", Host: 1, World: 1, Timeline: 1,
		},
		ObservationSeq: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(context.Background()); err != nil {
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
	if result.Mode != "read-only" || result.SessionID != "session.inspect" ||
		result.Revision != 1 || result.ActorCount != 1 ||
		len(result.Timeline) != 1 {
		t.Fatalf("unexpected inspect output: %+v", result)
	}
}

func TestRunInspectHelpIsSuccessfulAndVisible(t *testing.T) {
	var output bytes.Buffer
	if err := runInspect([]string{"--help"}, &output); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"Usage of rin inspect:", "-data", "-session"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("help output omitted %q: %s", expected, output.String())
		}
	}
}

func TestRunInspectSupportsStateAboveInlineSnapshotLimit(t *testing.T) {
	directory := t.TempDir()
	fileStore, err := store.OpenFile(directory)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := rintime.OpenWithOptions(
		fileStore,
		cognition.Deterministic{},
		rintime.EngineOptions{
			MaxSessionStateBytes: rintime.MaxConfigurableSessionStateBytes,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	request := largeInspectCreateRequest()
	if _, err := engine.CreateSession(request); err != nil {
		t.Fatal(err)
	}
	state, err := engine.State(protocol.SessionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       request.SessionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) <= rintime.MaxInlineSnapshotBytes ||
		uint64(len(encoded)) > rintime.MaxConfigurableSessionStateBytes {
		t.Fatalf("test State size = %d, want (%d, %d]",
			len(encoded),
			rintime.MaxInlineSnapshotBytes,
			rintime.MaxConfigurableSessionStateBytes)
	}
	if err := engine.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := fileStore.Close(); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := runInspect([]string{
		"-data", directory,
		"-session", request.SessionID,
		"-revision", "1",
		"-timeline-limit", "0",
	}, &output); err != nil {
		t.Fatal(err)
	}
	var result inspectOutput
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.SessionID != request.SessionID || result.Revision != 1 ||
		result.ActorCount != 128 || result.StateHash == "" {
		t.Fatalf("large State inspection = %+v", result)
	}
}

func largeInspectCreateRequest() protocol.CreateSessionRequest {
	actors := make([]protocol.ActorSeed, 128)
	for actorIndex := range actors {
		actor := protocol.ActorSeed{
			ID:              fmt.Sprintf("npc.inspect-large.%03d", actorIndex),
			Kind:            "npc",
			DisplayName:     strings.Repeat("角", 120),
			ThinkEveryTicks: 1,
			Enabled:         true,
			Metadata:        make(map[string]string, 32),
			Boundaries:      make([]protocol.Boundary, 24),
			Goals:           make([]protocol.Goal, 32),
		}
		for index := 0; index < 32; index++ {
			actor.Metadata[fmt.Sprintf("meta.%02d", index)] =
				strings.Repeat("界", 500)
			actor.Goals[index] = protocol.Goal{
				ID:             fmt.Sprintf("goal.%02d", index),
				Description:    strings.Repeat("愿", 300),
				Motivation:     strings.Repeat("因", 300),
				Priority:       1,
				TargetProgress: 1,
				Status:         "active",
			}
		}
		for index := range actor.Boundaries {
			actor.Boundaries[index] = protocol.Boundary{
				ID:          fmt.Sprintf("boundary.%02d", index),
				Description: strings.Repeat("界", 300),
				Response:    "refuse",
			}
		}
		for index := 0; index < 24; index++ {
			actor.Traits = append(actor.Traits, fmt.Sprintf(
				"trait.%02d.%s",
				index,
				strings.Repeat("t", 80),
			))
		}
		actors[actorIndex] = actor
	}
	return protocol.CreateSessionRequest{
		ProtocolVersion: protocol.Version,
		RequestID:       "create.inspect-large",
		SessionID:       "session.inspect-large",
		Binding: protocol.Binding{
			GameID:         "game.inspect",
			ContentID:      "large-state",
			ContentVersion: "1",
			ContentHash:    strings.Repeat("a", 64),
		},
		Features: protocol.RecommendedFeatures(),
		Actors:   actors,
	}
}

func TestInspectTimelineReadsOnlyRequestedTail(t *testing.T) {
	counted := &inspectRangeCountingStore{Memory: store.NewMemory()}
	engine, err := rintime.Open(counted, cognition.Deterministic{})
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
		Features: protocol.RecommendedFeatures(),
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
			Epoch: protocol.Epoch{
				SessionID: sessionID, WorldID: "world.inspect", Host: 1, World: 1, Timeline: 1,
			},
			ObservationSeq: uint64(revision),
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
