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
	client    ControlClient
	principal host.Principal
	server    *mcp.Server
}

// New creates a scope-bounded MCP gateway.
func New(
	service *controlplane.Service,
	principal host.Principal,
) (*Gateway, error) {
	if service == nil {
		return nil, errorsInvalid("service is required")
	}
	return NewClient(&serviceClient{
		service:   service,
		principal: clonePrincipal(principal),
	}, principal)
}

// NewClient creates a gateway backed by a local or remote Control client.
func NewClient(
	client ControlClient,
	principal host.Principal,
) (*Gateway, error) {
	if client == nil {
		return nil, errorsInvalid("client is required")
	}
	if err := host.ValidatePrincipal(principal); err != nil {
		return nil, errorsInvalid("principal: " + err.Error())
	}
	if !hasControlScope(principal) {
		return nil, errorsInvalid("principal has no Control Plane scope")
	}
	gateway := &Gateway{
		client:    client,
		principal: clonePrincipal(principal),
	}
	gateway.server = mcp.NewServer(
		&mcp.Implementation{Name: "rin", Version: "0.7.0"},
		&mcp.ServerOptions{
			Instructions: "Inspect Host-published state and submit only the bounded operations allowed by the configured principal scopes.",
			Capabilities: &mcp.ServerCapabilities{},
		},
	)
	if gateway.granted(controlplane.ScopeActorRead) {
		gateway.addReadTools()
	}
	gateway.addOperationTools()
	if gateway.granted(controlplane.ScopeActorConverse) {
		gateway.addMessageTool()
	}
	if gateway.granted(controlplane.ScopeActorDirect) {
		gateway.addDirectiveTool()
	}
	if gateway.granted(controlplane.ScopeActorSpeak) {
		gateway.addUtteranceTool()
	}
	if gateway.granted(controlplane.ScopeActorExecute) {
		gateway.addExecuteOfferTool()
	}
	if gateway.granted(controlplane.ScopeOperationCancel) {
		gateway.addCancelTool()
	}
	return gateway, nil
}

// Server returns the configured official SDK server.
func (gateway *Gateway) Server() *mcp.Server {
	return gateway.server
}

// Run serves MCP using the official SDK's protocol negotiation.
func (gateway *Gateway) Run(ctx context.Context, transport mcp.Transport) error {
	return gateway.server.Run(ctx, transport)
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
		Name:        "wait_actor_update",
		Description: "Wait up to 25 seconds for a newer redacted actor observation or authority revision.",
		Annotations: annotations,
	}, gateway.waitActorUpdate)
	mcp.AddTool(gateway.server, &mcp.Tool{
		Name:        "list_actor_offers",
		Description: "List exact unexpired action offers currently published for an actor.",
		Annotations: annotations,
	}, gateway.listActorOffers)
}

func (gateway *Gateway) addOperationTools() {
	mcp.AddTool(gateway.server, &mcp.Tool{
		Name:        "get_operation",
		Description: "Read the current state and authoritative Host outcome of one submitted operation.",
		Annotations: readAnnotations(),
	}, gateway.getOperation)
}

func (gateway *Gateway) addMessageTool() {
	mcp.AddTool(gateway.server, &mcp.Tool{
		Name:        "send_actor_message",
		Description: "Send plain conversation to an actor without directly authorizing a world mutation.",
		Annotations: writeAnnotations(false),
	}, gateway.sendActorMessage)
}

func (gateway *Gateway) addDirectiveTool() {
	mcp.AddTool(gateway.server, &mcp.Tool{
		Name:        "send_actor_directive",
		Description: "Submit a negotiable goal that the actor and game Host may refuse.",
		Annotations: writeAnnotations(true),
	}, gateway.sendActorDirective)
}

func (gateway *Gateway) addUtteranceTool() {
	mcp.AddTool(gateway.server, &mcp.Tool{
		Name:        "speak_as_actor",
		Description: "Speak as an externally controlled actor; the Host records and renders the bounded utterance.",
		Annotations: writeAnnotations(false),
	}, gateway.speakAsActor)
}

func (gateway *Gateway) addExecuteOfferTool() {
	mcp.AddTool(gateway.server, &mcp.Tool{
		Name:        "execute_actor_offer",
		Description: "Select one exact currently published offer without supplying new action arguments.",
		Annotations: writeAnnotations(true),
	}, gateway.executeActorOffer)
}

func (gateway *Gateway) addCancelTool() {
	mcp.AddTool(gateway.server, &mcp.Tool{
		Name:        "cancel_operation",
		Description: "Request cancellation of one operation; cancellation does not imply rollback.",
		Annotations: writeAnnotations(false),
	}, gateway.cancelOperation)
}

