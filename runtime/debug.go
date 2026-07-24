package runtime

import (
	"encoding/json"
	"fmt"

	"github.com/sunrioa/rin/protocol"
)

// Timeline returns redacted, structural event metadata. It deliberately
// decodes only identifiers and enum-like state; authored and model text stays
// inside the authenticated state/replay APIs.
func (e *Engine) Timeline(request protocol.TimelineRequest) (protocol.TimelineResponse, error) {
	if err := protocol.ValidateTimeline(request); err != nil {
		return protocol.TimelineResponse{}, validationError(err)
	}
	session, err := e.session(request.SessionID)
	if err != nil {
		return protocol.TimelineResponse{}, err
	}
	session.mu.Lock()
	throughRevision := session.state.Revision
	session.mu.Unlock()

	events, hasMore, err := e.loadTimelineEvents(
		request.SessionID,
		request.AfterRevision,
		throughRevision,
		request.Limit,
	)
	if err != nil {
		return protocol.TimelineResponse{}, err
	}
	response := protocol.TimelineResponse{
		SessionID: request.SessionID, CurrentRevision: throughRevision,
		Entries:           make([]protocol.TimelineEntry, 0, request.Limit),
		NextAfterRevision: request.AfterRevision,
		HasMore:           hasMore,
	}
	for _, event := range events {
		entry, err := timelineEntry(event)
		if err != nil {
			return protocol.TimelineResponse{}, NewError("timeline_decode_failed", "could not decode event metadata", err)
		}
		response.Entries = append(response.Entries, entry)
		response.NextAfterRevision = event.Sequence
	}
	return response, nil
}

// Replay reconstructs a session at an exact event-log revision without
// mutating current state or writing a snapshot.
func (e *Engine) Replay(request protocol.ReplayRequest) (protocol.Snapshot, error) {
	if err := protocol.ValidateReplay(request); err != nil {
		return protocol.Snapshot{}, validationError(err)
	}
	session, err := e.session(request.SessionID)
	if err != nil {
		return protocol.Snapshot{}, err
	}
	session.mu.Lock()
	currentRevision := session.state.Revision
	currentLineageEpoch := session.lineageEpoch
	if request.Revision > currentRevision {
		session.mu.Unlock()
		return protocol.Snapshot{}, NewFieldError("revision_not_found", "requested revision does not exist", "revision", ErrNotFound)
	}
	identifiers, err := cloneIdentifierHistory(session.identifiers)
	session.mu.Unlock()
	if err != nil {
		return protocol.Snapshot{}, NewError(
			"replay_failed",
			"could not capture current identifier history",
			err,
		)
	}
	state, _, targetLineageEpoch, err := e.loadSessionThrough(
		request.SessionID,
		request.Revision,
	)
	if err != nil {
		return protocol.Snapshot{}, err
	}
	if state.Revision != request.Revision {
		return protocol.Snapshot{}, NewFieldError("revision_not_found", "requested revision does not exist", "revision", ErrNotFound)
	}
	if targetLineageEpoch > currentLineageEpoch {
		return protocol.Snapshot{}, NewError(
			"replay_failed",
			"replayed lineage is newer than the captured session",
			ErrCorruptLog,
		)
	}
	snapshot, err := snapshotWithIdentifiers(state, identifiers)
	if err != nil {
		if ErrorCode(err) == "snapshot_too_large" {
			return protocol.Snapshot{}, err
		}
		return protocol.Snapshot{}, NewError("replay_failed", "could not snapshot replayed state", err)
	}
	return snapshot, nil
}

func (e *Engine) loadTimelineEvents(
	sessionID string,
	afterRevision uint64,
	throughRevision uint64,
	limit int,
) ([]protocol.EventRecord, bool, error) {
	if afterRevision >= throughRevision {
		return []protocol.EventRecord{}, false, nil
	}
	if ranged, ok := e.store.(RangeStore); ok {
		previousHash := ""
		if afterRevision > 0 {
			anchor, err := loadVerifiedAnchor(
				sessionID,
				afterRevision,
				ranged,
			)
			if err != nil {
				return nil, false, NewError(
					"store_load_failed",
					"could not load timeline anchor",
					err,
				)
			}
			previousHash = anchor.Hash
		}
		page, err := ranged.LoadRange(
			sessionID,
			afterRevision,
			throughRevision,
			limit,
		)
		if err != nil {
			return nil, false, NewError(
				"store_load_failed",
				"could not load timeline range",
				err,
			)
		}
		if len(page.Events) == 0 || len(page.Events) > limit {
			return nil, false, NewError(
				"timeline_decode_failed",
				"timeline range is not a bounded contiguous page",
				ErrCorruptLog,
			)
		}
		previousRevision := afterRevision
		for _, event := range page.Events {
			if err := VerifyEventRecord(previousRevision, previousHash, event); err != nil {
				return nil, false, NewError(
					"timeline_decode_failed",
					"timeline event hash chain is invalid",
					err,
				)
			}
			previousRevision = event.Sequence
			previousHash = event.Hash
		}
		if previousRevision > throughRevision ||
			(previousRevision < throughRevision) != page.HasMore {
			return nil, false, NewError(
				"timeline_decode_failed",
				"timeline range boundary metadata is inconsistent",
				ErrCorruptLog,
			)
		}
		return page.Events, page.HasMore, nil
	}

	events, err := e.store.Load(sessionID)
	if err != nil {
		return nil, false, NewError(
			"store_load_failed",
			"could not load session log",
			err,
		)
	}
	selected := make([]protocol.EventRecord, 0, limit)
	previousRevision := uint64(0)
	previousHash := ""
	foundThrough := false
	for _, event := range events {
		if event.Sequence > throughRevision {
			break
		}
		if err := VerifyEventRecord(previousRevision, previousHash, event); err != nil {
			return nil, false, NewError(
				"timeline_decode_failed",
				"timeline event hash chain is invalid",
				err,
			)
		}
		previousRevision = event.Sequence
		previousHash = event.Hash
		if event.Sequence > afterRevision && len(selected) < limit {
			selected = append(selected, event)
		}
		if event.Sequence == throughRevision {
			foundThrough = true
			break
		}
	}
	if !foundThrough {
		return nil, false, NewError(
			"timeline_decode_failed",
			"timeline log ended before the captured revision",
			ErrCorruptLog,
		)
	}
	hasMore := len(selected) > 0 &&
		selected[len(selected)-1].Sequence < throughRevision
	return selected, hasMore, nil
}

