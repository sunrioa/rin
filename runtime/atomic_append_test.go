package runtime_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/sunrioa/rin/policy"
	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
	"github.com/sunrioa/rin/store"
)

func TestCommitAppendFailureDoesNotMutateLiveStateAndRetryReplays(t *testing.T) {
	eventStore := newFailOnceAppendStore(store.NewMemory())
	engine := newEngine(t, eventStore, policy.Deterministic{})
	const sessionID = "session.atomic-commit"

	if _, err := engine.CreateSession(createRequest(sessionID)); err != nil {
		t.Fatal(err)
	}
	proposal, _, err := engine.Propose(context.Background(), proposeRequest(sessionID, "propose.atomic-commit", 0, nil))
	if err != nil {
		t.Fatal(err)
	}
	before, err := engine.State(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	request := protocol.CommitRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
		RequestID:       "commit.atomic-commit",
		ProposalID:      proposal.ID,
		EventID:         "event.atomic-commit",
		Tick:            proposal.Tick,
		Accepted:        true,
		Outcome:         "The game applied the action before reporting it.",
	}

	eventStore.failNextAppend()
	if _, err := engine.Commit(request); !errors.Is(err, errInjectedAppend) || rinruntime.ErrorCode(err) != "store_append_failed" {
		t.Fatalf("expected injected store_append_failed, got %v", err)
	}
	afterFailure, err := engine.State(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, afterFailure) {
		t.Fatalf("failed append mutated live state:\nbefore=%+v\nafter=%+v", before, afterFailure)
	}

	result, err := engine.Commit(request)
	if err != nil {
		t.Fatalf("same request id should retry after an unpersisted failure: %v", err)
	}
	if result.Duplicate {
		t.Fatalf("retry of an unpersisted request was incorrectly reported as duplicate: %+v", result)
	}
	assertAcceptedOutcomeOnce(t, engine, sessionID, proposal.ActorID, proposal.ID, request.EventID)

	repeated, err := engine.Commit(request)
	if err != nil {
		t.Fatalf("persisted request should be idempotent: %v", err)
	}
	if !repeated.Duplicate || repeated.Revision != result.Revision {
		t.Fatalf("persisted retry should return the original revision as duplicate: first=%+v repeated=%+v", result, repeated)
	}

	reopened := newEngine(t, eventStore, policy.Deterministic{})
	assertAcceptedOutcomeOnce(t, reopened, sessionID, proposal.ActorID, proposal.ID, request.EventID)
	replayed, err := reopened.State(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Revision != result.Revision {
		t.Fatalf("replay revision = %d, want %d", replayed.Revision, result.Revision)
	}
}

func TestBatchCommitAppendFailureDoesNotMutateLiveStateAndRetryReplays(t *testing.T) {
	eventStore := newFailOnceAppendStore(store.NewMemory())
	engine := newEngine(t, eventStore, policy.Deterministic{})
	create := twoActorWorldRequest("session.atomic-batch")

	if _, err := engine.CreateSession(create); err != nil {
		t.Fatal(err)
	}
	mira, _, err := engine.Propose(context.Background(), targetedProposalRequest(create.SessionID, "propose.atomic-mira", "npc.mira"))
	if err != nil {
		t.Fatal(err)
	}
	oren, _, err := engine.Propose(context.Background(), targetedProposalRequest(create.SessionID, "propose.atomic-oren", "npc.oren"))
	if err != nil {
		t.Fatal(err)
	}
	before, err := engine.State(sessionRequest(create.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	request := protocol.BatchCommitRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       create.SessionID,
		RequestID:       "commit.atomic-batch",
		Tick:            mira.Tick,
		Items: []protocol.CommitItem{
			{
				ProposalID: mira.ID,
				EventID:    "event.atomic-mira",
				Accepted:   true,
				Outcome:    "Mira completed the coordinated action.",
			},
			{
				ProposalID: oren.ID,
				EventID:    "event.atomic-oren",
				Accepted:   true,
				Outcome:    "Oren completed the coordinated action.",
			},
		},
	}

	eventStore.failNextAppend()
	if _, err := engine.CommitBatch(request); !errors.Is(err, errInjectedAppend) || rinruntime.ErrorCode(err) != "store_append_failed" {
		t.Fatalf("expected injected store_append_failed, got %v", err)
	}
	afterFailure, err := engine.State(sessionRequest(create.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, afterFailure) {
		t.Fatalf("failed batch append mutated live state:\nbefore=%+v\nafter=%+v", before, afterFailure)
	}

	result, err := engine.CommitBatch(request)
	if err != nil {
		t.Fatalf("same batch request id should retry after an unpersisted failure: %v", err)
	}
	if result.Duplicate {
		t.Fatalf("retry of an unpersisted batch was incorrectly reported as duplicate: %+v", result)
	}
	assertAcceptedOutcomeOnce(t, engine, create.SessionID, mira.ActorID, mira.ID, request.Items[0].EventID)
	assertAcceptedOutcomeOnce(t, engine, create.SessionID, oren.ActorID, oren.ID, request.Items[1].EventID)

	repeated, err := engine.CommitBatch(request)
	if err != nil {
		t.Fatalf("persisted batch should be idempotent: %v", err)
	}
	if !repeated.Duplicate || repeated.Revision != result.Revision {
		t.Fatalf("persisted batch retry should return the original revision as duplicate: first=%+v repeated=%+v", result, repeated)
	}

	reopened := newEngine(t, eventStore, policy.Deterministic{})
	assertAcceptedOutcomeOnce(t, reopened, create.SessionID, mira.ActorID, mira.ID, request.Items[0].EventID)
	assertAcceptedOutcomeOnce(t, reopened, create.SessionID, oren.ActorID, oren.ID, request.Items[1].EventID)
	replayed, err := reopened.State(sessionRequest(create.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Revision != result.Revision {
		t.Fatalf("batch replay revision = %d, want %d", replayed.Revision, result.Revision)
	}
}

func TestCommitReconcilesPostWriteAppendErrorWithoutDuplicateLogEntry(t *testing.T) {
	eventStore := newFailAfterAppendOnceStore(store.NewMemory())
	engine := newEngine(t, eventStore, policy.Deterministic{})
	const sessionID = "session.atomic-post-write"
	if _, err := engine.CreateSession(createRequest(sessionID)); err != nil {
		t.Fatal(err)
	}
	proposal, _, err := engine.Propose(
		context.Background(),
		proposeRequest(sessionID, "propose.atomic-post-write", 0, nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	request := protocol.CommitRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
		RequestID:       "commit.atomic-post-write",
		ProposalID:      proposal.ID,
		EventID:         "event.atomic-post-write",
		Tick:            0,
		Accepted:        true,
		Outcome:         "The game already applied this action.",
	}

	eventStore.failAfterNextAppend()
	result, err := engine.Commit(request)
	if err != nil {
		t.Fatalf("engine should reconcile an exact event written before an append error: %v", err)
	}
	if result.Duplicate {
		t.Fatalf("first reconciled report is not a client duplicate: %+v", result)
	}
	if calls := eventStore.appendCallCount(); calls != 2 {
		t.Fatalf("post-write reconciliation used %d append calls, want initial append plus exact retry", calls)
	}
	events, err := eventStore.Load(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("post-write reconciliation appended %d log events, want 3", len(events))
	}
	assertAcceptedOutcomeOnce(t, engine, sessionID, proposal.ActorID, proposal.ID, request.EventID)

	reopened := newEngine(t, eventStore, policy.Deterministic{})
	assertAcceptedOutcomeOnce(t, reopened, sessionID, proposal.ActorID, proposal.ID, request.EventID)
}

func TestAppendReconciliationNeverPublishesUnverifiedLoadedEvent(t *testing.T) {
	for _, test := range staleHashEventMutations() {
		t.Run(test.name, func(t *testing.T) {
			delegate := store.NewMemory()
			eventStore := &tamperedConfirmationStore{Store: delegate}
			engine := newEngine(t, eventStore, policy.Deterministic{})
			sessionID := "session.atomic-append-tamper-" + test.name
			if _, err := engine.CreateSession(createRequest(sessionID)); err != nil {
				t.Fatal(err)
			}
			before, err := engine.State(sessionRequest(sessionID))
			if err != nil {
				t.Fatal(err)
			}
			eventStore.failAfterNextAppend(test.mutate)

			request := observeRequest(
				sessionID,
				"observe.atomic-append-tamper-"+test.name,
				"event.atomic-append-tamper-"+test.name,
				1,
			)
			if _, err := engine.Observe(request); err == nil {
				t.Fatalf("unverified loaded tail should fail reconciliation: %v", err)
			}
			after, err := engine.State(sessionRequest(sessionID))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("unverified loaded tail advanced live state:\nbefore=%+v\nafter=%+v", before, after)
			}
			events, err := delegate.Load(sessionID)
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 2 {
				t.Fatalf("post-write store contains %d events, want 2", len(events))
			}
		})
	}
}

func TestCommitRecoversWhenPostWriteConfirmationInitiallyFails(t *testing.T) {
	eventStore := newFailAfterAppendAndConfirmationStore(store.NewMemory())
	engine := newEngine(t, eventStore, policy.Deterministic{})
	const sessionID = "session.atomic-confirmation"
	if _, err := engine.CreateSession(createRequest(sessionID)); err != nil {
		t.Fatal(err)
	}
	proposal, _, err := engine.Propose(
		context.Background(),
		proposeRequest(sessionID, "propose.atomic-confirmation", 0, nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	before, err := engine.State(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	request := protocol.CommitRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
		RequestID:       "commit.atomic-confirmation",
		ProposalID:      proposal.ID,
		EventID:         "event.atomic-confirmation",
		Tick:            0,
		Accepted:        true,
		Outcome:         "The game already applied this action.",
	}

	eventStore.failPostWriteAndConfirmation()
	if _, err := engine.Commit(request); !errors.Is(err, errInjectedAppend) ||
		rinruntime.ErrorCode(err) != "mutation_outcome_unknown" {
		t.Fatalf("failed durability confirmation should be reported: %v", err)
	}
	afterFailure, err := engine.State(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, afterFailure) {
		t.Fatalf("unconfirmed append advanced live state:\nbefore=%+v\nafter=%+v", before, afterFailure)
	}
	if calls := eventStore.appendCallCount(); calls != 2 {
		t.Fatalf("failed confirmation used %d append calls, want 2", calls)
	}

	altered := request
	altered.Outcome = "A different payload must not claim the persisted request."
	eventStore.forceNextAppendConflict()
	if _, err := engine.Commit(altered); err == nil ||
		rinruntime.ErrorCode(err) != "request_id_conflict" {
		t.Fatalf("altered same-ID retry must not reconcile the persisted event: %v", err)
	}
	afterAltered, err := engine.State(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, afterAltered) {
		t.Fatalf("altered retry advanced live state:\nbefore=%+v\nafter=%+v", before, afterAltered)
	}
	events, err := eventStore.Load(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("altered retry changed persisted event count to %d, want 3", len(events))
	}

	eventStore.forceNextAppendConflict()
	if _, err := engine.Commit(request); err != nil {
		t.Fatalf("client retry should reconcile the previously persisted logical event: %v", err)
	}
	if calls := eventStore.appendCallCount(); calls != 4 {
		t.Fatalf("logical reconciliation used %d append calls, want 4", calls)
	}
	assertAcceptedOutcomeOnce(t, engine, sessionID, proposal.ActorID, proposal.ID, request.EventID)
	events, err = eventStore.Load(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("confirmation recovery left %d events, want 3", len(events))
	}
	reopened := newEngine(t, eventStore, policy.Deterministic{})
	assertAcceptedOutcomeOnce(t, reopened, sessionID, proposal.ActorID, proposal.ID, request.EventID)
}

func TestProposeReportsUnknownAndSameRequestRecoversAfterConfirmationFailure(t *testing.T) {
	eventStore := newFailAfterAppendAndConfirmationStore(store.NewMemory())
	engine := newEngine(t, eventStore, policy.Deterministic{})
	const sessionID = "session.atomic-proposal-confirmation"
	if _, err := engine.CreateSession(createRequest(sessionID)); err != nil {
		t.Fatal(err)
	}
	before, err := engine.State(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	request := proposeRequest(sessionID, "propose.atomic-confirmation", 0, nil)

	eventStore.failPostWriteAndConfirmation()
	if _, _, err := engine.Propose(context.Background(), request); !errors.Is(err, errInjectedAppend) ||
		rinruntime.ErrorCode(err) != "proposal_outcome_unknown" {
		t.Fatalf("failed Proposal durability confirmation should report proposal_outcome_unknown: %v", err)
	}
	afterFailure, err := engine.State(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, afterFailure) {
		t.Fatalf("unconfirmed Proposal append advanced live state:\nbefore=%+v\nafter=%+v", before, afterFailure)
	}
	if calls := eventStore.appendCallCount(); calls != 2 {
		t.Fatalf("failed Proposal confirmation used %d append calls, want 2", calls)
	}
	events, err := eventStore.Load(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[1].Type != rinruntime.EventProposed {
		t.Fatalf("Proposal outcome should be uncertain because its event is already present: %+v", events)
	}

	proposal, duplicate, err := engine.Propose(context.Background(), request)
	if err != nil {
		t.Fatalf("same request should reconcile the previously persisted Proposal: %v", err)
	}
	if duplicate {
		t.Fatalf("the first confirmed response should not be marked as a client duplicate: %+v", proposal)
	}
	if calls := eventStore.appendCallCount(); calls != 3 {
		t.Fatalf("Proposal recovery used %d append calls, want one exact same-event confirmation", calls)
	}
	state, err := engine.State(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	if receipt := state.Receipts[request.RequestID]; receipt.Kind != rinruntime.EventProposed ||
		receipt.EntityID != proposal.ID {
		t.Fatalf("recovered Proposal receipt mismatch: %+v proposal=%+v", receipt, proposal)
	}
	if retained := state.Proposals[proposal.ID]; !reflect.DeepEqual(retained, proposal) {
		t.Fatalf("recovered Proposal mismatch:\nretained=%+v\nreturned=%+v", retained, proposal)
	}
	events, err = eventStore.Load(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("Proposal recovery left %d events, want exactly 2", len(events))
	}
}

func TestProposalReconciliationFailureIsOutcomeUnknownAndRetryable(t *testing.T) {
	eventStore := newCorruptProposalReconcileOnceStore(store.NewMemory())
	changingPolicy := &changingAtomicPolicy{}
	engine := newEngine(t, eventStore, changingPolicy)
	const sessionID = "session.atomic-proposal-reconcile"
	if _, err := engine.CreateSession(createRequest(sessionID)); err != nil {
		t.Fatal(err)
	}
	request := proposeRequest(sessionID, "propose.atomic-reconcile", 0, nil)

	eventStore.failAfterWriteAndCorruptLoad()
	if _, _, err := engine.Propose(context.Background(), request); err == nil ||
		rinruntime.ErrorCode(err) != "proposal_outcome_unknown" {
		t.Fatalf("persisted but unreconciled Proposal must be outcome-unknown: %v", err)
	}
	state, err := engine.State(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Proposals) != 0 || state.Revision != 1 {
		t.Fatalf("unreconciled Proposal advanced live state: %+v", state)
	}
	altered := request
	altered.Intent = "A different request must not claim the uncertain event."
	if _, _, err := engine.Propose(context.Background(), altered); err == nil ||
		rinruntime.ErrorCode(err) != "request_id_conflict" {
		t.Fatalf("altered retry claimed an uncertain Proposal identity: %v", err)
	}

	proposal, duplicate, err := engine.Propose(context.Background(), request)
	if err != nil {
		t.Fatalf("same request did not reconcile the persisted Proposal: %v", err)
	}
	if duplicate || proposal.RequestID != request.RequestID {
		t.Fatalf("unexpected reconciled Proposal: %+v duplicate=%v", proposal, duplicate)
	}
	if proposal.Action.ID != "talk" || changingPolicy.callCount() != 1 {
		t.Fatalf(
			"retry reran the non-deterministic policy: proposal=%+v calls=%d",
			proposal,
			changingPolicy.callCount(),
		)
	}
	events, err := eventStore.Load(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[1].Type != rinruntime.EventProposed {
		t.Fatalf("reconciliation should retain one Proposal event: %+v", events)
	}
}

func TestUncertainProposalBlocksOtherMutationsUntilExactRetry(t *testing.T) {
	eventStore := newFailProposalBeforeWriteAndLoadOnceStore(store.NewMemory())
	changingPolicy := &changingAtomicPolicy{}
	engine := newEngine(t, eventStore, changingPolicy)
	const sessionID = "session.atomic-proposal-block"
	if _, err := engine.CreateSession(createRequest(sessionID)); err != nil {
		t.Fatal(err)
	}
	request := proposeRequest(sessionID, "propose.atomic-block", 0, nil)

	eventStore.failProposalAndLoad()
	if _, _, err := engine.Propose(context.Background(), request); err == nil ||
		rinruntime.ErrorCode(err) != "proposal_outcome_unknown" {
		t.Fatalf("indeterminate Proposal append must report proposal_outcome_unknown: %v", err)
	}
	if calls := eventStore.appendCallCount(); calls != 1 {
		t.Fatalf("failed Proposal used %d append calls, want 1", calls)
	}

	observation := observeRequest(sessionID, "observe.while-proposal-unknown", "event.while-proposal-unknown", 0)
	if _, err := engine.Observe(observation); err == nil ||
		rinruntime.ErrorCode(err) != "proposal_outcome_unknown" {
		t.Fatalf("another mutation must be blocked behind the uncertain Proposal: %v", err)
	}
	if calls := eventStore.appendCallCount(); calls != 1 {
		t.Fatalf("blocked observation reached the store; append calls = %d, want 1", calls)
	}
	events, err := eventStore.Load(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("blocked mutation changed persisted event count to %d, want 1", len(events))
	}

	proposal, duplicate, err := engine.Propose(context.Background(), request)
	if err != nil {
		t.Fatalf("exact Proposal retry did not recover the session: %v", err)
	}
	if duplicate || proposal.RequestID != request.RequestID {
		t.Fatalf("unexpected recovered Proposal: %+v duplicate=%v", proposal, duplicate)
	}
	if changingPolicy.callCount() != 1 {
		t.Fatalf("exact retry reran policy %d times, want once", changingPolicy.callCount())
	}
	if calls := eventStore.appendCallCount(); calls != 2 {
		t.Fatalf("exact recovery used %d append calls, want 2", calls)
	}
	if _, err := engine.Observe(observation); err != nil {
		t.Fatalf("mutations should resume after exact Proposal recovery: %v", err)
	}
}

func TestProposalRequestHashRejectsAlteredRetriesAfterReplay(t *testing.T) {
	eventStore := store.NewMemory()
	engine := newEngine(t, eventStore, policy.Deterministic{})
	const sessionID = "session.proposal-request-hash"
	if _, err := engine.CreateSession(createRequest(sessionID)); err != nil {
		t.Fatal(err)
	}
	request := proposeRequest(sessionID, "propose.request-hash", 0, nil)
	proposal, duplicate, err := engine.Propose(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if duplicate {
		t.Fatal("first Proposal was reported as a duplicate")
	}
	state, err := engine.State(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	if receipt := state.Receipts[request.RequestID]; len(receipt.RequestHash) != 64 {
		t.Fatalf("Proposal receipt did not retain a SHA-256 request hash: %+v", receipt)
	}
	invalidState := state
	invalidState.Receipts = make(map[string]protocol.RequestReceipt, len(state.Receipts))
	for requestID, receipt := range state.Receipts {
		invalidState.Receipts[requestID] = receipt
	}
	invalidReceipt := invalidState.Receipts[request.RequestID]
	invalidReceipt.RequestHash = "NOT-A-SHA256-DIGEST"
	invalidState.Receipts[request.RequestID] = invalidReceipt
	if err := protocol.ValidateSessionState(invalidState); err == nil {
		t.Fatal("session validation accepted an invalid Proposal request hash")
	}

	repeated, duplicate, err := engine.Propose(context.Background(), request)
	if err != nil || !duplicate || !reflect.DeepEqual(repeated, proposal) {
		t.Fatalf("exact Proposal retry was not idempotent: proposal=%+v duplicate=%v err=%v", repeated, duplicate, err)
	}
	altered := request
	altered.Intent = "A different payload must not reuse the persisted request id."
	if _, _, err := engine.Propose(context.Background(), altered); err == nil ||
		rinruntime.ErrorCode(err) != "request_id_conflict" {
		t.Fatalf("altered live retry did not conflict: %v", err)
	}

	reopened := newEngine(t, eventStore, policy.Deterministic{})
	if _, _, err := reopened.Propose(context.Background(), altered); err == nil ||
		rinruntime.ErrorCode(err) != "request_id_conflict" {
		t.Fatalf("altered replayed retry did not conflict: %v", err)
	}
	replayed, duplicate, err := reopened.Propose(context.Background(), request)
	if err != nil || !duplicate || !reflect.DeepEqual(replayed, proposal) {
		t.Fatalf("exact replayed retry was not idempotent: proposal=%+v duplicate=%v err=%v", replayed, duplicate, err)
	}
}

func TestCreateReconcilesPostWriteErrorWithoutRestart(t *testing.T) {
	eventStore := newAmbiguousCreateStore(store.NewMemory())
	eventStore.failAfterWrite(false)
	engine := newEngine(t, eventStore, policy.Deterministic{})
	request := createRequest("session.atomic-create")

	result, err := engine.CreateSession(request)
	if err != nil {
		t.Fatalf("engine should reconcile a fully written create event: %v", err)
	}
	if result.Duplicate || result.Revision != 1 {
		t.Fatalf("unexpected reconciled create result: %+v", result)
	}
	if calls := eventStore.createCallCount(); calls != 2 {
		t.Fatalf("create reconciliation used %d calls, want write plus exact confirmation", calls)
	}
	events, err := eventStore.Load(request.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("create reconciliation persisted %d events, want 1", len(events))
	}
	repeated, err := engine.CreateSession(request)
	if err != nil {
		t.Fatalf("registered create retry should be idempotent: %v", err)
	}
	if !repeated.Duplicate || repeated.Revision != result.Revision {
		t.Fatalf("registered create retry mismatch: first=%+v repeated=%+v", result, repeated)
	}
	reopened := newEngine(t, eventStore, policy.Deterministic{})
	if _, err := reopened.State(sessionRequest(request.SessionID)); err != nil {
		t.Fatalf("reconciled create did not replay after restart: %v", err)
	}
}

func TestDefiniteCreateFailureDoesNotLeavePendingIdentity(t *testing.T) {
	eventStore := &definiteCreateFailureStore{Store: store.NewMemory(), fail: true}
	engine := newEngine(t, eventStore, policy.Deterministic{})
	request := createRequest("session.atomic-definite-create-failure")
	if _, err := engine.CreateSession(request); !errors.Is(err, errInjectedAppend) ||
		rinruntime.ErrorCode(err) != "store_create_failed" {
		t.Fatalf("definite pre-write create failure = %v, want store_create_failed", err)
	}
	eventStore.fail = false
	altered := request
	altered.Seed++
	if _, err := engine.CreateSession(altered); err != nil {
		t.Fatalf("definite failure incorrectly retained a pending request identity: %v", err)
	}
}

func TestCreateReconciliationNeverPublishesUnverifiedLoadedEvent(t *testing.T) {
	for _, test := range staleHashEventMutations() {
		t.Run(test.name, func(t *testing.T) {
			delegate := store.NewMemory()
			eventStore := &tamperedConfirmationStore{Store: delegate}
			eventStore.failAfterNextCreate(test.mutate)
			engine := newEngine(t, eventStore, policy.Deterministic{})
			request := createRequest("session.atomic-create-tamper-" + test.name)

			if _, err := engine.CreateSession(request); err == nil {
				t.Fatalf("unverified loaded create should fail reconciliation: %v", err)
			}
			if _, err := engine.State(sessionRequest(request.SessionID)); rinruntime.ErrorCode(err) != "session_not_found" {
				t.Fatalf("unverified loaded create registered live state: %v", err)
			}
			events, err := delegate.Load(request.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 1 {
				t.Fatalf("post-write store contains %d events, want 1", len(events))
			}
		})
	}
}

func TestCreateRetryRecoversAfterConfirmationFailure(t *testing.T) {
	eventStore := newAmbiguousCreateStore(store.NewMemory())
	eventStore.failAfterWrite(true)
	engine := newEngine(t, eventStore, policy.Deterministic{})
	request := createRequest("session.atomic-create-retry")

	if _, err := engine.CreateSession(request); !errors.Is(err, errInjectedAppend) ||
		rinruntime.ErrorCode(err) != "mutation_outcome_unknown" {
		t.Fatalf("failed create confirmation should be reported: %v", err)
	}
	if _, err := engine.State(sessionRequest(request.SessionID)); rinruntime.ErrorCode(err) != "session_not_found" {
		t.Fatalf("unconfirmed create must not register live state: %v", err)
	}
	result, err := engine.CreateSession(request)
	if err != nil {
		t.Fatalf("same-engine retry should reconcile persisted create: %v", err)
	}
	if result.Duplicate {
		t.Fatalf("first registered result is not a client duplicate: %+v", result)
	}
	if calls := eventStore.createCallCount(); calls != 3 {
		t.Fatalf("create recovery used %d calls, want failed write/confirm plus exact-event retry", calls)
	}
	events, err := eventStore.Load(request.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("create recovery persisted %d events, want 1", len(events))
	}
	reopened := newEngine(t, eventStore, policy.Deterministic{})
	if _, err := reopened.State(sessionRequest(request.SessionID)); err != nil {
		t.Fatalf("recovered create did not replay after restart: %v", err)
	}
}

func TestPendingCreateBlocksOtherMutationsWithOutcomeUnknown(t *testing.T) {
	eventStore := newAmbiguousCreateStore(store.NewMemory())
	eventStore.failAfterWrite(true)
	engine := newEngine(t, eventStore, policy.Deterministic{})
	request := createRequest("session.atomic-create-barrier")

	if _, err := engine.CreateSession(request); rinruntime.ErrorCode(err) != "mutation_outcome_unknown" {
		t.Fatalf("failed create confirmation = %v, want mutation_outcome_unknown", err)
	}
	if _, err := engine.Observe(observeRequest(
		request.SessionID,
		"observe.atomic-create-barrier",
		"event.atomic-create-barrier",
		1,
	)); rinruntime.ErrorCode(err) != "mutation_outcome_unknown" || !errors.Is(err, rinruntime.ErrConflict) {
		t.Fatalf("observation crossed pending create barrier: %v", err)
	}
	if _, _, err := engine.Propose(
		context.Background(),
		proposeRequest(request.SessionID, "propose.atomic-create-barrier", 0, nil),
	); rinruntime.ErrorCode(err) != "mutation_outcome_unknown" || !errors.Is(err, rinruntime.ErrConflict) {
		t.Fatalf("proposal crossed pending create barrier: %v", err)
	}
}

func TestFreshRestoreRetryRecoversAfterConfirmationFailure(t *testing.T) {
	const sessionID = "session.atomic-fresh-restore"
	source := newEngine(t, store.NewMemory(), policy.Deterministic{})
	if _, err := source.CreateSession(createRequest(sessionID)); err != nil {
		t.Fatal(err)
	}
	snapshot, err := source.Snapshot(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}

	eventStore := newAmbiguousCreateStore(store.NewMemory())
	eventStore.failAfterWrite(true)
	engine := newEngine(t, eventStore, policy.Deterministic{})
	request := protocol.RestoreRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
		RequestID:       "restore.atomic-fresh",
		Snapshot:        snapshot,
	}
	if _, err := engine.Restore(request); !errors.Is(err, errInjectedAppend) ||
		rinruntime.ErrorCode(err) != "mutation_outcome_unknown" {
		t.Fatalf("failed fresh-restore confirmation should be reported: %v", err)
	}
	if _, err := engine.State(sessionRequest(sessionID)); rinruntime.ErrorCode(err) != "session_not_found" {
		t.Fatalf("unconfirmed fresh restore must not register live state: %v", err)
	}
	result, err := engine.Restore(request)
	if err != nil {
		t.Fatalf("same-engine restore retry should reconcile persisted event: %v", err)
	}
	if result.Duplicate {
		t.Fatalf("first registered restore result is not a client duplicate: %+v", result)
	}
	if calls := eventStore.createCallCount(); calls != 3 {
		t.Fatalf("fresh-restore recovery used %d create calls, want 3", calls)
	}
	state, err := engine.State(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	if receipt := state.Receipts[request.RequestID]; receipt.Kind != rinruntime.EventSessionRestored {
		t.Fatalf("fresh restore receipt was not reconciled: %+v", receipt)
	}
	events, err := eventStore.Load(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("fresh restore recovery persisted %d events, want 1", len(events))
	}
	reopened := newEngine(t, eventStore, policy.Deterministic{})
	if _, err := reopened.State(sessionRequest(sessionID)); err != nil {
		t.Fatalf("recovered fresh restore did not replay after restart: %v", err)
	}
}

func assertAcceptedOutcomeOnce(
	t *testing.T,
	engine *rinruntime.Engine,
	sessionID string,
	actorID string,
	proposalID string,
	eventID string,
) {
	t.Helper()
	state, err := engine.State(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	if proposal := state.Proposals[proposalID]; proposal.Status != "accepted" {
		t.Fatalf("proposal %q status = %q, want accepted", proposalID, proposal.Status)
	}
	actor := state.Actors[actorID]
	memoryCount := 0
	for _, memory := range actor.Memories {
		if memory.EventID == eventID {
			memoryCount++
		}
	}
	if memoryCount != 1 {
		t.Fatalf("event %q appears in actor memory %d times, want exactly once", eventID, memoryCount)
	}
	recentCount := 0
	for _, action := range actor.RecentActions {
		if action.ID == proposalID {
			recentCount++
		}
	}
	if recentCount != 1 {
		t.Fatalf("proposal %q appears in recent actions %d times, want exactly once", proposalID, recentCount)
	}
}

var errInjectedAppend = errors.New("injected append failure")

type failOnceAppendStore struct {
	rinruntime.Store

	mu       sync.Mutex
	failNext bool
}

type failAfterAppendOnceStore struct {
	rinruntime.Store

	mu          sync.Mutex
	failNext    bool
	appendCalls int
}

type failAfterAppendAndConfirmationStore struct {
	rinruntime.Store

	mu            sync.Mutex
	failStage     int
	appendCalls   int
	forceConflict bool
}

type corruptProposalReconcileOnceStore struct {
	rinruntime.Store

	mu          sync.Mutex
	failNext    bool
	corruptNext bool
}

type failProposalBeforeWriteAndLoadOnceStore struct {
	rinruntime.Store

	mu          sync.Mutex
	failAppend  bool
	failLoad    bool
	appendCalls int
}

type changingAtomicPolicy struct {
	mu    sync.Mutex
	calls int
}

type ambiguousCreateStore struct {
	rinruntime.Store

	mu                   sync.Mutex
	failStage            int
	failConfirmationOnce bool
	createCalls          int
}

type definiteCreateFailureStore struct {
	rinruntime.Store
	fail bool
}

func (s *definiteCreateFailureStore) Create(
	sessionID string,
	event protocol.EventRecord,
) error {
	if s.fail {
		return errInjectedAppend
	}
	return s.Store.Create(sessionID, event)
}

type tamperedConfirmationStore struct {
	rinruntime.Store

	mu             sync.Mutex
	failCreate     func(*protocol.EventRecord)
	failAppend     func(*protocol.EventRecord)
	tamperNextLoad func(*protocol.EventRecord)
}

func newFailOnceAppendStore(delegate rinruntime.Store) *failOnceAppendStore {
	return &failOnceAppendStore{Store: delegate}
}

func newFailAfterAppendOnceStore(delegate rinruntime.Store) *failAfterAppendOnceStore {
	return &failAfterAppendOnceStore{Store: delegate}
}

func newFailAfterAppendAndConfirmationStore(delegate rinruntime.Store) *failAfterAppendAndConfirmationStore {
	return &failAfterAppendAndConfirmationStore{Store: delegate}
}

func newCorruptProposalReconcileOnceStore(delegate rinruntime.Store) *corruptProposalReconcileOnceStore {
	return &corruptProposalReconcileOnceStore{Store: delegate}
}

func newFailProposalBeforeWriteAndLoadOnceStore(
	delegate rinruntime.Store,
) *failProposalBeforeWriteAndLoadOnceStore {
	return &failProposalBeforeWriteAndLoadOnceStore{Store: delegate}
}

func newAmbiguousCreateStore(delegate rinruntime.Store) *ambiguousCreateStore {
	return &ambiguousCreateStore{Store: delegate}
}

func (s *ambiguousCreateStore) failAfterWrite(failConfirmation bool) {
	s.mu.Lock()
	s.failStage = 1
	s.failConfirmationOnce = failConfirmation
	s.createCalls = 0
	s.mu.Unlock()
}

func (s *ambiguousCreateStore) Create(sessionID string, event protocol.EventRecord) error {
	s.mu.Lock()
	s.createCalls++
	stage := s.failStage
	switch stage {
	case 1:
		s.failStage = 2
	case 2:
		s.failStage = 0
	}
	failConfirmation := s.failConfirmationOnce
	s.mu.Unlock()

	switch stage {
	case 1:
		if err := s.Store.Create(sessionID, event); err != nil {
			return err
		}
		return errInjectedAppend
	case 2:
		if failConfirmation {
			return errInjectedAppend
		}
		return s.Store.Create(sessionID, event)
	default:
		return s.Store.Create(sessionID, event)
	}
}

func (s *ambiguousCreateStore) createCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.createCalls
}

func (s *tamperedConfirmationStore) failAfterNextCreate(mutate func(*protocol.EventRecord)) {
	s.mu.Lock()
	s.failCreate = mutate
	s.mu.Unlock()
}

func (s *tamperedConfirmationStore) failAfterNextAppend(mutate func(*protocol.EventRecord)) {
	s.mu.Lock()
	s.failAppend = mutate
	s.mu.Unlock()
}

func (s *tamperedConfirmationStore) Create(sessionID string, event protocol.EventRecord) error {
	s.mu.Lock()
	mutate := s.failCreate
	s.failCreate = nil
	s.mu.Unlock()
	if mutate == nil {
		return s.Store.Create(sessionID, event)
	}
	if err := s.Store.Create(sessionID, event); err != nil {
		return err
	}
	s.mu.Lock()
	s.tamperNextLoad = mutate
	s.mu.Unlock()
	return errInjectedAppend
}

func (s *tamperedConfirmationStore) Append(sessionID string, event protocol.EventRecord) error {
	s.mu.Lock()
	mutate := s.failAppend
	s.failAppend = nil
	s.mu.Unlock()
	if mutate == nil {
		return s.Store.Append(sessionID, event)
	}
	if err := s.Store.Append(sessionID, event); err != nil {
		return err
	}
	s.mu.Lock()
	s.tamperNextLoad = mutate
	s.mu.Unlock()
	return errInjectedAppend
}

func (s *tamperedConfirmationStore) Load(sessionID string) ([]protocol.EventRecord, error) {
	events, err := s.Store.Load(sessionID)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	mutate := s.tamperNextLoad
	s.tamperNextLoad = nil
	s.mu.Unlock()
	if mutate == nil || len(events) == 0 {
		return events, nil
	}
	result := make([]protocol.EventRecord, len(events))
	for index, event := range events {
		event.Data = append([]byte(nil), event.Data...)
		result[index] = event
	}
	mutate(&result[len(result)-1])
	return result, nil
}

func (s *failOnceAppendStore) failNextAppend() {
	s.mu.Lock()
	s.failNext = true
	s.mu.Unlock()
}

func (s *failOnceAppendStore) Append(sessionID string, event protocol.EventRecord) error {
	s.mu.Lock()
	if s.failNext {
		s.failNext = false
		s.mu.Unlock()
		return errInjectedAppend
	}
	s.mu.Unlock()
	return s.Store.Append(sessionID, event)
}

func (s *failAfterAppendOnceStore) failAfterNextAppend() {
	s.mu.Lock()
	s.failNext = true
	s.appendCalls = 0
	s.mu.Unlock()
}

func (s *failAfterAppendOnceStore) Append(sessionID string, event protocol.EventRecord) error {
	s.mu.Lock()
	s.appendCalls++
	fail := s.failNext
	s.failNext = false
	s.mu.Unlock()
	if err := s.Store.Append(sessionID, event); err != nil {
		return err
	}
	if fail {
		return errInjectedAppend
	}
	return nil
}

func (s *failAfterAppendOnceStore) appendCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendCalls
}

func (s *failAfterAppendAndConfirmationStore) failPostWriteAndConfirmation() {
	s.mu.Lock()
	s.failStage = 1
	s.appendCalls = 0
	s.mu.Unlock()
}

func (s *failAfterAppendAndConfirmationStore) Append(sessionID string, event protocol.EventRecord) error {
	s.mu.Lock()
	s.appendCalls++
	forceConflict := s.forceConflict
	s.forceConflict = false
	stage := s.failStage
	if stage > 0 {
		s.failStage++
		if s.failStage > 2 {
			s.failStage = 0
		}
	}
	s.mu.Unlock()
	if forceConflict {
		return rinruntime.ErrConflict
	}
	switch stage {
	case 1:
		if err := s.Store.Append(sessionID, event); err != nil {
			return err
		}
		return errInjectedAppend
	case 2:
		return errInjectedAppend
	default:
		return s.Store.Append(sessionID, event)
	}
}

func (s *failAfterAppendAndConfirmationStore) forceNextAppendConflict() {
	s.mu.Lock()
	s.forceConflict = true
	s.mu.Unlock()
}

func (s *failAfterAppendAndConfirmationStore) appendCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendCalls
}

func (s *corruptProposalReconcileOnceStore) failAfterWriteAndCorruptLoad() {
	s.mu.Lock()
	s.failNext = true
	s.mu.Unlock()
}

func (s *corruptProposalReconcileOnceStore) Append(
	sessionID string,
	event protocol.EventRecord,
) error {
	s.mu.Lock()
	fail := s.failNext && event.Type == rinruntime.EventProposed
	if fail {
		s.failNext = false
		s.corruptNext = true
	}
	s.mu.Unlock()
	if err := s.Store.Append(sessionID, event); err != nil {
		return err
	}
	if fail {
		return errInjectedAppend
	}
	return nil
}

func (s *corruptProposalReconcileOnceStore) Load(sessionID string) ([]protocol.EventRecord, error) {
	events, err := s.Store.Load(sessionID)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	corrupt := s.corruptNext
	s.corruptNext = false
	s.mu.Unlock()
	if corrupt && len(events) > 0 {
		events[len(events)-1].Hash = "corrupt-reconciliation-hash"
	}
	return events, nil
}

func (s *failProposalBeforeWriteAndLoadOnceStore) failProposalAndLoad() {
	s.mu.Lock()
	s.failAppend = true
	s.appendCalls = 0
	s.mu.Unlock()
}

func (s *failProposalBeforeWriteAndLoadOnceStore) Append(
	sessionID string,
	event protocol.EventRecord,
) error {
	s.mu.Lock()
	s.appendCalls++
	fail := s.failAppend && event.Type == rinruntime.EventProposed
	if fail {
		s.failAppend = false
		s.failLoad = true
	}
	s.mu.Unlock()
	if fail {
		return errInjectedAppend
	}
	return s.Store.Append(sessionID, event)
}

func (s *failProposalBeforeWriteAndLoadOnceStore) Load(
	sessionID string,
) ([]protocol.EventRecord, error) {
	s.mu.Lock()
	fail := s.failLoad
	s.failLoad = false
	s.mu.Unlock()
	if fail {
		return nil, errInjectedAppend
	}
	return s.Store.Load(sessionID)
}

func (s *failProposalBeforeWriteAndLoadOnceStore) appendCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendCalls
}

func (p *changingAtomicPolicy) Propose(
	_ context.Context,
	_ rinruntime.PolicyContext,
) (rinruntime.ProposalDraft, error) {
	p.mu.Lock()
	p.calls++
	call := p.calls
	p.mu.Unlock()
	actionID := "talk"
	if call > 1 {
		actionID = "wait"
	}
	return rinruntime.ProposalDraft{
		ActionID:  actionID,
		Stance:    "engage",
		Summary:   "A deliberately changing policy result.",
		Rationale: "Used to prove an uncertain append retry does not invoke policy twice.",
	}, nil
}

func (p *changingAtomicPolicy) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func staleHashEventMutations() []struct {
	name   string
	mutate func(*protocol.EventRecord)
} {
	return []struct {
		name   string
		mutate func(*protocol.EventRecord)
	}{
		{
			name: "data-bytes",
			mutate: func(event *protocol.EventRecord) {
				event.Data = append(event.Data, ' ')
			},
		},
		{
			name: "type",
			mutate: func(event *protocol.EventRecord) {
				event.Type += ".tampered"
			},
		},
		{
			name: "request-id",
			mutate: func(event *protocol.EventRecord) {
				event.RequestID += ".tampered"
			},
		},
		{
			name: "prev-hash",
			mutate: func(event *protocol.EventRecord) {
				event.PrevHash += "0"
			},
		},
		{
			name: "recorded-at",
			mutate: func(event *protocol.EventRecord) {
				event.RecordedAt += "0"
			},
		},
	}
}
