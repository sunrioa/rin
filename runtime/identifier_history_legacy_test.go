package runtime

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sunrioa/rin/protocol"
)

func TestLegacyEventPayloadsRebuildVerifiedAndAmbiguousIdentifiers(t *testing.T) {
	t.Run("restore request keeps its pre-expected-binding digest", func(t *testing.T) {
		const sessionID = "session.legacy-restore-digest"
		create := invariantCreate(sessionID, nil, nil)
		created := mustLegacyEvent(
			t,
			protocol.SessionState{},
			EventSessionCreated,
			create.RequestID,
			createdPayload{Request: create},
			1,
		)
		state, err := applyEvent(protocol.SessionState{}, created)
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := SnapshotOf(state)
		if err != nil {
			t.Fatal(err)
		}
		request := legacyRestoreRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       sessionID,
			RequestID:       "restore.legacy-no-expected-binding",
			Snapshot:        snapshot,
		}
		digest, err := requestDigest(request)
		if err != nil {
			t.Fatal(err)
		}
		restored := mustLegacyEvent(
			t,
			protocol.SessionState{},
			EventSessionRestored,
			request.RequestID,
			restoredPayload{Snapshot: snapshot, RequestHash: digest},
			2,
		)

		replayed, identifiers, err := replayEvents([]protocol.EventRecord{restored}, 0)
		if err != nil {
			t.Fatalf("legacy restore event no longer replays: %v", err)
		}
		if replayed.SessionID != sessionID {
			t.Fatalf("legacy restore replayed the wrong session: %+v", replayed)
		}
		identity := identifiers.Requests[request.RequestID]
		if identity.Ambiguous || identity.RequestHash != digest {
			t.Fatalf("legacy restore digest was not preserved: %+v", identity)
		}

		eventStore := newInvariantStore()
		eventStore.events[sessionID] = []protocol.EventRecord{restored}
		engine, err := Open(eventStore, invariantPolicy{})
		if err != nil {
			t.Fatalf("engine could not open the legacy restore event: %v", err)
		}
		repeated, err := engine.Restore(protocol.RestoreRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       sessionID,
			RequestID:       request.RequestID,
			ExpectedBinding: snapshot.State.Binding,
			Snapshot:        snapshot,
		})
		if err != nil || !repeated.Duplicate ||
			repeated.Revision != identity.ResultRevision ||
			repeated.HeadHash != identity.ResultHeadHash {
			t.Fatalf("new-schema retry did not match legacy restore: result=%+v identity=%+v err=%v", repeated, identity, err)
		}
	})

	t.Run("full requests derive canonical digests", func(t *testing.T) {
		create := invariantCreate("session.legacy-identifiers", nil, nil)
		created, err := newEvent(
			protocol.SessionState{},
			EventSessionCreated,
			create.RequestID,
			createdPayload{Request: create},
			time.Unix(1, 0),
		)
		if err != nil {
			t.Fatal(err)
		}
		state, err := applyEvent(protocol.SessionState{}, created)
		if err != nil {
			t.Fatal(err)
		}
		observe := invariantObserve(
			create.SessionID,
			"observe.legacy-verified",
			"event.legacy-verified",
			1,
		)
		observed, err := newEvent(
			state,
			EventObserved,
			observe.RequestID,
			observedPayload{Request: observe},
			time.Unix(2, 0),
		)
		if err != nil {
			t.Fatal(err)
		}
		_, identifiers, err := replayEvents([]protocol.EventRecord{created, observed}, 0)
		if err != nil {
			t.Fatal(err)
		}
		digest, err := requestDigest(observe)
		if err != nil {
			t.Fatal(err)
		}
		identity, used, err := identifierRequest(
			identifiers,
			observe.RequestID,
			EventObserved,
			digest,
		)
		if err != nil || !used || identity.Ambiguous {
			t.Fatalf("legacy full request was not verified: identity=%+v used=%v err=%v", identity, used, err)
		}
		if !identifierEventExists(identifiers, observe.EventID) {
			t.Fatal("legacy observation event id was not rebuilt")
		}
	})

	t.Run("historical request reuse becomes an ambiguous tombstone", func(t *testing.T) {
		create := invariantCreate("session.legacy-request-reuse", nil, nil)
		created := mustLegacyEvent(
			t,
			protocol.SessionState{},
			EventSessionCreated,
			create.RequestID,
			createdPayload{Request: create},
			1,
		)
		state, err := applyEvent(protocol.SessionState{}, created)
		if err != nil {
			t.Fatal(err)
		}
		first := invariantObserve(
			create.SessionID,
			"observe.legacy-reused-request",
			"event.legacy-reused-first",
			1,
		)
		firstEvent := mustLegacyEvent(
			t,
			state,
			EventObserved,
			first.RequestID,
			observedPayload{Request: first},
			2,
		)
		state, err = applyEvent(state, firstEvent)
		if err != nil {
			t.Fatal(err)
		}
		second := first
		second.EventID = "event.legacy-reused-second"
		second.Summary = "A historically reused request carried another payload."
		secondEvent := mustLegacyEvent(
			t,
			state,
			EventObserved,
			second.RequestID,
			observedPayload{Request: second},
			3,
		)
		replayed, identifiers, err := replayEvents(
			[]protocol.EventRecord{created, firstEvent, secondEvent},
			0,
		)
		if err != nil {
			t.Fatal(err)
		}
		identity := identifiers.Requests[first.RequestID]
		if !identity.Ambiguous || identity.RequestHash != "" {
			t.Fatalf("historical reuse was not tombstoned: %+v", identity)
		}
		digest, err := requestDigest(first)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := identifierRequest(
			identifiers,
			first.RequestID,
			EventObserved,
			digest,
		); ErrorCode(err) != "request_id_conflict" || !errors.Is(err, ErrConflict) {
			t.Fatalf("ambiguous legacy request did not fail closed: %v", err)
		}
		if !identifierEventExists(identifiers, first.EventID) ||
			!identifierEventExists(identifiers, second.EventID) {
			t.Fatalf("legacy request reuse lost event ids: %+v", identifiers.Events)
		}
		if _, err := snapshotWithIdentifiers(replayed, identifiers); err != nil {
			t.Fatalf("ambiguous current legacy request made Snapshot unavailable: %v", err)
		}

		eventStore := newInvariantStore()
		eventStore.events[create.SessionID] = []protocol.EventRecord{created, firstEvent, secondEvent}
		engine, err := Open(eventStore, invariantPolicy{})
		if err != nil {
			t.Fatal(err)
		}
		request := protocol.SessionRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       create.SessionID,
		}
		if _, err := engine.Snapshot(request); err != nil {
			t.Fatalf("engine Snapshot rejected ambiguous current legacy request: %v", err)
		}
		if _, err := engine.Replay(protocol.ReplayRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       create.SessionID,
			Revision:        replayed.Revision,
		}); err != nil {
			t.Fatalf("engine Replay rejected ambiguous current legacy request: %v", err)
		}
	})

	t.Run("typed events without a digest fail closed", func(t *testing.T) {
		create := invariantCreate(
			"session.legacy-typed-identifiers",
			[]string{protocol.FeatureArbitration},
			nil,
		)
		created := mustLegacyEvent(
			t,
			protocol.SessionState{},
			EventSessionCreated,
			create.RequestID,
			createdPayload{Request: create},
			1,
		)
		state, err := applyEvent(protocol.SessionState{}, created)
		if err != nil {
			t.Fatal(err)
		}
		proposal := protocol.ActionProposal{
			ID:                   "proposal.legacy-no-digest",
			SessionID:            create.SessionID,
			RequestID:            "propose.legacy-no-digest",
			ActorID:              "npc.mira",
			BasedOnRevision:      state.Revision,
			BasedOnHeadHash:      state.HeadHash,
			BasedOnWorldRevision: state.WorldRevision,
			CreatedRevision:      state.Revision + 1,
			Action: protocol.ActionSpec{
				ID: "wait", Kind: "wait", Description: "Wait safely.",
			},
			Stance:    "wait",
			Summary:   "Wait.",
			Rationale: "Waiting is allowed.",
			Status:    "pending",
		}
		proposed := mustLegacyEvent(
			t,
			state,
			EventProposed,
			proposal.RequestID,
			proposedPayload{Proposal: proposal},
			2,
		)
		state, err = applyEvent(state, proposed)
		if err != nil {
			t.Fatal(err)
		}
		record := protocol.ArbitrationRecord{
			ID:                   "arbitration.legacy-no-digest",
			RequestID:            "arbitrate.legacy-no-digest",
			BasedOnWorldRevision: state.WorldRevision,
			CreatedRevision:      state.Revision + 1,
			Decisions: []protocol.ArbitrationDecision{{
				ProposalID: proposal.ID,
				ActorID:    proposal.ActorID,
				Status:     "selected",
				Reason:     "No conflict.",
			}},
		}
		arbitrated := mustLegacyEvent(
			t,
			state,
			EventArbitrated,
			record.RequestID,
			arbitratedPayload{Record: record},
			3,
		)
		_, identifiers, err := replayEvents(
			[]protocol.EventRecord{created, proposed, arbitrated},
			0,
		)
		if err != nil {
			t.Fatal(err)
		}
		for _, requestID := range []string{proposal.RequestID, record.RequestID} {
			identity := identifiers.Requests[requestID]
			if !identity.Ambiguous || identity.RequestHash != "" {
				t.Fatalf("legacy typed request %q was guessed as verified: %+v", requestID, identity)
			}
		}
	})
}

