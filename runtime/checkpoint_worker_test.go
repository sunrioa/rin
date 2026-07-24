package runtime

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/sunrioa/rin/protocol"
)

func TestAutomaticCheckpointScheduleUsesPowersOfTwo(t *testing.T) {
	for _, revision := range []uint64{256, 512, 1024, 1 << 63} {
		if !shouldSaveAutomaticCheckpoint(revision) {
			t.Fatalf("revision %d did not trigger an automatic checkpoint", revision)
		}
	}
	for _, revision := range []uint64{0, 1, 255, 257, 768, 1023, ^uint64(0)} {
		if shouldSaveAutomaticCheckpoint(revision) {
			t.Fatalf("revision %d unexpectedly triggered an automatic checkpoint", revision)
		}
	}

	if !shouldRepairHeadCheckpoint(300, 0) {
		t.Fatal("missing checkpoint did not request self-healing")
	}
	if shouldRepairHeadCheckpoint(300, 256) {
		t.Fatal("small checkpoint tail requested an exact-head rewrite")
	}
	if !shouldRepairHeadCheckpoint(512, 256) {
		t.Fatal("doubled checkpoint tail did not request self-healing")
	}
	if shouldRepairHeadCheckpoint(512, 512) {
		t.Fatal("exact-head checkpoint requested a rewrite")
	}
}

func TestCheckpointSaveDoesNotHoldTheMutationLock(t *testing.T) {
	eventStore := newCheckpointWorkerStore(newInvariantStore())
	engine := openCheckpointWorkerEngine(t, eventStore)
	const sessionID = "session.checkpoint-worker-boundary"
	if _, err := engine.CreateSession(invariantCreate(sessionID, nil, nil)); err != nil {
		t.Fatal(err)
	}
	eventStore.waitSavedRevision(t, 1)
	appendCheckpointObservations(t, engine, sessionID, 255)

	started, release, finished := eventStore.blockRevision(256)
	result := make(chan error, 1)
	go func() {
		_, err := engine.Observe(invariantObserve(
			sessionID,
			"observe.checkpoint.0256",
			"event.checkpoint.0256",
			255,
		))
		result <- err
	}()
	waitCheckpointSignal(t, started, "revision 256 checkpoint save did not start")
	waitCheckpointResult(t, result, "boundary mutation waited for checkpoint save")

	attempted, found := eventStore.attemptedRevision(256)
	if !found {
		t.Fatal("revision 256 checkpoint was not captured")
	}
	if err := ValidateCheckpoint(attempted); err != nil {
		t.Fatalf("captured checkpoint is invalid: %v", err)
	}

	next := make(chan error, 1)
	go func() {
		_, err := engine.Observe(invariantObserve(
			sessionID,
			"observe.checkpoint.0257",
			"event.checkpoint.0257",
			256,
		))
		next <- err
	}()
	waitCheckpointResult(t, next, "next mutation waited for checkpoint save")

	close(release)
	waitCheckpointSignal(t, finished, "revision 256 checkpoint save did not finish")
	saved, found := eventStore.savedRevision(256)
	if !found {
		t.Fatal("revision 256 checkpoint was not saved")
	}
	if !reflect.DeepEqual(saved, attempted) {
		t.Fatal("checkpoint capture changed while later mutations were applied")
	}
	if saved.Revision != 256 ||
		saved.Snapshot.State.Revision != 256 ||
		saved.Snapshot.IdentifierHistory == nil {
		t.Fatalf("saved checkpoint has the wrong revision: %+v", saved)
	}
	if _, leaked := saved.Snapshot.IdentifierHistory.Requests["observe.checkpoint.0257"]; leaked {
		t.Fatal("blocked checkpoint absorbed a later Request identity")
	}
	if err := ValidateCheckpoint(saved); err != nil {
		t.Fatalf("saved checkpoint became invalid after later mutation: %v", err)
	}
}

