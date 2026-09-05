package signalbox

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

const maxInboxBytes = 1 << 20
const maxSignalStoreBytes = 64 << 20

var ErrPersistence = errors.New("signal persistence failed")

type durableSignal struct {
	Signal   Signal `json:"signal"`
	Sequence uint64 `json:"sequence"`
}
type durableInbox struct {
	Settings       Settings         `json:"settings"`
	Signals        []durableSignal  `json:"signals"`
	NextCursor     uint64           `json:"next_cursor"`
	LastKindMillis map[string]int64 `json:"last_kind_millis"`
}
type sqliteInboxStore struct {
	db    *sql.DB
	close func() error
	bytes map[actorKey]int64
	total int64
}

// OpenSQLiteStore preserves accepted Signals and delivery retries across daemon
// restart. Epoch validation still happens against the current Host on dispatch.
func OpenSQLiteStore(path string, config StoreConfig) (*Store, error) {
	store, err := NewStore(config)
	if err != nil {
		return nil, err
	}
	if path == "" {
		return nil, errors.New("signal database path is required")
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	db, closeDB, err := sqlitestore.OpenExclusive(path)
	if err != nil {
		return nil, err
	}
	store.durable = &sqliteInboxStore{db: db, close: closeDB, bytes: make(map[actorKey]int64)}
	if err := store.restoreSQLite(); err != nil {
		return nil, errors.Join(fmt.Errorf("%w: %w", ErrPersistence, err), store.Close())
	}
	return store, nil
}

func (store *Store) restoreSQLite() error {
	db := store.durable.db
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version < 0 || version > 1 {
		return errors.New("unsupported Signal schema")
	}
	if version == 0 {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		for _, statement := range []string{
			`CREATE TABLE signal_meta(singleton INTEGER PRIMARY KEY CHECK(singleton=1),sequence INTEGER NOT NULL) STRICT`,
			`INSERT INTO signal_meta VALUES(1,0)`,
			`CREATE TABLE signal_inboxes(host_id TEXT NOT NULL,world_id TEXT NOT NULL,actor_id TEXT NOT NULL,payload BLOB NOT NULL,PRIMARY KEY(host_id,world_id,actor_id)) STRICT`,
			`PRAGMA user_version=1`,
		} {
			if _, err := tx.Exec(statement); err != nil {
				return err
			}
		}
		return tx.Commit()
	}
	if err := db.QueryRow(`SELECT sequence FROM signal_meta WHERE singleton=1`).Scan(&store.globalSequence); err != nil {
		return err
	}
	if store.globalSequence > 1<<53-1 {
		return ErrInvalid
	}
	var count int
	if err := db.QueryRow(`SELECT count(*),coalesce(sum(length(payload)),0) FROM signal_inboxes`).Scan(&count, &store.durable.total); err != nil {
		return err
	}
	if count > int(store.maxActors) || store.durable.total > maxSignalStoreBytes {
		return ErrInvalid
	}
	rows, err := db.Query(`SELECT host_id,world_id,actor_id,CASE WHEN length(payload)<=1048576 THEN payload END FROM signal_inboxes`)
	if err != nil {
		return err
	}
	defer rows.Close()
	sequences := make(map[uint64]bool)
	for rows.Next() {
		var key actorKey
		var payload []byte
		if err := rows.Scan(&key.hostID, &key.worldID, &key.actorID, &payload); err != nil {
			return err
		}
		if err := ValidateTarget(Target{key.hostID, key.worldID, key.actorID}); err != nil {
			return err
		}
		if err := jsonwire.Validate(payload); err != nil {
			return err
		}
		var saved durableInbox
		decoder := json.NewDecoder(bytes.NewReader(payload))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&saved); err != nil {
			return err
		}
		if err := ValidateSettings(saved.Settings); err != nil {
			return err
		}
		if len(saved.Signals) > int(saved.Settings.MaxPending) || saved.NextCursor > 1<<53-1 || len(saved.LastKindMillis) > 1024 {
			return ErrInvalid
		}
		box := &inbox{settings: saved.Settings, nextCursor: saved.NextCursor, lastKindMillis: saved.LastKindMillis}
		if box.lastKindMillis == nil {
			box.lastKindMillis = make(map[string]int64)
		}
		for kind, at := range box.lastKindMillis {
			if validateIdentifier("kind", kind, 128) != nil || at < 0 || at > 1<<53-1 {
				return ErrInvalid
			}
		}
		seenIDs := make(map[string]bool)
		var lastCursor uint64
		for _, item := range saved.Signals {
			signal := item.Signal
			if keyOf(Target{signal.HostID, signal.WorldID, signal.ActorID}) != key || signal.Cursor <= lastCursor || signal.Cursor > saved.NextCursor || item.Sequence == 0 || item.Sequence > store.globalSequence || sequences[item.Sequence] || seenIDs[signal.SignalID] {
				return ErrInvalid
			}
			lastCursor = signal.Cursor
			sequences[item.Sequence] = true
			seenIDs[signal.SignalID] = true
			original := signal
			signal.Delivery = DeliveryState{}
			signal.Cursor = 0
			signal.ReceivedAtUnixMillis = 0
			if original.ReceivedAtUnixMillis < 0 || original.ReceivedAtUnixMillis > 1<<53-1 {
				return ErrInvalid
			}
			if err := validateSignal(signal, original.ReceivedAtUnixMillis); err != nil {
				return err
			}
			if err := validateDelivery(original.Delivery); err != nil {
				return err
			}
			original.globalSequence = item.Sequence
			box.signals = append(box.signals, original)
		}
		store.pruneLocked(box, store.now().UnixMilli())
		store.inboxes[key] = box
		store.durable.bytes[key] = int64(len(payload))
	}
	return rows.Err()
}

