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
	SessionSoftLimitBytes      uint64         `json:"session_soft_limit_bytes"`
	SessionHardLimitBytes      uint64         `json:"session_hard_limit_bytes"`
}

func (e *Engine) Diagnostics() Diagnostics {
	e.mu.RLock()
	sessions := make([]*managedSession, 0, len(e.sessions))
	for _, session := range e.sessions {
		sessions = append(sessions, session)
	}
	pendingCreates := len(e.pendingCreates)
	e.mu.RUnlock()

	result := Diagnostics{
		KnownSessions:              len(sessions),
		LoadErrorsByCode:           make(map[string]int),
		PendingUncertaintyBarriers: pendingCreates,
		CheckpointFailures:         e.checkpointFailures.Load(),
		CheckpointQuotaSkips:       e.checkpointQuotaSkips.Load(),
		SessionSoftLimitBytes:      e.sessionSoftLimitBytes,
		SessionHardLimitBytes:      e.sessionHardLimitBytes,
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
		if session.checkpointRunning {
			result.CheckpointWorkers++
		}
		if session.checkpointPending != nil {
			result.CheckpointPending++
		}
		session.checkpointMu.Unlock()
	}
	return result
}

func (e *Engine) Ready() error {
	_, err := e.store.ListSessions()
	if err != nil {
		return errors.New("Session Store is not readable")
	}
	return nil
}