func TestCheckpointWorkerCoalescesLatestPendingRevision(t *testing.T) {
	eventStore := newCheckpointWorkerStore(newInvariantStore())
	engine := openCheckpointWorkerEngine(t, eventStore)
	const sessionID = "session.checkpoint-worker-coalesce"
	if _, err := engine.CreateSession(invariantCreate(sessionID, nil, nil)); err != nil {
		t.Fatal(err)
	}
	eventStore.waitSavedRevision(t, 1)
	appendCheckpointObservations(t, engine, sessionID, 255)

	started, release, finished := eventStore.blockRevision(256)
	appendCheckpointObservations(t, engine, sessionID, 256)
	waitCheckpointSignal(t, started, "revision 256 checkpoint save did not start")

	// Both 512 and 1024 are crossed while the old save is blocked. The single
	// pending slot must retain only the newest stable capture.
	appendCheckpointObservations(t, engine, sessionID, 1024)
	close(release)
	waitCheckpointSignal(t, finished, "revision 256 checkpoint save did not finish")
	eventStore.waitSavedRevision(t, 1024)

	if attempts := eventStore.attemptRevisions(); !reflect.DeepEqual(
		attempts,
		[]uint64{1, 256, 1024},
	) {
		t.Fatalf("checkpoint attempts = %v, want latest-wins coalescing", attempts)
	}
	if maximum := eventStore.maximumInFlight(); maximum != 1 {
		t.Fatalf("checkpoint saves in flight = %d, want at most 1", maximum)
	}
	checkpoint, found := eventStore.savedRevision(1024)
	if !found {
		t.Fatal("latest pending checkpoint was not saved")
	}
	if err := ValidateCheckpoint(checkpoint); err != nil {
		t.Fatalf("latest pending checkpoint is invalid: %v", err)
	}
}

func TestCheckpointFailureDoesNotReverseOrBlockMutation(t *testing.T) {
	eventStore := newCheckpointWorkerStore(newInvariantStore())
	engine := openCheckpointWorkerEngine(t, eventStore)
	const sessionID = "session.checkpoint-worker-failure"
	if _, err := engine.CreateSession(invariantCreate(sessionID, nil, nil)); err != nil {
		t.Fatal(err)
	}
	eventStore.waitSavedRevision(t, 1)
	appendCheckpointObservations(t, engine, sessionID, 255)

	finished := eventStore.failRevision(256)
	appendCheckpointObservations(t, engine, sessionID, 256)
	waitCheckpointSignal(t, finished, "failed checkpoint save did not return")
	appendCheckpointObservations(t, engine, sessionID, 257)

	state, err := engine.State(protocol.SessionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.Revision != 257 {
		t.Fatalf("durable mutation revision = %d after checkpoint failure", state.Revision)
	}
	if _, found := eventStore.savedRevision(256); found {
		t.Fatal("injected failed checkpoint was recorded as saved")
	}
}

func TestLazyHeadCheckpointBuildAndSaveRunOutsideSessionLock(t *testing.T) {
	events := newInvariantStore()
	source, err := Open(checkpointBaseStore{Store: events}, invariantPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	const sessionID = "session.checkpoint-worker-lazy-head"
	if _, err := source.CreateSession(invariantCreate(sessionID, nil, nil)); err != nil {
		t.Fatal(err)
	}
	appendCheckpointObservations(t, source, sessionID, 3)

	eventStore := newCheckpointWorkerStore(events)
	started, release, finished := eventStore.blockRevision(3)
	reopened := openCheckpointWorkerEngine(t, eventStore)
	stateResult := make(chan error, 1)
	go func() {
		_, stateErr := reopened.State(protocol.SessionRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       sessionID,
		})
		stateResult <- stateErr
	}()
	waitCheckpointSignal(t, started, "lazy exact-head checkpoint save did not start")
	waitCheckpointResult(t, stateResult, "lazy State waited for checkpoint save")

	next := make(chan error, 1)
	go func() {
		_, observeErr := reopened.Observe(invariantObserve(
			sessionID,
			"observe.checkpoint.0004",
			"event.checkpoint.0004",
			3,
		))
		next <- observeErr
	}()
	waitCheckpointResult(t, next, "mutation waited for lazy checkpoint save")

	close(release)
	waitCheckpointSignal(t, finished, "lazy exact-head checkpoint save did not finish")
	checkpoint, found := eventStore.savedRevision(3)
	if !found {
		t.Fatal("lazy exact-head checkpoint was not saved")
	}
	if err := ValidateCheckpoint(checkpoint); err != nil {
		t.Fatalf("lazy exact-head checkpoint is invalid: %v", err)
	}
}

func BenchmarkCheckpointBoundaryCapture(b *testing.B) {
	eventStore := newCheckpointWorkerStore(newInvariantStore())
	engine := openCheckpointWorkerEngine(b, eventStore)
	const sessionID = "session.checkpoint-worker-benchmark"
	if _, err := engine.CreateSession(invariantCreate(sessionID, nil, nil)); err != nil {
		b.Fatal(err)
	}
	eventStore.waitSavedRevision(b, 1)
	appendCheckpointObservations(b, engine, sessionID, 255)

	session, err := engine.mutationSession(sessionID)
	if err != nil {
		b.Fatal(err)
	}
	started, release, finished := eventStore.blockRevision(256)
	appendCheckpointObservations(b, engine, sessionID, 256)
	waitCheckpointSignal(b, started, "benchmark checkpoint save did not start")

	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		session.mu.Lock()
		engine.queueCheckpointLocked(session)
		session.mu.Unlock()
	}
	b.StopTimer()

	close(release)
	waitCheckpointSignal(b, finished, "benchmark checkpoint save did not finish")
	b.ReportMetric(float64(len(session.identifiers.Requests)), "request-identities")
}

