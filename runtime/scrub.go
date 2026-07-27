package runtime

import (
	"context"
	"sort"

	"github.com/sunrioa/rin/protocol"
)

const (
	// DefaultScrubEventBudget bounds one maintenance pass without making the
	// operator choose a value for the common case.
	DefaultScrubEventBudget = 4096
	MaxScrubEventBudget     = 65536
)

// ScrubReport describes one bounded, checkpoint-independent maintenance pass.
// A cycle is complete only after every Session known to that cycle has been
// reduced from genesis through its captured durable head.
type ScrubReport struct {
	CheckedEvents     int    `json:"checked_events"`
	CompletedSessions int    `json:"completed_sessions"`
	SessionID         string `json:"session_id,omitempty"`
	Revision          uint64 `json:"revision,omitempty"`
	TargetRevision    uint64 `json:"target_revision,omitempty"`
	CompletedCycles   uint64 `json:"completed_cycles"`
	CycleComplete     bool   `json:"cycle_complete"`
}

type scrubProgress struct {
	afterSessionID  string
	active          *scrubSession
	completedCycles uint64
}

type scrubSession struct {
	id          string
	target      EventAnchor
	state       protocol.SessionState
	identifiers identifierLedger
}

// Scrub incrementally verifies authoritative event logs without consulting
// checkpoints. Calls are serialized, resumable, and process at most
// maxEvents. Sessions appended after their head is captured are covered by the
// next cycle.
func (e *Engine) Scrub(
	ctx context.Context,
	maxEvents int,
) (report ScrubReport, resultErr error) {
	defer func() {
		if ErrorCode(resultErr) == "scrub_failed" {
			e.scrubFailures.Add(1)
		}
	}()
	if ctx == nil {
		return ScrubReport{}, NewFieldError(
			"invalid_scrub_context",
			"scrub context is required",
			"context",
			ErrConflict,
		)
	}
	if maxEvents < 1 || maxEvents > MaxScrubEventBudget {
		return ScrubReport{}, NewFieldError(
			"invalid_scrub_budget",
			"scrub event budget must be between 1 and 65536",
			"max_events",
			ErrConflict,
		)
	}
	finish, err := e.beginOperation()
	if err != nil {
		return ScrubReport{}, err
	}
	defer finish()
	if err := ctx.Err(); err != nil {
		return ScrubReport{}, scrubContextError(err)
	}
	ranged, ok := e.store.(RangeStore)
	if !ok {
		return ScrubReport{}, NewError(
			"scrub_unsupported",
			"incremental scrub requires RangeStore",
			ErrConflict,
		)
	}

	select {
	case e.scrubGate <- struct{}{}:
		defer func() { <-e.scrubGate }()
	case <-ctx.Done():
		return ScrubReport{}, scrubContextError(ctx.Err())
	}

	e.scrubMu.Lock()
	progress := e.scrub
	if progress.active != nil {
		active := *progress.active
		progress.active = &active
	}
	e.scrubMu.Unlock()
	defer func() {
		e.scrubMu.Lock()
		e.scrub = progress
		e.scrubMu.Unlock()
	}()

	report = ScrubReport{CompletedCycles: progress.completedCycles}
	ids := e.scrubSessionIDs()
	if progress.active != nil &&
		!containsSortedString(ids, progress.active.id) {
		// A concurrent lifecycle delete permanently retires the Session. It no
		// longer belongs to the current cycle.
		progress.afterSessionID = progress.active.id
		progress.active = nil
	}

	for report.CheckedEvents < maxEvents {
		if err := ctx.Err(); err != nil {
			return currentScrubReport(report, progress), scrubContextError(err)
		}
		if progress.active == nil {
			sessionID, found := nextSortedString(
				ids,
				progress.afterSessionID,
			)
			if !found {
				progress.completedCycles++
				progress.afterSessionID = ""
				report.CompletedCycles = progress.completedCycles
				report.CycleComplete = true
				return report, nil
			}
			head, headErr := ranged.Head(sessionID)
			if headErr != nil {
				report.SessionID = sessionID
				return report, NewError(
					"scrub_failed",
					"could not read the Session event-log head",
					headErr,
				)
			}
			if head.Revision == 0 || head.HeadHash == "" {
				report.SessionID = sessionID
				return report, NewError(
					"scrub_failed",
					"Session event log has no valid head",
					ErrCorruptLog,
				)
			}
			identifiers, identityErr := identifierLedgerFromHistory(
				newIdentifierHistory(true),
			)
			if identityErr != nil {
				return report, NewError(
					"scrub_failed",
					"could not initialize identifier verification",
					identityErr,
				)
			}
			progress.active = &scrubSession{
				id:          sessionID,
				target:      head,
				identifiers: identifiers,
			}
		}

		active := progress.active
		report.SessionID = active.id
		report.Revision = active.state.Revision
		report.TargetRevision = active.target.Revision
		e.publishScrubDiagnostics(progress)
		remaining := maxEvents - report.CheckedEvents
		limit := min(remaining, replayPageSize)
		page, loadErr := ranged.LoadRange(
			active.id,
			active.state.Revision,
			active.target.Revision,
			limit,
		)
		if loadErr != nil {
			return currentScrubReport(report, progress), NewError(
				"scrub_failed",
				"could not load a Session event range",
				loadErr,
			)
		}
		if len(page.Events) == 0 || len(page.Events) > limit {
			progress.active = nil
			return report, NewError(
				"scrub_failed",
				"Session event range is not a bounded page",
				ErrCorruptLog,
			)
		}

		before := active.state.Revision
		for _, event := range page.Events {
			if err := ctx.Err(); err != nil {
				return currentScrubReport(report, progress), scrubContextError(err)
			}
			if event.Sequence > active.target.Revision {
				progress.active = nil
				return report, NewError(
					"scrub_failed",
					"Session event range exceeded its captured head",
					ErrCorruptLog,
				)
			}
			normalizeWritableState(&active.state)
			next, applyErr := applyEvent(active.state, event)
			if applyErr != nil {
				progress.active = nil
				return report, NewError(
					"scrub_failed",
					"Session event range is invalid",
					applyErr,
				)
			}
			if sizeErr := ensureSessionStateSize(
				next,
				e.maxSessionStateBytes,
			); sizeErr != nil {
				progress.active = nil
				return report, NewError(
					"scrub_failed",
					"Session State exceeds its configured byte limit",
					sizeErr,
				)
			}
			nextIdentifiers, identityErr := prepareLedgerIdentifierEvent(
				active.identifiers,
				event,
			)
			if identityErr != nil {
				progress.active = nil
				return report, NewError(
					"scrub_failed",
					"Session event identifiers are invalid",
					identityErr,
				)
			}
			active.state = next
			active.identifiers = nextIdentifiers
			report.CheckedEvents++
			report.Revision = active.state.Revision
		}
		if active.state.Revision <= before {
			progress.active = nil
			return report, NewError(
				"scrub_failed",
				"Session event range made no progress",
				ErrCorruptLog,
			)
		}
		if active.state.Revision < active.target.Revision && !page.HasMore {
			progress.active = nil
			return report, NewError(
				"scrub_failed",
				"Session event range omitted a durable suffix",
				ErrCorruptLog,
			)
		}
		if active.state.Revision == active.target.Revision {
			if page.HasMore ||
				active.state.SessionID != active.id ||
				active.state.HeadHash != active.target.HeadHash {
				progress.active = nil
				return report, NewError(
					"scrub_failed",
					"Session replay does not match its captured head",
					ErrCorruptLog,
				)
			}
			report.SessionID = active.id
			report.Revision = active.state.Revision
			report.TargetRevision = active.target.Revision
			report.CompletedSessions++
			progress.afterSessionID = active.id
			progress.active = nil
		}
	}
	report = currentScrubReport(report, progress)
	if progress.active == nil {
		if _, found := nextSortedString(ids, progress.afterSessionID); !found {
			progress.completedCycles++
			progress.afterSessionID = ""
			report.CompletedCycles = progress.completedCycles
			report.CycleComplete = true
		}
	}
	return report, nil
}

