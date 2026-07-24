package runtime_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/sunrioa/rin/policy"
	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
	"github.com/sunrioa/rin/store"
)

const (
	benchmarkLongSessionID       = "session.benchmark-long"
	benchmarkLongSessionRevision = uint64(1_000)
	benchmarkReplayRevision      = uint64(700)
)

var benchmarkEngineSink *rinruntime.Engine

type longSessionBenchmarkFixture struct {
	root                     string
	currentState             protocol.SessionState
	replaySnapshot           protocol.Snapshot
	latestCheckpointRevision uint64
	replayCheckpointRevision uint64
	eventLogBytes            int64
	eventIndexBytes          int64
	checkpointBytes          int64
}

func TestEngineOpenEnumeratesWithoutReadingSessionLogs(t *testing.T) {
	spy := newOpenEnumerationSpy(1_000)
	engine, err := rinruntime.Open(spy, policy.Deterministic{})
	if err != nil {
		t.Fatal(err)
	}
	if spy.loadCalls != 0 ||
		spy.headCalls != 0 ||
		spy.rangeCalls != 0 ||
		spy.checkpointLoadCalls != 0 ||
		spy.checkpointSaveCalls != 0 {
		t.Fatalf(
			"Open read session data: Load=%d Head=%d LoadRange=%d LoadCheckpoint=%d SaveCheckpoint=%d",
			spy.loadCalls,
			spy.headCalls,
			spy.rangeCalls,
			spy.checkpointLoadCalls,
			spy.checkpointSaveCalls,
		)
	}
	benchmarkEngineSink = engine
}

func BenchmarkEngineOpenEnumeratesSessions(b *testing.B) {
	spy := newOpenEnumerationSpy(10_000)
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		engine, err := rinruntime.Open(spy, policy.Deterministic{})
		if err != nil {
			b.Fatal(err)
		}
		benchmarkEngineSink = engine
	}
	b.StopTimer()
	b.ReportMetric(float64(len(spy.ids)), "sessions/op")
	if spy.loadCalls != 0 ||
		spy.headCalls != 0 ||
		spy.rangeCalls != 0 ||
		spy.checkpointLoadCalls != 0 ||
		spy.checkpointSaveCalls != 0 {
		b.Fatalf(
			"Open read session data: Load=%d Head=%d LoadRange=%d LoadCheckpoint=%d SaveCheckpoint=%d",
			spy.loadCalls,
			spy.headCalls,
			spy.rangeCalls,
			spy.checkpointLoadCalls,
			spy.checkpointSaveCalls,
		)
	}
}

