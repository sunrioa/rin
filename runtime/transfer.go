package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"

	"github.com/sunrioa/rin/protocol"
)

// TransferSink receives an ordered, bounded-memory export. Implementations
// must consume each call before returning; Runtime reuses no frame buffers but
// intentionally provides no complete-transfer aggregate.
type TransferSink interface {
	WriteManifest(manifest protocol.TransferManifest) error
	WriteEvent(frame protocol.TransferEvent) error
	WriteComplete(complete protocol.TransferComplete) error
}

// ExportTransfer streams one immutable complete-lineage boundary. Stores
// without RangeStore are rejected rather than falling back to unbounded Load.
func (e *Engine) ExportTransfer(
	ctx context.Context,
	request protocol.SessionRequest,
	sink TransferSink,
) error {
	if ctx == nil {
		return NewError(
			"transfer_invalid",
			"transfer context is required",
			nil,
		)
	}
	if sink == nil {
		return NewError("transfer_invalid", "transfer sink is required", nil)
	}
	if err := protocol.ValidateSessionRequest(request); err != nil {
		return validationError(err)
	}
	ranged, ok := e.store.(RangeStore)
	if !ok {
		return NewError(
			"transfer_unavailable",
			"store does not support bounded event ranges",
			ErrConflict,
		)
	}
	session, err := e.session(request.SessionID)
	if err != nil {
		return err
	}
	session.mu.Lock()
	manifest := protocol.TransferManifest{
		Type:              protocol.TransferFrameManifest,
		TransferVersion:   protocol.TransferVersion,
		ProtocolVersion:   protocol.Version,
		ProjectionVersion: ReducerProjectionVersion,
		SessionID:         session.id,
		Binding:           session.state.Binding,
		TerminalRevision:  session.state.Revision,
		TerminalHeadHash:  session.state.HeadHash,
		EventCount:        session.state.Revision,
		LineageGeneration: session.lineageEpoch,
		HashAlgorithm:     protocol.TransferHashAlgorithm,
	}
	session.mu.Unlock()
	manifest.TransferID, err = newTransferID()
	if err != nil {
		return NewError(
			"transfer_id_failed",
			"could not create transfer identity",
			err,
		)
	}
	if err := protocol.ValidateTransferManifest(manifest); err != nil {
		return NewError(
			"transfer_failed",
			"captured transfer boundary is invalid",
			err,
		)
	}
	hasher := protocol.NewTransferStreamHasher()
	if err := hasher.WriteManifest(manifest); err != nil {
		return NewError("transfer_failed", "could not hash transfer manifest", err)
	}
	if err := sink.WriteManifest(manifest); err != nil {
		return err
	}

	var previousRevision uint64
	var previousHash string
	for previousRevision < manifest.TerminalRevision {
		if err := ctx.Err(); err != nil {
			return NewError("transfer_cancelled", "transfer was cancelled", err)
		}
		page, err := ranged.LoadRange(
			manifest.SessionID,
			previousRevision,
			manifest.TerminalRevision,
			replayPageSize,
		)
		if err != nil {
			return NewError(
				"store_load_failed",
				"could not load transfer event range",
				err,
			)
		}
		if len(page.Events) == 0 || len(page.Events) > replayPageSize {
			return NewError(
				"transfer_failed",
				"transfer event range is not a bounded page",
				ErrCorruptLog,
			)
		}
		for _, event := range page.Events {
			if err := ctx.Err(); err != nil {
				return NewError(
					"transfer_cancelled",
					"transfer was cancelled",
					err,
				)
			}
			if event.Sequence > manifest.TerminalRevision {
				return NewError(
					"transfer_failed",
					"transfer event range exceeded its boundary",
					ErrCorruptLog,
				)
			}
			if err := VerifyEventRecord(
				previousRevision,
				previousHash,
				event,
			); err != nil {
				return NewError(
					"transfer_failed",
					"transfer event hash chain is invalid",
					err,
				)
			}
			recordHash, err := protocol.TransferEventRecordSHA256(event)
			if err != nil {
				return NewError(
					"transfer_failed",
					"could not hash transfer event",
					err,
				)
			}
			frame := protocol.TransferEvent{
				Type:         protocol.TransferFrameEvent,
				Record:       event,
				RecordSHA256: recordHash,
			}
			if err := hasher.WriteEvent(frame); err != nil {
				return NewError(
					"transfer_failed",
					"could not hash transfer event frame",
					err,
				)
			}
			if err := sink.WriteEvent(frame); err != nil {
				return err
			}
			previousRevision = event.Sequence
			previousHash = event.Hash
		}
		if previousRevision < manifest.TerminalRevision && !page.HasMore {
			return NewError(
				"transfer_failed",
				"transfer event range omitted a durable suffix",
				ErrCorruptLog,
			)
		}
		if previousRevision == manifest.TerminalRevision && page.HasMore {
			return NewError(
				"transfer_failed",
				"transfer event range reported data past its boundary",
				ErrCorruptLog,
			)
		}
	}
	if previousHash != manifest.TerminalHeadHash {
		return NewError(
			"transfer_failed",
			"transfer terminal event does not match captured head",
			ErrCorruptLog,
		)
	}
	streamHash, err := hasher.SumSHA256()
	if err != nil {
		return NewError(
			"transfer_failed",
			"could not finish transfer checksum",
			err,
		)
	}
	return sink.WriteComplete(protocol.TransferComplete{
		Type:             protocol.TransferFrameComplete,
		TerminalRevision: manifest.TerminalRevision,
		TerminalHeadHash: manifest.TerminalHeadHash,
		EventCount:       manifest.EventCount,
		StreamSHA256:     streamHash,
	})
}

