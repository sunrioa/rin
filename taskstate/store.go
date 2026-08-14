package taskstate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/internal/sqlitedsn"
	_ "modernc.org/sqlite"
)

const (
	storeSchemaVersion = 1
	defaultMaxPlans    = 1_024
	defaultMaxEvents   = 4_096
	maxWaitMillis      = 25_000
)

type StoreConfig struct {
	MaxPlans         uint32
	MaxEventsPerPlan uint32
	Now              func() time.Time
}

type PlanEvent struct {
	Sequence             uint64        `json:"sequence"`
	PlanID               string        `json:"plan_id"`
	Revision             uint64        `json:"revision"`
	Kind                 string        `json:"kind"`
	Summary              string        `json:"summary"`
	Evidence             *PlanEvidence `json:"evidence,omitempty"`
	OperationID          string        `json:"operation_id,omitempty"`
	OccurredAtUnixMillis int64         `json:"occurred_at_unix_millis"`
}

type EventPage struct {
	Events     []PlanEvent `json:"events"`
	NextCursor uint64      `json:"next_cursor"`
	More       bool        `json:"more"`
}

type WaitInput struct {
	PlanID        string `json:"plan_id"`
	AfterRevision uint64 `json:"after_revision"`
	WaitMillis    uint32 `json:"wait_millis,omitempty"`
}

type PlanUpdate struct {
	Changed bool      `json:"changed"`
	Plan    PlanState `json:"plan"`
}

type ReviseInput struct {
	PlanID           string       `json:"plan_id"`
	ExpectedRevision uint64       `json:"expected_revision"`
	Reason           ReplanReason `json:"reason"`
	Summary          string       `json:"summary"`
	Draft            Draft        `json:"draft"`
}

type StatusInput struct {
	PlanID           string     `json:"plan_id"`
	ExpectedRevision uint64     `json:"expected_revision"`
	Status           PlanStatus `json:"status"`
	Summary          string     `json:"summary"`
}

type TransitionInput struct {
	PlanID           string       `json:"plan_id"`
	ExpectedRevision uint64       `json:"expected_revision"`
	ConditionID      string       `json:"condition_id"`
	Kind             EvidenceKind `json:"kind"`
	EvidenceID       string       `json:"evidence_id"`
}

type OperationLink struct {
	OperationID  string   `json:"operation_id"`
	PlanID       string   `json:"plan_id"`
	PlanRevision uint64   `json:"plan_revision"`
	StepID       string   `json:"step_id"`
	ConditionIDs []string `json:"condition_ids,omitempty"`
}

type OperationResult struct {
	OperationID        string
	ExecutionConfirmed bool
	Outcome            host.ActionOutcome
}

type Store struct {
	mu       sync.Mutex
	db       *sql.DB
	path     string
	lockFile *os.File
	config   StoreConfig
	closed   bool
	waiters  map[string]chan struct{}
}

func OpenSQLiteStore(path string, config StoreConfig) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, invalid("path", "is required")
	}
	if config.MaxPlans == 0 {
		config.MaxPlans = defaultMaxPlans
	}
	if config.MaxEventsPerPlan == 0 {
		config.MaxEventsPerPlan = defaultMaxEvents
	}
	if config.MaxPlans > 100_000 || config.MaxEventsPerPlan > 100_000 {
		return nil, ErrCapacity
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := prepareStorePath(absolute); err != nil {
		return nil, err
	}
	lockFile, err := acquireStoreLock(absolute + ".lock")
	if err != nil {
		return nil, err
	}
	dsn := sqlitedsn.File(absolute)
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		_ = releaseStoreLock(lockFile)
		return nil, fmt.Errorf("%w: open sqlite: %v", ErrPersist, err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	store := &Store{
		db: database, path: absolute, lockFile: lockFile, config: config,
		waiters: make(map[string]chan struct{}),
	}
	if err := store.initialize(context.Background()); err != nil {
		_ = database.Close()
		_ = releaseStoreLock(lockFile)
		return nil, err
	}
	if err := os.Chmod(absolute, 0o600); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("%w: protect database: %v", ErrPersist, err)
	}
	return store, nil
}

