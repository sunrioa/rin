package runtime_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sunrioa/rin/policy"
	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
	"github.com/sunrioa/rin/store"
)

func TestIdentifierHistoryObserveRetryReturnsOriginalResult(t *testing.T) {
	const sessionID = "session.identifier-observe-retry"
	engine := newEngine(t, store.NewMemory(), policy.Deterministic{})
	if _, err := engine.CreateSession(createRequest(sessionID)); err != nil {
		t.Fatal(err)
	}

	original := identifierObserveRequest(
		sessionID,
		"observe.identifier-original",
		"event.identifier-original",
		1,
	)
	first, err := engine.Observe(original)
	if err != nil {
		t.Fatal(err)
	}
	advanced, err := engine.Observe(identifierObserveRequest(
		sessionID,
		"observe.identifier-advance",
		"event.identifier-advance",
		2,
	))
	if err != nil {
		t.Fatal(err)
	}
	if advanced.Revision <= first.Revision || advanced.HeadHash == first.HeadHash {
		t.Fatalf("second observation did not advance state: first=%+v advanced=%+v", first, advanced)
	}

	repeated, err := engine.Observe(original)
	if err != nil {
		t.Fatal(err)
	}
	if !repeated.Duplicate ||
		repeated.Revision != first.Revision ||
		repeated.HeadHash != first.HeadHash {
		t.Fatalf("exact retry did not return the first durable result: first=%+v retry=%+v", first, repeated)
	}

	altered := original
	altered.Summary = "The same request id must not identify altered observation data."
	if _, err := engine.Observe(altered); err == nil {
		t.Fatal("altered retry unexpectedly succeeded")
	} else {
		assertIdentifierRequestConflict(t, err)
	}
	assertIdentifierStateHead(t, engine, sessionID, advanced)
}

