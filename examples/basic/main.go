package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sunrioa/rin/protocol"
)

type client struct {
	baseURL string
	token   string
	http    *http.Client
}

const defaultMaxRinResponseBytes = 32 << 20

type envelope struct {
	OK    bool                  `json:"ok"`
	Data  json.RawMessage       `json:"data"`
	Error *protocol.ErrorDetail `json:"error"`
}

type appliedOutcome struct {
	accepted bool
	outcome  string
}

// appliedMarker is game-owned proof that one operation already affected the
// authoritative world. ProposalID and OccurrenceTick are stored independently
// from the Outbox so a valid-looking replacement Commit cannot authenticate
// itself during recovery.
type appliedMarker struct {
	outcome        appliedOutcome
	proposalID     string
	occurrenceTick int64
}

type pendingReport struct {
	kind     string
	commit   protocol.CommitRequest
	observe  protocol.ObserveRequest
	fallback protocol.ObserveRequest
}

// proposalAttempt is persisted before the first Propose POST.  Until the
// authoritative effect and its report are durably recorded together, this
// exact request remains the only turn the example is allowed to resume.
type proposalAttempt struct {
	OperationID string
	Sequence    uint64
	Request     protocol.ProposeRequest
	Fallback    protocol.ActionProposal
	Submitted   bool
}

type gameTransaction struct {
	rollbacks []func()
}

func (tx *gameTransaction) onRollback(rollback func()) {
	if rollback != nil {
		tx.rollbacks = append(tx.rollbacks, rollback)
	}
}

func (tx *gameTransaction) rollback() {
	for index := len(tx.rollbacks) - 1; index >= 0; index-- {
		tx.rollbacks[index]()
	}
}

// gameOutcomeStore is the smallest useful game-side outcome state machine:
// applied prevents a repeated operation from touching game state twice, while
// pending retains the exact Commit or Observe until Rin acknowledges it.
type gameOutcomeStore struct {
	runID                    string
	create                   protocol.CreateSessionRequest
	operationSequence        uint64
	lastAuthoritativeTick    int64
	proposalAttempt          *proposalAttempt
	applied                  map[string]appliedMarker
	pending                  map[string]pendingReport
	authoritativeTransaction func(func(*gameTransaction) error) error
	persistReportConversion  func(string, pendingReport) error
	persistReportAck         func(string) error
	currentTick              func() int64
	applyEffect              func(*gameTransaction, protocol.ActionSpec)
	durabilityBlocked        error
}

const gameOutcomeStateVersion = 3
const exampleRunIDLayout = "20060102T150405.000000000"

type persistedAppliedOutcome struct {
	Accepted       bool   `json:"accepted"`
	Outcome        string `json:"outcome"`
	ProposalID     string `json:"proposal_id"`
	OccurrenceTick int64  `json:"occurrence_tick"`
}

type persistedPendingReport struct {
	Kind     string                  `json:"kind"`
	Commit   protocol.CommitRequest  `json:"commit,omitempty"`
	Observe  protocol.ObserveRequest `json:"observe,omitempty"`
	Fallback protocol.ObserveRequest `json:"fallback,omitempty"`
}

type persistedProposalAttempt struct {
	OperationID string                  `json:"operation_id"`
	Sequence    uint64                  `json:"sequence"`
	Request     protocol.ProposeRequest `json:"request"`
	Fallback    protocol.ActionProposal `json:"fallback"`
	Submitted   bool                    `json:"submitted"`
}

type persistedGameOutcomeState struct {
	Version               int                                `json:"version"`
	RunID                 string                             `json:"run_id"`
	Create                protocol.CreateSessionRequest      `json:"create"`
	OperationSequence     uint64                             `json:"operation_sequence"`
	LastAuthoritativeTick int64                              `json:"last_authoritative_tick"`
	ProposalAttempt       *persistedProposalAttempt          `json:"proposal_attempt,omitempty"`
	Applied               map[string]persistedAppliedOutcome `json:"applied"`
	Pending               map[string]persistedPendingReport  `json:"pending"`
}

func main() {
	address := flag.String("url", "http://127.0.0.1:7374", "Rin base URL")
	statePath := flag.String(
		"state",
		defaultGameOutcomeStatePath(),
		"durable game-side marker and outcome Outbox file",
	)
	flag.Parse()
	c := client{baseURL: *address, token: os.Getenv("RIN_TOKEN"), http: &http.Client{Timeout: 5 * time.Second}}
	game, err := newDurableGameOutcomeStore(*statePath)
	must(err)
	must(game.runExampleInvocation(&c))
}

func newGameOutcomeStore() *gameOutcomeStore {
	store := &gameOutcomeStore{
		applied: make(map[string]appliedMarker),
		pending: make(map[string]pendingReport),
		currentTick: func() int64 {
			return 0
		},
		applyEffect: applyGameEffect,
		persistReportConversion: func(string, pendingReport) error {
			// PRODUCTION PERSISTENCE HOOK: atomically replace the Commit with
			// its pre-persisted Observe fallback before updating this cache.
			return nil
		},
		persistReportAck: func(string) error {
			// PRODUCTION PERSISTENCE HOOK: durably delete the Outbox row. Only
			// after this succeeds may the in-memory cache evict the report.
			return nil
		},
	}
	store.authoritativeTransaction = runInMemoryGameTransaction
	return store
}

func defaultGameOutcomeStatePath() string {
	configDir, err := os.UserConfigDir()
	if err != nil || configDir == "" {
		return "rin-basic-example-state.json"
	}
	return filepath.Join(configDir, "rin", "basic-example-state.json")
}

func newExampleRun(now time.Time) (string, protocol.CreateSessionRequest) {
	runID := now.UTC().Format(exampleRunIDLayout)
	return runID, exampleCreateRequest(runID)
}

func exampleCreateRequest(runID string) protocol.CreateSessionRequest {
	sessionID := "example." + runID
	return protocol.CreateSessionRequest{
		ProtocolVersion: protocol.Version,
		RequestID:       "create." + runID,
		SessionID:       sessionID,
		Binding: protocol.Binding{
			GameID: "rin-example", ContentID: "base",
			ContentVersion: "1.0.0", ContentHash: "example-hash",
		},
		Seed:     42,
		Features: []string{protocol.FeatureOutcomeReporting},
		Actors: []protocol.ActorSeed{{
			ID: "npc.mira", Kind: "npc", DisplayName: "Mira",
			Traits: []string{"curious", "careful"}, ThinkEveryTicks: 5, Enabled: true,
			Boundaries: []protocol.Boundary{{
				ID: "boundary.privacy", Description: "Do not reveal private letters.",
				TriggerTags: []string{"private"}, Response: "refuse",
			}},
			Goals: []protocol.Goal{{
				ID: "goal.connect", Description: "Build trust through specific actions.",
				Priority: 4, PreferredActions: []string{"talk"},
				TargetProgress: 3, Status: "active",
			}},
		}},
	}
}

