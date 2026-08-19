package taskstate_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/taskstate"
)

func TestSQLiteStorePersistsCASAndSingleActiveActorPlan(t *testing.T) {
	path := filepath.Join(t.TempDir(), "taskstate.db")
	clock := time.UnixMilli(10)
	store, err := taskstate.OpenSQLiteStore(path, taskstate.StoreConfig{
		Now: func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := taskstate.OpenSQLiteStore(path, taskstate.StoreConfig{}); !errors.Is(err, taskstate.ErrLocked) {
		t.Fatalf("second writer error = %v", err)
	}
	created, err := store.Create(context.Background(), testDraft("plan.one", "task.one"))
	if err != nil {
		t.Fatal(err)
	}
	plans, err := store.List(context.Background())
	if err != nil || len(plans) != 1 || plans[0].PlanID != created.PlanID {
		t.Fatalf("listed plans = %#v, %v", plans, err)
	}
	if _, err := store.Create(context.Background(), testDraft("plan.two", "task.two")); !errors.Is(err, taskstate.ErrConflict) {
		t.Fatalf("second active actor plan error = %v", err)
	}
	clock = time.UnixMilli(20)
	paused, err := store.SetStatus(context.Background(), taskstate.StatusInput{
		PlanID: created.PlanID, ExpectedRevision: created.Revision,
		Status: taskstate.PlanPaused, Summary: "External controller disconnected.",
	})
	if err != nil || paused.Status != taskstate.PlanPaused || paused.Revision != 2 {
		t.Fatalf("paused = %#v, %v", paused, err)
	}
	if _, err := store.SetStatus(context.Background(), taskstate.StatusInput{
		PlanID: created.PlanID, ExpectedRevision: 1,
		Status: taskstate.PlanActive, Summary: "Stale resume.",
	}); !errors.Is(err, taskstate.ErrConflict) {
		t.Fatalf("stale CAS error = %v", err)
	}
	clock = time.UnixMilli(30)
	resumed, err := store.SetStatus(context.Background(), taskstate.StatusInput{
		PlanID: created.PlanID, ExpectedRevision: paused.Revision,
		Status: taskstate.PlanActive, Summary: "Controller resumed after revalidation.",
	})
	if err != nil || resumed.Status != taskstate.PlanActive {
		t.Fatalf("resumed = %#v, %v", resumed, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = taskstate.OpenSQLiteStore(path, taskstate.StoreConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	restored, err := store.Get(context.Background(), created.PlanID)
	if err != nil || restored.Revision != resumed.Revision || restored.Status != taskstate.PlanActive {
		t.Fatalf("restored = %#v, %v", restored, err)
	}
}

func TestSQLiteStoreLinksOneOperationAndAdvancesFromOutcome(t *testing.T) {
	clock := time.UnixMilli(10)
	store, err := taskstate.OpenSQLiteStore(
		filepath.Join(t.TempDir(), "taskstate.db"),
		taskstate.StoreConfig{Now: func() time.Time { return clock }},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	plan, err := store.Create(context.Background(), testDraft("plan.one", "task.one"))
	if err != nil {
		t.Fatal(err)
	}
	link := taskstate.OperationLink{
		OperationID: "operation.one", PlanID: plan.PlanID, PlanRevision: plan.Revision,
		StepID: plan.CurrentStepID, ConditionIDs: []string{"condition.collected"},
	}
	if err := store.LinkOperation(context.Background(), link); err != nil {
		t.Fatal(err)
	}
	if err := store.LinkOperation(context.Background(), link); err != nil {
		t.Fatalf("idempotent link: %v", err)
	}
	other := link
	other.OperationID = "operation.two"
	if err := store.LinkOperation(context.Background(), other); !errors.Is(err, taskstate.ErrConflict) {
		t.Fatalf("parallel operation error = %v", err)
	}
	clock = time.UnixMilli(20)
	outcome := host.ActionOutcome{
		OperationID: link.OperationID, Status: host.ActionSucceeded,
		Summary: "The Host collected the requested material.", Epoch: plan.BasedOnEpoch,
		WorldSeq: 5, OccurredAt: host.Timepoint{Clock: host.ClockStep, Value: 20},
	}
	advanced, changed, err := store.ApplyOperationResult(context.Background(), taskstate.OperationResult{
		OperationID: link.OperationID, ExecutionConfirmed: true, Outcome: outcome,
	})
	if err != nil || !changed || advanced.CurrentStepID != "step.return" ||
		advanced.Steps[0].Status != taskstate.StepCompleted {
		t.Fatalf("advanced = %#v, %v, %v", advanced, changed, err)
	}
	replayed, changed, err := store.ApplyOperationResult(context.Background(), taskstate.OperationResult{
		OperationID: link.OperationID, ExecutionConfirmed: true, Outcome: outcome,
	})
	if err != nil || changed || replayed.Revision != advanced.Revision {
		t.Fatalf("replayed = %#v, %v, %v", replayed, changed, err)
	}
	page, err := store.Events(context.Background(), plan.PlanID, 0, 16)
	if err != nil || len(page.Events) != 2 || page.Events[1].Kind != "operation.outcome" {
		t.Fatalf("events = %#v, %v", page, err)
	}
}

func TestSQLiteStoreWaitAndRevisionRefuseUnfinishedOperation(t *testing.T) {
	clock := time.UnixMilli(10)
	store, err := taskstate.OpenSQLiteStore(
		filepath.Join(t.TempDir(), "taskstate.db"),
		taskstate.StoreConfig{Now: func() time.Time { return clock }},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	plan, err := store.Create(context.Background(), testDraft("plan.one", "task.one"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.LinkOperation(context.Background(), taskstate.OperationLink{
		OperationID: "operation.running", PlanID: plan.PlanID, PlanRevision: plan.Revision,
		StepID: plan.CurrentStepID, ConditionIDs: []string{"condition.collected"},
	}); err != nil {
		t.Fatal(err)
	}
	revision := testDraft(plan.PlanID, plan.TaskID)
	revision.Goal = "Collect safer material and return home."
	if _, err := store.Revise(context.Background(), taskstate.ReviseInput{
		PlanID: plan.PlanID, ExpectedRevision: plan.Revision,
		Reason:  taskstate.ReplanRequiredCapabilityMissing,
		Summary: "The running operation still owns the current step.", Draft: revision,
	}); !errors.Is(err, taskstate.ErrConflict) {
		t.Fatalf("revision with unfinished operation error = %v", err)
	}
	waited := make(chan taskstate.PlanUpdate, 1)
	failed := make(chan error, 1)
	go func() {
		update, waitErr := store.Wait(context.Background(), taskstate.WaitInput{
			PlanID: plan.PlanID, AfterRevision: plan.Revision, WaitMillis: 1_000,
		})
		if waitErr != nil {
			failed <- waitErr
			return
		}
		waited <- update
	}()
	time.Sleep(10 * time.Millisecond)
	clock = time.UnixMilli(20)
	paused, err := store.SetStatus(context.Background(), taskstate.StatusInput{
		PlanID: plan.PlanID, ExpectedRevision: plan.Revision,
		Status: taskstate.PlanPaused, Summary: "Pause for the test.",
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-failed:
		t.Fatal(err)
	case update := <-waited:
		if !update.Changed || update.Plan.Revision != paused.Revision {
			t.Fatalf("wait update = %#v", update)
		}
	case <-time.After(time.Second):
		t.Fatal("plan wait did not wake")
	}
}

func TestSQLiteStoreLatencySample(t *testing.T) {
	store, err := taskstate.OpenSQLiteStore(
		filepath.Join(t.TempDir(), "taskstate.db"), taskstate.StoreConfig{},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const samples = 40
	plans := make([]taskstate.PlanState, samples)
	outcomePlans := make([]taskstate.PlanState, samples)
	for index := range plans {
		draft := testDraft(
			fmt.Sprintf("plan.perf.%02d", index), fmt.Sprintf("task.perf.%02d", index),
		)
		draft.ActorID = fmt.Sprintf("actor.perf.%02d", index)
		plans[index], err = store.Create(context.Background(), draft)
		if err != nil {
			t.Fatal(err)
		}
		outcomeDraft := testDraft(
			fmt.Sprintf("plan.outcome.%02d", index), fmt.Sprintf("task.outcome.%02d", index),
		)
		outcomeDraft.ActorID = fmt.Sprintf("actor.outcome.%02d", index)
		outcomePlans[index], err = store.Create(context.Background(), outcomeDraft)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.LinkOperation(context.Background(), taskstate.OperationLink{
			OperationID: fmt.Sprintf("operation.perf.%02d", index),
			PlanID:      outcomePlans[index].PlanID, PlanRevision: outcomePlans[index].Revision,
			StepID: outcomePlans[index].CurrentStepID, ConditionIDs: []string{"condition.collected"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	durations := make([]time.Duration, 0, samples*4)
	for index, plan := range plans {
		started := time.Now()
		if _, err := store.Get(context.Background(), plan.PlanID); err != nil {
			t.Fatal(err)
		}
		durations = append(durations, time.Since(started))

		started = time.Now()
		paused, err := store.SetStatus(context.Background(), taskstate.StatusInput{
			PlanID: plan.PlanID, ExpectedRevision: plan.Revision,
			Status: taskstate.PlanPaused, Summary: "Latency sample pause.",
		})
		if err != nil {
			t.Fatal(err)
		}
		durations = append(durations, time.Since(started))

		started = time.Now()
		_, err = store.SetStatus(context.Background(), taskstate.StatusInput{
			PlanID: plan.PlanID, ExpectedRevision: paused.Revision,
			Status: taskstate.PlanActive, Summary: "Latency sample resume.",
		})
		if err != nil {
			t.Fatal(err)
		}
		durations = append(durations, time.Since(started))

		operationID := fmt.Sprintf("operation.perf.%02d", index)
		outcomePlan := outcomePlans[index]
		started = time.Now()
		if _, changed, err := store.ApplyOperationResult(
			context.Background(), taskstate.OperationResult{
				OperationID: operationID, ExecutionConfirmed: true,
				Outcome: host.ActionOutcome{
					OperationID: operationID, Status: host.ActionSucceeded,
					Summary: "Latency sample outcome.", Epoch: outcomePlan.BasedOnEpoch,
					WorldSeq: 5, OccurredAt: host.Timepoint{Clock: host.ClockStep, Value: 5},
				},
			},
		); err != nil || !changed {
			t.Fatalf("outcome %d: changed=%v err=%v", index, changed, err)
		}
		durations = append(durations, time.Since(started))
	}
	sort.Slice(durations, func(left, right int) bool { return durations[left] < durations[right] })
	p50, p95 := durations[len(durations)/2], durations[len(durations)*95/100]
	t.Logf("SQLite Plan Get/CAS/Outcome sample: p50=%s p95=%s", p50, p95)
	if p95 > 100*time.Millisecond {
		t.Fatalf("Plan store p95 %s exceeds regression guard", p95)
	}
}

func testDraft(planID, taskID string) taskstate.Draft {
	epoch := host.Epoch{
		SessionID: "session.one", WorldID: "world.one", Host: 1, World: 1, Timeline: 1,
	}
	return taskstate.Draft{
		PlanID: planID, TaskID: taskID, SessionID: "session.one",
		HostID: "host.one", WorldID: "world.one", ActorID: "actor.one",
		ControllerID: "controller.one", ControllerSource: taskstate.ControllerExternal,
		Goal: "Collect material and return home.", PlanningMode: taskstate.PlanningAuto,
		Steps: []taskstate.StepDraft{
			{
				StepID: "step.collect", Title: "Collect", Objective: "Collect nearby material.",
				CapabilityHints: []host.CapabilityRef{{ID: "resource.harvest", Version: "1.0.0"}},
				MaxAttempts:     3, SuccessConditions: []taskstate.PlanCondition{{
					ConditionID: "condition.collected", Kind: taskstate.EvidenceOperationOutcome,
					Summary:    "The Host confirms collection.",
					Capability: &host.CapabilityRef{ID: "resource.harvest", Version: "1.0.0"},
				}},
			},
			{
				StepID: "step.return", Title: "Return", Objective: "Return home.",
				CapabilityHints: []host.CapabilityRef{{ID: "navigation.return_home", Version: "1.0.0"}},
				MaxAttempts:     3, SuccessConditions: []taskstate.PlanCondition{{
					ConditionID: "condition.returned", Kind: taskstate.EvidenceOperationOutcome,
					Summary:    "The Host confirms return.",
					Capability: &host.CapabilityRef{ID: "navigation.return_home", Version: "1.0.0"},
				}},
			},
		},
		MaxReplans: 2, BasedOnEpoch: epoch, BasedOnObservationSequence: 4,
	}
}
