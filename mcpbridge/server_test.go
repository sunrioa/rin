package mcpbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
)

func TestGatewayNegotiatesCurrentProtocolAndReadsPublishedState(t *testing.T) {
	service := publishedService(t)
	session := connectClient(t, service, host.Principal{
		ID:            "player.one",
		GrantedScopes: []string{controlplane.ScopeActorRead},
	})

	discovery := session.InitializeResult()
	if discovery == nil || discovery.ProtocolVersion != ProtocolVersion {
		t.Fatalf("discovery result = %#v", discovery)
	}

	tools, err := session.ListTools(testContext(t), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := make([]string, len(tools.Tools))
	for index, tool := range tools.Tools {
		names[index] = tool.Name
		if tool.Annotations == nil ||
			!tool.Annotations.ReadOnlyHint ||
			!tool.Annotations.IdempotentHint {
			t.Fatalf("tool %q is not marked read-only and idempotent", tool.Name)
		}
	}
	slices.Sort(names)
	expectedNames := []string{
		"get_actor_state",
		"get_operation",
		"list_actor_offers",
		"list_actors",
		"list_worlds",
		"wait_actor_update",
		"wait_operation",
	}
	if !slices.Equal(names, expectedNames) {
		t.Fatalf("tool names = %#v", names)
	}

	var worlds ListWorldsOutput
	callTool(t, session, "list_worlds", map[string]any{}, &worlds)
	if len(worlds.Worlds) != 1 ||
		worlds.Worlds[0].WorldID != "world.one" ||
		!worlds.Worlds[0].Online {
		t.Fatalf("list_worlds = %#v", worlds)
	}

	actorInput := map[string]any{
		"host_id":  "test.host",
		"world_id": "world.one",
	}
	var actors ListActorsOutput
	callTool(t, session, "list_actors", actorInput, &actors)
	if len(actors.Actors) != 1 ||
		actors.Actors[0].ActorID != "actor.one" {
		t.Fatalf("list_actors = %#v", actors)
	}

	var state GetActorStateOutput
	callTool(t, session, "get_actor_state", map[string]any{
		"host_id":  "test.host",
		"world_id": "world.one",
		"actor_id": "actor.one",
	}, &state)
	if state.Actor.DisplayName != "Companion" ||
		state.Actor.State["status"] != "ready" {
		t.Fatalf("get_actor_state = %#v", state)
	}
	var update WaitActorUpdateOutput
	callTool(t, session, "wait_actor_update", map[string]any{
		"host_id":                  "test.host",
		"world_id":                 "world.one",
		"actor_id":                 "actor.one",
		"after_observation_seq":    0,
		"after_authority_revision": 0,
		"wait_millis":              0,
	}, &update)
	if !update.Changed ||
		update.Actor.ObservationSeq != state.Actor.ObservationSeq {
		t.Fatalf("wait_actor_update = %#v", update)
	}
	callTool(t, session, "wait_actor_update", map[string]any{
		"host_id":                  "test.host",
		"world_id":                 "world.one",
		"actor_id":                 "actor.one",
		"after_observation_seq":    state.Actor.ObservationSeq,
		"after_authority_revision": state.Actor.DecisionAuthority.Revision,
		"wait_millis":              0,
	}, &update)
	if update.Changed {
		t.Fatalf("unchanged actor cursor reported an update: %#v", update)
	}

	var offers ListActorOffersOutput
	callTool(t, session, "list_actor_offers", map[string]any{
		"host_id":  "test.host",
		"world_id": "world.one",
		"actor_id": "actor.one",
	}, &offers)
	if len(offers.Offers) != 1 ||
		offers.Offers[0].OfferID != "offer.follow" ||
		offers.Offers[0].Arguments["distance"] != json.Number("2") {
		t.Fatalf("list_actor_offers = %#v", offers)
	}
}

func TestGatewayAcceptsLegacyProtocolThroughOfficialSDK(t *testing.T) {
	const legacyProtocolVersion = "2025-11-25"

	service := publishedService(t)
	gateway, err := New(service, host.Principal{
		ID:            "player.one",
		GrantedScopes: []string{controlplane.ScopeActorRead},
	})
	if err != nil {
		t.Fatalf("New gateway: %v", err)
	}

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := gateway.Server().Connect(
		testContext(t),
		serverTransport,
		nil,
	)
	if err != nil {
		t.Fatalf("connect server: %v", err)
	}
	t.Cleanup(func() {
		_ = serverSession.Close()
	})

	connection, err := clientTransport.Connect(testContext(t))
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	t.Cleanup(func() {
		_ = connection.Close()
	})

	payload := callLegacyMCP(t, connection, 1, "initialize", &mcp.InitializeParams{
		ProtocolVersion: legacyProtocolVersion,
		Capabilities:    &mcp.ClientCapabilities{},
		ClientInfo: &mcp.Implementation{
			Name:    "rin-legacy-test",
			Version: "1.0.0",
		},
	})
	var initialized mcp.InitializeResult
	if err := json.Unmarshal(payload, &initialized); err != nil {
		t.Fatalf("decode initialize result: %v", err)
	}
	if initialized.ProtocolVersion != legacyProtocolVersion {
		t.Fatalf("legacy negotiation result = %#v", initialized)
	}

	params, err := json.Marshal(&mcp.InitializedParams{})
	if err != nil {
		t.Fatalf("marshal initialized notification: %v", err)
	}
	if err := connection.Write(testContext(t), &jsonrpc.Request{
		Method: "notifications/initialized",
		Params: params,
	}); err != nil {
		t.Fatalf("send initialized notification: %v", err)
	}

	payload = callLegacyMCP(t, connection, 2, "tools/list", &mcp.ListToolsParams{})
	var tools mcp.ListToolsResult
	if err := json.Unmarshal(payload, &tools); err != nil {
		t.Fatalf("decode tools result: %v", err)
	}
	if len(tools.Tools) == 0 {
		t.Fatal("legacy tools/list returned no tools")
	}
}

func TestGatewayDoesNotRevealAnotherPrincipalsWorlds(t *testing.T) {
	session := connectClient(t, publishedService(t), host.Principal{
		ID:            "player.two",
		GrantedScopes: []string{controlplane.ScopeActorRead},
	})

	var worlds ListWorldsOutput
	callTool(t, session, "list_worlds", map[string]any{}, &worlds)
	if len(worlds.Worlds) != 0 {
		t.Fatalf("visible worlds = %#v", worlds.Worlds)
	}
}

func TestGatewayRegistersScopedWriteToolsAndQueuesOperations(t *testing.T) {
	service, lease := publishedServiceWithLease(t)
	principal := host.Principal{
		ID: "player.one",
		GrantedScopes: []string{
			controlplane.ScopeActorRead,
			controlplane.ScopeActorConverse,
			controlplane.ScopeActorDirect,
			controlplane.ScopeActorSpeak,
			controlplane.ScopeActorExecute,
			controlplane.ScopeOperationCancel,
		},
	}
	session := connectClient(t, service, principal)
	tools, err := session.ListTools(testContext(t), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := make([]string, len(tools.Tools))
	for index, tool := range tools.Tools {
		names[index] = tool.Name
	}
	slices.Sort(names)
	expected := []string{
		"cancel_operation",
		"execute_actor_offer",
		"get_actor_state",
		"get_operation",
		"list_actor_offers",
		"list_actors",
		"list_worlds",
		"send_actor_directive",
		"send_actor_message",
		"speak_as_actor",
		"wait_actor_update",
		"wait_operation",
	}
	if !slices.Equal(names, expected) {
		t.Fatalf("scoped tool names = %#v", names)
	}

	messageInput := map[string]any{
		"request_id": "request.mcp.message",
		"host_id":    "test.host",
		"world_id":   "world.one",
		"actor_id":   "actor.one",
		"text":       "Can you hear me?",
	}
	var message OperationOutput
	callTool(t, session, "send_actor_message", messageInput, &message)
	if message.Operation.Status != controlplane.OperationQueued ||
		message.Operation.Kind != controlplane.ControlMessage ||
		message.Operation.Terminal ||
		message.Operation.ExecutionConfirmed {
		t.Fatalf("message operation = %#v", message.Operation)
	}
	var retry OperationOutput
	callTool(t, session, "send_actor_message", messageInput, &retry)
	if retry.Operation.OperationID != message.Operation.OperationID {
		t.Fatalf("message retry = %#v", retry.Operation)
	}
	batch, err := service.PollHost(
		testContext(t),
		"test.host",
		lease.LeaseID,
		1,
	)
	if err != nil || len(batch.Requests) != 1 {
		t.Fatalf("PollHost = %#v, %v", batch, err)
	}
	if err := service.AcknowledgeHost(
		"test.host",
		lease.LeaseID,
		controlplane.HostAcknowledgement{
			OperationID: message.Operation.OperationID,
			Accepted:    true,
		},
	); err != nil {
		t.Fatalf("AcknowledgeHost: %v", err)
	}
	output := json.RawMessage(
		`{"type":"actor_turn","reply":"I heard you.","capability":"activity.wait"}`,
	)
	if err := service.ReportHostResult(
		"test.host",
		lease.LeaseID,
		host.ActionOutcome{
			OperationID: message.Operation.OperationID,
			Status:      host.ActionSucceeded,
			Summary:     "The actor completed the turn.",
			Epoch: host.Epoch{
				SessionID: "session.one",
				WorldID:   "world.one",
				Host:      1,
				World:     1,
				Timeline:  1,
			},
			WorldSeq:   2,
			OccurredAt: host.Timepoint{Clock: host.ClockStep, Value: 2},
		},
		output,
	); err != nil {
		t.Fatalf("ReportHostResult: %v", err)
	}

	var directive OperationOutput
	callTool(t, session, "send_actor_directive", map[string]any{
		"request_id": "request.mcp.directive",
		"host_id":    "test.host",
		"world_id":   "world.one",
		"actor_id":   "actor.one",
		"text":       "Wait near the entrance.",
	}, &directive)
	if directive.Operation.Kind != controlplane.ControlDirective {
		t.Fatalf("directive operation = %#v", directive.Operation)
	}

	var utterance OperationOutput
	callTool(t, session, "speak_as_actor", map[string]any{
		"request_id": "request.mcp.utterance",
		"host_id":    "test.host",
		"world_id":   "world.one",
		"actor_id":   "actor.one",
		"turn_id":    "turn.mcp.one",
		"text":       "I can help with that.",
	}, &utterance)
	if utterance.Operation.Kind != controlplane.ControlUtterance ||
		utterance.Operation.TurnID != "turn.mcp.one" {
		t.Fatalf("utterance operation = %#v", utterance.Operation)
	}

	var offered OperationOutput
	callTool(t, session, "execute_actor_offer", map[string]any{
		"request_id": "request.mcp.offer",
		"host_id":    "test.host",
		"world_id":   "world.one",
		"actor_id":   "actor.one",
		"offer_id":   "offer.follow",
		"turn_id":    "turn.mcp.one",
	}, &offered)
	if offered.Operation.Kind != controlplane.ControlOffer ||
		offered.Operation.TurnID != "turn.mcp.one" {
		t.Fatalf("offer operation = %#v", offered.Operation)
	}

	var fetched OperationOutput
	callTool(t, session, "get_operation", map[string]any{
		"operation_id": message.Operation.OperationID,
	}, &fetched)
	if fetched.Operation.OperationID != message.Operation.OperationID {
		t.Fatalf("get_operation = %#v", fetched.Operation)
	}
	if fetched.Operation.Output["reply"] != "I heard you." ||
		fetched.Operation.Output["capability"] != "activity.wait" ||
		!fetched.Operation.Terminal ||
		!fetched.Operation.ExecutionConfirmed {
		t.Fatalf("get_operation output = %#v", fetched.Operation.Output)
	}
	var operationUpdate OperationUpdateOutput
	callTool(t, session, "wait_operation", map[string]any{
		"operation_id": message.Operation.OperationID,
		"after_cursor": message.Operation.Cursor,
		"wait_millis":  0,
	}, &operationUpdate)
	if !operationUpdate.Changed ||
		!operationUpdate.Operation.ExecutionConfirmed {
		t.Fatalf("wait_operation = %#v", operationUpdate)
	}

	var cancelled OperationOutput
	callTool(t, session, "cancel_operation", map[string]any{
		"operation_id": directive.Operation.OperationID,
	}, &cancelled)
	if cancelled.Operation.Status != controlplane.OperationCancelled {
		t.Fatalf("cancel_operation = %#v", cancelled.Operation)
	}

	changed := mapsClone(messageInput)
	changed["text"] = "Different retry payload."
	result, err := session.CallTool(testContext(t), &mcp.CallToolParams{
		Name:      "send_actor_message",
		Arguments: changed,
	})
	if err != nil {
		t.Fatalf("changed retry CallTool: %v", err)
	}
	if !result.IsError {
		t.Fatalf("changed retry result = %#v", result)
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
	serverSession, err := gateway.Server().Connect(
		testContext(t),
		serverTransport,
		nil,
	)
	if err != nil {
		t.Fatalf("connect server: %v", err)
	}
	t.Cleanup(func() {
		_ = serverSession.Close()
	})

	client := mcp.NewClient(
		&mcp.Implementation{Name: "rin-test", Version: "1.0.0"},
		nil,
	)
	clientSession, err := client.Connect(testContext(t), clientTransport, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	t.Cleanup(func() {
		_ = clientSession.Close()
	})
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
		Name:      name,
		Arguments: arguments,
	})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	if result.IsError {
		t.Fatalf("CallTool(%s) returned an error: %#v", name, result.Content)
	}
	payload, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal %s output: %v", name, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(output); err != nil {
		t.Fatalf("decode %s output: %v", name, err)
	}
}

func callLegacyMCP(
	t *testing.T,
	connection mcp.Connection,
	idValue int64,
	method string,
	params any,
) json.RawMessage {
	t.Helper()
	payload, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal %s params: %v", method, err)
	}
	id, err := jsonrpc.MakeID(float64(idValue))
	if err != nil {
		t.Fatalf("make %s request ID: %v", method, err)
	}
	if err := connection.Write(testContext(t), &jsonrpc.Request{
		ID:     id,
		Method: method,
		Params: payload,
	}); err != nil {
		t.Fatalf("write %s request: %v", method, err)
	}
	message, err := connection.Read(testContext(t))
	if err != nil {
		t.Fatalf("read %s response: %v", method, err)
	}
	response, ok := message.(*jsonrpc.Response)
	if !ok {
		t.Fatalf("%s response type = %T", method, message)
	}
	if response.Error != nil {
		t.Fatalf("%s response error: %v", method, response.Error)
	}
	return response.Result
}

