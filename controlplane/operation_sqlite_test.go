package controlplane

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/policy"
)

func readSQLiteOperation(t *testing.T, service *Service, id string) persistedOperation {
	t.Helper()
	var payload []byte
	if err := service.operationSQLite.db.QueryRow(`SELECT payload FROM operation_rows WHERE operation_id=?`, id).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var value persistedOperation
	if err := decodeOperationRow(payload, &value); err != nil {
		t.Fatal(err)
	}
	return value
}
func readSQLiteMetadata(t *testing.T, service *Service) persistedOperations {
	t.Helper()
	var payload []byte
	if err := service.operationSQLite.db.QueryRow(`SELECT payload FROM operation_meta WHERE singleton=1`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var state persistedOperations
	if err := decodeOperationRow(payload, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestOperationSQLiteMigratesPolicyAndPendingDeliveryOnce(t *testing.T) {
	now := time.UnixMilli(1000000)
	root := t.TempDir()
	legacy, lease, principal, actionHost := openActionFileHarness(t, root, &now, "instance.sqlite.legacy", map[string]OutcomeSink{"task-plan": outcomeSinkFunc(func(context.Context, OutcomeEvidence) error { return errors.New("temporarily unavailable") })})
	view := commitTestOutcome(t, legacy, lease, principal, actionHost)
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	before := readPersistedOperationsForTest(t, root)
	backup, err := os.ReadFile(filepath.Join(root, operationFileName))
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	opts := fileTestOptions(&now, 4096)
	opts.ActionHost, opts.PolicyEngine = actionGatewayTestComponents(t, host.RiskLow, policy.ProfileOpen)
	opts.OutcomeSinks = map[string]OutcomeSink{"task-plan": outcomeSinkFunc(func(context.Context, OutcomeEvidence) error { calls.Add(1); return nil })}
	service, err := OpenSQLite(root, opts)
	if err != nil {
		t.Fatal(err)
	}
	waitForOutcomeDelivery(t, service, principal, view.OperationID, "task-plan")
	durable := readSQLiteOperation(t, service, view.OperationID)
	if durable.Outcome == nil || !durable.OutcomeDelivery["task-plan"] {
		t.Fatalf("migration lost outcome or acknowledgement: %#v", durable)
	}
	state := readSQLiteMetadata(t, service)
	assertPolicyUsageAndReservations(t, *before.PolicyState, *state.PolicyState)
	if _, err := OpenFile(root, opts); err == nil {
		t.Fatal("legacy writer ran beside SQLite")
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(root, operationFileName))
	if string(after) != string(backup) {
		t.Fatal("JSON backup changed")
	}
	if err := os.WriteFile(filepath.Join(root, operationFileName), []byte("obsolete corrupt backup"), 0600); err != nil {
		t.Fatal(err)
	}
	opts.ActionHost, opts.PolicyEngine = actionGatewayTestComponents(t, host.RiskLow, policy.ProfileOpen)
	reopened, err := OpenSQLite(root, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.GetOperation(principal, view.OperationID); err != nil {
		t.Fatal(err)
	}
	if readSQLiteOperation(t, reopened, view.OperationID).Outcome == nil {
		t.Fatal("SQLite state was replaced by legacy JSON")
	}
	if calls.Load() != 1 {
		t.Fatalf("acknowledged subscriber replayed: %d", calls.Load())
	}
}

func TestOperationSQLiteOutcomePolicyAndOutboxRollbackTogether(t *testing.T) {
	now := time.UnixMilli(1000000)
	root := t.TempDir()
	delivered := make(chan struct{}, 8)
	service, lease, principal, actionHost := openActionPersistenceHarness(t, root, &now, "instance.sqlite", OpenSQLite, map[string]OutcomeSink{"task-plan": outcomeSinkFunc(func(context.Context, OutcomeEvidence) error { delivered <- struct{}{}; return nil })})
	view, err := service.SubmitAction(context.Background(), principal, actionHost.input("request.sqlite", "action.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	pollHost(t, service, lease, 1)
	if err := service.AcknowledgeHost("test.host", lease.LeaseID, HostAcknowledgement{OperationID: view.OperationID, Accepted: true}); err != nil {
		t.Fatal(err)
	}
	before := readSQLiteMetadata(t, service)
	if _, err := service.operationSQLite.db.Exec(`CREATE TRIGGER reject_meta BEFORE UPDATE ON operation_meta BEGIN SELECT RAISE(ABORT,'injected checkpoint failure'); END`); err != nil {
		t.Fatal(err)
	}
	outcome := host.ActionOutcome{OperationID: view.OperationID, Status: host.ActionSucceeded, Summary: "The Host completed the action.", Epoch: testEpoch(), WorldSeq: 2, OccurredAt: host.Timepoint{Clock: host.ClockStep, Value: 12}}
	if err := service.ReportHostOutcome("test.host", lease.LeaseID, outcome); !errors.Is(err, ErrPersistence) {
		t.Fatalf("commit failure = %v", err)
	}
	durable := readSQLiteOperation(t, service, view.OperationID)
	if durable.Outcome != nil || len(durable.OutcomeDelivery) != 0 || durable.Status != OperationAccepted {
		t.Fatalf("partial operation/outbox commit: %#v", durable)
	}
	after := readSQLiteMetadata(t, service)
	assertPolicyUsageAndReservations(t, *before.PolicyState, *after.PolicyState)
	if _, err := service.GetOperation(principal, view.OperationID); !errors.Is(err, ErrPersistence) {
		t.Fatalf("uncommitted result exposed: %v", err)
	}
	select {
	case <-delivered:
		t.Fatal("subscriber observed a failed commit")
	case <-time.After(30 * time.Millisecond):
	}
	if _, err := service.operationSQLite.db.Exec(`DROP TRIGGER reject_meta`); err != nil {
		t.Fatal(err)
	}
	if err := service.ReportHostOutcome("test.host", lease.LeaseID, outcome); err != nil {
		t.Fatal(err)
	}
	waitForOutcomeDelivery(t, service, principal, view.OperationID, "task-plan")
	durable = readSQLiteOperation(t, service, view.OperationID)
	if durable.Outcome == nil || !durable.OutcomeDelivery["task-plan"] {
		t.Fatalf("recovery did not commit result and acknowledgement: %#v", durable)
	}
}

func TestOperationSQLiteUpdatesOnlyChangedRowsAndPersistsControllerStop(t *testing.T) {
	now := time.UnixMilli(1000000)
	root := t.TempDir()
	service, lease, principal, actionHost := openActionPersistenceHarness(t, root, &now, "instance.sqlite.rows", OpenSQLite)
	cold := commitTestOutcome(t, service, lease, principal, actionHost)
	hot, err := service.SubmitAction(context.Background(), principal, actionHost.input("request.hot", "action.hot"))
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{`CREATE TABLE changed_rows(id TEXT)`, `CREATE TRIGGER track_changed AFTER UPDATE ON operation_rows BEGIN INSERT INTO changed_rows VALUES(NEW.operation_id); END`} {
		if _, err := service.operationSQLite.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.CancelOperation(principal, hot.OperationID); err != nil {
		t.Fatal(err)
	}
	var changed int
	if err := service.operationSQLite.db.QueryRow(`SELECT count(*) FROM changed_rows WHERE id=?`, cold.OperationID).Scan(&changed); err != nil {
		t.Fatal(err)
	}
	if changed != 0 {
		t.Fatal("unrelated retained operation was rewritten")
	}
	if err := service.operationSQLite.db.QueryRow(`SELECT count(*) FROM changed_rows WHERE id=?`, hot.OperationID).Scan(&changed); err != nil {
		t.Fatal(err)
	}
	if changed != 1 {
		t.Fatalf("changed operation row writes = %d", changed)
	}
	// A single persistence barrier also includes controller emergency state.
	stop, err := service.SetActorEmergencyStop(principal, testActorControlTarget(), true)
	if err != nil {
		t.Fatal(err)
	}
	if readSQLiteOperation(t, service, hot.OperationID).Status != OperationCancelled {
		t.Fatal("cancel did not persist")
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	opts := fileTestOptions(&now, 4096)
	opts.ActionHost, opts.PolicyEngine = actionGatewayTestComponents(t, host.RiskLow, policy.ProfileOpen)
	reopened, err := OpenSQLite(root, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	state := readSQLiteMetadata(t, reopened)
	if len(state.Controllers) != 1 {
		t.Fatalf("controller lease lost: %#v", state.Controllers)
	}
	if len(state.EmergencyStops) != 1 || state.EmergencyStops[0].Revision != stop.Revision {
		t.Fatalf("emergency stop lost: %#v", state.EmergencyStops)
	}
	if readSQLiteOperation(t, reopened, hot.OperationID).Status != OperationCancelled {
		t.Fatal("cancellation lost on restart")
	}
	// Decode paths remain strict despite the row-oriented representation.
	if err := decodeOperationRow([]byte(`{"version":"rin.control.operations/v6","unexpected":true}`), &persistedOperations{}); err == nil {
		t.Fatal("unknown metadata field accepted")
	}
}