type checkpointBaseStore struct {
	Store
}

type checkpointSaveControl struct {
	started      chan struct{}
	release      chan struct{}
	finished     chan struct{}
	fail         bool
	startedOnce  sync.Once
	finishedOnce sync.Once
}

type checkpointWorkerStore struct {
	*invariantStore

	checkpointMu sync.Mutex
	checkpoints  []Checkpoint
	attempts     []Checkpoint
	controls     map[uint64]*checkpointSaveControl
	inFlight     int
	maxInFlight  int
}

func newCheckpointWorkerStore(events *invariantStore) *checkpointWorkerStore {
	return &checkpointWorkerStore{
		invariantStore: events,
		controls:       make(map[uint64]*checkpointSaveControl),
	}
}

func (s *checkpointWorkerStore) Head(sessionID string) (EventAnchor, error) {
	events, err := s.Load(sessionID)
	if err != nil {
		return EventAnchor{}, err
	}
	if len(events) == 0 {
		return EventAnchor{}, ErrNotFound
	}
	tail := events[len(events)-1]
	return EventAnchor{Revision: tail.Sequence, HeadHash: tail.Hash}, nil
}

func (s *checkpointWorkerStore) LoadRange(
	sessionID string,
	afterRevision uint64,
	throughRevision uint64,
	limit int,
) (EventPage, error) {
	if limit <= 0 || throughRevision <= afterRevision {
		return EventPage{}, errors.New("invalid checkpoint test range")
	}
	events, err := s.Load(sessionID)
	if err != nil {
		return EventPage{}, err
	}
	if throughRevision > uint64(len(events)) || afterRevision > uint64(len(events)) {
		return EventPage{}, ErrNotFound
	}
	end := throughRevision
	if end-afterRevision > uint64(limit) {
		end = afterRevision + uint64(limit)
	}
	page := EventPage{
		Events:  append([]protocol.EventRecord(nil), events[afterRevision:end]...),
		HasMore: end < throughRevision,
	}
	return page, nil
}

func (s *checkpointWorkerStore) LoadCheckpoint(
	sessionID string,
	atOrBeforeRevision uint64,
) (Checkpoint, error) {
	s.checkpointMu.Lock()
	defer s.checkpointMu.Unlock()
	var selected Checkpoint
	found := false
	for _, checkpoint := range s.checkpoints {
		if checkpoint.SessionID != sessionID ||
			checkpoint.Revision > atOrBeforeRevision ||
			(found && checkpoint.Revision <= selected.Revision) {
			continue
		}
		selected = checkpoint
		found = true
	}
	if !found {
		return Checkpoint{}, ErrNotFound
	}
	return selected, nil
}

func (s *checkpointWorkerStore) SaveCheckpoint(
	sessionID string,
	checkpoint Checkpoint,
) error {
	if checkpoint.SessionID != sessionID {
		return errors.New("checkpoint session does not match destination")
	}

	s.checkpointMu.Lock()
	s.attempts = append(s.attempts, checkpoint)
	s.inFlight++
	if s.inFlight > s.maxInFlight {
		s.maxInFlight = s.inFlight
	}
	control := s.controls[checkpoint.Revision]
	if control != nil {
		control.startedOnce.Do(func() {
			close(control.started)
		})
	}
	s.checkpointMu.Unlock()

	if control != nil && control.release != nil {
		<-control.release
	}

	s.checkpointMu.Lock()
	defer s.checkpointMu.Unlock()
	s.inFlight--
	if control != nil {
		defer control.finishedOnce.Do(func() {
			close(control.finished)
		})
		if control.fail {
			return errors.New("injected checkpoint save failure")
		}
	}
	s.checkpoints = append(s.checkpoints, checkpoint)
	return nil
}