func TestLegacyOversizedRestoreEventRemainsReplayable(t *testing.T) {
	const (
		sessionID       = "session.legacy-oversized-restore"
		extraIdentities = 1_100
	)
	create := invariantCreate(sessionID, nil, nil)
	created := mustLegacyEvent(
		t,
		protocol.SessionState{},
		EventSessionCreated,
		create.RequestID,
		createdPayload{Request: create},
		1,
	)
	state, err := applyEvent(protocol.SessionState{}, created)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := SnapshotOf(state)
	if err != nil {
		t.Fatal(err)
	}
	parameters := make(map[string]string, 32)
	for index := 0; index < 32; index++ {
		parameters[fmt.Sprintf("parameter.%02d", index)] = strings.Repeat("v", 500)
	}
	for index := 0; index < extraIdentities; index++ {
		requestID := fmt.Sprintf("request.legacy-large.%04d", index)
		proposal := protocol.ActionProposal{
			ID:              fmt.Sprintf("proposal.legacy-large.%04d", index),
			SessionID:       sessionID,
			RequestID:       requestID,
			ActorID:         "npc.legacy",
			BasedOnRevision: 1,
			BasedOnHeadHash: state.HeadHash,
			CreatedRevision: 2,
			Action: protocol.ActionSpec{
				ID:          "action.wait",
				Kind:        "wait",
				Description: strings.Repeat("d", 300),
				Parameters:  parameters,
			},
			Stance:    "wait",
			Summary:   strings.Repeat("s", 500),
			Rationale: strings.Repeat("r", 500),
			Status:    "pending",
		}
		snapshot.IdentifierHistory.Requests[requestID] = protocol.RequestIdentity{
			Kind:           EventProposed,
			RequestHash:    state.HeadHash,
			ResultRevision: proposal.CreatedRevision,
			ResultHeadHash: state.HeadHash,
			Proposal:       &proposal,
		}
	}
	snapshot.IdentifierHistoryHash, err = hashJSON(*snapshot.IdentifierHistory)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateSnapshot(snapshot); ErrorCode(err) != "snapshot_too_large" {
		t.Fatalf("legacy fixture is not above the new inline limit: %v", err)
	}
	checkpoint, err := BuildCheckpoint(
		snapshot.State,
		*snapshot.IdentifierHistory,
		0,
	)
	if err != nil {
		t.Fatalf("internal checkpoint incorrectly used the inline limit: %v", err)
	}
	if err := ValidateCheckpoint(checkpoint); err != nil {
		t.Fatalf("oversized internal checkpoint did not validate: %v", err)
	}

	request := legacyRestoreRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
		RequestID:       "restore.legacy-oversized",
		Snapshot:        snapshot,
	}
	digest, err := requestDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	restored := mustLegacyEvent(
		t,
		protocol.SessionState{},
		EventSessionRestored,
		request.RequestID,
		restoredPayload{Snapshot: snapshot, RequestHash: digest},
		2,
	)
	eventStore := newInvariantStore()
	eventStore.events[sessionID] = []protocol.EventRecord{restored}
	engine, err := Open(eventStore, invariantPolicy{})
	if err != nil {
		t.Fatalf("upgrade could not replay a pre-limit restore event: %v", err)
	}
	replayed, err := engine.State(protocol.SessionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if replayed.SessionID != sessionID || replayed.Binding != snapshot.State.Binding {
		t.Fatalf("oversized legacy restore replayed the wrong state: %+v", replayed)
	}
}

func mustLegacyEvent(
	t *testing.T,
	state protocol.SessionState,
	eventType string,
	requestID string,
	payload any,
	second int64,
) protocol.EventRecord {
	t.Helper()
	event, err := newEvent(state, eventType, requestID, payload, time.Unix(second, 0))
	if err != nil {
		t.Fatal(err)
	}
	return event
}