func BenchmarkLongSessionReadPaths(b *testing.B) {
	fixture := buildLongFileSessionFixture(b)

	b.Run("RestartFirstStateCheckpointTail1000", func(b *testing.B) {
		request := protocol.SessionRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       benchmarkLongSessionID,
		}
		b.ReportAllocs()
		b.ResetTimer()
		b.StopTimer()
		for index := 0; index < b.N; index++ {
			counted, engine := openCountedFileEngine(b, fixture.root)
			b.StartTimer()
			state, err := engine.State(request)
			b.StopTimer()
			if err != nil {
				_ = counted.Close()
				b.Fatal(err)
			}
			if !reflect.DeepEqual(state, fixture.currentState) {
				_ = counted.Close()
				b.Fatalf(
					"checkpoint State differs from fixture: revision=%d head=%s",
					state.Revision,
					state.HeadHash,
				)
			}
			if counted.loadCalls != 0 ||
				counted.headCalls != 1 ||
				counted.checkpointCalls == 0 {
				_ = counted.Close()
				b.Fatalf(
					"first State did not use checkpoint + tail: Load=%d Head=%d LoadCheckpoint=%d LoadRange=%d",
					counted.loadCalls,
					counted.headCalls,
					counted.checkpointCalls,
					len(counted.rangeRequests),
				)
			}
			assertBoundedRangeRequests(
				b,
				counted.rangeRequests,
				3,
				3,
				fixture.latestCheckpointRevision-2,
				benchmarkLongSessionRevision,
				256,
			)
			if err := counted.Close(); err != nil {
				b.Fatal(err)
			}
		}
		reportLongSessionFixtureMetrics(b, fixture)
	})

	b.Run("RestartFirstStateGenesis1000", func(b *testing.B) {
		request := protocol.SessionRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       benchmarkLongSessionID,
		}
		b.ReportAllocs()
		b.ResetTimer()
		b.StopTimer()
		for index := 0; index < b.N; index++ {
			counted, engine := openRangeOnlyFileEngine(b, fixture.root)
			b.StartTimer()
			state, err := engine.State(request)
			b.StopTimer()
			if err != nil {
				_ = counted.Close()
				b.Fatal(err)
			}
			if !reflect.DeepEqual(state, fixture.currentState) {
				_ = counted.Close()
				b.Fatalf(
					"genesis State differs from fixture: revision=%d head=%s",
					state.Revision,
					state.HeadHash,
				)
			}
			if counted.loadCalls != 0 || counted.headCalls != 1 {
				_ = counted.Close()
				b.Fatalf(
					"genesis State bypassed ranged replay: Load=%d Head=%d LoadRange=%d",
					counted.loadCalls,
					counted.headCalls,
					len(counted.rangeRequests),
				)
			}
			assertBoundedRangeRequests(
				b,
				counted.rangeRequests,
				1,
				4,
				0,
				benchmarkLongSessionRevision,
				256,
			)
			assertGenesisRangeStarts(b, counted.rangeRequests, 1)
			if err := counted.Close(); err != nil {
				b.Fatal(err)
			}
		}
		reportLongSessionFixtureMetrics(b, fixture)
	})

	b.Run("TimelineTailPage", func(b *testing.B) {
		counted, engine := openLoadedCountedFileEngine(b, fixture.root, fixture.currentState)
		defer counted.Close()
		counted.resetReadCounts()
		request := protocol.TimelineRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       benchmarkLongSessionID,
			AfterRevision:   benchmarkLongSessionRevision - 10,
			Limit:           10,
		}
		b.ReportAllocs()
		b.ResetTimer()
		for index := 0; index < b.N; index++ {
			response, err := engine.Timeline(request)
			if err != nil {
				b.Fatal(err)
			}
			if len(response.Entries) != request.Limit ||
				response.NextAfterRevision != benchmarkLongSessionRevision ||
				response.CurrentRevision != benchmarkLongSessionRevision ||
				response.HasMore {
				b.Fatalf("unexpected tail page: %+v", response)
			}
		}
		b.StopTimer()
		reportLongSessionFixtureMetrics(b, fixture)
		if counted.loadCalls != 0 ||
			counted.checkpointCalls != 0 {
			b.Fatalf(
				"tail pagination bypassed bounded ranges: Load=%d LoadCheckpoint=%d LoadRange=%d",
				counted.loadCalls,
				counted.checkpointCalls,
				len(counted.rangeRequests),
			)
		}
		assertBoundedRangeRequests(
			b,
			counted.rangeRequests,
			b.N,
			2*b.N,
			request.AfterRevision-2,
			benchmarkLongSessionRevision,
			request.Limit,
		)
		if err := counted.Close(); err != nil {
			b.Fatal(err)
		}
	})

	b.Run("TimelineTenContinuousPages", func(b *testing.B) {
		counted, engine := openLoadedCountedFileEngine(b, fixture.root, fixture.currentState)
		defer counted.Close()
		counted.resetReadCounts()
		const (
			firstAfter = uint64(800)
			pageLimit  = 20
			pageCount  = 10
		)
		b.ReportAllocs()
		b.ResetTimer()
		for index := 0; index < b.N; index++ {
			after := firstAfter
			for pageNumber := 0; pageNumber < pageCount; pageNumber++ {
				response, err := engine.Timeline(protocol.TimelineRequest{
					ProtocolVersion: protocol.Version,
					SessionID:       benchmarkLongSessionID,
					AfterRevision:   after,
					Limit:           pageLimit,
				})
				if err != nil {
					b.Fatal(err)
				}
				after = response.NextAfterRevision
				if response.HasMore != (pageNumber < pageCount-1) {
					b.Fatalf("page %d HasMore = %t", pageNumber, response.HasMore)
				}
			}
			if after != benchmarkLongSessionRevision {
				b.Fatalf("continuous pagination ended at revision %d", after)
			}
		}
		b.StopTimer()
		b.ReportMetric(pageCount, "pages/op")
		reportLongSessionFixtureMetrics(b, fixture)
		if counted.loadCalls != 0 ||
			counted.checkpointCalls != 0 {
			b.Fatalf(
				"continuous pagination repeated an unbounded read: Load=%d LoadCheckpoint=%d LoadRange=%d",
				counted.loadCalls,
				counted.checkpointCalls,
				len(counted.rangeRequests),
			)
		}
		assertBoundedRangeRequests(
			b,
			counted.rangeRequests,
			pageCount*b.N,
			2*pageCount*b.N,
			firstAfter-2,
			benchmarkLongSessionRevision,
			pageLimit,
		)
		if err := counted.Close(); err != nil {
			b.Fatal(err)
		}
	})

	b.Run("ReplayRevision700CheckpointTail", func(b *testing.B) {
		counted, engine := openLoadedCountedFileEngine(b, fixture.root, fixture.currentState)
		defer counted.Close()
		counted.resetReadCounts()
		request := protocol.ReplayRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       benchmarkLongSessionID,
			Revision:        benchmarkReplayRevision,
		}
		b.ReportAllocs()
		b.ResetTimer()
		for index := 0; index < b.N; index++ {
			snapshot, err := engine.Replay(request)
			if err != nil {
				b.Fatal(err)
			}
			if !reflect.DeepEqual(snapshot, fixture.replaySnapshot) {
				b.Fatalf(
					"checkpoint Replay differs from fixture: revision=%d head=%s",
					snapshot.State.Revision,
					snapshot.State.HeadHash,
				)
			}
		}
		b.StopTimer()
		reportLongSessionFixtureMetrics(b, fixture)
		if counted.loadCalls != 0 ||
			counted.checkpointCalls < uint64(b.N) ||
			counted.checkpointCalls > uint64(2*b.N) {
			b.Fatalf(
				"Replay did not use checkpoint + bounded tail: Load=%d LoadCheckpoint=%d LoadRange=%d",
				counted.loadCalls,
				counted.checkpointCalls,
				len(counted.rangeRequests),
			)
		}
		assertBoundedRangeRequests(
			b,
			counted.rangeRequests,
			b.N,
			2*b.N,
			fixture.replayCheckpointRevision-2,
			benchmarkReplayRevision,
			256,
		)
		if err := counted.Close(); err != nil {
			b.Fatal(err)
		}
	})

	b.Run("ReplayRevision700Genesis", func(b *testing.B) {
		counted, engine := openLoadedRangeOnlyFileEngine(
			b,
			fixture.root,
			fixture.currentState,
		)
		defer counted.Close()
		counted.resetReadCounts()
		request := protocol.ReplayRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       benchmarkLongSessionID,
			Revision:        benchmarkReplayRevision,
		}
		b.ReportAllocs()
		b.ResetTimer()
		for index := 0; index < b.N; index++ {
			snapshot, err := engine.Replay(request)
			if err != nil {
				b.Fatal(err)
			}
			if !reflect.DeepEqual(snapshot, fixture.replaySnapshot) {
				b.Fatalf(
					"genesis Replay differs from fixture: revision=%d head=%s",
					snapshot.State.Revision,
					snapshot.State.HeadHash,
				)
			}
		}
		b.StopTimer()
		reportLongSessionFixtureMetrics(b, fixture)
		if counted.loadCalls != 0 {
			b.Fatalf(
				"genesis Replay bypassed ranged replay: Load=%d LoadRange=%d",
				counted.loadCalls,
				len(counted.rangeRequests),
			)
		}
		assertBoundedRangeRequests(
			b,
			counted.rangeRequests,
			b.N,
			3*b.N,
			0,
			benchmarkReplayRevision,
			256,
		)
		assertGenesisRangeStarts(b, counted.rangeRequests, b.N)
		if err := counted.Close(); err != nil {
			b.Fatal(err)
		}
	})
}

