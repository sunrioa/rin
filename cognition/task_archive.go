package cognition

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/sunrioa/rin/internal/jsonwire"
)

func (store *SQLiteTaskStore) initializeArchive() error {
	tx, err := store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS task_archive(task_id TEXT PRIMARY KEY, revision INTEGER NOT NULL CHECK(revision>0), payload BLOB NOT NULL, archived_at INTEGER NOT NULL) STRICT`,
		`CREATE INDEX IF NOT EXISTS task_archive_recent ON task_archive(archived_at DESC, task_id)`,
		`PRAGMA user_version=2`,
	} {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func decodeArchivedTask(id string, revision uint64, payload []byte) (TaskSession, error) {
	var value TaskSession
	if err := jsonwire.Validate(payload); err != nil {
		return value, err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, err
	}
	if value.TaskID != id || value.Revision != revision || taskOccupiesActor(value) {
		return value, ErrTaskStorePersistence
	}
	return sealTaskSession(value)
}

func (store *SQLiteTaskStore) loadArchived(ctx context.Context, id string) (TaskSession, error) {
	if err := requireMemoryContext(ctx); err != nil {
		return TaskSession{}, err
	}
	if err := validateTaskID(id); err != nil {
		return TaskSession{}, err
	}
	var revision uint64
	var payload []byte
	err := store.db.QueryRowContext(ctx, `SELECT revision,CASE WHEN length(payload)<=? THEN payload END FROM task_archive WHERE task_id=?`, maxTaskSnapshotBytes, id).Scan(&revision, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return TaskSession{}, ErrProviderNotFound
	}
	if err != nil {
		return TaskSession{}, err
	}
	return decodeArchivedTask(id, revision, payload)
}

// Archived tasks retain their identity, audit history and Plan references.
// A late skill-learning checkpoint may update them, but never reactivate them.
func (store *SQLiteTaskStore) updateArchived(ctx context.Context, revision uint64, task TaskSession) (TaskSession, error) {
	current, err := store.loadArchived(ctx, task.TaskID)
	if err != nil {
		return TaskSession{}, err
	}
	if current.Revision != revision || task.Revision != revision {
		return TaskSession{}, ErrTaskRevisionConflict
	}
	if task.Status != current.Status || taskOccupiesActor(task) {
		return TaskSession{}, ErrProviderConflict
	}
	task.Revision++
	sealed, err := sealTaskSession(task)
	if err != nil {
		return TaskSession{}, err
	}
	payload, err := json.Marshal(sealed)
	if err != nil || len(payload) > maxTaskSnapshotBytes {
		return TaskSession{}, ErrProviderCapacity
	}
	tx, err := store.db.Begin()
	if err != nil {
		return TaskSession{}, err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`UPDATE task_archive SET revision=?,payload=? WHERE task_id=? AND revision=?`, sealed.Revision, payload, sealed.TaskID, revision)
	if err == nil {
		count, countErr := result.RowsAffected()
		err = countErr
		if err == nil && count != 1 {
			err = ErrTaskRevisionConflict
		}
	}
	if err == nil {
		_, err = tx.Exec(`UPDATE task_meta SET revision=revision+1 WHERE singleton=1`)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		store.writeBlocked = err
		return TaskSession{}, err
	}
	store.local.revision++
	return sealed, nil
}

func (store *SQLiteTaskStore) ArchivedTasks(ctx context.Context, limit uint32) (TaskSnapshot, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.ready(); err != nil {
		return TaskSnapshot{}, err
	}
	if limit == 0 || limit > 500 {
		return TaskSnapshot{}, ErrProviderCapacity
	}
	rows, err := store.db.QueryContext(ctx, `SELECT task_id,revision,CASE WHEN length(payload)<=? THEN payload END FROM task_archive ORDER BY archived_at DESC,task_id LIMIT ?`, maxTaskSnapshotBytes, limit)
	if err != nil {
		return TaskSnapshot{}, err
	}
	defer rows.Close()
	result := TaskSnapshot{Version: TaskSnapshotVersion, Revision: store.local.revision, Tasks: []TaskSession{}}
	readBytes := 0
	for rows.Next() {
		var id string
		var revision uint64
		var payload []byte
		if err := rows.Scan(&id, &revision, &payload); err != nil {
			return TaskSnapshot{}, err
		}
		readBytes += len(payload)
		if readBytes > maxTaskSnapshotBytes {
			return TaskSnapshot{}, ErrProviderCapacity
		}
		task, err := decodeArchivedTask(id, revision, payload)
		if err != nil {
			return TaskSnapshot{}, err
		}
		result.Tasks = append(result.Tasks, task)
	}
	return result, rows.Err()
}

func (store *SQLiteTaskStore) SchedulingSnapshot(ctx context.Context) (TaskSnapshot, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.ready(); err != nil {
		return TaskSnapshot{}, err
	}
	return store.local.SchedulingSnapshot(ctx)
}

func (store *SQLiteTaskStore) ActorTasks(ctx context.Context, hostID, worldID, actorID string) (TaskSnapshot, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.ready(); err != nil {
		return TaskSnapshot{}, err
	}
	return store.local.ActorTasks(ctx, hostID, worldID, actorID)
}
