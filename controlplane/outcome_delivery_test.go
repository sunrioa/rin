package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sunrioa/rin/host"
)

type outcomeSinkFunc func(context.Context, OutcomeEvidence) error

func (call outcomeSinkFunc) RecordOutcome(ctx context.Context, value OutcomeEvidence) error {
	return call(ctx, value)
}

func commitTestOutcome(t *testing.T, service *Service, lease HostLease, principal host.Principal, actionHost *actionGatewayHost) OperationView {
	t.Helper()
	input := actionHost.input("request.outbox", "action.outbox")
	input.Request.TaskID = "task.outbox"
	operation, err := service.SubmitAction(context.Background(), principal, input)
	if err != nil {
		t.Fatal(err)
	}
	pollHost(t, service, lease, 1)
	if err := service.AcknowledgeHost("test.host", lease.LeaseID, HostAcknowledgement{OperationID: operation.OperationID, Accepted: true}); err != nil {
		t.Fatal(err)
	}
	if err := service.ReportHostOutcome("test.host", lease.LeaseID, host.ActionOutcome{
		OperationID: operation.OperationID, Status: host.ActionSucceeded, Summary: "Committed Host result.",
		Epoch: testEpoch(), WorldSeq: 2, OccurredAt: host.Timepoint{Clock: host.ClockStep, Value: 12},
	}); err != nil {
		t.Fatal(err)
	}
	return operation
}

func TestOutcomeDeliveryReplaysCrashSnapshotIndependently(t *testing.T) {
	now := time.UnixMilli(1_000_000)
	root := t.TempDir()
	failed := make(chan struct{}, 8)
	var memoryCalls atomic.Int32
	service, lease, principal, actionHost := openActionFileHarness(t, root, &now, "instance.outbox", map[string]OutcomeSink{
		"task-plan": outcomeSinkFunc(func(context.Context, OutcomeEvidence) error {
			select {
			case failed <- struct{}{}:
			default:
			}
			return errors.New("database temporarily unavailable")
		}),
		"memory": outcomeSinkFunc(func(context.Context, OutcomeEvidence) error { memoryCalls.Add(1); return nil }),
	})
	operation := commitTestOutcome(t, service, lease, principal, actionHost)
	select {
	case <-failed:
	case <-time.After(2 * time.Second):
		t.Fatal("failed subscriber was not attempted")
	}
	waitForOutcomeDelivery(t, service, principal, operation.OperationID, "memory")
	pending, err := service.OutcomeProjectionPending(principal, operation.OperationID, "task-plan")
	if err != nil || !pending {
		t.Fatalf("failed delivery was acknowledged: %v %v", pending, err)
	}
	// Read the committed file before graceful Close: this is exactly what a new
	// process would see after a crash, including one ack and one pending sink.
	payload, err := os.ReadFile(filepath.Join(root, operationFileName))
	if err != nil {
		t.Fatal(err)
	}
	crashRoot := t.TempDir()
	if err := os.Chmod(crashRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(crashRoot, operationFileName), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	var planCalls atomic.Int32
	recovered, _, _, _ := openActionFileHarness(t, crashRoot, &now, "instance.outbox.recovered", map[string]OutcomeSink{
		"task-plan": outcomeSinkFunc(func(context.Context, OutcomeEvidence) error { planCalls.Add(1); return nil }),
		"memory":    outcomeSinkFunc(func(context.Context, OutcomeEvidence) error { memoryCalls.Add(1); return nil }),
	})
	waitForOutcomeDelivery(t, recovered, principal, operation.OperationID, "task-plan")
	if planCalls.Load() != 1 || memoryCalls.Load() != 1 {
		t.Fatalf("replayed acknowledged sinks: plan=%d memory=%d", planCalls.Load(), memoryCalls.Load())
	}
	view, err := recovered.GetOperation(principal, operation.OperationID)
	if err != nil || !view.ExecutionConfirmed {
		t.Fatalf("recovery lost authoritative outcome: %v", err)
	}
	if err := recovered.Close(); err != nil {
		t.Fatal(err)
	}
	var state persistedOperations
	payload, err = os.ReadFile(filepath.Join(crashRoot, operationFileName))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(payload, &state); err != nil {
		t.Fatal(err)
	}
	if !state.Operations[0].OutcomeDelivery["task-plan"] || !state.Operations[0].OutcomeDelivery["memory"] {
		t.Fatal("subscriber acknowledgements were not durable")
	}
}

func TestSlowMemoryDoesNotBlockHostReplyOrPlanDelivery(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	service, lease, _, principal, actionHost := actionOperationTestHarness(t, Options{OutcomeSinks: map[string]OutcomeSink{
		"memory": outcomeSinkFunc(func(ctx context.Context, _ OutcomeEvidence) error {
			close(started)
			<-ctx.Done()
			close(stopped)
			return ctx.Err()
		}),
		"task-plan": outcomeSinkFunc(func(context.Context, OutcomeEvidence) error { return nil }),
	}})
	done := make(chan OperationView, 1)
	go func() { done <- commitTestOutcome(t, service, lease, principal, actionHost) }()
	var operation OperationView
	select {
	case operation = <-done:
	case <-time.After(time.Second):
		t.Fatal("Host reply blocked on memory")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("memory delivery never started")
	}
	waitForOutcomeDelivery(t, service, principal, operation.OperationID, "task-plan")
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	default:
		t.Fatal("Close did not stop subscriber")
	}
}

