package grid

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/policy"
	"github.com/sunrioa/rin/sdk/hostkit"
	"github.com/sunrioa/rin/sdk/hostkit/conformance"
)

func TestGridAdapterConformance(t *testing.T) {
	adapter, err := New()
	if err != nil {
		t.Fatal(err)
	}
	report, err := conformance.Run(context.Background(), conformance.Scenario{
		Adapter:   adapter,
		Target:    Target(),
		Principal: gridPrincipal(),
		BuildAction: func(
			catalog host.CapabilitySnapshot,
			snapshot controlplane.ActionHostSnapshot,
			requestID, idempotencyKey string,
		) (host.ActionRequest, error) {
			return gridRequest(
				catalog, snapshot, CapabilityCollect,
				json.RawMessage(`{"resource":"wood","quantity":1}`),
				requestID, idempotencyKey,
			)
		},
		BuildCancellable: func(
			catalog host.CapabilitySnapshot,
			snapshot controlplane.ActionHostSnapshot,
			requestID, idempotencyKey string,
		) (host.ActionRequest, error) {
			return gridRequest(
				catalog, snapshot, CapabilityWait, json.RawMessage(`{}`),
				requestID, idempotencyKey,
			)
		},
		AdvanceObservation: adapter.AdvanceObservation,
		RestartHost:        adapter.RestartHost,
		StateDigest:        adapter.StateDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.CapabilityCount != 5 || report.EffectCount != 1 ||
		!report.IdempotentReplay || !report.StaleRejected ||
		!report.RestartRejected || !report.CancellationWorks {
		t.Fatalf("conformance report = %#v", report)
	}
}

func TestGridCollectStoreAndProtectedPolicyThroughControlPlane(t *testing.T) {
	adapter, err := New()
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := hostkit.NewAdapterCoordinator(
		context.Background(), adapter, directDispatcher{},
	)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := gridPolicy()
	if err != nil {
		t.Fatal(err)
	}
	now := time.UnixMilli(1_000_000)
	randomBytes := make([]byte, 8_192)
	for index := range randomBytes {
		randomBytes[index] = byte(index)
	}
	random := bytes.NewReader(randomBytes)
	service := controlplane.New(controlplane.Options{
		Now:          func() time.Time { return now },
		Random:       random,
		ActionHost:   coordinator,
		PolicyEngine: engine,
	})
	defer service.Close()
	hostLease, err := service.RegisterHost(controlplane.HostRegistration{
		ContractVersion: controlplane.ContractVersion,
		HostID:          HostID,
		InstanceID:      "instance.grid.one",
		Manifest:        coordinator.Manifest(),
		LeaseTTLMillis:  5_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := publishGrid(t, service, hostLease, coordinator, adapter); err != nil {
		t.Fatal(err)
	}
	principal := gridPrincipal()
	if _, err := service.AcquireController(
		principal,
		controlplane.AcquireControllerInput{
			ActorControlTarget: gridControlTarget(),
			ControllerID:       "controller.grid.external",
			LeaseTTLMillis:     5_000,
		},
	); err != nil {
		t.Fatal(err)
	}

	collect := currentGridRequest(
		t, coordinator, adapter, CapabilityCollect,
		json.RawMessage(`{"resource":"wood","quantity":2}`),
		"request.grid.collect", "action.grid.collect",
	)
	collectView := executeGridAction(
		t, service, hostLease, coordinator, principal, collect,
	)
	if !collectView.ExecutionConfirmed || collectView.Status != controlplane.OperationSucceeded {
		t.Fatalf("collect operation = %#v", collectView)
	}
	if err := publishGrid(t, service, hostLease, coordinator, adapter); err != nil {
		t.Fatal(err)
	}

	put := currentGridRequest(
		t, coordinator, adapter, CapabilityPut,
		json.RawMessage(`{"resource":"wood","quantity":2}`),
		"request.grid.put", "action.grid.put",
	)
	putView := executeGridAction(
		t, service, hostLease, coordinator, principal, put,
	)
	if !putView.ExecutionConfirmed || putView.Status != controlplane.OperationSucceeded {
		t.Fatalf("put operation = %#v", putView)
	}
	state := adapter.State()
	if state.Inventory["wood"] != 0 || state.Container["wood"] != 2 ||
		state.Resources["wood"] != 1 {
		t.Fatalf("resource task state = %#v", state)
	}
	if err := publishGrid(t, service, hostLease, coordinator, adapter); err != nil {
		t.Fatal(err)
	}

	beforeProtected := adapter.StateDigest()
	protected := currentGridRequest(
		t, coordinator, adapter, CapabilityCollect,
		json.RawMessage(`{"resource":"crystal","quantity":1}`),
		"request.grid.protected", "action.grid.protected",
	)
	rejected, err := service.SubmitAction(context.Background(), principal, controlplane.SubmitActionInput{
		HostID: HostID, WorldID: WorldID, Request: protected,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rejected.Status != controlplane.OperationRejected || !rejected.Terminal ||
		rejected.ExecutionConfirmed || rejected.PolicyDecision == nil ||
		rejected.PolicyDecision.ReasonCode != "grid.protected_resource" {
		t.Fatalf("protected operation = %#v", rejected)
	}
	if adapter.StateDigest() != beforeProtected {
		t.Fatal("denied protected resource changed the grid")
	}
	pollContext, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	batch, err := service.PollHost(pollContext, HostID, hostLease.LeaseID, 8)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("denied action PollHost = %#v, %v", batch, err)
	}
}

type directDispatcher struct{}

func (directDispatcher) Dispatch(
	ctx context.Context,
	work func(context.Context) error,
) error {
	return work(ctx)
}

func gridPrincipal() host.Principal {
	return host.Principal{
		ID: "player.grid.one",
		GrantedScopes: []string{
			controlplane.ScopeActorControl,
			controlplane.ScopeActorExecute,
			controlplane.ScopeActorRead,
			controlplane.ScopeOperationCancel,
		},
	}
}

func gridPolicy() (*policy.Engine, error) {
	return policy.New(policy.Config{
		Revision:         1,
		Profile:          policy.ProfileOpen,
		KnownEffectKinds: []string{"world.container", "world.position", "world.resource", "world.time"},
		KnownScopes:      []string{"world.protected", "world.public"},
		Rules: []policy.Rule{{
			RuleID:       "grid.protected-resource",
			Layer:        policy.LayerWorld,
			Priority:     100,
			Result:       policy.Deny,
			Scopes:       []string{"world.protected"},
			ReasonCode:   "grid.protected_resource",
			HumanSummary: "Protected grid resources cannot be collected.",
		}},
		ConfirmationTTL:    host.Duration{Clock: host.ClockStep, Value: 10},
		ConfirmationScopes: []string{"rin.policy.confirm"},
	})
}

func publishGrid(
	t *testing.T,
	service *controlplane.Service,
	lease controlplane.HostLease,
	coordinator *hostkit.AdapterCoordinator,
	adapter *Adapter,
) error {
	t.Helper()
	snapshot, err := coordinator.SnapshotAction(context.Background(), gridControlTarget())
	if err != nil {
		return err
	}
	observation, err := coordinator.Observe(context.Background(), host.ObservationQuery{
		QueryID:       fmt.Sprintf("query.grid.publish.%d", snapshot.ObservationSeq),
		HostID:        HostID,
		WorldID:       WorldID,
		ActorID:       ActorID,
		ExpectedEpoch: snapshot.Epoch,
		Limit:         128,
	})
	if err != nil {
		return err
	}
	state, err := json.Marshal(adapter.State())
	if err != nil {
		return err
	}
	catalog := coordinator.Capabilities()
	authority := controlplane.DecisionAuthority{
		Source:                controlplane.DecisionExternal,
		ControllerPrincipalID: gridPrincipal().ID,
		Revision:              1,
		PersonaMode:           controlplane.PersonaAgentAvatar,
	}
	return service.PublishWorld(HostID, lease.LeaseID, controlplane.WorldPublication{
		WorldID:     WorldID,
		DisplayName: "Neutral Grid",
		Sequence:    snapshot.ObservationSeq,
		Actors: []controlplane.ActorPublication{{
			ActorID:          ActorID,
			OwnerPrincipalID: gridPrincipal().ID,
			DisplayName:      "Grid Actor",
			ObservationSeq:   snapshot.ObservationSeq,
			Epoch:            snapshot.Epoch,
			Authority:        &authority,
			State:            state,
			Observation:      &observation,
			Capabilities:     &catalog,
		}},
	})
}

func currentGridRequest(
	t *testing.T,
	coordinator *hostkit.AdapterCoordinator,
	adapter *Adapter,
	capability string,
	arguments json.RawMessage,
	requestID, idempotencyKey string,
) host.ActionRequest {
	t.Helper()
	snapshot, err := coordinator.SnapshotAction(context.Background(), gridControlTarget())
	if err != nil {
		t.Fatal(err)
	}
	request, err := gridRequest(
		coordinator.Capabilities(), snapshot, capability, arguments,
		requestID, idempotencyKey,
	)
	if err != nil {
		t.Fatal(err)
	}
	if request.ExpectedEpoch != adapter.State().Epoch {
		t.Fatal("request did not use current grid epoch")
	}
	return request
}

func gridRequest(
	catalog host.CapabilitySnapshot,
	snapshot controlplane.ActionHostSnapshot,
	capability string,
	arguments json.RawMessage,
	requestID, idempotencyKey string,
) (host.ActionRequest, error) {
	index := slices.IndexFunc(catalog.Specs, func(spec host.CapabilitySpec) bool {
		return spec.Capability.ID == capability
	})
	if index < 0 {
		return host.ActionRequest{}, fmt.Errorf("capability %s is missing", capability)
	}
	spec := catalog.Specs[index]
	return host.ActionRequest{
		RequestID:      requestID,
		ControllerID:   "controller.grid.external",
		ActorID:        ActorID,
		Capability:     spec.Capability,
		SpecDigest:     spec.Digest,
		Arguments:      append(json.RawMessage(nil), arguments...),
		ExpectedEpoch:  snapshot.Epoch,
		ObservationSeq: snapshot.ObservationSeq,
		IdempotencyKey: idempotencyKey,
	}, nil
}

func executeGridAction(
	t *testing.T,
	service *controlplane.Service,
	lease controlplane.HostLease,
	coordinator *hostkit.AdapterCoordinator,
	principal host.Principal,
	request host.ActionRequest,
) controlplane.OperationView {
	t.Helper()
	queued, err := service.SubmitAction(context.Background(), principal, controlplane.SubmitActionInput{
		HostID: HostID, WorldID: WorldID, Request: request,
	})
	if err != nil {
		t.Fatal(err)
	}
	if queued.Status != controlplane.OperationQueued || queued.ExecutionConfirmed {
		t.Fatalf("queued operation = %#v", queued)
	}
	batch, err := service.PollHost(context.Background(), HostID, lease.LeaseID, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Requests) != 1 ||
		batch.Requests[0].Request.OperationID != queued.OperationID {
		t.Fatalf("Host batch = %#v", batch)
	}
	if err := service.AcknowledgeHost(
		HostID,
		lease.LeaseID,
		controlplane.HostAcknowledgement{OperationID: queued.OperationID, Accepted: true},
	); err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.ExecuteDelivery(context.Background(), batch.Requests[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ReportHostRun(HostID, lease.LeaseID, result.Run); err != nil {
		t.Fatal(err)
	}
	if result.Outcome == nil {
		t.Fatal("grid action returned no Outcome")
	}
	if err := service.ReportHostResult(
		HostID, lease.LeaseID, *result.Outcome, result.Output,
	); err != nil {
		t.Fatal(err)
	}
	view, err := service.GetOperation(principal, queued.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if !coordinator.ForgetOperation(queued.OperationID) {
		t.Fatal("reported grid operation was not released")
	}
	return view
}

func gridControlTarget() controlplane.ActorControlTarget {
	target := Target()
	return controlplane.ActorControlTarget{
		HostID: target.HostID, WorldID: target.WorldID, ActorID: target.ActorID,
	}
}

func TestGridRequestRejectsMissingCapability(t *testing.T) {
	_, err := gridRequest(
		host.CapabilitySnapshot{},
		controlplane.ActionHostSnapshot{},
		"grid.missing",
		json.RawMessage(`{}`),
		"request.grid.missing",
		"action.grid.missing",
	)
	if err == nil {
		t.Fatal("missing capability was accepted")
	}
}
