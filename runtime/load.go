package runtime

import "github.com/sunrioa/rin/protocol"

const (
	replayPageSize       = 256
	checkpointMinimumRev = uint64(256)
)

func (e *Engine) ensureLoaded(session *managedSession) error {
	session.mu.Lock()
	if session.loaded {
		session.mu.Unlock()
		return nil
	}
	state, identifiers, lineageEpoch, checkpointRevision, err := e.loadCurrentSession(session.id)
	if err != nil {
		// A failed lazy load remains retryable. The Session descriptor stays
		// present, so callers can never mistake a damaged durable Session for an
		// unused identifier.
		session.mu.Unlock()
		return err
	}
	if state.SessionID == "" || state.SessionID != session.id {
		session.mu.Unlock()
		return NewError(
			"replay_failed",
			"session event log identifies a different session",
			ErrCorruptLog,
		)
	}
	session.state = state
	session.identifiers = identifiers
	session.lineageEpoch = lineageEpoch
	session.loaded = true
	if shouldRepairHeadCheckpoint(state.Revision, checkpointRevision) {
		// Successful recovery is the migration path for pre-checkpoint data and
		// the self-healing path after a cache is removed or rejected. A valid
		// checkpoint with a small tail is left alone: rewriting exact head after
		// every restart would reintroduce quadratic history serialization.
		e.queueCheckpointLocked(session)
	}
	session.mu.Unlock()
	return nil
}

func (e *Engine) loadCurrentSession(
	sessionID string,
) (protocol.SessionState, protocol.IdentifierHistory, uint64, uint64, error) {
	if ranged, ok := e.store.(RangeStore); ok {
		head, err := ranged.Head(sessionID)
		if err != nil {
			return protocol.SessionState{}, protocol.IdentifierHistory{}, 0, 0, NewError(
				"store_load_failed",
				"could not read session log head",
				err,
			)
		}
		if head.Revision == 0 || head.HeadHash == "" {
			return protocol.SessionState{}, protocol.IdentifierHistory{}, 0, 0, NewError(
				"replay_failed",
				"session event log has no valid head",
				ErrCorruptLog,
			)
		}
		state, identifiers, epoch, checkpointRevision, err := e.loadRangedThrough(
			sessionID,
			head.Revision,
			ranged,
		)
		if err != nil {
			return protocol.SessionState{}, protocol.IdentifierHistory{}, 0, 0, err
		}
		if state.Revision != head.Revision || state.HeadHash != head.HeadHash {
			return protocol.SessionState{}, protocol.IdentifierHistory{}, 0, 0, NewError(
				"replay_failed",
				"session replay does not match the durable log head",
				ErrCorruptLog,
			)
		}
		return state, identifiers, epoch, checkpointRevision, nil
	}
	state, identifiers, epoch, err := e.loadLegacyThrough(sessionID, 0)
	return state, identifiers, epoch, 0, err
}

// loadSessionThrough reconstructs an exact local event-log revision. A through
// value of zero means the complete legacy Store.Load result.
func (e *Engine) loadSessionThrough(
	sessionID string,
	through uint64,
) (protocol.SessionState, protocol.IdentifierHistory, uint64, error) {
	if ranged, ok := e.store.(RangeStore); ok {
		state, identifiers, epoch, _, err := e.loadRangedThrough(
			sessionID,
			through,
			ranged,
		)
		return state, identifiers, epoch, err
	}
	return e.loadLegacyThrough(sessionID, through)
}

func (e *Engine) loadLegacyThrough(
	sessionID string,
	through uint64,
) (protocol.SessionState, protocol.IdentifierHistory, uint64, error) {
	events, err := e.store.Load(sessionID)
	if err != nil {
		return protocol.SessionState{}, protocol.IdentifierHistory{}, 0, NewError(
			"store_load_failed",
			"could not load session log",
			err,
		)
	}
	if through > 0 {
		count := 0
		for count < len(events) && events[count].Sequence <= through {
			count++
		}
		if count == 0 || events[count-1].Sequence != through {
			return protocol.SessionState{}, protocol.IdentifierHistory{}, 0, NewFieldError(
				"revision_not_found",
				"requested revision does not exist",
				"revision",
				ErrNotFound,
			)
		}
		events = events[:count]
	}
	state, identifiers, err := replayEvents(events, 0)
	if err != nil {
		return protocol.SessionState{}, protocol.IdentifierHistory{}, 0, NewError(
			"replay_failed",
			"session event log is invalid",
			err,
		)
	}
	return state, identifiers, restoredEventCount(events), nil
}