// BeginTransferImport validates trusted metadata and creates a Runtime-owned
// import writer. The wrapper replays every staged event before the Store can
// publish it and registers the Session only after atomic publication succeeds.
func (e *Engine) BeginTransferImport(
	manifest protocol.TransferManifest,
	expectedBinding protocol.Binding,
) (TransferWriter, error) {
	if err := protocol.ValidateTransferManifest(manifest); err != nil {
		return nil, validationError(err)
	}
	if err := protocol.ValidateBinding(expectedBinding); err != nil {
		return nil, validationError(err)
	}
	if manifest.Binding != expectedBinding {
		return nil, NewFieldError(
			"binding_mismatch",
			"transfer does not match the caller's expected game content",
			"expected_binding",
			ErrConflict,
		)
	}
	transferStore, ok := e.store.(TransferStore)
	if !ok {
		return nil, NewError(
			"transfer_unavailable",
			"store does not support atomic transfer import",
			ErrConflict,
		)
	}
	unlockLifecycle := e.lockSessionLifecycle(manifest.SessionID)
	release := true
	defer func() {
		if release {
			unlockLifecycle()
		}
	}()
	e.mu.RLock()
	_, exists := e.sessions[manifest.SessionID]
	_, pending := e.pendingCreates[manifest.SessionID]
	e.mu.RUnlock()
	if exists || pending {
		return nil, NewError(
			"session_exists",
			"session already exists",
			ErrConflict,
		)
	}
	staged, err := transferStore.BeginTransfer(manifest)
	if err != nil {
		if errors.Is(err, ErrRetired) {
			return nil, NewError(
				"session_retired",
				"session id was permanently retired",
				ErrConflict,
			)
		}
		if errors.Is(err, ErrConflict) {
			return nil, NewError(
				"session_exists",
				"session already exists",
				err,
			)
		}
		return nil, NewError(
			"transfer_stage_failed",
			"could not create transfer staging",
			err,
		)
	}
	release = false
	return &runtimeTransferWriter{
		engine:          e,
		manifest:        manifest,
		expectedBinding: expectedBinding,
		staged:          staged,
		identifiers:     newIdentifierHistory(true),
		unlockLifecycle: unlockLifecycle,
	}, nil
}

type runtimeTransferWriter struct {
	mu sync.Mutex

	engine            *Engine
	manifest          protocol.TransferManifest
	expectedBinding   protocol.Binding
	staged            TransferWriter
	state             protocol.SessionState
	identifiers       protocol.IdentifierHistory
	lineageGeneration uint64
	unlockLifecycle   func()
	finished          bool
	failed            error
}

var _ TransferWriter = (*runtimeTransferWriter)(nil)

