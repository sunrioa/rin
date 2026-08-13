package timeline

import (
	"errors"
	"strings"
	"testing"

	"github.com/sunrioa/rin/host"
)

func TestBuildPageSortsPaginatesAndKeepsOpaqueCursorMonotonic(t *testing.T) {
	snapshot := Snapshot{
		TaskID: "task.timeline", LatestSequence: 3,
		Records: []Record{
			{Sequence: 3, Event: validEvent("task.timeline", "task.completed")},
			{Sequence: 1, Event: validEvent("task.timeline", "task.created")},
			{Sequence: 2, Event: validEvent("task.timeline", "model.decision")},
		},
	}
	first, err := BuildPage(snapshot, Query{TaskID: snapshot.TaskID, Limit: 2})
	if err != nil || len(first.Events) != 2 || !first.More ||
		first.Events[0].Cursor != FormatCursor(1) || first.NextCursor != FormatCursor(2) {
		t.Fatalf("first page = %#v, %v", first, err)
	}
	second, err := BuildPage(snapshot, Query{
		TaskID: snapshot.TaskID, AfterCursor: first.NextCursor, Limit: 2,
	})
	if err != nil || len(second.Events) != 1 || second.More ||
		second.NextCursor != FormatCursor(3) {
		t.Fatalf("second page = %#v, %v", second, err)
	}
	beyond := FormatCursor(20)
	empty, err := BuildPage(snapshot, Query{
		TaskID: snapshot.TaskID, AfterCursor: beyond,
	})
	if err != nil || len(empty.Events) != 0 || empty.NextCursor != beyond {
		t.Fatalf("cursor moved backwards: %#v, %v", empty, err)
	}
}

func TestBuildPageReportsTruncationAndDefensivelyCopiesMetrics(t *testing.T) {
	latency := uint64(12)
	event := validEvent("task.timeline", "model.decision")
	event.Model = &ModelUsage{Model: "model.test", LatencyMillis: &latency}
	event.Policy = &PolicySummary{
		Disposition: "allow", ReasonCode: "policy.allow", HumanSummary: "Allowed.",
		MatchedRuleIDs: []string{"rule.allow"},
	}
	page, err := BuildPage(Snapshot{
		TaskID: "task.timeline", LatestSequence: 4, TruncatedBefore: 2,
		Records: []Record{{Sequence: 3, Event: event}, {Sequence: 4, Event: validEvent("task.timeline", "operation.succeeded")}},
	}, Query{TaskID: "task.timeline"})
	if err != nil || !page.Truncated || len(page.Events) != 2 {
		t.Fatalf("page = %#v, %v", page, err)
	}
	*page.Events[0].Model.LatencyMillis = 99
	page.Events[0].Policy.MatchedRuleIDs[0] = "changed"
	if latency != 12 || event.Policy.MatchedRuleIDs[0] != "rule.allow" {
		t.Fatal("page aliases projection source")
	}
}

func TestBuildPageRejectsInvalidOrAmbiguousEvidence(t *testing.T) {
	epoch := host.Epoch{
		SessionID: "session.test", WorldID: "world.test", Host: 1, World: 1, Timeline: 1,
	}
	for _, test := range []struct {
		name    string
		records []Record
	}{
		{
			name: "duplicate sequence",
			records: []Record{
				{Sequence: 1, Event: validEvent("task.timeline", "first")},
				{Sequence: 1, Event: validEvent("task.timeline", "second")},
			},
		},
		{
			name: "observation without epoch",
			records: []Record{{Sequence: 1, Event: Event{
				TaskID: "task.timeline", EventKind: "invalid",
				ObservationSequence: 1,
			}}},
		},
		{
			name: "invalid execution claim",
			records: []Record{{Sequence: 1, Event: Event{
				TaskID: "task.timeline", EventKind: "invalid",
				ObservationSequence: 1, Epoch: &epoch,
				Operation: &OperationSummary{
					OperationID: "operation.test", Status: "running",
					ExecutionConfirmed: true,
				},
			}}},
		},
		{
			name: "duplicate memory rank",
			records: []Record{{Sequence: 1, Event: Event{
				TaskID: "task.timeline", EventKind: "invalid",
				MemoryContextRefs: []MemoryContextRef{
					{MemoryID: "memory.one", Domain: "actor", Source: "outcome", Rank: 1, Digest: strings.Repeat("a", 64)},
					{MemoryID: "memory.two", Domain: "actor", Source: "outcome", Rank: 1, Digest: strings.Repeat("b", 64)},
				},
			}}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := BuildPage(Snapshot{
				TaskID: "task.timeline", LatestSequence: 2, Records: test.records,
			}, Query{TaskID: "task.timeline"})
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	for _, cursor := range []string{
		"other:1", "tl1:", "tl1:!", "tl1:00", "tl1:A", strings.Repeat("x", 65),
	} {
		if _, err := ParseCursor(cursor); !errors.Is(err, ErrInvalid) {
			t.Fatalf("ParseCursor(%q) error = %v", cursor, err)
		}
	}
}

func TestBuildPageRejectsInvalidSnapshotMetadata(t *testing.T) {
	for name, snapshot := range map[string]Snapshot{
		"goal": {
			TaskID: "task.timeline", Goal: strings.Repeat("x", 2_001),
		},
		"status": {
			TaskID: "task.timeline", Status: strings.Repeat("x", 65),
		},
		"digest": {
			TaskID: "task.timeline", GoalDigest: "invalid",
		},
		"truncation": {
			TaskID: "task.timeline", LatestSequence: 1, TruncatedBefore: 2,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := BuildPage(snapshot, Query{TaskID: snapshot.TaskID}); !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid snapshot error = %v", err)
			}
		})
	}
}

func validEvent(taskID, kind string) Event {
	return Event{TaskID: taskID, EventKind: kind, OccurredAtUnixMillis: 1_000}
}