type openEnumerationSpy struct {
	ids                 []string
	loadCalls           uint64
	headCalls           uint64
	rangeCalls          uint64
	checkpointLoadCalls uint64
	checkpointSaveCalls uint64
}

func newOpenEnumerationSpy(sessionCount int) *openEnumerationSpy {
	ids := make([]string, sessionCount)
	for index := range ids {
		ids[index] = fmt.Sprintf("session.%05d", index)
	}
	return &openEnumerationSpy{ids: ids}
}

func (s *openEnumerationSpy) Create(string, protocol.EventRecord) error {
	return errors.New("unexpected Create")
}

func (s *openEnumerationSpy) Append(string, protocol.EventRecord) error {
	return errors.New("unexpected Append")
}

func (s *openEnumerationSpy) Load(string) ([]protocol.EventRecord, error) {
	s.loadCalls++
	return nil, errors.New("unexpected Load")
}

func (s *openEnumerationSpy) ListSessions() ([]string, error) {
	return append([]string(nil), s.ids...), nil
}

func (s *openEnumerationSpy) SaveSnapshot(string, protocol.Snapshot) error {
	return errors.New("unexpected SaveSnapshot")
}

func (s *openEnumerationSpy) Head(string) (rinruntime.EventAnchor, error) {
	s.headCalls++
	return rinruntime.EventAnchor{}, errors.New("unexpected Head")
}