func newDurableGameOutcomeStore(path string) (*gameOutcomeStore, error) {
	if path == "" {
		return nil, errors.New("durable game state path is empty")
	}
	state, exists, err := loadGameOutcomeState(path)
	if err != nil {
		return nil, fmt.Errorf("restore durable game state: %w", err)
	}
	store := newGameOutcomeStore()
	if exists {
		if err := store.restore(state); err != nil {
			return nil, fmt.Errorf("restore durable game state: %w", err)
		}
	} else {
		store.runID, store.create = newExampleRun(time.Now())
		if err := persistGameOutcomeState(path, store.snapshot()); err != nil {
			// Fail closed before making a new request if the stable identity and
			// Outbox cannot be made durable.
			return nil, fmt.Errorf("initialize durable game state: %w", err)
		}
	}

	store.authoritativeTransaction = func(mutate func(*gameTransaction) error) error {
		return store.runPersistedMutation(mutate, func() error {
			return persistGameOutcomeState(path, store.snapshot())
		})
	}
	store.persistReportConversion = func(
		operationID string,
		replacement pendingReport,
	) error {
		state := store.snapshot()
		state.Pending[operationID] = persistPendingReport(replacement)
		return persistGameOutcomeState(path, state)
	}
	store.persistReportAck = func(operationID string) error {
		state := store.snapshot()
		delete(state.Pending, operationID)
		return persistGameOutcomeState(path, state)
	}
	return store, nil
}

func (s *gameOutcomeStore) snapshot() persistedGameOutcomeState {
	state := persistedGameOutcomeState{
		Version:               gameOutcomeStateVersion,
		RunID:                 s.runID,
		Create:                s.create,
		OperationSequence:     s.operationSequence,
		LastAuthoritativeTick: s.lastAuthoritativeTick,
		Applied:               make(map[string]persistedAppliedOutcome, len(s.applied)),
		Pending:               make(map[string]persistedPendingReport, len(s.pending)),
	}
	if s.proposalAttempt != nil {
		state.ProposalAttempt = &persistedProposalAttempt{
			OperationID: s.proposalAttempt.OperationID,
			Sequence:    s.proposalAttempt.Sequence,
			Request:     s.proposalAttempt.Request,
			Fallback:    s.proposalAttempt.Fallback,
			Submitted:   s.proposalAttempt.Submitted,
		}
	}
	for operationID, marker := range s.applied {
		state.Applied[operationID] = persistedAppliedOutcome{
			Accepted:       marker.outcome.accepted,
			Outcome:        marker.outcome.outcome,
			ProposalID:     marker.proposalID,
			OccurrenceTick: marker.occurrenceTick,
		}
	}
	for operationID, report := range s.pending {
		state.Pending[operationID] = persistPendingReport(report)
	}
	return state
}

func persistPendingReport(report pendingReport) persistedPendingReport {
	return persistedPendingReport{
		Kind:     report.kind,
		Commit:   report.commit,
		Observe:  report.observe,
		Fallback: report.fallback,
	}
}

func validatePersistedGameOutcomeState(state persistedGameOutcomeState) error {
	if state.Version != gameOutcomeStateVersion {
		return fmt.Errorf(
			"unsupported state version %d (want %d)",
			state.Version,
			gameOutcomeStateVersion,
		)
	}
	if state.Applied == nil || state.Pending == nil {
		return errors.New("state is missing applied or pending maps")
	}
	if state.RunID == "" ||
		state.Create.SessionID != "example."+state.RunID ||
		state.Create.RequestID != "create."+state.RunID {
		return errors.New("state has a non-canonical run, Session, or Create request identity")
	}
	parsedRunID, err := time.Parse(exampleRunIDLayout, state.RunID)
	if err != nil || parsedRunID.Format(exampleRunIDLayout) != state.RunID {
		return errors.New("state run ID is not canonical")
	}
	if err := protocol.ValidateCreateSession(state.Create); err != nil {
		return fmt.Errorf("state contains an invalid Create request: %w", err)
	}
	if !reflect.DeepEqual(state.Create, exampleCreateRequest(state.RunID)) {
		return errors.New("state Create request differs from the canonical persisted payload")
	}
	hasOutcomeReporting := false
	for _, feature := range state.Create.Features {
		if feature == protocol.FeatureOutcomeReporting {
			hasOutcomeReporting = true
			break
		}
	}
	if !hasOutcomeReporting {
		return errors.New("state Create request does not enable outcome-reporting-v1")
	}
	const maxInt64 = uint64(^uint64(0) >> 1)
	if state.OperationSequence > maxInt64 || state.LastAuthoritativeTick < 0 {
		return errors.New("state contains an invalid operation sequence or tick high-water")
	}

	validateOperation := func(operationID string) (uint64, error) {
		prefix := "turn." + state.RunID + "."
		if !strings.HasPrefix(operationID, prefix) {
			return 0, fmt.Errorf("operation %q is not bound to run %q", operationID, state.RunID)
		}
		suffix := strings.TrimPrefix(operationID, prefix)
		sequence, err := strconv.ParseUint(suffix, 10, 64)
		if err != nil || sequence == 0 ||
			strconv.FormatUint(sequence, 10) != suffix ||
			sequence > state.OperationSequence {
			return 0, fmt.Errorf("operation %q has a non-canonical sequence", operationID)
		}
		return sequence, nil
	}

	for operationID, marker := range state.Applied {
		if _, err := validateOperation(operationID); err != nil {
			return err
		}
		if marker.Accepted && marker.Outcome == "" {
			return fmt.Errorf("accepted applied marker %q has no outcome", operationID)
		}
		if marker.ProposalID == "" ||
			marker.OccurrenceTick < 0 ||
			marker.OccurrenceTick > state.LastAuthoritativeTick {
			return fmt.Errorf(
				"applied marker %q has an invalid Proposal identity or occurrence tick",
				operationID,
			)
		}
	}
	if state.ProposalAttempt != nil {
		attempt := state.ProposalAttempt
		sequence, err := validateOperation(attempt.OperationID)
		if err != nil {
			return err
		}
		if sequence != attempt.Sequence ||
			sequence != state.OperationSequence ||
			attempt.Request.SessionID != state.Create.SessionID ||
			attempt.Request.RequestID != fmt.Sprintf(
				"propose.%s.%d",
				state.RunID,
				attempt.Sequence,
			) {
			return errors.New("state Proposal Attempt identity is not canonical")
		}
		if err := protocol.ValidatePropose(attempt.Request); err != nil {
			return fmt.Errorf("state contains an invalid Propose request: %w", err)
		}
		if !reflect.DeepEqual(
			attempt.Request,
			exampleProposeRequest(
				state.RunID,
				state.Create.SessionID,
				attempt.Sequence,
				attempt.Request.Tick,
			),
		) {
			return errors.New("state Propose request differs from its canonical payload")
		}
		if attempt.Request.Tick > state.LastAuthoritativeTick {
			return errors.New("state Proposal Attempt tick exceeds the authoritative high-water")
		}
		expectedFallback, fallbackErr := authoredFallback(attempt.Request, "wait")
		if fallbackErr != nil ||
			!reflect.DeepEqual(attempt.Fallback, expectedFallback) {
			return errors.New("state Proposal Attempt contains an invalid authored fallback")
		}
		if _, applied := state.Applied[attempt.OperationID]; applied {
			return errors.New("state Proposal Attempt already has an applied marker")
		}
		if len(state.Pending) != 0 {
			return errors.New("state cannot contain a Proposal Attempt and an outcome Outbox together")
		}
	}

	for operationID, report := range state.Pending {
		sequence, err := validateOperation(operationID)
		if err != nil {
			return err
		}
		marker, ok := state.Applied[operationID]
		if !ok {
			return fmt.Errorf("pending operation %q has no authoritative applied marker", operationID)
		}
		switch report.Kind {
		case "commit":
			if !reflect.DeepEqual(report.Observe, protocol.ObserveRequest{}) {
				return fmt.Errorf("pending Commit %q contains an unexpected Observe", operationID)
			}
			if err := validatePersistedCommitReport(
				state,
				operationID,
				marker,
				report,
			); err != nil {
				return err
			}
		case "observe":
			if report.Commit.RequestID != "" {
				if err := validatePersistedCommitReport(
					state,
					operationID,
					marker,
					report,
				); err != nil {
					return err
				}
				if !reflect.DeepEqual(report.Observe, report.Fallback) {
					return fmt.Errorf("converted Observe %q does not equal its persisted fallback", operationID)
				}
			} else {
				if !reflect.DeepEqual(report.Commit, protocol.CommitRequest{}) ||
					!reflect.DeepEqual(report.Fallback, protocol.ObserveRequest{}) {
					return fmt.Errorf("pending Observe %q contains unexpected Commit data", operationID)
				}
				if err := protocol.ValidateObserve(report.Observe); err != nil {
					return fmt.Errorf("pending Observe %q is invalid: %w", operationID, err)
				}
				expectedOutcome := planGameAction(protocol.ActionSpec{ID: "wait"})
				expectedMarker := persistedAppliedOutcome{
					Accepted: expectedOutcome.accepted,
					Outcome:  expectedOutcome.outcome,
					ProposalID: fmt.Sprintf(
						"offline.propose.%s.%d.wait",
						state.RunID,
						sequence,
					),
					OccurrenceTick: marker.OccurrenceTick,
				}
				expectedObserve := protocol.ObserveRequest{
					ProtocolVersion: protocol.Version,
					SessionID:       state.Create.SessionID,
					RequestID:       "reconcile." + operationID,
					EventID:         "fallback." + operationID,
					Tick:            marker.OccurrenceTick,
					ObserverIDs:     []string{"npc.mira"},
					Source:          "basic-example",
					Kind:            "fallback_action",
					Summary:         "Local fallback wait: " + expectedOutcome.outcome,
					Tags:            []string{"fallback"},
					Importance:      3,
				}
				if marker != expectedMarker ||
					!reflect.DeepEqual(report.Observe, expectedObserve) {
					return fmt.Errorf("pending Observe %q is not bound to its operation", operationID)
				}
			}
		default:
			return fmt.Errorf(
				"pending operation %q has unknown report kind %q",
				operationID,
				report.Kind,
			)
		}
	}
	return nil
}

