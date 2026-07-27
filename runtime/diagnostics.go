package runtime

import "errors"

type Diagnostics struct {
	KnownSessions              int            `json:"known_sessions"`
	LoadedSessions             int            `json:"loaded_sessions"`
	KnownCorruptSessions       int            `json:"known_corrupt_sessions"`
	LoadErrorsByCode           map[string]int `json:"load_errors_by_code"`
	PendingUncertaintyBarriers int            `json:"pending_uncertainty_barriers"`
	CheckpointWorkers          int            `json:"checkpoint_workers"`
	CheckpointPending          int            `json:"checkpoint_pending"`
	CheckpointFailures         uint64         `json:"checkpoint_failures"`
	CheckpointQuotaSkips       uint64         `json:"checkpoint_quota_skips"`
	ScrubCompletedCycles       uint64         `json:"scrub_completed_cycles"`
	ScrubFailures              uint64         `json:"scrub_failures"`
	ScrubActive                bool           `json:"scrub_active"`
	ScrubRevision              uint64         `json:"scrub_revision,omitempty"`
	ScrubTargetRevision        uint64         `json:"scrub_target_revision,omitempty"`
	SessionSoftLimitBytes      uint64         `json:"session_soft_limit_bytes"`
	SessionHardLimitBytes      uint64         `json:"session_hard_limit_bytes"`
	Closed                     bool           `json:"closed"`
	ActiveOperations           int            `json:"active_operations"`
}

func (e *Engine) Diagnostics() Diagnostics {
	e.shutdownMu.Lock()
	closed := e.closed
	activeOperations := e.activeOperations
	checkpointWorkers := e.checkpointWorkers
	e.shutdownMu.Unlock()
	e.mu.RLock()
	sessions := make([]*managedSession, 0, len(e.sessions))
	for _, session := range e.sessions {
		sessions = append(sessions, session)
	}
	pendingCreates := len(e.pendingCreates)
	e.mu.RUnlock()
	e.scrubMu.Lock()
	scrubCompletedCycles := e.scrub.completedCycles
	var scrubRevision, scrubTargetRevision uint64
	scrubActive := e.scrub.active != nil
	if e.scrub.active != nil {
		scrubRevision = e.scrub.active.state.Revision
		scrubTargetRevision = e.scrub.active.target.Revision
	}
	e.scrubMu.Unlock()

	result := Diagnostics{
		KnownSessions:              len(sessions),
		LoadErrorsByCode:           make(map[string]int),
		PendingUncertaintyBarriers: pendingCreates,
		CheckpointFailures:         e.checkpointFailures.Load(),
		CheckpointQuotaSkips:       e.checkpointQuotaSkips.Load(),
		ScrubCompletedCycles:       scrubCompletedCycles,
		ScrubFailures:              e.scrubFailures.Load(),
		SessionSoftLimitBytes:      e.sessionSoftLimitBytes,
		SessionHardLimitBytes:      e.sessionHardLimitBytes,
		Closed:                     closed,
		ActiveOperations:           activeOperations,
		CheckpointWorkers:          checkpointWorkers,
		ScrubActive:                scrubActive,
		ScrubRevision:              scrubRevision,
		ScrubTargetRevision:        scrubTargetRevision,
	}
	for _, session := range sessions {
		session.mu.Lock()
		if session.loaded {
			result.LoadedSessions++
		}
		if session.lastLoadErrorCode != "" {
			result.KnownCorruptSessions++
			result.LoadErrorsByCode[session.lastLoadErrorCode]++
		}
		result.PendingUncertaintyBarriers += len(session.uncertainMutations)
		session.mu.Unlock()

		session.checkpointMu.Lock()
		// Worker count is tracked at Engine scope so Close can wait without
		// scanning Session locks.
		if session.checkpointPending != nil {
			result.CheckpointPending++
		}
		session.checkpointMu.Unlock()
	}
	return result
}

func (e *Engine) Ready() error {
	finish, err := e.beginOperation()
	if err != nil {
		return err
	}
	defer finish()
	_, err = e.store.ListSessions()
	if err != nil {
		return errors.New("Session Store is not readable")
	}
	return nil
}