func (s *openEnumerationSpy) LoadRange(
	string,
	uint64,
	uint64,
	int,
) (rinruntime.EventPage, error) {
	s.rangeCalls++
	return rinruntime.EventPage{}, errors.New("unexpected LoadRange")
}

func (s *openEnumerationSpy) LoadCheckpoint(
	string,
	uint64,
) (rinruntime.Checkpoint, error) {
	s.checkpointLoadCalls++
	return rinruntime.Checkpoint{}, errors.New("unexpected LoadCheckpoint")
}

func (s *openEnumerationSpy) SaveCheckpoint(
	string,
	rinruntime.Checkpoint,
) error {
	s.checkpointSaveCalls++
	return errors.New("unexpected SaveCheckpoint")
}

type benchmarkRangeRequest struct {
	sessionID       string
	afterRevision   uint64
	throughRevision uint64
	limit           int
}

type benchmarkReadStats struct {
	loadCalls           uint64
	headCalls           uint64
	checkpointCalls     uint64
	checkpointSaveCalls uint64
	rangeRequests       []benchmarkRangeRequest
}

func (s *benchmarkReadStats) resetReadCounts() {
	s.loadCalls = 0
	s.headCalls = 0
	s.checkpointCalls = 0
	s.checkpointSaveCalls = 0
	s.rangeRequests = nil
}

type benchmarkReadCountingStore struct {
	*store.File
	benchmarkReadStats
}

func (s *benchmarkReadCountingStore) Load(
	sessionID string,
) ([]protocol.EventRecord, error) {
	s.loadCalls++
	return s.File.Load(sessionID)
}

func (s *benchmarkReadCountingStore) Head(
	sessionID string,
) (rinruntime.EventAnchor, error) {
	s.headCalls++
	return s.File.Head(sessionID)
}

func (s *benchmarkReadCountingStore) LoadRange(
	sessionID string,
	afterRevision uint64,
	throughRevision uint64,
	limit int,
) (rinruntime.EventPage, error) {
	s.rangeRequests = append(s.rangeRequests, benchmarkRangeRequest{
		sessionID:       sessionID,
		afterRevision:   afterRevision,
		throughRevision: throughRevision,
		limit:           limit,
	})
	return s.File.LoadRange(sessionID, afterRevision, throughRevision, limit)
}

func (s *benchmarkReadCountingStore) LoadCheckpoint(
	sessionID string,
	atOrBeforeRevision uint64,
) (rinruntime.Checkpoint, error) {
	s.checkpointCalls++
	return s.File.LoadCheckpoint(sessionID, atOrBeforeRevision)
}

func (s *benchmarkReadCountingStore) SaveCheckpoint(
	_ string,
	_ rinruntime.Checkpoint,
) error {
	// Keep repeated benchmark iterations on the same checkpoint + tail fixture.
	// Runtime still performs checkpoint construction, while this read-path spy
	// prevents best-effort current-head publication from changing later runs.
	s.checkpointSaveCalls++
	return nil
}

// benchmarkRangeOnlyStore deliberately hides CheckpointStore while preserving
// the same File Store, event log, and revision index. It provides a directly
// comparable genesis replay baseline without copying or changing the fixture.
type benchmarkRangeOnlyStore struct {
	file *store.File
	benchmarkReadStats
}

func (s *benchmarkRangeOnlyStore) Create(
	sessionID string,
	event protocol.EventRecord,
) error {
	return s.file.Create(sessionID, event)
}