func (store *Store) initialize(ctx context.Context) error {
	for _, statement := range []string{
		`PRAGMA journal_mode=WAL`, `PRAGMA synchronous=FULL`,
		`PRAGMA foreign_keys=ON`, `PRAGMA busy_timeout=5000`,
	} {
		if _, err := store.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("%w: configure sqlite: %v", ErrPersist, err)
		}
	}
	var version int
	if err := store.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("%w: read schema: %v", ErrPersist, err)
	}
	if version != 0 && version != storeSchemaVersion {
		return fmt.Errorf("%w: unsupported schema %d", ErrPersist, version)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS task_plans (
			plan_id TEXT PRIMARY KEY, task_id TEXT NOT NULL, session_id TEXT NOT NULL,
			host_id TEXT NOT NULL, world_id TEXT NOT NULL, actor_id TEXT NOT NULL,
			controller_id TEXT NOT NULL, status TEXT NOT NULL, revision INTEGER NOT NULL,
			state_json TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
		) STRICT`,
		`CREATE UNIQUE INDEX IF NOT EXISTS task_plans_task_idx ON task_plans(task_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS task_plans_active_actor_idx
			ON task_plans(session_id, actor_id)
			WHERE status IN ('planned','active','blocked','paused')`,
		`CREATE TABLE IF NOT EXISTS task_plan_steps (
			plan_id TEXT NOT NULL REFERENCES task_plans(plan_id) ON DELETE CASCADE,
			step_id TEXT NOT NULL, ordinal INTEGER NOT NULL, status TEXT NOT NULL,
			step_json TEXT NOT NULL, PRIMARY KEY(plan_id, step_id), UNIQUE(plan_id, ordinal)
		) STRICT`,
		`CREATE TABLE IF NOT EXISTS task_plan_events (
			sequence INTEGER PRIMARY KEY AUTOINCREMENT, plan_id TEXT NOT NULL
			REFERENCES task_plans(plan_id) ON DELETE CASCADE, revision INTEGER NOT NULL,
			kind TEXT NOT NULL, summary TEXT NOT NULL, evidence_json TEXT,
			operation_id TEXT NOT NULL DEFAULT '', occurred_at INTEGER NOT NULL
		) STRICT`,
		`CREATE INDEX IF NOT EXISTS task_plan_events_idx
			ON task_plan_events(plan_id, sequence)`,
		`CREATE TABLE IF NOT EXISTS task_plan_operations (
			operation_id TEXT PRIMARY KEY, plan_id TEXT NOT NULL
			REFERENCES task_plans(plan_id) ON DELETE CASCADE, plan_revision INTEGER NOT NULL,
			step_id TEXT NOT NULL, condition_ids TEXT NOT NULL, terminal INTEGER NOT NULL DEFAULT 0,
			outcome_digest TEXT NOT NULL DEFAULT ''
		) STRICT`,
		`CREATE INDEX IF NOT EXISTS task_plan_operations_plan_idx
			ON task_plan_operations(plan_id, terminal)`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("%w: migrate sqlite: %v", ErrPersist, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `PRAGMA user_version = 1`); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%w: commit schema: %v", ErrPersist, err)
	}
	return nil
}

func (store *Store) Create(ctx context.Context, draft Draft) (PlanState, error) {
	if err := requireContext(ctx); err != nil {
		return PlanState{}, err
	}
	now := store.config.Now().UnixMilli()
	state, err := NewPlan(draft, now)
	if err != nil {
		return PlanState{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.ready(); err != nil {
		return PlanState{}, err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return PlanState{}, err
	}
	defer tx.Rollback()
	var count uint32
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_plans`).Scan(&count); err != nil {
		return PlanState{}, err
	}
	if count >= store.config.MaxPlans {
		return PlanState{}, ErrCapacity
	}
	if err := insertPlan(ctx, tx, state); err != nil {
		return PlanState{}, classifyStoreError(err)
	}
	if err := insertEvent(ctx, tx, state, "plan.created", "Plan created.", nil, "", now,
		store.config.MaxEventsPerPlan); err != nil {
		return PlanState{}, err
	}
	if err := tx.Commit(); err != nil {
		return PlanState{}, classifyStoreError(err)
	}
	store.notifyLocked(state.PlanID)
	return clonePlan(state), nil
}

