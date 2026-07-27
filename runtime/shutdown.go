package runtime

import (
	"context"
	"errors"
)

// Close prevents new Engine operations and waits for in-flight operations,
// transfer imports, and derived checkpoint workers. It does not close the
// caller-owned Store. A timed-out Close leaves the Engine closed; the caller
// may call Close again after releasing the blocked dependency.
func (e *Engine) Close(ctx context.Context) error {
	if ctx == nil {
		return errors.New("runtime close context is required")
	}
	e.shutdownMu.Lock()
	e.closed = true
	e.signalShutdownLocked()
	done := e.shutdownDone
	e.shutdownMu.Unlock()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *Engine) beginOperation() (func(), error) {
	e.shutdownMu.Lock()
	if e.closed {
		e.shutdownMu.Unlock()
		return nil, NewError(
			"runtime_closed",
			"runtime is closed",
			ErrClosed,
		)
	}
	e.activeOperations++
	e.shutdownMu.Unlock()
	return e.finishOperation, nil
}

func (e *Engine) finishOperation() {
	e.shutdownMu.Lock()
	e.activeOperations--
	e.signalShutdownLocked()
	e.shutdownMu.Unlock()
}

func (e *Engine) beginCheckpointWorker() bool {
	e.shutdownMu.Lock()
	defer e.shutdownMu.Unlock()
	if e.closed {
		return false
	}
	e.checkpointWorkers++
	return true
}

func (e *Engine) finishCheckpointWorker() {
	e.shutdownMu.Lock()
	e.checkpointWorkers--
	e.signalShutdownLocked()
	e.shutdownMu.Unlock()
}

func (e *Engine) signalShutdownLocked() {
	if !e.closed || e.shutdownSignaled ||
		e.activeOperations != 0 || e.checkpointWorkers != 0 {
		return
	}
	e.shutdownSignaled = true
	close(e.shutdownDone)
}
