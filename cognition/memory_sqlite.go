package cognition

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/sunrioa/rin/experience"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/internal/privatefile"
	"github.com/sunrioa/rin/internal/sqlitedsn"
	_ "modernc.org/sqlite"
)

const (
	MemorySQLiteSchemaVersion = 2
	maxMemoryJSONLBytes       = 128 << 20
)

const (
	recallSourceFTS    = "fts"
	recallSourceRecent = "recent"
)

const memoryColumns = `
memory_key, memory_id, session_id, actor_id, controller_id, domain, content,
subject_refs, tags, source_event_ids, source, source_id, authoritative,
canon_ref, confidence, importance, created_clock, created_value,
last_recalled_clock, last_recalled_value, expires_clock, expires_value,
supersedes, recall_count, forgotten, forget_reason, forget_clock, forget_value`

type SQLiteMemoryProvider struct {
	mu       sync.Mutex
	path     string
	db       *sql.DB
	lockFile *os.File
	config   LocalMemoryConfig
	closed   bool
}

func OpenSQLiteMemoryProvider(
	path string,
	config LocalMemoryConfig,
) (*SQLiteMemoryProvider, error) {
	if path == "" {
		return nil, errors.New("memory database path is required")
	}
	config, err := normalizeLocalMemoryConfig(config)
	if err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := prepareProviderStorePath(
		absolute, ErrMemoryStorePersistence, "memory database",
	); err != nil {
		return nil, err
	}
	lockFile, err := acquireProviderStoreLock(
		absolute+".lock", ErrMemoryStoreLocked,
		ErrMemoryStorePersistence, "memory database",
	)
	if err != nil {
		return nil, err
	}
	dsn := sqlitedsn.File(absolute)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		_ = releaseProviderStoreLock(lockFile)
		return nil, fmt.Errorf("%w: open sqlite: %v", ErrMemoryStorePersistence, err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	provider := &SQLiteMemoryProvider{
		path: absolute, db: db, lockFile: lockFile, config: config,
	}
	if err := provider.initialize(context.Background()); err != nil {
		_ = db.Close()
		_ = releaseProviderStoreLock(lockFile)
		return nil, err
	}
	if err := os.Chmod(absolute, 0o600); err != nil {
		_ = provider.Close()
		return nil, fmt.Errorf("%w: protect memory database: %v", ErrMemoryStorePersistence, err)
	}
	if err := provider.importLegacySnapshot(context.Background()); err != nil {
		_ = provider.Close()
		return nil, err
	}
	return provider, nil
}

func (provider *SQLiteMemoryProvider) importLegacySnapshot(ctx context.Context) error {
	var count int
	if err := provider.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memory_records`).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return nil
	}
	legacyPath := filepath.Join(filepath.Dir(provider.path), "memory.json")
	var snapshot MemorySnapshot
	if err := privatefile.ReadJSON(legacyPath, maxMemorySnapshotBytes, &snapshot); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("%w: import legacy memory snapshot: %v", ErrMemoryStorePersistence, err)
	}
	validated, err := RestoreLocalMemoryProvider(provider.config, snapshot)
	if err != nil {
		return fmt.Errorf("%w: validate legacy memory snapshot: %v", ErrMemoryStorePersistence, err)
	}
	sealed, err := validated.Snapshot(ctx)
	if err != nil {
		return err
	}
	return provider.replaceSnapshot(ctx, sealed)
}

func (provider *SQLiteMemoryProvider) initialize(ctx context.Context) error {
	for _, statement := range []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA synchronous=FULL`,
		`PRAGMA foreign_keys=ON`,
		`PRAGMA busy_timeout=5000`,
	} {
		if _, err := provider.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("%w: configure sqlite: %v", ErrMemoryStorePersistence, err)
		}
	}
	var version int
	if err := provider.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("%w: read schema version: %v", ErrMemoryStorePersistence, err)
	}
	if version < 0 || version > MemorySQLiteSchemaVersion {
		return fmt.Errorf("%w: unsupported memory schema version %d", ErrMemoryStorePersistence, version)
	}
	tx, err := provider.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%w: begin migration: %v", ErrMemoryStorePersistence, err)
	}
	defer tx.Rollback()
	for _, statement := range memorySchemaStatements() {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("%w: migrate sqlite: %v", ErrMemoryStorePersistence, err)
		}
	}
	if err := rebuildMemorySearchIndexes(ctx, tx); err != nil {
		return fmt.Errorf("%w: rebuild search indexes: %v", ErrMemoryStorePersistence, err)
	}
	if _, err := tx.ExecContext(ctx, `PRAGMA user_version = 2`); err != nil {
		return fmt.Errorf("%w: set schema version: %v", ErrMemoryStorePersistence, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%w: commit migration: %v", ErrMemoryStorePersistence, err)
	}
	return nil
}

