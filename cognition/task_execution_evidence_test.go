package cognition

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

func longEvidenceTask() TaskSession {
	task := sqliteTestTask()
	task.Revision = 1
	task.Budget = TaskBudget{MaxSteps: 512, MaxModelCalls: 512, MaxActions: 512, MaxModelTokens: 100_000}
	appendTaskEvent(&task, TaskEvent{Kind: "task.created", AtUnixMillis: 10})
	for i := 0; i < 300; i++ {
		task.ActionCount++
		appendTaskEvent(&task, TaskEvent{Kind: "action.selected", Code: "activity.wait", AtUnixMillis: 10})
		appendTaskEvent(&task, TaskEvent{Kind: "operation.terminal", Code: "succeeded", OperationID: fmt.Sprintf("operation.evidence.%d", i), AtUnixMillis: 10})
	}
	task.Status = TaskCompleted
	appendTaskEvent(&task, TaskEvent{Kind: "task.completed", AtUnixMillis: 10})
	return task
}

func TestTaskExecutionEvidenceSurvivesRetentionAndSQLiteReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "tasks.db")
	store, err := OpenSQLiteTaskStore(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	input := longEvidenceTask()
	input.Revision = 0
	task, err := store.Create(ctx, input)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if len(task.History) != 512 || task.History[0].Sequence <= 1 {
		t.Fatal("fixture did not exceed history retention")
	}
	// A returned copy cannot alter the authoritative aggregate.
	task.ExecutionEvidence.SelectedCapabilities[0] = "tampered.capability"
	task.ExecutionEvidence.SuccessfulOperationCount = 0
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenSQLiteTaskStore(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	loaded, err := store.Load(ctx, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	e := loaded.ExecutionEvidence
	if e == nil || !e.Complete || e.EventSequence != 602 || e.SelectedActionCount != 300 ||
		e.TerminalOperationCount != 300 || e.SuccessfulOperationCount != 300 || e.TaskCompletedCount != 1 ||
		e.SelectedCapabilities[0] != "activity.wait" || e.LastOperationID != "operation.evidence.299" {
		t.Fatalf("durable evidence changed: %+v", e)
	}
}

func TestLegacyTaskEvidenceNeverTreatsTruncatedHistoryAsComplete(t *testing.T) {
	task := longEvidenceTask()
	task.ExecutionEvidence = nil
	appendTaskEvent(&task, TaskEvent{Kind: "skill.learning-skipped", AtUnixMillis: 10})
	if task.ExecutionEvidence.Complete {
		t.Fatal("fabricated complete evidence from a retained history tail")
	}
	if _, err := sealTaskSession(task); err != nil {
		t.Fatalf("legacy task became unreadable: %v", err)
	}
	full := sqliteTestTask()
	full.History = []TaskEvent{{Sequence: 1, Kind: "task.created", AtUnixMillis: 10}}
	full.EventSequence = 1
	appendTaskEvent(&full, TaskEvent{Kind: "task.resumed", AtUnixMillis: 10})
	if !full.ExecutionEvidence.Complete || full.ExecutionEvidence.EventSequence != 2 {
		t.Fatal("lost complete legacy history")
	}
	store, _ := NewLocalTaskStore(10)
	_, err := store.Create(context.Background(), full)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _ := store.Snapshot(context.Background())
	snapshot.Version = lookaheadTaskSnapshotVersion
	snapshot.Tasks[0].ExecutionEvidence = nil
	if _, err := RestoreLocalTaskStore(10, snapshot); err != nil {
		t.Fatalf("v6 snapshot migration: %v", err)
	}
}

func TestTaskExecutionEvidenceCountsMacrosAndInvalidatedIntents(t *testing.T) {
	task := sqliteTestTask()
	task.Revision = 1
	appendTaskEvent(&task, TaskEvent{Kind: "task.created", AtUnixMillis: 10})
	task.ActionCount++
	appendTaskEvent(&task, TaskEvent{Kind: "action.selected", Code: "macro.collect", AtUnixMillis: 10})
	appendTaskEvent(&task, TaskEvent{Kind: "macro.started", OperationID: "operation.parent", AtUnixMillis: 10})
	task.ActionCount++
	appendTaskEvent(&task, TaskEvent{Kind: "action.selected", Code: "resource.pickup", AtUnixMillis: 10})
	appendTaskEvent(&task, TaskEvent{Kind: "operation.terminal", Code: "failed", OperationID: "operation.child", AtUnixMillis: 10})
	appendTaskEvent(&task, TaskEvent{Kind: "macro.terminal", Code: "succeeded", OperationID: "operation.parent", AtUnixMillis: 10})
	task.ActionCount++
	appendTaskEvent(&task, TaskEvent{Kind: "action.selected", Code: "resource.pickup", AtUnixMillis: 10})
	appendTaskEvent(&task, TaskEvent{Kind: "action.invalidated", Code: "stale", AtUnixMillis: 10})
	task.Status = TaskCompleted
	appendTaskEvent(&task, TaskEvent{Kind: "task.completed", AtUnixMillis: 10})
	if _, err := sealTaskSession(task); err != nil {
		t.Fatal(err)
	}
	e := task.ExecutionEvidence
	if e.SelectedActionCount != 3 || e.TerminalOperationCount != 2 || e.RejectedActionCount != 1 ||
		e.SuccessfulOperationCount != 1 || e.StartedMacroCount != 1 || e.TerminalMacroCount != 1 {
		t.Fatalf("incorrect aggregate: %+v", e)
	}
	broken := cloneTaskSession(task)
	broken.ExecutionEvidence.RejectedActionCount = 0
	if _, err := sealTaskSession(broken); err == nil {
		t.Fatal("accepted completed task with unresolved evidence")
	}
	if task.ExecutionEvidence.RejectedActionCount != 1 {
		t.Fatal("cloning aliased execution evidence")
	}
}
