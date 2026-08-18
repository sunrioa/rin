package mcpbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/policy"
	"github.com/sunrioa/rin/signalbox"
)

func TestGatewayNegotiatesCurrentProtocolAndReadsV2State(t *testing.T) {
	environment := newMCPEnvironment(t, host.RiskLow)
	session := connectClient(t, environment.service, readPrincipal())
	if discovery := session.InitializeResult(); discovery == nil ||
		discovery.ProtocolVersion != ProtocolVersion {
		t.Fatalf("discovery result = %#v", discovery)
	}

	tools, err := session.ListTools(testContext(t), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := toolNames(tools.Tools)
	expected := []string{
		"describe_actor_capability",
		"get_actor_state",
		"get_operation",
		"get_task_timeline",
		"list_actor_capabilities",
		"list_actors",
		"list_worlds",
		"observe_actor",
		"wait_actor_update",
		"wait_operation",
		"wait_task_timeline",
	}
	if !slices.Equal(names, expected) {
		t.Fatalf("tool names = %#v", names)
	}
	for _, tool := range tools.Tools {
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint ||
			!tool.Annotations.IdempotentHint {
			t.Fatalf("read tool %q annotations = %#v", tool.Name, tool.Annotations)
		}
	}

	var worlds ListWorldsOutput
	callTool(t, session, "list_worlds", map[string]any{}, &worlds)
	if len(worlds.Worlds) != 1 || worlds.Worlds[0].WorldID != "world.one" ||
		!worlds.Worlds[0].Online {
		t.Fatalf("list_worlds = %#v", worlds)
	}
	target := actorTargetArguments()
	var actors ListActorsOutput
	callTool(t, session, "list_actors", map[string]any{
		"host_id": "test.host", "world_id": "world.one",
	}, &actors)
	if len(actors.Actors) != 1 || actors.Actors[0].ActorID != "actor.one" {
		t.Fatalf("list_actors = %#v", actors)
	}
	var state GetActorStateOutput
	callTool(t, session, "get_actor_state", target, &state)
	if state.Actor.DisplayName != "Companion" ||
		state.Actor.State["status"] != "ready" || state.Actor.Controller != nil {
		t.Fatalf("get_actor_state = %#v", state)
	}
	var observation ObserveActorOutput
	callTool(t, session, "observe_actor", target, &observation)
	if observation.Observation.ObservationID != "observation.actor.one.1" ||
		len(observation.Observation.Facts) != 1 {
		t.Fatalf("observe_actor = %#v", observation)
	}
	var capabilities ListActorCapabilitiesOutput
	callTool(t, session, "list_actor_capabilities", target, &capabilities)
	if capabilities.Revision != 1 || len(capabilities.Capabilities) != 1 ||
		capabilities.Capabilities[0].Digest != environment.spec.Digest {
		t.Fatalf("list_actor_capabilities = %#v", capabilities)
	}
	var described DescribeActorCapabilityOutput
	describeInput := mapsClone(target)
	describeInput["capability_id"] = environment.spec.Capability.ID
	describeInput["capability_version"] = environment.spec.Capability.Version
	callTool(t, session, "describe_actor_capability", describeInput, &described)
	if described.Capability.Digest != environment.spec.Digest {
		t.Fatalf("describe_actor_capability = %#v", described)
	}
	var update WaitActorUpdateOutput
	waitInput := mapsClone(target)
	waitInput["after_observation_seq"] = state.Actor.ObservationSeq
	waitInput["after_authority_revision"] = state.Actor.DecisionAuthority.Revision
	waitInput["wait_millis"] = 0
	callTool(t, session, "wait_actor_update", waitInput, &update)
	if update.Changed {
		t.Fatalf("unchanged actor cursor reported an update: %#v", update)
	}
}

func TestGatewayAcceptsOlderProtocolThroughOfficialNegotiation(t *testing.T) {
	const olderProtocolVersion = "2025-11-25"
	environment := newMCPEnvironment(t, host.RiskLow)
	gateway, err := New(environment.service, readPrincipal())
	if err != nil {
		t.Fatalf("New gateway: %v", err)
	}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := gateway.Server().Connect(testContext(t), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	connection, err := clientTransport.Connect(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	payload := callRawMCP(t, connection, 1, "initialize", &mcp.InitializeParams{
		ProtocolVersion: olderProtocolVersion,
		Capabilities:    &mcp.ClientCapabilities{},
		ClientInfo:      &mcp.Implementation{Name: "rin-compat-test", Version: "1.0.0"},
	})
	var initialized mcp.InitializeResult
	if err := json.Unmarshal(payload, &initialized); err != nil {
		t.Fatal(err)
	}
	if initialized.ProtocolVersion != olderProtocolVersion {
		t.Fatalf("negotiated protocol = %#v", initialized)
	}
	params, _ := json.Marshal(&mcp.InitializedParams{})
	if err := connection.Write(testContext(t), &jsonrpc.Request{
		Method: "notifications/initialized", Params: params,
	}); err != nil {
		t.Fatal(err)
	}
	payload = callRawMCP(t, connection, 2, "tools/list", &mcp.ListToolsParams{})
	var tools mcp.ListToolsResult
	if err := json.Unmarshal(payload, &tools); err != nil || len(tools.Tools) == 0 {
		t.Fatalf("older tools/list = %#v, %v", tools, err)
	}
}

func TestGatewayExposesHostSignalsAsReadOnlyHints(t *testing.T) {
	environment := newMCPEnvironment(t, host.RiskLow)
	principal := readPrincipal()
	client, err := controlplane.NewClientService(environment.service, principal)
	if err != nil {
		t.Fatal(err)
	}
	store, err := signalbox.NewStore(signalbox.StoreConfig{
		Now: func() time.Time { return time.UnixMilli(1_000_000) },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Configure(signalbox.Target{
		HostID: "test.host", WorldID: "world.one", ActorID: "actor.one",
	}, signalbox.Settings{Enabled: true, MaxPending: 8}); err != nil {
		t.Fatal(err)
	}
	if result, err := store.Publish(signalbox.Signal{
		SignalID: "signal.one", HostID: "test.host", WorldID: "world.one", ActorID: "actor.one",
		Kind: "test.player.hurt", Summary: "The player was hurt.", Epoch: testEpoch(),
		ObservationSequence: 1, ExpiresAtUnixMillis: 1_010_000,
	}); err != nil || !result.Accepted {
		t.Fatalf("publish = %#v, %v", result, err)
	}
	signals, err := signalbox.NewService(store, environment.service, client)
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := NewClientWithServices(client, nil, nil, signals, principal)
	if err != nil {
		t.Fatal(err)
	}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := gateway.Server().Connect(testContext(t), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "rin-test", Version: "1.0.0"}, nil)
	session, err := mcpClient.Connect(testContext(t), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	tools, err := session.ListTools(testContext(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := toolNames(tools.Tools)
	if !slices.Contains(names, "list_actor_signals") || !slices.Contains(names, "wait_actor_signals") {
		t.Fatalf("signal tools = %v", names)
	}
	var listed SignalsOutput
	callTool(t, session, "list_actor_signals", actorTargetArguments(), &listed)
	if len(listed.Inbox.Signals) != 1 || listed.Inbox.Signals[0].SignalID != "signal.one" {
		t.Fatalf("list_actor_signals = %#v", listed)
	}
}

func TestGatewayDoesNotRevealAnotherPrincipalsWorlds(t *testing.T) {
	environment := newMCPEnvironment(t, host.RiskLow)
	session := connectClient(t, environment.service, host.Principal{
		ID: "player.two", GrantedScopes: []string{controlplane.ScopeActorRead},
	})
	var worlds ListWorldsOutput
	callTool(t, session, "list_worlds", map[string]any{}, &worlds)
	if len(worlds.Worlds) != 0 {
		t.Fatalf("visible worlds = %#v", worlds.Worlds)
	}
}

func TestGatewayV2ActionUsesControllerHostBindingPolicyAndOutcome(t *testing.T) {
	environment := newMCPEnvironment(t, host.RiskLow)
	principal := actionPrincipal()
	session := connectClient(t, environment.service, principal)
	tools, err := session.ListTools(testContext(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{
		"acquire_actor_control", "cancel_operation", "confirm_action",
		"describe_actor_capability", "get_actor_control", "get_actor_state",
		"get_operation", "get_task_timeline", "list_actor_capabilities", "list_actors", "list_worlds",
		"observe_actor", "release_actor_control", "renew_actor_control",
		"set_actor_emergency_stop", "submit_actor_action", "wait_actor_update",
		"wait_operation", "wait_task_timeline",
	}
	if names := toolNames(tools.Tools); !slices.Equal(names, expected) {
		t.Fatalf("scoped tool names = %#v", names)
	}

	controller := acquireControllerThroughMCP(t, session)
	actionInput := environment.actionArguments("request.mcp.action.1", "action.mcp.1")
	actionInput["task_id"] = "task.mcp.timeline"
	result := callToolAsync(session, "submit_actor_action", actionInput)
	gatewayDelivery := pollGateway(t, environment)
	binding := environment.bind(t, *gatewayDelivery.ActionRequest)
	if err := environment.service.ReportHostGatewayResult(
		"test.host", environment.lease.LeaseID,
		controlplane.HostGatewayResult{
			GatewayRequestID: gatewayDelivery.GatewayRequestID,
			Binding:          &binding,
		},
	); err != nil {
		t.Fatal(err)
	}
	var submitted OperationOutput
	decodeAsyncTool(t, result, &submitted)
	if submitted.Operation.Status != controlplane.OperationQueued ||
		submitted.Operation.ExecutionConfirmed || submitted.Operation.Terminal ||
		submitted.Operation.ControllerLeaseID != controller.Controller.LeaseID {
		t.Fatalf("submit_actor_action = %#v", submitted.Operation)
	}
	var initialTimeline TaskTimelineOutput
	callTool(t, session, "get_task_timeline", map[string]any{
		"task_id": "task.mcp.timeline", "limit": 64,
	}, &initialTimeline)
	if len(initialTimeline.Timeline.Events) == 0 {
		t.Fatal("submitted action produced no task timeline evidence")
	}

	work := pollHost(t, environment, 1)
	if len(work.Requests) != 1 {
		t.Fatalf("operation poll = %#v", work)
	}
	request := work.Requests[0].Request
	if err := controlplane.ValidateActionDelivery(request); err != nil {
		t.Fatalf("ValidateActionDelivery: %v", err)
	}
	if err := environment.registry.AuthorizeBoundAction(
		*request.BoundAction,
		environment.snapshot.Now,
		environment.snapshot.Epoch,
		environment.snapshot.ObservationSeq,
		request.Principal,
	); err != nil {
		t.Fatalf("Host final authorization: %v", err)
	}
	if err := environment.service.AcknowledgeHost(
		"test.host", environment.lease.LeaseID,
		controlplane.HostAcknowledgement{
			OperationID: submitted.Operation.OperationID, Accepted: true,
		},
	); err != nil {
		t.Fatal(err)
	}
	if err := environment.service.ReportHostResult(
		"test.host", environment.lease.LeaseID,
		host.ActionOutcome{
			OperationID: submitted.Operation.OperationID,
			Status:      host.ActionSucceeded, Summary: "The Host moved the actor.",
			Epoch: environment.snapshot.Epoch, WorldSeq: 2,
			OccurredAt: host.Timepoint{Clock: host.ClockStep, Value: 12},
		},
		json.RawMessage(`{"distance":2}`),
	); err != nil {
		t.Fatal(err)
	}
	var fetched OperationOutput
	callTool(t, session, "get_operation", map[string]any{
		"operation_id": submitted.Operation.OperationID,
	}, &fetched)
	if !fetched.Operation.ExecutionConfirmed || !fetched.Operation.Terminal ||
		fetched.Operation.Status != controlplane.OperationSucceeded ||
		fmt.Sprint(fetched.Operation.Output["distance"]) != "2" {
		t.Fatalf("get_operation = %#v", fetched.Operation)
	}
	var waited OperationUpdateOutput
	callTool(t, session, "wait_operation", map[string]any{
		"operation_id": submitted.Operation.OperationID,
		"after_cursor": submitted.Operation.Cursor,
		"wait_millis":  0,
	}, &waited)
	if !waited.Changed || !waited.Operation.ExecutionConfirmed ||
		fmt.Sprint(waited.Operation.Output["distance"]) != "2" {
		t.Fatalf("wait_operation = %#v", waited)
	}
	var timelineUpdate TaskTimelineUpdateOutput
	callTool(t, session, "wait_task_timeline", map[string]any{
		"task_id":      "task.mcp.timeline",
		"after_cursor": initialTimeline.Timeline.NextCursor,
		"limit":        64, "wait_millis": 0,
	}, &timelineUpdate)
	if !timelineUpdate.Changed || len(timelineUpdate.Timeline.Events) == 0 {
		t.Fatalf("wait_task_timeline = %#v", timelineUpdate)
	}
	lastEvent := timelineUpdate.Timeline.Events[len(timelineUpdate.Timeline.Events)-1]
	if lastEvent.Operation == nil || !lastEvent.Operation.ExecutionConfirmed ||
		!lastEvent.Operation.Terminal {
		t.Fatalf("terminal timeline event = %#v", lastEvent)
	}
	timelineKinds := make([]string, 0, len(initialTimeline.Timeline.Events)+len(timelineUpdate.Timeline.Events))
	for _, event := range append(initialTimeline.Timeline.Events, timelineUpdate.Timeline.Events...) {
		timelineKinds = append(timelineKinds, event.EventKind)
	}
	wantTimelineKinds := []string{
		"operation.queued", "operation.delivered", "operation.accepted", "operation.succeeded",
	}
	if !slices.Equal(timelineKinds, wantTimelineKinds) {
		t.Fatalf("external timeline kinds = %v, want %v", timelineKinds, wantTimelineKinds)
	}

	var retry OperationOutput
	callTool(t, session, "submit_actor_action", actionInput, &retry)
	if retry.Operation.OperationID != submitted.Operation.OperationID {
		t.Fatalf("idempotent retry = %#v", retry.Operation)
	}
	changed := mapsClone(actionInput)
	changed["arguments"] = map[string]any{"distance": 2}
	changedResult, err := session.CallTool(testContext(t), &mcp.CallToolParams{
		Name: "submit_actor_action", Arguments: changed,
	})
	if err != nil || !changedResult.IsError {
		t.Fatalf("changed idempotency retry = %#v, %v", changedResult, err)
	}

	var stop EmergencyStopOutput
	callTool(t, session, "set_actor_emergency_stop", mergeMaps(
		actorTargetArguments(), map[string]any{"active": true},
	), &stop)
	if !stop.EmergencyStop.Active {
		t.Fatalf("emergency stop = %#v", stop)
	}
	callTool(t, session, "set_actor_emergency_stop", mergeMaps(
		actorTargetArguments(), map[string]any{"active": false},
	), &stop)
	var renewed ControllerOutput
	callTool(t, session, "renew_actor_control", mergeMaps(
		actorTargetArguments(), map[string]any{
			"lease_id": controller.Controller.LeaseID, "lease_ttl_millis": 10_000,
		},
	), &renewed)
	if renewed.Controller.ExpiresAtUnixMillis <= controller.Controller.ExpiresAtUnixMillis {
		t.Fatalf("renewed controller = %#v", renewed)
	}
	var released ReleaseActorControlOutput
	callTool(t, session, "release_actor_control", mergeMaps(
		actorTargetArguments(), map[string]any{"lease_id": renewed.Controller.LeaseID},
	), &released)
	if !released.Released {
		t.Fatalf("release_actor_control = %#v", released)
	}
}

func TestGatewayConfirmationUsesFreshHostSnapshot(t *testing.T) {
	environment := newMCPEnvironment(t, host.RiskCritical)
	session := connectClient(t, environment.service, actionPrincipal())
	acquireControllerThroughMCP(t, session)
	submission := callToolAsync(
		session,
		"submit_actor_action",
		environment.actionArguments("request.mcp.confirm", "action.mcp.confirm"),
	)
	delivery := pollGateway(t, environment)
	binding := environment.bind(t, *delivery.ActionRequest)
	if err := environment.service.ReportHostGatewayResult(
		"test.host", environment.lease.LeaseID,
		controlplane.HostGatewayResult{
			GatewayRequestID: delivery.GatewayRequestID, Binding: &binding,
		},
	); err != nil {
		t.Fatal(err)
	}
	var pending OperationOutput
	decodeAsyncTool(t, submission, &pending)
	if pending.Operation.Status != controlplane.OperationAwaitingConfirmation {
		t.Fatalf("pending operation = %#v", pending.Operation)
	}
	confirmation := callToolAsync(session, "confirm_action", map[string]any{
		"operation_id": pending.Operation.OperationID,
	})
	snapshotDelivery := pollGateway(t, environment)
	if snapshotDelivery.Kind != controlplane.HostGatewaySnapshot {
		t.Fatalf("snapshot gateway = %#v", snapshotDelivery)
	}
	snapshot := environment.snapshot
	if err := environment.service.ReportHostGatewayResult(
		"test.host", environment.lease.LeaseID,
		controlplane.HostGatewayResult{
			GatewayRequestID: snapshotDelivery.GatewayRequestID,
			Snapshot:         &snapshot,
		},
	); err != nil {
		t.Fatal(err)
	}
	var confirmed OperationOutput
	decodeAsyncTool(t, confirmation, &confirmed)
	if confirmed.Operation.Status != controlplane.OperationQueued ||
		confirmed.Operation.PolicyDecision == nil ||
		confirmed.Operation.PolicyDecision.Result != policy.Allow {
		t.Fatalf("confirmed operation = %#v", confirmed.Operation)
	}
}

func TestSubmitActionSchemaDoesNotExposeAuthorityOrEffectFields(t *testing.T) {
	environment := newMCPEnvironment(t, host.RiskLow)
	session := connectClient(t, environment.service, actionPrincipal())
	tools, err := session.ListTools(testContext(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name != "submit_actor_action" {
			continue
		}
		payload, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		text := string(payload)
		for _, forbidden := range []string{
			"principal", "granted_scopes", "effect_preview", "policy_decision",
		} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("submit schema exposes %q: %s", forbidden, text)
			}
		}
		return
	}
	t.Fatal("submit_actor_action tool is missing")
}

type mcpEnvironment struct {
	service  *controlplane.Service
	lease    controlplane.HostLease
	registry *host.Registry
	spec     host.CapabilitySpec
	snapshot controlplane.ActionHostSnapshot
	risk     host.RiskLevel
	mu       sync.Mutex
	binds    int
}

func newMCPEnvironment(t *testing.T, risk host.RiskLevel) *mcpEnvironment {
	t.Helper()
	manifest := testManifest()
	registry, err := host.NewRegistry(manifest)
	if err != nil {
		t.Fatal(err)
	}
	emptySchema, err := host.NewSchema([]byte(`{
  "$schema":"https://json-schema.org/draft/2020-12/schema",
  "type":"object",
  "additionalProperties":false,
  "properties":{"distance":{"type":"integer","minimum":1,"maximum":4}}
}`))
	if err != nil {
		t.Fatal(err)
	}
	spec, err := registry.RegisterSpec(host.CapabilitySpec{
		Capability:  host.CapabilityRef{ID: "test.actor.move", Version: "2.0.0"},
		Description: "Move the test actor through the authoritative adapter.",
		Input:       emptySchema, Output: emptySchema, EffectSchema: emptySchema,
		Kind: host.CapabilityAtomic, Execution: host.ExecutionImmediate,
		Cancellation: host.CancellationUnsupported, RiskFloor: host.RiskLow,
		RequiredDurability: host.DurabilityAdvisory,
		ExecutionBudget:    host.Duration{Clock: host.ClockStep, Value: 100},
		MaxInputBytes:      1_024, MaxOutputBytes: 1_024, MaxEffects: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	engine, err := policy.New(policy.Config{
		Revision: 1, Profile: policy.ProfileOpen,
		KnownEffectKinds:   []string{"world.position"},
		KnownScopes:        []string{"world.public"},
		ConfirmationTTL:    policy.ConfirmationDurations{Step: 20},
		ConfirmationScopes: []string{"rin.policy.confirm"},
	})
	if err != nil {
		t.Fatal(err)
	}
	random := make([]byte, 8_192)
	for index := range random {
		random[index] = byte(index)
	}
	service := controlplane.New(controlplane.Options{
		Now:    func() time.Time { return time.UnixMilli(1_000_000) },
		Random: bytes.NewReader(random), PolicyEngine: engine,
	})
	lease, err := service.RegisterHost(controlplane.HostRegistration{
		ContractVersion: controlplane.ContractVersion,
		HostID:          "test.host", InstanceID: "instance.one",
		Manifest: manifest, LeaseTTLMillis: 5_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	epoch := testEpoch()
	publication := controlplane.WorldPublication{
		WorldID: "world.one", DisplayName: "Test World", Sequence: 1,
		Actors: []controlplane.ActorPublication{{
			ActorID: "actor.one", OwnerPrincipalID: "player.one",
			DisplayName: "Companion", ObservationSeq: 1, Epoch: epoch,
			Authority: &controlplane.DecisionAuthority{
				Source:                controlplane.DecisionExternal,
				ControllerPrincipalID: "player.one", Revision: 1,
				PersonaMode: controlplane.PersonaCharacterBound,
			},
			State: json.RawMessage(`{"status":"ready"}`),
			Observation: &host.ObservationEnvelope{
				ObservationID: "observation.actor.one.1",
				HostID:        "test.host", WorldID: "world.one", ActorID: "actor.one",
				Epoch: epoch, Sequence: 1,
				ObservedAt: host.Timepoint{Clock: host.ClockStep, Value: 10},
				Schema: host.SchemaRef{
					ID: "schema.actor.observation", Version: "1.0.0",
					SHA256: strings.Repeat("a", 64),
				},
				Payload: json.RawMessage(`{"status":"ready"}`),
				Facts: []host.ObservationFact{{
					FactID: "fact.actor.status", Kind: "actor.status",
					Value: json.RawMessage(`"ready"`),
				}},
			},
			Capabilities: &host.CapabilitySnapshot{
				Revision: 1, Specs: []host.CapabilitySpec{spec},
			},
		}},
	}
	if err := service.PublishWorld("test.host", lease.LeaseID, publication); err != nil {
		t.Fatalf("PublishWorld: %v", err)
	}
	return &mcpEnvironment{
		service: service, lease: lease, registry: registry, spec: spec, risk: risk,
		snapshot: controlplane.ActionHostSnapshot{
			Now:   host.Timepoint{Clock: host.ClockStep, Value: 10},
			Epoch: epoch, ObservationSeq: 1,
		},
	}
}

func (environment *mcpEnvironment) bind(
	t *testing.T,
	request host.ActionRequest,
) controlplane.ActionBindingResult {
	t.Helper()
	environment.mu.Lock()
	environment.binds++
	index := environment.binds
	environment.mu.Unlock()
	action, err := environment.registry.SealBinding(
		request,
		host.BindingDraft{
			BindingID: fmt.Sprintf("binding.mcp.%d", index),
			Effects: []host.Effect{{
				EffectID: fmt.Sprintf("effect.mcp.%d", index),
				Kind:     "world.position", Operation: host.EffectOperationUpdate,
				Tags: []string{"actor.movement"}, Ownership: host.OwnershipActor,
				Scope: "world.public", Quantity: 1, Unit: "step",
				Reversible: true, Risk: environment.risk,
				Attributes: json.RawMessage(`{}`),
			}},
			ValidUntil: host.Timepoint{Clock: host.ClockStep, Value: 60},
		},
		environment.snapshot.Now,
		environment.snapshot.Epoch,
		environment.snapshot.ObservationSeq,
	)
	if err != nil {
		t.Fatal(err)
	}
	return controlplane.ActionBindingResult{
		Action: action, Snapshot: environment.snapshot,
	}
}

func (environment *mcpEnvironment) actionArguments(
	requestID, idempotencyKey string,
) map[string]any {
	return mergeMaps(actorTargetArguments(), map[string]any{
		"request_id": requestID, "controller_id": "controller.mcp.one",
		"capability_id":      environment.spec.Capability.ID,
		"capability_version": environment.spec.Capability.Version,
		"spec_digest":        environment.spec.Digest, "arguments": map[string]any{},
		"expected_epoch":       environment.snapshot.Epoch,
		"observation_sequence": environment.snapshot.ObservationSeq,
		"idempotency_key":      idempotencyKey,
	})
}

func acquireControllerThroughMCP(
	t *testing.T,
	session *mcp.ClientSession,
) ControllerOutput {
	t.Helper()
	var controller ControllerOutput
	callTool(t, session, "acquire_actor_control", mergeMaps(
		actorTargetArguments(), map[string]any{
			"controller_id": "controller.mcp.one", "lease_ttl_millis": 5_000,
		},
	), &controller)
	if controller.Controller.LeaseID == "" {
		t.Fatalf("acquire_actor_control = %#v", controller)
	}
	return controller
}

func pollGateway(
	t *testing.T,
	environment *mcpEnvironment,
) controlplane.HostGatewayRequest {
	t.Helper()
	batch := pollHost(t, environment, 1)
	if len(batch.GatewayRequests) != 1 || len(batch.Requests) != 0 {
		t.Fatalf("Host gateway poll = %#v", batch)
	}
	return batch.GatewayRequests[0].Request
}

func pollHost(
	t *testing.T,
	environment *mcpEnvironment,
	limit int,
) controlplane.HostControlBatch {
	t.Helper()
	batch, err := environment.service.PollHost(
		testContext(t), "test.host", environment.lease.LeaseID, limit,
	)
	if err != nil {
		t.Fatalf("PollHost: %v", err)
	}
	return batch
}

type asyncToolResult struct {
	result *mcp.CallToolResult
	err    error
}

func callToolAsync(
	session *mcp.ClientSession,
	name string,
	arguments any,
) <-chan asyncToolResult {
	result := make(chan asyncToolResult, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		called, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name: name, Arguments: arguments,
		})
		result <- asyncToolResult{result: called, err: err}
	}()
	return result
}

func decodeAsyncTool(t *testing.T, result <-chan asyncToolResult, output any) {
	t.Helper()
	select {
	case called := <-result:
		if called.err != nil || called.result == nil || called.result.IsError {
			t.Fatalf("async tool result = %#v, %v", called.result, called.err)
		}
		decodeStructured(t, called.result, output)
	case <-time.After(6 * time.Second):
		t.Fatal("async MCP tool did not complete")
	}
}

func connectClient(
	t *testing.T,
	service *controlplane.Service,
	principal host.Principal,
) *mcp.ClientSession {
	t.Helper()
	gateway, err := New(service, principal)
	if err != nil {
		t.Fatalf("New gateway: %v", err)
	}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := gateway.Server().Connect(testContext(t), serverTransport, nil)
	if err != nil {
		t.Fatalf("connect server: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcp.NewClient(
		&mcp.Implementation{Name: "rin-test", Version: "1.0.0"}, nil,
	)
	clientSession, err := client.Connect(testContext(t), clientTransport, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })
	return clientSession
}

func callTool(
	t *testing.T,
	session *mcp.ClientSession,
	name string,
	arguments any,
	output any,
) {
	t.Helper()
	result, err := session.CallTool(testContext(t), &mcp.CallToolParams{
		Name: name, Arguments: arguments,
	})
	if err != nil || result.IsError {
		var messages []string
		if result != nil {
			for _, content := range result.Content {
				if text, ok := content.(*mcp.TextContent); ok {
					messages = append(messages, text.Text)
				}
			}
		}
		t.Fatalf("CallTool(%s) = %#v, messages=%#v, %v", name, result, messages, err)
	}
	decodeStructured(t, result, output)
}

func decodeStructured(t *testing.T, result *mcp.CallToolResult, output any) {
	t.Helper()
	payload, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(output); err != nil {
		t.Fatalf("decode structured output: %v", err)
	}
}

func callRawMCP(
	t *testing.T,
	connection mcp.Connection,
	idValue int64,
	method string,
	params any,
) json.RawMessage {
	t.Helper()
	payload, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	id, err := jsonrpc.MakeID(float64(idValue))
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.Write(testContext(t), &jsonrpc.Request{
		ID: id, Method: method, Params: payload,
	}); err != nil {
		t.Fatal(err)
	}
	message, err := connection.Read(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	response, ok := message.(*jsonrpc.Response)
	if !ok || response.Error != nil {
		t.Fatalf("%s response = %#v", method, message)
	}
	return response.Result
}

func toolNames(tools []*mcp.Tool) []string {
	names := make([]string, len(tools))
	for index, tool := range tools {
		names[index] = tool.Name
	}
	slices.Sort(names)
	return names
}

func readPrincipal() host.Principal {
	return host.Principal{
		ID: "player.one", GrantedScopes: []string{controlplane.ScopeActorRead},
	}
}

func actionPrincipal() host.Principal {
	return host.Principal{
		ID: "player.one",
		GrantedScopes: []string{
			controlplane.ScopeActorRead,
			controlplane.ScopeActorControl,
			controlplane.ScopeActorExecute,
			controlplane.ScopeOperationCancel,
			"rin.policy.confirm",
		},
	}
}

func actorTargetArguments() map[string]any {
	return map[string]any{
		"host_id": "test.host", "world_id": "world.one", "actor_id": "actor.one",
	}
}

func mergeMaps(left, right map[string]any) map[string]any {
	result := mapsClone(left)
	for key, value := range right {
		result[key] = value
	}
	return result
}

func mapsClone(value map[string]any) map[string]any {
	cloned := make(map[string]any, len(value))
	for key, item := range value {
		cloned[key] = item
	}
	return cloned
}

func testEpoch() host.Epoch {
	return host.Epoch{
		SessionID: "session.one", WorldID: "world.one",
		Host: 1, World: 1, Timeline: 1,
	}
}

func testManifest() host.HostManifest {
	return host.HostManifest{
		ContractVersion: host.ContractVersion,
		AdapterID:       "test.adapter", AdapterVersion: "1.0.0",
		EngineID: "test.engine", EngineVersion: "1",
		Runtime: "go", Platform: "test", Headless: true,
		Authority:           host.AuthorityServer,
		Deployment:          host.DeploymentLoopbackSidecar,
		Control:             host.ControlSemantic,
		ClockModes:          []host.ClockMode{host.ClockStep},
		DecisionModes:       []host.DecisionMode{host.DecisionAsynchronous},
		MaxConcurrentActors: 4,
		Durability: host.Durability{
			Profile: host.DurabilityAdvisory, StableIdentity: true,
		},
	}
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}
