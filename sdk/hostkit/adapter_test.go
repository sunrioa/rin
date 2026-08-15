package hostkit

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/policy"
)

func TestAdapterCoordinatorBindsPreviewsAndExecutesExactlyOnce(t *testing.T) {
	adapter := newAdapterFixture(t)
	dispatcher := &adapterDispatcher{}
	coordinator, err := NewAdapterCoordinator(
		context.Background(), adapter, dispatcher,
	)
	if err != nil {
		t.Fatal(err)
	}
	request := adapterRequest(
		coordinator.Capabilities().Specs[0],
		adapter.snapshot,
		"request.adapter.execute",
		"action.adapter.execute",
	)
	bound, err := coordinator.BindAction(
		context.Background(), adapterControlTarget(), request,
	)
	if err != nil {
		t.Fatal(err)
	}
	if adapter.bindCalls != 1 || adapter.previewCalls != 1 ||
		bound.Action.BoundAt != adapter.snapshot.Now ||
		len(bound.Action.Effects) != 1 {
		t.Fatalf("binding = %#v", bound)
	}
	delivery := adapterDelivery(t, request, bound, "operation.adapter.execute")
	result, err := coordinator.ExecuteDelivery(context.Background(), delivery)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome == nil || result.Run.Status != host.ActionSucceeded ||
		string(result.Output) != `{"moved":true}` || adapter.executeCalls != 1 {
		t.Fatalf("execution = %#v, calls = %d", result, adapter.executeCalls)
	}

	replayed, err := coordinator.ExecuteDelivery(context.Background(), delivery)
	if err != nil {
		t.Fatal(err)
	}
	if adapter.executeCalls != 1 || string(replayed.Output) != string(result.Output) ||
		replayed.Run != result.Run {
		t.Fatalf("redelivery executed again: %#v", replayed)
	}
	if !dispatcher.used {
		t.Fatal("adapter work did not use the authority dispatcher")
	}
	if !coordinator.ForgetOperation(delivery.Request.OperationID) {
		t.Fatal("terminal operation was not released")
	}
}

func TestAdapterCoordinatorRejectsStaleBindingBeforeExecution(t *testing.T) {
	adapter := newAdapterFixture(t)
	coordinator, err := NewAdapterCoordinator(
		context.Background(), adapter, &adapterDispatcher{},
	)
	if err != nil {
		t.Fatal(err)
	}
	request := adapterRequest(
		coordinator.Capabilities().Specs[0],
		adapter.snapshot,
		"request.adapter.stale",
		"action.adapter.stale",
	)
	bound, err := coordinator.BindAction(
		context.Background(), adapterControlTarget(), request,
	)
	if err != nil {
		t.Fatal(err)
	}
	delivery := adapterDelivery(t, request, bound, "operation.adapter.stale")
	adapter.snapshot.ObservationSeq += 9
	adapter.snapshot.Now.Value += 9
	if _, err := coordinator.ExecuteDelivery(
		context.Background(), delivery,
	); err == nil || !strings.Contains(err.Error(), "observation") {
		t.Fatalf("stale execution error = %v", err)
	}
	if adapter.executeCalls != 0 {
		t.Fatal("stale action reached adapter Execute")
	}
}

