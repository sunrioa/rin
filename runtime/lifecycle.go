package runtime

import (
	"errors"
	"time"

	"github.com/sunrioa/rin/protocol"
)

func (e *Engine) SessionStats(
	request protocol.SessionRequest,
) (protocol.SessionStats, error) {
	if err := protocol.ValidateSessionRequest(request); err != nil {
		return protocol.SessionStats{}, validationError(err)
	}
	lifecycle, ok := e.store.(LifecycleStore)
	if !ok {
		return protocol.SessionStats{}, NewError(
			"lifecycle_unsupported",
			"store does not support Session lifecycle operations",
			ErrConflict,
		)
	}
	session, err := e.session(request.SessionID)
	if err != nil {
		return protocol.SessionStats{}, err
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	stored, err := lifecycle.Stats(request.SessionID)
	if err != nil {
		return protocol.SessionStats{}, NewError(
			"store_stats_failed",
			"could not inspect Session storage",
			err,
		)
	}
	total := stored.EventLogBytes + stored.SnapshotBytes +
		stored.CheckpointBytes + stored.IndexBytes + stored.OtherBytes
	state := "active"
	if session.archived {
		state = "archived"
	}
	return protocol.SessionStats{
		SessionID:  request.SessionID,
		Lifecycle:  state,
		Revision:   session.state.Revision,
		HeadHash:   session.state.HeadHash,
		EventCount: stored.EventCount,
		Bytes: protocol.SessionStorageBytes{
			EventLog:    stored.EventLogBytes,
			Snapshots:   stored.SnapshotBytes,
			Checkpoints: stored.CheckpointBytes,
			Indexes:     stored.IndexBytes,
			Other:       stored.OtherBytes,
			Total:       total,
		},
		SoftLimitBytes: e.sessionSoftLimitBytes,
		HardLimitBytes: e.sessionHardLimitBytes,
		SoftLimitExceeded: e.sessionSoftLimitBytes > 0 &&
			total > e.sessionSoftLimitBytes,
		HardLimitExceeded: e.sessionHardLimitBytes > 0 &&
			total > e.sessionHardLimitBytes,
	}, nil
}

func (e *Engine) ArchiveSession(
	request protocol.ArchiveSessionRequest,
) (protocol.ArchiveSessionResult, error) {
	if err := protocol.ValidateArchiveSession(request); err != nil {
		return protocol.ArchiveSessionResult{}, validationError(err)
	}
	lifecycle, ok := e.store.(LifecycleStore)
	if !ok {
		return protocol.ArchiveSessionResult{}, NewError(
			"lifecycle_unsupported",
			"store does not support Session lifecycle operations",
			ErrConflict,
		)
	}
	requestHash, err := requestDigest(request)
	if err != nil {
		return protocol.ArchiveSessionResult{}, NewError(
			"request_encode_failed",
			"could not identify archive request",
			err,
		)
	}
	session, err := e.session(request.SessionID)
	if err != nil {
		return protocol.ArchiveSessionResult{}, err
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.state.Binding != request.ExpectedBinding {
		return protocol.ArchiveSessionResult{}, NewFieldError(
			"binding_mismatch",
			"Session does not match expected game content",
			"expected_binding",
			ErrConflict,
		)
	}
	if session.state.Revision != request.ExpectedRevision ||
		session.state.HeadHash != request.ExpectedHeadHash {
		return protocol.ArchiveSessionResult{}, NewError(
			"archive_precondition_failed",
			"Session head does not match archive precondition",
			ErrConflict,
		)
	}
	if len(session.uncertainMutations) != 0 {
		return protocol.ArchiveSessionResult{}, unresolvedMutationError(session)
	}
	receiptHash, err := hashJSON(struct {
		SessionID   string `json:"session_id"`
		RequestHash string `json:"request_hash"`
	}{request.SessionID, requestHash})
	if err != nil {
		return protocol.ArchiveSessionResult{}, NewError(
			"request_encode_failed",
			"could not identify archive receipt",
			err,
		)
	}
	record := ArchiveRecord{
		SessionID:   request.SessionID,
		RequestID:   request.RequestID,
		RequestHash: requestHash,
		ReceiptID:   "archive." + receiptHash[:24],
		ArchivedAt:  e.now().UTC().Format(time.RFC3339Nano),
		Anchor: EventAnchor{
			Revision: session.state.Revision,
			HeadHash: session.state.HeadHash,
		},
	}
	stored, duplicate, err := lifecycle.Archive(record)
	if err != nil {
		if errors.Is(err, ErrConflict) {
			return protocol.ArchiveSessionResult{}, requestConflict(request.RequestID)
		}
		return protocol.ArchiveSessionResult{}, NewError(
			"store_archive_failed",
			"could not archive Session",
			err,
		)
	}
	session.archived = true
	session.archive = stored
	return protocol.ArchiveSessionResult{
		SessionID:  stored.SessionID,
		ReceiptID:  stored.ReceiptID,
		Revision:   stored.Anchor.Revision,
		HeadHash:   stored.Anchor.HeadHash,
		ArchivedAt: stored.ArchivedAt,
		Duplicate:  duplicate,
	}, nil
}

func (e *Engine) DeleteSession(
	request protocol.DeleteSessionRequest,
) (protocol.DeleteSessionResult, error) {
	if err := protocol.ValidateDeleteSession(request); err != nil {
		return protocol.DeleteSessionResult{}, validationError(err)
	}
	lifecycle, ok := e.store.(LifecycleStore)
	if !ok {
		return protocol.DeleteSessionResult{}, NewError(
			"lifecycle_unsupported",
			"store does not support Session lifecycle operations",
			ErrConflict,
		)
	}
	requestHash, err := requestDigest(request)
	if err != nil {
		return protocol.DeleteSessionResult{}, NewError(
			"request_encode_failed",
			"could not identify delete request",
			err,
		)
	}
	bindingHash, err := hashJSON(request.ExpectedBinding)
	if err != nil {
		return protocol.DeleteSessionResult{}, NewError(
			"request_encode_failed",
			"could not identify expected Binding",
			err,
		)
	}
	receiptHash, err := hashJSON(struct {
		SessionID   string `json:"session_id"`
		RequestHash string `json:"request_hash"`
	}{request.SessionID, requestHash})
	if err != nil {
		return protocol.DeleteSessionResult{}, NewError(
			"request_encode_failed",
			"could not identify delete receipt",
			err,
		)
	}
	record := DeleteRecord{
		FormatVersion: "rin.session-tombstone/v1",
		SessionID:     request.SessionID,
		RequestID:     request.RequestID,
		RequestHash:   requestHash,
		ReceiptID:     "delete." + receiptHash[:24],
		DeletedAt:     e.now().UTC().Format(time.RFC3339Nano),
		Anchor: EventAnchor{
			Revision: request.ExpectedRevision,
			HeadHash: request.ExpectedHeadHash,
		},
		BindingHash:      bindingHash,
		ArchiveReceiptID: request.ArchiveReceiptID,
	}

	unlockLifecycle := e.lockSessionLifecycle(request.SessionID)
	defer unlockLifecycle()
	e.mu.RLock()
	session := e.sessions[request.SessionID]
	e.mu.RUnlock()
	if session == nil {
		stored, duplicate, deleteErr := lifecycle.Delete(record)
		if deleteErr != nil {
			if errors.Is(deleteErr, ErrRetired) {
				return protocol.DeleteSessionResult{}, NewError(
					"session_retired",
					"Session was deleted by a different request",
					ErrConflict,
				)
			}
			return protocol.DeleteSessionResult{}, NewFieldError(
				"session_not_found",
				"session does not exist",
				"session_id",
				ErrNotFound,
			)
		}
		return deleteResult(stored, duplicate), nil
	}
	if err := e.ensureLoaded(session); err != nil {
		return protocol.DeleteSessionResult{}, err
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.state.Binding != request.ExpectedBinding {
		return protocol.DeleteSessionResult{}, NewFieldError(
			"binding_mismatch",
			"Session does not match expected game content",
			"expected_binding",
			ErrConflict,
		)
	}
	if !session.archived ||
		session.archive.ReceiptID != request.ArchiveReceiptID ||
		session.state.Revision != request.ExpectedRevision ||
		session.state.HeadHash != request.ExpectedHeadHash {
		return protocol.DeleteSessionResult{}, NewError(
			"delete_precondition_failed",
			"Session archive does not match delete precondition",
			ErrConflict,
		)
	}
	stored, duplicate, err := lifecycle.Delete(record)
	if err != nil {
		return protocol.DeleteSessionResult{}, NewError(
			"store_delete_failed",
			"could not delete Session",
			err,
		)
	}
	e.mu.Lock()
	delete(e.sessions, request.SessionID)
	e.mu.Unlock()
	return deleteResult(stored, duplicate), nil
}

func deleteResult(
	record DeleteRecord,
	duplicate bool,
) protocol.DeleteSessionResult {
	return protocol.DeleteSessionResult{
		SessionID: record.SessionID,
		ReceiptID: record.ReceiptID,
		DeletedAt: record.DeletedAt,
		Duplicate: duplicate,
	}
}
