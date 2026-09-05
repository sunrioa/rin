package taskstate

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sunrioa/rin/internal/sqlitedsn"
)

func TestMigrateV1PreservesPlanAndEvidence(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "plans.db")
	store, err := OpenSQLiteStore(path, StoreConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	plan, err := store.Create(ctx, httpTestDraft())
	if err != nil {
		t.Fatal(err)
	}
	link := OperationLink{OperationID: "op.migration", PlanID: plan.PlanID, PlanRevision: plan.Revision, StepID: plan.CurrentStepID, ConditionIDs: []string{"condition.collected"}}
	if err := store.LinkOperation(ctx, link); err != nil {
		t.Fatal(err)
	}
	plan, err = store.Get(ctx, plan.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	events, err := store.Events(ctx, plan.PlanID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	// Recreate the v1-only projection and version on a populated database.
	if _, err := store.db.Exec(`CREATE TABLE task_plan_steps (
 plan_id TEXT NOT NULL REFERENCES task_plans(plan_id) ON DELETE CASCADE,
 step_id TEXT NOT NULL, ordinal INTEGER NOT NULL, status TEXT NOT NULL,
 step_json TEXT NOT NULL, PRIMARY KEY(plan_id, step_id), UNIQUE(plan_id, ordinal)) STRICT`); err != nil {
		t.Fatal(err)
	}
	for i, step := range plan.Steps {
		payload, err := json.Marshal(step)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(`INSERT INTO task_plan_steps VALUES (?, ?, ?, ?, ?)`, plan.PlanID, step.StepID, i, string(step.Status), string(payload)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.Exec(`PRAGMA user_version = 1`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	for reopen := 0; reopen < 2; reopen++ {
		store, err = OpenSQLiteStore(path, StoreConfig{})
		if err != nil {
			t.Fatal(err)
		}
		restored, err := store.Get(ctx, plan.PlanID)
		if err != nil || !reflect.DeepEqual(restored, plan) {
			t.Fatalf("plan changed across migration: %v", err)
		}
		restoredEvents, err := store.Events(ctx, plan.PlanID, 0, 100)
		if err != nil || !reflect.DeepEqual(restoredEvents, events) {
			t.Fatalf("events changed across migration: %v", err)
		}
		var links int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM task_plan_operations
			WHERE operation_id = ? AND plan_id = ? AND plan_revision = ? AND step_id = ?`,
			link.OperationID, link.PlanID, link.PlanRevision, link.StepID).Scan(&links); err != nil {
			t.Fatal(err)
		}
		if links != 1 {
			t.Fatal("migration lost the existing operation link")
		}
		if err := store.LinkOperation(ctx, link); err != nil {
			t.Fatalf("operation link lost idempotency: %v", err)
		}
		var tables, version int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'task_plan_steps'`).Scan(&tables); err != nil {
			t.Fatal(err)
		}
		if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
			t.Fatal(err)
		}
		if tables != 0 || version != 2 {
			t.Fatalf("tables=%d version=%d", tables, version)
		}
		if reopen == 1 {
			_, err := store.SetStatus(ctx, StatusInput{PlanID: plan.PlanID, ExpectedRevision: plan.Revision + 1, Status: PlanPaused, Summary: "stale"})
			if !errors.Is(err, ErrConflict) {
				t.Fatalf("stale CAS accepted after migration: %v", err)
			}
			if _, err := store.SetStatus(ctx, StatusInput{PlanID: plan.PlanID, ExpectedRevision: plan.Revision, Status: PlanPaused, Summary: "pause"}); err != nil {
				t.Fatalf("update without steps table: %v", err)
			}
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStoreRejectsFutureSchemaWithoutChangingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "future.db")
	db, err := sql.Open("sqlite", sqlitedsn.File(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA user_version = 3`); err != nil {
		t.Fatal(err)
	}
	if store, err := OpenSQLiteStore(path, StoreConfig{}); err == nil {
		store.Close()
		t.Fatal("future schema accepted")
	}
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 3 {
		t.Fatalf("future version changed to %d", version)
	}
}
