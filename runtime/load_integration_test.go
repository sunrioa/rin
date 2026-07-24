package runtime_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/sunrioa/rin/policy"
	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
	"github.com/sunrioa/rin/store"
)

func TestOpenLazilySingleflightsFirstSessionLoad(t *testing.T) {
	memory := store.NewMemory()
	source := newEngine(t, memory, policy.Deterministic{})
	const sessionID = "session.lazy-singleflight"
	if _, err := source.CreateSession(createRequest(sessionID)); err != nil {
		t.Fatal(err)
	}
	blocking := newBlockingLoadStore(memory)
	engine, err := rinruntime.Open(blocking, policy.Deterministic{})
	if err != nil {
		t.Fatal(err)
	}
	if calls := blocking.calls(); calls != 0 {
		t.Fatalf("Open eagerly loaded %d logs", calls)
	}

	const readers = 8
	start := make(chan struct{})
	results := make(chan error, readers)
	for index := 0; index < readers; index++ {
		go func() {
			<-start
			_, stateErr := engine.State(sessionRequest(sessionID))
			results <- stateErr
		}()
	}
	close(start)
	select {
	case <-blocking.started:
	case <-time.After(time.Second):
		t.Fatal("first lazy load did not start")
	}
	if calls := blocking.calls(); calls != 1 {
		t.Fatalf("concurrent first access started %d loads, want 1", calls)
	}
	close(blocking.release)
	for index := 0; index < readers; index++ {
		select {
		case resultErr := <-results:
			if resultErr != nil {
				t.Fatal(resultErr)
			}
		case <-time.After(time.Second):
			t.Fatal("concurrent lazy reader did not finish")
		}
	}
	if calls := blocking.calls(); calls != 1 {
		t.Fatalf("lazy load was not singleflight: %d calls", calls)
	}
	if err := engine.VerifyAll(); err != nil {
		t.Fatal(err)
	}
	if calls := blocking.calls(); calls != 2 {
		t.Fatalf("VerifyAll did not independently scan the loaded session: %d calls", calls)
	}
}

