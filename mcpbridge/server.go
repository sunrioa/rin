package mcpbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
)

// Gateway binds one configured external principal to Control Plane tools.
type Gateway struct {
	service   *controlplane.Service
	principal host.Principal
	server    *mcp.Server
}

// New creates a strict MCP 2026-07-28 read gateway.
func New(
	service *controlplane.Service,
	principal host.Principal,
) (*Gateway, error) {
	if service == nil {
		return nil, errorsInvalid("service is required")
	}
	if err := host.ValidatePrincipal(principal); err != nil {
		return nil, errorsInvalid("principal: " + err.Error())
	}
	if !scopeGranted(principal, controlplane.ScopeActorRead) &&
		!scopeGranted(principal, controlplane.ScopeHostAdmin) {
		return nil, errorsInvalid("principal requires actor.read or host.admin")
	}
	gateway := &Gateway{
		service:   service,
		principal: clonePrincipal(principal),
	}
	gateway.server = mcp.NewServer(
		&mcp.Implementation{Name: "rin", Version: "0.7.0"},
		&mcp.ServerOptions{
			Instructions: "Inspect only host-published actor state and currently bound action offers.",
			Capabilities: &mcp.ServerCapabilities{},
		},
	)
	gateway.addReadTools()
	return gateway, nil
}

// Server returns the configured official SDK server.
func (gateway *Gateway) Server() *mcp.Server {
	return gateway.server
}

// Run serves MCP over one transport while advertising only 2026-07-28.
func (gateway *Gateway) Run(ctx context.Context, transport mcp.Transport) error {
	return gateway.server.Run(ctx, StrictTransport{Base: transport})
}

func (gateway *Gateway) addReadTools() {
	annotations := &mcp.ToolAnnotations{
		ReadOnlyHint:   true,
		IdempotentHint: true,
	}
	mcp.AddTool(gateway.server, &mcp.Tool{
		Name:        "list_worlds",
		Description: "List game worlds visible to the configured principal.",
		Annotations: annotations,
	}, gateway.listWorlds)
	mcp.AddTool(gateway.server, &mcp.Tool{
		Name:        "list_actors",
		Description: "List visible actors in one host-published world.",
		Annotations: annotations,
	}, gateway.listActors)
	mcp.AddTool(gateway.server, &mcp.Tool{
		Name:        "get_actor_state",
		Description: "Read one actor's current redacted host-published state.",
		Annotations: annotations,
	}, gateway.getActorState)
	mcp.AddTool(gateway.server, &mcp.Tool{
		Name:        "list_actor_offers",
		Description: "List exact unexpired action offers currently published for an actor.",
		Annotations: annotations,
	}, gateway.listActorOffers)
}

func (gateway *Gateway) listWorlds(
	_ context.Context,
	_ *mcp.CallToolRequest,
	_ ListWorldsInput,
) (*mcp.CallToolResult, ListWorldsOutput, error) {
	views, err := gateway.service.ListWorlds(gateway.principal)
	if err != nil {
		return nil, ListWorldsOutput{}, err
	}
	output := ListWorldsOutput{Worlds: make([]World, len(views))}
	for index, view := range views {
		output.Worlds[index] = World{
			HostID:                   view.HostID,
			WorldID:                  view.WorldID,
			DisplayName:              view.DisplayName,
			Sequence:                 view.Sequence,
			Online:                   view.Online,
			LeaseExpiresAtUnixMillis: view.LeaseExpiresAtMillis,
		}
	}
	return nil, output, nil
}

func (gateway *Gateway) listActors(
	_ context.Context,
	_ *mcp.CallToolRequest,
	input ListActorsInput,
) (*mcp.CallToolResult, ListActorsOutput, error) {
	views, err := gateway.service.ListActors(
		gateway.principal, input.HostID, input.WorldID,
	)
	if err != nil {
		return nil, ListActorsOutput{}, err
	}
	output := ListActorsOutput{Actors: make([]Actor, len(views))}
	for index, view := range views {
		output.Actors[index], err = convertActor(view)
		if err != nil {
			return nil, ListActorsOutput{}, err
		}
	}
	return nil, output, nil
}

func (gateway *Gateway) getActorState(
	_ context.Context,
	_ *mcp.CallToolRequest,
	input GetActorStateInput,
) (*mcp.CallToolResult, GetActorStateOutput, error) {
	view, err := gateway.service.GetActor(
		gateway.principal, input.HostID, input.WorldID, input.ActorID,
	)
	if err != nil {
		return nil, GetActorStateOutput{}, err
	}
	actor, err := convertActor(view)
	if err != nil {
		return nil, GetActorStateOutput{}, err
	}
	return nil, GetActorStateOutput{Actor: actor}, nil
}

func (gateway *Gateway) listActorOffers(
	_ context.Context,
	_ *mcp.CallToolRequest,
	input ListActorOffersInput,
) (*mcp.CallToolResult, ListActorOffersOutput, error) {
	offers, err := gateway.service.ListActorOffers(
		gateway.principal, input.HostID, input.WorldID, input.ActorID,
	)
	if err != nil {
		return nil, ListActorOffersOutput{}, err
	}
	output := ListActorOffersOutput{Offers: make([]Offer, len(offers))}
	for index, offer := range offers {
		arguments, err := decodeObject(offer.Arguments)
		if err != nil {
			return nil, ListActorOffersOutput{}, err
		}
		output.Offers[index] = Offer{
			OfferID:          offer.OfferID,
			DecisionWindowID: offer.DecisionWindowID,
			ActorID:          offer.ActorID,
			Capability:       offer.Capability,
			DescriptorDigest: offer.DescriptorDigest,
			Description:      offer.Description,
			Arguments:        arguments,
			Targets:          append([]host.HostRef(nil), offer.Targets...),
			ExpectedEpoch:    offer.ExpectedEpoch,
			ObservationSeq:   offer.ObservationSeq,
			Deadline:         offer.Deadline,
		}
	}
	return nil, output, nil
}

func convertActor(view controlplane.ActorView) (Actor, error) {
	state, err := decodeObject(view.State)
	if err != nil {
		return Actor{}, err
	}
	return Actor{
		HostID:                   view.HostID,
		WorldID:                  view.WorldID,
		ActorID:                  view.ActorID,
		DisplayName:              view.DisplayName,
		ObservationSeq:           view.ObservationSeq,
		Epoch:                    view.Epoch,
		State:                    state,
		Online:                   view.Online,
		LeaseExpiresAtUnixMillis: view.LeaseExpiresAtMillis,
	}, nil
}

func decodeObject(raw json.RawMessage) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode host-published object: %w", err)
	}
	return value, nil
}

func clonePrincipal(value host.Principal) host.Principal {
	value.GrantedScopes = append([]string(nil), value.GrantedScopes...)
	return value
}

func scopeGranted(principal host.Principal, scope string) bool {
	for _, granted := range principal.GrantedScopes {
		if granted == scope {
			return true
		}
	}
	return false
}

func errorsInvalid(message string) error {
	return fmt.Errorf("invalid MCP gateway configuration: %s", message)
}
