package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/policy"
)

func TestOperationFileRestoresTerminalActionAndStalesUnacceptedAction(t *testing.T) {
	root := t.TempDir()
	now := time.UnixMilli(1_000_000)
	service, lease, principal, actionHost := openActionFileHarness(
		t,
		root,
		&now,
		"instance.file.first",
	)
	terminal, err := service.SubmitAction(
		context.Background(), principal,
		actionHost.input("request.file.terminal", "action.file.terminal"),
	)
	if err != nil {
		t.Fatalf("SubmitAction terminal: %v", err)
	}
	pollHost(t, service, lease, 1)
	if err := service.AcknowledgeHost(
		"test.host", lease.LeaseID,
		HostAcknowledgement{OperationID: terminal.OperationID, Accepted: true},
	); err != nil {
		t.Fatalf("AcknowledgeHost: %v", err)
	}
	if err := service.ReportHostOutcome(
		"test.host", lease.LeaseID,
		host.ActionOutcome{
			OperationID: terminal.OperationID,
			Status:      host.ActionSucceeded,
			Summary:     "The persisted action completed.",
			Epoch:       testEpoch(),
			WorldSeq:    2,
			OccurredAt:  host.Timepoint{Clock: host.ClockStep, Value: 12},
		},
	); err != nil {
		t.Fatalf("ReportHostOutcome: %v", err)
	}
	unaccepted, err := service.SubmitAction(
		context.Background(), principal,
		actionHost.input("request.file.unaccepted", "action.file.unaccepted"),
	)
	if err != nil {
		t.Fatalf("SubmitAction unaccepted: %v", err)
	}
	if err := service.Close(); err != nil {
		t.Fatalf("Close first service: %v", err)
	}

	restored, _, restoredPrincipal, _ := openActionFileHarness(
		t,
		root,
		&now,
		"instance.file.restored",
	)
	defer restored.Close()
	terminalView, err := restored.GetOperation(restoredPrincipal, terminal.OperationID)
	if err != nil || terminalView.Status != OperationSucceeded ||
		!terminalView.Terminal || !terminalView.ExecutionConfirmed {
		t.Fatalf("restored terminal action = %#v, %v", terminalView, err)
	}
	unacceptedView, err := restored.GetOperation(
		restoredPrincipal,
		unaccepted.OperationID,
	)
	if err != nil || unacceptedView.Status != OperationStale ||
		!unacceptedView.Terminal || unacceptedView.ExecutionConfirmed ||
		unacceptedView.DeliveryAttempts != 0 {
		t.Fatalf("restored unaccepted action = %#v, %v", unacceptedView, err)
	}
}

func TestOperationFilePersistsControllerAndEmergencyStop(t *testing.T) {
	root := t.TempDir()
	now := time.UnixMilli(1_000_000)
	service, _, principal, _ := openActionFileHarness(
		t,
		root,
		&now,
		"instance.file.control",
	)
	controller, err := service.GetController(principal, testActorControlTarget())
	if err != nil {
		t.Fatalf("GetController: %v", err)
	}
	stop, err := service.SetActorEmergencyStop(
		principal,
		testActorControlTarget(),
		true,
	)
	if err != nil {
		t.Fatalf("SetActorEmergencyStop: %v", err)
	}
	if err := service.Close(); err != nil {
		t.Fatalf("Close first service: %v", err)
	}

	restored, _, restoredPrincipal, _ := openActionFileHarness(
		t,
		root,
		&now,
		"instance.file.control.restored",
	)
	defer restored.Close()
	restoredController, err := restored.GetController(
		restoredPrincipal,
		testActorControlTarget(),
	)
	if err != nil || restoredController.LeaseID != controller.LeaseID {
		t.Fatalf("restored controller = %#v, %v", restoredController, err)
	}
	restoredActor, err := restored.GetActor(
		restoredPrincipal,
		"test.host",
		"world.one",
		"actor.one",
	)
	if err != nil || !restoredActor.EmergencyStopped ||
		restoredActor.EmergencyStopRevision != stop.Revision {
		t.Fatalf("restored emergency stop = %#v, %v", restoredActor, err)
	}
}

