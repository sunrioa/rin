package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sunrioa/rin/policy"
	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
)

func TestFileTransferStagesAndPublishesCompleteLineageAtomically(t *testing.T) {
	manifest, frames, complete := fileTransferFixture(
		t,
		"session.transfer-complete",
		3,
	)
	root := t.TempDir()
	fileStore, err := OpenFile(root)
	if err != nil {
		t.Fatal(err)
	}
	defer fileStore.Close()

	writer, err := fileStore.BeginTransfer(manifest)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Abort()
	target := filepath.Join(root, "sessions", manifest.SessionID)
	for index, frame := range frames {
		if err := writer.WriteEvent(frame); err != nil {
			t.Fatalf("write event %d: %v", index+1, err)
		}
		if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("target became visible before publish: %v", err)
		}
	}
	if err := writer.Publish(complete); err != nil {
		t.Fatal(err)
	}

	events, err := fileStore.Load(manifest.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != len(frames) {
		t.Fatalf("published events = %d, want %d", len(events), len(frames))
	}
	for index := range events {
		if !rinruntime.EventRecordsExactlyEqual(events[index], frames[index].Record) {
			t.Fatalf("published event %d differs from transfer", index+1)
		}
	}
	if _, err := fileStore.BeginTransfer(manifest); !errors.Is(err, rinruntime.ErrConflict) {
		t.Fatalf("existing target error = %v, want conflict", err)
	}
}

func TestFileTransferCorruptionAndTruncationNeverPublish(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T, writer rinruntime.TransferWriter, frames []protocol.TransferEvent, complete protocol.TransferComplete)
	}{
		{
			name: "corrupt event checksum",
			run: func(
				t *testing.T,
				writer rinruntime.TransferWriter,
				frames []protocol.TransferEvent,
				_ protocol.TransferComplete,
			) {
				frames[0].Record.Data = []byte(`{"corrupt":true}`)
				if err := writer.WriteEvent(frames[0]); err == nil {
					t.Fatal("corrupt event was accepted")
				}
			},
		},
		{
			name: "truncated stream",
			run: func(
				t *testing.T,
				writer rinruntime.TransferWriter,
				frames []protocol.TransferEvent,
				complete protocol.TransferComplete,
			) {
				if err := writer.WriteEvent(frames[0]); err != nil {
					t.Fatal(err)
				}
				if err := writer.Publish(complete); err == nil {
					t.Fatal("truncated transfer was published")
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sessionID := "session.transfer-" + strings.ReplaceAll(test.name, " ", "-")
			manifest, frames, complete := fileTransferFixture(t, sessionID, 2)
			root := t.TempDir()
			fileStore, err := OpenFile(root)
			if err != nil {
				t.Fatal(err)
			}
			defer fileStore.Close()
			writer, err := fileStore.BeginTransfer(manifest)
			if err != nil {
				t.Fatal(err)
			}
			test.run(t, writer, frames, complete)
			if err := writer.Abort(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(
				filepath.Join(root, "sessions", sessionID),
			); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed transfer published a target: %v", err)
			}
			entries, err := os.ReadDir(filepath.Join(root, "sessions"))
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".transfer-") {
					t.Fatalf("abort retained staging directory %q", entry.Name())
				}
			}
		})
	}
}

func TestFileOpenCleansAbandonedTransferStaging(t *testing.T) {
	root := t.TempDir()
	fileStore, err := OpenFile(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := fileStore.Close(); err != nil {
		t.Fatal(err)
	}
	abandoned := filepath.Join(
		root,
		"sessions",
		".transfer-session.abandoned-123.tmp",
	)
	if err := os.Mkdir(abandoned, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(abandoned, "events.jsonl"),
		[]byte("partial"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenFile(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := os.Stat(abandoned); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("abandoned transfer staging was not removed: %v", err)
	}
}

func fileTransferFixture(
	t *testing.T,
	sessionID string,
	eventCount int,
) (
	protocol.TransferManifest,
	[]protocol.TransferEvent,
	protocol.TransferComplete,
) {
	t.Helper()
	memory := NewMemory()
	engine, err := rinruntime.Open(memory, policy.Deterministic{})
	if err != nil {
		t.Fatal(err)
	}
	createStep5Session(t, engine, sessionID)
	for revision := 2; revision <= eventCount; revision++ {
		step5Observe(t, engine, sessionID, revision)
	}
	events, err := memory.Load(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	tail := events[len(events)-1]
	manifest := protocol.TransferManifest{
		Type:              protocol.TransferFrameManifest,
		TransferVersion:   protocol.TransferVersion,
		ProtocolVersion:   protocol.Version,
		ProjectionVersion: rinruntime.ReducerProjectionVersion,
		TransferID:        "transfer." + sessionID,
		SessionID:         sessionID,
		Binding: protocol.Binding{
			GameID: "game.test", ContentID: "base", ContentVersion: "1", ContentHash: "hash",
		},
		TerminalRevision:  tail.Sequence,
		TerminalHeadHash:  tail.Hash,
		EventCount:        uint64(len(events)),
		LineageGeneration: 1,
		HashAlgorithm:     protocol.TransferHashAlgorithm,
	}
	hasher := protocol.NewTransferStreamHasher()
	if err := hasher.WriteManifest(manifest); err != nil {
		t.Fatal(err)
	}
	frames := make([]protocol.TransferEvent, 0, len(events))
	for _, event := range events {
		recordHash, err := protocol.TransferEventRecordSHA256(event)
		if err != nil {
			t.Fatal(err)
		}
		frame := protocol.TransferEvent{
			Type:         protocol.TransferFrameEvent,
			Record:       event,
			RecordSHA256: recordHash,
		}
		if err := hasher.WriteEvent(frame); err != nil {
			t.Fatal(err)
		}
		frames = append(frames, frame)
	}
	streamHash, err := hasher.SumSHA256()
	if err != nil {
		t.Fatal(err)
	}
	return manifest, frames, protocol.TransferComplete{
		Type:             protocol.TransferFrameComplete,
		TerminalRevision: manifest.TerminalRevision,
		TerminalHeadHash: manifest.TerminalHeadHash,
		EventCount:       manifest.EventCount,
		StreamSHA256:     streamHash,
	}
}