// replayEvents reconstructs State as of targetRevision while projecting
// identifier membership through the complete local log. Consequently a
// Replay Snapshot cannot release IDs used on a later, abandoned branch.
func replayEvents(
	events []protocol.EventRecord,
	targetRevision uint64,
) (protocol.SessionState, protocol.IdentifierHistory, error) {
	var (
		state       protocol.SessionState
		targetState protocol.SessionState
		err         error
	)
	identifiers := newIdentifierHistory(true)
	for _, event := range events {
		normalizeWritableState(&state)
		state, err = applyEvent(state, event)
		if err != nil {
			return protocol.SessionState{}, protocol.IdentifierHistory{}, err
		}
		identifierDelta, identityErr := prepareIdentifierEvent(identifiers, event)
		if identityErr != nil {
			return protocol.SessionState{}, protocol.IdentifierHistory{}, identityErr
		}
		applyIdentifierDelta(&identifiers, identifierDelta)
		if targetRevision > 0 && event.Sequence == targetRevision {
			targetState, err = clone(state)
			if err != nil {
				return protocol.SessionState{}, protocol.IdentifierHistory{}, err
			}
		}
	}
	if targetRevision > 0 {
		return targetState, identifiers, nil
	}
	return state, identifiers, nil
}

func timelineEntry(event protocol.EventRecord) (protocol.TimelineEntry, error) {
	entry := protocol.TimelineEntry{
		Sequence: event.Sequence, Type: event.Type, RequestID: event.RequestID,
		RecordedAt: event.RecordedAt, Hash: event.Hash, PrevHash: event.PrevHash,
	}
	switch event.Type {
	case EventSessionCreated:
		var payload createdPayload
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			return protocol.TimelineEntry{}, err
		}
		entry.EntityIDs = []string{payload.Request.SessionID}
		for _, actor := range payload.Request.Actors {
			entry.ActorIDs = append(entry.ActorIDs, actor.ID)
		}
		entry.Status = "created"
	case EventObserved:
		var payload observedPayload
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			return protocol.TimelineEntry{}, err
		}
		entry.EntityIDs = []string{payload.Request.EventID}
		entry.ActorIDs = append([]string(nil), payload.Request.ObserverIDs...)
		entry.Status = payload.Request.Kind
	case EventProposed:
		var payload proposedPayload
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			return protocol.TimelineEntry{}, err
		}
		entry.EntityIDs = []string{payload.Proposal.ID, payload.Proposal.Action.ID}
		entry.ActorIDs = []string{payload.Proposal.ActorID}
		entry.Status = payload.Proposal.Status
	case EventCommitted:
		var payload committedPayload
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			return protocol.TimelineEntry{}, err
		}
		entry.EntityIDs = []string{payload.Request.ProposalID, payload.Request.EventID}
		entry.Status = acceptedStatus(payload.Request.Accepted)
	case EventBatchCommitted:
		var payload batchCommittedPayload
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			return protocol.TimelineEntry{}, err
		}
		for _, item := range payload.Request.Items {
			entry.EntityIDs = append(entry.EntityIDs, item.ProposalID, item.EventID)
		}
		entry.Status = "committed"
	case EventActivityUpdated:
		var payload activityUpdatedPayload
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			return protocol.TimelineEntry{}, err
		}
		for _, update := range payload.Request.Updates {
			entry.ActorIDs = append(entry.ActorIDs, update.ActorID)
		}
		entry.Status = "updated"
	case EventArbitrated:
		var payload arbitratedPayload
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			return protocol.TimelineEntry{}, err
		}
		entry.EntityIDs = append(entry.EntityIDs, payload.Record.ID)
		for _, decision := range payload.Record.Decisions {
			entry.EntityIDs = append(entry.EntityIDs, decision.ProposalID)
			entry.ActorIDs = append(entry.ActorIDs, decision.ActorID)
		}
		entry.Status = "resolved"
	case EventSessionRestored:
		var payload restoredPayload
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			return protocol.TimelineEntry{}, err
		}
		entry.EntityIDs = []string{payload.Snapshot.State.SessionID}
		entry.Status = "restored"
	default:
		return protocol.TimelineEntry{}, fmt.Errorf("%w: unknown event type %q", ErrCorruptLog, event.Type)
	}
	entry.EntityIDs = uniqueSorted(entry.EntityIDs)
	entry.ActorIDs = uniqueSorted(entry.ActorIDs)
	return entry, nil
}

func acceptedStatus(accepted bool) string {
	if accepted {
		return "accepted"
	}
	return "rejected"
}