func TestLazyLoadFailureIsIsolatedAndRetryable(t *testing.T) {
	memory := store.NewMemory()
	source := newEngine(t, memory, policy.Deterministic{})
	const (
		healthyID = "session.lazy-healthy"
		failingID = "session.lazy-failing"
	)
	if _, err := source.CreateSession(createRequest(healthyID)); err != nil {
		t.Fatal(err)
	}
	if _, err := source.CreateSession(createRequest(failingID)); err != nil {
		t.Fatal(err)
	}
	failures := &failSessionLoadStore{
		Store: memory, sessionID: failingID, remaining: 1,
	}
	engine, err := rinruntime.Open(failures, policy.Deterministic{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.State(sessionRequest(healthyID)); err != nil {
		t.Fatalf("one lazy load failure affected a healthy Session: %v", err)
	}
	if _, err := engine.State(sessionRequest(failingID)); err == nil {
		t.Fatal("injected first lazy load failure was ignored")
	}
	if _, err := engine.State(sessionRequest(failingID)); err != nil {
		t.Fatalf("failed lazy load was permanently cached: %v", err)
	}
}

func TestLazyRecoveryWritesMissingHeadCheckpointOnlyOnce(t *testing.T) {
	eventStore := newRangeCheckpointStore()
	legacyStore := &baseStoreOnly{Store: eventStore}
	source := newEngine(t, legacyStore, policy.Deterministic{})
	const sessionID = "session.lazy-checkpoint-migration"
	if _, err := source.CreateSession(createRequest(sessionID)); err != nil {
		t.Fatal(err)
	}
	for index := 1; index <= 2; index++ {
		if _, err := source.Observe(observeRequest(
			sessionID,
			"observe.lazy-checkpoint."+threeDigits(index),
			"event.lazy-checkpoint."+threeDigits(index),
			int64(index),
		)); err != nil {
			t.Fatal(err)
		}
	}
	expected, err := source.State(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	if revisions := eventStore.checkpointRevisions(sessionID); len(revisions) != 0 {
		t.Fatalf("legacy Store unexpectedly wrote checkpoints: %v", revisions)
	}

	firstRecovery, err := rinruntime.Open(eventStore, policy.Deterministic{})
	if err != nil {
		t.Fatal(err)
	}
	firstState, err := firstRecovery.State(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(firstState, expected) {
		t.Fatal("genesis migration recovery changed current state")
	}
	eventStore.waitCheckpointSaves(t, 1)
	if revisions := eventStore.checkpointRevisions(sessionID); !reflect.DeepEqual(
		revisions,
		[]uint64{expected.Revision},
	) {
		t.Fatalf("lazy recovery checkpoints = %v", revisions)
	}
	if saves := eventStore.checkpointSaves(); saves != 1 {
		t.Fatalf("lazy recovery checkpoint saves = %d, want 1", saves)
	}

	audit, err := rinruntime.Open(eventStore, policy.Deterministic{})
	if err != nil {
		t.Fatal(err)
	}
	if err := audit.VerifyAll(); err != nil {
		t.Fatal(err)
	}
	if saves := eventStore.checkpointSaves(); saves != 1 {
		t.Fatalf("VerifyAll wrote a derived checkpoint: saves=%d", saves)
	}

	eventStore.resetRangeReads()
	secondRecovery, err := rinruntime.Open(eventStore, policy.Deterministic{})
	if err != nil {
		t.Fatal(err)
	}
	secondState, err := secondRecovery.State(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(secondState, expected) {
		t.Fatal("exact-head checkpoint recovery changed current state")
	}
	if saves := eventStore.checkpointSaves(); saves != 1 {
		t.Fatalf("exact-head recovery rewrote its checkpoint: saves=%d", saves)
	}
	anchorAfter := uint64(0)
	if expected.Revision > 1 {
		anchorAfter = expected.Revision - 2
	}
	if afters := eventStore.rangeAfters(); !reflect.DeepEqual(
		afters,
		[]uint64{anchorAfter},
	) {
		t.Fatalf("exact-head recovery range reads = %v, want anchor only", afters)
	}
}

func TestCheckpointTailReplayMatchesFullReplayAndFallsBackFromDamage(t *testing.T) {
	eventStore := newRangeCheckpointStore()
	engine := newEngine(t, eventStore, policy.Deterministic{})
	const sessionID = "session.checkpoint-tail"
	if _, err := engine.CreateSession(createRequest(sessionID)); err != nil {
		t.Fatal(err)
	}
	original := observeRequest(
		sessionID,
		"observe.checkpoint.000",
		"event.checkpoint.000",
		1,
	)
	first, err := engine.Observe(original)
	if err != nil {
		t.Fatal(err)
	}
	for index := 1; index < 300; index++ {
		request := observeRequest(
			sessionID,
			"observe.checkpoint."+threeDigits(index),
			"event.checkpoint."+threeDigits(index),
			int64(index+1),
		)
		if _, err := engine.Observe(request); err != nil {
			t.Fatal(err)
		}
	}
	eventStore.waitCheckpointSaves(t, 2)
	expected, err := engine.State(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	if revisions := eventStore.checkpointRevisions(sessionID); !reflect.DeepEqual(
		revisions,
		[]uint64{1, 256},
	) {
		t.Fatalf("automatic checkpoint revisions = %v", revisions)
	}

	eventStore.failNextRangeAfter(256)
	reopened, err := rinruntime.Open(eventStore, policy.Deterministic{})
	if err != nil {
		t.Fatal(err)
	}
	actual, err := reopened.State(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatal("checkpoint + tail replay changed current state")
	}
	repeated, err := reopened.Observe(original)
	if err != nil || !repeated.Duplicate ||
		repeated.Revision != first.Revision ||
		repeated.HeadHash != first.HeadHash {
		t.Fatalf("checkpoint replay lost exact idempotency: %+v err=%v", repeated, err)
	}

	eventStore.damageOldPrefix(true)
	checkpointOnly, err := rinruntime.Open(eventStore, policy.Deterministic{})
	if err != nil {
		t.Fatal(err)
	}
	checkpointState, err := checkpointOnly.State(sessionRequest(sessionID))
	if err != nil {
		t.Fatalf("usable checkpoint could not recover an intact tail: %v", err)
	}
	if !reflect.DeepEqual(checkpointState, expected) {
		t.Fatal("checkpoint + tail recovery changed current state")
	}
	if err := checkpointOnly.VerifyAll(); err == nil {
		t.Fatal("VerifyAll trusted checkpoint instead of scanning the old prefix")
	}
	eventStore.damageOldPrefix(false)

	eventStore.damageLatestCheckpoint(sessionID)
	fallback, err := rinruntime.Open(eventStore, policy.Deterministic{})
	if err != nil {
		t.Fatal(err)
	}
	fallbackState, err := fallback.State(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	current, err := reopened.State(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fallbackState, current) {
		t.Fatal("damaged latest checkpoint did not fall back to an older checkpoint")
	}
	if loads := eventStore.checkpointLoads(); loads < 2 {
		t.Fatalf("damaged checkpoint did not attempt an older generation: %d loads", loads)
	}
}

func TestTimelineAndReplayRangeIOReleaseMutationLock(t *testing.T) {
	eventStore := newRangeCheckpointStore()
	engine := newEngine(t, eventStore, policy.Deterministic{})
	const sessionID = "session.audit-lock-boundary"
	if _, err := engine.CreateSession(createRequest(sessionID)); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Observe(observeRequest(
		sessionID,
		"observe.audit.seed",
		"event.audit.seed",
		1,
	)); err != nil {
		t.Fatal(err)
	}

	t.Run("timeline", func(t *testing.T) {
		started, release := eventStore.blockNextRange()
		result := make(chan error, 1)
		go func() {
			_, timelineErr := engine.Timeline(protocol.TimelineRequest{
				ProtocolVersion: protocol.Version,
				SessionID:       sessionID,
				Limit:           2,
			})
			result <- timelineErr
		}()
		waitStarted(t, started)
		assertMutationCompletes(t, engine, observeRequest(
			sessionID,
			"observe.audit.timeline",
			"event.audit.timeline",
			2,
		))
		close(release)
		if err := waitError(t, result); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("replay", func(t *testing.T) {
		started, release := eventStore.blockNextRange()
		result := make(chan error, 1)
		go func() {
			_, replayErr := engine.Replay(protocol.ReplayRequest{
				ProtocolVersion: protocol.Version,
				SessionID:       sessionID,
				Revision:        1,
			})
			result <- replayErr
		}()
		waitStarted(t, started)
		assertMutationCompletes(t, engine, observeRequest(
			sessionID,
			"observe.audit.replay",
			"event.audit.replay",
			3,
		))
		close(release)
		if err := waitError(t, result); err != nil {
			t.Fatal(err)
		}
	})
}

func TestCheckpointFailureDoesNotReverseDurableCreate(t *testing.T) {
	eventStore := newRangeCheckpointStore()
	eventStore.failCheckpointSave = true
	engine := newEngine(t, eventStore, policy.Deterministic{})
	const sessionID = "session.checkpoint-derived-cache"
	result, err := engine.CreateSession(createRequest(sessionID))
	if err != nil {
		t.Fatalf("derived checkpoint failure reversed Create: %v", err)
	}
	if result.Revision != 1 {
		t.Fatalf("Create revision = %d", result.Revision)
	}
	state, err := engine.State(sessionRequest(sessionID))
	if err != nil || state.Revision != 1 {
		t.Fatalf("durable state unavailable after checkpoint failure: %+v err=%v", state, err)
	}
	eventStore.waitCheckpointSaves(t, 1)
}

func TestCheckpointReplayRestoresWritableOmittedMaps(t *testing.T) {
	eventStore := newRangeCheckpointStore()
	source := newEngine(t, eventStore, policy.Deterministic{})
	const sessionID = "session.checkpoint-writable-maps"
	if _, err := source.CreateSession(createRequest(sessionID)); err != nil {
		t.Fatal(err)
	}
	reopened, err := rinruntime.Open(eventStore, policy.Deterministic{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := reopened.Propose(
		context.Background(),
		proposeRequest(sessionID, "propose.checkpoint-map", 0, nil),
	); err != nil {
		t.Fatalf("checkpoint omitted Proposals map was not restored: %v", err)
	}
	observation := observeRequest(
		sessionID,
		"observe.checkpoint-map",
		"event.checkpoint-map",
		1,
	)
	observation.Facts = []protocol.Fact{{
		SubjectID: "gate", Predicate: "state", Object: "open", Confidence: 100,
	}}
	if _, err := reopened.Observe(observation); err != nil {
		t.Fatalf("checkpoint omitted actor Beliefs map was not restored: %v", err)
	}
}

func TestBaseStoreRestoreMutationReopenNormalizesOmittedMaps(t *testing.T) {
	const sessionID = "session.base-store-restore-maps"
	source := newEngine(t, store.NewMemory(), policy.Deterministic{})
	if _, err := source.CreateSession(createRequest(sessionID)); err != nil {
		t.Fatal(err)
	}
	snapshot, err := source.Snapshot(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}

	legacy := &baseStoreOnly{Store: store.NewMemory()}
	target := newEngine(t, legacy, policy.Deterministic{})
	if _, err := target.Restore(protocol.RestoreRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
		RequestID:       "restore.base-store-maps",
		ExpectedBinding: snapshot.State.Binding,
		Snapshot:        snapshot,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := target.Propose(
		context.Background(),
		proposeRequest(sessionID, "propose.base-store-maps", 0, nil),
	); err != nil {
		t.Fatalf("mutation after Restore could not write an omitted map: %v", err)
	}
	observation := observeRequest(
		sessionID,
		"observe.base-store-maps",
		"event.base-store-maps",
		1,
	)
	observation.Facts = []protocol.Fact{{
		SubjectID: "gate", Predicate: "state", Object: "open", Confidence: 100,
	}}
	if _, err := target.Observe(observation); err != nil {
		t.Fatalf("mutation after Restore could not write omitted actor maps: %v", err)
	}

	reopened, err := rinruntime.Open(legacy, policy.Deterministic{})
	if err != nil {
		t.Fatal(err)
	}
	state, err := reopened.State(sessionRequest(sessionID))
	if err != nil {
		t.Fatalf("base Store replay failed after Restore and mutations: %v", err)
	}
	if len(state.Proposals) != 1 ||
		state.Actors["npc.mira"].Beliefs["gate:state"].Object != "open" {
		t.Fatalf("base Store replay lost restored mutations: %+v", state)
	}
}

func TestCreateStoreIODoesNotHoldEngineMapLock(t *testing.T) {
	eventStore := newBlockingCreateStore(store.NewMemory())
	engine := newEngine(t, eventStore, policy.Deterministic{})
	const existingID = "session.create-lock-existing"
	if _, err := engine.CreateSession(createRequest(existingID)); err != nil {
		t.Fatal(err)
	}
	started, release := eventStore.blockNextCreate()
	createResult := make(chan error, 1)
	go func() {
		_, err := engine.CreateSession(createRequest("session.create-lock-new"))
		createResult <- err
	}()
	waitStarted(t, started)
	stateResult := make(chan error, 1)
	go func() {
		_, err := engine.State(sessionRequest(existingID))
		stateResult <- err
	}()
	if err := waitError(t, stateResult); err != nil {
		t.Fatalf("unrelated State was blocked by Create Store I/O: %v", err)
	}
	close(release)
	if err := waitError(t, createResult); err != nil {
		t.Fatal(err)
	}
}

func TestDifferentSessionCreatesDoNotShareLifecycleGate(t *testing.T) {
	eventStore := newBlockingCreateStore(store.NewMemory())
	engine := newEngine(t, eventStore, policy.Deterministic{})

	started, release := eventStore.blockNextCreate()
	firstResult := make(chan error, 1)
	go func() {
		_, err := engine.CreateSession(createRequest("session.lifecycle-first"))
		firstResult <- err
	}()
	waitStarted(t, started)

	secondResult := make(chan error, 1)
	go func() {
		_, err := engine.CreateSession(createRequest("session.lifecycle-second"))
		secondResult <- err
	}()
	select {
	case err := <-secondResult:
		if err != nil {
			close(release)
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		close(release)
		t.Fatal("unrelated Create was blocked by another Session lifecycle")
	}

	close(release)
	if err := waitError(t, firstResult); err != nil {
		t.Fatal(err)
	}
}

func TestSameSessionCreatesShareLifecycleGate(t *testing.T) {
	eventStore := newBlockingCreateStore(store.NewMemory())
	engine := newEngine(t, eventStore, policy.Deterministic{})
	request := createRequest("session.lifecycle-shared")

	type createOutcome struct {
		result protocol.MutationResult
		err    error
	}
	started, release := eventStore.blockNextCreate()
	firstResult := make(chan createOutcome, 1)
	go func() {
		result, err := engine.CreateSession(request)
		firstResult <- createOutcome{result: result, err: err}
	}()
	waitStarted(t, started)

	secondResult := make(chan createOutcome, 1)
	go func() {
		result, err := engine.CreateSession(request)
		secondResult <- createOutcome{result: result, err: err}
	}()
	select {
	case outcome := <-secondResult:
		close(release)
		t.Fatalf("same-Session Create bypassed lifecycle gate: %+v", outcome)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	first := <-firstResult
	if first.err != nil {
		t.Fatal(first.err)
	}
	second := <-secondResult
	if second.err != nil {
		t.Fatal(second.err)
	}
	if !second.result.Duplicate ||
		second.result.Revision != first.result.Revision ||
		second.result.HeadHash != first.result.HeadHash {
		t.Fatalf("serialized retry changed the first result: first=%+v second=%+v", first.result, second.result)
	}
}

type blockingLoadStore struct {
	rinruntime.Store
	mu      sync.Mutex
	count   int
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

// baseStoreOnly deliberately hides optional Store extensions so tests exercise
// the compatibility path that replays complete logs through Store.Load.
type baseStoreOnly struct {
	rinruntime.Store
}

type failSessionLoadStore struct {
	rinruntime.Store
	mu        sync.Mutex
	sessionID string
	remaining int
}

func (s *failSessionLoadStore) Load(
	sessionID string,
) ([]protocol.EventRecord, error) {
	s.mu.Lock()
	if sessionID == s.sessionID && s.remaining > 0 {
		s.remaining--
		s.mu.Unlock()
		return nil, errors.New("injected lazy load failure")
	}
	s.mu.Unlock()
	return s.Store.Load(sessionID)
}

func newBlockingLoadStore(delegate rinruntime.Store) *blockingLoadStore {
	return &blockingLoadStore{
		Store: delegate, started: make(chan struct{}), release: make(chan struct{}),
	}
}

func (s *blockingLoadStore) Load(sessionID string) ([]protocol.EventRecord, error) {
	s.mu.Lock()
	s.count++
	s.mu.Unlock()
	s.once.Do(func() { close(s.started) })
	<-s.release
	return s.Store.Load(sessionID)
}

func (s *blockingLoadStore) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count
}

type blockingCreateStore struct {
	rinruntime.Store
	mu      sync.Mutex
	block   bool
	started chan struct{}
	release chan struct{}
}

func newBlockingCreateStore(delegate rinruntime.Store) *blockingCreateStore {
	return &blockingCreateStore{Store: delegate}
}

func (s *blockingCreateStore) Create(
	sessionID string,
	event protocol.EventRecord,
) error {
	s.mu.Lock()
	block := s.block
	started := s.started
	release := s.release
	if block {
		s.block = false
	}
	s.mu.Unlock()
	if block {
		close(started)
		<-release
	}
	return s.Store.Create(sessionID, event)
}

func (s *blockingCreateStore) blockNextCreate() (<-chan struct{}, chan<- struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.block = true
	s.started = make(chan struct{})
	s.release = make(chan struct{})
	return s.started, s.release
}

type rangeCheckpointStore struct {
	delegate *store.Memory

	mu                  sync.Mutex
	checkpoints         map[string][]rinruntime.Checkpoint
	checkpointLoadCount int
	checkpointSaveCount int
	rangeAfterRevisions []uint64
	failCheckpointSave  bool
	blockRange          bool
	rangeStarted        chan struct{}
	rangeRelease        chan struct{}
	corruptOldPrefix    bool
	failAfterRevision   uint64
}

func newRangeCheckpointStore() *rangeCheckpointStore {
	return &rangeCheckpointStore{
		delegate:    store.NewMemory(),
		checkpoints: make(map[string][]rinruntime.Checkpoint),
	}
}

func (s *rangeCheckpointStore) Create(
	sessionID string,
	event protocol.EventRecord,
) error {
	return s.delegate.Create(sessionID, event)
}

func (s *rangeCheckpointStore) Append(
	sessionID string,
	event protocol.EventRecord,
) error {
	return s.delegate.Append(sessionID, event)
}

func (s *rangeCheckpointStore) Load(
	sessionID string,
) ([]protocol.EventRecord, error) {
	return s.delegate.Load(sessionID)
}

func (s *rangeCheckpointStore) ListSessions() ([]string, error) {
	return s.delegate.ListSessions()
}

func (s *rangeCheckpointStore) SaveSnapshot(
	sessionID string,
	snapshot protocol.Snapshot,
) error {
	return s.delegate.SaveSnapshot(sessionID, snapshot)
}

func (s *rangeCheckpointStore) Head(
	sessionID string,
) (rinruntime.EventAnchor, error) {
	events, err := s.delegate.Load(sessionID)
	if err != nil {
		return rinruntime.EventAnchor{}, err
	}
	tail := events[len(events)-1]
	return rinruntime.EventAnchor{
		Revision: tail.Sequence,
		HeadHash: tail.Hash,
	}, nil
}

func (s *rangeCheckpointStore) LoadRange(
	sessionID string,
	afterRevision uint64,
	throughRevision uint64,
	limit int,
) (rinruntime.EventPage, error) {
	s.mu.Lock()
	s.rangeAfterRevisions = append(s.rangeAfterRevisions, afterRevision)
	block := s.blockRange
	started := s.rangeStarted
	release := s.rangeRelease
	corruptOldPrefix := s.corruptOldPrefix
	failAfterRevision := s.failAfterRevision
	if failAfterRevision != 0 && afterRevision == failAfterRevision {
		s.failAfterRevision = 0
	}
	if block {
		s.blockRange = false
	}
	s.mu.Unlock()
	if block {
		close(started)
		<-release
	}
	if failAfterRevision != 0 && afterRevision == failAfterRevision {
		return rinruntime.EventPage{}, errors.New("injected checkpoint tail failure")
	}
	events, err := s.delegate.Load(sessionID)
	if err != nil {
		return rinruntime.EventPage{}, err
	}
	result := make([]protocol.EventRecord, 0, limit)
	previousRevision := uint64(0)
	previousHash := ""
	for _, event := range events {
		if event.Sequence > throughRevision {
			break
		}
		if err := rinruntime.VerifyEventRecord(
			previousRevision,
			previousHash,
			event,
		); err != nil {
			return rinruntime.EventPage{}, err
		}
		previousRevision = event.Sequence
		previousHash = event.Hash
		if event.Sequence > afterRevision && len(result) < limit {
			result = append(result, event)
		}
	}
	hasMore := len(result) > 0 &&
		result[len(result)-1].Sequence < throughRevision
	if corruptOldPrefix && afterRevision == 0 && len(result) > 0 {
		result[0].Hash = "damaged"
	}
	return rinruntime.EventPage{Events: result, HasMore: hasMore}, nil
}

func (s *rangeCheckpointStore) LoadCheckpoint(
	sessionID string,
	atOrBeforeRevision uint64,
) (rinruntime.Checkpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checkpointLoadCount++
	var selected rinruntime.Checkpoint
	found := false
	for _, checkpoint := range s.checkpoints[sessionID] {
		if checkpoint.Revision <= atOrBeforeRevision &&
			(!found || checkpoint.Revision > selected.Revision) {
			selected = checkpoint
			found = true
		}
	}
	if !found {
		return rinruntime.Checkpoint{}, rinruntime.ErrNotFound
	}
	return selected, nil
}

func (s *rangeCheckpointStore) SaveCheckpoint(
	sessionID string,
	checkpoint rinruntime.Checkpoint,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checkpointSaveCount++
	if s.failCheckpointSave {
		return errors.New("injected checkpoint failure")
	}
	for index, existing := range s.checkpoints[sessionID] {
		if existing.Revision == checkpoint.Revision {
			s.checkpoints[sessionID][index] = checkpoint
			return nil
		}
	}
	s.checkpoints[sessionID] = append(s.checkpoints[sessionID], checkpoint)
	return nil
}

func (s *rangeCheckpointStore) checkpointRevisions(sessionID string) []uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]uint64, 0, len(s.checkpoints[sessionID]))
	for _, checkpoint := range s.checkpoints[sessionID] {
		result = append(result, checkpoint.Revision)
	}
	return result
}

func (s *rangeCheckpointStore) damageLatestCheckpoint(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	latest := 0
	for index := range s.checkpoints[sessionID] {
		if s.checkpoints[sessionID][index].Revision >
			s.checkpoints[sessionID][latest].Revision {
			latest = index
		}
	}
	s.checkpoints[sessionID][latest].Checksum = "damaged"
	s.checkpointLoadCount = 0
}

func (s *rangeCheckpointStore) checkpointLoads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.checkpointLoadCount
}

func (s *rangeCheckpointStore) checkpointSaves() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.checkpointSaveCount
}

func (s *rangeCheckpointStore) waitCheckpointSaves(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if s.checkpointSaves() >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf(
				"checkpoint saves = %d, want at least %d",
				s.checkpointSaves(),
				want,
			)
		}
		time.Sleep(time.Millisecond)
	}
}

func (s *rangeCheckpointStore) resetRangeReads() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rangeAfterRevisions = nil
}

func (s *rangeCheckpointStore) rangeAfters() []uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]uint64(nil), s.rangeAfterRevisions...)
}

func (s *rangeCheckpointStore) blockNextRange() (<-chan struct{}, chan<- struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blockRange = true
	s.rangeStarted = make(chan struct{})
	s.rangeRelease = make(chan struct{})
	return s.rangeStarted, s.rangeRelease
}

func (s *rangeCheckpointStore) damageOldPrefix(damage bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.corruptOldPrefix = damage
}

func (s *rangeCheckpointStore) failNextRangeAfter(revision uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failAfterRevision = revision
}

func waitStarted(t *testing.T, started <-chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("range read did not start")
	}
}

func assertMutationCompletes(
	t *testing.T,
	engine *rinruntime.Engine,
	request protocol.ObserveRequest,
) {
	t.Helper()
	result := make(chan error, 1)
	go func() {
		_, err := engine.Observe(request)
		result <- err
	}()
	if err := waitError(t, result); err != nil {
		t.Fatalf("mutation was blocked by audit I/O: %v", err)
	}
}

func waitError(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(time.Second):
		t.Fatal("operation did not finish")
		return nil
	}
}

func threeDigits(value int) string {
	return string([]byte{
		byte('0' + value/100%10),
		byte('0' + value/10%10),
		byte('0' + value%10),
	})
}