func (e *Engine) loadRangedThrough(
	sessionID string,
	through uint64,
	ranged RangeStore,
) (protocol.SessionState, protocol.IdentifierHistory, uint64, uint64, error) {
	return e.loadRangedThroughMode(sessionID, through, ranged, true)
}

func (e *Engine) loadRangedThroughMode(
	sessionID string,
	through uint64,
	ranged RangeStore,
	useCheckpoint bool,
) (protocol.SessionState, protocol.IdentifierHistory, uint64, uint64, error) {
	if through == 0 {
		return protocol.SessionState{}, protocol.IdentifierHistory{}, 0, 0, NewFieldError(
			"revision_not_found",
			"requested revision does not exist",
			"revision",
			ErrNotFound,
		)
	}
	var (
		state              protocol.SessionState
		identifiers        = newIdentifierHistory(true)
		epoch              uint64
		usedCheckpoint     bool
		checkpointRevision uint64
	)
	if checkpoints, ok := e.store.(CheckpointStore); ok && useCheckpoint {
		checkpoint, found := e.loadUsableCheckpoint(
			sessionID,
			through,
			ranged,
			checkpoints,
		)
		if found {
			usedCheckpoint = true
			checkpointRevision = checkpoint.Revision
			var err error
			state, err = clone(checkpoint.Snapshot.State)
			if err != nil {
				return e.loadRangedThroughMode(sessionID, through, ranged, false)
			}
			canonicalizeStateProposalPresentation(&state)
			identifiers, err = cloneIdentifierHistory(*checkpoint.Snapshot.IdentifierHistory)
			if err != nil {
				return e.loadRangedThroughMode(sessionID, through, ranged, false)
			}
			normalizeWritableState(&state)
			epoch = checkpoint.LineageEpoch
		}
	}

	state, identifiers, epoch, err := replayRangedTail(
		sessionID,
		through,
		ranged,
		state,
		identifiers,
		epoch,
	)
	if err != nil && usedCheckpoint {
		// A checkpoint is never authoritative. Even a structurally valid cache
		// can be stale relative to its tail; retry from genesis before surfacing
		// an event-log failure.
		return e.loadRangedThroughMode(sessionID, through, ranged, false)
	}
	return state, identifiers, epoch, checkpointRevision, err
}

func replayRangedTail(
	sessionID string,
	through uint64,
	ranged RangeStore,
	state protocol.SessionState,
	identifiers protocol.IdentifierHistory,
	epoch uint64,
) (protocol.SessionState, protocol.IdentifierHistory, uint64, error) {
	for state.Revision < through {
		page, err := ranged.LoadRange(
			sessionID,
			state.Revision,
			through,
			replayPageSize,
		)
		if err != nil {
			return protocol.SessionState{}, protocol.IdentifierHistory{}, 0, NewError(
				"store_load_failed",
				"could not load session event range",
				err,
			)
		}
		if len(page.Events) == 0 || len(page.Events) > replayPageSize {
			return protocol.SessionState{}, protocol.IdentifierHistory{}, 0, NewError(
				"replay_failed",
				"session event range is not a bounded page",
				ErrCorruptLog,
			)
		}
		before := state.Revision
		for _, event := range page.Events {
			if event.Sequence > through {
				return protocol.SessionState{}, protocol.IdentifierHistory{}, 0, NewError(
					"replay_failed",
					"session event range exceeded its requested boundary",
					ErrCorruptLog,
				)
			}
			normalizeWritableState(&state)
			next, applyErr := applyEvent(state, event)
			if applyErr != nil {
				return protocol.SessionState{}, protocol.IdentifierHistory{}, 0, NewError(
					"replay_failed",
					"session event range is invalid",
					applyErr,
				)
			}
			delta, identityErr := prepareIdentifierEvent(identifiers, event)
			if identityErr != nil {
				return protocol.SessionState{}, protocol.IdentifierHistory{}, 0, NewError(
					"replay_failed",
					"session event identifiers are invalid",
					identityErr,
				)
			}
			applyIdentifierDelta(&identifiers, delta)
			state = next
			if event.Type == EventSessionRestored && epoch != ^uint64(0) {
				epoch++
			}
		}
		if state.Revision <= before {
			return protocol.SessionState{}, protocol.IdentifierHistory{}, 0, NewError(
				"replay_failed",
				"session event range made no progress",
				ErrCorruptLog,
			)
		}
		if state.Revision < through && !page.HasMore {
			return protocol.SessionState{}, protocol.IdentifierHistory{}, 0, NewError(
				"replay_failed",
				"session event range omitted a durable suffix",
				ErrCorruptLog,
			)
		}
		if state.Revision == through && page.HasMore {
			return protocol.SessionState{}, protocol.IdentifierHistory{}, 0, NewError(
				"replay_failed",
				"session event range reported data past its boundary",
				ErrCorruptLog,
			)
		}
	}
	return state, identifiers, epoch, nil
}

