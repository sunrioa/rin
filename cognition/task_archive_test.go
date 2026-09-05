package cognition_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/controlplane"
)

func TestCompletedTasksArchiveWithoutExhaustingCapacity(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "tasks.db")
	store, err := cognition.OpenSQLiteTaskStore(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		task := validTaskSession(fmt.Sprintf("task.%02d", i))
		task.Status, task.Schedule = cognition.TaskCompleted, cognition.TaskSchedule{Kind: cognition.ScheduleStopped}
		task.UpdatedAtUnixMillis += int64(i)
		if _, err := store.Create(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Load(ctx, "task.00"); err != nil {
		t.Fatalf("archived task lost: %v", err)
	}
	if _, err := store.Create(ctx, validTaskSession("task.00")); !errors.Is(err, cognition.ErrProviderConflict) {
		t.Fatalf("archived identity reused: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = cognition.OpenSQLiteTaskStore(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	old, err := store.Load(ctx, "task.00")
	if err != nil || old.Status != cognition.TaskCompleted {
		t.Fatalf("reopened archive: %v %v", old.Status, err)
	}
	old.SkillLearning = &cognition.SkillLearningState{Status: cognition.SkillLearningSkipped, Attempts: 1, Code: "below-threshold"}
	updated, err := store.CompareAndSwap(ctx, old.Revision, old)
	if err != nil || updated.Revision != old.Revision+1 {
		t.Fatalf("late learning checkpoint: %v", err)
	}
	archives, err := store.ArchivedTasks(ctx, 3)
	if err != nil || len(archives.Tasks) != 3 {
		t.Fatalf("bounded archive query: %v %v", len(archives.Tasks), err)
	}
	for _, id := range []string{"task.active-one", "task.active-two"} {
		if _, err := store.Create(ctx, validTaskSession(id)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Create(ctx, validTaskSession("task.active-three")); !errors.Is(err, cognition.ErrProviderCapacity) {
		t.Fatalf("active task cap bypassed: %v", err)
	}
}

func TestSchedulingIndexOmitsFinishedHistoryAndReturnsDefensiveCopies(t *testing.T) {
	ctx := context.Background()
	store, _ := cognition.NewLocalTaskStore(32)
	for i := 0; i < 100; i++ {
		task := validTaskSession(fmt.Sprintf("task.history.%03d", i))
		task.Status, task.Schedule = cognition.TaskCompleted, cognition.TaskSchedule{Kind: cognition.ScheduleStopped}
		if _, err := store.Create(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	active, err := store.Create(ctx, validTaskSession("task.active"))
	if err != nil {
		t.Fatal(err)
	}
	epoch := active.ControllerLease.Epoch
	active.Schedule = cognition.TaskSchedule{Kind: cognition.ScheduleObservation, ObservationEpoch: &epoch, AfterObservationSequence: 1}
	if _, err := store.CompareAndSwap(ctx, active.Revision, active); err != nil {
		t.Fatal(err)
	}
	index, err := store.SchedulingSnapshot(ctx)
	if err != nil || len(index.Tasks) != 1 || len(index.Tasks[0].History) != 0 {
		t.Fatalf("scheduler copied historical tasks: %#v %v", index, err)
	}
	index.Tasks[0].Schedule.ObservationEpoch.Host++
	loaded, _ := store.Load(ctx, active.TaskID)
	if loaded.Schedule.ObservationEpoch.Host != epoch.Host {
		t.Fatal("index mutated stored schedule")
	}
}

func TestSchedulingSelectionTracksActorOperationAndTerminalTransitions(t *testing.T) {
	ctx := context.Background()
	store, _ := cognition.NewLocalTaskStore(32)
	for i := 0; i < 12; i++ {
		task := validTaskSession(fmt.Sprintf("task.index.%02d", i))
		task.ActorID = fmt.Sprintf("actor.%02d", i)
		task.ControllerLease.ActorID = task.ActorID
		task.MacroOperationID = fmt.Sprintf("operation.%02d", i)
		task.Schedule = cognition.TaskSchedule{Kind: cognition.ScheduleOperation, OperationID: task.MacroOperationID}
		if _, err := store.Create(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	selectTasks := func(filter cognition.TaskSchedulingFilter) cognition.TaskSnapshot {
		t.Helper()
		page, err := store.SchedulingSelection(ctx, filter)
		if err != nil {
			t.Fatal(err)
		}
		return page
	}
	actorFilter := cognition.TaskSchedulingFilter{Changes: []controlplane.SchedulingChange{{Target: controlplane.ActorControlTarget{HostID: "host.test", WorldID: "world.test", ActorID: "actor.03"}}}}
	page := selectTasks(actorFilter)
	if len(page.Tasks) != 1 || page.Tasks[0].TaskID != "task.index.03" {
		t.Fatalf("actor invalidation scanned unrelated tasks: %#v", page)
	}
	operationFilter := cognition.TaskSchedulingFilter{Changes: []controlplane.SchedulingChange{{OperationID: "operation.03"}}}
	if page := selectTasks(operationFilter); len(page.Tasks) != 1 {
		t.Fatalf("operation invalidation: %#v", page)
	}
	task, _ := store.Load(ctx, "task.index.03")
	task.Schedule.OperationID = "operation.changed"
	task.MacroOperationID = "operation.changed"
	task, err := store.CompareAndSwap(ctx, task.Revision, task)
	if err != nil {
		t.Fatal(err)
	}
	if page := selectTasks(operationFilter); len(page.Tasks) != 0 {
		t.Fatal("old operation index remained")
	}
	task.Status = cognition.TaskCompleted
	task.MacroOperationID = ""
	task.Schedule = cognition.TaskSchedule{Kind: cognition.ScheduleStopped}
	if _, err := store.CompareAndSwap(ctx, task.Revision, task); err != nil {
		t.Fatal(err)
	}
	if page := selectTasks(actorFilter); len(page.Tasks) != 0 {
		t.Fatal("settled history was scheduled")
	}
}

func BenchmarkTaskSchedulingSelection(b *testing.B) {
	ctx := context.Background()
	store, _ := cognition.NewLocalTaskStore(1024)
	for i := 0; i < 1000; i++ {
		task := validTaskSession(fmt.Sprintf("task.scan.%04d", i))
		task.ActorID = fmt.Sprintf("actor.%04d", i)
		task.ControllerLease.ActorID = task.ActorID
		for j := 0; j < 64; j++ {
			task.History = append(task.History, cognition.TaskEvent{Kind: "task.created", Summary: "Retained task history.", AtUnixMillis: 10})
		}
		if _, err := store.Create(ctx, task); err != nil {
			b.Fatal(err)
		}
	}
	filter := cognition.TaskSchedulingFilter{Changes: []controlplane.SchedulingChange{{Target: controlplane.ActorControlTarget{HostID: "host.test", WorldID: "world.test", ActorID: "actor.0500"}}}}
	for _, mode := range []string{"full-snapshot", "targeted-actor"} {
		b.Run(mode, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				var err error
				if mode == "full-snapshot" {
					_, err = store.Snapshot(ctx)
				} else {
					_, err = store.SchedulingSelection(ctx, filter)
				}
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
