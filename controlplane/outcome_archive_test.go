package controlplane

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/policy"
)

func TestSQLiteOutcomeBacklogDoesNotConsumeExecutionCapacity(t *testing.T) {
	now := time.UnixMilli(1000000)
	root := t.TempDir()
	var memoryCalls atomic.Int32
	service, lease, principal, actionHost := openActionPersistenceHarness(t, root, &now, "instance.archive", OpenSQLite, map[string]OutcomeSink{
		"task-plan": outcomeSinkFunc(func(context.Context, OutcomeEvidence) error { return errors.New("projection offline") }),
		"memory":    outcomeSinkFunc(func(context.Context, OutcomeEvidence) error { memoryCalls.Add(1); return nil }),
	})
	service.mu.Lock()
	service.maxOperations = 2
	service.mu.Unlock()
	var first OperationView
	var firstInput SubmitActionInput
	for i := 0; i < 12; i++ {
		input := actionHost.input(fmt.Sprintf("request.archive.%d", i), fmt.Sprintf("action.archive.%d", i))
		input.Request.TaskID = "task.archive"
		operation, err := service.SubmitAction(context.Background(), principal, input)
		if err != nil {
			t.Fatalf("new action %d rejected by backlog: %v", i, err)
		}
		if i == 0 {
			first, firstInput = operation, input
		}
		pollHost(t, service, lease, 1)
		if err := service.AcknowledgeHost("test.host", lease.LeaseID, HostAcknowledgement{OperationID: operation.OperationID, Accepted: true}); err != nil {
			t.Fatal(err)
		}
		if err := service.ReportHostOutcome("test.host", lease.LeaseID, host.ActionOutcome{OperationID: operation.OperationID, Status: host.ActionSucceeded, Summary: "Committed.", Epoch: testEpoch(), WorldSeq: 2, OccurredAt: host.Timepoint{Clock: host.ClockStep, Value: 12}}); err != nil {
			t.Fatal(err)
		}
		waitForOutcomeDelivery(t, service, principal, operation.OperationID, "memory")
	}
	admin := operationPrincipal(ScopeHostAdmin)
	deadline := time.Now().Add(2 * time.Second)
	for {
		health, err := service.OutcomeBacklog(admin)
		if err != nil {
			t.Fatal(err)
		}
		if health.Pending == 12 && len(health.Entries) == 12 && health.Entries[0].Attempts > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("health missing durable failures: %#v", health)
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := service.OutcomeBacklog(principal); !errors.Is(err, ErrForbidden) {
		t.Fatalf("unprivileged diagnostics: %v", err)
	}
	view, err := service.GetOperation(principal, first.OperationID)
	if err != nil || !view.ExecutionConfirmed {
		t.Fatalf("archive lookup: %#v %v", view, err)
	}
	view, err = service.FindActionOperation(principal, firstInput)
	if err != nil || view.OperationID != first.OperationID {
		t.Fatalf("archive recovery lookup: %#v %v", view, err)
	}
	replay, err := service.SubmitAction(context.Background(), principal, firstInput)
	if err != nil || replay.OperationID != first.OperationID {
		t.Fatalf("archive idempotency lost: %#v %v", replay, err)
	}
	service.mu.Lock()
	poolCount := len(service.operations)
	// Artificially delay one existing projection to exercise the management retry.
	_, err = service.operationSQLite.db.Exec(`UPDATE outcome_backlog SET next_attempt_at=? WHERE operation_id=?`, time.Now().Add(time.Hour).UnixMilli(), first.OperationID)
	service.mu.Unlock()
	if poolCount != 2 {
		t.Fatalf("hot pool=%d", poolCount)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RetryOutcomeDelivery(admin, OutcomeRetryInput{OperationID: first.OperationID, Subscriber: "task-plan"}); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	var recoveredCalls atomic.Int32
	opts := fileTestOptions(&now, 4096)
	opts.ActionHost, opts.PolicyEngine = actionGatewayTestComponents(t, host.RiskLow, policy.ProfileOpen)
	opts.OutcomeSinks = map[string]OutcomeSink{
		"task-plan": outcomeSinkFunc(func(context.Context, OutcomeEvidence) error { recoveredCalls.Add(1); return nil }),
		"memory":    outcomeSinkFunc(func(context.Context, OutcomeEvidence) error { memoryCalls.Add(1); return nil }),
	}
	recovered, err := OpenSQLite(root, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	deadline = time.Now().Add(3 * time.Second)
	for {
		health, err := recovered.OutcomeBacklog(admin)
		if err != nil {
			t.Fatal(err)
		}
		if health.Pending == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("backlog did not replay: %#v", health)
		}
		time.Sleep(time.Millisecond)
	}
	if recoveredCalls.Load() != 12 || memoryCalls.Load() != 12 {
		t.Fatalf("per-sink replay: plan=%d memory=%d", recoveredCalls.Load(), memoryCalls.Load())
	}
	if pending, err := recovered.OutcomeProjectionPending(principal, first.OperationID, "task-plan"); err != nil || pending {
		t.Fatalf("archive ACK lost: %v %v", pending, err)
	}
	if _, err := recovered.FindActionOperation(principal, firstInput); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteArchiveAndEvictionCommitAtomically(t *testing.T) {
	now := time.UnixMilli(1000000)
	service, lease, principal, actionHost := openActionPersistenceHarness(t, t.TempDir(), &now, "instance.archive.atomic", OpenSQLite)
	first := commitTestOutcome(t, service, lease, principal, actionHost)
	service.mu.Lock()
	service.maxOperations = 1
	_, err := service.operationSQLite.db.Exec(`CREATE TRIGGER reject_archive BEFORE INSERT ON operation_archive BEGIN SELECT RAISE(ABORT,'injected archive failure'); END`)
	service.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	input := actionHost.input("request.next", "action.next")
	if _, err := service.SubmitAction(context.Background(), principal, input); err == nil {
		t.Fatal("eviction returned success without archive commit")
	}
	if value := readSQLiteOperation(t, service, first.OperationID); value.Outcome == nil {
		t.Fatal("old outcome deleted before archive")
	}
	service.mu.Lock()
	_, err = service.operationSQLite.db.Exec(`DROP TRIGGER reject_archive`)
	service.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SubmitAction(context.Background(), principal, input); err != nil {
		t.Fatal(err)
	}
	if view, err := service.GetOperation(principal, first.OperationID); err != nil || !view.ExecutionConfirmed {
		t.Fatalf("archive lost after retry: %#v %v", view, err)
	}
}

func TestUnknownOutcomeSurvivesRetentionForLateHostReconciliation(t *testing.T) {
	now := time.UnixMilli(1000000)
	service, lease, principal, actionHost := openActionPersistenceHarness(t, t.TempDir(), &now, "instance.unknown.retention", OpenSQLite)
	view, err := service.SubmitAction(context.Background(), principal, actionHost.input("request.unknown", "action.unknown"))
	if err != nil {
		t.Fatal(err)
	}
	pollHost(t, service, lease, 1)
	if err := service.AcknowledgeHost("test.host", lease.LeaseID, HostAcknowledgement{OperationID: view.OperationID, Accepted: true}); err != nil {
		t.Fatal(err)
	}
	service.mu.Lock()
	service.pruneOperationsLocked(now.Add(time.Hour).UnixMilli())
	service.pruneOperationsLocked(now.Add(2 * time.Hour).UnixMilli())
	err = service.persistOperationsLocked()
	retained := service.operations[view.OperationID] != nil
	service.mu.Unlock()
	if err != nil || !retained {
		t.Fatalf("unknown result was lost: retained=%v err=%v", retained, err)
	}
	if err := service.ReportHostOutcome("test.host", lease.LeaseID, host.ActionOutcome{OperationID: view.OperationID, Status: host.ActionSucceeded, Summary: "Late authoritative result.", Epoch: testEpoch(), WorldSeq: 2, OccurredAt: host.Timepoint{Clock: host.ClockStep, Value: 12}}); err != nil {
		t.Fatal(err)
	}
}
