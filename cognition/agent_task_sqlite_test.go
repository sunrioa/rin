package cognition

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/internal/privatefile"
)

func sqliteTestTask() TaskSession {
	epoch := host.Epoch{SessionID: "session.sqlite", WorldID: "world.sqlite", Host: 1, World: 1, Timeline: 1}
	return TaskSession{TaskID: "task.sqlite", SessionID: epoch.SessionID, HostID: "host.sqlite", WorldID: epoch.WorldID, ActorID: "actor.sqlite", ControllerID: "controller.sqlite", Goal: "Wait for the player.", Status: TaskActive,
		ControllerLease: controlplane.ControllerLease{LeaseID: "lease.sqlite", ControllerID: "controller.sqlite", PrincipalID: "principal.sqlite", HostID: "host.sqlite", WorldID: epoch.WorldID, ActorID: "actor.sqlite", Source: controlplane.DecisionInternal, PersonaMode: controlplane.PersonaCharacterBound, AuthorityRevision: 1, Epoch: epoch, AcquiredAtUnixMillis: 1, ExpiresAtUnixMillis: 60001}, CreatedAtUnixMillis: 10, UpdatedAtUnixMillis: 10}
}

func TestSQLiteTaskStoreMigratesLegacyWaitExactlyOnce(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "tasks.json")
	path := filepath.Join(root, "tasks.db")
	local, _ := NewLocalTaskStore(10)
	task := sqliteTestTask()
	task.LastObservationID = "observation.sqlite"
	task.LastObservationSeq = 2
	task.History = []TaskEvent{{Kind: "task.wait", AtUnixMillis: 10}}
	if _, err := local.Create(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := local.Snapshot(context.Background())
	snapshot.Version = legacyTaskSnapshotVersion
	snapshot.Tasks[0].Schedule = TaskSchedule{}
	if err := privatefile.WriteJSON(legacy, snapshot); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(legacy)
	store, err := OpenSQLiteTaskStore(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFileTaskStore(legacy, 10); !errors.Is(err, ErrTaskStoreLocked) {
		t.Fatalf("legacy writer was not locked: %v", err)
	}
	if _, err := OpenSQLiteTaskStore(path, 10); !errors.Is(err, ErrTaskStoreLocked) {
		t.Fatalf("second SQLite writer: %v", err)
	}
	loaded, err := store.Load(context.Background(), task.TaskID)
	if err != nil || loaded.Schedule.Kind != ScheduleObservation || loaded.Completion.Mode != CompletionModel {
		t.Fatalf("migration lost task semantics: %#v %v", loaded, err)
	}
	loaded.Goal = "An updated durable goal."
	updated, err := store.CompareAndSwap(context.Background(), loaded.Revision, loaded)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompareAndSwap(context.Background(), loaded.Revision, loaded); !errors.Is(err, ErrTaskRevisionConflict) {
		t.Fatalf("stale CAS = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	backup, _ := os.ReadFile(legacy)
	if string(original) != string(backup) {
		t.Fatal("migration modified the JSON backup")
	}
	if err := os.WriteFile(legacy, []byte(`corrupt obsolete backup`), 0600); err != nil {
		t.Fatal(err)
	}
	store, err = OpenSQLiteTaskStore(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	loaded, err = store.Load(context.Background(), task.TaskID)
	if err != nil || loaded.Goal != updated.Goal || loaded.Revision != updated.Revision {
		t.Fatalf("reopen reimported backup: %#v %v", loaded, err)
	}
	var synchronous int
	var journal string
	if err := store.db.QueryRow(`PRAGMA synchronous`).Scan(&synchronous); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`PRAGMA journal_mode`).Scan(&journal); err != nil {
		t.Fatal(err)
	}
	if synchronous != 2 || journal != "wal" {
		t.Fatalf("durability mode = %d %s", synchronous, journal)
	}
}

func TestSQLiteTaskCommitFailureCannotExposeUncommittedCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.db")
	store, err := OpenSQLiteTaskStore(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.Create(context.Background(), sqliteTestTask())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER reject_meta BEFORE UPDATE ON task_meta BEGIN SELECT RAISE(ABORT,'injected checkpoint failure'); END`); err != nil {
		t.Fatal(err)
	}
	task.Goal = "This must not become visible."
	if _, err := store.CompareAndSwap(context.Background(), task.Revision, task); !errors.Is(err, ErrTaskStorePersistence) {
		t.Fatalf("commit error = %v", err)
	}
	if _, err := store.Load(context.Background(), task.TaskID); !errors.Is(err, ErrTaskStorePersistence) {
		t.Fatalf("uncommitted cache remained readable: %v", err)
	}
	var payload []byte
	if err := store.db.QueryRow(`SELECT payload FROM task_sessions WHERE task_id=?`, task.TaskID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var durable TaskSession
	if err := json.Unmarshal(payload, &durable); err != nil {
		t.Fatal(err)
	}
	if durable.Revision != 1 || durable.Goal == task.Goal {
		t.Fatal("Task row committed without its snapshot revision")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenSQLiteTaskStore(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	loaded, err := reopened.Load(context.Background(), task.TaskID)
	if err != nil || loaded.Goal != durable.Goal || loaded.Revision != durable.Revision {
		t.Fatalf("recovery = %#v %v", loaded, err)
	}
}

func TestSQLiteTaskStoreRejectsMismatchedRowIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.db")
	store, err := OpenSQLiteTaskStore(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(context.Background(), sqliteTestTask()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE task_sessions SET revision=revision+1`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenSQLiteTaskStore(path, 10); !errors.Is(err, ErrTaskStorePersistence) {
		t.Fatalf("inconsistent persisted task accepted: %v", err)
	}
}