func validateDelivery(state DeliveryState) error {
	switch state.Status {
	case "", "started", "attached", "merged", "dropped", "retry":
	default:
		return ErrInvalid
	}
	if state.Attempts > 32 || state.RetryAtUnixMillis < 0 || state.RetryAtUnixMillis > 1<<53-1 {
		return ErrInvalid
	}
	if state.Status == "retry" && (state.Attempts == 0 || state.Attempts >= 32 || state.RetryAtUnixMillis == 0) {
		return ErrInvalid
	}
	if state.Status != "retry" && state.RetryAtUnixMillis != 0 {
		return ErrInvalid
	}
	if err := validateText("delivery.reason", state.Reason, 128, false); err != nil {
		return err
	}
	if state.TaskID != "" {
		return validateIdentifier("delivery.task_id", state.TaskID, 128)
	}
	return nil
}

func (store *Store) persistInboxLocked(key actorKey, box *inbox) (err error) {
	if store.durable == nil {
		return nil
	}
	defer func() {
		if err != nil {
			store.blocked = fmt.Errorf("%w: %w", ErrPersistence, err)
			err = store.blocked
			store.notifyLocked()
		}
	}()
	saved := durableInbox{Settings: box.settings, NextCursor: box.nextCursor, LastKindMillis: box.lastKindMillis, Signals: []durableSignal{}}
	for _, signal := range box.signals {
		saved.Signals = append(saved.Signals, durableSignal{Signal: signal, Sequence: signal.globalSequence})
	}
	payload, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	total := store.durable.total - store.durable.bytes[key] + int64(len(payload))
	if len(payload) > maxInboxBytes || total > maxSignalStoreBytes {
		return ErrInvalid
	}
	tx, err := store.durable.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO signal_inboxes VALUES(?,?,?,?) ON CONFLICT(host_id,world_id,actor_id) DO UPDATE SET payload=excluded.payload`, key.hostID, key.worldID, key.actorID, payload); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE signal_meta SET sequence=? WHERE singleton=1`, store.globalSequence); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	store.durable.total = total
	store.durable.bytes[key] = int64(len(payload))
	return nil
}

func (store *Store) readyLocked() error {
	if store.closed {
		return ErrClosed
	}
	return store.blocked
}
