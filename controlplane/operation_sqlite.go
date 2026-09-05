package controlplane

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/sunrioa/rin/internal/jsonwire"
	"github.com/sunrioa/rin/internal/sqlitestore"
)

const operationSQLiteName = "operations.db"
const OperationSQLiteSchemaVersion = 1

type operationSQLite struct {
	db           *sql.DB
	file         *operationFile
	newDatabase  bool
	cacheInvalid bool
	versions     map[string]uint64
	rowBytes     map[string]int64
	totalBytes   int64
}

// OpenSQLite imports operations.json once, under the same directory lock.
// Changed operations, per-sink acknowledgements, controller/emergency state and
// the Policy checkpoint commit atomically before authoritative success returns.
func OpenSQLite(root string, options Options) (*Service, error) {
	if err := validateOutcomeSinks(options); err != nil {
		return nil, err
	}
	file, err := openOperationDirectory(root)
	if err != nil {
		return nil, err
	}
	store := &operationSQLite{file: file, versions: make(map[string]uint64), rowBytes: make(map[string]int64)}
	store.db, err = sqlitestore.Open(filepath.Join(file.root, operationSQLiteName))
	if err != nil {
		_ = store.close()
		return nil, fmt.Errorf("%w: open SQLite operations: %w", ErrPersistence, err)
	}
	service := newService(options)
	state, err := store.read(service.maxOperations)
	if err != nil {
		_ = store.close()
		return nil, fmt.Errorf("%w: read SQLite operations: %w", ErrPersistence, err)
	}
	service.operationSQLite = store
	if err := service.restoreOperations(state); err != nil {
		return nil, errors.Join(err, store.close())
	}
	if err := service.writeOperationsLocked(maxOperationFileBytes); err != nil {
		return nil, errors.Join(err, store.close())
	}
	service.startOutcomeDelivery()
	return service, nil
}

func (store *operationSQLite) read(maxOperations int) (persistedOperations, error) {
	var version int
	if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return persistedOperations{}, err
	}
	if version < 0 || version > OperationSQLiteSchemaVersion {
		return persistedOperations{}, errors.New("unsupported operations database schema")
	}
	if version == 0 {
		store.newDatabase = true
		return store.file.read()
	}
	var payload []byte
	if err := store.db.QueryRow(`SELECT payload FROM operation_meta WHERE singleton=1 AND length(payload)<=?`, maxOperationFileBytes).Scan(&payload); err != nil {
		return persistedOperations{}, err
	}
	if len(payload) > maxOperationFileBytes {
		return persistedOperations{}, ErrCapacity
	}
	var state persistedOperations
	if err := decodeOperationRow(payload, &state); err != nil {
		return state, err
	}
	if state.Version != operationFileVersion || len(state.Operations) != 0 {
		return state, errors.New("invalid operations metadata")
	}
	var count int
	if err := store.db.QueryRow(`SELECT count(*),coalesce(sum(length(payload)),0) FROM operation_rows`).Scan(&count, &store.totalBytes); err != nil {
		return state, err
	}
	if count > maxOperations || store.totalBytes+int64(len(payload))+int64(count)+32 > maxOperationFileBytes {
		return state, ErrCapacity
	}
	rows, err := store.db.Query(`SELECT operation_id,payload FROM operation_rows ORDER BY operation_id`)
	if err != nil {
		return state, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var payload []byte
		if err := rows.Scan(&id, &payload); err != nil {
			return state, err
		}
		var operation persistedOperation
		if err := decodeOperationRow(payload, &operation); err != nil {
			return state, err
		}
		if operation.Request.OperationID != id {
			return state, errors.New("operation row identity mismatch")
		}
		state.Operations = append(state.Operations, operation)
		store.versions[id] = 0
		store.rowBytes[id] = int64(len(payload))
	}
	return state, rows.Err()
}

func decodeOperationRow(payload []byte, target any) error {
	if err := jsonwire.Validate(payload); err != nil {
		return err
	}
	return decodeSingleJSON(bytes.NewReader(payload), target)
}