func TestOperationFilePolicyRevisionCommitRecovery(t *testing.T) {
	root := t.TempDir()
	crashRoot := t.TempDir()
	if err := os.Chmod(crashRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.UnixMilli(1_000_000)
	service, lease, principal, actionHost := openActionFileHarness(
		t,
		root,
		&now,
		"instance.file.policy-update",
	)
	configureActionGatewayBudget(t, service.policyEngine, 1)
	accepted, err := service.SubmitAction(
		context.Background(),
		principal,
		actionHost.input("request.file.policy-update", "action.file.policy-update"),
	)
	if err != nil {
		t.Fatalf("SubmitAction: %v", err)
	}
	pollHost(t, service, lease, 1)
	if err := service.AcknowledgeHost(
		"test.host",
		lease.LeaseID,
		HostAcknowledgement{OperationID: accepted.OperationID, Accepted: true},
	); err != nil {
		t.Fatalf("AcknowledgeHost: %v", err)
	}

	configV2 := service.policyEngine.Config()
	stateV2 := service.policyEngine.SnapshotState()
	if len(stateV2.Usage) != 1 || len(stateV2.Reservations) != 1 {
		t.Fatalf("policy state before update = %#v", stateV2)
	}
	checkpointV2 := readPersistedOperationsForTest(t, root)
	if checkpointV2.PolicyState == nil ||
		checkpointV2.PolicyState.PolicyRevision != configV2.Revision {
		t.Fatalf("checkpoint before update = %#v", checkpointV2.PolicyState)
	}
	crashPayload, err := os.ReadFile(filepath.Join(root, operationFileName))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(crashRoot, operationFileName),
		crashPayload,
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	configV3 := configV2
	configV3.Revision++
	configV3.Budgets[0].MaxActions++
	if err := service.policyEngine.Update(configV3); err != nil {
		t.Fatalf("Policy Update: %v", err)
	}
	stateV3 := service.policyEngine.SnapshotState()
	assertPolicyUsageAndReservations(t, stateV2, stateV3)
	if err := service.Close(); err != nil {
		t.Fatalf("Close after policy update: %v", err)
	}

	cleanCheckpoint := readPersistedOperationsForTest(t, root)
	if cleanCheckpoint.PolicyState == nil ||
		cleanCheckpoint.PolicyState.PolicyRevision != configV3.Revision {
		t.Fatalf("clean-close checkpoint = %#v", cleanCheckpoint.PolicyState)
	}
	cleanEngine, err := policy.New(configV3)
	if err != nil {
		t.Fatal(err)
	}
	cleanOptions := fileTestOptions(&now, 128)
	cleanOptions.ActionHost = actionHost
	cleanOptions.PolicyEngine = cleanEngine
	cleanRestored, err := OpenFile(root, cleanOptions)
	if err != nil {
		t.Fatalf("OpenFile after clean close: %v", err)
	}
	assertRestoredAcceptedOperation(t, cleanRestored, principal, accepted.OperationID)
	assertPolicyUsageAndReservations(t, stateV2, cleanEngine.SnapshotState())
	if err := cleanRestored.Close(); err != nil {
		t.Fatalf("Close clean restore: %v", err)
	}

	crashEngine, err := policy.New(configV3)
	if err != nil {
		t.Fatal(err)
	}
	crashOptions := fileTestOptions(&now, 256)
	crashOptions.ActionHost = actionHost
	crashOptions.PolicyEngine = crashEngine
	crashRestored, err := OpenFile(crashRoot, crashOptions)
	if err != nil {
		t.Fatalf("OpenFile across policy/checkpoint crash window: %v", err)
	}
	if !crashRestored.operationCheckpointDirty {
		t.Fatal("forward-migrated checkpoint was not marked dirty")
	}
	assertRestoredAcceptedOperation(t, crashRestored, principal, accepted.OperationID)
	assertPolicyUsageAndReservations(t, stateV2, crashEngine.SnapshotState())
	if err := crashRestored.Close(); err != nil {
		t.Fatalf("Close crash-window restore: %v", err)
	}
	migratedCheckpoint := readPersistedOperationsForTest(t, crashRoot)
	if migratedCheckpoint.PolicyState == nil ||
		migratedCheckpoint.PolicyState.PolicyRevision != configV3.Revision {
		t.Fatalf("migrated checkpoint = %#v", migratedCheckpoint.PolicyState)
	}

	rollbackEngine, err := policy.New(configV2)
	if err != nil {
		t.Fatal(err)
	}
	rollbackOptions := fileTestOptions(&now, 384)
	rollbackOptions.ActionHost = actionHost
	rollbackOptions.PolicyEngine = rollbackEngine
	if _, err := OpenFile(crashRoot, rollbackOptions); !errors.Is(err, ErrPersistence) {
		t.Fatalf("future checkpoint rollback error = %v", err)
	}
	changedV3 := configV3
	changedV3.Budgets[0].MaxActions++
	changedEngine, err := policy.New(changedV3)
	if err != nil {
		t.Fatal(err)
	}
	changedOptions := fileTestOptions(&now, 512)
	changedOptions.ActionHost = actionHost
	changedOptions.PolicyEngine = changedEngine
	if _, err := OpenFile(crashRoot, changedOptions); !errors.Is(err, ErrPersistence) {
		t.Fatalf("same-revision digest mismatch error = %v", err)
	}
}

func readPersistedOperationsForTest(t *testing.T, root string) persistedOperations {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join(root, operationFileName))
	if err != nil {
		t.Fatal(err)
	}
	var state persistedOperations
	if err := json.Unmarshal(payload, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func assertPolicyUsageAndReservations(t *testing.T, want, got policy.State) {
	t.Helper()
	if !reflect.DeepEqual(got.Usage, want.Usage) ||
		!reflect.DeepEqual(got.Reservations, want.Reservations) {
		t.Fatalf("policy runtime state changed: got=%#v want=%#v", got, want)
	}
}

func assertRestoredAcceptedOperation(
	t *testing.T,
	service *Service,
	principal host.Principal,
	operationID string,
) {
	t.Helper()
	view, err := service.GetOperation(principal, operationID)
	if err != nil || view.Status != OperationAccepted {
		t.Fatalf("restored operation = %#v, %v", view, err)
	}
}

func TestOperationFileRejectsConcurrentWriterAndReleasesLock(t *testing.T) {
	root := t.TempDir()
	first, err := OpenFile(root, Options{})
	if err != nil {
		t.Fatalf("OpenFile first: %v", err)
	}
	if _, err := OpenFile(root, Options{}); !errors.Is(err, ErrDataLocked) {
		t.Fatalf("concurrent OpenFile error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close first: %v", err)
	}
	second, err := OpenFile(root, Options{})
	if err != nil {
		t.Fatalf("OpenFile after release: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("Close second: %v", err)
	}
}

func TestOperationFileRejectsOldOrAmbiguousState(t *testing.T) {
	for _, test := range []struct {
		name    string
		payload string
	}{
		{
			name: "old version",
			payload: `{"version":"rin.control.operations/v4",` +
				`"operations":[]}`,
		},
		{
			name: "duplicate root member",
			payload: `{"version":"` + operationFileVersion + `",` +
				`"version":"` + operationFileVersion + `","operations":[]}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, operationFileName)
			if err := os.WriteFile(path, []byte(test.payload), 0o600); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			if _, err := OpenFile(root, Options{}); !errors.Is(err, ErrPersistence) {
				t.Fatalf("OpenFile error = %v", err)
			}
		})
	}
}

func TestOperationFileWritesOnlyCurrentActionSchema(t *testing.T) {
	root := t.TempDir()
	now := time.UnixMilli(1_000_000)
	service, _, principal, actionHost := openActionFileHarness(
		t,
		root,
		&now,
		"instance.file.schema",
	)
	if _, err := service.SubmitAction(
		context.Background(), principal,
		actionHost.input("request.file.schema", "action.file.schema"),
	); err != nil {
		t.Fatalf("SubmitAction: %v", err)
	}
	if err := service.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	payload, err := os.ReadFile(filepath.Join(root, operationFileName))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Contains(payload, []byte(`"version":"`+operationFileVersion+`"`)) ||
		!bytes.Contains(payload, []byte(`"kind":"action"`)) {
		t.Fatalf("current operation state = %s", payload)
	}
	for _, legacy := range []string{
		`"kind":"message"`,
		`"kind":"directive"`,
		`"kind":"utterance"`,
		`"kind":"offer"`,
		`"turn_id"`,
		`"invocation"`,
	} {
		if strings.Contains(string(payload), legacy) {
			t.Fatalf("operation state retained legacy field %s", legacy)
		}
	}
}

func openActionFileHarness(
	t *testing.T,
	root string,
	now *time.Time,
	instanceID string,
	subscribers ...map[string]OutcomeSink,
) (*Service, HostLease, host.Principal, *actionGatewayHost) {
	t.Helper()
	actionHost, engine := actionGatewayTestComponents(t, host.RiskLow, policy.ProfileOpen)
	random := make([]byte, 4_096)
	for index := range random {
		random[index] = byte(index)
	}
	var sinks map[string]OutcomeSink
	if len(subscribers) != 0 {
		sinks = subscribers[0]
	}
	service, err := OpenFile(root, Options{
		OutcomeSinks: sinks,
		Now:          func() time.Time { return *now },
		Random:       bytes.NewReader(random),
		ActionHost:   actionHost,
		PolicyEngine: engine,
	})
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	lease := mustRegister(t, service, registration(instanceID))
	if err := service.PublishWorld(
		"test.host", lease.LeaseID, worldPublication(1, "ready"),
	); err != nil {
		service.Close()
		t.Fatalf("PublishWorld: %v", err)
	}
	principal := operationPrincipal(
		ScopeActorRead,
		ScopeActorControl,
		ScopeActorExecute,
		ScopeOperationCancel,
	)
	if _, err := service.AcquireController(principal, AcquireControllerInput{
		ActorControlTarget: testActorControlTarget(),
		ControllerID:       "controller.gateway.one",
		LeaseTTLMillis:     5_000,
	}); err != nil {
		service.Close()
		t.Fatalf("AcquireController: %v", err)
	}
	return service, lease, principal, actionHost
}

func registerAndPublishOperationHost(
	t *testing.T,
	service *Service,
	instanceID string,
) HostLease {
	t.Helper()
	lease := mustRegister(t, service, registration(instanceID))
	if err := service.PublishWorld(
		"test.host",
		lease.LeaseID,
		worldPublication(1, "ready"),
	); err != nil {
		t.Fatalf("PublishWorld: %v", err)
	}
	return lease
}

func fileTestOptions(now *time.Time, offset int) Options {
	random := make([]byte, 4_096)
	for index := range random {
		random[index] = byte(index + offset)
	}
	return Options{
		Now:    func() time.Time { return *now },
		Random: bytes.NewReader(random),
	}
}
