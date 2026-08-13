package cognition_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
)

func TestOutcomeMemorySinkProjectsAuthoritativeResultIdempotently(t *testing.T) {
	memory, err := cognition.OpenSQLiteMemoryProvider(
		filepath.Join(t.TempDir(), "memory.db"), cognition.LocalMemoryConfig{},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	sink, err := cognition.NewOutcomeMemorySink(memory)
	if err != nil {
		t.Fatal(err)
	}
	epoch := host.Epoch{
		SessionID: "session.one", WorldID: "world.one", Host: 1, World: 2, Timeline: 3,
	}
	evidence := controlplane.OutcomeEvidence{
		TaskID: "task.one", OperationID: "operation.one",
		HostID: "host.one", WorldID: "world.one", ActorID: "actor.one",
		ControllerID: "external.agent", Capability: host.CapabilityRef{ID: "world.move", Version: "1.0.0"},
		ExpectedEpoch: epoch, ObservationSequence: 8,
		Outcome: host.ActionOutcome{
			OperationID: "operation.one", Status: host.ActionSucceeded,
			Summary: "The Host confirmed movement.", Epoch: epoch, WorldSeq: 9,
			OccurredAt: host.Timepoint{Clock: host.ClockStep, Value: 20},
		},
	}
	for range 2 {
		if err := sink.RecordOutcome(context.Background(), evidence); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := memory.Snapshot(context.Background())
	if err != nil || len(snapshot.Records) != 1 {
		t.Fatalf("snapshot = %#v, %v", snapshot, err)
	}
	record := snapshot.Records[0]
	if record.Provenance.Source != cognition.MemorySourceHostOutcome ||
		!record.Provenance.Authoritative || record.CanonRef == nil ||
		record.CanonRef.HostID != evidence.HostID || record.CanonRef.Sequence != 9 ||
		record.CanonRef.Status != cognition.MemoryCanonCurrent {
		t.Fatalf("projected memory = %#v", record)
	}
}