func publishedService(t *testing.T) *controlplane.Service {
	service, _ := publishedServiceWithLease(t)
	return service
}

func publishedServiceWithLease(
	t *testing.T,
) (*controlplane.Service, controlplane.HostLease) {
	t.Helper()
	random := make([]byte, 4_096)
	for index := range random {
		random[index] = byte(index)
	}
	service := controlplane.New(controlplane.Options{
		Now:    func() time.Time { return time.UnixMilli(1_000_000) },
		Random: bytes.NewReader(random),
	})
	manifest := host.HostManifest{
		ContractVersion:     host.ContractVersion,
		AdapterID:           "test.adapter",
		AdapterVersion:      "1.0.0",
		EngineID:            "test.engine",
		EngineVersion:       "1",
		Runtime:             "go",
		Platform:            "test",
		Headless:            true,
		Authority:           host.AuthorityServer,
		Deployment:          host.DeploymentLoopbackSidecar,
		Control:             host.ControlSemantic,
		ClockModes:          []host.ClockMode{host.ClockStep},
		DecisionModes:       []host.DecisionMode{host.DecisionAsynchronous},
		MaxConcurrentActors: 4,
		Durability: host.Durability{
			Profile:        host.DurabilityAdvisory,
			StableIdentity: true,
		},
	}
	lease, err := service.RegisterHost(controlplane.HostRegistration{
		ContractVersion: controlplane.ContractVersion,
		HostID:          "test.host",
		InstanceID:      "instance.one",
		Manifest:        manifest,
		LeaseTTLMillis:  5_000,
	})
	if err != nil {
		t.Fatalf("RegisterHost: %v", err)
	}
	epoch := host.Epoch{
		SessionID: "session.one",
		WorldID:   "world.one",
		Host:      1,
		World:     1,
		Timeline:  1,
	}
	err = service.PublishWorld(
		"test.host",
		lease.LeaseID,
		controlplane.WorldPublication{
			WorldID:     "world.one",
			DisplayName: "Test World",
			Sequence:    1,
			Actors: []controlplane.ActorPublication{{
				ActorID:          "actor.one",
				OwnerPrincipalID: "player.one",
				DisplayName:      "Companion",
				ObservationSeq:   1,
				Epoch:            epoch,
				State:            json.RawMessage(`{"status":"ready"}`),
				Offers: []host.ActionOffer{{
					OfferID:          "offer.follow",
					DecisionWindowID: "window.one",
					ActorID:          "actor.one",
					Capability: host.CapabilityRef{
						ID:      "movement.follow",
						Version: "1.0.0",
					},
					DescriptorDigest: strings.Repeat("a", 64),
					Description:      "Follow the owner",
					Arguments:        json.RawMessage(`{"distance":2}`),
					ExpectedEpoch:    epoch,
					ObservationSeq:   1,
					Deadline: host.Timepoint{
						Clock: host.ClockStep,
						Value: 100,
					},
				}},
			}},
		},
	)
	if err != nil {
		t.Fatalf("PublishWorld: %v", err)
	}
	return service, lease
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func mapsClone(value map[string]any) map[string]any {
	cloned := make(map[string]any, len(value))
	for key, item := range value {
		cloned[key] = item
	}
	return cloned
}