func (e *Engine) verifySessionFromGenesis(sessionID string) error {
	if ranged, ok := e.store.(RangeStore); ok {
		head, err := ranged.Head(sessionID)
		if err != nil {
			return NewError("store_load_failed", "could not read session log head", err)
		}
		state, _, _, _, err := e.loadRangedThroughMode(
			sessionID,
			head.Revision,
			ranged,
			false,
		)
		if err != nil {
			return err
		}
		if state.SessionID != sessionID ||
			state.Revision != head.Revision ||
			state.HeadHash != head.HeadHash {
			return NewError(
				"replay_failed",
				"full session verification does not match the durable log head",
				ErrCorruptLog,
			)
		}
		return nil
	}
	state, _, _, err := e.loadLegacyThrough(sessionID, 0)
	if err != nil {
		return err
	}
	if state.SessionID != sessionID {
		return NewError(
			"replay_failed",
			"full session verification identifies a different session",
			ErrCorruptLog,
		)
	}
	return nil
}

func (e *Engine) loadUsableCheckpoint(
	sessionID string,
	atOrBefore uint64,
	ranged RangeStore,
	checkpoints CheckpointStore,
) (Checkpoint, bool) {
	cursor := atOrBefore
	for cursor > 0 {
		checkpoint, err := checkpoints.LoadCheckpoint(sessionID, cursor)
		if err != nil {
			// Checkpoints are a derived cache. Any read failure safely falls
			// back to the authoritative event log.
			return Checkpoint{}, false
		}
		if checkpoint.Revision == 0 || checkpoint.Revision > cursor {
			return Checkpoint{}, false
		}
		if ValidateCheckpoint(checkpoint) == nil &&
			checkpoint.SessionID == sessionID &&
			checkpointAnchorMatches(sessionID, checkpoint, ranged) {
			return checkpoint, true
		}
		if checkpoint.Revision == 1 {
			break
		}
		cursor = checkpoint.Revision - 1
	}
	return Checkpoint{}, false
}

func checkpointAnchorMatches(
	sessionID string,
	checkpoint Checkpoint,
	ranged RangeStore,
) bool {
	event, err := loadVerifiedAnchor(sessionID, checkpoint.Revision, ranged)
	return err == nil && event.Hash == checkpoint.HeadHash
}

// loadVerifiedAnchor reads enough adjacent records to supply an actual
// previous-event hash. RangeStore already promises a hash-verified prefix; the
// Runtime additionally verifies the selected event against genesis or its
// immediate predecessor instead of accepting event.PrevHash as its own proof.
func loadVerifiedAnchor(
	sessionID string,
	revision uint64,
	ranged RangeStore,
) (protocol.EventRecord, error) {
	if revision == 0 {
		return protocol.EventRecord{}, ErrNotFound
	}
	after := uint64(0)
	limit := 1
	if revision > 1 {
		after = revision - 2
		limit = 2
	}
	page, err := ranged.LoadRange(sessionID, after, revision, limit)
	if err != nil {
		return protocol.EventRecord{}, err
	}
	if len(page.Events) != limit || page.HasMore {
		return protocol.EventRecord{}, ErrCorruptLog
	}
	if revision == 1 {
		event := page.Events[0]
		if err := VerifyEventRecord(0, "", event); err != nil {
			return protocol.EventRecord{}, err
		}
		return event, nil
	}
	previous := page.Events[0]
	event := page.Events[1]
	if previous.Sequence != revision-1 || event.Sequence != revision {
		return protocol.EventRecord{}, ErrCorruptLog
	}
	if err := VerifyEventRecord(previous.Sequence, previous.Hash, event); err != nil {
		return protocol.EventRecord{}, err
	}
	return event, nil
}

