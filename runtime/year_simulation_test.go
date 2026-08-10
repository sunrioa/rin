//go:build !race

package runtime_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
	"github.com/sunrioa/rin/store"
)

const (
	acceleratedYearDays         = 365
	acceleratedYearHoursPerStep = 6
	acceleratedYearStepsPerDay  = 24 / acceleratedYearHoursPerStep
)

// TestAcceleratedYearSession covers one deterministic observation every six
// simulated hours, one proposal/outcome per day, monthly snapshots, restart,
// historical retrieval, and final archival. It is a capacity and lifecycle
// regression, not a wall-clock soak or a claim about one game's frame budget.
func TestAcceleratedYearSession(t *testing.T) {
	const sessionID = "session.accelerated-year"
	root := t.TempDir()
	fileStore, err := store.OpenFile(root)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := rinruntime.Open(fileStore, cognition.Deterministic{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = engine.Close(context.Background())
		_ = fileStore.Close()
	})
	create := createRequest(sessionID)
	create.Features = []string{protocol.FeatureMemoryArchive}
	if _, err := engine.CreateSession(create); err != nil {
		t.Fatal(err)
	}

	for day := 0; day < acceleratedYearDays; day++ {
		for step := 0; step < acceleratedYearStepsPerDay; step++ {
			hour := day*24 + step*acceleratedYearHoursPerStep
			sequence := day*acceleratedYearStepsPerDay + step
			request := observeRequest(
				sessionID,
				fmt.Sprintf("observe.year.%04d", sequence),
				fmt.Sprintf("event.year.%04d", sequence),
				int64(hour),
			)
			request.Summary = fmt.Sprintf(
				"Six-hour NPC world interval %d completed.",
				sequence,
			)
			request.Quote = ""
			request.Importance = sequence%5 + 1
			if _, err := engine.Observe(request); err != nil {
				t.Fatalf("day %d step %d observation: %v", day, step, err)
			}
		}

		proposal, _, err := engine.Propose(
			context.Background(),
			proposeRequest(
				sessionID,
				fmt.Sprintf("propose.year.%03d", day),
				int64(day*24+23),
				[]string{"conversation"},
			),
		)
		if err != nil {
			t.Fatalf("day %d proposal: %v", day, err)
		}
		report := successfulReportRequest(
			proposal,
			fmt.Sprintf("report.year.%03d", day),
			fmt.Sprintf("outcome.year.%03d", day),
			int64(day*24+24),
			"Daily NPC action completed.",
		)
		report.Report.Outcome.WorldSeq = uint64(day + 1)
		if _, err := engine.ReportAction(report); err != nil {
			t.Fatalf("day %d outcome: %v", day, err)
		}
		if (day+1)%30 == 0 {
			if _, err := engine.Snapshot(sessionRequest(sessionID)); err != nil {
				t.Fatalf("day %d snapshot: %v", day+1, err)
			}
		}
	}

	expectedRevision := uint64(
		1 + acceleratedYearDays*
			(acceleratedYearStepsPerDay+2),
	)
	state, err := engine.State(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	if state.Revision != expectedRevision {
		t.Fatalf("revision = %d, want %d", state.Revision, expectedRevision)
	}
	actor := state.Actors["npc.mira"]
	if len(actor.Memories) > 128 || len(actor.MemorySummaries) == 0 {
		t.Fatalf(
			"memory projection is not bounded and archived: memories=%d summaries=%d",
			len(actor.Memories),
			len(actor.MemorySummaries),
		)
	}
	if err := engine.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := fileStore.Close(); err != nil {
		t.Fatal(err)
	}

	reopenedStore, err := store.OpenFile(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedStore.Close()
	reopened, err := rinruntime.Open(reopenedStore, cognition.Deterministic{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close(context.Background())
	recovered, err := reopened.State(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Revision != state.Revision ||
		recovered.HeadHash != state.HeadHash {
		t.Fatalf("year restart changed state: %+v", recovered)
	}
	timeline, err := reopened.Timeline(protocol.TimelineRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
		AfterRevision:   expectedRevision - 10,
		Limit:           10,
	})
	if err != nil || len(timeline.Entries) != 10 ||
		timeline.NextAfterRevision != expectedRevision {
		t.Fatalf("tail retrieval failed: %+v, %v", timeline, err)
	}
	historical, err := reopened.Replay(protocol.ReplayRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
		Revision:        1_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if historical.State.Revision != 1_000 {
		t.Fatalf("historical revision = %d, want 1000", historical.State.Revision)
	}
	stats, err := reopened.SessionStats(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	if stats.EventCount != expectedRevision ||
		stats.Bytes.EventLog == 0 ||
		stats.Bytes.Indexes == 0 ||
		stats.Bytes.Snapshots == 0 {
		t.Fatalf("year storage statistics are incomplete: %+v", stats)
	}
	archived, err := reopened.ArchiveSession(protocol.ArchiveSessionRequest{
		ProtocolVersion:  protocol.Version,
		SessionID:        sessionID,
		RequestID:        "archive.accelerated-year",
		ExpectedBinding:  recovered.Binding,
		ExpectedRevision: recovered.Revision,
		ExpectedHeadHash: recovered.HeadHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	if archived.ReceiptID == "" {
		t.Fatal("year archive did not return a receipt")
	}
}
