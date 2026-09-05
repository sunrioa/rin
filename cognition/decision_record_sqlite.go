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

type SQLiteDecisionRecorder struct {
	mu         sync.Mutex
	db         *sql.DB
	closeDB    func() error
	legacyLock *os.File
	limit      uint32
	closed     bool
	blocked    error
}

func OpenSQLiteDecisionRecorder(path string, limit uint32) (*SQLiteDecisionRecorder, error) {
	if path == "" {
		return nil, errors.New("decision database path is required")
	}
	local, err := NewLocalDecisionRecorder(limit)
	if err != nil {
		return nil, err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	legacy := strings.TrimSuffix(path, filepath.Ext(path)) + ".json"
	if legacy == path {
		return nil, errors.New("decision database must have a non-JSON extension")
	}
	if err := prepareProviderStorePath(path, ErrTaskStorePersistence, "decision records"); err != nil {
		return nil, err
	}
	recorder := &SQLiteDecisionRecorder{limit: local.limit}
	recorder.legacyLock, err = acquireProviderStoreLock(legacy+".lock", ErrTaskStoreLocked, ErrTaskStorePersistence, "decision records")
	if err != nil {
		return nil, err
	}
	recorder.db, recorder.closeDB, err = sqlitestore.OpenExclusive(path)
	if err == nil {
		err = recorder.initialize(legacy)
	}
	if err != nil {
		return nil, errors.Join(err, recorder.Close())
	}
	return recorder, nil
}

func (recorder *SQLiteDecisionRecorder) initialize(legacy string) error {
	var version int
	if err := recorder.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version < 0 || version > 1 {
		return errors.New("unsupported decision record schema")
	}
	if version == 1 {
		_, err := recorder.Snapshot(context.Background())
		return err
	}
	snapshot := DecisionRecordSnapshot{Version: DecisionRecordSnapshotVersion, Revision: 1}
	if err := privatefile.ReadJSON(legacy, maxDecisionRecordSnapshotBytes, &snapshot); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	local, err := RestoreLocalDecisionRecorder(recorder.limit, snapshot)
	if err != nil {
		return err
	}
	snapshot, err = local.Snapshot(context.Background())
	if err != nil {
		return err
	}
	tx, err := recorder.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, s := range []string{
		`CREATE TABLE decision_records(sequence INTEGER PRIMARY KEY AUTOINCREMENT,record_id TEXT NOT NULL UNIQUE,payload BLOB NOT NULL) STRICT`,
		`CREATE TABLE decision_meta(singleton INTEGER PRIMARY KEY CHECK(singleton=1),revision INTEGER NOT NULL,count INTEGER NOT NULL,bytes INTEGER NOT NULL) STRICT`,
	} {
		if _, err := tx.Exec(s); err != nil {
			return err
		}
	}
	total := 0
	for _, record := range snapshot.Records {
		payload, err := json.Marshal(record)
		if err != nil {
			return err
		}
		total += len(payload)
		if total > maxDecisionRecordSnapshotBytes {
			return ErrProviderCapacity
		}
		if _, err := tx.Exec(`INSERT INTO decision_records(record_id,payload) VALUES(?,?)`, record.RecordID, payload); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO decision_meta VALUES(1,?,?,?)`, snapshot.Revision, len(snapshot.Records), total); err != nil {
		return err
	}
	if _, err := tx.Exec(`PRAGMA user_version=1`); err != nil {
		return err
	}
	return tx.Commit()
}

func (recorder *SQLiteDecisionRecorder) Append(ctx context.Context, record DecisionRecord) error {
	if err := requireMemoryContext(ctx); err != nil {
		return err
	}
	sealed, err := sealDecisionRecord(record)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(sealed)
	if err != nil {
		return err
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if err := recorder.ready(); err != nil {
		return err
	}
	tx, err := recorder.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var existing []byte
	err = tx.QueryRow(`SELECT CASE WHEN length(payload)<=67108864 THEN payload END FROM decision_records WHERE record_id=?`, sealed.RecordID).Scan(&existing)
	if err == nil {
		if bytes.Equal(existing, payload) {
			return nil
		}
		return ErrDecisionRecordConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var revision uint64
	var count, total int
	if err := tx.QueryRow(`SELECT revision,count,bytes FROM decision_meta WHERE singleton=1`).Scan(&revision, &count, &total); err != nil {
		return err
	}
	if revision == 0 || revision >= maxProviderWireInteger || count < 0 || total < 0 || count > int(recorder.limit) || total > maxDecisionRecordSnapshotBytes {
		return ErrTaskStorePersistence
	}
	if count == int(recorder.limit) {
		var oldest int64
		var size int
		if err := tx.QueryRow(`SELECT sequence,length(payload) FROM decision_records ORDER BY sequence LIMIT 1`).Scan(&oldest, &size); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM decision_records WHERE sequence=?`, oldest); err != nil {
			return err
		}
		total -= size
		count--
	}
	total += len(payload)
	count++
	if total > maxDecisionRecordSnapshotBytes {
		return ErrProviderCapacity
	}
	if _, err := tx.Exec(`INSERT INTO decision_records(record_id,payload) VALUES(?,?)`, sealed.RecordID, payload); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE decision_meta SET revision=?,count=?,bytes=? WHERE singleton=1`, revision+1, count, total); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		recorder.blocked = fmt.Errorf("%w: decision commit: %w", ErrTaskStorePersistence, err)
		return recorder.blocked
	}
	return nil
}

func (recorder *SQLiteDecisionRecorder) Snapshot(ctx context.Context) (DecisionRecordSnapshot, error) {
	if err := requireMemoryContext(ctx); err != nil {
		return DecisionRecordSnapshot{}, err
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if err := recorder.ready(); err != nil {
		return DecisionRecordSnapshot{}, err
	}
	result := DecisionRecordSnapshot{Version: DecisionRecordSnapshotVersion, Records: []DecisionRecord{}}
	var count, total int
	if err := recorder.db.QueryRowContext(ctx, `SELECT revision,count,bytes FROM decision_meta WHERE singleton=1`).Scan(&result.Revision, &count, &total); err != nil {
		return result, err
	}
	if result.Revision == 0 || result.Revision > maxProviderWireInteger || count < 0 || count > int(recorder.limit) || total < 0 || total > maxDecisionRecordSnapshotBytes {
		return result, ErrTaskStorePersistence
	}
	rows, err := recorder.db.QueryContext(ctx, `SELECT record_id,CASE WHEN length(payload)<=67108864 THEN payload END FROM decision_records ORDER BY sequence`)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	readBytes := 0
	for rows.Next() {
		var id string
		var payload []byte
		if err := rows.Scan(&id, &payload); err != nil {
			return result, err
		}
		readBytes += len(payload)
		if readBytes > maxDecisionRecordSnapshotBytes || len(result.Records) >= int(recorder.limit) {
			return result, ErrTaskStorePersistence
		}
		var record DecisionRecord
		if err := jsonwire.Validate(payload); err != nil {
			return result, err
		}
		decoder := json.NewDecoder(bytes.NewReader(payload))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&record); err != nil {
			return result, err
		}
		if record.RecordID != id {
			return result, ErrTaskStorePersistence
		}
		sealed, err := sealDecisionRecord(record)
		if err != nil {
			return result, err
		}
		result.Records = append(result.Records, sealed)
	}
	if len(result.Records) != count || total != readBytes {
		return result, ErrTaskStorePersistence
	}
	return result, rows.Err()
}

func (recorder *SQLiteDecisionRecorder) ready() error {
	if recorder.closed {
		return ErrTaskStoreClosed
	}
	return recorder.blocked
}

func (recorder *SQLiteDecisionRecorder) Close() error {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if recorder.closed {
		return nil
	}
	recorder.closed = true
	var err error
	if recorder.closeDB != nil {
		err = recorder.closeDB()
	}
	return errors.Join(err, releaseProviderStoreLock(recorder.legacyLock))
}
