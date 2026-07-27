package runtime_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sunrioa/rin/policy"
	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
	"github.com/sunrioa/rin/store"
)

type collectingTransferSink struct {
	manifest   protocol.TransferManifest
	events     []protocol.TransferEvent
	complete   protocol.TransferComplete
	onManifest func() error
}

func (s *collectingTransferSink) WriteManifest(
	manifest protocol.TransferManifest,
) error {
	s.manifest = manifest
	if s.onManifest != nil {
		return s.onManifest()
	}
	return nil
}

func (s *collectingTransferSink) WriteEvent(
	frame protocol.TransferEvent,
) error {
	s.events = append(s.events, frame)
	return nil
}

func (s *collectingTransferSink) WriteComplete(
	complete protocol.TransferComplete,
) error {
	s.complete = complete
	return nil
}

func TestTransferRoundTripUsesImmutableExportBoundary(t *testing.T) {
	sourceStore := store.NewMemory()
	source := transferEngine(t, sourceStore)
	const sessionID = "session.transfer-roundtrip"
	createTransferSession(t, source, sessionID)
	observeTransferSession(t, source, sessionID, 2)

	sink := &collectingTransferSink{
		onManifest: func() error {
			observeTransferSession(t, source, sessionID, 3)
			return nil
		},
	}
	if err := source.ExportTransfer(
		context.Background(),
		protocol.SessionRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       sessionID,
		},
		sink,
	); err != nil {
		t.Fatal(err)
	}
	if sink.manifest.TerminalRevision != 2 ||
		sink.manifest.EventCount != 2 ||
		len(sink.events) != 2 {
		t.Fatalf(
			"export boundary revision=%d count=%d frames=%d, want 2",
			sink.manifest.TerminalRevision,
			sink.manifest.EventCount,
			len(sink.events),
		)
	}
	current, err := source.State(protocol.SessionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if current.Revision != 3 {
		t.Fatalf("concurrent source revision = %d, want 3", current.Revision)
	}

	targetStore, err := store.OpenFile(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer targetStore.Close()
	target := transferEngine(t, targetStore)
	writer, err := target.BeginTransferImport(
		sink.manifest,
		sink.manifest.Binding,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Abort()
	for _, frame := range sink.events {
		if err := writer.WriteEvent(frame); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Publish(sink.complete); err != nil {
		t.Fatal(err)
	}
	imported, err := target.State(protocol.SessionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if imported.Revision != sink.manifest.TerminalRevision ||
		imported.HeadHash != sink.manifest.TerminalHeadHash ||
		imported.Binding != sink.manifest.Binding {
		t.Fatalf("imported state does not match export boundary: %+v", imported)
	}

	reopened := transferEngine(t, targetStore)
	replayed, err := reopened.State(protocol.SessionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Revision != imported.Revision ||
		replayed.HeadHash != imported.HeadHash {
		t.Fatalf("genesis replay differs after import: %+v != %+v", replayed, imported)
	}
}

func TestTransferImportRejectsWrongBindingAndLineageGeneration(t *testing.T) {
	sourceStore := store.NewMemory()
	source := transferEngine(t, sourceStore)
	const sessionID = "session.transfer-reject"
	createTransferSession(t, source, sessionID)
	sink := &collectingTransferSink{}
	if err := source.ExportTransfer(
		context.Background(),
		protocol.SessionRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       sessionID,
		},
		sink,
	); err != nil {
		t.Fatal(err)
	}

	targetStore, err := store.OpenFile(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer targetStore.Close()
	target := transferEngine(t, targetStore)
	wrongBinding := sink.manifest.Binding
	wrongBinding.ContentVersion = "wrong"
	if _, err := target.BeginTransferImport(
		sink.manifest,
		wrongBinding,
	); rinruntime.ErrorCode(err) != "binding_mismatch" {
		t.Fatalf("wrong binding error = %v", err)
	}

	manifest := sink.manifest
	manifest.LineageGeneration++
	complete := transferCompleteFor(t, manifest, sink.events)
	writer, err := target.BeginTransferImport(manifest, manifest.Binding)
	if err != nil {
		t.Fatal(err)
	}
	for _, frame := range sink.events {
		if err := writer.WriteEvent(frame); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Publish(complete); rinruntime.ErrorCode(err) != "transfer_replay_failed" {
		t.Fatalf("wrong lineage generation error = %v", err)
	}
	if err := writer.Abort(); err != nil {
		t.Fatal(err)
	}
	if _, err := target.State(protocol.SessionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
	}); !errors.Is(err, rinruntime.ErrNotFound) {
		t.Fatalf("rejected import became visible: %v", err)
	}
}

func TestTransferRequiresBoundedAndAtomicStoreCapabilities(t *testing.T) {
	legacy := &transferLegacyStore{Store: store.NewMemory()}
	engine := transferEngine(t, legacy)
	const sessionID = "session.transfer-unavailable"
	createTransferSession(t, engine, sessionID)
	err := engine.ExportTransfer(
		context.Background(),
		protocol.SessionRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       sessionID,
		},
		&collectingTransferSink{},
	)
	if rinruntime.ErrorCode(err) != "transfer_unavailable" {
		t.Fatalf("legacy export error = %v", err)
	}

	manifest := protocol.TransferManifest{
		Type:              protocol.TransferFrameManifest,
		TransferVersion:   protocol.TransferVersion,
		ProtocolVersion:   protocol.Version,
		ProjectionVersion: rinruntime.ReducerProjectionVersion,
		TransferID:        "transfer.unavailable",
		SessionID:         "session.import-unavailable",
		Binding: protocol.Binding{
			GameID:         "game.transfer",
			ContentID:      "content.transfer",
			ContentVersion: "1",
			ContentHash:    strings.Repeat("a", 64),
		},
		TerminalRevision: 1,
		TerminalHeadHash: strings.Repeat("b", 64),
		EventCount:       1,
		HashAlgorithm:    protocol.TransferHashAlgorithm,
	}
	if _, err := engine.BeginTransferImport(
		manifest,
		manifest.Binding,
	); rinruntime.ErrorCode(err) != "transfer_unavailable" {
		t.Fatalf("legacy import error = %v", err)
	}
}

func TestTransferExportStopsAfterCancellation(t *testing.T) {
	sourceStore := store.NewMemory()
	source := transferEngine(t, sourceStore)
	const sessionID = "session.transfer-cancel"
	createTransferSession(t, source, sessionID)
	observeTransferSession(t, source, sessionID, 2)
	ctx, cancel := context.WithCancel(context.Background())
	sink := &collectingTransferSink{onManifest: func() error {
		cancel()
		return nil
	}}
	err := source.ExportTransfer(
		ctx,
		protocol.SessionRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       sessionID,
		},
		sink,
	)
	if rinruntime.ErrorCode(err) != "transfer_cancelled" {
		t.Fatalf("cancelled export error = %v", err)
	}
	if len(sink.events) != 0 || sink.complete.Type != "" {
		t.Fatal("cancelled export emitted events or a complete frame")
	}
}

func TestTransferImportHardQuotaFailsBeforePublication(t *testing.T) {
	sourceStore := store.NewMemory()
	source := transferEngine(t, sourceStore)
	const sessionID = "session.transfer-quota"
	createTransferSession(t, source, sessionID)
	sink := &collectingTransferSink{}
	if err := source.ExportTransfer(
		context.Background(),
		protocol.SessionRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       sessionID,
		},
		sink,
	); err != nil {
		t.Fatal(err)
	}

	targetStore, err := store.OpenFile(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer targetStore.Close()
	target, err := rinruntime.OpenWithOptions(
		targetStore,
		policy.Deterministic{},
		rinruntime.EngineOptions{SessionHardLimitBytes: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := target.BeginTransferImport(
		sink.manifest,
		sink.manifest.Binding,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteEvent(sink.events[0]); rinruntime.ErrorCode(err) != "session_quota_exceeded" {
		t.Fatalf("transfer quota error = %v", err)
	}
	if err := writer.Abort(); err != nil {
		t.Fatal(err)
	}
	if _, err := target.State(protocol.SessionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
	}); !errors.Is(err, rinruntime.ErrNotFound) {
		t.Fatalf("quota-rejected import became visible: %v", err)
	}
}

func TestEngineCloseWaitsForTransferImportAbort(t *testing.T) {
	source := transferEngine(t, store.NewMemory())
	const sessionID = "session.transfer-close"
	createTransferSession(t, source, sessionID)
	sink := &collectingTransferSink{}
	if err := source.ExportTransfer(
		context.Background(),
		protocol.SessionRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       sessionID,
		},
		sink,
	); err != nil {
		t.Fatal(err)
	}

	targetStore, err := store.OpenFile(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer targetStore.Close()
	target := transferEngine(t, targetStore)
	writer, err := target.BeginTransferImport(sink.manifest, sink.manifest.Binding)
	if err != nil {
		t.Fatal(err)
	}

	closeContext, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := target.Close(closeContext); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close error = %v, want deadline for retained import writer", err)
	}
	if err := writer.Abort(); err != nil {
		t.Fatal(err)
	}
	if err := target.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

type transferLegacyStore struct {
	rinruntime.Store
}

func transferEngine(t *testing.T, eventStore rinruntime.Store) *rinruntime.Engine {
	t.Helper()
	engine, err := rinruntime.Open(eventStore, policy.Deterministic{})
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func createTransferSession(
	t *testing.T,
	engine *rinruntime.Engine,
	sessionID string,
) {
	t.Helper()
	_, err := engine.CreateSession(protocol.CreateSessionRequest{
		ProtocolVersion: protocol.Version,
		RequestID:       "create." + sessionID,
		SessionID:       sessionID,
		Binding: protocol.Binding{
			GameID:         "game.transfer",
			ContentID:      "content.transfer",
			ContentVersion: "1",
			ContentHash:    strings.Repeat("a", 64),
		},
		Features: protocol.RecommendedFeatures(),
		Actors: []protocol.ActorSeed{{
			ID: "npc.transfer", Kind: "npc", DisplayName: "Transfer",
			ThinkEveryTicks: 1, Enabled: true,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func observeTransferSession(
	t *testing.T,
	engine *rinruntime.Engine,
	sessionID string,
	revision int64,
) {
	t.Helper()
	_, err := engine.Observe(protocol.ObserveRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
		RequestID:       "observe." + sessionID + "." + strconv.FormatInt(revision, 10),
		EventID:         "event." + sessionID + "." + strconv.FormatInt(revision, 10),
		Tick:            revision,
		ObserverIDs:     []string{"npc.transfer"},
		Source:          "game",
		Kind:            "world",
		Summary:         "Transfer boundary event.",
		Importance:      2,
		Epoch: protocol.Epoch{
			SessionID: sessionID,
			WorldID:   "world.transfer",
			Host:      1,
			World:     1,
			Timeline:  1,
		},
		ObservationSeq: uint64(revision),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func transferCompleteFor(
	t *testing.T,
	manifest protocol.TransferManifest,
	events []protocol.TransferEvent,
) protocol.TransferComplete {
	t.Helper()
	hasher := protocol.NewTransferStreamHasher()
	if err := hasher.WriteManifest(manifest); err != nil {
		t.Fatal(err)
	}
	for _, frame := range events {
		if err := hasher.WriteEvent(frame); err != nil {
			t.Fatal(err)
		}
	}
	digest, err := hasher.SumSHA256()
	if err != nil {
		t.Fatal(err)
	}
	return protocol.TransferComplete{
		Type:             protocol.TransferFrameComplete,
		TerminalRevision: manifest.TerminalRevision,
		TerminalHeadHash: manifest.TerminalHeadHash,
		EventCount:       manifest.EventCount,
		StreamSHA256:     digest,
	}
}