func (w *runtimeTransferWriter) WriteEvent(
	frame protocol.TransferEvent,
) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.ready(); err != nil {
		return err
	}
	normalizeWritableState(&w.state)
	next, err := applyEvent(w.state, frame.Record)
	if err != nil {
		return w.fail(NewError(
			"transfer_replay_failed",
			"transfer event could not be applied",
			err,
		))
	}
	delta, err := prepareIdentifierEvent(w.identifiers, frame.Record)
	if err != nil {
		return w.fail(NewError(
			"transfer_replay_failed",
			"transfer event identifiers are invalid",
			err,
		))
	}
	if frame.Record.Sequence == 1 &&
		(next.SessionID != w.manifest.SessionID ||
			next.Binding != w.expectedBinding) {
		return w.fail(NewError(
			"binding_mismatch",
			"first transfer event does not match trusted Session identity",
			ErrConflict,
		))
	}
	if err := w.staged.WriteEvent(frame); err != nil {
		return w.fail(NewError(
			"transfer_stage_failed",
			"could not stage transfer event",
			err,
		))
	}
	applyIdentifierDelta(&w.identifiers, delta)
	w.state = next
	if frame.Record.Type == EventSessionRestored &&
		w.lineageGeneration != ^uint64(0) {
		w.lineageGeneration++
	}
	return nil
}

func (w *runtimeTransferWriter) Publish(
	complete protocol.TransferComplete,
) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.ready(); err != nil {
		return err
	}
	if w.state.SessionID != w.manifest.SessionID ||
		w.state.Binding != w.expectedBinding ||
		w.state.Revision != w.manifest.TerminalRevision ||
		w.state.HeadHash != w.manifest.TerminalHeadHash ||
		w.lineageGeneration != w.manifest.LineageGeneration {
		return w.fail(NewError(
			"transfer_replay_failed",
			"replayed transfer does not match its manifest boundary",
			ErrCorruptLog,
		))
	}
	if err := protocol.ValidateSessionState(w.state); err != nil {
		return w.fail(NewError(
			"transfer_replay_failed",
			"replayed transfer state is invalid",
			err,
		))
	}
	if err := protocol.ValidateIdentifierHistory(
		w.identifiers,
		w.manifest.SessionID,
	); err != nil {
		return w.fail(NewError(
			"transfer_replay_failed",
			"replayed transfer identifier history is invalid",
			err,
		))
	}
	if err := validateIdentifiersCoverState(w.identifiers, w.state); err != nil {
		return w.fail(NewError(
			"transfer_replay_failed",
			"replayed transfer identifiers do not cover state",
			err,
		))
	}
	if err := w.staged.Publish(complete); err != nil {
		return w.fail(NewError(
			"transfer_publish_failed",
			"could not publish staged transfer",
			err,
		))
	}
	if err := w.engine.verifySessionFromGenesis(w.manifest.SessionID); err != nil {
		return w.fail(NewError(
			"transfer_replay_failed",
			"published transfer failed genesis verification",
			err,
		))
	}
	managed := &managedSession{
		id:           w.manifest.SessionID,
		loaded:       true,
		state:        w.state,
		identifiers:  w.identifiers,
		lineageEpoch: w.lineageGeneration,
	}
	w.engine.mu.Lock()
	w.engine.sessions[w.manifest.SessionID] = managed
	w.engine.mu.Unlock()
	managed.mu.Lock()
	w.engine.queueCheckpointLocked(managed)
	managed.mu.Unlock()
	w.finish()
	return nil
}

func (w *runtimeTransferWriter) Abort() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.finished {
		return nil
	}
	err := w.staged.Abort()
	w.finish()
	return err
}

func (w *runtimeTransferWriter) ready() error {
	if w.finished {
		return errors.New("transfer import writer is closed")
	}
	if w.failed != nil {
		return errors.Join(
			errors.New("transfer import writer has failed"),
			w.failed,
		)
	}
	return nil
}

func (w *runtimeTransferWriter) fail(err error) error {
	if w.failed == nil {
		w.failed = err
	}
	return err
}

func (w *runtimeTransferWriter) finish() {
	if w.finished {
		return
	}
	w.finished = true
	if w.unlockLifecycle != nil {
		w.unlockLifecycle()
		w.unlockLifecycle = nil
	}
}

func newTransferID() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return "transfer." + hex.EncodeToString(random[:]), nil
}