func (e *Engine) scrubSessionIDs() []string {
	e.mu.RLock()
	ids := make([]string, 0, len(e.sessions))
	for id := range e.sessions {
		ids = append(ids, id)
	}
	e.mu.RUnlock()
	sort.Strings(ids)
	return ids
}

func (e *Engine) publishScrubDiagnostics(progress scrubProgress) {
	published := scrubProgress{
		afterSessionID:  progress.afterSessionID,
		completedCycles: progress.completedCycles,
	}
	if progress.active != nil {
		published.active = &scrubSession{
			id:     progress.active.id,
			target: progress.active.target,
			state: protocol.SessionState{
				Revision: progress.active.state.Revision,
			},
		}
	}
	e.scrubMu.Lock()
	e.scrub = published
	e.scrubMu.Unlock()
}

func currentScrubReport(
	report ScrubReport,
	progress scrubProgress,
) ScrubReport {
	report.CompletedCycles = progress.completedCycles
	if progress.active != nil {
		report.SessionID = progress.active.id
		report.Revision = progress.active.state.Revision
		report.TargetRevision = progress.active.target.Revision
	}
	return report
}

func scrubContextError(err error) error {
	return NewError(
		"scrub_canceled",
		"incremental scrub was canceled",
		err,
	)
}

func nextSortedString(values []string, after string) (string, bool) {
	index := sort.SearchStrings(values, after)
	if index < len(values) && values[index] == after {
		index++
	}
	if index == len(values) {
		return "", false
	}
	return values[index], true
}

func containsSortedString(values []string, target string) bool {
	index := sort.SearchStrings(values, target)
	return index < len(values) && values[index] == target
}
