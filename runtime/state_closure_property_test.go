package runtime

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/sunrioa/rin/protocol"
)

func TestMutationStateClosureProperty(t *testing.T) {
	for seed := byte(0); seed < 8; seed++ {
		operations := make([]byte, 64)
		value := uint32(seed) + 1
		for index := range operations {
			value = value*1664525 + 1013904223
			operations[index] = byte(value >> 24)
		}
		t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) {
			runMutationStateClosureSequence(t, operations)
		})
	}
}

func FuzzMutationStateClosure(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6, 7})
	f.Add([]byte{1, 1, 1, 2, 2, 3, 4, 5, 6})
	f.Add([]byte{6, 5, 4, 3, 2, 1, 0})
	f.Fuzz(func(t *testing.T, operations []byte) {
		if len(operations) > 64 {
			operations = operations[:64]
		}
		runMutationStateClosureSequence(t, operations)
	})
}

func runMutationStateClosureSequence(t *testing.T, operations []byte) {
	t.Helper()
	const sessionID = "session.state-closure"
	engine, eventStore := invariantEngine(
		t,
		sessionID,
		protocol.SupportedFeatures(),
		nil,
		invariantPolicy{},
	)
	assertEngineStateClosure(t, engine, sessionID)

	for index, operation := range operations {
		suffix := fmt.Sprintf("%03d", index)
		switch operation % 8 {
		case 0:
			before := mustEngineState(t, engine, sessionID)
			tick := before.Tick + 1
			request := invariantObserve(
				sessionID,
				"observe.property."+suffix,
				"event.observe.property."+suffix,
				tick,
			)
			request.Facts = []protocol.Fact{{
				SubjectID:  "subject." + suffix,
				Predicate:  "state",
				Object:     fmt.Sprintf("value-%d", operation),
				Visibility: []string{"npc.mira"},
				Confidence: 80,
			}}
			_, err := engine.Observe(request)
			assertEngineTransitionClosure(t, engine, sessionID, before, err)

		case 1:
			before := mustEngineState(t, engine, sessionID)
			tick := before.Tick + 1
			request := invariantPropose(sessionID, "propose.commit.property."+suffix, nil)
			request.Tick = tick
			request.Urgent = true
			proposal, _, err := engine.Propose(context.Background(), request)
			assertEngineTransitionClosure(t, engine, sessionID, before, err)
			if err != nil {
				continue
			}
			before = mustEngineState(t, engine, sessionID)
			_, err = engine.Commit(protocol.CommitRequest{
				ProtocolVersion: protocol.Version,
				SessionID:       sessionID,
				RequestID:       "commit.property." + suffix,
				ProposalID:      proposal.ID,
				EventID:         "event.commit.property." + suffix,
				Tick:            tick,
				Accepted:        true,
				Outcome:         "The property-test action occurred.",
				Facts: []protocol.Fact{{
					SubjectID:  "action." + suffix,
					Predicate:  "outcome",
					Object:     "accepted",
					Visibility: []string{"npc.mira"},
					Confidence: 100,
				}},
			})
			assertEngineTransitionClosure(t, engine, sessionID, before, err)

		case 2:
			before := mustEngineState(t, engine, sessionID)
			tick := before.Tick + 1
			request := invariantPropose(sessionID, "propose.batch.property."+suffix, nil)
			request.Tick = tick
			request.Urgent = true
			proposal, _, err := engine.Propose(context.Background(), request)
			assertEngineTransitionClosure(t, engine, sessionID, before, err)
			if err != nil {
				continue
			}
			before = mustEngineState(t, engine, sessionID)
			accepted := (operation>>3)&1 == 0
			outcome := ""
			if accepted {
				outcome = "The property-test batch action occurred."
			}
			_, err = engine.CommitBatch(protocol.BatchCommitRequest{
				ProtocolVersion: protocol.Version,
				SessionID:       sessionID,
				RequestID:       "batch.property." + suffix,
				Tick:            tick,
				Items: []protocol.CommitItem{{
					ProposalID: proposal.ID,
					EventID:    "event.batch.property." + suffix,
					Accepted:   accepted,
					Outcome:    outcome,
				}},
			})
			assertEngineTransitionClosure(t, engine, sessionID, before, err)

		case 3:
			before := mustEngineState(t, engine, sessionID)
			tick := before.Tick + 1
			request := invariantPropose(sessionID, "propose.arbitrate.property."+suffix, nil)
			request.Tick = tick
			request.Urgent = true
			proposal, _, err := engine.Propose(context.Background(), request)
			assertEngineTransitionClosure(t, engine, sessionID, before, err)
			if err != nil {
				continue
			}
			before = mustEngineState(t, engine, sessionID)
			_, _, err = engine.Arbitrate(protocol.ArbitrateRequest{
				ProtocolVersion: protocol.Version,
				SessionID:       sessionID,
				RequestID:       "arbitrate.property." + suffix,
				Tick:            tick,
				ProposalIDs:     []string{proposal.ID},
			})
			assertEngineTransitionClosure(t, engine, sessionID, before, err)
			if err != nil {
				continue
			}
			before = mustEngineState(t, engine, sessionID)
			_, err = engine.CommitBatch(protocol.BatchCommitRequest{
				ProtocolVersion: protocol.Version,
				SessionID:       sessionID,
				RequestID:       "batch.arbitrated.property." + suffix,
				Tick:            tick,
				Items: []protocol.CommitItem{{
					ProposalID: proposal.ID,
					EventID:    "event.arbitrated.property." + suffix,
					Accepted:   true,
					Outcome:    "The arbitrated property-test action occurred.",
				}},
			})
			assertEngineTransitionClosure(t, engine, sessionID, before, err)

		case 4:
			before := mustEngineState(t, engine, sessionID)
			_, err := engine.SetActorActivity(protocol.SetActorActivityRequest{
				ProtocolVersion: protocol.Version,
				SessionID:       sessionID,
				RequestID:       "activity.property." + suffix,
				Tick:            before.Tick + 1,
				Updates: []protocol.ActorActivityUpdate{{
					ActorID:  "npc.mira",
					RegionID: "region.test",
					State:    []string{"awake", "dormant"}[int((operation>>3)&1)],
					Reason:   "Property-test lifecycle update.",
				}},
			})
			assertEngineTransitionClosure(t, engine, sessionID, before, err)

		case 5:
			before := mustEngineState(t, engine, sessionID)
			snapshot, err := SnapshotOf(before)
			if err != nil {
				t.Fatal(err)
			}
			_, err = engine.Restore(protocol.RestoreRequest{
				ProtocolVersion: protocol.Version,
				SessionID:       sessionID,
				RequestID:       "restore.property." + suffix,
				ExpectedBinding: snapshot.State.Binding,
				Snapshot:        snapshot,
			})
			assertEngineTransitionClosure(t, engine, sessionID, before, err)

		case 6:
			before := mustEngineState(t, engine, sessionID)
			tick := before.Tick + 1
			request := invariantPropose(sessionID, "propose.reject.property."+suffix, nil)
			request.Tick = tick
			request.Urgent = true
			proposal, _, err := engine.Propose(context.Background(), request)
			assertEngineTransitionClosure(t, engine, sessionID, before, err)
			if err != nil {
				continue
			}
			before = mustEngineState(t, engine, sessionID)
			_, err = engine.Commit(protocol.CommitRequest{
				ProtocolVersion: protocol.Version,
				SessionID:       sessionID,
				RequestID:       "commit.reject.property." + suffix,
				ProposalID:      proposal.ID,
				EventID:         "event.reject.property." + suffix,
				Tick:            tick,
				Accepted:        false,
			})
			assertEngineTransitionClosure(t, engine, sessionID, before, err)

		case 7:
			before := mustEngineState(t, engine, sessionID)
			beforeAppends, _ := eventStore.counts()
			var err error
			switch (operation >> 3) & 3 {
			case 0:
				request := invariantObserve(
					sessionID,
					"observe.invalid.property."+suffix,
					"event.invalid.property."+suffix,
					before.Tick,
				)
				request.Facts = []protocol.Fact{{
					SubjectID:  "invalid." + suffix,
					Predicate:  "visibility",
					Object:     "unknown",
					Visibility: []string{"npc.unknown"},
					Confidence: 100,
				}}
				_, err = engine.Observe(request)
			case 1:
				_, err = engine.SetActorActivity(protocol.SetActorActivityRequest{
					ProtocolVersion: protocol.Version,
					SessionID:       sessionID,
					RequestID:       "activity.invalid.property." + suffix,
					Tick:            before.Tick,
					Updates: []protocol.ActorActivityUpdate{{
						ActorID: "npc.unknown",
						State:   "awake",
					}},
				})
			case 2:
				_, err = engine.Commit(protocol.CommitRequest{
					ProtocolVersion: protocol.Version,
					SessionID:       sessionID,
					RequestID:       "commit.invalid.property." + suffix,
					ProposalID:      "proposal.unknown." + suffix,
					EventID:         "event.invalid.property." + suffix,
					Tick:            before.Tick,
					Accepted:        false,
				})
			case 3:
				_, err = engine.CommitBatch(protocol.BatchCommitRequest{
					ProtocolVersion: protocol.Version,
					SessionID:       sessionID,
					RequestID:       "batch.invalid.property." + suffix,
					Tick:            before.Tick,
					Items: []protocol.CommitItem{{
						ProposalID: "proposal.unknown." + suffix,
						EventID:    "event.invalid.property." + suffix,
						Accepted:   false,
					}},
				})
			}
			if err == nil {
				t.Fatal("property sequence expected an invalid mutation to fail")
			}
			assertEngineTransitionClosure(t, engine, sessionID, before, err)
			afterAppends, _ := eventStore.counts()
			if afterAppends != beforeAppends {
				t.Fatalf("failed mutation changed append count from %d to %d", beforeAppends, afterAppends)
			}
		}
	}

	assertSnapshotRestoreRoundTrip(t, engine, sessionID)
}