type checkpointCapture struct {
	state        protocol.SessionState
	identifiers  protocol.IdentifierHistory
	lineageEpoch uint64
}

func shouldSaveAutomaticCheckpoint(revision uint64) bool {
	return revision >= checkpointMinimumRev &&
		revision&(revision-1) == 0
}

func shouldRepairHeadCheckpoint(headRevision, checkpointRevision uint64) bool {
	if headRevision == 0 || checkpointRevision == headRevision {
		return false
	}
	if checkpointRevision == 0 {
		return true
	}
	return headRevision/checkpointRevision >= 2
}

// queueCheckpointLocked captures a stable revision while session.mu is held,
// then leaves all full cloning, validation, hashing, and Store I/O to one
// latest-wins background worker for the Session.
func (e *Engine) queueCheckpointLocked(session *managedSession) {
	checkpoints, ok := e.store.(CheckpointStore)
	if !ok {
		return
	}
	if _, ok := e.store.(RangeStore); !ok {
		// Runtime cannot consume a checkpoint without independently validating
		// its event-chain anchor through RangeStore.
		return
	}

	capture := checkpointCapture{
		// Reducers clone State before every transition and publish a replacement;
		// therefore the published object graph is immutable once captured here.
		state:        session.state,
		lineageEpoch: session.lineageEpoch,
		identifiers: protocol.IdentifierHistory{
			Version:          session.identifiers.Version,
			CoverageComplete: session.identifiers.CoverageComplete,
			Requests: make(
				map[string]protocol.RequestIdentity,
				len(session.identifiers.Requests),
			),
			Events: make(
				map[string]protocol.EventIdentity,
				len(session.identifiers.Events),
			),
		},
	}
	// Identifier ledger entries are immutable after insertion (including the
	// pointed-to Proposal/Arbitration values), so shallow map snapshots detach
	// map ownership without doing linear JSON work under the mutation lock.
	for requestID, identity := range session.identifiers.Requests {
		capture.identifiers.Requests[requestID] = identity
	}
	for eventID, identity := range session.identifiers.Events {
		capture.identifiers.Events[eventID] = identity
	}

	session.checkpointMu.Lock()
	if session.checkpointRunning {
		if session.checkpointPending == nil ||
			capture.state.Revision >= session.checkpointPending.state.Revision {
			session.checkpointPending = &capture
		}
		session.checkpointMu.Unlock()
		return
	}
	session.checkpointRunning = true
	session.checkpointMu.Unlock()

	go e.runCheckpointWorker(session, checkpoints, capture)
}

func (e *Engine) runCheckpointWorker(
	session *managedSession,
	checkpoints CheckpointStore,
	capture checkpointCapture,
) {
	for {
		checkpoint, err := BuildCheckpoint(
			capture.state,
			capture.identifiers,
			capture.lineageEpoch,
		)
		if err == nil {
			// A checkpoint is only a derived cache. A failure here must never
			// reverse a mutation whose event is already durable and published.
			_ = checkpoints.SaveCheckpoint(capture.state.SessionID, checkpoint)
		}

		session.checkpointMu.Lock()
		if session.checkpointPending == nil {
			session.checkpointRunning = false
			session.checkpointMu.Unlock()
			return
		}
		capture = *session.checkpointPending
		session.checkpointPending = nil
		session.checkpointMu.Unlock()
	}
}

func (e *Engine) maybeSaveCheckpoint(
	session *managedSession,
	event protocol.EventRecord,
) {
	if !shouldSaveAutomaticCheckpoint(event.Sequence) {
		return
	}
	e.queueCheckpointLocked(session)
}