func TestAdapterCoordinatorRejectsInvalidAdapterResult(t *testing.T) {
	adapter := newAdapterFixture(t)
	adapter.output = json.RawMessage(`{"moved":"yes"}`)
	coordinator, err := NewAdapterCoordinator(
		context.Background(), adapter, &adapterDispatcher{},
	)
	if err != nil {
		t.Fatal(err)
	}
	request := adapterRequest(
		coordinator.Capabilities().Specs[0],
		adapter.snapshot,
		"request.adapter.output",
		"action.adapter.output",
	)
	bound, err := coordinator.BindAction(
		context.Background(), adapterControlTarget(), request,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.ExecuteDelivery(
		context.Background(),
		adapterDelivery(t, request, bound, "operation.adapter.output"),
	); err == nil || !strings.Contains(err.Error(), "validate adapter Output") {
		t.Fatalf("invalid output error = %v", err)
	}
}

func TestAdapterCoordinatorValidatesRequestBeforeAdapterBinding(t *testing.T) {
	adapter := newAdapterFixture(t)
	coordinator, err := NewAdapterCoordinator(
		context.Background(), adapter, &adapterDispatcher{},
	)
	if err != nil {
		t.Fatal(err)
	}
	request := adapterRequest(
		coordinator.Capabilities().Specs[0],
		adapter.snapshot,
		"request.adapter.invalid",
		"action.adapter.invalid",
	)
	request.Arguments = json.RawMessage(`{"steps":99}`)
	if _, err := coordinator.BindAction(
		context.Background(), adapterControlTarget(), request,
	); err == nil {
		t.Fatal("invalid request reached adapter binding")
	}
	if adapter.bindCalls != 0 || adapter.previewCalls != 0 {
		t.Fatalf("invalid request calls = bind %d, preview %d",
			adapter.bindCalls, adapter.previewCalls)
	}
}

func TestAdapterCoordinatorObservesAndNormalizesPolicyFacts(t *testing.T) {
	adapter := newAdapterFixture(t)
	coordinator, err := NewAdapterCoordinator(
		context.Background(), adapter, &adapterDispatcher{},
	)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := coordinator.Observe(
		context.Background(),
		host.ObservationQuery{
			QueryID:       "query.adapter.one",
			HostID:        "test.host",
			WorldID:       "world.one",
			ActorID:       "actor.one",
			ExpectedEpoch: adapter.snapshot.Epoch,
			Limit:         16,
		},
	)
	if err != nil || observation.Sequence != adapter.snapshot.ObservationSeq {
		t.Fatalf("Observe = %#v, %v", observation, err)
	}
	facts, err := coordinator.PolicyFacts(
		context.Background(),
		AdapterTarget{HostID: "test.host", WorldID: "world.one", ActorID: "actor.one"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts.KnownEffectKinds) != 1 || facts.KnownEffectKinds[0] != "world.position" ||
		len(facts.KnownScopes) != 1 || facts.KnownScopes[0] != "world.public" {
		t.Fatalf("PolicyFacts = %#v", facts)
	}
}

type adapterDispatcher struct {
	used bool
}

func (dispatcher *adapterDispatcher) Dispatch(
	ctx context.Context,
	work func(context.Context) error,
) error {
	dispatcher.used = true
	return work(ctx)
}

type adapterFixture struct {
	manifest     host.HostManifest
	spec         host.CapabilitySpec
	schema       host.Schema
	snapshot     AdapterSnapshot
	bindCalls    int
	previewCalls int
	executeCalls int
	output       json.RawMessage
}

func newAdapterFixture(t *testing.T) *adapterFixture {
	t.Helper()
	emptySchema := adapterSchema(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"additionalProperties":false
	}`)
	inputSchema := adapterSchema(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{"steps":{"type":"integer","minimum":1,"maximum":4}},
		"required":["steps"],
		"additionalProperties":false
	}`)
	outputSchema := adapterSchema(t, `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{"moved":{"type":"boolean"}},
		"required":["moved"],
		"additionalProperties":false
	}`)
	return &adapterFixture{
		manifest: host.HostManifest{
			ContractVersion:     host.ContractVersion,
			AdapterID:           "test.adapter.v2",
			AdapterVersion:      "2.0.0",
			EngineID:            "test.engine",
			EngineVersion:       "1.0.0",
			Runtime:             "go",
			Platform:            "test",
			Headless:            true,
			Authority:           host.AuthorityServer,
			Deployment:          host.DeploymentEmbeddedOffline,
			Control:             host.ControlSemantic,
			ClockModes:          []host.ClockMode{host.ClockStep},
			DecisionModes:       []host.DecisionMode{host.DecisionAsynchronous},
			MaxConcurrentActors: 1,
			Durability: host.Durability{
				Profile:        host.DurabilityAdvisory,
				StableIdentity: true,
			},
		},
		spec: host.CapabilitySpec{
			Capability:         host.CapabilityRef{ID: "test.actor.move", Version: "2.0.0"},
			Description:        "Move one test actor.",
			Input:              inputSchema,
			Output:             outputSchema,
			EffectSchema:       emptySchema,
			Kind:               host.CapabilityAtomic,
			Execution:          host.ExecutionImmediate,
			Cancellation:       host.CancellationUnsupported,
			RiskFloor:          host.RiskLow,
			RequiredDurability: host.DurabilityAdvisory,
			RequiredScopes:     []string{controlplane.ScopeActorExecute},
			ExecutionBudget:    host.Duration{Clock: host.ClockStep, Value: 20},
			MaxInputBytes:      1_024,
			MaxOutputBytes:     1_024,
			MaxEffects:         2,
		},
		schema: emptySchema,
		snapshot: AdapterSnapshot{
			Now:            host.Timepoint{Clock: host.ClockStep, Value: 10},
			Epoch:          adapterEpoch(),
			ObservationSeq: 1,
		},
		output: json.RawMessage(`{"moved":true}`),
	}
}

func (adapter *adapterFixture) Manifest() host.HostManifest {
	return adapter.manifest
}

func (adapter *adapterFixture) Snapshot(
	context.Context,
	AdapterTarget,
) (AdapterSnapshot, error) {
	return adapter.snapshot, nil
}

func (adapter *adapterFixture) Observe(
	_ context.Context,
	query host.ObservationQuery,
) (host.ObservationEnvelope, error) {
	return host.ObservationEnvelope{
		ObservationID: "observation.adapter.one",
		HostID:        query.HostID,
		WorldID:       query.WorldID,
		ActorID:       query.ActorID,
		Epoch:         adapter.snapshot.Epoch,
		Sequence:      adapter.snapshot.ObservationSeq,
		ObservedAt:    adapter.snapshot.Now,
		Schema: host.SchemaRef{
			ID: "test.adapter.observation", Version: "1.0.0", SHA256: adapter.schema.SHA256,
		},
		Payload: json.RawMessage(`{}`),
	}, nil
}

func (adapter *adapterFixture) ListCapabilities(
	context.Context,
) ([]host.CapabilitySpec, error) {
	return []host.CapabilitySpec{adapter.spec}, nil
}

func (adapter *adapterFixture) Bind(
	_ context.Context,
	_ AdapterTarget,
	_ host.ActionRequest,
) (AdapterBinding, error) {
	adapter.bindCalls++
	return AdapterBinding{
		BindingID:  "binding.adapter.one",
		ValidUntil: host.Timepoint{Clock: host.ClockStep, Value: adapter.snapshot.Now.Value + 10},
	}, nil
}

func (adapter *adapterFixture) Preview(
	_ context.Context,
	_ AdapterTarget,
	_ host.ActionRequest,
	_ AdapterBinding,
) ([]host.Effect, error) {
	adapter.previewCalls++
	return []host.Effect{{
		EffectID:   "effect.adapter.move",
		Kind:       "world.position",
		Operation:  host.EffectOperationUpdate,
		Tags:       []string{"actor.movement"},
		Ownership:  host.OwnershipActor,
		Scope:      "world.public",
		Quantity:   1,
		Unit:       "step",
		Reversible: true,
		Risk:       host.RiskLow,
		Attributes: json.RawMessage(`{}`),
	}}, nil
}

func (adapter *adapterFixture) Execute(
	_ context.Context,
	operation AdapterOperation,
) (AdapterResult, error) {
	adapter.executeCalls++
	adapter.snapshot.Now.Value++
	adapter.snapshot.ObservationSeq++
	return adapterSuccessResult(operation, adapter.snapshot, adapter.output), nil
}

func (adapter *adapterFixture) Cancel(
	context.Context,
	AdapterOperation,
) (AdapterResult, error) {
	return AdapterResult{}, errors.New("cancellation is unsupported")
}

func (adapter *adapterFixture) Verify(
	context.Context,
	AdapterOperation,
) (AdapterResult, error) {
	return AdapterResult{}, errors.New("no running operation")
}

func (adapter *adapterFixture) PolicyFacts(
	context.Context,
	AdapterTarget,
) (AdapterPolicyFacts, error) {
	return AdapterPolicyFacts{
		KnownEffectKinds: []string{"world.position"},
		KnownScopes:      []string{"world.public"},
	}, nil
}

func adapterSuccessResult(
	operation AdapterOperation,
	snapshot AdapterSnapshot,
	output json.RawMessage,
) AdapterResult {
	return AdapterResult{
		Run: host.ActionRun{
			OperationID: operation.OperationID,
			Status:      host.ActionSucceeded,
			ProgressSeq: 1,
			Progress:    100,
			UpdatedAt:   snapshot.Now,
		},
		Outcome: &host.ActionOutcome{
			OperationID: operation.OperationID,
			Status:      host.ActionSucceeded,
			Summary:     "The adapter applied the action.",
			Epoch:       snapshot.Epoch,
			WorldSeq:    snapshot.ObservationSeq,
			OccurredAt:  snapshot.Now,
		},
		Output: append(json.RawMessage(nil), output...),
	}
}

func adapterRequest(
	spec host.CapabilitySpec,
	snapshot AdapterSnapshot,
	requestID, idempotencyKey string,
) host.ActionRequest {
	return host.ActionRequest{
		RequestID:      requestID,
		ControllerID:   "controller.adapter.one",
		ActorID:        "actor.one",
		Capability:     spec.Capability,
		SpecDigest:     spec.Digest,
		Arguments:      json.RawMessage(`{"steps":1}`),
		ExpectedEpoch:  snapshot.Epoch,
		ObservationSeq: snapshot.ObservationSeq,
		IdempotencyKey: idempotencyKey,
	}
}

func adapterDelivery(
	t *testing.T,
	request host.ActionRequest,
	binding controlplane.ActionBindingResult,
	operationID string,
) controlplane.HostControlDelivery {
	t.Helper()
	principal := host.Principal{
		ID:            "player.one",
		GrantedScopes: []string{controlplane.ScopeActorExecute},
	}
	engine, err := policy.New(policy.Config{
		Revision:           1,
		Profile:            policy.ProfileOpen,
		KnownEffectKinds:   []string{"world.position"},
		KnownScopes:        []string{"world.public"},
		ConfirmationTTL:    policy.ConfirmationDurations{Step: 10},
		ConfirmationScopes: []string{"rin.policy.confirm"},
	})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := engine.Evaluate(binding.Action, policy.Context{
		Now:          binding.Snapshot.Now,
		CurrentEpoch: binding.Snapshot.Epoch,
		Principal:    principal,
		ServerID:     "test.host",
	})
	if err != nil || decision.Result != policy.Allow {
		t.Fatalf("policy decision = %#v, %v", decision, err)
	}
	return controlplane.HostControlDelivery{
		DeliveryAttempt: 1,
		Request: controlplane.HostControlRequest{
			OperationID: operationID,
			RequestID:   request.RequestID,
			Principal:   principal,
			HostID:      "test.host",
			WorldID:     "world.one",
			ActorID:     "actor.one",
			Kind:        controlplane.ControlAction,
			Binding: &controlplane.ControlBinding{
				Epoch:             binding.Snapshot.Epoch,
				ObservationSeq:    binding.Snapshot.ObservationSeq,
				AuthorityRevision: 1,
				ControllerLeaseID: "lease.adapter.one",
			},
			ActionRequest:  &request,
			BoundAction:    &binding.Action,
			PolicyDecision: &decision,
			SubmittedAt:    1,
		},
	}
}

func adapterControlTarget() controlplane.ActorControlTarget {
	return controlplane.ActorControlTarget{
		HostID: "test.host", WorldID: "world.one", ActorID: "actor.one",
	}
}

func adapterEpoch() host.Epoch {
	return host.Epoch{
		SessionID: "session.adapter.one",
		WorldID:   "world.one",
		Host:      1,
		World:     1,
		Timeline:  1,
	}
}

func adapterSchema(t *testing.T, document string) host.Schema {
	t.Helper()
	schema, err := host.NewSchema([]byte(document))
	if err != nil {
		t.Fatal(err)
	}
	return schema
}
