package controlplane

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/sunrioa/rin/host"
)

// Archive migration is independent of the bounded hot pool and never deletes
// settled audit records. The pending projection index contains only unacked work.
func (store *operationSQLite) initializeArchive(service *Service) error {
	tx, err := store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS operation_archive(operation_id TEXT PRIMARY KEY,request_key TEXT NOT NULL UNIQUE,payload BLOB NOT NULL,archived_at INTEGER NOT NULL) STRICT`,
		`CREATE TABLE IF NOT EXISTS outcome_backlog(operation_id TEXT NOT NULL,subscriber TEXT NOT NULL,created_at INTEGER NOT NULL,attempts INTEGER NOT NULL DEFAULT 0,last_attempt_at INTEGER NOT NULL DEFAULT 0,next_attempt_at INTEGER NOT NULL DEFAULT 0,last_error TEXT NOT NULL DEFAULT '',PRIMARY KEY(operation_id,subscriber)) STRICT`,
		`CREATE INDEX IF NOT EXISTS outcome_backlog_ready ON outcome_backlog(subscriber,next_attempt_at,last_attempt_at,operation_id)`,
		`PRAGMA user_version=2`,
	} {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	for _, operation := range service.operations {
		if err := syncOutcomeBacklog(tx, operation); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	store.archives = make(map[string]*operationState)
	return nil
}

func syncOutcomeBacklog(tx *sql.Tx, operation *operationState) error {
	for subscriber, delivered := range operation.outcomeDelivery {
		var err error
		if delivered {
			_, err = tx.Exec(`DELETE FROM outcome_backlog WHERE operation_id=? AND subscriber=?`, operation.request.OperationID, subscriber)
		} else {
			_, err = tx.Exec(`INSERT INTO outcome_backlog(operation_id,subscriber,created_at) VALUES(?,?,?) ON CONFLICT DO NOTHING`, operation.request.OperationID, subscriber, operation.updatedAt)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (store *operationSQLite) writeArchives(tx *sql.Tx) error {
	for id, operation := range store.archives {
		payload, err := json.Marshal(persistedOperationValue(operation))
		if err != nil {
			return err
		}
		if len(payload) > maxOperationFileBytes {
			return ErrCapacity
		}
		if _, err := tx.Exec(`INSERT INTO operation_archive VALUES(?,?,?,?) ON CONFLICT(operation_id) DO UPDATE SET payload=excluded.payload`, id, operation.idempotency, payload, operation.updatedAt); err != nil {
			return err
		}
		if err := syncOutcomeBacklog(tx, operation); err != nil {
			return err
		}
	}
	return nil
}

func (service *Service) retireSettledOperationLocked() {
	var oldest *operationState
	for _, operation := range service.operations {
		if !completeOperation(operation) || operation.status == OperationOutcomeUnknown || len(operation.children) != 0 {
			continue
		}
		if oldest == nil || operation.updatedAt < oldest.updatedAt || (operation.updatedAt == oldest.updatedAt && operation.request.OperationID < oldest.request.OperationID) {
			oldest = operation
		}
	}
	if oldest == nil {
		return
	}
	id := oldest.request.OperationID
	service.operationSQLite.archives[id] = oldest
	delete(service.operations, id)
	delete(service.requests, oldest.idempotency)
	if parent := service.operations[oldest.request.ParentOperationID]; parent != nil {
		parent.children = slices.DeleteFunc(parent.children, func(child string) bool { return child == id })
		parent.persistenceRevision++
	}
	service.markOperationsDirtyLocked()
}

func (store *operationSQLite) archivedOperation(id string) (*operationState, error) {
	if staged := store.archives[id]; staged != nil {
		return staged, nil
	}
	var payload []byte
	err := store.db.QueryRow(`SELECT CASE WHEN length(payload)<=? THEN payload END FROM operation_archive WHERE operation_id=?`, maxOperationFileBytes, id).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var saved persistedOperation
	if err := decodeOperationRow(payload, &saved); err != nil {
		return nil, fmt.Errorf("%w: archived operation: %w", ErrPersistence, err)
	}
	if saved.Request.OperationID != id {
		return nil, ErrPersistence
	}
	operation, err := restoreOperation(saved)
	if err != nil || operation == nil || !completeOperation(operation) || operation.status == OperationOutcomeUnknown {
		return nil, ErrPersistence
	}
	return operation, nil
}

func (service *Service) lookupOperationLocked(id string) (*operationState, error) {
	if operation := service.operations[id]; operation != nil {
		return operation, nil
	}
	if service.operationSQLite != nil {
		return service.operationSQLite.archivedOperation(id)
	}
	return nil, ErrNotFound
}

func (store *operationSQLite) archivedRequest(key string) (*operationState, error) {
	for _, operation := range store.archives {
		if operation.idempotency == key {
			return operation, nil
		}
	}
	var id string
	err := store.db.QueryRow(`SELECT operation_id FROM operation_archive WHERE request_key=?`, key).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return store.archivedOperation(id)
}

func (store *operationSQLite) pendingOutcomeIDs(subscriber string, now int64) ([]string, error) {
	rows, err := store.db.Query(`SELECT operation_id FROM outcome_backlog WHERE subscriber=? AND next_attempt_at<=? ORDER BY last_attempt_at,operation_id LIMIT 64`, subscriber, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (service *Service) ackArchivedOutcomeLocked(operation *operationState, subscriber string) error {
	operation.outcomeDelivery[subscriber] = true
	payload, err := json.Marshal(persistedOperationValue(operation))
	if err != nil {
		return err
	}
	tx, err := service.operationSQLite.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE operation_archive SET payload=? WHERE operation_id=?`, payload, operation.request.OperationID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM outcome_backlog WHERE operation_id=? AND subscriber=?`, operation.request.OperationID, subscriber); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%w: acknowledge archived outcome: %w", ErrPersistence, err)
	}
	service.recordOperationChangeLocked(operation.request.OperationID)
	service.notifyLocked()
	return nil
}

// Health data contains stable codes, never arbitrary subscriber error strings.
// Attempt count and retry deadlines survive restart on the default SQLite store.
type OutcomeBacklogEntry struct {
	OperationID   string `json:"operation_id"`
	Subscriber    string `json:"subscriber"`
	CreatedAt     int64  `json:"created_at_unix_millis"`
	Attempts      uint64 `json:"attempts"`
	LastAttemptAt int64  `json:"last_attempt_at_unix_millis"`
	NextAttemptAt int64  `json:"next_attempt_at_unix_millis"`
	LastError     string `json:"last_error_code,omitempty"`
	Configured    bool   `json:"configured"`
}
type OutcomeBacklogHealth struct {
	Pending  uint64                `json:"pending"`
	OldestAt int64                 `json:"oldest_at_unix_millis"`
	Durable  bool                  `json:"durable"`
	Entries  []OutcomeBacklogEntry `json:"entries"`
}
type OutcomeRetryInput struct {
	OperationID string `json:"operation_id"`
	Subscriber  string `json:"subscriber"`
}

func (service *Service) OutcomeBacklog(principal host.Principal) (OutcomeBacklogHealth, error) {
	if err := host.ValidatePrincipal(principal); err != nil {
		return OutcomeBacklogHealth{}, ErrInvalid
	}
	if !hasScope(principal, ScopeHostAdmin) {
		return OutcomeBacklogHealth{}, ErrForbidden
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	result := OutcomeBacklogHealth{Entries: []OutcomeBacklogEntry{}, Durable: service.operationSQLite != nil}
	if err := service.persistOperationsLocked(); err != nil {
		return result, err
	}
	if store := service.operationSQLite; store != nil {
		if err := store.db.QueryRow(`SELECT count(*),coalesce(min(created_at),0) FROM outcome_backlog`).Scan(&result.Pending, &result.OldestAt); err != nil {
			return result, err
		}
		rows, err := store.db.Query(`SELECT operation_id,subscriber,created_at,attempts,last_attempt_at,next_attempt_at,last_error FROM outcome_backlog ORDER BY created_at,operation_id,subscriber LIMIT 100`)
		if err != nil {
			return result, err
		}
		defer rows.Close()
		for rows.Next() {
			var entry OutcomeBacklogEntry
			if err := rows.Scan(&entry.OperationID, &entry.Subscriber, &entry.CreatedAt, &entry.Attempts, &entry.LastAttemptAt, &entry.NextAttemptAt, &entry.LastError); err != nil {
				return result, err
			}
			entry.Configured = service.outcomeSinks[entry.Subscriber] != nil
			result.Entries = append(result.Entries, entry)
		}
		return result, rows.Err()
	}
	for _, operation := range service.operations {
		for subscriber, delivered := range operation.outcomeDelivery {
			if delivered {
				continue
			}
			result.Pending++
			if result.OldestAt == 0 || operation.updatedAt < result.OldestAt {
				result.OldestAt = operation.updatedAt
			}
			result.Entries = append(result.Entries, OutcomeBacklogEntry{OperationID: operation.request.OperationID, Subscriber: subscriber, CreatedAt: operation.updatedAt, Configured: service.outcomeSinks[subscriber] != nil})
		}
	}
	slices.SortFunc(result.Entries, func(a, b OutcomeBacklogEntry) int {
		if a.CreatedAt < b.CreatedAt {
			return -1
		}
		if a.CreatedAt > b.CreatedAt {
			return 1
		}
		return compare(a.OperationID+a.Subscriber, b.OperationID+b.Subscriber)
	})
	if len(result.Entries) > 100 {
		result.Entries = result.Entries[:100]
	}
	return result, nil
}

// RetryOutcomeDelivery only replays previously committed evidence to its
// existing projection subscriber. It never submits or repeats a game action.
func (service *Service) RetryOutcomeDelivery(principal host.Principal, input OutcomeRetryInput) error {
	if err := host.ValidatePrincipal(principal); err != nil {
		return ErrInvalid
	}
	if !hasScope(principal, ScopeHostAdmin) {
		return ErrForbidden
	}
	if validateID("operation_id", input.OperationID) != nil || validateID("subscriber", input.Subscriber) != nil {
		return ErrInvalid
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if err := service.persistOperationsLocked(); err != nil {
		return err
	}
	if service.outcomeSinks[input.Subscriber] == nil {
		return ErrUnavailable
	}
	operation, err := service.lookupOperationLocked(input.OperationID)
	if err != nil {
		return err
	}
	delivered, registered := operation.outcomeDelivery[input.Subscriber]
	if !registered {
		return ErrNotFound
	}
	if delivered {
		return nil
	}
	if store := service.operationSQLite; store != nil {
		if _, err := store.db.Exec(`UPDATE outcome_backlog SET next_attempt_at=0 WHERE operation_id=? AND subscriber=?`, input.OperationID, input.Subscriber); err != nil {
			return err
		}
	}
	service.notifyLocked()
	return nil
}