func TestIdentifierHistorySurvivesReceiptAndMemoryEviction(t *testing.T) {
	const sessionID = "session.identifier-retention"
	eventStore := store.NewMemory()
	engine := newEngine(t, eventStore, policy.Deterministic{})
	create := createRequest(sessionID)
	create.Features = append(create.Features, protocol.FeatureActorActivity)
	if _, err := engine.CreateSession(create); err != nil {
		t.Fatal(err)
	}

	oldest := identifierObserveRequest(
		sessionID,
		"observe.identifier-oldest",
		"event.identifier-oldest",
		1,
	)
	first, err := engine.Observe(oldest)
	if err != nil {
		t.Fatal(err)
	}

	// Activity mutations are deliberately used for the receipt churn: they keep
	// the fixture small while forcing the oldest receipt beyond the 1,024-entry
	// public projection.
	var latest protocol.MutationResult
	for index := 0; index < 1025; index++ {
		latest, err = engine.SetActorActivity(identifierActivityRequest(
			sessionID,
			fmt.Sprintf("activity.identifier-churn.%04d", index),
			int64(index+2),
		))
		if err != nil {
			t.Fatalf("activity churn %d: %v", index, err)
		}
	}
	state, err := engine.State(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	if _, retained := state.Receipts[oldest.RequestID]; retained {
		t.Fatal("test setup did not evict the oldest request receipt")
	}

	repeated, err := engine.Observe(oldest)
	if err != nil {
		t.Fatal(err)
	}
	if !repeated.Duplicate ||
		repeated.Revision != first.Revision ||
		repeated.HeadHash != first.HeadHash {
		t.Fatalf("evicted exact request was not permanently recognized: first=%+v retry=%+v", first, repeated)
	}
	altered := oldest
	altered.Summary = "An altered payload must remain a conflict after receipt eviction."
	if _, err := engine.Observe(altered); err == nil {
		t.Fatal("altered evicted request unexpectedly succeeded")
	} else {
		assertIdentifierRequestConflict(t, err)
	}
	assertIdentifierStateHead(t, engine, sessionID, latest)

	// The first observation remains as detailed memory after activity-only
	// churn. Add exactly enough small observations to push it beyond the
	// 128-memory projection, without relying on Summary or Belief retention.
	for index := 0; index < 128; index++ {
		_, err = engine.Observe(identifierObserveRequest(
			sessionID,
			fmt.Sprintf("observe.identifier-memory-churn.%03d", index),
			fmt.Sprintf("event.identifier-memory-churn.%03d", index),
			int64(1027+index),
		))
		if err != nil {
			t.Fatalf("memory churn %d: %v", index, err)
		}
	}
	state, err = engine.State(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	for _, memory := range state.Actors["npc.mira"].Memories {
		if memory.EventID == oldest.EventID {
			t.Fatal("test setup did not evict the oldest observation memory")
		}
	}
	if _, retained := state.Receipts[oldest.RequestID]; retained {
		t.Fatal("oldest observation receipt unexpectedly returned to the public projection")
	}
	beforeReuse := protocol.MutationResult{
		SessionID: sessionID,
		Revision:  state.Revision,
		HeadHash:  state.HeadHash,
	}

	reuse := identifierObserveRequest(
		sessionID,
		"observe.identifier-reuse-old-event",
		oldest.EventID,
		state.Tick,
	)
	if _, err := engine.Observe(reuse); err == nil {
		t.Fatal("evicted historical event id was reused")
	} else if rinruntime.ErrorCode(err) != "event_exists" ||
		!errors.Is(err, rinruntime.ErrConflict) {
		t.Fatalf("historical event id error = %v, want event_exists conflict", err)
	}
	assertIdentifierStateHead(t, engine, sessionID, beforeReuse)
}

func TestIdentifierHistoryRestoreKeepsFutureIdentifiers(t *testing.T) {
	const sessionID = "session.identifier-restore"
	engine := newEngine(t, store.NewMemory(), policy.Deterministic{})
	if _, err := engine.CreateSession(createRequest(sessionID)); err != nil {
		t.Fatal(err)
	}
	oldSnapshot, err := engine.Snapshot(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}

	futureRequest := identifierObserveRequest(
		sessionID,
		"observe.identifier-future",
		"event.identifier-future",
		1,
	)
	futureResult, err := engine.Observe(futureRequest)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := engine.Restore(protocol.RestoreRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
		RequestID:       "restore.identifier-old-snapshot",
		Snapshot:        oldSnapshot,
	})
	if err != nil {
		t.Fatal(err)
	}

	repeated, err := engine.Observe(futureRequest)
	if err != nil {
		t.Fatal(err)
	}
	if !repeated.Duplicate ||
		repeated.Revision != futureResult.Revision ||
		repeated.HeadHash != futureResult.HeadHash {
		t.Fatalf("restore forgot the future request identity: first=%+v retry=%+v", futureResult, repeated)
	}
	altered := futureRequest
	altered.Summary = "Restore must not free a future request id for altered data."
	if _, err := engine.Observe(altered); err == nil {
		t.Fatal("restore freed a future request id")
	} else {
		assertIdentifierRequestConflict(t, err)
	}

	eventReuse := identifierObserveRequest(
		sessionID,
		"observe.identifier-future-event-reuse",
		futureRequest.EventID,
		1,
	)
	if _, err := engine.Observe(eventReuse); err == nil {
		t.Fatal("restore freed a future event id")
	} else if rinruntime.ErrorCode(err) != "event_exists" ||
		!errors.Is(err, rinruntime.ErrConflict) {
		t.Fatalf("restored future event id error = %v, want event_exists conflict", err)
	}
	assertIdentifierStateHead(t, engine, sessionID, restored)
}

func TestSnapshotIdentifierHistoryTamperIsRejected(t *testing.T) {
	const sessionID = "session.identifier-snapshot-tamper"
	engine := newEngine(t, store.NewMemory(), policy.Deterministic{})
	if _, err := engine.CreateSession(createRequest(sessionID)); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Observe(identifierObserveRequest(
		sessionID,
		"observe.identifier-snapshot",
		"event.identifier-snapshot",
		1,
	)); err != nil {
		t.Fatal(err)
	}
	proposal, _, err := engine.Propose(
		context.Background(),
		proposeRequest(sessionID, "propose.identifier-snapshot", 1, nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := engine.Snapshot(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.IdentifierHistory == nil || snapshot.IdentifierHistoryHash == "" {
		t.Fatalf("snapshot omitted durable identifier history: %+v", snapshot)
	}

	t.Run("history content", func(t *testing.T) {
		tampered := cloneIdentifierSnapshot(t, snapshot)
		tampered.IdentifierHistory.Requests["request.identifier-tampered"] = protocol.RequestIdentity{
			Ambiguous: true,
		}
		assertInvalidIdentifierSnapshot(t, tampered)
	})
	t.Run("history hash", func(t *testing.T) {
		tampered := cloneIdentifierSnapshot(t, snapshot)
		tampered.IdentifierHistoryHash = strings.Repeat("0", 64)
		if tampered.IdentifierHistoryHash == snapshot.IdentifierHistoryHash {
			tampered.IdentifierHistoryHash = strings.Repeat("1", 64)
		}
		assertInvalidIdentifierSnapshot(t, tampered)
	})
	t.Run("retained typed request coverage", func(t *testing.T) {
		tampered := cloneIdentifierSnapshot(t, snapshot)
		delete(tampered.IdentifierHistory.Requests, proposal.RequestID)
		payload, err := json.Marshal(tampered.IdentifierHistory)
		if err != nil {
			t.Fatal(err)
		}
		tampered.IdentifierHistoryHash = fmt.Sprintf("%x", sha256.Sum256(payload))
		if err := rinruntime.ValidateSnapshot(tampered); err == nil ||
			rinruntime.ErrorCode(err) != "invalid_snapshot" {
			t.Fatalf("missing retained typed request was not rejected: %v", err)
		}
	})
}

func TestSnapshotIdentifierHistoryRejectsRehashedCrossProjectionTampering(t *testing.T) {
	const sessionID = "session.identifier-cross-projection"
	engine := newEngine(t, store.NewMemory(), policy.Deterministic{})
	create := twoActorWorldRequest(sessionID)
	if _, err := engine.CreateSession(create); err != nil {
		t.Fatal(err)
	}
	mira, _, err := engine.Propose(
		context.Background(),
		targetedProposalRequest(sessionID, "propose.identifier-cross-mira", "npc.mira"),
	)
	if err != nil {
		t.Fatal(err)
	}
	oren, _, err := engine.Propose(
		context.Background(),
		targetedProposalRequest(sessionID, "propose.identifier-cross-oren", "npc.oren"),
	)
	if err != nil {
		t.Fatal(err)
	}
	record, _, err := engine.Arbitrate(protocol.ArbitrateRequest{
		ProtocolVersion:    protocol.Version,
		SessionID:          sessionID,
		RequestID:          "arbitrate.identifier-cross",
		Tick:               0,
		ProposalIDs:        []string{mira.ID, oren.ID},
		ExclusiveTargetIDs: []string{"object.camera"},
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := engine.Snapshot(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(*protocol.Snapshot)
	}{
		{
			name: "receipt result revision",
			mutate: func(snapshot *protocol.Snapshot) {
				identity := snapshot.IdentifierHistory.Requests[create.RequestID]
				identity.ResultRevision++
				snapshot.IdentifierHistory.Requests[create.RequestID] = identity
			},
		},
		{
			name: "current result head",
			mutate: func(snapshot *protocol.Snapshot) {
				identity := snapshot.IdentifierHistory.Requests[record.RequestID]
				identity.ResultHeadHash = strings.Repeat("c", 64)
				snapshot.IdentifierHistory.Requests[record.RequestID] = identity
			},
		},
		{
			name: "proposal result",
			mutate: func(snapshot *protocol.Snapshot) {
				identity := snapshot.IdentifierHistory.Requests[mira.RequestID]
				identity.Proposal.Summary = "A structurally valid but forged proposal result."
				snapshot.IdentifierHistory.Requests[mira.RequestID] = identity
			},
		},
		{
			name: "arbitration result",
			mutate: func(snapshot *protocol.Snapshot) {
				identity := snapshot.IdentifierHistory.Requests[record.RequestID]
				identity.Arbitration.Decisions[0].Reason = "A structurally valid but forged arbitration decision."
				snapshot.IdentifierHistory.Requests[record.RequestID] = identity
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tampered := cloneIdentifierSnapshot(t, snapshot)
			test.mutate(&tampered)
			rehashIdentifierHistory(t, &tampered)
			assertInvalidCrossProjectionSnapshot(t, tampered)
		})
	}
}

func TestIdentifierHistoryRetrySurvivesOpen(t *testing.T) {
	const sessionID = "session.identifier-reopen"
	eventStore := store.NewMemory()
	engine := newEngine(t, eventStore, policy.Deterministic{})
	if _, err := engine.CreateSession(createRequest(sessionID)); err != nil {
		t.Fatal(err)
	}
	original := identifierObserveRequest(
		sessionID,
		"observe.identifier-reopen-original",
		"event.identifier-reopen-original",
		1,
	)
	first, err := engine.Observe(original)
	if err != nil {
		t.Fatal(err)
	}
	advanced, err := engine.Observe(identifierObserveRequest(
		sessionID,
		"observe.identifier-reopen-advance",
		"event.identifier-reopen-advance",
		2,
	))
	if err != nil {
		t.Fatal(err)
	}

	reopened := newEngine(t, eventStore, policy.Deterministic{})
	repeated, err := reopened.Observe(original)
	if err != nil {
		t.Fatal(err)
	}
	if !repeated.Duplicate ||
		repeated.Revision != first.Revision ||
		repeated.HeadHash != first.HeadHash {
		t.Fatalf("Open lost exact request history: first=%+v retry=%+v", first, repeated)
	}
	altered := original
	altered.Summary = "A restarted engine must reject altered retry data."
	if _, err := reopened.Observe(altered); err == nil {
		t.Fatal("Open forgot the altered-request conflict")
	} else {
		assertIdentifierRequestConflict(t, err)
	}
	assertIdentifierStateHead(t, reopened, sessionID, advanced)
}

func TestIdentifierHistoryReturnsEvictedArbitrationResult(t *testing.T) {
	const sessionID = "session.identifier-arbitration-eviction"
	eventStore := store.NewMemory()
	engine := newEngine(t, eventStore, policy.Deterministic{})
	create := twoActorWorldRequest(sessionID)
	if _, err := engine.CreateSession(create); err != nil {
		t.Fatal(err)
	}
	mira, _, err := engine.Propose(
		context.Background(),
		targetedProposalRequest(sessionID, "propose.identifier-mira", "npc.mira"),
	)
	if err != nil {
		t.Fatal(err)
	}
	oren, _, err := engine.Propose(
		context.Background(),
		targetedProposalRequest(sessionID, "propose.identifier-oren", "npc.oren"),
	)
	if err != nil {
		t.Fatal(err)
	}
	original := protocol.ArbitrateRequest{
		ProtocolVersion:    protocol.Version,
		SessionID:          sessionID,
		RequestID:          "arbitrate.identifier-oldest",
		Tick:               0,
		ProposalIDs:        []string{mira.ID, oren.ID},
		ExclusiveTargetIDs: []string{"object.camera"},
	}
	first, duplicate, err := engine.Arbitrate(original)
	if err != nil || duplicate {
		t.Fatalf("first arbitration: record=%+v duplicate=%v err=%v", first, duplicate, err)
	}
	for index := 0; index < 32; index++ {
		request := original
		request.RequestID = fmt.Sprintf("arbitrate.identifier-churn.%02d", index)
		if _, _, err := engine.Arbitrate(request); err != nil {
			t.Fatalf("arbitration churn %d: %v", index, err)
		}
	}
	state, err := engine.State(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range state.Arbitrations {
		if record.ID == first.ID {
			t.Fatal("test setup did not evict the first arbitration projection")
		}
	}

	repeated, duplicate, err := engine.Arbitrate(original)
	if err != nil || !duplicate || !reflect.DeepEqual(repeated, first) {
		t.Fatalf("evicted arbitration was not replayed: record=%+v duplicate=%v err=%v", repeated, duplicate, err)
	}
	altered := original
	altered.ExclusiveTargetIDs = nil
	if _, _, err := engine.Arbitrate(altered); err == nil {
		t.Fatal("altered evicted arbitration retry unexpectedly succeeded")
	} else {
		assertIdentifierRequestConflict(t, err)
	}

	reopened := newEngine(t, eventStore, policy.Deterministic{})
	replayed, duplicate, err := reopened.Arbitrate(original)
	if err != nil || !duplicate || !reflect.DeepEqual(replayed, first) {
		t.Fatalf("reopened arbitration was not replayed: record=%+v duplicate=%v err=%v", replayed, duplicate, err)
	}
}

func TestIdentifierHistoryReplayRetainsLaterLineageTombstones(t *testing.T) {
	const sessionID = "session.identifier-replay-tombstones"
	source := newEngine(t, store.NewMemory(), policy.Deterministic{})
	if _, err := source.CreateSession(createRequest(sessionID)); err != nil {
		t.Fatal(err)
	}
	future := identifierObserveRequest(
		sessionID,
		"observe.identifier-after-replay-point",
		"event.identifier-after-replay-point",
		1,
	)
	first, err := source.Observe(future)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := source.Replay(protocol.ReplayRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
		Revision:        1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if replayed.State.Revision != 1 ||
		replayed.IdentifierHistory == nil ||
		replayed.IdentifierHistory.Requests[future.RequestID].ResultRevision != first.Revision {
		t.Fatalf("replay snapshot omitted later lineage tombstone: %+v", replayed)
	}

	target := newEngine(t, store.NewMemory(), policy.Deterministic{})
	if _, err := target.Restore(protocol.RestoreRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
		RequestID:       "restore.identifier-replay-tombstones",
		Snapshot:        replayed,
	}); err != nil {
		t.Fatal(err)
	}
	repeated, err := target.Observe(future)
	if err != nil {
		t.Fatal(err)
	}
	if !repeated.Duplicate ||
		repeated.Revision != first.Revision ||
		repeated.HeadHash != first.HeadHash {
		t.Fatalf("fresh restore forgot replay tombstone: first=%+v repeated=%+v", first, repeated)
	}
	reuse := future
	reuse.RequestID = "observe.identifier-replay-event-reuse"
	if _, err := target.Observe(reuse); err == nil ||
		rinruntime.ErrorCode(err) != "event_exists" {
		t.Fatalf("fresh restore released later replay event id: %v", err)
	}
}

func TestLegacySnapshotImportsStickyPartialHistory(t *testing.T) {
	const sessionID = "session.identifier-legacy-partial"
	source := newEngine(t, store.NewMemory(), policy.Deterministic{})
	if _, err := source.CreateSession(createRequest(sessionID)); err != nil {
		t.Fatal(err)
	}
	legacyRequest := identifierObserveRequest(
		sessionID,
		"observe.identifier-legacy-retained",
		"event.identifier-legacy-retained",
		1,
	)
	if _, err := source.Observe(legacyRequest); err != nil {
		t.Fatal(err)
	}
	legacy, err := source.Snapshot(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	legacy.IdentifierHistory = nil
	legacy.IdentifierHistoryHash = ""
	if err := rinruntime.ValidateSnapshot(legacy); err != nil {
		t.Fatalf("legacy state-only snapshot was rejected: %v", err)
	}

	target := newEngine(t, store.NewMemory(), policy.Deterministic{})
	if _, err := target.Restore(protocol.RestoreRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
		RequestID:       "restore.identifier-legacy-partial",
		Snapshot:        legacy,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := target.Observe(legacyRequest); err == nil {
		t.Fatal("unverifiable retained legacy request was guessed to be an exact retry")
	} else {
		assertIdentifierRequestConflict(t, err)
	}
	reuse := legacyRequest
	reuse.RequestID = "observe.identifier-legacy-event-reuse"
	if _, err := target.Observe(reuse); err == nil ||
		rinruntime.ErrorCode(err) != "event_exists" {
		t.Fatalf("legacy retained event id was released: %v", err)
	}

	newRequest := identifierObserveRequest(
		sessionID,
		"observe.identifier-after-legacy-import",
		"event.identifier-after-legacy-import",
		2,
	)
	first, err := target.Observe(newRequest)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := target.Observe(newRequest)
	if err != nil || !repeated.Duplicate ||
		repeated.Revision != first.Revision ||
		repeated.HeadHash != first.HeadHash {
		t.Fatalf("post-import request was not durably idempotent: first=%+v repeated=%+v err=%v", first, repeated, err)
	}
	snapshot, err := target.Snapshot(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.IdentifierHistory == nil || snapshot.IdentifierHistory.CoverageComplete {
		t.Fatalf("legacy partial coverage was incorrectly promoted: %+v", snapshot.IdentifierHistory)
	}

	t.Run("overlap remains snapshotable", func(t *testing.T) {
		overlap := newEngine(t, store.NewMemory(), policy.Deterministic{})
		overlapSessionID := "session.identifier-legacy-overlap"
		if _, err := overlap.CreateSession(createRequest(overlapSessionID)); err != nil {
			t.Fatal(err)
		}
		if _, err := overlap.Observe(identifierObserveRequest(
			overlapSessionID,
			"observe.identifier-legacy-overlap",
			"event.identifier-legacy-overlap",
			1,
		)); err != nil {
			t.Fatal(err)
		}
		stateOnly, err := overlap.Snapshot(sessionRequest(overlapSessionID))
		if err != nil {
			t.Fatal(err)
		}
		stateOnly.IdentifierHistory = nil
		stateOnly.IdentifierHistoryHash = ""
		if _, err := overlap.Restore(protocol.RestoreRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       overlapSessionID,
			RequestID:       "restore.identifier-legacy-overlap",
			Snapshot:        stateOnly,
		}); err != nil {
			t.Fatal(err)
		}
		roundTrip, err := overlap.Snapshot(sessionRequest(overlapSessionID))
		if err != nil {
			t.Fatalf("overlapping partial restore poisoned future snapshots: %v", err)
		}
		if roundTrip.IdentifierHistory == nil || roundTrip.IdentifierHistory.CoverageComplete {
			t.Fatalf("overlap incorrectly promoted partial coverage: %+v", roundTrip.IdentifierHistory)
		}
	})
}

func TestLegacySnapshotSeedsTypedRequestIDsAfterReceiptEviction(t *testing.T) {
	const sessionID = "session.identifier-legacy-typed-tombstones"
	source := newEngine(t, store.NewMemory(), policy.Deterministic{})
	create := twoActorWorldRequest(sessionID)
	create.Features = append(create.Features, protocol.FeatureActorActivity)
	if _, err := source.CreateSession(create); err != nil {
		t.Fatal(err)
	}
	proposalRequest := targetedProposalRequest(
		sessionID,
		"propose.identifier-legacy-without-receipt",
		"npc.mira",
	)
	mira, _, err := source.Propose(context.Background(), proposalRequest)
	if err != nil {
		t.Fatal(err)
	}
	oren, _, err := source.Propose(
		context.Background(),
		targetedProposalRequest(
			sessionID,
			"propose.identifier-legacy-second",
			"npc.oren",
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	arbitrationRequest := protocol.ArbitrateRequest{
		ProtocolVersion:    protocol.Version,
		SessionID:          sessionID,
		RequestID:          "arbitrate.identifier-legacy-without-receipt",
		Tick:               0,
		ProposalIDs:        []string{mira.ID, oren.ID},
		ExclusiveTargetIDs: []string{"object.camera"},
	}
	if _, _, err := source.Arbitrate(arbitrationRequest); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 1025; index++ {
		if _, err := source.SetActorActivity(identifierActivityRequest(
			sessionID,
			fmt.Sprintf("activity.identifier-legacy-typed-churn.%04d", index),
			int64(index+1),
		)); err != nil {
			t.Fatalf("activity churn %d: %v", index, err)
		}
	}
	state, err := source.State(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	if _, retained := state.Receipts[proposalRequest.RequestID]; retained {
		t.Fatal("test setup retained the proposal receipt")
	}
	if _, retained := state.Receipts[arbitrationRequest.RequestID]; retained {
		t.Fatal("test setup retained the arbitration receipt")
	}
	legacy, err := rinruntime.SnapshotOf(state)
	if err != nil {
		t.Fatal(err)
	}
	legacy.IdentifierHistory = nil
	legacy.IdentifierHistoryHash = ""

	target := newEngine(t, store.NewMemory(), policy.Deterministic{})
	if _, err := target.Restore(protocol.RestoreRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
		RequestID:       "restore.identifier-legacy-typed-tombstones",
		Snapshot:        legacy,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := target.Propose(context.Background(), proposalRequest); err == nil {
		t.Fatal("legacy proposal request id was released after receipt eviction")
	} else {
		assertIdentifierRequestConflict(t, err)
	}
	if _, _, err := target.Arbitrate(arbitrationRequest); err == nil {
		t.Fatal("legacy arbitration request id was released after receipt eviction")
	} else {
		assertIdentifierRequestConflict(t, err)
	}
}

func TestIdentifierHistoryRejectsAlteredPayloadForEveryMutation(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		const sessionID = "session.identifier-altered-create"
		engine := newEngine(t, store.NewMemory(), policy.Deterministic{})
		request := createRequest(sessionID)
		if _, err := engine.CreateSession(request); err != nil {
			t.Fatal(err)
		}
		altered := request
		altered.Seed++
		if _, err := engine.CreateSession(altered); err == nil {
			t.Fatal("altered create unexpectedly succeeded")
		} else {
			assertIdentifierRequestConflict(t, err)
		}
	})

	t.Run("activity", func(t *testing.T) {
		const sessionID = "session.identifier-altered-activity"
		engine := newEngine(t, store.NewMemory(), policy.Deterministic{})
		create := createRequest(sessionID)
		create.Features = append(create.Features, protocol.FeatureActorActivity)
		if _, err := engine.CreateSession(create); err != nil {
			t.Fatal(err)
		}
		request := identifierActivityRequest(sessionID, "activity.identifier-altered", 1)
		first, err := engine.SetActorActivity(request)
		if err != nil {
			t.Fatal(err)
		}
		altered := request
		altered.Updates = append([]protocol.ActorActivityUpdate(nil), request.Updates...)
		altered.Updates[0].Reason = "A different activity reason."
		if _, err := engine.SetActorActivity(altered); err == nil {
			t.Fatal("altered activity unexpectedly succeeded")
		} else {
			assertIdentifierRequestConflict(t, err)
		}
		repeated, err := engine.SetActorActivity(request)
		if err != nil || !repeated.Duplicate ||
			repeated.Revision != first.Revision ||
			repeated.HeadHash != first.HeadHash {
			t.Fatalf("activity exact retry mismatch: first=%+v repeated=%+v err=%v", first, repeated, err)
		}
	})

	t.Run("commit", func(t *testing.T) {
		const sessionID = "session.identifier-altered-commit"
		engine := newEngine(t, store.NewMemory(), policy.Deterministic{})
		if _, err := engine.CreateSession(createRequest(sessionID)); err != nil {
			t.Fatal(err)
		}
		proposal, _, err := engine.Propose(
			context.Background(),
			proposeRequest(sessionID, "propose.identifier-altered-commit", 0, nil),
		)
		if err != nil {
			t.Fatal(err)
		}
		request := protocol.CommitRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       sessionID,
			RequestID:       "commit.identifier-altered",
			ProposalID:      proposal.ID,
			EventID:         "event.identifier-altered-commit",
			Tick:            0,
			Accepted:        false,
			Outcome:         "The game rejected the action.",
		}
		first, err := engine.Commit(request)
		if err != nil {
			t.Fatal(err)
		}
		altered := request
		altered.Outcome = "A different rejection outcome."
		if _, err := engine.Commit(altered); err == nil {
			t.Fatal("altered commit unexpectedly succeeded")
		} else {
			assertIdentifierRequestConflict(t, err)
		}
		repeated, err := engine.Commit(request)
		if err != nil || !repeated.Duplicate ||
			repeated.Revision != first.Revision ||
			repeated.HeadHash != first.HeadHash {
			t.Fatalf("commit exact retry mismatch: first=%+v repeated=%+v err=%v", first, repeated, err)
		}
	})

	t.Run("batch", func(t *testing.T) {
		const sessionID = "session.identifier-altered-batch"
		engine := newEngine(t, store.NewMemory(), policy.Deterministic{})
		create := twoActorWorldRequest(sessionID)
		if _, err := engine.CreateSession(create); err != nil {
			t.Fatal(err)
		}
		mira, _, err := engine.Propose(
			context.Background(),
			targetedProposalRequest(sessionID, "propose.identifier-altered-batch-mira", "npc.mira"),
		)
		if err != nil {
			t.Fatal(err)
		}
		oren, _, err := engine.Propose(
			context.Background(),
			targetedProposalRequest(sessionID, "propose.identifier-altered-batch-oren", "npc.oren"),
		)
		if err != nil {
			t.Fatal(err)
		}
		request := protocol.BatchCommitRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       sessionID,
			RequestID:       "batch.identifier-altered",
			Tick:            0,
			Items: []protocol.CommitItem{
				{ProposalID: mira.ID, EventID: "event.identifier-altered-batch-mira", Accepted: false},
				{ProposalID: oren.ID, EventID: "event.identifier-altered-batch-oren", Accepted: false},
			},
		}
		first, err := engine.CommitBatch(request)
		if err != nil {
			t.Fatal(err)
		}
		altered := request
		altered.Items = append([]protocol.CommitItem(nil), request.Items...)
		altered.Items[0].Outcome = "A different batch outcome."
		if _, err := engine.CommitBatch(altered); err == nil {
			t.Fatal("altered batch unexpectedly succeeded")
		} else {
			assertIdentifierRequestConflict(t, err)
		}
		repeated, err := engine.CommitBatch(request)
		if err != nil || !repeated.Duplicate ||
			repeated.Revision != first.Revision ||
			repeated.HeadHash != first.HeadHash {
			t.Fatalf("batch exact retry mismatch: first=%+v repeated=%+v err=%v", first, repeated, err)
		}
	})

	t.Run("restore", func(t *testing.T) {
		const sessionID = "session.identifier-altered-restore"
		source := newEngine(t, store.NewMemory(), policy.Deterministic{})
		if _, err := source.CreateSession(createRequest(sessionID)); err != nil {
			t.Fatal(err)
		}
		firstSnapshot, err := source.Snapshot(sessionRequest(sessionID))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := source.Observe(identifierObserveRequest(
			sessionID,
			"observe.identifier-before-second-snapshot",
			"event.identifier-before-second-snapshot",
			1,
		)); err != nil {
			t.Fatal(err)
		}
		secondSnapshot, err := source.Snapshot(sessionRequest(sessionID))
		if err != nil {
			t.Fatal(err)
		}
		target := newEngine(t, store.NewMemory(), policy.Deterministic{})
		request := protocol.RestoreRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       sessionID,
			RequestID:       "restore.identifier-altered",
			Snapshot:        firstSnapshot,
		}
		first, err := target.Restore(request)
		if err != nil {
			t.Fatal(err)
		}
		altered := request
		altered.Snapshot = secondSnapshot
		if _, err := target.Restore(altered); err == nil {
			t.Fatal("altered restore unexpectedly succeeded")
		} else {
			assertIdentifierRequestConflict(t, err)
		}
		repeated, err := target.Restore(request)
		if err != nil || !repeated.Duplicate ||
			repeated.Revision != first.Revision ||
			repeated.HeadHash != first.HeadHash {
			t.Fatalf("restore exact retry mismatch: first=%+v repeated=%+v err=%v", first, repeated, err)
		}
	})
}

func TestProposeRejectsRestoreEvenWhenWorldRevisionRepeats(t *testing.T) {
	const sessionID = "session.identifier-propose-restore-epoch"
	blocking := &firstCallBlockingPolicy{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	engine := newEngine(t, store.NewMemory(), blocking)
	create := createRequest(sessionID)
	create.Features = append(create.Features, protocol.FeatureArbitration)
	if _, err := engine.CreateSession(create); err != nil {
		t.Fatal(err)
	}
	oldSnapshot, err := engine.Snapshot(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Observe(identifierObserveRequest(
		sessionID,
		"observe.identifier-before-blocked-propose",
		"event.identifier-before-blocked-propose",
		1,
	)); err != nil {
		t.Fatal(err)
	}
	before, err := engine.State(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	if before.WorldRevision != 2 {
		t.Fatalf("test setup world revision = %d, want 2", before.WorldRevision)
	}

	result := make(chan error, 1)
	go func() {
		_, _, proposeErr := engine.Propose(
			context.Background(),
			proposeRequest(sessionID, "propose.identifier-across-restore", 1, nil),
		)
		result <- proposeErr
	}()
	select {
	case <-blocking.started:
	case <-time.After(time.Second):
		t.Fatal("policy did not block")
	}
	if _, err := engine.Restore(protocol.RestoreRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
		RequestID:       "restore.identifier-during-propose",
		Snapshot:        oldSnapshot,
	}); err != nil {
		t.Fatal(err)
	}
	afterRestore, err := engine.State(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	if afterRestore.WorldRevision != before.WorldRevision {
		t.Fatalf(
			"test no longer exercises repeated world revision: before=%d after=%d",
			before.WorldRevision,
			afterRestore.WorldRevision,
		)
	}
	close(blocking.release)
	select {
	case err := <-result:
		if rinruntime.ErrorCode(err) != "state_changed" {
			t.Fatalf("proposal crossed restore generation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked proposal did not finish")
	}
}

func TestIdentifierHistoryFileStoreRestart(t *testing.T) {
	const sessionID = "session.identifier-file-restart"
	directory := t.TempDir()
	fileStore, err := store.OpenFile(directory)
	if err != nil {
		t.Fatal(err)
	}
	engine := newEngine(t, fileStore, policy.Deterministic{})
	if _, err := engine.CreateSession(createRequest(sessionID)); err != nil {
		t.Fatal(err)
	}
	original := identifierObserveRequest(
		sessionID,
		"observe.identifier-file-original",
		"event.identifier-file-original",
		1,
	)
	first, err := engine.Observe(original)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Observe(identifierObserveRequest(
		sessionID,
		"observe.identifier-file-advance",
		"event.identifier-file-advance",
		2,
	)); err != nil {
		t.Fatal(err)
	}

	reopenedStore, err := store.OpenFile(directory)
	if err != nil {
		t.Fatal(err)
	}
	reopened := newEngine(t, reopenedStore, policy.Deterministic{})
	repeated, err := reopened.Observe(original)
	if err != nil || !repeated.Duplicate ||
		repeated.Revision != first.Revision ||
		repeated.HeadHash != first.HeadHash {
		t.Fatalf("File restart lost original result: first=%+v repeated=%+v err=%v", first, repeated, err)
	}
	altered := original
	altered.Summary = "A different File-backed retry payload."
	if _, err := reopened.Observe(altered); err == nil {
		t.Fatal("File restart accepted an altered request")
	} else {
		assertIdentifierRequestConflict(t, err)
	}
	snapshot, err := reopened.Snapshot(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.IdentifierHistory == nil || !snapshot.IdentifierHistory.CoverageComplete {
		t.Fatalf("File replay did not rebuild complete history: %+v", snapshot.IdentifierHistory)
	}
}

func identifierObserveRequest(
	sessionID string,
	requestID string,
	eventID string,
	tick int64,
) protocol.ObserveRequest {
	return protocol.ObserveRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
		RequestID:       requestID,
		EventID:         eventID,
		Tick:            tick,
		ObserverIDs:     []string{"npc.mira"},
		Source:          "game",
		Kind:            "world",
		Summary:         "A compact identifier-history observation.",
		Importance:      1,
	}
}

func identifierActivityRequest(
	sessionID string,
	requestID string,
	tick int64,
) protocol.SetActorActivityRequest {
	return protocol.SetActorActivityRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
		RequestID:       requestID,
		Tick:            tick,
		Updates: []protocol.ActorActivityUpdate{{
			ActorID: "npc.mira",
			State:   "awake",
			Reason:  "Bounded request-history churn.",
		}},
	}
}

func assertIdentifierRequestConflict(t *testing.T, err error) {
	t.Helper()
	if rinruntime.ErrorCode(err) != "request_id_conflict" ||
		!errors.Is(err, rinruntime.ErrConflict) {
		t.Fatalf("altered request error = %v, want request_id_conflict mapped to 409", err)
	}
}

func assertIdentifierStateHead(
	t *testing.T,
	engine *rinruntime.Engine,
	sessionID string,
	expected protocol.MutationResult,
) {
	t.Helper()
	state, err := engine.State(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	if state.Revision != expected.Revision || state.HeadHash != expected.HeadHash {
		t.Fatalf(
			"failed identifier retry changed state head: got revision=%d head=%s want revision=%d head=%s",
			state.Revision,
			state.HeadHash,
			expected.Revision,
			expected.HeadHash,
		)
	}
}

func cloneIdentifierSnapshot(t *testing.T, snapshot protocol.Snapshot) protocol.Snapshot {
	t.Helper()
	payload, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var cloned protocol.Snapshot
	if err := json.Unmarshal(payload, &cloned); err != nil {
		t.Fatal(err)
	}
	return cloned
}

func rehashIdentifierHistory(t *testing.T, snapshot *protocol.Snapshot) {
	t.Helper()
	payload, err := json.Marshal(snapshot.IdentifierHistory)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.IdentifierHistoryHash = fmt.Sprintf("%x", sha256.Sum256(payload))
}

func assertInvalidCrossProjectionSnapshot(t *testing.T, snapshot protocol.Snapshot) {
	t.Helper()
	err := rinruntime.ValidateSnapshot(snapshot)
	if err == nil || rinruntime.ErrorCode(err) != "invalid_snapshot" {
		t.Fatalf("cross-projection tampering unexpectedly validated: %v", err)
	}
}

func assertInvalidIdentifierSnapshot(t *testing.T, snapshot protocol.Snapshot) {
	t.Helper()
	err := rinruntime.ValidateSnapshot(snapshot)
	if err == nil {
		t.Fatal("tampered identifier history unexpectedly validated")
	}
	if rinruntime.ErrorCode(err) != "invalid_snapshot" ||
		rinruntime.ErrorField(err) != "snapshot.identifier_history_hash" ||
		!errors.Is(err, rinruntime.ErrCorruptLog) {
		t.Fatalf(
			"tampered identifier history error = %v code=%s field=%s",
			err,
			rinruntime.ErrorCode(err),
			rinruntime.ErrorField(err),
		)
	}
}
