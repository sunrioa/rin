package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sunrioa/rin/protocol"
)

const legacyPrivateProposalCanary = "PRIVATE_LEGACY_PROPOSAL_CANARY_80E4"

func TestLegacyProposalEventPresentationIsCanonicalizedWithoutRewritingEvent(t *testing.T) {
	const sessionID = "session.legacy-proposal-presentation"
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
	request, proposal, requestHash := legacyPrivateProposalFixture(t, state)
	proposed := mustLegacyEvent(
		t,
		state,
		EventProposed,
		request.RequestID,
		proposedPayload{Proposal: proposal, RequestHash: requestHash},
		2,
	)
	originalData := append([]byte(nil), proposed.Data...)
	originalHash := proposed.Hash

	replayed, identifiers, err := replayEvents(
		[]protocol.EventRecord{created, proposed},
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	assertCanonicalLegacyProposal(t, replayed.Proposals[proposal.ID])
	assertCanonicalLegacyProposal(
		t,
		*identifiers.Requests[request.RequestID].Proposal,
	)
	if proposed.Hash != originalHash ||
		!bytes.Equal(proposed.Data, originalData) ||
		!bytes.Contains(proposed.Data, []byte(legacyPrivateProposalCanary)) {
		t.Fatal("projection migration modified or erased the authoritative legacy event")
	}
	if err := VerifyEventRecord(state.Revision, state.HeadHash, proposed); err != nil {
		t.Fatalf("legacy event hash no longer verifies: %v", err)
	}

	eventStore := newInvariantStore()
	eventStore.events[sessionID] = []protocol.EventRecord{created, proposed}
	engine, err := Open(eventStore, invariantPolicy{
		propose: func(context.Context, PolicyContext) (ProposalDraft, error) {
			return ProposalDraft{}, errors.New("policy must not run for an exact legacy retry")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	retried, duplicate, err := engine.Propose(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !duplicate {
		t.Fatal("exact legacy proposal retry was not identified as a duplicate")
	}
	assertCanonicalLegacyProposal(t, retried)
}

func TestLegacySnapshotPresentationIsCanonicalizedAtRestoreAndExactRetry(t *testing.T) {
	const sessionID = "session.legacy-snapshot-presentation"
	create := invariantCreate(
		sessionID,
		[]string{protocol.FeatureOutcomeReporting},
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
	request, proposal, requestHash := legacyPrivateProposalFixture(t, state)
	proposed := mustLegacyEvent(
		t,
		state,
		EventProposed,
		request.RequestID,
		proposedPayload{Proposal: proposal, RequestHash: requestHash},
		2,
	)
	state, identifiers, err := replayEvents(
		[]protocol.EventRecord{created, proposed},
		0,
	)
	if err != nil {
		t.Fatal(err)
	}

	legacyProposal := state.Proposals[proposal.ID]
	legacyProposal.Summary = legacyPrivateProposalCanary
	legacyProposal.Rationale = legacyPrivateProposalCanary
	state.Proposals[proposal.ID] = legacyProposal
	identity := identifiers.Requests[request.RequestID]
	identityProposal := *identity.Proposal
	identityProposal.Summary = legacyPrivateProposalCanary
	identityProposal.Rationale = legacyPrivateProposalCanary
	identity.Proposal = &identityProposal
	identifiers.Requests[request.RequestID] = identity

	stateHash, err := hashJSON(state)
	if err != nil {
		t.Fatal(err)
	}
	identifierHash, err := hashJSON(identifiers)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := protocol.Snapshot{
		ProtocolVersion:       protocol.Version,
		StateHash:             stateHash,
		State:                 state,
		IdentifierHistory:     &identifiers,
		IdentifierHistoryHash: identifierHash,
	}
	if err := ValidateSnapshot(snapshot); err != nil {
		t.Fatalf("legacy snapshot fixture is invalid: %v", err)
	}

	eventStore := newInvariantStore()
	engine, err := Open(eventStore, invariantPolicy{
		propose: func(context.Context, PolicyContext) (ProposalDraft, error) {
			return ProposalDraft{}, errors.New("policy must not run for an imported exact retry")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Restore(protocol.RestoreRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
		RequestID:       "restore.legacy-proposal-presentation",
		ExpectedBinding: snapshot.State.Binding,
		Snapshot:        snapshot,
	}); err != nil {
		t.Fatal(err)
	}
	restored, err := engine.State(protocol.SessionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCanonicalLegacyProposal(t, restored.Proposals[proposal.ID])

	exported, err := engine.Snapshot(protocol.SessionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCanonicalLegacySnapshot(t, exported, proposal.ID, request.RequestID)
	replayedSnapshot, err := engine.Replay(protocol.ReplayRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
		Revision:        1,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertCanonicalLegacySnapshot(t, replayedSnapshot, proposal.ID, request.RequestID)

	retried, duplicate, err := engine.Propose(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !duplicate {
		t.Fatal("imported exact proposal retry was not identified as a duplicate")
	}
	assertCanonicalLegacyProposal(t, retried)

	persisted, err := eventStore.Load(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted) != 1 ||
		persisted[0].Type != EventSessionRestored ||
		!bytes.Contains(persisted[0].Data, []byte(legacyPrivateProposalCanary)) {
		t.Fatal("restore migration should retain the authoritative raw legacy snapshot event")
	}
	if err := VerifyEventRecord(0, "", persisted[0]); err != nil {
		t.Fatalf("raw restore event hash no longer verifies: %v", err)
	}
}

func legacyPrivateProposalFixture(
	t *testing.T,
	state protocol.SessionState,
) (protocol.ProposeRequest, protocol.ActionProposal, string) {
	t.Helper()
	action := protocol.ActionSpec{
		ID: "wait", Kind: "wait", Description: "wait for the next game-authored beat",
	}
	request := protocol.ProposeRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       state.SessionID,
		RequestID:       "propose.legacy-private-presentation",
		ActorID:         "npc.mira",
		Intent:          "Choose one allowed action.",
		CandidateActions: []protocol.ActionSpec{
			action,
		},
	}
	requestHash, err := requestDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	proposal := protocol.ActionProposal{
		ID:              "proposal.legacy-private-presentation",
		SessionID:       state.SessionID,
		RequestID:       request.RequestID,
		ActorID:         request.ActorID,
		BasedOnRevision: state.Revision,
		BasedOnHeadHash: state.HeadHash,
		CreatedRevision: state.Revision + 1,
		Action:          action,
		Stance:          "wait",
		Summary:         legacyPrivateProposalCanary,
		Rationale:       legacyPrivateProposalCanary,
		PolicySource:    "legacy",
		Status:          "pending",
	}
	return request, proposal, requestHash
}

func assertCanonicalLegacyProposal(t *testing.T, proposal protocol.ActionProposal) {
	t.Helper()
	if proposal.Summary != "Proposes: wait for the next game-authored beat" ||
		proposal.Rationale != "Selects a game-authorized wait action." {
		t.Fatalf("legacy proposal presentation was not canonicalized: %+v", proposal)
	}
	if strings.Contains(proposal.Summary, legacyPrivateProposalCanary) ||
		strings.Contains(proposal.Rationale, legacyPrivateProposalCanary) {
		t.Fatalf("legacy private presentation reached an API projection: %+v", proposal)
	}
}

func assertCanonicalLegacySnapshot(
	t *testing.T,
	snapshot protocol.Snapshot,
	proposalID string,
	requestID string,
) {
	t.Helper()
	assertCanonicalLegacyProposal(t, snapshot.State.Proposals[proposalID])
	if snapshot.IdentifierHistory == nil ||
		snapshot.IdentifierHistory.Requests[requestID].Proposal == nil {
		t.Fatal("snapshot omitted durable proposal identity")
	}
	assertCanonicalLegacyProposal(
		t,
		*snapshot.IdentifierHistory.Requests[requestID].Proposal,
	)
	payload, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(payload, []byte(legacyPrivateProposalCanary)) {
		t.Fatal("snapshot API projection retained legacy private proposal text")
	}
}
