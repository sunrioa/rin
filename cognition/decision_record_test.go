package cognition

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sunrioa/rin/host"
)

func TestLocalDecisionRecorderBoundsAndDeduplicates(t *testing.T) {
	recorder, err := NewLocalDecisionRecorder(2)
	if err != nil {
		t.Fatal(err)
	}
	first := validDecisionRecord("record.one", 1)
	if err := recorder.Append(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Append(context.Background(), first); err != nil {
		t.Fatalf("idempotent append failed: %v", err)
	}
	conflict := first
	conflict.DecisionSummary = "A different decision."
	if err := recorder.Append(context.Background(), conflict); !errors.Is(err, ErrDecisionRecordConflict) {
		t.Fatalf("conflicting append error = %v", err)
	}
	if err := recorder.Append(context.Background(), validDecisionRecord("record.two", 2)); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Append(context.Background(), validDecisionRecord("record.three", 3)); err != nil {
		t.Fatal(err)
	}
	snapshot, err := recorder.Snapshot(context.Background())
	if err != nil || len(snapshot.Records) != 2 || snapshot.Records[0].RecordID != "record.two" ||
		snapshot.Records[1].RecordID != "record.three" {
		t.Fatalf("bounded snapshot = %#v, %v", snapshot, err)
	}
	snapshot.Records[0].DecisionSummary = "mutated"
	again, err := recorder.Snapshot(context.Background())
	if err != nil || again.Records[0].DecisionSummary == "mutated" {
		t.Fatal("decision recorder returned aliased records")
	}
}

func TestFileDecisionRecorderPersistsPrivateBoundedSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decision-records.json")
	recorder, err := OpenFileDecisionRecorder(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := recorder.Append(context.Background(), validDecisionRecord("record.one", 1)); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenFileDecisionRecorder(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	snapshot, err := reopened.Snapshot(context.Background())
	if err != nil || len(snapshot.Records) != 1 || snapshot.Records[0].RecordID != "record.one" {
		t.Fatalf("restored snapshot = %#v, %v", snapshot, err)
	}
}

func validDecisionRecord(recordID string, sequence uint64) DecisionRecord {
	digest := "sha256:" + strings.Repeat("a", 64)
	return DecisionRecord{
		RecordID: recordID, TaskID: "task.test", SessionID: "session.test",
		ActorID: "actor.test", ControllerID: "controller.test", Step: uint32(sequence),
		OccurredAtUnixMilli: 1_000, ObservationID: "observation.test",
		ObservationSequence: sequence,
		Epoch: host.Epoch{
			SessionID: "session.test", WorldID: "world.test", Host: 1, World: 1, Timeline: 1,
		},
		GoalDigest: digest, PersonaID: "persona.test", PersonaVersion: "v1",
		PersonaDigest: digest, CapabilityDigest: digest, ContextDigest: digest,
		DecisionKind: ModelDecisionWait, DecisionSummary: "Wait for a clearer signal.",
	}
}
