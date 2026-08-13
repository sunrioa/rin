package cognition

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/sunrioa/rin/internal/privatefile"
)

const maxDecisionRecordSnapshotBytes = 64 << 20

type FileDecisionRecorder struct {
	mu           sync.Mutex
	path         string
	limit        uint32
	lockFile     *os.File
	local        *LocalDecisionRecorder
	closed       bool
	writeBlocked error
}

func OpenFileDecisionRecorder(path string, limit uint32) (*FileDecisionRecorder, error) {
	if path == "" {
		return nil, errors.New("decision record path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := prepareProviderStorePath(
		absolute, ErrTaskStorePersistence, "decision record store",
	); err != nil {
		return nil, err
	}
	lockFile, err := acquireProviderStoreLock(
		absolute+".lock", ErrTaskStoreLocked, ErrTaskStorePersistence, "decision record store",
	)
	if err != nil {
		return nil, err
	}
	recorder := &FileDecisionRecorder{path: absolute, limit: limit, lockFile: lockFile}
	var snapshot DecisionRecordSnapshot
	if err := privatefile.ReadJSON(absolute, maxDecisionRecordSnapshotBytes, &snapshot); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			_ = releaseProviderStoreLock(lockFile)
			return nil, fmt.Errorf("load decision records: %w", err)
		}
		recorder.local, err = NewLocalDecisionRecorder(limit)
		if err == nil {
			snapshot, err = recorder.local.Snapshot(context.Background())
		}
		if err == nil {
			err = privatefile.WriteJSONBounded(absolute, snapshot, maxDecisionRecordSnapshotBytes)
		}
	} else {
		recorder.local, err = RestoreLocalDecisionRecorder(limit, snapshot)
	}
	if err != nil {
		_ = releaseProviderStoreLock(lockFile)
		return nil, fmt.Errorf("initialize decision records: %w", err)
	}
	return recorder, nil
}

func (recorder *FileDecisionRecorder) Append(ctx context.Context, record DecisionRecord) error {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if err := recorder.ready(); err != nil {
		return err
	}
	if err := recorder.local.Append(ctx, record); err != nil {
		return err
	}
	return recorder.persistLocked()
}

func (recorder *FileDecisionRecorder) Snapshot(
	ctx context.Context,
) (DecisionRecordSnapshot, error) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if err := recorder.ready(); err != nil {
		return DecisionRecordSnapshot{}, err
	}
	return recorder.local.Snapshot(ctx)
}

func (recorder *FileDecisionRecorder) Close() error {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if recorder.closed {
		return nil
	}
	recorder.closed = true
	err := releaseProviderStoreLock(recorder.lockFile)
	recorder.lockFile = nil
	return err
}

func (recorder *FileDecisionRecorder) persistLocked() error {
	snapshot, err := recorder.local.Snapshot(context.Background())
	if err != nil {
		return err
	}
	if err := privatefile.WriteJSONBounded(
		recorder.path, snapshot, maxDecisionRecordSnapshotBytes,
	); err != nil {
		recorder.writeBlocked = fmt.Errorf("persist decision records: %w", err)
		return recorder.writeBlocked
	}
	return nil
}

func (recorder *FileDecisionRecorder) ready() error {
	if recorder.closed {
		return ErrTaskStoreClosed
	}
	return recorder.writeBlocked
}