func memorySchemaStatements() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS memory_meta (
            key TEXT PRIMARY KEY,
            value INTEGER NOT NULL
        ) STRICT`,
		`INSERT OR IGNORE INTO memory_meta(key, value) VALUES ('revision', 1)`,
		`CREATE TABLE IF NOT EXISTS memory_records (
            memory_key TEXT PRIMARY KEY,
            memory_id TEXT NOT NULL,
            session_id TEXT NOT NULL,
            actor_id TEXT NOT NULL,
            controller_id TEXT NOT NULL,
            domain TEXT NOT NULL,
            content TEXT NOT NULL,
            subject_refs TEXT NOT NULL,
            tags TEXT NOT NULL,
            source_event_ids TEXT NOT NULL,
            source TEXT NOT NULL,
            source_id TEXT NOT NULL,
            authoritative INTEGER NOT NULL CHECK(authoritative IN (0, 1)),
            canon_ref TEXT,
            confidence REAL NOT NULL,
            importance REAL NOT NULL,
            created_clock TEXT NOT NULL,
            created_value INTEGER NOT NULL,
            last_recalled_clock TEXT,
            last_recalled_value INTEGER,
            expires_clock TEXT,
            expires_value INTEGER,
            supersedes TEXT NOT NULL,
            recall_count INTEGER NOT NULL DEFAULT 0,
            forgotten INTEGER NOT NULL DEFAULT 0 CHECK(forgotten IN (0, 1)),
            forget_reason TEXT,
            forget_clock TEXT,
            forget_value INTEGER,
            UNIQUE(session_id, actor_id, controller_id, domain, memory_id)
        ) STRICT`,
		`CREATE INDEX IF NOT EXISTS memory_namespace_idx ON memory_records(
            session_id, actor_id, controller_id, domain, forgotten, created_value DESC
        )`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS memory_fts USING fts5(
            memory_key UNINDEXED, content, tokenize='unicode61'
        )`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS memory_fts_trigram USING fts5(
            memory_key UNINDEXED, content, tokenize='trigram'
        )`,
		`CREATE TRIGGER IF NOT EXISTS memory_records_ai AFTER INSERT ON memory_records BEGIN
            INSERT INTO memory_fts(memory_key, content) VALUES (new.memory_key, new.content);
        END`,
		`CREATE TRIGGER IF NOT EXISTS memory_records_ad AFTER DELETE ON memory_records BEGIN
            DELETE FROM memory_fts WHERE memory_key = old.memory_key;
        END`,
		`CREATE TRIGGER IF NOT EXISTS memory_records_au AFTER UPDATE OF content ON memory_records BEGIN
            DELETE FROM memory_fts WHERE memory_key = old.memory_key;
            INSERT INTO memory_fts(memory_key, content) VALUES (new.memory_key, new.content);
		END`,
		`CREATE TRIGGER IF NOT EXISTS memory_records_trigram_ai AFTER INSERT ON memory_records BEGIN
			INSERT INTO memory_fts_trigram(memory_key, content) VALUES (new.memory_key, new.content);
		END`,
		`CREATE TRIGGER IF NOT EXISTS memory_records_trigram_ad AFTER DELETE ON memory_records BEGIN
			DELETE FROM memory_fts_trigram WHERE memory_key = old.memory_key;
		END`,
		`CREATE TRIGGER IF NOT EXISTS memory_records_trigram_au AFTER UPDATE OF content ON memory_records BEGIN
			DELETE FROM memory_fts_trigram WHERE memory_key = old.memory_key;
			INSERT INTO memory_fts_trigram(memory_key, content) VALUES (new.memory_key, new.content);
        END`,
		`CREATE TABLE IF NOT EXISTS experience_corrections (
            task_id TEXT NOT NULL,
            correction_id TEXT NOT NULL,
            occurred_at_unix_millis INTEGER NOT NULL,
            summary TEXT NOT NULL,
            related_event_id TEXT NOT NULL,
            PRIMARY KEY(task_id, correction_id)
        ) STRICT`,
	}
}

func rebuildMemorySearchIndexes(ctx context.Context, tx *sql.Tx) error {
	var records int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM memory_records`).Scan(&records); err != nil {
		return err
	}
	for _, table := range []string{"memory_fts", "memory_fts_trigram"} {
		var indexed int64
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&indexed); err != nil {
			return err
		}
		if indexed == records {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO `+table+`(memory_key, content)
			SELECT memory_key, content FROM memory_records`); err != nil {
			return err
		}
	}
	return nil
}

func (provider *SQLiteMemoryProvider) Append(
	ctx context.Context,
	record MemoryRecord,
) (MemoryRecord, error) {
	if err := requireMemoryContext(ctx); err != nil {
		return MemoryRecord{}, err
	}
	sealed, err := sealMemoryRecord(record)
	if err != nil {
		return MemoryRecord{}, err
	}
	if sealed.LastRecalledAt != nil || sealed.RecallCount != 0 {
		return MemoryRecord{}, errors.New("append cannot set provider-owned recall metadata")
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if err := provider.ready(); err != nil {
		return MemoryRecord{}, err
	}
	tx, err := provider.db.BeginTx(ctx, nil)
	if err != nil {
		return MemoryRecord{}, err
	}
	defer tx.Rollback()
	key := sqliteMemoryKey(sealed)
	existing, _, err := scanMemoryRow(tx.QueryRowContext(
		ctx, `SELECT `+memoryColumns+` FROM memory_records WHERE memory_key = ?`, key,
	))
	if err == nil {
		if memoryRecordsEqual(existing, sealed) {
			return existing, nil
		}
		return MemoryRecord{}, ErrProviderConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return MemoryRecord{}, err
	}
	active, history, err := namespaceCounts(ctx, tx, sealed.Namespace)
	if err != nil {
		return MemoryRecord{}, err
	}
	if active >= provider.config.MaxActiveRecordsPerNamespace ||
		history >= provider.config.MaxHistoryPerNamespace {
		return MemoryRecord{}, ErrProviderCapacity
	}
	if err := insertMemoryRecord(ctx, tx, sealed); err != nil {
		return MemoryRecord{}, err
	}
	if err := incrementMemoryRevision(ctx, tx); err != nil {
		return MemoryRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return MemoryRecord{}, err
	}
	return sealed, nil
}

func (provider *SQLiteMemoryProvider) Retrieve(
	ctx context.Context,
	query MemoryQuery,
) ([]MemoryMatch, error) {
	if err := requireMemoryContext(ctx); err != nil {
		return nil, err
	}
	sealed, err := sealMemoryQuery(query)
	if err != nil {
		return nil, err
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if err := provider.ready(); err != nil {
		return nil, err
	}
	tx, err := provider.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	limit := max(256, int(sealed.Budget.MaxRecords)*16)
	limit = min(limit, 2_048)
	candidates := make(map[string]recallCandidate)
	if err := queryRecentMemories(ctx, tx, sealed, limit, candidates); err != nil {
		return nil, err
	}
	if len(sealed.Terms) != 0 {
		if err := queryFTSMemories(ctx, tx, sealed, limit, candidates); err != nil {
			return nil, err
		}
		if err := queryTrigramMemories(ctx, tx, sealed, limit, candidates); err != nil {
			return nil, err
		}
	}
	matches := make([]MemoryMatch, 0, len(candidates))
	for _, candidate := range candidates {
		record := candidate.record
		if !memoryVisibleToQuery(record, sealed) || memoryExpired(record, sealed.Now) ||
			!memoryMatchesFilters(record, sealed) {
			continue
		}
		score, reasons := scoreMemory(record, sealed)
		if rank, ok := candidate.ranks[recallSourceFTS]; ok {
			score += max(1, 48-min(rank, 47))
			reasons = appendUniqueString(reasons, recallSourceFTS)
		}
		if rank, ok := candidate.ranks[recallSourceRecent]; ok {
			score += max(1, 16-min(rank, 15))
			reasons = appendUniqueString(reasons, recallSourceRecent)
		}
		matches = append(matches, MemoryMatch{Record: record, Score: score, Reasons: reasons})
	}
	slices.SortFunc(matches, compareMemoryMatches)
	selected := make([]MemoryMatch, 0, min(len(matches), int(sealed.Budget.MaxRecords)))
	characters := 0
	for _, match := range matches {
		count := utf8.RuneCountInString(match.Record.Content)
		if characters+count > int(sealed.Budget.MaxCharacters) {
			continue
		}
		selected = append(selected, match)
		characters += count
		if len(selected) == int(sealed.Budget.MaxRecords) {
			break
		}
	}
	for index := range selected {
		key := sqliteMemoryKey(selected[index].Record)
		if _, err := tx.ExecContext(ctx, `UPDATE memory_records SET
            recall_count = CASE WHEN recall_count < ? THEN recall_count + 1 ELSE recall_count END,
            last_recalled_clock = ?, last_recalled_value = ? WHERE memory_key = ?`,
			maxProviderWireInteger, string(sealed.Now.Clock), sealed.Now.Value, key,
		); err != nil {
			return nil, err
		}
		if selected[index].Record.RecallCount < maxProviderWireInteger {
			selected[index].Record.RecallCount++
		}
		now := sealed.Now
		selected[index].Record.LastRecalledAt = &now
	}
	if len(selected) != 0 {
		if err := incrementMemoryRevision(ctx, tx); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return selected, nil
}

func (provider *SQLiteMemoryProvider) Consolidate(
	ctx context.Context,
	request MemoryConsolidation,
) (MemoryRecord, error) {
	if err := requireMemoryContext(ctx); err != nil {
		return MemoryRecord{}, err
	}
	if err := validateMemoryNamespace(request.Namespace); err != nil {
		return MemoryRecord{}, err
	}
	sourceIDs, err := normalizeMemoryIDs("source_memory_ids", request.SourceMemoryIDs, 64)
	if err != nil || len(sourceIDs) < 2 {
		return MemoryRecord{}, errors.New("memory consolidation requires at least two valid source records")
	}
	if err := validateProviderText("reason", request.Reason, 500, true); err != nil {
		return MemoryRecord{}, err
	}
	request.Summary.Namespace = request.Namespace
	request.Summary.Supersedes = sourceIDs
	summary, err := sealMemoryRecord(request.Summary)
	if err != nil {
		return MemoryRecord{}, err
	}
	if summary.LastRecalledAt != nil || summary.RecallCount != 0 {
		return MemoryRecord{}, errors.New("consolidation cannot set provider-owned recall metadata")
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if err := provider.ready(); err != nil {
		return MemoryRecord{}, err
	}
	tx, err := provider.db.BeginTx(ctx, nil)
	if err != nil {
		return MemoryRecord{}, err
	}
	defer tx.Rollback()
	for _, memoryID := range sourceIDs {
		record, forgotten, err := loadMemoryByIdentity(ctx, tx, request.Namespace, memoryID)
		if err != nil {
			return MemoryRecord{}, err
		}
		if forgotten || record.MemoryID == summary.MemoryID {
			return MemoryRecord{}, ErrProviderConflict
		}
	}
	_, history, err := namespaceCounts(ctx, tx, request.Namespace)
	if err != nil {
		return MemoryRecord{}, err
	}
	if history >= provider.config.MaxHistoryPerNamespace {
		return MemoryRecord{}, ErrProviderCapacity
	}
	if err := insertMemoryRecord(ctx, tx, summary); err != nil {
		return MemoryRecord{}, err
	}
	for _, memoryID := range sourceIDs {
		if _, err := tx.ExecContext(ctx, `UPDATE memory_records SET
            forgotten = 1, forget_reason = ?, forget_clock = ?, forget_value = ?
            WHERE session_id = ? AND actor_id = ? AND controller_id = ? AND domain = ? AND memory_id = ?`,
			request.Reason, string(summary.CreatedAt.Clock), summary.CreatedAt.Value,
			request.Namespace.SessionID, request.Namespace.ActorID,
			request.Namespace.ControllerID, string(request.Namespace.Domain), memoryID,
		); err != nil {
			return MemoryRecord{}, err
		}
	}
	if err := incrementMemoryRevision(ctx, tx); err != nil {
		return MemoryRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return MemoryRecord{}, err
	}
	return summary, nil
}

func (provider *SQLiteMemoryProvider) Forget(
	ctx context.Context,
	request MemoryForgetRequest,
) error {
	if err := requireMemoryContext(ctx); err != nil {
		return err
	}
	if err := validateMemoryNamespace(request.Namespace); err != nil {
		return err
	}
	ids, err := normalizeMemoryIDs("memory_ids", request.MemoryIDs, 128)
	if err != nil || len(ids) == 0 {
		return errors.New("memory_ids is required")
	}
	if err := validateProviderText("reason", request.Reason, 500, true); err != nil {
		return err
	}
	if err := validateMemoryTimepoint("at", request.At); err != nil {
		return err
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if err := provider.ready(); err != nil {
		return err
	}
	tx, err := provider.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	changed := false
	for _, memoryID := range ids {
		_, forgotten, err := loadMemoryByIdentity(ctx, tx, request.Namespace, memoryID)
		if err != nil {
			return err
		}
		if forgotten {
			continue
		}
		result, err := tx.ExecContext(ctx, `UPDATE memory_records SET
            forgotten = 1, forget_reason = ?, forget_clock = ?, forget_value = ?
            WHERE session_id = ? AND actor_id = ? AND controller_id = ? AND domain = ? AND memory_id = ?`,
			request.Reason, string(request.At.Clock), request.At.Value,
			request.Namespace.SessionID, request.Namespace.ActorID,
			request.Namespace.ControllerID, string(request.Namespace.Domain), memoryID,
		)
		if err != nil {
			return err
		}
		rows, _ := result.RowsAffected()
		changed = changed || rows != 0
	}
	if changed {
		if err := incrementMemoryRevision(ctx, tx); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (provider *SQLiteMemoryProvider) Snapshot(
	ctx context.Context,
) (MemorySnapshot, error) {
	if err := requireMemoryContext(ctx); err != nil {
		return MemorySnapshot{}, err
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if err := provider.ready(); err != nil {
		return MemorySnapshot{}, err
	}
	return provider.snapshotLocked(ctx)
}

func (provider *SQLiteMemoryProvider) snapshotLocked(
	ctx context.Context,
) (MemorySnapshot, error) {
	var snapshot MemorySnapshot
	if err := provider.db.QueryRowContext(
		ctx, `SELECT value FROM memory_meta WHERE key = 'revision'`,
	).Scan(&snapshot.Revision); err != nil {
		return MemorySnapshot{}, err
	}
	rows, err := provider.db.QueryContext(
		ctx, `SELECT `+memoryColumns+` FROM memory_records ORDER BY memory_key`,
	)
	if err != nil {
		return MemorySnapshot{}, err
	}
	defer rows.Close()
	for rows.Next() {
		record, tombstone, err := scanMemoryRow(rows)
		if err != nil {
			return MemorySnapshot{}, err
		}
		snapshot.Records = append(snapshot.Records, record)
		if tombstone != nil {
			snapshot.Tombstones = append(snapshot.Tombstones, *tombstone)
		}
	}
	return snapshot, rows.Err()
}

func (provider *SQLiteMemoryProvider) Health(ctx context.Context) ProviderHealth {
	if ctx == nil || ctx.Err() != nil {
		return ProviderHealth{Code: "context_unavailable"}
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.ready() != nil {
		return ProviderHealth{Degraded: true, Code: "memory_store_unavailable"}
	}
	if err := provider.db.PingContext(ctx); err != nil {
		return ProviderHealth{Degraded: true, Code: "memory_store_unavailable"}
	}
	return ProviderHealth{Available: true}
}

func (provider *SQLiteMemoryProvider) AppendCorrection(
	ctx context.Context,
	taskID string,
	correction experience.Correction,
) error {
	if err := validateMemoryOpaqueID("task_id", taskID); err != nil {
		return err
	}
	if correction.CorrectionID == "" || correction.Summary == "" ||
		correction.OccurredAtUnixMillis < 0 {
		return errors.New("correction is invalid")
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if err := provider.ready(); err != nil {
		return err
	}
	result, err := provider.db.ExecContext(ctx, `INSERT INTO experience_corrections(
        task_id, correction_id, occurred_at_unix_millis, summary, related_event_id
    ) VALUES (?, ?, ?, ?, ?) ON CONFLICT(task_id, correction_id) DO UPDATE SET
        occurred_at_unix_millis = excluded.occurred_at_unix_millis,
        summary = excluded.summary, related_event_id = excluded.related_event_id
    WHERE occurred_at_unix_millis = excluded.occurred_at_unix_millis
      AND summary = excluded.summary AND related_event_id = excluded.related_event_id`,
		taskID, correction.CorrectionID, correction.OccurredAtUnixMillis,
		correction.Summary, correction.RelatedEventID,
	)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return ErrProviderConflict
	}
	return nil
}

func (provider *SQLiteMemoryProvider) Corrections(
	ctx context.Context,
	taskID string,
) ([]experience.Correction, error) {
	if err := validateMemoryOpaqueID("task_id", taskID); err != nil {
		return nil, err
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if err := provider.ready(); err != nil {
		return nil, err
	}
	rows, err := provider.db.QueryContext(ctx, `SELECT correction_id,
        occurred_at_unix_millis, summary, related_event_id
        FROM experience_corrections WHERE task_id = ?
        ORDER BY occurred_at_unix_millis, correction_id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []experience.Correction
	for rows.Next() {
		var correction experience.Correction
		if err := rows.Scan(
			&correction.CorrectionID, &correction.OccurredAtUnixMillis,
			&correction.Summary, &correction.RelatedEventID,
		); err != nil {
			return nil, err
		}
		result = append(result, correction)
	}
	return result, rows.Err()
}

