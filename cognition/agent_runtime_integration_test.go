package cognition_test

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/policy"
	"github.com/sunrioa/rin/taskstate"
)

func TestAgentRuntimeUsesRealActionGatewayAndHostOutcome(t *testing.T) {
	t.Run("without plan", func(t *testing.T) { runAgentGatewayIntegration(t, false) })
	t.Run("waits for durable plan projection", func(t *testing.T) { runAgentGatewayIntegration(t, true) })
}

func runAgentGatewayIntegration(t *testing.T, planned bool) {
	epoch := host.Epoch{
		SessionID: "session.integration", WorldID: "world.integration",
		Host: 1, World: 1, Timeline: 1,
	}
	manifest := agentIntegrationManifest()
	registry, err := host.NewRegistry(manifest)
	if err != nil {
		t.Fatal(err)
	}
	spec := agentCapabilitySpec(t)
	spec, err = registry.RegisterSpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	actionHost := &agentIntegrationActionHost{
		registry: registry,
		snapshot: controlplane.ActionHostSnapshot{
			Now:   host.Timepoint{Clock: host.ClockStep, Value: 10},
			Epoch: epoch, ObservationSeq: 1,
		},
	}
	engine, err := policy.New(policy.Config{
		Revision: 1, Profile: policy.ProfileOpen,
		KnownEffectKinds: []string{"world.position"}, KnownScopes: []string{"world.public"},
		ConfirmationTTL:    policy.ConfirmationDurations{Step: 20},
		ConfirmationScopes: []string{"rin.policy.confirm"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var planStore *taskstate.Store
	planStarted, planRelease := make(chan struct{}), make(chan struct{})
	var planStartOnce sync.Once
	sinks := make(map[string]controlplane.OutcomeSink)
	if planned {
		planStore, err = taskstate.OpenSQLiteStore(filepath.Join(t.TempDir(), "plans.db"), taskstate.StoreConfig{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = planStore.Close() })
		planSink, err := taskstate.NewOutcomeSink(planStore)
		if err != nil {
			t.Fatal(err)
		}
		sinks["task-plan"] = integrationOutcomeSinkFunc(func(ctx context.Context, e controlplane.OutcomeEvidence) error {
			planStartOnce.Do(func() { close(planStarted) })
			select {
			case <-planRelease:
			case <-ctx.Done():
				return ctx.Err()
			}
			return planSink.RecordOutcome(ctx, e)
		})
		sinks["memory"] = integrationOutcomeSinkFunc(func(ctx context.Context, _ controlplane.OutcomeEvidence) error {
			<-ctx.Done()
			return ctx.Err()
		})
	}
	service := controlplane.New(controlplane.Options{ActionHost: actionHost, PolicyEngine: engine, OutcomeSinks: sinks})
	t.Cleanup(func() { _ = service.Close() })
	hostLease, err := service.RegisterHost(controlplane.HostRegistration{
		ContractVersion: controlplane.ContractVersion, HostID: "host.integration",
		InstanceID: "instance.integration", Manifest: manifest, LeaseTTLMillis: 30_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	authority := controlplane.DecisionAuthority{
		Source: controlplane.DecisionInternal, Revision: 1,
		PersonaMode: controlplane.PersonaCharacterBound,
	}
	if err := service.PublishWorld("host.integration", hostLease.LeaseID, controlplane.WorldPublication{
		WorldID: "world.integration", DisplayName: "Integration World", Sequence: 1,
		Actors: []controlplane.ActorPublication{{
			ActorID: "actor.mira", OwnerPrincipalID: "player.owner", DisplayName: "Mira",
			ObservationSeq: 1, Epoch: epoch, Authority: &authority,
			Capabilities: &host.CapabilitySnapshot{Revision: 1, Specs: []host.CapabilitySpec{spec}},
			State:        json.RawMessage(`{"status":"ready"}`),
		}},
	}); err != nil {
		t.Fatal(err)
	}

	playerRef := host.HostRef{
		Namespace: "integration.world", Type: "player", Key: "owner", Epoch: epoch,
	}
	observation := host.ObservationEnvelope{
		ObservationID: "observation.integration.1", HostID: "host.integration",
		WorldID: "world.integration", ActorID: "actor.mira", Epoch: epoch, Sequence: 1,
		ObservedAt: host.Timepoint{Clock: host.ClockStep, Value: 10},
		Schema: host.SchemaRef{
			ID: "rin.observation.actor", Version: "2.0.0", SHA256: strings.Repeat("a", 64),
		},
		Payload: json.RawMessage(`{"activity":"idle"}`),
		Resources: []host.ObservationResource{{
			Ref: playerRef, Kind: "player.status", Tags: []string{"player.nearby"},
			Ownership: host.OwnershipPlayer, Scope: "world.local", Quantity: 1,
			Unit: "player", Attributes: json.RawMessage(`{"distance":2}`),
		}},
	}
	environment := &fakeAgentEnvironment{
		observation: observation,
		catalog:     host.CapabilitySnapshot{Revision: 1, Specs: []host.CapabilitySpec{spec}},
	}
	persona, err := cognition.NewLocalPersonaProvider(
		[]cognition.PersonaProfile{{
			PersonaID: "persona.mira", Version: "v1", Identity: "Mira is careful.",
		}},
		[]cognition.PersonaBinding{{
			ActorID: "actor.mira", PersonaID: "persona.mira", Version: "v1",
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	memory, err := cognition.NewLocalMemoryProvider(cognition.LocalMemoryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := cognition.NewLocalTaskStore(10)
	if err != nil {
		t.Fatal(err)
	}
	model := &scriptedModelProvider{decisions: []cognition.ModelDecision{
		agentActionDecision(),
		{Kind: cognition.ModelDecisionComplete, Summary: "The Host confirms arrival."},
	}}
	principal := host.Principal{
		ID: "principal.internal", GrantedScopes: []string{controlplane.ScopeHostAdmin},
	}
	var plans taskstate.PlanClient
	planningMode := taskstate.PlanningDisabled
	if planned {
		client, err := controlplane.NewClientService(service, principal)
		if err != nil {
			t.Fatal(err)
		}
		plans, err = taskstate.NewCoordinator(planStore, client)
		if err != nil {
			t.Fatal(err)
		}
		planningMode = taskstate.PlanningRequired
		capability := model.decisions[0].Capability
		model.decisions[0].PlanDraft = &taskstate.Draft{
			Phase: "Approach", MaxReplans: 2,
			Steps: []taskstate.StepDraft{{StepID: "step.approach", Title: "Approach", Objective: "Reach the owner.",
				CapabilityHints: []host.CapabilityRef{capability}, MaxAttempts: 3,
				SuccessConditions: []taskstate.PlanCondition{{ConditionID: "condition.arrived", Kind: taskstate.EvidenceOperationOutcome,
					Summary: "The Host confirms arrival.", Capability: &capability}},
			}},
		}
	}
	runtime, err := cognition.NewAgentRuntime(cognition.AgentRuntimeOptions{
		Principal: principal, Control: service, Environment: environment,
		Persona: persona, Memory: memory, Model: model, Tasks: tasks,
		MaxAdvancesPerRun: 16, Plans: plans,
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := runtime.StartTask(context.Background(), cognition.StartTaskInput{
		TaskID: "task.integration", HostID: "host.integration", WorldID: "world.integration",
		ActorID: "actor.mira", ControllerID: "controller.internal",
		Goal: "Move near the owner.", Tags: []string{"task.follow"}, PlanningMode: planningMode,
	})
	if err != nil {
		t.Fatal(err)
	}
	queued, err := runtime.RunTask(context.Background(), started.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if queued.PendingOperationID == "" || queued.Step != 0 || queued.Status != cognition.TaskActive {
		t.Fatalf("real gateway queue was treated as execution: %+v", queued)
	}

	pollCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	batch, err := service.PollHost(pollCtx, "host.integration", hostLease.LeaseID, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Requests) != 1 {
		t.Fatalf("Host received %d requests, want 1", len(batch.Requests))
	}
	delivery := batch.Requests[0].Request
	if err := controlplane.ValidateActionDelivery(delivery); err != nil {
		t.Fatal(err)
	}
	if err := registry.AuthorizeBoundAction(
		*delivery.BoundAction, actionHost.snapshot.Now, epoch, 1, delivery.Principal,
	); err != nil {
		t.Fatal(err)
	}
	if err := service.AcknowledgeHost("host.integration", hostLease.LeaseID, controlplane.HostAcknowledgement{
		OperationID: delivery.OperationID, Accepted: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.ReportHostRun("host.integration", hostLease.LeaseID, host.ActionRun{
		OperationID: delivery.OperationID, Status: host.ActionRunning,
		ProgressSeq: 1, Progress: 50, UpdatedAt: host.Timepoint{Clock: host.ClockStep, Value: 11},
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.ReportHostOutcome("host.integration", hostLease.LeaseID, host.ActionOutcome{
		OperationID: delivery.OperationID, Status: host.ActionSucceeded,
		Summary: "The Host moved Mira near the owner.", Evidence: []host.HostRef{playerRef},
		Epoch: epoch, WorldSeq: 2, OccurredAt: host.Timepoint{Clock: host.ClockStep, Value: 12},
	}); err != nil {
		t.Fatal(err)
	}

	if planned {
		awaitTaskSignal(t, planStarted)
		waiting, err := runtime.RunTask(context.Background(), started.TaskID)
		if err != nil || waiting.Schedule.Kind != cognition.ScheduleOperation || len(model.inputs) != 1 {
			t.Fatalf("task advanced before plan projection: %#v, calls=%d, %v", waiting, len(model.inputs), err)
		}
		ready, err := runtime.TaskReady(context.Background(), waiting)
		if err != nil || ready {
			t.Fatalf("unacknowledged plan was ready: %v %v", ready, err)
		}
		state, err := planStore.Get(context.Background(), waiting.PlanID)
		if err != nil || state.Status != taskstate.PlanActive {
			t.Fatalf("plan already advanced: %#v %v", state, err)
		}
		close(planRelease)
		deadline := time.Now().Add(2 * time.Second)
		for {
			pending, err := service.OutcomeProjectionPending(principal, delivery.OperationID, "task-plan")
			if err != nil {
				t.Fatal(err)
			}
			if !pending {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("plan acknowledgement did not arrive")
			}
			time.Sleep(time.Millisecond)
		}
		ready, err = runtime.TaskReady(context.Background(), waiting)
		if err != nil || !ready {
			t.Fatalf("acknowledged plan did not wake: %v %v", ready, err)
		}
		pending, err := service.OutcomeProjectionPending(principal, delivery.OperationID, "memory")
		if err != nil || !pending {
			t.Fatalf("expected independent pending memory: %v %v", pending, err)
		}
	}
	completed, err := runtime.RunTask(context.Background(), started.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != cognition.TaskCompleted || completed.Step != 1 ||
		!historyHasKind(completed.History, "operation.terminal") {
		t.Fatalf("Host outcome did not complete the real gateway loop: %+v", completed)
	}
	if planned {
		state, err := planStore.Get(context.Background(), completed.PlanID)
		if err != nil || state.Status != taskstate.PlanCompleted {
			t.Fatalf("plan did not complete: %#v %v", state, err)
		}
	}
}

type integrationOutcomeSinkFunc func(context.Context, controlplane.OutcomeEvidence) error

func (f integrationOutcomeSinkFunc) RecordOutcome(ctx context.Context, e controlplane.OutcomeEvidence) error {
	return f(ctx, e)
}

type agentIntegrationActionHost struct {
	registry *host.Registry
	snapshot controlplane.ActionHostSnapshot
	binds    int
}

func (adapter *agentIntegrationActionHost) BindAction(
	ctx context.Context,
	target controlplane.ActorControlTarget,
	request host.ActionRequest,
) (controlplane.ActionBindingResult, error) {
	if err := ctx.Err(); err != nil {
		return controlplane.ActionBindingResult{}, err
	}
	adapter.binds++
	action, err := adapter.registry.SealBinding(request, host.BindingDraft{
		BindingID:       fmt.Sprintf("binding.integration.%d", adapter.binds),
		ResolvedTargets: append([]host.HostRef(nil), request.Targets...),
		Effects: []host.Effect{{
			EffectID: "effect.integration.move", Kind: "world.position",
			Operation: host.EffectOperationUpdate, Tags: []string{"actor.movement"},
			Ownership: host.OwnershipActor, Scope: "world.public", Quantity: 1,
			Unit: "step", Reversible: true, Risk: host.RiskLow,
			Attributes: json.RawMessage(`{"distance":2}`),
		}},
		ValidUntil: host.Timepoint{Clock: host.ClockStep, Value: 20},
	}, adapter.snapshot.Now, adapter.snapshot.Epoch, adapter.snapshot.ObservationSeq)
	if err != nil {
		return controlplane.ActionBindingResult{}, err
	}
	return controlplane.ActionBindingResult{Action: action, Snapshot: adapter.snapshot}, nil
}

func (adapter *agentIntegrationActionHost) SnapshotAction(
	ctx context.Context,
	target controlplane.ActorControlTarget,
) (controlplane.ActionHostSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return controlplane.ActionHostSnapshot{}, err
	}
	return adapter.snapshot, nil
}

func agentIntegrationManifest() host.HostManifest {
	return host.HostManifest{
		ContractVersion: host.ContractVersion, AdapterID: "integration.adapter",
		AdapterVersion: "1.0.0", EngineID: "integration.engine", EngineVersion: "1",
		Runtime: "go", Platform: "test", Headless: true, Authority: host.AuthorityServer,
		Deployment: host.DeploymentLoopbackSidecar, Control: host.ControlSemantic,
		ClockModes:    []host.ClockMode{host.ClockStep},
		DecisionModes: []host.DecisionMode{host.DecisionAsynchronous}, MaxConcurrentActors: 4,
		Durability: host.Durability{Profile: host.DurabilityAdvisory, StableIdentity: true},
	}
}

var _ controlplane.ActionHost = (*agentIntegrationActionHost)(nil)