func (s *benchmarkRangeOnlyStore) Append(
	sessionID string,
	event protocol.EventRecord,
) error {
	return s.file.Append(sessionID, event)
}

func (s *benchmarkRangeOnlyStore) Load(
	sessionID string,
) ([]protocol.EventRecord, error) {
	s.loadCalls++
	return s.file.Load(sessionID)
}

func (s *benchmarkRangeOnlyStore) ListSessions() ([]string, error) {
	return s.file.ListSessions()
}

func (s *benchmarkRangeOnlyStore) SaveSnapshot(
	sessionID string,
	snapshot protocol.Snapshot,
) error {
	return s.file.SaveSnapshot(sessionID, snapshot)
}

func (s *benchmarkRangeOnlyStore) Head(
	sessionID string,
) (rinruntime.EventAnchor, error) {
	s.headCalls++
	return s.file.Head(sessionID)
}

func (s *benchmarkRangeOnlyStore) LoadRange(
	sessionID string,
	afterRevision uint64,
	throughRevision uint64,
	limit int,
) (rinruntime.EventPage, error) {
	s.rangeRequests = append(s.rangeRequests, benchmarkRangeRequest{
		sessionID:       sessionID,
		afterRevision:   afterRevision,
		throughRevision: throughRevision,
		limit:           limit,
	})
	return s.file.LoadRange(sessionID, afterRevision, throughRevision, limit)
}

func (s *benchmarkRangeOnlyStore) Close() error {
	return s.file.Close()
}

func buildLongFileSessionFixture(b *testing.B) longSessionBenchmarkFixture {
	b.Helper()
	memory := store.NewMemory()
	engine, err := rinruntime.Open(memory, policy.Deterministic{})
	if err != nil {
		b.Fatal(err)
	}
	if _, err := engine.CreateSession(longSessionCreateRequest()); err != nil {
		b.Fatal(err)
	}
	for revision := uint64(2); revision <= benchmarkLongSessionRevision; revision++ {
		state := "awake"
		if revision%2 == 0 {
			state = "dormant"
		}
		_, err := engine.SetActorActivity(protocol.SetActorActivityRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       benchmarkLongSessionID,
			RequestID:       fmt.Sprintf("activity.%04d", revision),
			Tick:            int64(revision - 1),
			Updates: []protocol.ActorActivityUpdate{{
				ActorID:  "npc.benchmark",
				RegionID: "region.benchmark",
				State:    state,
				Reason:   "long session benchmark",
			}},
		})
		if err != nil {
			b.Fatalf("create revision %d: %v", revision, err)
		}
	}
	currentState, err := engine.State(protocol.SessionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       benchmarkLongSessionID,
	})
	if err != nil {
		b.Fatal(err)
	}
	replaySnapshot, err := engine.Replay(protocol.ReplayRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       benchmarkLongSessionID,
		Revision:        benchmarkReplayRevision,
	})
	if err != nil {
		b.Fatal(err)
	}

	events, err := memory.Load(benchmarkLongSessionID)
	if err != nil {
		b.Fatal(err)
	}
	if len(events) != int(benchmarkLongSessionRevision) {
		b.Fatalf("fixture event count = %d", len(events))
	}
	latestCheckpoint := waitForBenchmarkCheckpoint(
		b,
		memory,
		benchmarkLongSessionRevision,
		512,
	)
	previousCheckpoint, err := memory.LoadCheckpoint(
		benchmarkLongSessionID,
		latestCheckpoint.Revision-1,
	)
	if err != nil {
		b.Fatal(err)
	}
	replayCheckpoint, err := memory.LoadCheckpoint(
		benchmarkLongSessionID,
		benchmarkReplayRevision,
	)
	if err != nil {
		b.Fatal(err)
	}
	if replayCheckpoint.Revision > benchmarkReplayRevision ||
		latestCheckpoint.Revision >= benchmarkLongSessionRevision {
		b.Fatalf(
			"fixture checkpoints do not leave bounded replay tails: replay=%d latest=%d",
			replayCheckpoint.Revision,
			latestCheckpoint.Revision,
		)
	}
	if replayCheckpoint.Revision != latestCheckpoint.Revision ||
		replayCheckpoint.Checksum != latestCheckpoint.Checksum {
		b.Fatalf(
			"LoadCheckpoint(%d) selected revision %d, want latest retained %d",
			benchmarkReplayRevision,
			replayCheckpoint.Revision,
			latestCheckpoint.Revision,
		)
	}

	root := b.TempDir()
	fileStore, err := store.OpenFile(root)
	if err != nil {
		b.Fatal(err)
	}
	if err := fileStore.Create(benchmarkLongSessionID, events[0]); err != nil {
		_ = fileStore.Close()
		b.Fatal(err)
	}
	for _, event := range events[1:] {
		if err := fileStore.Append(benchmarkLongSessionID, event); err != nil {
			_ = fileStore.Close()
			b.Fatalf("persist revision %d: %v", event.Sequence, err)
		}
	}
	uniqueCheckpoints := make(map[string]rinruntime.Checkpoint)
	for _, checkpoint := range []rinruntime.Checkpoint{
		previousCheckpoint,
		replayCheckpoint,
		latestCheckpoint,
	} {
		uniqueCheckpoints[fmt.Sprintf(
			"%d:%s",
			checkpoint.Revision,
			checkpoint.Checksum,
		)] = checkpoint
	}
	for _, checkpoint := range uniqueCheckpoints {
		if err := fileStore.SaveCheckpoint(benchmarkLongSessionID, checkpoint); err != nil {
			_ = fileStore.Close()
			b.Fatalf("persist checkpoint %d: %v", checkpoint.Revision, err)
		}
	}
	if err := fileStore.Close(); err != nil {
		b.Fatal(err)
	}

	sessionDirectory := filepath.Join(root, "sessions", benchmarkLongSessionID)
	eventInfo, err := os.Stat(filepath.Join(sessionDirectory, "events.jsonl"))
	if err != nil {
		b.Fatal(err)
	}
	indexInfo, err := os.Stat(filepath.Join(sessionDirectory, "events.idx"))
	if err != nil {
		b.Fatal(err)
	}
	if indexInfo.Size() == 0 {
		b.Fatal("fixture events.idx is empty")
	}
	checkpoints, err := filepath.Glob(filepath.Join(sessionDirectory, "checkpoint-*.json"))
	if err != nil {
		b.Fatal(err)
	}
	if len(checkpoints) < 2 {
		b.Fatalf("fixture checkpoint count = %d", len(checkpoints))
	}
	var checkpointBytes int64
	for _, path := range checkpoints {
		info, statErr := os.Stat(path)
		if statErr != nil {
			b.Fatal(statErr)
		}
		checkpointBytes += info.Size()
	}
	return longSessionBenchmarkFixture{
		root:                     root,
		currentState:             currentState,
		replaySnapshot:           replaySnapshot,
		latestCheckpointRevision: latestCheckpoint.Revision,
		replayCheckpointRevision: replayCheckpoint.Revision,
		eventLogBytes:            eventInfo.Size(),
		eventIndexBytes:          indexInfo.Size(),
		checkpointBytes:          checkpointBytes,
	}
}