func validatePersistedCommitReport(
	state persistedGameOutcomeState,
	operationID string,
	marker persistedAppliedOutcome,
	report persistedPendingReport,
) error {
	if err := protocol.ValidateCommit(report.Commit); err != nil {
		return fmt.Errorf("pending Commit %q is invalid: %w", operationID, err)
	}
	if err := protocol.ValidateObserve(report.Fallback); err != nil {
		return fmt.Errorf("pending Commit fallback %q is invalid: %w", operationID, err)
	}
	canonicalCommit := protocol.CommitRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       state.Create.SessionID,
		RequestID:       "commit." + operationID,
		ProposalID:      marker.ProposalID,
		EventID:         "outcome." + operationID,
		Tick:            marker.OccurrenceTick,
		Accepted:        marker.Accepted,
		Outcome:         marker.Outcome,
		Tags:            []string{"conversation"},
	}
	expected := newCommitReport(
		operationID,
		"npc.mira",
		canonicalCommit,
	).withOccurrenceTick(marker.OccurrenceTick)
	if report.Kind == "observe" {
		expected.kind = "observe"
		expected.observe = expected.fallback
	}
	if !reflect.DeepEqual(report, persistPendingReport(expected)) {
		return fmt.Errorf("pending Commit %q is not bound to its marker and fallback", operationID)
	}
	return nil
}

func (s *gameOutcomeStore) restore(state persistedGameOutcomeState) error {
	if err := validatePersistedGameOutcomeState(state); err != nil {
		return err
	}
	s.runID = state.RunID
	s.create = state.Create
	s.operationSequence = state.OperationSequence
	s.lastAuthoritativeTick = state.LastAuthoritativeTick
	if state.ProposalAttempt != nil {
		attempt := state.ProposalAttempt
		s.proposalAttempt = &proposalAttempt{
			OperationID: attempt.OperationID,
			Sequence:    attempt.Sequence,
			Request:     attempt.Request,
			Fallback:    attempt.Fallback,
			Submitted:   attempt.Submitted,
		}
	}
	for operationID, outcome := range state.Applied {
		s.applied[operationID] = appliedMarker{
			outcome: appliedOutcome{
				accepted: outcome.Accepted,
				outcome:  outcome.Outcome,
			},
			proposalID:     outcome.ProposalID,
			occurrenceTick: outcome.OccurrenceTick,
		}
	}
	for operationID, report := range state.Pending {
		s.pending[operationID] = pendingReport{
			kind:     report.Kind,
			commit:   report.Commit,
			observe:  report.Observe,
			fallback: report.Fallback,
		}
	}
	return nil
}

func loadGameOutcomeState(path string) (persistedGameOutcomeState, bool, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return persistedGameOutcomeState{}, false, nil
	}
	if err != nil {
		return persistedGameOutcomeState{}, false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return persistedGameOutcomeState{}, false, err
	}
	if info.Size() > 2<<20 {
		return persistedGameOutcomeState{}, false, fmt.Errorf(
			"state file is %d bytes; limit is %d",
			info.Size(),
			2<<20,
		)
	}
	decoder := json.NewDecoder(io.LimitReader(file, (2<<20)+1))
	decoder.DisallowUnknownFields()
	var state persistedGameOutcomeState
	if err := decoder.Decode(&state); err != nil {
		return persistedGameOutcomeState{}, false, err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return persistedGameOutcomeState{}, false, errors.New(
				"state file contains multiple JSON values",
			)
		}
		return persistedGameOutcomeState{}, false, err
	}
	return state, true, nil
}

func persistGameOutcomeState(path string, state persistedGameOutcomeState) error {
	return persistGameOutcomeStateWithDirectorySync(path, state, syncStateDirectory)
}

