package taskstate_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/sunrioa/rin/taskstate"
)

func TestTerminalPlansRemainQueryableWithoutConsumingActiveCapacity(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "plans.db")
	store, err := taskstate.OpenSQLiteStore(path, taskstate.StoreConfig{MaxPlans: 1})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		id := fmt.Sprintf("%02d", i)
		plan, err := store.Create(ctx, testDraft("plan."+id, "task."+id))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.SetStatus(ctx, taskstate.StatusInput{PlanID: plan.PlanID, ExpectedRevision: plan.Revision, Status: taskstate.PlanCancelled, Summary: "Cancelled before execution."}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = taskstate.OpenSQLiteStore(path, taskstate.StoreConfig{MaxPlans: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Get(ctx, "plan.00"); err != nil {
		t.Fatalf("archived Plan lost: %v", err)
	}
	if _, err := store.Create(ctx, testDraft("plan.new", "task.new")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, testDraft("plan.excess", "task.excess")); !errors.Is(err, taskstate.ErrCapacity) {
		t.Fatalf("active capacity bypassed: %v", err)
	}
	listed, err := store.List(ctx)
	if err != nil || len(listed) != 2 {
		t.Fatalf("unbounded management history: %d %v", len(listed), err)
	}
}