func assertEngineTransitionClosure(
	t *testing.T,
	engine *Engine,
	sessionID string,
	before protocol.SessionState,
	mutationErr error,
) {
	t.Helper()
	after := mustEngineState(t, engine, sessionID)
	if mutationErr != nil && !reflect.DeepEqual(after, before) {
		t.Fatalf("failed mutation changed state: %v", mutationErr)
	}
	assertStateAndSnapshotClosure(t, after)
}

func assertEngineStateClosure(t *testing.T, engine *Engine, sessionID string) {
	t.Helper()
	assertStateAndSnapshotClosure(t, mustEngineState(t, engine, sessionID))
}

func assertStateAndSnapshotClosure(t *testing.T, state protocol.SessionState) {
	t.Helper()
	if err := protocol.ValidateSessionState(state); err != nil {
		t.Fatalf("successful mutation produced invalid state: %v", err)
	}
	snapshot, err := SnapshotOf(state)
	if err != nil {
		t.Fatalf("valid state could not be snapshotted: %v", err)
	}
	if err := ValidateSnapshot(snapshot); err != nil {
		t.Fatalf("SnapshotOf result did not validate: %v", err)
	}
}

func mustEngineState(t *testing.T, engine *Engine, sessionID string) protocol.SessionState {
	t.Helper()
	state, err := engine.State(protocol.SessionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func assertSnapshotRestoreRoundTrip(t *testing.T, source *Engine, sessionID string) {
	t.Helper()
	sourceState := mustEngineState(t, source, sessionID)
	snapshot, err := SnapshotOf(sourceState)
	if err != nil {
		t.Fatal(err)
	}

	fresh, err := Open(newInvariantStore(), invariantPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.Restore(protocol.RestoreRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
		RequestID:       "restore.fresh.roundtrip",
		ExpectedBinding: snapshot.State.Binding,
		Snapshot:        snapshot,
	}); err != nil {
		t.Fatalf("fresh restore failed: %v", err)
	}
	assertEngineStateClosure(t, fresh, sessionID)

	restoredSnapshot, err := fresh.Snapshot(protocol.SessionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
	})
	if err != nil {
		t.Fatalf("snapshot after fresh restore failed: %v", err)
	}
	if err := ValidateSnapshot(restoredSnapshot); err != nil {
		t.Fatalf("snapshot after fresh restore is invalid: %v", err)
	}
	before := mustEngineState(t, fresh, sessionID)
	_, err = fresh.Restore(protocol.RestoreRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
		RequestID:       "restore.existing.roundtrip",
		ExpectedBinding: restoredSnapshot.State.Binding,
		Snapshot:        restoredSnapshot,
	})
	assertEngineTransitionClosure(t, fresh, sessionID, before, err)
	if err != nil {
		t.Fatalf("existing restore failed: %v", err)
	}
}