func waitForBenchmarkCheckpoint(
	b *testing.B,
	memory *store.Memory,
	atOrBeforeRevision uint64,
	minimumRevision uint64,
) rinruntime.Checkpoint {
	b.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		checkpoint, err := memory.LoadCheckpoint(
			benchmarkLongSessionID,
			atOrBeforeRevision,
		)
		if err == nil && checkpoint.Revision >= minimumRevision {
			return checkpoint
		}
		if err != nil && !errors.Is(err, rinruntime.ErrNotFound) {
			b.Fatal(err)
		}
		if time.Now().After(deadline) {
			if err != nil {
				b.Fatalf("wait for benchmark checkpoint: %v", err)
			}
			b.Fatalf(
				"benchmark checkpoint revision = %d, want at least %d",
				checkpoint.Revision,
				minimumRevision,
			)
		}
		time.Sleep(time.Millisecond)
	}
}

func longSessionCreateRequest() protocol.CreateSessionRequest {
	return protocol.CreateSessionRequest{
		ProtocolVersion: protocol.Version,
		RequestID:       "create.benchmark-long",
		SessionID:       benchmarkLongSessionID,
		Binding: protocol.Binding{
			GameID:         "game.benchmark",
			ContentID:      "content.benchmark",
			ContentVersion: "1.0.0",
			ContentHash:    "sha256-benchmark",
		},
		Seed:     42,
		Features: []string{protocol.FeatureActorActivity},
		Actors: []protocol.ActorSeed{{
			ID:              "npc.benchmark",
			Kind:            "npc",
			DisplayName:     "Benchmark Actor",
			ThinkEveryTicks: 5,
			Enabled:         true,
		}},
	}
}

