package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sunrioa/rin/host"
)

func TestOperationFileRecoversUnknownWorkAndTerminalOutcome(t *testing.T) {
	root := t.TempDir()
	now := time.UnixMilli(1_000_000)
	service, lease := openPublishedOperationService(t, root, &now, 0)
	principal := operationPrincipal(ScopeActorConverse)
	operation, err := service.SendActorMessage(principal, ActorTextInput{
		RequestID: "request.persist.accepted",
		HostID:    "test.host",
		WorldID:   "world.one",
		ActorID:   "actor.one",
		Text:      "Remember this after restart.",
	})
	if err != nil {
		t.Fatalf("SendActorMessage: %v", err)
	}
	if batch := pollHost(t, service, lease, 1); len(batch.Requests) != 1 {
		t.Fatalf("PollHost = %#v", batch)
	}
	if err := service.AcknowledgeHost(
		"test.host",
		lease.LeaseID,
		HostAcknowledgement{OperationID: operation.OperationID, Accepted: true},
	); err != nil {
		t.Fatalf("AcknowledgeHost: %v", err)
	}
	if err := service.ReportHostRun(
		"test.host",
		lease.LeaseID,
		host.ActionRun{
			OperationID: operation.OperationID,
			Status:      host.ActionSucceeded,
			ProgressSeq: 1,
			Progress:    100,
			UpdatedAt:   host.Timepoint{Clock: host.ClockStep, Value: 2},
		},
	); err != nil {
		t.Fatalf("ReportHostRun: %v", err)
	}
	if err := service.Close(); err != nil {
		t.Fatalf("Close before recovery: %v", err)
	}

	recovered, err := OpenFile(root, fileTestOptions(&now, 64))
	if err != nil {
		t.Fatalf("OpenFile after accepted run: %v", err)
	}
	view, err := recovered.GetOperation(principal, operation.OperationID)
	if err != nil ||
		view.Status != OperationOutcomeUnknown ||
		view.Run == nil ||
		view.Run.Status != host.ActionSucceeded {
		t.Fatalf("recovered operation = %#v, %v", view, err)
	}

	recoveryLease := registerAndPublishOperationHost(
		t,
		recovered,
		"instance.recovered",
	)
	outcome := host.ActionOutcome{
		OperationID: operation.OperationID,
		Status:      host.ActionSucceeded,
		Summary:     "Recovered from the Host outcome outbox.",
		Epoch:       testEpoch(),
		WorldSeq:    2,
		OccurredAt:  host.Timepoint{Clock: host.ClockStep, Value: 3},
	}
	output := json.RawMessage(
		`{"type":"actor_turn","reply":"Recovered reply.","capability":"activity.wait"}`,
	)
	if err := recovered.ReportHostResult(
		"test.host",
		recoveryLease.LeaseID,
		outcome,
		output,
	); err != nil {
		t.Fatalf("ReportHostResult after restart: %v", err)
	}
	if err := recovered.Close(); err != nil {
		t.Fatalf("Close recovered service: %v", err)
	}

	reopened, err := OpenFile(root, fileTestOptions(&now, 128))
	if err != nil {
		t.Fatalf("OpenFile after outcome: %v", err)
	}
	view, err = reopened.GetOperation(principal, operation.OperationID)
	if err != nil ||
		view.Status != OperationSucceeded ||
		view.Outcome == nil ||
		view.Outcome.Summary != outcome.Summary ||
		view.Output["reply"] != "Recovered reply." ||
		view.Output["capability"] != "activity.wait" {
		t.Fatalf("reopened operation = %#v, %v", view, err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("Close reopened service: %v", err)
	}

	info, err := os.Stat(filepath.Join(root, operationFileName))
	if err != nil {
		t.Fatalf("Stat operation state: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("operation state mode = %o", info.Mode().Perm())
	}
}

func TestOperationFileRedeliversUnacknowledgedRequestWithStableID(t *testing.T) {
	root := t.TempDir()
	now := time.UnixMilli(1_000_000)
	service, lease := openPublishedOperationService(t, root, &now, 0)
	principal := operationPrincipal(ScopeActorDirect)
	operation, err := service.SendActorDirective(principal, ActorTextInput{
		RequestID: "request.persist.redelivery",
		HostID:    "test.host",
		WorldID:   "world.one",
		ActorID:   "actor.one",
		Text:      "Inspect the entrance.",
	})
	if err != nil {
		t.Fatalf("SendActorDirective: %v", err)
	}
	first := pollHost(t, service, lease, 1)
	if len(first.Requests) != 1 ||
		first.Requests[0].DeliveryAttempt != 1 {
		t.Fatalf("first delivery = %#v", first)
	}
	if err := service.Close(); err != nil {
		t.Fatalf("Close before redelivery: %v", err)
	}

	recovered, err := OpenFile(root, fileTestOptions(&now, 64))
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	view, err := recovered.GetOperation(principal, operation.OperationID)
	if err != nil || view.Status != OperationQueued {
		t.Fatalf("recovered unacknowledged operation = %#v, %v", view, err)
	}
	recoveryLease := registerAndPublishOperationHost(
		t,
		recovered,
		"instance.redelivery",
	)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	second, err := recovered.PollHost(
		ctx,
		"test.host",
		recoveryLease.LeaseID,
		1,
	)
	if err != nil {
		t.Fatalf("PollHost after restart: %v", err)
	}
	if len(second.Requests) != 1 ||
		second.Requests[0].Request.OperationID != operation.OperationID ||
		second.Requests[0].DeliveryAttempt != 2 {
		t.Fatalf("redelivery = %#v", second)
	}
	if err := recovered.Close(); err != nil {
		t.Fatalf("Close recovered service: %v", err)
	}
}

func TestOperationFileRejectsConcurrentWriterAndReleasesLock(t *testing.T) {
	root := t.TempDir()
	now := time.UnixMilli(1_000_000)
	first, err := OpenFile(root, fileTestOptions(&now, 0))
	if err != nil {
		t.Fatalf("OpenFile first writer: %v", err)
	}
	if _, err := OpenFile(root, fileTestOptions(&now, 64)); !errors.Is(err, ErrDataLocked) {
		t.Fatalf("OpenFile concurrent writer error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close first writer: %v", err)
	}
	second, err := OpenFile(root, fileTestOptions(&now, 128))
	if err != nil {
		t.Fatalf("OpenFile after release: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("Close second writer: %v", err)
	}
}

func TestOperationFileRejectsAmbiguousOrInsecureState(t *testing.T) {
	for name, payload := range map[string]string{
		"duplicate": `{"version":"rin.control.operations/v1","operations":[],"operations":[]}`,
		"unknown":   `{"version":"rin.control.operations/v1","operations":[],"extra":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, operationFileName)
			if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			if _, err := OpenFile(
				root,
				Options{},
			); !errors.Is(err, ErrPersistence) {
				t.Fatalf("OpenFile error = %v", err)
			}
		})
	}

	root := t.TempDir()
	path := filepath.Join(root, operationFileName)
	payload := `{"version":"rin.control.operations/v1","operations":[]}`
	if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
		t.Fatalf("WriteFile insecure state: %v", err)
	}
	if _, err := OpenFile(
		root,
		Options{},
	); !errors.Is(err, ErrPersistence) {
		t.Fatalf("insecure OpenFile error = %v", err)
	}
}

func openPublishedOperationService(
	t *testing.T,
	root string,
	now *time.Time,
	randomOffset int,
) (*Service, HostLease) {
	t.Helper()
	service, err := OpenFile(root, fileTestOptions(now, randomOffset))
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	lease := registerAndPublishOperationHost(
		t,
		service,
		"instance.persisted",
	)
	return service, lease
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
