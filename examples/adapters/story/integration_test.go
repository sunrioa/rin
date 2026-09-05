package story_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/examples/adapters/story"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/mcpbridge"
	"github.com/sunrioa/rin/sdk/hostkit"
)

func TestInternalAgentAdvancesStoryThroughSharedControlPlane(t *testing.T) {
	principal := host.Principal{
		ID: "player.story.internal",
		GrantedScopes: []string{
			controlplane.ScopeActorControl,
			controlplane.ScopeActorExecute,
			controlplane.ScopeActorRead,
			controlplane.ScopeHostAdmin,
		},
	}
	harness := newStoryHarness(t, principal, controlplane.DecisionAuthority{
		Source:      controlplane.DecisionInternal,
		Revision:    1,
		PersonaMode: controlplane.PersonaCharacterBound,
	})
	persona, err := cognition.NewLocalPersonaProvider(
		[]cognition.PersonaProfile{{
			PersonaID: "persona.mira", Version: "v1",
			Identity: "Mira is a thoughtful archive volunteer who speaks for herself.",
			Boundaries: []cognition.PersonaBoundary{{
				BoundaryID: "sealed-letter", Rule: "Do not discuss the sealed letter.",
				Response: "I am not ready to discuss that letter.",
			}},
		}},
		[]cognition.PersonaBinding{{
			ActorID: story.ActorID, PersonaID: "persona.mira", Version: "v1",
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := cognition.NewLocalTaskStore(8)
	if err != nil {
		t.Fatal(err)
	}
	model := &storyModel{decisions: []cognition.ModelDecision{
		{
			Kind:       cognition.ModelDecisionAction,
			Capability: host.CapabilityRef{ID: story.CapabilitySpeak, Version: story.CapabilityVersion},
			Arguments:  json.RawMessage(`{"text":"The light in this photograph feels familiar."}`),
			Summary:    "Share an observation about the photograph.",
		},
		{Kind: cognition.ModelDecisionComplete, Summary: "The opening exchange is complete."},
	}}
	runtime, err := cognition.NewAgentRuntime(cognition.AgentRuntimeOptions{
		Principal: principal,
		Control:   harness.service,
		Environment: storyEnvironment{
			coordinator: harness.coordinator,
		},
		Persona:             persona,
		Model:               model,
		Tasks:               tasks,
		OperationWaitMillis: 2_000,
		MaxAdvancesPerRun:   16,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.StartTask(context.Background(), cognition.StartTaskInput{
		Completion: cognition.TaskCompletionPolicy{Mode: cognition.CompletionModel},
		TaskID:     "task.story.internal", HostID: story.HostID, WorldID: story.WorldID,
		ActorID: story.ActorID, ControllerID: "controller.story.internal",
		Goal: "Open the archive-room scene with one grounded line.",
		Tags: []string{"story.opening"},
	})
	if err != nil {
		t.Fatal(err)
	}

	workerResult := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		workerResult <- harness.processOne(ctx)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	task, runErr := runtime.RunTask(ctx, "task.story.internal")
	if workerErr := <-workerResult; workerErr != nil {
		t.Fatal(workerErr)
	}
	if runErr != nil {
		t.Fatal(runErr)
	}
	if task.Status != cognition.TaskCompleted {
		task, runErr = runtime.RunTask(ctx, "task.story.internal")
		if runErr != nil {
			t.Fatal(runErr)
		}
	}
	if task.Status != cognition.TaskCompleted || task.ActionCount != 1 {
		t.Fatalf("internal story task = %#v", task)
	}
	state := harness.adapter.State()
	if state.Relation != 1 || len(state.Transcript) != 1 ||
		state.Transcript[0].Speaker != "Mira" {
		t.Fatalf("internal story state = %#v", state)
	}
	if len(model.inputs) != 2 || model.inputs[0].Persona.PersonaID != "persona.mira" {
		t.Fatalf("internal model inputs = %#v", model.inputs)
	}
}

func TestExternalMCPAdvancesStoryThroughSharedControlPlane(t *testing.T) {
	principal := host.Principal{
		ID: "player.story.external",
		GrantedScopes: []string{
			controlplane.ScopeActorControl,
			controlplane.ScopeActorExecute,
			controlplane.ScopeActorRead,
			controlplane.ScopeOperationCancel,
		},
	}
	harness := newStoryHarness(t, principal, controlplane.DecisionAuthority{
		Source:                controlplane.DecisionExternal,
		ControllerPrincipalID: principal.ID,
		Revision:              1,
		PersonaMode:           controlplane.PersonaAgentAvatar,
	})
	session := connectStoryMCP(t, harness.service, principal)
	target := map[string]any{
		"host_id": story.HostID, "world_id": story.WorldID, "actor_id": story.ActorID,
	}
	var observed mcpbridge.ObserveActorOutput
	callStoryTool(t, session, "observe_actor", target, &observed)
	var catalog mcpbridge.ListActorCapabilitiesOutput
	callStoryTool(t, session, "list_actor_capabilities", target, &catalog)
	index := slices.IndexFunc(catalog.Capabilities, func(value mcpbridge.CapabilitySummary) bool {
		return value.Capability.ID == story.CapabilityAcceptTask
	})
	if index < 0 {
		t.Fatal("story task capability was not published through MCP")
	}
	capability := catalog.Capabilities[index]
	var controller mcpbridge.ControllerOutput
	callStoryTool(t, session, "acquire_actor_control", mergeStoryMaps(target, map[string]any{
		"controller_id": "controller.story.external", "lease_ttl_millis": 5_000,
	}), &controller)
	if controller.Controller.LeaseID == "" {
		t.Fatal("MCP did not acquire a story controller lease")
	}

	var submitted mcpbridge.OperationOutput
	callStoryTool(t, session, "submit_actor_action", mergeStoryMaps(target, map[string]any{
		"request_id":           "request.story.mcp.accept",
		"controller_id":        "controller.story.external",
		"capability_id":        capability.Capability.ID,
		"capability_version":   capability.Capability.Version,
		"spec_digest":          capability.Digest,
		"arguments":            map[string]any{"task": "prepare-exhibit"},
		"expected_epoch":       observed.Observation.Epoch,
		"observation_sequence": observed.Observation.Sequence,
		"idempotency_key":      "action.story.mcp.accept",
	}), &submitted)
	if submitted.Operation.Status != controlplane.OperationQueued ||
		submitted.Operation.ExecutionConfirmed {
		t.Fatalf("submitted MCP story operation = %#v", submitted.Operation)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := harness.processOne(ctx); err != nil {
		t.Fatal(err)
	}
	var completed mcpbridge.OperationOutput
	callStoryTool(t, session, "get_operation", map[string]any{
		"operation_id": submitted.Operation.OperationID,
	}, &completed)
	if completed.Operation.Status != controlplane.OperationSucceeded ||
		!completed.Operation.Terminal || !completed.Operation.ExecutionConfirmed ||
		completed.Operation.Outcome == nil {
		t.Fatalf("completed MCP story operation = %#v", completed.Operation)
	}
	if harness.adapter.State().AcceptedTask != "prepare-exhibit" {
		t.Fatalf("external story state = %#v", harness.adapter.State())
	}
}

type storyHarness struct {
	testing     *testing.T
	adapter     *story.Adapter
	coordinator *hostkit.AdapterCoordinator
	service     *controlplane.Service
	hostLease   controlplane.HostLease
	ownerID     string
	authority   controlplane.DecisionAuthority
}

func newStoryHarness(
	t *testing.T,
	principal host.Principal,
	authority controlplane.DecisionAuthority,
) *storyHarness {
	t.Helper()
	adapter, err := story.New()
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := hostkit.NewAdapterCoordinator(
		context.Background(), adapter, directDispatcher{},
	)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := story.NewPolicy()
	if err != nil {
		t.Fatal(err)
	}
	service := controlplane.New(controlplane.Options{
		ActionHost: coordinator, PolicyEngine: engine,
	})
	t.Cleanup(func() { _ = service.Close() })
	hostLease, err := service.RegisterHost(controlplane.HostRegistration{
		ContractVersion: controlplane.ContractVersion,
		HostID:          story.HostID,
		InstanceID:      "instance.story.one",
		Manifest:        coordinator.Manifest(),
		LeaseTTLMillis:  60_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	harness := &storyHarness{
		testing: t, adapter: adapter, coordinator: coordinator,
		service: service, hostLease: hostLease, ownerID: principal.ID,
		authority: authority,
	}
	if err := harness.publish(); err != nil {
		t.Fatal(err)
	}
	return harness
}

func (harness *storyHarness) publish() error {
	snapshot, err := harness.coordinator.SnapshotAction(
		context.Background(), storyControlTarget(),
	)
	if err != nil {
		return err
	}
	observation, err := harness.coordinator.Observe(context.Background(), host.ObservationQuery{
		QueryID:       "query.story.publish",
		HostID:        story.HostID,
		WorldID:       story.WorldID,
		ActorID:       story.ActorID,
		ExpectedEpoch: snapshot.Epoch,
		Limit:         128,
	})
	if err != nil {
		return err
	}
	state, err := json.Marshal(harness.adapter.State())
	if err != nil {
		return err
	}
	catalog := harness.coordinator.Capabilities()
	authority := harness.authority
	return harness.service.PublishWorld(
		story.HostID,
		harness.hostLease.LeaseID,
		controlplane.WorldPublication{
			WorldID: story.WorldID, DisplayName: "Archive Room",
			Sequence: snapshot.ObservationSeq,
			Actors: []controlplane.ActorPublication{{
				ActorID: story.ActorID, OwnerPrincipalID: harness.ownerID,
				DisplayName: "Mira", ObservationSeq: snapshot.ObservationSeq,
				Epoch: snapshot.Epoch, Authority: &authority, State: state,
				Observation: &observation, Capabilities: &catalog,
			}},
		},
	)
}

func (harness *storyHarness) processOne(ctx context.Context) error {
	batch, err := harness.service.PollHost(
		ctx, story.HostID, harness.hostLease.LeaseID, 1,
	)
	if err != nil {
		return err
	}
	if len(batch.Requests) != 1 || len(batch.GatewayRequests) != 0 {
		return errors.New("story Host received an unexpected control batch")
	}
	delivery := batch.Requests[0]
	if err := harness.service.AcknowledgeHost(
		story.HostID,
		harness.hostLease.LeaseID,
		controlplane.HostAcknowledgement{
			OperationID: delivery.Request.OperationID, Accepted: true,
		},
	); err != nil {
		return err
	}
	result, err := harness.coordinator.ExecuteDelivery(ctx, delivery)
	if err != nil {
		return err
	}
	if result.Outcome == nil {
		return errors.New("story adapter returned no authoritative Outcome")
	}
	if err := harness.publish(); err != nil {
		return err
	}
	if err := harness.service.ReportHostResult(
		story.HostID, harness.hostLease.LeaseID, *result.Outcome, result.Output,
	); err != nil {
		return err
	}
	if !harness.coordinator.ForgetOperation(delivery.Request.OperationID) {
		return errors.New("story adapter did not release the reported operation")
	}
	return nil
}

type storyEnvironment struct {
	coordinator *hostkit.AdapterCoordinator
}

func (environment storyEnvironment) Observe(
	ctx context.Context,
	query host.ObservationQuery,
) (host.ObservationEnvelope, error) {
	return environment.coordinator.Observe(ctx, query)
}

func (environment storyEnvironment) Capabilities(
	context.Context,
	controlplane.ActorControlTarget,
) (host.CapabilitySnapshot, error) {
	return environment.coordinator.Capabilities(), nil
}

type storyModel struct {
	decisions []cognition.ModelDecision
	inputs    []cognition.ModelInput
}

func (model *storyModel) Decide(
	ctx context.Context,
	input cognition.ModelInput,
) (cognition.ModelDecision, error) {
	if err := ctx.Err(); err != nil {
		return cognition.ModelDecision{}, err
	}
	model.inputs = append(model.inputs, input)
	if len(model.decisions) == 0 {
		return cognition.ModelDecision{}, errors.New("story model has no scripted decision")
	}
	decision := model.decisions[0]
	model.decisions = model.decisions[1:]
	decision.Usage.TotalTokens = 10
	return decision, nil
}

func (model *storyModel) Health(ctx context.Context) cognition.ProviderHealth {
	return cognition.ProviderHealth{Available: ctx != nil && ctx.Err() == nil}
}

func connectStoryMCP(
	t *testing.T,
	service *controlplane.Service,
	principal host.Principal,
) *mcp.ClientSession {
	t.Helper()
	gateway, err := mcpbridge.New(service, principal)
	if err != nil {
		t.Fatal(err)
	}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := gateway.Server().Connect(storyTestContext(t), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "rin-story-test", Version: "1.0.0"}, nil)
	clientSession, err := client.Connect(storyTestContext(t), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })
	return clientSession
}

func callStoryTool(
	t *testing.T,
	session *mcp.ClientSession,
	name string,
	arguments any,
	output any,
) {
	t.Helper()
	result, err := session.CallTool(storyTestContext(t), &mcp.CallToolParams{
		Name: name, Arguments: arguments,
	})
	if err != nil || result.IsError {
		t.Fatalf("CallTool(%s) = %#v, %v", name, result, err)
	}
	payload, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(output); err != nil {
		t.Fatalf("decode %s output: %v", name, err)
	}
}

func storyTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func mergeStoryMaps(left, right map[string]any) map[string]any {
	merged := make(map[string]any, len(left)+len(right))
	for key, value := range left {
		merged[key] = value
	}
	for key, value := range right {
		merged[key] = value
	}
	return merged
}