func (s *checkpointWorkerStore) blockRevision(
	revision uint64,
) (<-chan struct{}, chan struct{}, <-chan struct{}) {
	s.checkpointMu.Lock()
	defer s.checkpointMu.Unlock()
	control := &checkpointSaveControl{
		started:  make(chan struct{}),
		release:  make(chan struct{}),
		finished: make(chan struct{}),
	}
	s.controls[revision] = control
	return control.started, control.release, control.finished
}

func (s *checkpointWorkerStore) failRevision(revision uint64) <-chan struct{} {
	s.checkpointMu.Lock()
	defer s.checkpointMu.Unlock()
	control := &checkpointSaveControl{
		started:  make(chan struct{}),
		finished: make(chan struct{}),
		fail:     true,
	}
	s.controls[revision] = control
	return control.finished
}

func (s *checkpointWorkerStore) waitSavedRevision(
	tb testing.TB,
	revision uint64,
) Checkpoint {
	tb.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if checkpoint, found := s.savedRevision(revision); found {
			return checkpoint
		}
		if time.Now().After(deadline) {
			tb.Fatalf("checkpoint revision %d was not saved", revision)
		}
		time.Sleep(time.Millisecond)
	}
}

func (s *checkpointWorkerStore) attemptedRevision(
	revision uint64,
) (Checkpoint, bool) {
	s.checkpointMu.Lock()
	defer s.checkpointMu.Unlock()
	for _, checkpoint := range s.attempts {
		if checkpoint.Revision == revision {
			return checkpoint, true
		}
	}
	return Checkpoint{}, false
}

func (s *checkpointWorkerStore) savedRevision(
	revision uint64,
) (Checkpoint, bool) {
	s.checkpointMu.Lock()
	defer s.checkpointMu.Unlock()
	for _, checkpoint := range s.checkpoints {
		if checkpoint.Revision == revision {
			return checkpoint, true
		}
	}
	return Checkpoint{}, false
}

func (s *checkpointWorkerStore) attemptRevisions() []uint64 {
	s.checkpointMu.Lock()
	defer s.checkpointMu.Unlock()
	revisions := make([]uint64, 0, len(s.attempts))
	for _, checkpoint := range s.attempts {
		revisions = append(revisions, checkpoint.Revision)
	}
	return revisions
}

func (s *checkpointWorkerStore) maximumInFlight() int {
	s.checkpointMu.Lock()
	defer s.checkpointMu.Unlock()
	return s.maxInFlight
}

func openCheckpointWorkerEngine(
	tb testing.TB,
	eventStore Store,
) *Engine {
	tb.Helper()
	engine, err := Open(eventStore, invariantPolicy{})
	if err != nil {
		tb.Fatal(err)
	}
	return engine
}

func appendCheckpointObservations(
	tb testing.TB,
	engine *Engine,
	sessionID string,
	throughRevision uint64,
) {
	tb.Helper()
	state, err := engine.State(protocol.SessionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
	})
	if err != nil {
		tb.Fatal(err)
	}
	for revision := state.Revision + 1; revision <= throughRevision; revision++ {
		requestID := fmt.Sprintf("observe.checkpoint.%04d", revision)
		if _, err := engine.Observe(invariantObserve(
			sessionID,
			requestID,
			fmt.Sprintf("event.checkpoint.%04d", revision),
			int64(revision-1),
		)); err != nil {
			tb.Fatalf("append revision %d: %v", revision, err)
		}
	}
}

func waitCheckpointSignal(
	tb testing.TB,
	signal <-chan struct{},
	message string,
) {
	tb.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		tb.Fatal(message)
	}
}

func waitCheckpointResult(
	tb testing.TB,
	result <-chan error,
	message string,
) {
	tb.Helper()
	select {
	case err := <-result:
		if err != nil {
			tb.Fatal(err)
		}
	case <-time.After(time.Second):
		tb.Fatal(message)
	}
}
