package cognition

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/sunrioa/rin/internal/jsonwire"
	"github.com/sunrioa/rin/internal/privatefile"
	"github.com/sunrioa/rin/internal/sqlitestore"
)

const TaskSQLiteSchemaVersion = 1

// SQLiteTaskStore commits the changed Task and snapshot revision together. The
// in-memory projection serves scheduler reads; it is never acknowledged before
// the FULL-synchronous transaction commits.
type SQLiteTaskStore struct {
	mu           sync.Mutex
	db           *sql.DB
	local        *LocalTaskStore
	locks        []*os.File
	rowBytes     map[string]int64
	totalBytes   int64
	closed       bool
	writeBlocked error
}

// OpenSQLiteTaskStore imports a same-name .json snapshot exactly once. Both
// writer locks remain held so the legacy store cannot run beside this writer.
// The original JSON remains an untouched migration backup, not a live replica.
func OpenSQLiteTaskStore(path string, maxTasks uint32) (*SQLiteTaskStore, error) {
	if path == "" {
		return nil, errors.New("task database path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	legacy := strings.TrimSuffix(absolute, filepath.Ext(absolute)) + ".json"
	if absolute == legacy {
		return nil, errors.New("task database must have a non-JSON extension")
	}
	if err := prepareProviderStorePath(absolute, ErrTaskStorePersistence, "task database"); err != nil {
		return nil, err
	}
	store := &SQLiteTaskStore{rowBytes: make(map[string]int64)}
	for _, lockPath := range []string{legacy + ".lock", absolute + ".lock"} {
		lock, err := acquireProviderStoreLock(lockPath, ErrTaskStoreLocked, ErrTaskStorePersistence, "task database")
		if err != nil {
			_ = store.Close()
			return nil, err
		}
		store.locks = append(store.locks, lock)
	}
	store.db, err = sqlitestore.Open(absolute)
	if err == nil {
		err = store.initialize(legacy, maxTasks)
	}
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("%w: initialize SQLite tasks: %w", ErrTaskStorePersistence, err)
	}
	return store, nil
}

func (store *SQLiteTaskStore) initialize(legacy string, maxTasks uint32) error {
	var version int
	if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version < 0 || version > TaskSQLiteSchemaVersion {
		return errors.New("unsupported task database schema")
	}
	if version == 0 {
		var snapshot TaskSnapshot
		err := privatefile.ReadJSON(legacy, maxTaskSnapshotBytes, &snapshot)
		if errors.Is(err, os.ErrNotExist) {
			store.local, err = NewLocalTaskStore(maxTasks)
			if err == nil {
				snapshot, err = store.local.Snapshot(context.Background())
			}
		} else if err == nil {
			store.local, err = RestoreLocalTaskStore(maxTasks, snapshot)
		}
		if err != nil {
			return err
		}
		// Normalize legacy v3/v4 control state before the migration commit.
		snapshot, err = store.local.Snapshot(context.Background())
		if err != nil {
			return err
		}
		tx, err := store.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		for _, statement := range []string{
			`CREATE TABLE task_meta (singleton INTEGER PRIMARY KEY CHECK(singleton=1), snapshot_version TEXT NOT NULL, revision INTEGER NOT NULL CHECK(revision>0)) STRICT`,
			`CREATE TABLE task_sessions (task_id TEXT PRIMARY KEY, revision INTEGER NOT NULL CHECK(revision>0), payload BLOB NOT NULL) STRICT`,
		} {
			if _, err := tx.Exec(statement); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`INSERT INTO task_meta VALUES(1,?,?)`, TaskSnapshotVersion, snapshot.Revision); err != nil {
			return err
		}
		for _, task := range snapshot.Tasks {
			payload, err := json.Marshal(task)
			if err != nil {
				return err
			}
			store.rowBytes[task.TaskID] = int64(len(payload))
			store.totalBytes += int64(len(payload))
			if store.totalBytes > maxTaskSnapshotBytes {
				return ErrProviderCapacity
			}
			if _, err := tx.Exec(`INSERT INTO task_sessions VALUES(?,?,?)`, task.TaskID, task.Revision, payload); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`PRAGMA user_version=1`); err != nil {
			return err
		}
		return tx.Commit()
	}
	var snapshot TaskSnapshot
	if err := store.db.QueryRow(`SELECT snapshot_version,revision FROM task_meta WHERE singleton=1`).Scan(&snapshot.Version, &snapshot.Revision); err != nil {
		return err
	}
	var count int
	if err := store.db.QueryRow(`SELECT count(*),coalesce(sum(length(payload)),0) FROM task_sessions`).Scan(&count, &store.totalBytes); err != nil {
		return err
	}
	limit, err := NewLocalTaskStore(maxTasks)
	if err != nil {
		return err
	}
	if count > int(limit.maxTasks) || store.totalBytes > maxTaskSnapshotBytes {
		return ErrProviderCapacity
	}
	rows, err := store.db.Query(`SELECT task_id,revision,payload FROM task_sessions ORDER BY task_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var revision uint64
		var payload []byte
		if err := rows.Scan(&id, &revision, &payload); err != nil {
			return err
		}
		if err := jsonwire.Validate(payload); err != nil {
			return err
		}
		var task TaskSession
		decoder := json.NewDecoder(bytes.NewReader(payload))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&task); err != nil {
			return err
		}
		if task.TaskID != id || task.Revision != revision {
			return errors.New("task database row identity mismatch")
		}
		snapshot.Tasks = append(snapshot.Tasks, task)
		store.rowBytes[id] = int64(len(payload))
	}
	if err := rows.Err(); err != nil {
		return err
	}
	store.local, err = RestoreLocalTaskStore(maxTasks, snapshot)
	return err
}

func (store *SQLiteTaskStore) Create(ctx context.Context, task TaskSession) (TaskSession, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.ready(); err != nil {
		return TaskSession{}, err
	}
	created, err := store.local.Create(ctx, task)
	if err != nil {
		return TaskSession{}, err
	}
	if err := store.persistChanged(created, 0); err != nil {
		return TaskSession{}, err
	}
	return created, nil
}

func (store *SQLiteTaskStore) CompareAndSwap(ctx context.Context, revision uint64, task TaskSession) (TaskSession, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.ready(); err != nil {
		return TaskSession{}, err
	}
	updated, err := store.local.CompareAndSwap(ctx, revision, task)
	if err != nil {
		return TaskSession{}, err
	}
	if err := store.persistChanged(updated, revision); err != nil {
		return TaskSession{}, err
	}
	return updated, nil
}

func (store *SQLiteTaskStore) Load(ctx context.Context, id string) (TaskSession, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.ready(); err != nil {
		return TaskSession{}, err
	}
	return store.local.Load(ctx, id)
}

func (store *SQLiteTaskStore) Snapshot(ctx context.Context) (TaskSnapshot, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.ready(); err != nil {
		return TaskSnapshot{}, err
	}
	return store.local.Snapshot(ctx)
}

func (store *SQLiteTaskStore) persistChanged(task TaskSession, previous uint64) (err error) {
	// A failed/ambiguous commit poisons the cache. Reopening rebuilds solely from
	// durable rows; no read can expose an uncommitted in-memory CAS.
	defer func() {
		if err != nil {
			store.writeBlocked = fmt.Errorf("%w: %w", ErrTaskStorePersistence, err)
			err = store.writeBlocked
		}
	}()
	payload, err := json.Marshal(task)
	if err != nil {
		return err
	}
	total := store.totalBytes - store.rowBytes[task.TaskID] + int64(len(payload))
	if total > maxTaskSnapshotBytes {
		return ErrProviderCapacity
	}
	tx, err := store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if previous == 0 {
		_, err = tx.Exec(`INSERT INTO task_sessions VALUES(?,?,?)`, task.TaskID, task.Revision, payload)
	} else {
		var result sql.Result
		result, err = tx.Exec(`UPDATE task_sessions SET revision=?,payload=? WHERE task_id=? AND revision=?`, task.Revision, payload, task.TaskID, previous)
		if err == nil {
			var count int64
			count, err = result.RowsAffected()
			if err == nil && count != 1 {
				err = ErrTaskRevisionConflict
			}
		}
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE task_meta SET revision=?,snapshot_version=? WHERE singleton=1`, store.local.revision, TaskSnapshotVersion); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	store.totalBytes = total
	store.rowBytes[task.TaskID] = int64(len(payload))
	return nil
}

func (store *SQLiteTaskStore) ready() error {
	if store.closed {
		return ErrTaskStoreClosed
	}
	return store.writeBlocked
}

func (store *SQLiteTaskStore) Close() error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closed {
		return nil
	}
	store.closed = true
	var errs []error
	if store.db != nil {
		errs = append(errs, store.db.Close())
	}
	for i := len(store.locks) - 1; i >= 0; i-- {
		errs = append(errs, releaseProviderStoreLock(store.locks[i]))
	}
	return errors.Join(errs...)
}
