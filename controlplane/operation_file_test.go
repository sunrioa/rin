package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
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
		view.Terminal ||
		!view.ReconciliationPending ||
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
		!view.Terminal ||
		view.ReconciliationPending ||
		!view.ExecutionConfirmed ||
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
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("operation state mode = %o", info.Mode().Perm())
	}
}

func TestOperationFilePersistsControllerLeaseAndEmergencyStop(t *testing.T) {
	root := t.TempDir()
	now := time.UnixMilli(1_000_000)
	service, _ := openPublishedOperationService(t, root, &now, 0)
	principal := operationPrincipal(ScopeActorRead, ScopeActorControl)
	controller, err := service.AcquireController(
		principal,
		AcquireControllerInput{
			ActorControlTarget: testActorControlTarget(),
			ControllerID:       "controller.persisted.one",
			LeaseTTLMillis:     5_000,
		},
	)
	if err != nil {
		t.Fatalf("AcquireController: %v", err)
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
		t.Fatalf("Close: %v", err)
	}

	recovered, err := OpenFile(root, fileTestOptions(&now, 64))
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	registerAndPublishOperationHost(t, recovered, "instance.controller.recovered")
	restored, err := recovered.GetController(principal, testActorControlTarget())
	if err != nil || restored.LeaseID != controller.LeaseID {
		t.Fatalf("restored controller = %#v, %v", restored, err)
	}
	actor, err := recovered.GetActor(
		principal,
		"test.host",
		"world.one",
		"actor.one",
	)
	if err != nil || !actor.EmergencyStopped ||
		actor.EmergencyStopRevision != stop.Revision {
		t.Fatalf("restored emergency stop = %#v, %v", actor, err)
	}
	now = now.Add(5_001 * time.Millisecond)
	if err := recovered.Close(); err != nil {
		t.Fatalf("Close recovered: %v", err)
	}

	expired, err := OpenFile(root, fileTestOptions(&now, 128))
	if err != nil {
		t.Fatalf("OpenFile expired: %v", err)
	}
	defer expired.Close()
	registerAndPublishOperationHost(t, expired, "instance.controller.expired")
	if _, err := expired.GetController(
		principal,
		testActorControlTarget(),
	); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired controller error = %v", err)
	}
	actor, err = expired.GetActor(
		principal,
		"test.host",
		"world.one",
		"actor.one",
	)
	if err != nil || !actor.EmergencyStopped {
		t.Fatalf("persistent emergency stop = %#v, %v", actor, err)
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

func TestOperationFilePreservesOfferPlanningAcrossRestart(t *testing.T) {
	root := t.TempDir()
	now := time.UnixMilli(1_000_000)
	service, err := OpenFile(root, fileTestOptions(&now, 0))
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	lease := mustRegister(t, service, registration("instance.planning"))
	publication := worldPublication(1, "ready")
	want := &host.ActionPlanMetadata{
		Intent:         "Collect nearby logs",
		PlanID:         "plan.collect.logs",
		StepIndex:      2,
		PlanRevision:   4,
		Preconditions:  []string{"actor ready", "tool available"},
		Postconditions: []string{"logs collected"},
		BlockedReason:  "",
		Risk:           host.RiskModerate,
	}
	publication.Actors[0].Offers[0].Planning = want
	if err := service.PublishWorld(
		"test.host",
		lease.LeaseID,
		publication,
	); err != nil {
		t.Fatalf("PublishWorld: %v", err)
	}
	principal := operationPrincipal(ScopeActorExecute)
	operation, err := service.ExecuteActorOffer(principal, ExecuteOfferInput{
		RequestID: "request.persist.planning",
		HostID:    "test.host",
		WorldID:   "world.one",
		ActorID:   "actor.one",
		OfferID:   "offer.follow",
	})
	if err != nil {
		t.Fatalf("ExecuteActorOffer: %v", err)
	}
	if err := service.Close(); err != nil {
		t.Fatalf("Close before recovery: %v", err)
	}

	recovered, err := OpenFile(root, fileTestOptions(&now, 64))
	if err != nil {
		t.Fatalf("OpenFile after recovery: %v", err)
	}
	recoveryLease := mustRegister(t, recovered, registration("instance.planning.recovered"))
	if err := recovered.PublishWorld(
		"test.host",
		recoveryLease.LeaseID,
		publication,
	); err != nil {
		t.Fatalf("PublishWorld after recovery: %v", err)
	}
	batch := pollHost(t, recovered, recoveryLease, 1)
	if len(batch.Requests) != 1 ||
		batch.Requests[0].Request.OperationID != operation.OperationID ||
		batch.Requests[0].Request.Offer == nil ||
		!reflect.DeepEqual(batch.Requests[0].Request.Offer.Planning, want) {
		t.Fatalf("recovered request = %#v", batch)
	}
	if err := recovered.Close(); err != nil {
		t.Fatalf("Close recovered service: %v", err)
	}
}

func TestOperationFileRedeliversAcceptedRequestWithoutExecutionEvidence(
	t *testing.T,
) {
	root := t.TempDir()
	now := time.UnixMilli(1_000_000)
	service, lease := openPublishedOperationService(t, root, &now, 0)
	principal := operationPrincipal(ScopeActorDirect)
	operation, err := service.SendActorDirective(principal, ActorTextInput{
		RequestID: "request.persist.accepted.redelivery",
		HostID:    "test.host",
		WorldID:   "world.one",
		ActorID:   "actor.one",
		Text:      "Resume the durable request after reconnect.",
	})
	if err != nil {
		t.Fatalf("SendActorDirective: %v", err)
	}
	first := pollHost(t, service, lease, 1)
	if len(first.Requests) != 1 {
		t.Fatalf("first delivery = %#v", first)
	}
	ack := HostAcknowledgement{
		OperationID: operation.OperationID,
		Accepted:    true,
	}
	if err := service.AcknowledgeHost("test.host", lease.LeaseID, ack); err != nil {
		t.Fatalf("AcknowledgeHost: %v", err)
	}
	if err := service.Close(); err != nil {
		t.Fatalf("Close before recovery: %v", err)
	}

	recovered, err := OpenFile(root, fileTestOptions(&now, 64))
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer recovered.Close()
	view, err := recovered.GetOperation(principal, operation.OperationID)
	if err != nil || view.Status != OperationAccepted || view.Terminal ||
		view.ReconciliationPending || view.ExecutionConfirmed {
		t.Fatalf("recovered accepted operation = %#v, %v", view, err)
	}
	recoveryLease := registerAndPublishOperationHost(
		t,
		recovered,
		"instance.accepted.redelivery",
	)
	second := pollHost(t, recovered, recoveryLease, 1)
	if len(second.Requests) != 1 ||
		second.Requests[0].Request.OperationID != operation.OperationID ||
		second.Requests[0].DeliveryAttempt != 2 {
		t.Fatalf("accepted redelivery = %#v", second)
	}
	if err := recovered.AcknowledgeHost(
		"test.host",
		recoveryLease.LeaseID,
		ack,
	); err != nil {
		t.Fatalf("idempotent AcknowledgeHost after redelivery: %v", err)
	}
}

func TestOperationFileDoesNotRedeliverExpiredRequestAfterRestart(t *testing.T) {
	root := t.TempDir()
	now := time.UnixMilli(1_000_000)
	options := fileTestOptions(&now, 0)
	options.OperationTTL = time.Second
	service, err := OpenFile(root, options)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	registerAndPublishOperationHost(t, service, "instance.expiring")
	principal := operationPrincipal(ScopeActorDirect)
	operation, err := service.SendActorDirective(principal, ActorTextInput{
		RequestID: "request.persist.expired",
		HostID:    "test.host",
		WorldID:   "world.one",
		ActorID:   "actor.one",
		Text:      "Do not revive this after its retention window.",
	})
	if err != nil {
		t.Fatalf("SendActorDirective: %v", err)
	}
	if err := service.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	now = now.Add(2 * time.Second)
	options = fileTestOptions(&now, 64)
	options.OperationTTL = time.Second
	recovered, err := OpenFile(root, options)
	if err != nil {
		t.Fatalf("OpenFile after expiry: %v", err)
	}
	defer recovered.Close()
	recoveryLease := registerAndPublishOperationHost(
		t,
		recovered,
		"instance.expired.recovery",
	)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	batch, err := recovered.PollHost(
		ctx,
		"test.host",
		recoveryLease.LeaseID,
		1,
	)
	if !errors.Is(err, context.DeadlineExceeded) || len(batch.Requests) != 0 {
		t.Fatalf("expired delivery = %#v, %v", batch, err)
	}
	view, err := recovered.GetOperation(principal, operation.OperationID)
	if err != nil || view.Status != OperationStale || !view.Terminal ||
		view.DeliveryAttempts != 0 {
		t.Fatalf("expired operation = %#v, %v", view, err)
	}
}

func TestOperationFileDoesNotReviveExpiredAcceptedRequest(t *testing.T) {
	root := t.TempDir()
	now := time.UnixMilli(1_000_000)
	options := fileTestOptions(&now, 0)
	options.OperationTTL = time.Second
	service, err := OpenFile(root, options)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	lease := registerAndPublishOperationHost(t, service, "instance.accepted.expiring")
	principal := operationPrincipal(ScopeActorConverse)
	operation, err := service.SendActorMessage(principal, ActorTextInput{
		RequestID: "request.persist.accepted.expired",
		HostID:    "test.host",
		WorldID:   "world.one",
		ActorID:   "actor.one",
		Text:      "Do not revive this accepted request after expiry.",
	})
	if err != nil {
		t.Fatalf("SendActorMessage: %v", err)
	}
	pollHost(t, service, lease, 1)
	if err := service.AcknowledgeHost(
		"test.host",
		lease.LeaseID,
		HostAcknowledgement{OperationID: operation.OperationID, Accepted: true},
	); err != nil {
		t.Fatalf("AcknowledgeHost: %v", err)
	}
	now = now.Add(2 * time.Second)
	view, err := service.GetOperation(principal, operation.OperationID)
	if err != nil || view.Status != OperationOutcomeUnknown ||
		!view.ReconciliationPending {
		t.Fatalf("expired accepted operation = %#v, %v", view, err)
	}
	if err := service.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	options = fileTestOptions(&now, 64)
	options.OperationTTL = time.Second
	recovered, err := OpenFile(root, options)
	if err != nil {
		t.Fatalf("OpenFile after expiry: %v", err)
	}
	defer recovered.Close()
	view, err = recovered.GetOperation(principal, operation.OperationID)
	if err != nil || view.Status != OperationOutcomeUnknown ||
		view.Terminal || !view.ReconciliationPending {
		t.Fatalf("recovered expired accepted operation = %#v, %v", view, err)
	}
	recoveryLease := registerAndPublishOperationHost(
		t,
		recovered,
		"instance.accepted.expired.recovery",
	)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	batch, err := recovered.PollHost(
		ctx,
		"test.host",
		recoveryLease.LeaseID,
		1,
	)
	if !errors.Is(err, context.DeadlineExceeded) || len(batch.Requests) != 0 {
		t.Fatalf("revived accepted delivery = %#v, %v", batch, err)
	}
}

func TestOperationFileDoesNotRevivePersistedStaleRequest(t *testing.T) {
	root := t.TempDir()
	now := time.UnixMilli(1_000_000)
	options := fileTestOptions(&now, 0)
	options.OperationTTL = time.Second
	service, err := OpenFile(root, options)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	registerAndPublishOperationHost(t, service, "instance.stale.expiring")
	principal := operationPrincipal(ScopeActorDirect)
	operation, err := service.SendActorDirective(principal, ActorTextInput{
		RequestID: "request.persist.stale",
		HostID:    "test.host",
		WorldID:   "world.one",
		ActorID:   "actor.one",
		Text:      "Remain stale after restart.",
	})
	if err != nil {
		t.Fatalf("SendActorDirective: %v", err)
	}
	now = now.Add(2 * time.Second)
	view, err := service.GetOperation(principal, operation.OperationID)
	if err != nil || view.Status != OperationStale || !view.Terminal {
		t.Fatalf("stale operation = %#v, %v", view, err)
	}
	if err := service.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	options = fileTestOptions(&now, 64)
	options.OperationTTL = time.Second
	recovered, err := OpenFile(root, options)
	if err != nil {
		t.Fatalf("OpenFile after stale persistence: %v", err)
	}
	defer recovered.Close()
	view, err = recovered.GetOperation(principal, operation.OperationID)
	if err != nil || view.Status != OperationStale || !view.Terminal ||
		view.DeliveryAttempts != 0 {
		t.Fatalf("recovered stale operation = %#v, %v", view, err)
	}
	recoveryLease := registerAndPublishOperationHost(
		t,
		recovered,
		"instance.stale.recovery",
	)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	batch, err := recovered.PollHost(
		ctx,
		"test.host",
		recoveryLease.LeaseID,
		1,
	)
	if !errors.Is(err, context.DeadlineExceeded) || len(batch.Requests) != 0 {
		t.Fatalf("revived stale delivery = %#v, %v", batch, err)
	}
}

func TestOperationFileCoalescesDeliveryAndProgressCheckpoints(t *testing.T) {
	root := t.TempDir()
	now := time.UnixMilli(1_000_000)
	service, lease := openPublishedOperationService(t, root, &now, 0)
	principal := operationPrincipal(ScopeActorConverse)
	operation, err := service.SendActorMessage(principal, ActorTextInput{
		RequestID: "request.persist.checkpoint",
		HostID:    "test.host",
		WorldID:   "world.one",
		ActorID:   "actor.one",
		Text:      "Keep the durable boundary small.",
	})
	if err != nil {
		t.Fatalf("SendActorMessage: %v", err)
	}
	path := filepath.Join(root, operationFileName)
	queued := readOperationFileBytes(t, path)

	if batch := pollHost(t, service, lease, 1); len(batch.Requests) != 1 {
		t.Fatalf("PollHost = %#v", batch)
	}
	if delivered := readOperationFileBytes(t, path); !bytes.Equal(delivered, queued) {
		t.Fatal("delivery attempt rewrote durable operation state")
	}

	if err := service.AcknowledgeHost(
		"test.host",
		lease.LeaseID,
		HostAcknowledgement{OperationID: operation.OperationID, Accepted: true},
	); err != nil {
		t.Fatalf("AcknowledgeHost: %v", err)
	}
	acknowledged := readOperationFileBytes(t, path)
	if bytes.Equal(acknowledged, queued) {
		t.Fatal("acknowledgement was not persisted")
	}

	for sequence, progress := range []uint32{25, 75} {
		if err := service.ReportHostRun(
			"test.host",
			lease.LeaseID,
			host.ActionRun{
				OperationID: operation.OperationID,
				Status:      host.ActionRunning,
				ProgressSeq: uint64(sequence + 1),
				Progress:    progress,
				UpdatedAt: host.Timepoint{
					Clock: host.ClockStep,
					Value: int64(sequence + 2),
				},
			},
		); err != nil {
			t.Fatalf("ReportHostRun %d: %v", sequence+1, err)
		}
		if progressState := readOperationFileBytes(t, path); !bytes.Equal(
			progressState,
			acknowledged,
		) {
			t.Fatal("nonterminal progress rewrote durable operation state")
		}
	}

	if err := service.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	checkpoint := readOperationFileBytes(t, path)
	if bytes.Equal(checkpoint, acknowledged) {
		t.Fatal("graceful close did not flush the latest checkpoint")
	}
}

func TestOperationFileDoesNotRedeliverLegacyUnboundRequest(t *testing.T) {
	root := t.TempDir()
	if runtime.GOOS != "windows" {
		if err := os.Chmod(root, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	principal := operationPrincipal(ScopeActorConverse)
	operationID := "operation.legacy"
	state := persistedOperations{
		Version: legacyOperationFileVersion,
		Operations: []persistedOperation{{
			Request: HostControlRequest{
				OperationID: operationID,
				RequestID:   "request.legacy",
				Principal:   principal,
				HostID:      "test.host",
				WorldID:     "world.one",
				ActorID:     "actor.one",
				Kind:        ControlMessage,
				Text:        "A request from an older timeline.",
				SubmittedAt: 1,
			},
			Status:    OperationQueued,
			CreatedAt: 1,
			UpdatedAt: 1,
		}},
	}
	payload, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("Marshal legacy state: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(root, operationFileName),
		payload,
		0o600,
	); err != nil {
		t.Fatalf("WriteFile legacy state: %v", err)
	}

	now := time.UnixMilli(1_000_000)
	recovered, err := OpenFile(root, fileTestOptions(&now, 0))
	if err != nil {
		t.Fatalf("OpenFile legacy state: %v", err)
	}
	defer recovered.Close()
	view, err := recovered.GetOperation(principal, operationID)
	if err != nil || view.Status != OperationStale {
		t.Fatalf("legacy operation = %#v, %v", view, err)
	}
	lease := registerAndPublishOperationHost(t, recovered, "instance.legacy")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	batch, err := recovered.PollHost(
		ctx,
		"test.host",
		lease.LeaseID,
		1,
	)
	if !errors.Is(err, context.DeadlineExceeded) ||
		len(batch.Requests) != 0 {
		t.Fatalf("legacy redelivery = %#v, %v", batch, err)
	}
}

func readOperationFileBytes(t *testing.T, path string) []byte {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile operation state: %v", err)
	}
	return payload
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

	if runtime.GOOS == "windows" {
		return
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

func TestOperationFileRejectsCyclicParentGraph(t *testing.T) {
	operations := map[string]*operationState{
		"operation.one": {
			request: HostControlRequest{
				OperationID:       "operation.one",
				ParentOperationID: "operation.two",
			},
		},
		"operation.two": {
			request: HostControlRequest{
				OperationID:       "operation.two",
				ParentOperationID: "operation.one",
			},
		},
	}
	if err := validateOperationParentGraph(operations); err == nil {
		t.Fatal("cyclic operation parent graph was accepted")
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