func (provider *SQLiteMemoryProvider) ExportJSONL(
	ctx context.Context,
	output io.Writer,
) error {
	snapshot, err := provider.Snapshot(ctx)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	for _, record := range snapshot.Records {
		if err := encoder.Encode(struct {
			Type   string       `json:"type"`
			Record MemoryRecord `json:"record"`
		}{Type: "memory", Record: record}); err != nil {
			return err
		}
	}
	for _, tombstone := range snapshot.Tombstones {
		if err := encoder.Encode(struct {
			Type      string          `json:"type"`
			Tombstone MemoryTombstone `json:"tombstone"`
		}{Type: "tombstone", Tombstone: tombstone}); err != nil {
			return err
		}
	}
	return nil
}

func (provider *SQLiteMemoryProvider) ImportJSONL(
	ctx context.Context,
	input io.Reader,
) error {
	limited := &io.LimitedReader{R: input, N: maxMemoryJSONLBytes + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	snapshot := MemorySnapshot{Revision: 1}
	for scanner.Scan() {
		var envelope struct {
			Type      string           `json:"type"`
			Record    *MemoryRecord    `json:"record"`
			Tombstone *MemoryTombstone `json:"tombstone"`
		}
		decoder := json.NewDecoder(strings.NewReader(scanner.Text()))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&envelope); err != nil {
			return err
		}
		switch envelope.Type {
		case "memory":
			if envelope.Record == nil || envelope.Tombstone != nil {
				return errors.New("invalid memory JSONL record")
			}
			snapshot.Records = append(snapshot.Records, *envelope.Record)
		case "tombstone":
			if envelope.Tombstone == nil || envelope.Record != nil {
				return errors.New("invalid tombstone JSONL record")
			}
			snapshot.Tombstones = append(snapshot.Tombstones, *envelope.Tombstone)
		default:
			return errors.New("unknown memory JSONL record type")
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if limited.N <= 0 {
		return errors.New("memory JSONL exceeds size limit")
	}
	validated, err := RestoreLocalMemoryProvider(provider.config, snapshot)
	if err != nil {
		return err
	}
	validatedSnapshot, err := validated.Snapshot(ctx)
	if err != nil {
		return err
	}
	return provider.replaceSnapshot(ctx, validatedSnapshot)
}

func (provider *SQLiteMemoryProvider) replaceSnapshot(
	ctx context.Context,
	snapshot MemorySnapshot,
) error {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if err := provider.ready(); err != nil {
		return err
	}
	tx, err := provider.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM memory_records`); err != nil {
		return err
	}
	tombstones := make(map[string]MemoryTombstone, len(snapshot.Tombstones))
	for _, tombstone := range snapshot.Tombstones {
		tombstones[sqliteMemoryIdentityKey(tombstone.Namespace, tombstone.MemoryID)] = tombstone
	}
	for _, record := range snapshot.Records {
		if err := insertMemoryRecord(ctx, tx, record); err != nil {
			return err
		}
		if tombstone, exists := tombstones[sqliteMemoryIdentityKey(record.Namespace, record.MemoryID)]; exists {
			if _, err := tx.ExecContext(ctx, `UPDATE memory_records SET
                    forgotten = 1, forget_reason = ?, forget_clock = ?, forget_value = ?
                    WHERE memory_key = ?`, tombstone.Reason, string(tombstone.At.Clock),
				tombstone.At.Value, sqliteMemoryKey(record)); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(
		ctx, `UPDATE memory_meta SET value = ? WHERE key = 'revision'`, snapshot.Revision,
	); err != nil {
		return err
	}
	return tx.Commit()
}

func (provider *SQLiteMemoryProvider) Close() error {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.closed {
		return nil
	}
	provider.closed = true
	dbErr := provider.db.Close()
	lockErr := releaseProviderStoreLock(provider.lockFile)
	provider.lockFile = nil
	return errors.Join(dbErr, lockErr)
}

func (provider *SQLiteMemoryProvider) ready() error {
	if provider.closed {
		return ErrMemoryStoreClosed
	}
	return nil
}

func namespaceCounts(
	ctx context.Context,
	tx *sql.Tx,
	namespace MemoryNamespace,
) (uint32, uint32, error) {
	var active, history uint32
	err := tx.QueryRowContext(ctx, `SELECT
        COALESCE(SUM(CASE WHEN forgotten = 0 THEN 1 ELSE 0 END), 0), COUNT(*)
        FROM memory_records WHERE session_id = ? AND actor_id = ?
        AND controller_id = ? AND domain = ?`,
		namespace.SessionID, namespace.ActorID, namespace.ControllerID, string(namespace.Domain),
	).Scan(&active, &history)
	return active, history, err
}

func insertMemoryRecord(ctx context.Context, tx *sql.Tx, record MemoryRecord) error {
	subjects, _ := json.Marshal(record.SubjectRefs)
	tags, _ := json.Marshal(record.Tags)
	events, _ := json.Marshal(record.SourceEventIDs)
	supersedes, _ := json.Marshal(record.Supersedes)
	var canon any
	if record.CanonRef != nil {
		encoded, err := json.Marshal(record.CanonRef)
		if err != nil {
			return err
		}
		canon = string(encoded)
	}
	var recalledClock, recalledValue, expiresClock, expiresValue any
	if record.LastRecalledAt != nil {
		recalledClock, recalledValue = string(record.LastRecalledAt.Clock), record.LastRecalledAt.Value
	}
	if record.ExpiresAt != nil {
		expiresClock, expiresValue = string(record.ExpiresAt.Clock), record.ExpiresAt.Value
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO memory_records(
        memory_key, memory_id, session_id, actor_id, controller_id, domain, content,
        subject_refs, tags, source_event_ids, source, source_id, authoritative,
        canon_ref, confidence, importance, created_clock, created_value,
        last_recalled_clock, last_recalled_value, expires_clock, expires_value,
        supersedes, recall_count, forgotten
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`,
		sqliteMemoryKey(record), record.MemoryID, record.Namespace.SessionID,
		record.Namespace.ActorID, record.Namespace.ControllerID, string(record.Namespace.Domain),
		record.Content, string(subjects), string(tags), string(events),
		string(record.Provenance.Source), record.Provenance.SourceID,
		boolToInt(record.Provenance.Authoritative), canon, record.Confidence,
		record.Importance, string(record.CreatedAt.Clock), record.CreatedAt.Value,
		recalledClock, recalledValue, expiresClock, expiresValue,
		string(supersedes), record.RecallCount,
	)
	return err
}

type memoryRowScanner interface {
	Scan(...any) error
}

func scanMemoryRow(scanner memoryRowScanner) (MemoryRecord, *MemoryTombstone, error) {
	var key, memoryID, sessionID, actorID, controllerID, domain, content string
	var subjectsJSON, tagsJSON, eventsJSON, source, sourceID string
	var authoritative int
	var canonJSON, recalledClock, expiresClock sql.NullString
	var recalledValue, expiresValue sql.NullInt64
	var confidence, importance float64
	var createdClock string
	var createdValue, recallCount int64
	var supersedesJSON string
	var forgotten int
	var forgetReason, forgetClock sql.NullString
	var forgetValue sql.NullInt64
	if err := scanner.Scan(
		&key, &memoryID, &sessionID, &actorID, &controllerID, &domain, &content,
		&subjectsJSON, &tagsJSON, &eventsJSON, &source, &sourceID, &authoritative,
		&canonJSON, &confidence, &importance, &createdClock, &createdValue,
		&recalledClock, &recalledValue, &expiresClock, &expiresValue,
		&supersedesJSON, &recallCount, &forgotten, &forgetReason, &forgetClock, &forgetValue,
	); err != nil {
		return MemoryRecord{}, nil, err
	}
	record := MemoryRecord{
		MemoryID: memoryID,
		Namespace: MemoryNamespace{
			SessionID: sessionID, ActorID: actorID, ControllerID: controllerID,
			Domain: MemoryDomain(domain),
		},
		Content: content,
		Provenance: MemoryProvenance{
			Source: MemorySource(source), SourceID: sourceID, Authoritative: authoritative != 0,
		},
		Confidence: confidence, Importance: importance,
		CreatedAt:   host.Timepoint{Clock: host.ClockMode(createdClock), Value: createdValue},
		RecallCount: uint64(recallCount),
	}
	for payload, target := range map[string]*[]string{
		subjectsJSON: &record.SubjectRefs, tagsJSON: &record.Tags,
		eventsJSON: &record.SourceEventIDs, supersedesJSON: &record.Supersedes,
	} {
		if err := json.Unmarshal([]byte(payload), target); err != nil {
			return MemoryRecord{}, nil, err
		}
	}
	if canonJSON.Valid {
		var canon MemoryCanonRef
		if err := json.Unmarshal([]byte(canonJSON.String), &canon); err != nil {
			return MemoryRecord{}, nil, err
		}
		record.CanonRef = &canon
	}
	if recalledClock.Valid != recalledValue.Valid || expiresClock.Valid != expiresValue.Valid {
		return MemoryRecord{}, nil, errors.New("memory database contains partial timepoint")
	}
	if recalledClock.Valid {
		record.LastRecalledAt = &host.Timepoint{
			Clock: host.ClockMode(recalledClock.String), Value: recalledValue.Int64,
		}
	}
	if expiresClock.Valid {
		record.ExpiresAt = &host.Timepoint{
			Clock: host.ClockMode(expiresClock.String), Value: expiresValue.Int64,
		}
	}
	sealed, err := sealMemoryRecord(record)
	if err != nil || sqliteMemoryKey(sealed) != key {
		return MemoryRecord{}, nil, errors.New("memory database record is invalid")
	}
	if forgotten == 0 {
		return sealed, nil, nil
	}
	if !forgetReason.Valid || !forgetClock.Valid || !forgetValue.Valid {
		return MemoryRecord{}, nil, errors.New("forgotten memory lacks tombstone metadata")
	}
	tombstone, err := sealMemoryTombstone(MemoryTombstone{
		MemoryID: memoryID, Namespace: record.Namespace, Reason: forgetReason.String,
		At: host.Timepoint{Clock: host.ClockMode(forgetClock.String), Value: forgetValue.Int64},
	})
	return sealed, &tombstone, err
}

func queryRecentMemories(
	ctx context.Context,
	tx *sql.Tx,
	query MemoryQuery,
	limit int,
	result map[string]recallCandidate,
) error {
	predicate, arguments := memoryCandidatePredicate("r", query)
	arguments = append(arguments, limit)
	rows, err := tx.QueryContext(ctx, `SELECT `+prefixMemoryColumns("r")+`
		FROM memory_records r WHERE `+predicate+`
		ORDER BY r.created_value DESC, r.memory_key LIMIT ?`, arguments...)
	if err != nil {
		return err
	}
	defer rows.Close()
	rank := 0
	for rows.Next() {
		record, _, err := scanMemoryRow(rows)
		if err != nil {
			return err
		}
		mergeRecallCandidate(result, record, recallSourceRecent, rank)
		rank++
	}
	return rows.Err()
}

func queryFTSMemories(
	ctx context.Context,
	tx *sql.Tx,
	query MemoryQuery,
	limit int,
	result map[string]recallCandidate,
) error {
	terms := make([]string, len(query.Terms))
	for index, term := range query.Terms {
		terms[index] = `"` + strings.ReplaceAll(term, `"`, `""`) + `"`
	}
	predicate, arguments := memoryCandidatePredicate("r", query)
	arguments = append([]any{strings.Join(terms, " AND ")}, arguments...)
	arguments = append(arguments, limit)
	rows, err := tx.QueryContext(ctx, `SELECT `+prefixMemoryColumns("r")+`
		FROM memory_fts f JOIN memory_records r ON r.memory_key = f.memory_key
		WHERE memory_fts MATCH ? AND `+predicate+`
		ORDER BY bm25(memory_fts), r.created_value DESC, r.memory_key LIMIT ?`, arguments...)
	if err != nil {
		return err
	}
	defer rows.Close()
	rank := 0
	for rows.Next() {
		record, _, err := scanMemoryRow(rows)
		if err != nil {
			return err
		}
		mergeRecallCandidate(result, record, recallSourceFTS, rank)
		rank++
	}
	return rows.Err()
}

func queryTrigramMemories(
	ctx context.Context,
	tx *sql.Tx,
	query MemoryQuery,
	limit int,
	result map[string]recallCandidate,
) error {
	terms := make([]string, 0, len(query.Terms))
	for _, term := range query.Terms {
		if utf8.RuneCountInString(term) < 3 {
			continue
		}
		terms = append(terms, `"`+strings.ReplaceAll(term, `"`, `""`)+`"`)
	}
	if len(terms) == 0 {
		return nil
	}
	predicate, arguments := memoryCandidatePredicate("r", query)
	arguments = append([]any{strings.Join(terms, " AND ")}, arguments...)
	arguments = append(arguments, limit)
	rows, err := tx.QueryContext(ctx, `SELECT `+prefixMemoryColumns("r")+`
		FROM memory_fts_trigram f JOIN memory_records r ON r.memory_key = f.memory_key
		WHERE memory_fts_trigram MATCH ? AND `+predicate+`
		ORDER BY bm25(memory_fts_trigram), r.created_value DESC, r.memory_key LIMIT ?`, arguments...)
	if err != nil {
		return err
	}
	defer rows.Close()
	rank := 0
	for rows.Next() {
		record, _, err := scanMemoryRow(rows)
		if err != nil {
			return err
		}
		mergeRecallCandidate(result, record, recallSourceFTS, rank)
		rank++
	}
	return rows.Err()
}

type recallCandidate struct {
	record MemoryRecord
	ranks  map[string]int
}

func mergeRecallCandidate(
	result map[string]recallCandidate,
	record MemoryRecord,
	source string,
	rank int,
) {
	key := sqliteMemoryKey(record)
	candidate, exists := result[key]
	if !exists {
		candidate = recallCandidate{record: record, ranks: make(map[string]int, 2)}
	}
	if current, ranked := candidate.ranks[source]; !ranked || rank < current {
		candidate.ranks[source] = rank
	}
	result[key] = candidate
}

func memoryCandidatePredicate(alias string, query MemoryQuery) (string, []any) {
	parts := make([]string, 0, 5)
	arguments := []any{query.SessionID, query.ActorID}
	for _, namespace := range memoryQueryNamespaces(query) {
		parts = append(parts, "("+alias+".controller_id = ? AND "+alias+".domain = ?)")
		arguments = append(arguments, namespace.ControllerID, string(namespace.Domain))
	}
	predicate := alias + ".session_id = ? AND " + alias + ".actor_id = ?" +
		" AND " + alias + ".forgotten = 0 AND (" + strings.Join(parts, " OR ") + ")" +
		" AND (" + alias + ".expires_clock IS NULL OR " + alias + ".expires_clock != ?" +
		" OR " + alias + ".expires_value > ?)"
	arguments = append(arguments, string(query.Now.Clock), query.Now.Value)
	return predicate, arguments
}

func prefixMemoryColumns(alias string) string {
	parts := strings.Split(memoryColumns, ",")
	for index := range parts {
		parts[index] = alias + "." + strings.TrimSpace(parts[index])
	}
	return strings.Join(parts, ", ")
}

func memoryMatchesFilters(record MemoryRecord, query MemoryQuery) bool {
	for _, tag := range query.Tags {
		if !slices.Contains(record.Tags, tag) {
			return false
		}
	}
	for _, subject := range query.SubjectRefs {
		if !slices.Contains(record.SubjectRefs, subject) {
			return false
		}
	}
	return true
}

func loadMemoryByIdentity(
	ctx context.Context,
	tx *sql.Tx,
	namespace MemoryNamespace,
	memoryID string,
) (MemoryRecord, bool, error) {
	record, tombstone, err := scanMemoryRow(tx.QueryRowContext(ctx, `SELECT `+memoryColumns+`
        FROM memory_records WHERE session_id = ? AND actor_id = ? AND controller_id = ?
        AND domain = ? AND memory_id = ?`, namespace.SessionID, namespace.ActorID,
		namespace.ControllerID, string(namespace.Domain), memoryID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return MemoryRecord{}, false, ErrProviderNotFound
	}
	return record, tombstone != nil, err
}

func incrementMemoryRevision(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `UPDATE memory_meta SET value = value + 1 WHERE key = 'revision'`)
	return err
}

func sqliteMemoryKey(record MemoryRecord) string {
	return sqliteMemoryIdentityKey(record.Namespace, record.MemoryID)
}

func sqliteMemoryIdentityKey(namespace MemoryNamespace, memoryID string) string {
	return strings.Join([]string{
		namespace.SessionID, namespace.ActorID, namespace.ControllerID,
		string(namespace.Domain), memoryID,
	}, "\x1f")
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

var _ MemoryProvider = (*SQLiteMemoryProvider)(nil)