func TestFailedAcknowledgementRetainsOutcomeAndRetries(t *testing.T) {
	now := time.UnixMilli(1_000_000)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	service, lease, principal, actionHost := openActionFileHarness(t, t.TempDir(), &now, "instance.ack", map[string]OutcomeSink{
		"task-plan": outcomeSinkFunc(func(ctx context.Context, _ OutcomeEvidence) error {
			if calls.Add(1) == 1 {
				close(started)
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		}),
	})
	operation := commitTestOutcome(t, service, lease, principal, actionHost)
	<-started
	service.mu.Lock()
	originalPath := service.operationFile.path
	service.operationFile.path = filepath.Join(originalPath, "not-a-directory")
	service.mu.Unlock()
	close(release)
	// Wait until the sink's successful result has encountered the disk error.
	deadline := time.Now().Add(2 * time.Second)
	for {
		service.mu.Lock()
		failed := service.operationDirty && !service.operations[operation.OperationID].outcomeDelivery["task-plan"]
		service.mu.Unlock()
		if failed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("acknowledgement failure did not remain pending")
		}
		time.Sleep(time.Millisecond)
	}
	service.mu.Lock()
	service.pruneOperationsLocked(now.Add(time.Hour).UnixMilli())
	if service.operations[operation.OperationID] == nil {
		t.Error("retention pruned pending delivery")
	}
	service.operationFile.path = originalPath
	service.mu.Unlock()
	waitForOutcomeDelivery(t, service, principal, operation.OperationID, "task-plan")
	if calls.Load() < 2 {
		t.Fatal("failed acknowledgement was not retried")
	}
}

func TestLegacyOutcomeSnapshotReplaysConfiguredSubscribers(t *testing.T) {
	now := time.UnixMilli(1_000_000)
	root := t.TempDir()
	service, lease, principal, actionHost := openActionFileHarness(t, root, &now, "instance.legacy-outcome")
	operation := commitTestOutcome(t, service, lease, principal, actionHost)
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, operationFileName)
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state persistedOperations
	if err := json.Unmarshal(payload, &state); err != nil {
		t.Fatal(err)
	}
	state.Version = legacyOperationFileVersion
	for i := range state.Operations {
		state.Operations[i].OutcomeDelivery = nil
	}
	payload, err = json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	recovered, _, _, _ := openActionFileHarness(t, root, &now, "instance.legacy-recovered", map[string]OutcomeSink{
		"task-plan": outcomeSinkFunc(func(context.Context, OutcomeEvidence) error { calls.Add(1); return nil }),
	})
	waitForOutcomeDelivery(t, recovered, principal, operation.OperationID, "task-plan")
	if calls.Load() != 1 {
		t.Fatalf("legacy outcome delivery count = %d", calls.Load())
	}
}

func TestPersistentOutcomeSubscribersRejectInvalidConfiguration(t *testing.T) {
	sink := outcomeSinkFunc(func(context.Context, OutcomeEvidence) error { return nil })
	tooMany := make(map[string]OutcomeSink)
	for i := 0; i <= maxOutcomeSubscribers; i++ {
		tooMany[fmt.Sprintf("sink.%d", i)] = sink
	}
	cases := map[string]Options{
		"empty name":       {OutcomeSinks: map[string]OutcomeSink{"": sink}},
		"invalid name":     {OutcomeSinks: map[string]OutcomeSink{"has space": sink}},
		"too many":         {OutcomeSinks: tooMany},
		"legacy collision": {OutcomeSink: sink, OutcomeSinks: map[string]OutcomeSink{"default": sink}},
	}
	for name, options := range cases {
		t.Run(name, func(t *testing.T) {
			service, err := OpenFile(t.TempDir(), options)
			if service != nil {
				_ = service.Close()
			}
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid configuration error = %v", err)
			}
		})
	}
}
