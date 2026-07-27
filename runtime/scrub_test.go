package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestScrubAdvancesWithinEventBudgetAndCompletesCycles(t *testing.T) {
	eventStore := newCheckpointWorkerStore(newInvariantStore())
	engine := openCheckpointWorkerEngine(t, eventStore)
	for _, sessionID := range []string{"session.scrub.a", "session.scrub.b"} {
		if _, err := engine.CreateSession(
			invariantCreate(sessionID, nil, nil),
		); err != nil {
			t.Fatal(err)
		}
	}
	appendCheckpointObservations(t, engine, "session.scrub.a", 5)
	appendCheckpointObservations(t, engine, "session.scrub.b", 3)

	total := 0
	for attempts := 0; attempts < 10; attempts++ {
		report, err := engine.Scrub(context.Background(), 2)
		if err != nil {
			t.Fatal(err)
		}
		if report.CheckedEvents > 2 {
			t.Fatalf("scrub exceeded its event budget: %+v", report)
		}
		total += report.CheckedEvents
		if report.CycleComplete {
			if report.CompletedCycles != 1 {
				t.Fatalf("first completed cycle = %+v", report)
			}
			break
		}
	}
	if total != 8 {
		t.Fatalf("scrubbed events = %d, want 8", total)
	}
	diagnostics := engine.Diagnostics()
	if diagnostics.ScrubCompletedCycles != 1 ||
		diagnostics.ScrubActive {
		t.Fatalf("completed scrub diagnostics = %+v", diagnostics)
	}
}

func TestScrubDefersEventsAfterCapturedHeadToNextCycle(t *testing.T) {
	eventStore := newCheckpointWorkerStore(newInvariantStore())
	engine := openCheckpointWorkerEngine(t, eventStore)
	const sessionID = "session.scrub.moving-head"
	if _, err := engine.CreateSession(
		invariantCreate(sessionID, nil, nil),
	); err != nil {
		t.Fatal(err)
	}
	appendCheckpointObservations(t, engine, sessionID, 3)

	first, err := engine.Scrub(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision != 1 || first.TargetRevision != 3 {
		t.Fatalf("first scrub pass = %+v", first)
	}
	appendCheckpointObservations(t, engine, sessionID, 4)

	second, err := engine.Scrub(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if !second.CycleComplete ||
		second.Revision != 3 ||
		second.TargetRevision != 3 {
		t.Fatalf("captured-head cycle = %+v", second)
	}

	third, err := engine.Scrub(context.Background(), 4)
	if err != nil {
		t.Fatal(err)
	}
	if !third.CycleComplete ||
		third.Revision != 4 ||
		third.TargetRevision != 4 ||
		third.CompletedCycles != 2 {
		t.Fatalf("next scrub cycle = %+v", third)
	}
}

func TestScrubRejectsInvalidCallsAndLegacyStore(t *testing.T) {
	engine, err := Open(newInvariantStore(), invariantPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Scrub(nil, 1); ErrorCode(err) != "invalid_scrub_context" {
		t.Fatalf("nil context error = %v", err)
	}
	for _, budget := range []int{0, MaxScrubEventBudget + 1} {
		if _, err := engine.Scrub(
			context.Background(),
			budget,
		); ErrorCode(err) != "invalid_scrub_budget" {
			t.Fatalf("budget %d error = %v", budget, err)
		}
	}
	if _, err := engine.Scrub(
		context.Background(),
		1,
	); ErrorCode(err) != "scrub_unsupported" {
		t.Fatalf("legacy Store error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	eventStore := newCheckpointWorkerStore(newInvariantStore())
	rangedEngine := openCheckpointWorkerEngine(t, eventStore)
	_, err = rangedEngine.Scrub(ctx, 1)
	if ErrorCode(err) != "scrub_canceled" ||
		!errors.Is(err, context.Canceled) {
		t.Fatalf("canceled scrub error = %v", err)
	}
}

func TestScrubIgnoresCheckpointAndFindsCorruptPrefix(t *testing.T) {
	eventStore := newCheckpointWorkerStore(newInvariantStore())
	engine := openCheckpointWorkerEngine(t, eventStore)
	const sessionID = "session.scrub.corrupt"
	if _, err := engine.CreateSession(
		invariantCreate(sessionID, nil, nil),
	); err != nil {
		t.Fatal(err)
	}
	appendCheckpointObservations(t, engine, sessionID, 3)

	eventStore.mu.Lock()
	corrupt := eventStore.events[sessionID][0]
	corrupt.Data = []byte(`{"request":null}`)
	eventStore.events[sessionID][0] = corrupt
	eventStore.mu.Unlock()

	report, err := engine.Scrub(context.Background(), 3)
	if ErrorCode(err) != "scrub_failed" ||
		!errors.Is(err, ErrCorruptLog) {
		t.Fatalf("corrupt scrub result = %+v, %v", report, err)
	}
	if report.CheckedEvents != 0 {
		t.Fatalf("corrupt event was counted as verified: %+v", report)
	}
	if failures := engine.Diagnostics().ScrubFailures; failures != 1 {
		t.Fatalf("scrub failure count = %d", failures)
	}
}

func TestScrubEmptyRuntimeCompletesWithoutReadingEvents(t *testing.T) {
	eventStore := newCheckpointWorkerStore(newInvariantStore())
	engine := openCheckpointWorkerEngine(t, eventStore)
	report, err := engine.Scrub(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !report.CycleComplete ||
		report.CheckedEvents != 0 ||
		report.CompletedCycles != 1 {
		t.Fatalf("empty scrub report = %+v", report)
	}
}

func TestScrubWaitIsCancelableAndDoesNotBlockDiagnostics(t *testing.T) {
	eventStore := &blockingScrubStore{
		checkpointWorkerStore: newCheckpointWorkerStore(newInvariantStore()),
		entered:               make(chan struct{}),
		release:               make(chan struct{}),
	}
	engine := openCheckpointWorkerEngine(t, eventStore)
	const sessionID = "session.scrub.concurrent"
	if _, err := engine.CreateSession(
		invariantCreate(sessionID, nil, nil),
	); err != nil {
		t.Fatal(err)
	}

	first := make(chan error, 1)
	go func() {
		_, err := engine.Scrub(context.Background(), 1)
		first <- err
	}()
	select {
	case <-eventStore.entered:
	case <-time.After(time.Second):
		t.Fatal("first scrub did not enter Store range read")
	}

	diagnosticsDone := make(chan Diagnostics, 1)
	go func() {
		diagnosticsDone <- engine.Diagnostics()
	}()
	select {
	case diagnostics := <-diagnosticsDone:
		if !diagnostics.ScrubActive ||
			diagnostics.ScrubRevision != 0 ||
			diagnostics.ScrubTargetRevision != 1 {
			t.Fatalf("active scrub diagnostics = %+v", diagnostics)
		}
	case <-time.After(time.Second):
		t.Fatal("diagnostics waited for scrub Store I/O")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := engine.Scrub(ctx, 1); ErrorCode(err) != "scrub_canceled" {
		t.Fatalf("canceled waiting scrub error = %v", err)
	}

	close(eventStore.release)
	select {
	case err := <-first:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("first scrub did not finish")
	}
}

type blockingScrubStore struct {
	*checkpointWorkerStore
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (s *blockingScrubStore) LoadRange(
	sessionID string,
	afterRevision uint64,
	throughRevision uint64,
	limit int,
) (EventPage, error) {
	s.once.Do(func() {
		close(s.entered)
		<-s.release
	})
	return s.checkpointWorkerStore.LoadRange(
		sessionID,
		afterRevision,
		throughRevision,
		limit,
	)
}
