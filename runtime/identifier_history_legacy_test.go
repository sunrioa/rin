package runtime

import (
	"errors"
	"testing"
	"time"

	"github.com/sunrioa/rin/protocol"
)

func TestLegacyEventPayloadsRebuildVerifiedAndAmbiguousIdentifiers(t *testing.T) {
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
		_, identifiers, err := replayEvents(
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