func openCountedFileEngine(
	tb testing.TB,
	root string,
) (*benchmarkReadCountingStore, *rinruntime.Engine) {
	tb.Helper()
	fileStore, err := store.OpenFile(root)
	if err != nil {
		tb.Fatal(err)
	}
	counted := &benchmarkReadCountingStore{File: fileStore}
	engine, err := rinruntime.Open(counted, policy.Deterministic{})
	if err != nil {
		_ = counted.Close()
		tb.Fatal(err)
	}
	return counted, engine
}

func openLoadedCountedFileEngine(
	tb testing.TB,
	root string,
	expected protocol.SessionState,
) (*benchmarkReadCountingStore, *rinruntime.Engine) {
	tb.Helper()
	counted, engine := openCountedFileEngine(tb, root)
	state, err := engine.State(protocol.SessionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       benchmarkLongSessionID,
	})
	if err != nil {
		_ = counted.Close()
		tb.Fatal(err)
	}
	if !reflect.DeepEqual(state, expected) {
		_ = counted.Close()
		tb.Fatalf(
			"loaded fixture differs: revision=%d head=%s",
			state.Revision,
			state.HeadHash,
		)
	}
	return counted, engine
}

func openRangeOnlyFileEngine(
	tb testing.TB,
	root string,
) (*benchmarkRangeOnlyStore, *rinruntime.Engine) {
	tb.Helper()
	fileStore, err := store.OpenFile(root)
	if err != nil {
		tb.Fatal(err)
	}
	counted := &benchmarkRangeOnlyStore{file: fileStore}
	engine, err := rinruntime.Open(counted, policy.Deterministic{})
	if err != nil {
		_ = counted.Close()
		tb.Fatal(err)
	}
	return counted, engine
}

func openLoadedRangeOnlyFileEngine(
	tb testing.TB,
	root string,
	expected protocol.SessionState,
) (*benchmarkRangeOnlyStore, *rinruntime.Engine) {
	tb.Helper()
	counted, engine := openRangeOnlyFileEngine(tb, root)
	state, err := engine.State(protocol.SessionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       benchmarkLongSessionID,
	})
	if err != nil {
		_ = counted.Close()
		tb.Fatal(err)
	}
	if !reflect.DeepEqual(state, expected) {
		_ = counted.Close()
		tb.Fatalf(
			"loaded genesis fixture differs: revision=%d head=%s",
			state.Revision,
			state.HeadHash,
		)
	}
	return counted, engine
}

func reportLongSessionFixtureMetrics(
	b *testing.B,
	fixture longSessionBenchmarkFixture,
) {
	b.Helper()
	b.ReportMetric(float64(fixture.eventLogBytes), "event-log-bytes")
	b.ReportMetric(float64(fixture.eventIndexBytes), "event-index-bytes")
	b.ReportMetric(float64(fixture.checkpointBytes), "checkpoint-bytes")
}

func assertBoundedRangeRequests(
	tb testing.TB,
	requests []benchmarkRangeRequest,
	minimum int,
	maximum int,
	minimumAfter uint64,
	maximumThrough uint64,
	maximumLimit int,
) {
	tb.Helper()
	if len(requests) < minimum || len(requests) > maximum {
		tb.Fatalf(
			"LoadRange calls = %d, want between %d and %d",
			len(requests),
			minimum,
			maximum,
		)
	}
	for index, request := range requests {
		if request.sessionID != benchmarkLongSessionID ||
			request.afterRevision < minimumAfter ||
			request.afterRevision >= request.throughRevision ||
			request.throughRevision > maximumThrough ||
			request.limit < 1 ||
			request.limit > maximumLimit {
			tb.Fatalf("LoadRange call %d is not bounded as expected: %+v", index, request)
		}
	}
}

func assertGenesisRangeStarts(
	tb testing.TB,
	requests []benchmarkRangeRequest,
	expected int,
) {
	tb.Helper()
	starts := 0
	for _, request := range requests {
		if request.afterRevision == 0 {
			starts++
		}
	}
	if starts != expected {
		tb.Fatalf("genesis LoadRange starts = %d, want %d", starts, expected)
	}
}