func (store *Store) Get(ctx context.Context, planID string) (PlanState, error) {
	if err := requireContext(ctx); err != nil {
		return PlanState{}, err
	}
	if err := validateText("plan_id", planID, 256, true); err != nil {
		return PlanState{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.ready(); err != nil {
		return PlanState{}, err
	}
	return loadPlan(ctx, store.db, planID)
}

func (store *Store) Revise(ctx context.Context, input ReviseInput) (PlanState, error) {
	if err := requireContext(ctx); err != nil {
		return PlanState{}, err
	}
	if err := validateText("summary", input.Summary, 500, true); err != nil {
		return PlanState{}, err
	}
	if !validReplanReason(input.Reason) {
		return PlanState{}, invalid("reason", "is unsupported")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.ready(); err != nil {
		return PlanState{}, err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return PlanState{}, err
	}
	defer tx.Rollback()
	current, err := loadPlan(ctx, tx, input.PlanID)
	if err != nil {
		return PlanState{}, err
	}
	if current.Revision != input.ExpectedRevision {
		return PlanState{}, ErrConflict
	}
	if terminalPlanStatus(current.Status) || current.ReplanCount >= current.MaxReplans {
		return PlanState{}, ErrConflict
	}
	var activeOperations int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_plan_operations
		WHERE plan_id = ? AND terminal = 0`, current.PlanID).Scan(&activeOperations); err != nil {
		return PlanState{}, err
	}
	if activeOperations != 0 {
		return PlanState{}, fmt.Errorf("%w: plan has an unfinished operation", ErrConflict)
	}
	input.Draft.PlanID = current.PlanID
	input.Draft.TaskID = current.TaskID
	input.Draft.SessionID = current.SessionID
	input.Draft.HostID = current.HostID
	input.Draft.WorldID = current.WorldID
	input.Draft.ActorID = current.ActorID
	input.Draft.ControllerID = current.ControllerID
	input.Draft.ControllerSource = current.ControllerSource
	input.Draft.PlanningMode = current.PlanningMode
	input.Draft.MaxReplans = current.MaxReplans
	now := store.config.Now().UnixMilli()
	next, err := NewPlan(input.Draft, now)
	if err != nil {
		return PlanState{}, err
	}
	next.CreatedAtUnixMillis = current.CreatedAtUnixMillis
	next.Revision = current.Revision + 1
	next.ReplanCount = current.ReplanCount + 1
	if err := ValidatePlan(next); err != nil {
		return PlanState{}, err
	}
	if err := replacePlan(ctx, tx, current.Revision, next); err != nil {
		return PlanState{}, err
	}
	if err := insertEvent(ctx, tx, next, "plan.revised."+string(input.Reason), input.Summary,
		nil, "", now, store.config.MaxEventsPerPlan); err != nil {
		return PlanState{}, err
	}
	if err := tx.Commit(); err != nil {
		return PlanState{}, err
	}
	store.notifyLocked(next.PlanID)
	return clonePlan(next), nil
}

func (store *Store) SetStatus(ctx context.Context, input StatusInput) (PlanState, error) {
	if err := requireContext(ctx); err != nil {
		return PlanState{}, err
	}
	if input.Status != PlanPaused && input.Status != PlanActive && input.Status != PlanCancelled {
		return PlanState{}, invalid("status", "only active, paused, or cancelled is accepted")
	}
	if err := validateText("summary", input.Summary, 500, true); err != nil {
		return PlanState{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.ready(); err != nil {
		return PlanState{}, err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return PlanState{}, err
	}
	defer tx.Rollback()
	state, err := loadPlan(ctx, tx, input.PlanID)
	if err != nil {
		return PlanState{}, err
	}
	if state.Revision != input.ExpectedRevision || terminalPlanStatus(state.Status) {
		return PlanState{}, ErrConflict
	}
	if input.Status == PlanActive && state.Status != PlanPaused {
		return PlanState{}, fmt.Errorf("%w: only a paused plan can resume", ErrConflict)
	}
	if input.Status == PlanPaused && state.Status != PlanActive && state.Status != PlanBlocked {
		return PlanState{}, fmt.Errorf("%w: plan cannot be paused", ErrConflict)
	}
	if input.Status == PlanCancelled {
		var active int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_plan_operations
			WHERE plan_id = ? AND terminal = 0`, state.PlanID).Scan(&active); err != nil {
			return PlanState{}, err
		}
		if active != 0 {
			return PlanState{}, fmt.Errorf("%w: cancel linked operations first", ErrConflict)
		}
	}
	state.Status = input.Status
	if input.Status == PlanCancelled {
		for index := range state.Steps {
			if state.Steps[index].Status == StepActive || state.Steps[index].Status == StepPending ||
				state.Steps[index].Status == StepBlocked {
				state.Steps[index].Status = StepSkipped
			}
		}
		state.CurrentStepID = ""
	}
	state.Revision++
	state.UpdatedAtUnixMillis = store.config.Now().UnixMilli()
	if err := ValidatePlan(state); err != nil {
		return PlanState{}, err
	}
	if err := replacePlan(ctx, tx, input.ExpectedRevision, state); err != nil {
		return PlanState{}, err
	}
	if err := insertEvent(ctx, tx, state, "plan."+string(input.Status), input.Summary,
		nil, "", state.UpdatedAtUnixMillis, store.config.MaxEventsPerPlan); err != nil {
		return PlanState{}, err
	}
	if err := tx.Commit(); err != nil {
		return PlanState{}, err
	}
	store.notifyLocked(state.PlanID)
	return clonePlan(state), nil
}

func (store *Store) LinkOperation(ctx context.Context, link OperationLink) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if err := validateOperationLink(link); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.ready(); err != nil {
		return err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	state, err := loadPlan(ctx, tx, link.PlanID)
	if err != nil {
		return err
	}
	if state.Revision != link.PlanRevision || state.Status != PlanActive ||
		state.CurrentStepID != link.StepID {
		return ErrConflict
	}
	if err := validateLinkConditions(state, link); err != nil {
		return err
	}
	slices.Sort(link.ConditionIDs)
	var unfinished int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_plan_operations
		WHERE plan_id = ? AND terminal = 0 AND operation_id != ?`,
		link.PlanID, link.OperationID).Scan(&unfinished); err != nil {
		return err
	}
	if unfinished != 0 {
		return fmt.Errorf("%w: current step already has an unfinished operation", ErrConflict)
	}
	encoded, _ := json.Marshal(link.ConditionIDs)
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO task_plan_operations(
		operation_id, plan_id, plan_revision, step_id, condition_ids
	) VALUES (?, ?, ?, ?, ?)`, link.OperationID, link.PlanID, link.PlanRevision,
		link.StepID, string(encoded))
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		var existing OperationLink
		var conditions string
		if err := tx.QueryRowContext(ctx, `SELECT operation_id, plan_id, plan_revision,
			step_id, condition_ids FROM task_plan_operations WHERE operation_id = ?`,
			link.OperationID).Scan(&existing.OperationID, &existing.PlanID,
			&existing.PlanRevision, &existing.StepID, &conditions); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(conditions), &existing.ConditionIDs); err != nil {
			return err
		}
		if !operationLinksCompatible(existing, link) {
			return ErrConflict
		}
	}
	return tx.Commit()
}