func (store *operationSQLite) writeLocked(service *Service, maximumBytes int64) (err error) {
	// An I/O error can make commit acknowledgement ambiguous. Rebuild row
	// identities before retrying, so a rolled-back submission cannot leave an
	// orphan durable row merely because it was absent from the old cache.
	defer func() {
		if err != nil {
			store.cacheInvalid = true
		}
	}()
	if store.cacheInvalid {
		if err := store.reloadRowIdentities(); err != nil {
			return fmt.Errorf("%w: reload operation rows: %w", ErrPersistence, err)
		}
	}
	metadata, err := json.Marshal(service.persistedOperationMetadataLocked())
	if err != nil {
		return fmt.Errorf("%w: encode metadata: %w", ErrPersistence, err)
	}
	changed := make(map[string][]byte)
	var removed []string
	total := store.totalBytes
	for id, operation := range service.operations {
		if version, exists := store.versions[id]; exists && version == operation.persistenceRevision {
			continue
		}
		payload, err := json.Marshal(persistedOperationValue(operation))
		if err != nil {
			return fmt.Errorf("%w: encode operation: %w", ErrPersistence, err)
		}
		changed[id] = payload
		total += int64(len(payload)) - store.rowBytes[id]
	}
	for id := range store.versions {
		if service.operations[id] == nil {
			removed = append(removed, id)
			total -= store.rowBytes[id]
		}
	}
	// Account for the equivalent snapshot envelope too. Pending outcomes remain
	// pinned by normal retention rules; byte limits still apply backpressure.
	if total+int64(len(metadata))+int64(len(service.operations))+32 > maximumBytes {
		return fmt.Errorf("%w: operation state exceeds its configured byte budget", ErrCapacity)
	}
	tx, err := store.db.Begin()
	if err != nil {
		return fmt.Errorf("%w: begin operation commit: %w", ErrPersistence, err)
	}
	defer tx.Rollback()
	if store.newDatabase {
		for _, statement := range []string{
			`CREATE TABLE operation_meta (singleton INTEGER PRIMARY KEY CHECK(singleton=1), payload BLOB NOT NULL) STRICT`,
			`CREATE TABLE operation_rows (operation_id TEXT PRIMARY KEY,payload BLOB NOT NULL) STRICT`,
			`PRAGMA user_version=1`,
		} {
			if _, err := tx.Exec(statement); err != nil {
				return fmt.Errorf("%w: migrate operation schema: %w", ErrPersistence, err)
			}
		}
	}
	for _, id := range removed {
		if _, err := tx.Exec(`DELETE FROM operation_rows WHERE operation_id=?`, id); err != nil {
			return fmt.Errorf("%w: prune operation: %w", ErrPersistence, err)
		}
	}
	for id, payload := range changed {
		if _, err := tx.Exec(`INSERT INTO operation_rows(operation_id,payload) VALUES(?,?) ON CONFLICT(operation_id) DO UPDATE SET payload=excluded.payload`, id, payload); err != nil {
			return fmt.Errorf("%w: write operation: %w", ErrPersistence, err)
		}
	}
	if _, err := tx.Exec(`INSERT INTO operation_meta(singleton,payload) VALUES(1,?) ON CONFLICT(singleton) DO UPDATE SET payload=excluded.payload`, metadata); err != nil {
		return fmt.Errorf("%w: write operation metadata: %w", ErrPersistence, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%w: commit operations: %w", ErrPersistence, err)
	}
	store.newDatabase = false
	store.cacheInvalid = false
	store.totalBytes = total
	for _, id := range removed {
		delete(store.versions, id)
		delete(store.rowBytes, id)
	}
	for id, payload := range changed {
		store.versions[id] = service.operations[id].persistenceRevision
		store.rowBytes[id] = int64(len(payload))
	}
	return nil
}

func (store *operationSQLite) reloadRowIdentities() error {
	var version int
	if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version != 0 && version != OperationSQLiteSchemaVersion {
		return errors.New("unsupported operation database schema")
	}
	store.newDatabase = version == 0
	store.versions = make(map[string]uint64)
	store.rowBytes = make(map[string]int64)
	store.totalBytes = 0
	if store.newDatabase {
		return nil
	}
	rows, err := store.db.Query(`SELECT operation_id,length(payload) FROM operation_rows`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var size int64
		if err := rows.Scan(&id, &size); err != nil {
			return err
		}
		store.versions[id] = ^uint64(0)
		store.rowBytes[id] = size
		store.totalBytes += size
		if len(store.versions) > hardMaxOperations || store.totalBytes > maxOperationFileBytes {
			return ErrCapacity
		}
	}
	return rows.Err()
}

func (store *operationSQLite) close() error {
	var err error
	if store.db != nil {
		err = store.db.Close()
	}
	return errors.Join(err, store.file.close())
}
