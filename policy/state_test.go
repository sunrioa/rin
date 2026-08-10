package policy

import (
	"testing"
)

func TestPolicyStateRestoresAndFinalizesReservations(t *testing.T) {
	config := testConfig(ProfileOpen)
	config.Budgets = []Budget{{
		BudgetID:    "actor.action-limit",
		Layer:       LayerActor,
		EffectKinds: []string{"world.position"},
		MaxActions:  2,
	}}
	firstEngine := newTestEngine(t, config)
	firstAction := testBoundAction(t, testEffect())
	firstDecision, err := firstEngine.Evaluate(firstAction, testContext(firstAction))
	if err != nil || firstDecision.Result != Allow {
		t.Fatalf("first Evaluate = %#v, %v", firstDecision, err)
	}
	checkpoint := firstEngine.SnapshotState()
	if len(checkpoint.Usage) != 1 || len(checkpoint.Reservations) != 1 {
		t.Fatalf("active checkpoint = %#v", checkpoint)
	}

	restored := newTestEngine(t, config)
	if err := restored.RestoreState(checkpoint); err != nil {
		t.Fatalf("RestoreState: %v", err)
	}
	if !restored.Finalize(firstDecision.DecisionID, false) {
		t.Fatal("restored reservation was not rolled back")
	}
	rolledBack := restored.SnapshotState()
	if len(rolledBack.Usage) != 0 || len(rolledBack.Reservations) != 0 {
		t.Fatalf("rolled back checkpoint = %#v", rolledBack)
	}

	secondDecision, err := restored.Evaluate(firstAction, testContext(firstAction))
	if err != nil || secondDecision.Result != Allow {
		t.Fatalf("second Evaluate = %#v, %v", secondDecision, err)
	}
	if !restored.Finalize(secondDecision.DecisionID, true) {
		t.Fatal("committed reservation was not finalized")
	}
	committed := restored.SnapshotState()
	if len(committed.Usage) != 1 || len(committed.Reservations) != 0 ||
		committed.Usage[0].Actions != 1 {
		t.Fatalf("committed checkpoint = %#v", committed)
	}
	third := newTestEngine(t, config)
	if err := third.RestoreState(committed); err != nil {
		t.Fatalf("RestoreState committed: %v", err)
	}
}

func TestPolicyStateForExcludesUnregisteredReservationUsage(t *testing.T) {
	config := testConfig(ProfileOpen)
	config.Budgets = []Budget{{
		BudgetID:    "actor.action-limit",
		Layer:       LayerActor,
		EffectKinds: []string{"world.position"},
		MaxActions:  2,
	}}
	engine := newTestEngine(t, config)
	action := testBoundAction(t, testEffect())
	decision, err := engine.Evaluate(action, testContext(action))
	if err != nil || decision.Result != Allow {
		t.Fatalf("Evaluate = %#v, %v", decision, err)
	}

	excluded := engine.SnapshotStateFor(nil)
	if len(excluded.Usage) != 0 || len(excluded.Reservations) != 0 {
		t.Fatalf("excluded checkpoint = %#v", excluded)
	}
	included := engine.SnapshotStateFor([]string{decision.DecisionID})
	if len(included.Usage) != 1 || len(included.Reservations) != 1 {
		t.Fatalf("included checkpoint = %#v", included)
	}
}

func TestPolicyStateRejectsInconsistentReservation(t *testing.T) {
	state := State{
		Version:        StateVersion,
		PolicyRevision: 1,
		ConfigDigest:   "0000000000000000000000000000000000000000000000000000000000000000",
		Usage: []UsageCheckpoint{{
			Key: "bucket\x00one", Actions: 1,
		}},
		Reservations: []ReservationCheckpoint{{
			DecisionID: "decision.one",
			Deltas: []UsageCheckpoint{{
				Key: "bucket\x00one", Actions: 2,
			}},
		}},
	}
	if err := ValidateState(state); err == nil {
		t.Fatal("inconsistent policy reservation was accepted")
	}
	config := testConfig(ProfileOpen)
	engine := newTestEngine(t, config)
	state.ConfigDigest = engine.SnapshotState().ConfigDigest
	state.Reservations[0].Deltas[0].Actions = 1
	state.PolicyRevision = 2
	if err := engine.RestoreState(state); err == nil {
		t.Fatal("mismatched policy revision was accepted")
	}
}

func TestPolicyStateRejectsChangedConfigAtSameRevision(t *testing.T) {
	config := testConfig(ProfileOpen)
	first := newTestEngine(t, config)
	checkpoint := first.SnapshotState()
	config.Profile = ProfileGuarded
	changed := newTestEngine(t, config)
	if err := changed.RestoreState(checkpoint); err == nil {
		t.Fatal("policy state restored into a different config with the same revision")
	}
}