func (store *Store) ApplyTrustedEvidence(
	ctx context.Context,
	planID string,
	expectedRevision uint64,
	evidence PlanEvidence,
	summary string,
) (PlanState, error) {
	if err := requireContext(ctx); err != nil {
		return PlanState{}, err
	}
	if err := validateText("summary", summary, 500, true); err != nil {
		return PlanState{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.ready(); err != nil {
		return PlanState{}, err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return PlanState{}, err
	}
	defer tx.Rollback()
	state, err := loadPlan(ctx, tx, planID)
	if err != nil {
		return PlanState{}, err
	}
	if state.Revision != expectedRevision {
		return PlanState{}, ErrConflict
	}
	next, changed, err := ApplyEvidence(state, evidence, store.config.Now().UnixMilli())
	if err != nil {
		return PlanState{}, err
	}
	if !changed && next.Revision == state.Revision {
		return next, nil
	}
	if err := replacePlan(ctx, tx, state.Revision, next); err != nil {
		return PlanState{}, err
	}
	if err := insertEvent(ctx, tx, next, "plan.evidence", summary, &evidence, evidence.OperationID,
		next.UpdatedAtUnixMillis, store.config.MaxEventsPerPlan); err != nil {
		return PlanState{}, err
	}
	if err := tx.Commit(); err != nil {
		return PlanState{}, err
	}
	store.notifyLocked(planID)
	return next, nil
}

// ReconcileOperationLink restores a link from a Control Plane-owned
// ActionRequest after a crash between action submission and local linking.
// It cannot advance a plan and accepts no future revision.
func (store *Store) ReconcileOperationLink(ctx context.Context, link OperationLink) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if err := validateOperationLink(link); err != nil {
		return err
	}
	slices.Sort(link.ConditionIDs)
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.ready(); err != nil {
		return err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	state, err := loadPlan(ctx, tx, link.PlanID)
	if err != nil {
		return err
	}
	if link.PlanRevision > state.Revision {
		return ErrConflict
	}
	encoded, _ := json.Marshal(link.ConditionIDs)
	result, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO task_plan_operations(
		operation_id, plan_id, plan_revision, step_id, condition_ids
	) VALUES (?, ?, ?, ?, ?)`, link.OperationID, link.PlanID, link.PlanRevision,
		link.StepID, string(encoded))
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		var existing OperationLink
		var conditions string
		if err := tx.QueryRowContext(ctx, `SELECT operation_id, plan_id, plan_revision,
			step_id, condition_ids FROM task_plan_operations WHERE operation_id = ?`,
			link.OperationID).Scan(&existing.OperationID, &existing.PlanID,
			&existing.PlanRevision, &existing.StepID, &conditions); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(conditions), &existing.ConditionIDs); err != nil {
			return err
		}
		if !operationLinksCompatible(existing, link) {
			return ErrConflict
		}
	}
	return tx.Commit()
}

func (store *Store) ApplyOperationResult(ctx context.Context, result OperationResult) (PlanState, bool, error) {
	if err := requireContext(ctx); err != nil {
		return PlanState{}, false, err
	}
	if !result.ExecutionConfirmed {
		return PlanState{}, false, invalid("execution_confirmed", "must be true")
	}
	if err := host.ValidateActionOutcome(result.Outcome); err != nil {
		return PlanState{}, false, fmt.Errorf("%w: outcome: %v", ErrInvalid, err)
	}
	if result.OperationID != result.Outcome.OperationID {
		return PlanState{}, false, invalid("operation_id", "does not match outcome")
	}
	payload, _ := json.Marshal(result.Outcome)
	digestValue := sha256.Sum256(payload)
	digest := hex.EncodeToString(digestValue[:])
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.ready(); err != nil {
		return PlanState{}, false, err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return PlanState{}, false, err
	}
	defer tx.Rollback()
	var link OperationLink
	var conditions, existingDigest string
	var terminal int
	if err := tx.QueryRowContext(ctx, `SELECT operation_id, plan_id, plan_revision,
		step_id, condition_ids, terminal, outcome_digest FROM task_plan_operations
		WHERE operation_id = ?`, result.OperationID).Scan(
		&link.OperationID, &link.PlanID, &link.PlanRevision, &link.StepID,
		&conditions, &terminal, &existingDigest,
	); errors.Is(err, sql.ErrNoRows) {
		return PlanState{}, false, ErrNotFound
	} else if err != nil {
		return PlanState{}, false, err
	}
	if err := json.Unmarshal([]byte(conditions), &link.ConditionIDs); err != nil {
		return PlanState{}, false, err
	}
	state, err := loadPlan(ctx, tx, link.PlanID)
	if err != nil {
		return PlanState{}, false, err
	}
	if terminal != 0 {
		if existingDigest != digest {
			return PlanState{}, false, ErrConflict
		}
		return state, false, nil
	}
	changed := false
	applicable := state.Revision == link.PlanRevision && state.CurrentStepID == link.StepID
	now := store.config.Now().UnixMilli()
	if applicable {
		if result.Outcome.Status == host.ActionSucceeded {
			conditionIDs := link.ConditionIDs
			if len(conditionIDs) == 0 {
				conditionIDs = operationConditionIDs(state)
			}
			for _, conditionID := range conditionIDs {
				evidence := PlanEvidence{
					EvidenceID:  result.OperationID + "." + conditionID,
					ConditionID: conditionID, Kind: EvidenceOperationOutcome,
					OperationID: result.OperationID, Epoch: result.Outcome.Epoch,
					ObservationSequence: result.Outcome.WorldSeq, Digest: digest,
					RecordedAtUnixMillis: now,
				}
				state, _, err = ApplyEvidence(state, evidence, now)
				if err != nil {
					return PlanState{}, false, err
				}
				changed = true
			}
		} else {
			family := string(result.Outcome.Status)
			code := result.Outcome.Code
			if code == "" {
				code = family
			}
			state, err = ApplyFailure(state, result.OperationID, family, code, now)
			if err != nil {
				return PlanState{}, false, err
			}
			changed = true
		}
	}
	if changed {
		if err := replacePlan(ctx, tx, link.PlanRevision, state); err != nil {
			return PlanState{}, false, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE task_plan_operations
		SET terminal = 1, outcome_digest = ? WHERE operation_id = ?`, digest,
		result.OperationID); err != nil {
		return PlanState{}, false, err
	}
	kind := "operation.stale"
	if applicable {
		kind = "operation.outcome"
	}
	evidence := PlanEvidence{
		EvidenceID: result.OperationID, ConditionID: "operation.result",
		Kind: EvidenceOperationOutcome, OperationID: result.OperationID,
		Epoch: result.Outcome.Epoch, ObservationSequence: result.Outcome.WorldSeq,
		Digest: digest, RecordedAtUnixMillis: now,
	}
	if err := insertEvent(ctx, tx, state, kind, result.Outcome.Summary, &evidence,
		result.OperationID, now, store.config.MaxEventsPerPlan); err != nil {
		return PlanState{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return PlanState{}, false, err
	}
	store.notifyLocked(state.PlanID)
	return clonePlan(state), changed, nil
}

func (store *Store) Events(ctx context.Context, planID string, after uint64, limit uint32) (EventPage, error) {
	if err := requireContext(ctx); err != nil {
		return EventPage{}, err
	}
	if limit == 0 {
		limit = 64
	}
	if limit > 256 {
		return EventPage{}, ErrCapacity
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.ready(); err != nil {
		return EventPage{}, err
	}
	if _, err := loadPlan(ctx, store.db, planID); err != nil {
		return EventPage{}, err
	}
	rows, err := store.db.QueryContext(ctx, `SELECT sequence, plan_id, revision, kind,
		summary, evidence_json, operation_id, occurred_at FROM task_plan_events
		WHERE plan_id = ? AND sequence > ? ORDER BY sequence LIMIT ?`, planID, after, limit+1)
	if err != nil {
		return EventPage{}, err
	}
	defer rows.Close()
	page := EventPage{NextCursor: after}
	for rows.Next() {
		var event PlanEvent
		var evidence sql.NullString
		if err := rows.Scan(&event.Sequence, &event.PlanID, &event.Revision, &event.Kind,
			&event.Summary, &evidence, &event.OperationID, &event.OccurredAtUnixMillis); err != nil {
			return EventPage{}, err
		}
		if evidence.Valid {
			var item PlanEvidence
			if err := json.Unmarshal([]byte(evidence.String), &item); err != nil {
				return EventPage{}, err
			}
			event.Evidence = &item
		}
		page.Events = append(page.Events, event)
	}
	if err := rows.Err(); err != nil {
		return EventPage{}, err
	}
	if len(page.Events) > int(limit) {
		page.Events = page.Events[:limit]
		page.More = true
	}
	if len(page.Events) != 0 {
		page.NextCursor = page.Events[len(page.Events)-1].Sequence
	}
	return page, nil
}

func (store *Store) Wait(ctx context.Context, input WaitInput) (PlanUpdate, error) {
	if input.WaitMillis > maxWaitMillis {
		return PlanUpdate{}, ErrCapacity
	}
	state, err := store.Get(ctx, input.PlanID)
	if err != nil {
		return PlanUpdate{}, err
	}
	if input.AfterRevision > state.Revision {
		return PlanUpdate{}, ErrInvalid
	}
	if state.Revision > input.AfterRevision {
		return PlanUpdate{Changed: true, Plan: state}, nil
	}
	store.mu.Lock()
	if err := store.ready(); err != nil {
		store.mu.Unlock()
		return PlanUpdate{}, err
	}
	wakeup := store.waiterLocked(input.PlanID)
	store.mu.Unlock()
	wait := time.Duration(input.WaitMillis) * time.Millisecond
	if wait == 0 {
		return PlanUpdate{Plan: state}, nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return PlanUpdate{}, ctx.Err()
	case <-timer.C:
		latest, err := store.Get(ctx, input.PlanID)
		return PlanUpdate{Changed: latest.Revision > input.AfterRevision, Plan: latest}, err
	case <-wakeup:
		latest, err := store.Get(ctx, input.PlanID)
		return PlanUpdate{Changed: latest.Revision > input.AfterRevision, Plan: latest}, err
	}
}

func (store *Store) Close() error {
	if store == nil {
		return nil
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closed {
		return nil
	}
	store.closed = true
	for _, waiter := range store.waiters {
		close(waiter)
	}
	store.waiters = nil
	dbErr := store.db.Close()
	lockErr := releaseStoreLock(store.lockFile)
	store.lockFile = nil
	return errors.Join(dbErr, lockErr)
}

func insertPlan(ctx context.Context, tx *sql.Tx, state PlanState) error {
	payload, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO task_plans(
		plan_id, task_id, session_id, host_id, world_id, actor_id, controller_id,
		status, revision, state_json, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, state.PlanID, state.TaskID,
		state.SessionID, state.HostID, state.WorldID, state.ActorID, state.ControllerID,
		string(state.Status), state.Revision, string(payload), state.CreatedAtUnixMillis,
		state.UpdatedAtUnixMillis); err != nil {
		return err
	}
	return replaceSteps(ctx, tx, state)
}

func replacePlan(ctx context.Context, tx *sql.Tx, expected uint64, state PlanState) error {
	payload, err := json.Marshal(state)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE task_plans SET status = ?, revision = ?,
		state_json = ?, updated_at = ? WHERE plan_id = ? AND revision = ?`,
		string(state.Status), state.Revision, string(payload), state.UpdatedAtUnixMillis,
		state.PlanID, expected)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows != 1 {
		return ErrConflict
	}
	return replaceSteps(ctx, tx, state)
}

func replaceSteps(ctx context.Context, tx *sql.Tx, state PlanState) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM task_plan_steps WHERE plan_id = ?`, state.PlanID); err != nil {
		return err
	}
	for index, step := range state.Steps {
		payload, err := json.Marshal(step)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO task_plan_steps(
			plan_id, step_id, ordinal, status, step_json
		) VALUES (?, ?, ?, ?, ?)`, state.PlanID, step.StepID, index,
			string(step.Status), string(payload)); err != nil {
			return err
		}
	}
	return nil
}

type planQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadPlan(ctx context.Context, query planQuerier, planID string) (PlanState, error) {
	var payload string
	if err := query.QueryRowContext(ctx, `SELECT state_json FROM task_plans WHERE plan_id = ?`,
		planID).Scan(&payload); errors.Is(err, sql.ErrNoRows) {
		return PlanState{}, ErrNotFound
	} else if err != nil {
		return PlanState{}, err
	}
	decoder := json.NewDecoder(bytes.NewBufferString(payload))
	decoder.DisallowUnknownFields()
	var state PlanState
	if err := decoder.Decode(&state); err != nil {
		return PlanState{}, fmt.Errorf("%w: decode plan: %v", ErrPersist, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return PlanState{}, fmt.Errorf("%w: plan has trailing JSON", ErrPersist)
	}
	if err := ValidatePlan(state); err != nil {
		return PlanState{}, fmt.Errorf("%w: stored plan: %v", ErrPersist, err)
	}
	return clonePlan(state), nil
}

func insertEvent(
	ctx context.Context,
	tx *sql.Tx,
	state PlanState,
	kind string,
	summary string,
	evidence *PlanEvidence,
	operationID string,
	now int64,
	maximum uint32,
) error {
	var count uint32
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_plan_events WHERE plan_id = ?`,
		state.PlanID).Scan(&count); err != nil {
		return err
	}
	if count >= maximum {
		return ErrCapacity
	}
	var payload any
	if evidence != nil {
		encoded, err := json.Marshal(evidence)
		if err != nil {
			return err
		}
		payload = string(encoded)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO task_plan_events(
		plan_id, revision, kind, summary, evidence_json, operation_id, occurred_at
	) VALUES (?, ?, ?, ?, ?, ?, ?)`, state.PlanID, state.Revision, kind, summary,
		payload, operationID, now)
	return err
}

func validateOperationLink(link OperationLink) error {
	for field, value := range map[string]string{
		"operation_id": link.OperationID, "plan_id": link.PlanID, "step_id": link.StepID,
	} {
		if err := validateText(field, value, 256, true); err != nil {
			return err
		}
	}
	if link.PlanRevision == 0 || link.PlanRevision > maxWireInteger || len(link.ConditionIDs) > 16 {
		return ErrInvalid
	}
	link.ConditionIDs = append([]string(nil), link.ConditionIDs...)
	slices.Sort(link.ConditionIDs)
	for index, value := range link.ConditionIDs {
		if err := validateText("condition_id", value, 128, true); err != nil {
			return err
		}
		if index > 0 && link.ConditionIDs[index-1] == value {
			return ErrInvalid
		}
	}
	return nil
}

func validateLinkConditions(state PlanState, link OperationLink) error {
	index := currentStepIndex(state)
	if index < 0 {
		return ErrConflict
	}
	for _, id := range link.ConditionIDs {
		if !conditionInList(state.Steps[index].SuccessConditions, id, EvidenceOperationOutcome) &&
			!conditionInList(state.SuccessConditions, id, EvidenceOperationOutcome) {
			return invalid("condition_ids", "must name operation-outcome conditions")
		}
	}
	return nil
}

func operationConditionIDs(state PlanState) []string {
	index := currentStepIndex(state)
	if index < 0 {
		return nil
	}
	result := make([]string, 0, len(state.Steps[index].SuccessConditions))
	for _, condition := range state.Steps[index].SuccessConditions {
		if condition.Kind == EvidenceOperationOutcome {
			result = append(result, condition.ConditionID)
		}
	}
	return result
}

func operationLinksEqual(left, right OperationLink) bool {
	return left.OperationID == right.OperationID && left.PlanID == right.PlanID &&
		left.PlanRevision == right.PlanRevision && left.StepID == right.StepID &&
		slices.Equal(left.ConditionIDs, right.ConditionIDs)
}

func operationLinksCompatible(left, right OperationLink) bool {
	if left.OperationID != right.OperationID || left.PlanID != right.PlanID ||
		left.PlanRevision != right.PlanRevision || left.StepID != right.StepID {
		return false
	}
	return len(left.ConditionIDs) == 0 || len(right.ConditionIDs) == 0 ||
		slices.Equal(left.ConditionIDs, right.ConditionIDs)
}

func terminalPlanStatus(status PlanStatus) bool {
	return status == PlanCompleted || status == PlanFailed || status == PlanCancelled
}

func validReplanReason(reason ReplanReason) bool {
	switch reason {
	case ReplanGoalChanged, ReplanPreconditionInvalidated, ReplanRequiredCapabilityMissing,
		ReplanFailureThresholdReached, ReplanMacroUnrecoverable, ReplanEpochInvalidated,
		ReplanManualAuthorized:
		return true
	default:
		return false
	}
}

func classifyStoreError(err error) error {
	if strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return ErrConflict
	}
	return err
}

func requireContext(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalid
	}
	return ctx.Err()
}

func (store *Store) ready() error {
	if store.closed {
		return ErrClosed
	}
	return nil
}

func (store *Store) waiterLocked(planID string) chan struct{} {
	wakeup := store.waiters[planID]
	if wakeup == nil {
		wakeup = make(chan struct{})
		store.waiters[planID] = wakeup
	}
	return wakeup
}

func (store *Store) notifyLocked(planID string) {
	if wakeup := store.waiters[planID]; wakeup != nil {
		close(wakeup)
	}
	store.waiters[planID] = make(chan struct{})
}

func prepareStorePath(path string) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("%w: create directory: %v", ErrPersist, err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return fmt.Errorf("%w: protect directory: %v", ErrPersist, err)
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: parent is not a real directory", ErrPersist)
	}
	info, err = os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: database is not a real file", ErrPersist)
	}
	return nil
}