func persistGameOutcomeStateWithDirectorySync(
	path string,
	state persistedGameOutcomeState,
	syncDirectory func(string) error,
) error {
	if syncDirectory == nil {
		return errors.New("state directory sync function is nil")
	}
	payload, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	statePath, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return err
	}
	directory := filepath.Dir(statePath)
	syncPlan, err := loadOrCreateStateDirectorySyncPlan(
		statePath,
		directory,
		syncDirectory,
	)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if syncPlan != nil {
		// The durable journal survives failed cleanup and process reconstruction.
		// It is cleared only after every original parent has acknowledged its
		// newly created child entry.
		for _, parent := range syncPlan.parents {
			if err := syncDirectory(parent); err != nil {
				syncErr := fmt.Errorf(
					"sync state directory parent %q after MkdirAll: %w",
					parent,
					err,
				)
				if cleanupErr := removeCreatedStateDirectories(
					syncPlan.createdDirectories,
				); cleanupErr != nil {
					return errors.Join(
						syncErr,
						fmt.Errorf(
							"clean up newly created state directories after parent sync failure: %w",
							cleanupErr,
						),
					)
				}
				return syncErr
			}
		}
		if err := clearStateDirectorySyncJournal(syncPlan, syncDirectory); err != nil {
			return err
		}
	}
	file, err := os.CreateTemp(directory, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tempPath := file.Name()
	defer file.Close()
	defer os.Remove(tempPath)
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	if _, err := file.Write(payload); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, statePath); err != nil {
		return err
	}
	if err := syncDirectory(directory); err != nil {
		return &stateFileReplacedError{err: err}
	}
	return nil
}

const stateDirectorySyncJournalVersion = 1
const maxStateDirectorySyncJournalBytes = 64 << 10
const maxStateDirectorySyncJournalParents = 256

type persistedStateDirectorySyncJournal struct {
	Version   int      `json:"version"`
	StatePath string   `json:"state_path"`
	Parents   []string `json:"parents"`
}

type stateDirectorySyncPlan struct {
	journalPath        string
	journalDirectory   string
	parents            []string
	createdDirectories []string
}

func loadOrCreateStateDirectorySyncPlan(
	statePath string,
	directory string,
	syncDirectory func(string) error,
) (*stateDirectorySyncPlan, error) {
	journalPath, exists, err := findStateDirectorySyncJournal(statePath, directory)
	if err != nil {
		return nil, err
	}
	if exists {
		plan, err := loadStateDirectorySyncJournal(
			statePath,
			directory,
			journalPath,
		)
		if err != nil {
			return nil, err
		}
		// A prior call may have failed while syncing the journal's own directory.
		// Re-confirm the on-disk todo before it is allowed to authorize MkdirAll.
		if err := syncDirectory(plan.journalDirectory); err != nil {
			return nil, fmt.Errorf(
				"confirm recovered state directory sync journal in %q: %w",
				plan.journalDirectory,
				err,
			)
		}
		return plan, nil
	}
	parents, createdDirectories, err := stateDirectoryCreationPlan(directory)
	if err != nil {
		return nil, err
	}
	if len(parents) == 0 {
		return nil, nil
	}
	journalDirectory := parents[len(parents)-1]
	journalPath = filepath.Join(
		journalDirectory,
		stateDirectorySyncJournalName(statePath),
	)
	journal := persistedStateDirectorySyncJournal{
		Version:   stateDirectorySyncJournalVersion,
		StatePath: statePath,
		Parents:   parents,
	}
	if err := createStateDirectorySyncJournal(
		journalPath,
		journalDirectory,
		journal,
		syncDirectory,
	); err != nil {
		return nil, err
	}
	return &stateDirectorySyncPlan{
		journalPath:        journalPath,
		journalDirectory:   journalDirectory,
		parents:            parents,
		createdDirectories: createdDirectories,
	}, nil
}

