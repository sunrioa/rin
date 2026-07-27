package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sunrioa/rin/protocol"
)

type closeBlockingStore struct {
	Store
	block   atomic.Bool
	started chan struct{}
	release chan struct{}
}

func (store *closeBlockingStore) ListSessions() ([]string, error) {
	if store.block.Load() {
		close(store.started)
		<-store.release
	}
	return store.Store.ListSessions()
}

func TestEngineCloseWaitsForForegroundOperation(t *testing.T) {
	eventStore := &closeBlockingStore{
		Store:   newInvariantStore(),
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	engine, err := Open(eventStore, invariantPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	eventStore.block.Store(true)
	readyResult := make(chan error, 1)
	go func() {
		readyResult <- engine.Ready()
	}()
	waitCheckpointSignal(t, eventStore.started, "Ready did not reach Store")

	closeContext, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := engine.Close(closeContext); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close error = %v, want deadline while Ready is blocked", err)
	}
	close(eventStore.release)
	if err := <-readyResult; err != nil {
		t.Fatalf("in-flight Ready failed after Close: %v", err)
	}
	if err := engine.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestEngineCloseWaitsForCheckpointWorkerAndRejectsNewOperations(t *testing.T) {
	t.Log("recovery_state_cleanup")
	eventStore := newCheckpointWorkerStore(newInvariantStore())
	engine := openCheckpointWorkerEngine(t, eventStore)
	const sessionID = "session.shutdown-checkpoint"
	if _, err := engine.CreateSession(invariantCreate(sessionID, nil, nil)); err != nil {
		t.Fatal(err)
	}
	eventStore.waitSavedRevision(t, 1)
	appendCheckpointObservations(t, engine, sessionID, 255)

	started, release, finished := eventStore.blockRevision(256)
	appendCheckpointObservations(t, engine, sessionID, 256)
	waitCheckpointSignal(t, started, "checkpoint worker did not block")

	closeContext, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := engine.Close(closeContext); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close error = %v, want deadline while checkpoint is blocked", err)
	}
	if _, err := engine.State(protocol.SessionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
	}); !errors.Is(err, ErrClosed) || ErrorCode(err) != "runtime_closed" {
		t.Fatalf("operation after Close error = %v, want runtime_closed", err)
	}

	close(release)
	waitCheckpointSignal(t, finished, "checkpoint worker did not finish")
	if err := engine.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	diagnostics := engine.Diagnostics()
	if !diagnostics.Closed || diagnostics.ActiveOperations != 0 ||
		diagnostics.CheckpointWorkers != 0 {
		t.Fatalf("runtime did not drain: %+v", diagnostics)
	}
}

func TestEngineCloseRejectsNilContext(t *testing.T) {
	engine := openCheckpointWorkerEngine(t, newInvariantStore())
	if err := engine.Close(nil); err == nil {
		t.Fatal("Close accepted a nil context")
	}
	if _, err := engine.CreateSession(
		invariantCreate("session.close-nil-context", nil, nil),
	); err != nil {
		t.Fatalf("invalid Close attempt changed runtime state: %v", err)
	}
}
