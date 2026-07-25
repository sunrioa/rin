package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
)

type fileTransferWriter struct {
	mu sync.Mutex

	store        *File
	manifest     protocol.TransferManifest
	hasher       *protocol.TransferStreamHasher
	target       string
	staging      string
	events       *os.File
	index        *eventIndex
	offset       int64
	previousRev  uint64
	previousHash string
	done         func()
	finished     bool
	failed       error
}

var _ rinruntime.TransferStore = (*File)(nil)
var _ rinruntime.TransferWriter = (*fileTransferWriter)(nil)

func (s *File) BeginTransfer(
	manifest protocol.TransferManifest,
) (rinruntime.TransferWriter, error) {
	if err := protocol.ValidateTransferManifest(manifest); err != nil {
		return nil, err
	}
	if manifest.ProjectionVersion != rinruntime.ReducerProjectionVersion {
		return nil, fmt.Errorf(
			"transfer projection %q is unsupported",
			manifest.ProjectionVersion,
		)
	}
	target, done, err := s.beginSession(manifest.SessionID)
	if err != nil {
		return nil, err
	}
	release := true
	defer func() {
		if release {
			done()
		}
	}()
	if err := s.rejectDurabilityUncertainty(manifest.SessionID); err != nil {
		return nil, err
	}
	if _, err := os.Stat(s.tombstonePath(manifest.SessionID)); err == nil {
		return nil, rinruntime.ErrRetired
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if _, err := os.Stat(target); err == nil {
		return nil, rinruntime.ErrConflict
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	sessions := filepath.Dir(target)
	staging, err := os.MkdirTemp(
		sessions,
		".transfer-"+manifest.SessionID+"-*.tmp",
	)
	if err != nil {
		return nil, err
	}
	events, err := os.OpenFile(
		filepath.Join(staging, "events.jsonl"),
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		0o600,
	)
	if err != nil {
		return nil, errors.Join(err, os.RemoveAll(staging))
	}
	hasher := protocol.NewTransferStreamHasher()
	if err := hasher.WriteManifest(manifest); err != nil {
		return nil, errors.Join(err, events.Close(), os.RemoveAll(staging))
	}
	release = false
	return &fileTransferWriter{
		store:    s,
		manifest: manifest,
		hasher:   hasher,
		target:   target,
		staging:  staging,
		events:   events,
		index:    &eventIndex{},
		done:     done,
	}, nil
}

func (w *fileTransferWriter) WriteEvent(
	frame protocol.TransferEvent,
) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.ready(); err != nil {
		return err
	}
	if err := w.hasher.WriteEvent(frame); err != nil {
		return w.fail(err)
	}
	if err := rinruntime.VerifyEventRecord(
		w.previousRev,
		w.previousHash,
		frame.Record,
	); err != nil {
		return w.fail(err)
	}
	payload, err := encodeEventRecord(frame.Record)
	if err != nil {
		return w.fail(err)
	}
	start := w.offset
	written, err := w.events.Write(payload)
	w.offset += int64(written)
	if err != nil {
		return w.fail(err)
	}
	if written != len(payload) {
		return w.fail(errors.New("short write staging transfer event"))
	}
	w.index.entries = append(w.index.entries, eventIndexEntry{
		Revision:  frame.Record.Sequence,
		Offset:    start,
		EndOffset: w.offset,
		Hash:      frame.Record.Hash,
	})
	w.previousRev = frame.Record.Sequence
	w.previousHash = frame.Record.Hash
	return nil
}

func (w *fileTransferWriter) Publish(
	complete protocol.TransferComplete,
) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.ready(); err != nil {
		return err
	}
	if err := w.hasher.VerifyComplete(complete, w.manifest); err != nil {
		return w.fail(err)
	}
	if err := w.events.Sync(); err != nil {
		return w.fail(err)
	}
	if err := w.events.Close(); err != nil {
		w.events = nil
		return w.fail(err)
	}
	w.events = nil
	if err := writeEventIndex(w.staging, w.index); err != nil {
		return w.fail(err)
	}
	if err := w.store.syncDir(w.staging); err != nil {
		return w.fail(err)
	}
	if err := renameDurably(w.staging, w.target); err != nil {
		return w.fail(err)
	}
	w.staging = ""
	w.store.markSessionDurabilityConfirmed(w.manifest.SessionID)
	w.store.setCachedIndex(w.manifest.SessionID, w.index)
	syncErr := w.store.syncDir(filepath.Dir(w.target))
	w.finish()
	return syncErr
}

func (w *fileTransferWriter) Abort() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.finished {
		return nil
	}
	var result error
	if w.events != nil {
		result = errors.Join(result, w.events.Close())
		w.events = nil
	}
	if w.staging != "" {
		result = errors.Join(result, os.RemoveAll(w.staging))
		w.staging = ""
	}
	w.finish()
	return result
}

func (w *fileTransferWriter) ready() error {
	if w == nil {
		return errors.New("transfer writer is nil")
	}
	if w.finished {
		return errors.New("transfer writer is closed")
	}
	if w.failed != nil {
		return errors.Join(errors.New("transfer writer has failed"), w.failed)
	}
	return nil
}

func (w *fileTransferWriter) fail(err error) error {
	if w.failed == nil {
		w.failed = err
	}
	return err
}

func (w *fileTransferWriter) finish() {
	if w.finished {
		return
	}
	w.finished = true
	if w.done != nil {
		w.done()
		w.done = nil
	}
}
