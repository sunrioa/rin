package mcpbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
)

func TestStrictTransportSupportsOnlyCurrentProtocol(t *testing.T) {
	transport := StrictTransport{}
	if !transport.SupportsProtocolVersion(ProtocolVersion) {
		t.Fatalf("%s must be supported", ProtocolVersion)
	}
	for _, version := range []string{"2025-11-25", "2025-06-18", ""} {
		if transport.SupportsProtocolVersion(version) {
			t.Fatalf("legacy protocol %q was accepted", version)
		}
	}
}

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
		"list_actor_offers",
		"list_actors",
		"list_worlds",
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
		StrictTransport{Base: serverTransport},
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

func publishedService(t *testing.T) *controlplane.Service {
	t.Helper()
	service := controlplane.New(controlplane.Options{
		Now:    func() time.Time { return time.UnixMilli(1_000_000) },
		Random: bytes.NewReader(bytes.Repeat([]byte{7}, 64)),
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
	return service
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}