// stateDirectoryCreationPlan describes exactly the directory entries MkdirAll
// must add. Both slices are ordered deepest to shallowest.
func stateDirectoryCreationPlan(directory string) ([]string, []string, error) {
	current := filepath.Clean(directory)
	var parents []string
	var directories []string
	for {
		info, err := os.Stat(current)
		if err == nil {
			if !info.IsDir() {
				return nil, nil, fmt.Errorf(
					"state directory path %q is not a directory",
					current,
				)
			}
			return parents, directories, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, nil, fmt.Errorf("inspect state directory %q: %w", current, err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil, nil, fmt.Errorf(
				"state directory %q has no existing ancestor",
				directory,
			)
		}
		directories = append(directories, current)
		parents = append(parents, parent)
		current = parent
	}
}

func stateDirectorySyncJournalName(statePath string) string {
	sum := sha256.Sum256([]byte(statePath))
	return fmt.Sprintf(".rin-basic-dir-sync-%x.json", sum)
}

func findStateDirectorySyncJournal(
	statePath string,
	directory string,
) (string, bool, error) {
	name := stateDirectorySyncJournalName(statePath)
	current := filepath.Clean(directory)
	found := ""
	for depth := 0; ; depth++ {
		if depth > maxStateDirectorySyncJournalParents {
			return "", false, errors.New("state directory journal search exceeds depth limit")
		}
		candidate := filepath.Join(current, name)
		info, err := os.Lstat(candidate)
		switch {
		case err == nil:
			if !info.Mode().IsRegular() {
				return "", false, fmt.Errorf(
					"state directory sync journal %q is not a regular file",
					candidate,
				)
			}
			if found != "" {
				return "", false, errors.New(
					"multiple state directory sync journals match one state path",
				)
			}
			found = candidate
		case !errors.Is(err, os.ErrNotExist):
			return "", false, fmt.Errorf(
				"inspect state directory sync journal %q: %w",
				candidate,
				err,
			)
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return found, found != "", nil
}

func createStateDirectorySyncJournal(
	journalPath string,
	journalDirectory string,
	journal persistedStateDirectorySyncJournal,
	syncDirectory func(string) error,
) error {
	payload, err := json.Marshal(journal)
	if err != nil {
		return fmt.Errorf("encode state directory sync journal: %w", err)
	}
	payload = append(payload, '\n')
	if len(payload) > maxStateDirectorySyncJournalBytes {
		return fmt.Errorf(
			"state directory sync journal is %d bytes; limit is %d",
			len(payload),
			maxStateDirectorySyncJournalBytes,
		)
	}
	file, err := os.OpenFile(
		journalPath,
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		0o600,
	)
	if err != nil {
		return fmt.Errorf("create state directory sync journal: %w", err)
	}
	if _, err := file.Write(payload); err != nil {
		_ = file.Close()
		return fmt.Errorf("write state directory sync journal: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync state directory sync journal: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close state directory sync journal: %w", err)
	}
	if err := syncDirectory(journalDirectory); err != nil {
		return fmt.Errorf(
			"confirm state directory sync journal in %q: %w",
			journalDirectory,
			err,
		)
	}
	return nil
}

func loadStateDirectorySyncJournal(
	statePath string,
	directory string,
	journalPath string,
) (*stateDirectorySyncPlan, error) {
	file, err := os.Open(journalPath)
	if err != nil {
		return nil, fmt.Errorf("open state directory sync journal: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat state directory sync journal: %w", err)
	}
	if !info.Mode().IsRegular() ||
		info.Size() <= 0 ||
		info.Size() > maxStateDirectorySyncJournalBytes {
		return nil, fmt.Errorf(
			"state directory sync journal has invalid type or size %d",
			info.Size(),
		)
	}
	decoder := json.NewDecoder(io.LimitReader(
		file,
		maxStateDirectorySyncJournalBytes+1,
	))
	decoder.DisallowUnknownFields()
	var journal persistedStateDirectorySyncJournal
	if err := decoder.Decode(&journal); err != nil {
		return nil, fmt.Errorf("decode state directory sync journal: %w", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New(
				"state directory sync journal contains multiple JSON values",
			)
		}
		return nil, fmt.Errorf("decode state directory sync journal trailer: %w", err)
	}
	journalDirectory := filepath.Dir(journalPath)
	if err := validateStateDirectorySyncJournal(
		statePath,
		directory,
		journalDirectory,
		journal,
	); err != nil {
		return nil, err
	}
	createdDirectories := make([]string, 0, len(journal.Parents))
	createdDirectories = append(createdDirectories, directory)
	createdDirectories = append(
		createdDirectories,
		journal.Parents[:len(journal.Parents)-1]...,
	)
	return &stateDirectorySyncPlan{
		journalPath:        journalPath,
		journalDirectory:   journalDirectory,
		parents:            append([]string(nil), journal.Parents...),
		createdDirectories: createdDirectories,
	}, nil
}

func validateStateDirectorySyncJournal(
	statePath string,
	directory string,
	journalDirectory string,
	journal persistedStateDirectorySyncJournal,
) error {
	if journal.Version != stateDirectorySyncJournalVersion ||
		journal.StatePath != statePath {
		return errors.New(
			"state directory sync journal has an invalid version or state-path binding",
		)
	}
	if len(journal.Parents) == 0 ||
		len(journal.Parents) > maxStateDirectorySyncJournalParents {
		return errors.New("state directory sync journal parent count is out of bounds")
	}
	expected := filepath.Dir(directory)
	for index, parent := range journal.Parents {
		if parent == "" ||
			!filepath.IsAbs(parent) ||
			filepath.Clean(parent) != parent ||
			parent != expected {
			return fmt.Errorf(
				"state directory sync journal parent %d is not the expected ancestor",
				index,
			)
		}
		expected = filepath.Dir(parent)
		if expected == parent && index != len(journal.Parents)-1 {
			return errors.New("state directory sync journal extends beyond the filesystem root")
		}
	}
	if journal.Parents[len(journal.Parents)-1] != journalDirectory {
		return errors.New(
			"state directory sync journal is not stored at its bound existing ancestor",
		)
	}
	return nil
}

func clearStateDirectorySyncJournal(
	plan *stateDirectorySyncPlan,
	syncDirectory func(string) error,
) error {
	if err := os.Remove(plan.journalPath); err != nil {
		return fmt.Errorf("remove completed state directory sync journal: %w", err)
	}
	if err := syncDirectory(plan.journalDirectory); err != nil {
		return fmt.Errorf(
			"confirm completed state directory sync journal removal in %q: %w",
			plan.journalDirectory,
			err,
		)
	}
	return nil
}

func removeCreatedStateDirectories(directories []string) error {
	var cleanupErrors []error
	for _, directory := range directories {
		if err := os.Remove(directory); err != nil &&
			!errors.Is(err, os.ErrNotExist) {
			cleanupErrors = append(
				cleanupErrors,
				fmt.Errorf("remove newly created state directory %q: %w", directory, err),
			)
		}
	}
	return errors.Join(cleanupErrors...)
}

type stateFileReplacedError struct {
	err error
}

func (e *stateFileReplacedError) Error() string {
	return "state file was replaced but parent-directory sync failed: " + e.err.Error()
}

func (e *stateFileReplacedError) Unwrap() error {
	return e.err
}

func stateFileWasReplaced(err error) bool {
	var replaced *stateFileReplacedError
	return errors.As(err, &replaced)
}

func syncStateDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	return nil
}

func (s *gameOutcomeStore) applyAndEnqueue(
	operationID string,
	outcome appliedOutcome,
	report pendingReport,
	applyGameState func(*gameTransaction) error,
) (appliedOutcome, error) {
	return s.applyAndEnqueueAttempt(
		operationID,
		outcome,
		report,
		nil,
		0,
		applyGameState,
	)
}

func (s *gameOutcomeStore) applyAndEnqueueAttempt(
	operationID string,
	outcome appliedOutcome,
	report pendingReport,
	completedAttempt *proposalAttempt,
	proposalTick int64,
	applyGameState func(*gameTransaction) error,
) (appliedOutcome, error) {
	if err := s.checkDurability(); err != nil {
		return appliedOutcome{}, err
	}
	if marker, ok := s.applied[operationID]; ok {
		return marker.outcome, nil
	}
	if completedAttempt != nil && s.proposalAttempt != completedAttempt {
		return appliedOutcome{}, errors.New("Proposal Attempt changed before authoritative completion")
	}
	err := s.authoritativeTransaction(func(tx *gameTransaction) error {
		occurrenceTick := s.currentTick()
		if occurrenceTick < s.lastAuthoritativeTick {
			occurrenceTick = s.lastAuthoritativeTick
		}
		if completedAttempt != nil && occurrenceTick < completedAttempt.Request.Tick {
			occurrenceTick = completedAttempt.Request.Tick
		}
		if occurrenceTick < proposalTick {
			occurrenceTick = proposalTick
		}
		if occurrenceTick < 0 {
			return errors.New("authoritative occurrence tick is negative")
		}
		report = report.withOccurrenceTick(occurrenceTick)
		marker := appliedMarker{
			outcome:        outcome,
			occurrenceTick: occurrenceTick,
		}
		switch report.kind {
		case "commit":
			marker.proposalID = report.commit.ProposalID
		case "observe":
			if completedAttempt != nil {
				marker.proposalID = completedAttempt.Fallback.ID
			}
		}
		// PRODUCTION PERSISTENCE HOOK: the game effect, applied marker, complete
		// report (including its safe fallback), Proposal Attempt deletion, and
		// authoritative tick high-water share one durable game transaction.
		previousTick := s.lastAuthoritativeTick
		previousAttempt := s.proposalAttempt
		tx.onRollback(func() {
			delete(s.applied, operationID)
			delete(s.pending, operationID)
			s.lastAuthoritativeTick = previousTick
			s.proposalAttempt = previousAttempt
		})
		s.lastAuthoritativeTick = occurrenceTick
		s.applied[operationID] = marker
		s.pending[operationID] = report
		if completedAttempt != nil {
			s.proposalAttempt = nil
		}
		return applyGameState(tx)
	})
	if err != nil {
		return appliedOutcome{}, err
	}
	return outcome, nil
}

func (s *gameOutcomeStore) retainProposalAttempt() (*proposalAttempt, error) {
	if err := s.checkDurability(); err != nil {
		return nil, err
	}
	if s.proposalAttempt != nil {
		return s.proposalAttempt, nil
	}
	const maxInt64 = int64(^uint64(0) >> 1)
	if s.operationSequence >= uint64(maxInt64) ||
		s.lastAuthoritativeTick == maxInt64 {
		return nil, errors.New("authoritative operation clock overflow")
	}
	nextSequence := s.operationSequence + 1
	tick := int64(nextSequence)
	if tick <= s.lastAuthoritativeTick {
		tick = s.lastAuthoritativeTick + 1
	}
	if current := s.currentTick(); current > tick {
		tick = current
	}
	if tick < 0 {
		return nil, errors.New("authoritative operation clock is negative")
	}
	request := exampleProposeRequest(
		s.runID,
		s.create.SessionID,
		nextSequence,
		tick,
	)
	fallback, err := authoredFallback(request, "wait")
	if err != nil {
		return nil, err
	}
	attempt := &proposalAttempt{
		OperationID: fmt.Sprintf("turn.%s.%d", s.runID, nextSequence),
		Sequence:    nextSequence,
		Request:     request,
		Fallback:    fallback,
	}
	err = s.authoritativeTransaction(func(tx *gameTransaction) error {
		oldSequence := s.operationSequence
		oldTick := s.lastAuthoritativeTick
		tx.onRollback(func() {
			s.operationSequence = oldSequence
			s.lastAuthoritativeTick = oldTick
			s.proposalAttempt = nil
		})
		s.operationSequence = nextSequence
		s.lastAuthoritativeTick = tick
		s.proposalAttempt = attempt
		return nil
	})
	if err != nil {
		return nil, err
	}
	return attempt, nil
}

func exampleProposeRequest(
	runID string,
	sessionID string,
	sequence uint64,
	tick int64,
) protocol.ProposeRequest {
	return protocol.ProposeRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
		RequestID:       fmt.Sprintf("propose.%s.%d", runID, sequence),
		ActorID:         "npc.mira",
		Tick:            tick,
		Intent:          "Choose how to respond to the player.",
		Tags:            []string{"conversation"},
		CandidateActions: []protocol.ActionSpec{
			{ID: "talk", Kind: "dialogue", Description: "ask one honest question"},
			{ID: "refuse", Kind: "refuse", Description: "protect a private boundary"},
			{ID: "wait", Kind: "wait", Description: "stay silent for now"},
		},
	}
}

func (s *gameOutcomeStore) markProposalSubmitted(attempt *proposalAttempt) error {
	if err := s.checkDurability(); err != nil {
		return err
	}
	if s.proposalAttempt != attempt {
		return errors.New("Proposal Attempt changed before submit")
	}
	if attempt.Submitted {
		return nil
	}
	return s.authoritativeTransaction(func(tx *gameTransaction) error {
		tx.onRollback(func() {
			attempt.Submitted = false
		})
		attempt.Submitted = true
		return nil
	})
}

func (s *gameOutcomeStore) flush(c *client) error {
	if err := s.checkDurability(); err != nil {
		return err
	}
	operationIDs := make([]string, 0, len(s.pending))
	for operationID := range s.pending {
		operationIDs = append(operationIDs, operationID)
	}
	sort.Strings(operationIDs)
	for _, operationID := range operationIDs {
		report := s.pending[operationID]
		reported := false
		if report.kind == "commit" {
			err := c.post("/v1/action/commit", report.commit, &protocol.MutationResult{})
			if err != nil {
				if !isIrrecoverableCommitError(err) {
					// A timeout or temporary server error might mean the Commit
					// succeeded. Retain and retry its exact request ID.
					return err
				}
				converted := report
				converted.kind = "observe"
				converted.observe = report.fallback
				if persistErr := s.persistReportConversion(operationID, converted); persistErr != nil {
					if stateFileWasReplaced(persistErr) {
						s.pending[operationID] = converted
						s.blockDurability(persistErr)
					}
					return fmt.Errorf("persist Commit-to-Observe conversion: %w", persistErr)
				}
				s.pending[operationID] = converted
				report = converted
			} else {
				reported = true
			}
		}
		if !reported && report.kind == "observe" {
			if err := c.post("/v1/session/observe", report.observe, &protocol.MutationResult{}); err != nil {
				return err
			}
			reported = true
		}
		if !reported {
			return fmt.Errorf("unknown pending report kind %q", report.kind)
		}
		if err := s.persistReportAck(operationID); err != nil {
			if stateFileWasReplaced(err) {
				delete(s.pending, operationID)
				s.blockDurability(err)
			}
			return fmt.Errorf("persist report acknowledgement: %w", err)
		}
		delete(s.pending, operationID)
	}
	return nil
}

func planGameAction(action protocol.ActionSpec) appliedOutcome {
	switch action.ID {
	case "talk":
		return appliedOutcome{accepted: true, outcome: "Mira asked what the player wanted remembered."}
	case "refuse":
		return appliedOutcome{accepted: true, outcome: "Mira protected the private boundary."}
	case "wait":
		return appliedOutcome{accepted: true, outcome: "Mira stayed silent for now."}
	default:
		return appliedOutcome{outcome: "The game rejected an action outside its local allowlist."}
	}
}

func applyGameEffect(tx *gameTransaction, action protocol.ActionSpec) {
	// Replace with the actual game-state mutation enlisted in
	// authoritativeTransaction. Register its inverse before mutating so errors
	// and panics roll the effect back with the marker and Outbox.
	tx.onRollback(func() {
		fmt.Printf("roll back game-owned action: %s\n", action.ID)
	})
	fmt.Printf("apply game-owned action: %s\n", action.ID)
}

func runInMemoryGameTransaction(mutate func(*gameTransaction) error) (err error) {
	tx := &gameTransaction{}
	defer func() {
		if recovered := recover(); recovered != nil {
			tx.rollback()
			err = fmt.Errorf("authoritative game transaction panicked: %v", recovered)
		} else if err != nil {
			tx.rollback()
		}
	}()
	err = mutate(tx)
	return err
}

func runPersistedGameTransaction(
	mutate func(*gameTransaction) error,
	persist func() error,
) error {
	var committedErr error
	err := runInMemoryGameTransaction(func(tx *gameTransaction) error {
		if err := mutate(tx); err != nil {
			return err
		}
		if err := persist(); err != nil {
			if stateFileWasReplaced(err) {
				// Rename made the new state visible. Keep memory aligned with
				// disk, but surface that directory durability was not confirmed.
				committedErr = err
				return nil
			}
			return fmt.Errorf("persist authoritative game transaction: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return committedErr
}

func (s *gameOutcomeStore) runPersistedMutation(
	mutate func(*gameTransaction) error,
	persist func() error,
) error {
	if err := s.checkDurability(); err != nil {
		return err
	}
	err := runPersistedGameTransaction(mutate, persist)
	if stateFileWasReplaced(err) {
		s.blockDurability(err)
	}
	return err
}

func (s *gameOutcomeStore) blockDurability(err error) {
	if s.durabilityBlocked == nil {
		s.durabilityBlocked = err
	}
}

func (s *gameOutcomeStore) checkDurability() error {
	if s.durabilityBlocked == nil {
		return nil
	}
	return fmt.Errorf(
		"durability_unconfirmed: restart and restore the replaced state before continuing: %w",
		s.durabilityBlocked,
	)
}

func (r pendingReport) withOccurrenceTick(tick int64) pendingReport {
	if r.kind == "commit" {
		r.commit.Tick = tick
		r.fallback.Tick = tick
	} else {
		r.observe.Tick = tick
	}
	return r
}

func newCommitReport(
	operationID string,
	observerID string,
	commit protocol.CommitRequest,
) pendingReport {
	return pendingReport{
		kind:   "commit",
		commit: commit,
		// This degradation payload is persisted with the Commit at the same
		// occurrence. It records only episodic memory: no inferred goals,
		// recent actions, scheduler changes, or relative facts.
		fallback: protocol.ObserveRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       commit.SessionID,
			RequestID:       "reconcile." + operationID,
			EventID:         commit.EventID,
			ObserverIDs:     []string{observerID},
			Source:          "basic-example",
			Kind:            "action_outcome",
			Summary:         "Authoritative outcome: " + commit.Outcome,
			Tags:            append([]string{"outcome-report"}, commit.Tags...),
			Importance:      3,
		},
	}
}

func (s *gameOutcomeStore) runExampleInvocation(c *client) error {
	if err := s.checkDurability(); err != nil {
		return err
	}
	hadPendingReports := len(s.pending) != 0

	// Create is itself idempotent and is always retried from the exact persisted
	// payload before any prior Outbox entry is sent.
	createErr := retrySameRequest(2, func() error {
		return c.post("/v1/session/create", s.create, &protocol.MutationResult{})
	})
	if createErr != nil {
		if hadPendingReports {
			return fmt.Errorf("stable Create unavailable; retained Outbox was not drained: %w", createErr)
		}
		if s.proposalAttempt != nil && s.proposalAttempt.Submitted {
			return fmt.Errorf(
				"proposal_outcome_unknown: stable Create unavailable while an exact Proposal Attempt is retained: %w",
				createErr,
			)
		}
		if !createFailureAllowsOfflineFallback(createErr) {
			return fmt.Errorf(
				"stable Create failed closed; no fallback report was generated: %w",
				createErr,
			)
		}
		attempt, err := s.retainProposalAttempt()
		if err != nil {
			return err
		}
		return s.completeColdFallback(attempt)
	}

	if err := s.flush(c); err != nil {
		return fmt.Errorf("drain restored authoritative Outbox: %w", err)
	}
	if hadPendingReports && s.proposalAttempt == nil {
		// This invocation performed recovery work; it deliberately does not
		// start an unrelated new turn in the same process.
		return nil
	}

	attempt := s.proposalAttempt
	if attempt == nil {
		var err error
		attempt, err = s.retainProposalAttempt()
		if err != nil {
			return err
		}
	}
	if err := s.resolveProposalAttempt(c, attempt); err != nil {
		return err
	}
	if err := s.flush(c); err != nil {
		return fmt.Errorf("authoritative report remains queued: %w", err)
	}
	return nil
}

func createFailureAllowsOfflineFallback(err error) bool {
	var apiErr *apiError
	if !errors.As(err, &apiErr) {
		return true
	}
	return (apiErr.Code == "invalid_response" &&
		apiErr.Status >= http.StatusOK &&
		apiErr.Status < http.StatusMultipleChoices) ||
		apiErr.Status == http.StatusRequestTimeout ||
		apiErr.Status == http.StatusTooManyRequests ||
		apiErr.Status >= http.StatusInternalServerError
}

func (s *gameOutcomeStore) completeColdFallback(attempt *proposalAttempt) error {
	fallback := attempt.Fallback
	planned := planGameAction(fallback.Action)
	report := pendingReport{
		kind: "observe",
		observe: protocol.ObserveRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       attempt.Request.SessionID,
			RequestID:       "reconcile." + attempt.OperationID,
			EventID:         "fallback." + attempt.OperationID,
			ObserverIDs:     []string{attempt.Request.ActorID},
			Source:          "basic-example",
			Kind:            "fallback_action",
			Summary:         "Local fallback " + fallback.Action.ID + ": " + planned.outcome,
			Tags:            []string{"fallback"},
			Importance:      3,
		},
	}
	_, err := s.applyAndEnqueueAttempt(
		attempt.OperationID,
		planned,
		report,
		attempt,
		fallback.Tick,
		func(tx *gameTransaction) error {
			if planned.accepted {
				s.applyEffect(tx, fallback.Action)
			}
			return nil
		},
	)
	return err
}

func (s *gameOutcomeStore) resolveProposalAttempt(
	c *client,
	attempt *proposalAttempt,
) error {
	if err := s.markProposalSubmitted(attempt); err != nil {
		return err
	}
	var proposed protocol.ProposalResult
	if err := c.post("/v1/agent/propose", attempt.Request, &proposed); err != nil {
		switch {
		case isAmbiguousProposalError(err):
			// The retained Attempt is intentionally not deleted. A restart must
			// POST this exact request again; it must never choose a local fallback.
			return fmt.Errorf(
				"proposal_outcome_unknown: exact Proposal Attempt remains retained: %w",
				err,
			)
		case isStateChangedProposalError(err):
			// state_changed proves that this request produced no Proposal, but
			// the contract requires a new request_id. Retire this Attempt without
			// applying an effect; the next invocation allocates a new sequence.
			return s.retireProposalAttempt(attempt)
		case isConfirmedNoProposalError(err):
			// Validation and policy terminal errors prove that no Proposal was
			// created, so the pre-persisted authored fallback is safe.
			return s.completeColdFallback(attempt)
		default:
			// Identity/session/conflict errors are neither authority to execute
			// fallback nor evidence that replaying elsewhere is safe.
			return fmt.Errorf(
				"proposal_failed_closed: exact Proposal Attempt remains retained: %w",
				err,
			)
		}
	}
	if err := validateProposalIdentity(attempt.Request, proposed.Proposal); err != nil {
		return err
	}

	planned := planGameAction(proposed.Proposal.Action)
	var state protocol.SessionState
	if err := c.post(
		"/v1/session/get",
		protocol.SessionRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       attempt.Request.SessionID,
		},
		&state,
	); err != nil {
		// The exact Propose request remains durable. Replaying it is idempotent;
		// consuming the Attempt here would instead turn an unverified response
		// into game authority.
		return fmt.Errorf(
			"proposal_freshness_unknown: exact Proposal Attempt remains retained: %w",
			err,
		)
	}
	if !proposalIsFresh(state, proposed.Proposal) {
		planned = appliedOutcome{
			accepted: false,
			outcome:  "The game rejected a stale or inconsistent proposal before applying any effect.",
		}
	}

	commit := protocol.CommitRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       attempt.Request.SessionID,
		RequestID:       "commit." + attempt.OperationID,
		ProposalID:      proposed.Proposal.ID,
		EventID:         "outcome." + attempt.OperationID,
		Accepted:        planned.accepted,
		Outcome:         planned.outcome,
		Tags:            []string{"conversation"},
	}
	_, err := s.applyAndEnqueueAttempt(
		attempt.OperationID,
		planned,
		newCommitReport(attempt.OperationID, attempt.Request.ActorID, commit),
		attempt,
		proposed.Proposal.Tick,
		func(tx *gameTransaction) error {
			if planned.accepted {
				s.applyEffect(tx, proposed.Proposal.Action)
			}
			return nil
		},
	)
	return err
}

func (s *gameOutcomeStore) retireProposalAttempt(attempt *proposalAttempt) error {
	if err := s.checkDurability(); err != nil {
		return err
	}
	if s.proposalAttempt != attempt {
		return errors.New("Proposal Attempt changed before retirement")
	}
	return s.authoritativeTransaction(func(tx *gameTransaction) error {
		tx.onRollback(func() {
			s.proposalAttempt = attempt
		})
		s.proposalAttempt = nil
		return nil
	})
}

func isAmbiguousProposalError(err error) bool {
	var apiErr *apiError
	if !errors.As(err, &apiErr) {
		return true
	}
	return apiErr.Code == "proposal_outcome_unknown" ||
		(apiErr.Code == "invalid_response" &&
			apiErr.Status >= http.StatusOK &&
			apiErr.Status < http.StatusMultipleChoices) ||
		apiErr.Status == http.StatusRequestTimeout ||
		apiErr.Status >= http.StatusInternalServerError
}

func isStateChangedProposalError(err error) bool {
	var apiErr *apiError
	return errors.As(err, &apiErr) &&
		apiErr.Status == http.StatusConflict &&
		apiErr.Code == "state_changed"
}

func isConfirmedNoProposalError(err error) bool {
	var apiErr *apiError
	if !errors.As(err, &apiErr) {
		return false
	}
	return (apiErr.Status == http.StatusBadRequest &&
		apiErr.Code == "invalid_request") ||
		(apiErr.Status == http.StatusUnprocessableEntity &&
			apiErr.Code == "no_safe_action")
}

func validateProposalIdentity(
	request protocol.ProposeRequest,
	proposal protocol.ActionProposal,
) error {
	if proposal.ID == "" || proposal.Action.ID == "" {
		return errors.New("invalid_proposal_identity: proposal ID and action ID are required")
	}
	if proposal.SessionID != request.SessionID ||
		proposal.RequestID != request.RequestID ||
		proposal.ActorID != request.ActorID ||
		proposal.Tick != request.Tick {
		return errors.New("invalid_proposal_identity: response does not match retained Proposal Attempt")
	}
	for _, candidate := range request.CandidateActions {
		if reflect.DeepEqual(candidate, proposal.Action) {
			return nil
		}
	}
	return errors.New("invalid_proposal_identity: action is not an exact retained candidate")
}

func proposalIsFresh(state protocol.SessionState, proposal protocol.ActionProposal) bool {
	retained, ok := state.Proposals[proposal.ID]
	if !ok || retained.Status != "pending" {
		return false
	}
	if retained.SessionID != proposal.SessionID ||
		retained.RequestID != proposal.RequestID ||
		retained.ActorID != proposal.ActorID ||
		retained.Tick != proposal.Tick ||
		retained.BasedOnRevision != proposal.BasedOnRevision ||
		retained.BasedOnHeadHash != proposal.BasedOnHeadHash ||
		retained.BasedOnWorldRevision != proposal.BasedOnWorldRevision ||
		retained.CreatedRevision != proposal.CreatedRevision ||
		!reflect.DeepEqual(retained.Action, proposal.Action) {
		return false
	}
	if retained.BasedOnWorldRevision > 0 {
		return state.WorldRevision == retained.BasedOnWorldRevision
	}
	return state.Revision == retained.CreatedRevision
}

func authoredFallback(request protocol.ProposeRequest, fallbackID string) (protocol.ActionProposal, error) {
	for _, candidate := range request.CandidateActions {
		if candidate.ID == fallbackID {
			return protocol.ActionProposal{
				ID:           "offline." + request.RequestID + "." + candidate.ID,
				SessionID:    request.SessionID,
				RequestID:    request.RequestID,
				ActorID:      request.ActorID,
				Tick:         request.Tick,
				Action:       candidate,
				Stance:       candidate.Kind,
				Summary:      "The game used its authored offline fallback.",
				Rationale:    "The Rin Sidecar was unavailable; world state remains game-owned.",
				PolicySource: "adapter-offline",
				Status:       "offline",
			}, nil
		}
	}
	return protocol.ActionProposal{}, fmt.Errorf("invalid_fallback: %q is not a candidate action", fallbackID)
}

func retrySameRequest(attempts int, request func() error) error {
	var err error
	for attempt := 0; attempt < attempts; attempt++ {
		if err = request(); err == nil {
			return nil
		}
	}
	return err
}

func isIrrecoverableCommitError(err error) bool {
	var apiErr *apiError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.Code {
	case "session_not_found", "unknown_proposal", "proposal_resolved",
		"proposal_canceled", "proposal_stale":
		return true
	default:
		return false
	}
}

type apiError struct {
	Status  int
	Code    string
	Message string
}

func (e *apiError) Error() string {
	return e.Code + ": " + e.Message
}

func (c client) post(path string, input, output any) error {
	payload, err := json.Marshal(input)
	if err != nil {
		return err
	}
	request, err := http.NewRequest(http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, defaultMaxRinResponseBytes))
	if err != nil {
		return &apiError{
			Status:  response.StatusCode,
			Code:    "invalid_response",
			Message: "could not read Rin response",
		}
	}
	var result envelope
	if err := json.Unmarshal(body, &result); err != nil {
		return &apiError{
			Status:  response.StatusCode,
			Code:    "invalid_response",
			Message: "could not decode Rin response",
		}
	}
	if !result.OK {
		if result.Error == nil {
			return &apiError{
				Status:  response.StatusCode,
				Code:    "invalid_response",
				Message: "Rin returned an unspecified error",
			}
		}
		return &apiError{
			Status:  response.StatusCode,
			Code:    result.Error.Code,
			Message: result.Error.Message,
		}
	}
	if response.StatusCode < http.StatusOK ||
		response.StatusCode >= http.StatusMultipleChoices {
		return &apiError{
			Status:  response.StatusCode,
			Code:    "unexpected_http_status",
			Message: "Rin returned success data with a non-success HTTP status",
		}
	}
	if err := json.Unmarshal(result.Data, output); err != nil {
		return &apiError{
			Status:  response.StatusCode,
			Code:    "invalid_response",
			Message: "could not decode Rin success data",
		}
	}
	if err := validateSuccessResponseIdentity(input, output); err != nil {
		return &apiError{
			Status:  response.StatusCode,
			Code:    "invalid_response",
			Message: err.Error(),
		}
	}
	return nil
}

func validateSuccessResponseIdentity(input, output any) error {
	switch request := input.(type) {
	case protocol.CommitRequest:
		result, ok := output.(*protocol.MutationResult)
		if !ok || result == nil || result.SessionID == "" {
			return errors.New("Commit success data is missing a MutationResult")
		}
		if result.SessionID != request.SessionID {
			return errors.New("Commit MutationResult session_id does not match the request")
		}
	case protocol.ObserveRequest:
		result, ok := output.(*protocol.MutationResult)
		if !ok || result == nil || result.SessionID == "" {
			return errors.New("Observe success data is missing a MutationResult")
		}
		if result.SessionID != request.SessionID {
			return errors.New("Observe MutationResult session_id does not match the request")
		}
	case protocol.SessionRequest:
		state, ok := output.(*protocol.SessionState)
		if !ok || state == nil || state.SessionID == "" {
			return errors.New("Session GET success data is missing a SessionState")
		}
		if state.SessionID != request.SessionID {
			return errors.New("SessionState session_id does not match the request")
		}
	}
	return nil
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