func (gateway *Gateway) listWorlds(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	_ ListWorldsInput,
) (*mcp.CallToolResult, ListWorldsOutput, error) {
	views, err := gateway.client.ListWorlds(ctx)
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
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input ListActorsInput,
) (*mcp.CallToolResult, ListActorsOutput, error) {
	views, err := gateway.client.ListActors(
		ctx, input.HostID, input.WorldID,
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
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input GetActorStateInput,
) (*mcp.CallToolResult, GetActorStateOutput, error) {
	view, err := gateway.client.GetActor(
		ctx, input.HostID, input.WorldID, input.ActorID,
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

func (gateway *Gateway) waitActorUpdate(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input WaitActorUpdateInput,
) (*mcp.CallToolResult, WaitActorUpdateOutput, error) {
	update, err := gateway.client.WaitActor(
		ctx,
		controlplane.WaitActorInput{
			HostID:                 input.HostID,
			WorldID:                input.WorldID,
			ActorID:                input.ActorID,
			AfterObservationSeq:    input.AfterObservationSeq,
			AfterAuthorityRevision: input.AfterAuthorityRevision,
			WaitMillis:             input.WaitMillis,
		},
	)
	if err != nil {
		return nil, WaitActorUpdateOutput{}, err
	}
	actor, err := convertActor(update.Actor)
	if err != nil {
		return nil, WaitActorUpdateOutput{}, err
	}
	return nil, WaitActorUpdateOutput{
		Actor:   actor,
		Changed: update.Changed,
	}, nil
}

func (gateway *Gateway) listActorOffers(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input ListActorOffersInput,
) (*mcp.CallToolResult, ListActorOffersOutput, error) {
	offers, err := gateway.client.ListActorOffers(
		ctx, input.HostID, input.WorldID, input.ActorID,
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

func (gateway *Gateway) sendActorMessage(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input SendActorMessageInput,
) (*mcp.CallToolResult, OperationOutput, error) {
	operation, err := gateway.client.SendActorMessage(
		ctx,
		controlplane.ActorTextInput{
			RequestID: input.RequestID,
			HostID:    input.HostID,
			WorldID:   input.WorldID,
			ActorID:   input.ActorID,
			Text:      input.Text,
		},
	)
	return nil, OperationOutput{Operation: operation}, err
}

func (gateway *Gateway) sendActorDirective(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input SendActorDirectiveInput,
) (*mcp.CallToolResult, OperationOutput, error) {
	operation, err := gateway.client.SendActorDirective(
		ctx,
		controlplane.ActorTextInput{
			RequestID: input.RequestID,
			HostID:    input.HostID,
			WorldID:   input.WorldID,
			ActorID:   input.ActorID,
			Text:      input.Text,
		},
	)
	return nil, OperationOutput{Operation: operation}, err
}

func (gateway *Gateway) speakAsActor(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input SpeakAsActorInput,
) (*mcp.CallToolResult, OperationOutput, error) {
	operation, err := gateway.client.SubmitActorUtterance(
		ctx,
		controlplane.ActorUtteranceInput{
			RequestID: input.RequestID,
			HostID:    input.HostID,
			WorldID:   input.WorldID,
			ActorID:   input.ActorID,
			TurnID:    input.TurnID,
			Text:      input.Text,
		},
	)
	return nil, OperationOutput{Operation: operation}, err
}

func (gateway *Gateway) executeActorOffer(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input ExecuteActorOfferInput,
) (*mcp.CallToolResult, OperationOutput, error) {
	operation, err := gateway.client.ExecuteActorOffer(
		ctx,
		controlplane.ExecuteOfferInput{
			RequestID: input.RequestID,
			HostID:    input.HostID,
			WorldID:   input.WorldID,
			ActorID:   input.ActorID,
			OfferID:   input.OfferID,
			TurnID:    input.TurnID,
		},
	)
	return nil, OperationOutput{Operation: operation}, err
}

func (gateway *Gateway) getOperation(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input GetOperationInput,
) (*mcp.CallToolResult, OperationOutput, error) {
	operation, err := gateway.client.GetOperation(
		ctx, input.OperationID,
	)
	return nil, OperationOutput{Operation: operation}, err
}

func (gateway *Gateway) cancelOperation(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input CancelOperationInput,
) (*mcp.CallToolResult, OperationOutput, error) {
	operation, err := gateway.client.CancelOperation(
		ctx, input.OperationID,
	)
	return nil, OperationOutput{Operation: operation}, err
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
		DecisionAuthority:        view.Authority,
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

func (gateway *Gateway) granted(scope string) bool {
	return scopeGranted(gateway.principal, scope) ||
		scopeGranted(gateway.principal, controlplane.ScopeHostAdmin)
}

func hasControlScope(principal host.Principal) bool {
	for _, scope := range []string{
		controlplane.ScopeActorRead,
		controlplane.ScopeActorConverse,
		controlplane.ScopeActorDirect,
		controlplane.ScopeActorSpeak,
		controlplane.ScopeActorExecute,
		controlplane.ScopeOperationCancel,
		controlplane.ScopeHostAdmin,
	} {
		if scopeGranted(principal, scope) {
			return true
		}
	}
	return false
}

func readAnnotations() *mcp.ToolAnnotations {
	closedWorld := false
	return &mcp.ToolAnnotations{
		ReadOnlyHint:   true,
		IdempotentHint: true,
		OpenWorldHint:  &closedWorld,
	}
}

func writeAnnotations(destructive bool) *mcp.ToolAnnotations {
	closedWorld := false
	return &mcp.ToolAnnotations{
		DestructiveHint: &destructive,
		IdempotentHint:  true,
		OpenWorldHint:   &closedWorld,
		ReadOnlyHint:    false,
	}
}

func errorsInvalid(message string) error {
	return fmt.Errorf("invalid MCP gateway configuration: %s", message)
}
