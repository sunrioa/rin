package mcpbridge

import (
	"context"

	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
)

// ControlClient is the narrow Control Plane surface used by MCP tools.
type ControlClient interface {
	ListWorlds(context.Context) ([]controlplane.WorldView, error)
	ListActors(context.Context, string, string) ([]controlplane.ActorView, error)
	GetActor(context.Context, string, string, string) (controlplane.ActorView, error)
	WaitActor(
		context.Context,
		controlplane.WaitActorInput,
	) (controlplane.ActorUpdate, error)
	ListActorOffers(
		context.Context,
		string,
		string,
		string,
	) ([]host.ActionOffer, error)
	SendActorMessage(
		context.Context,
		controlplane.ActorTextInput,
	) (controlplane.OperationView, error)
	SendActorDirective(
		context.Context,
		controlplane.ActorTextInput,
	) (controlplane.OperationView, error)
	SubmitActorUtterance(
		context.Context,
		controlplane.ActorUtteranceInput,
	) (controlplane.OperationView, error)
	ExecuteActorOffer(
		context.Context,
		controlplane.ExecuteOfferInput,
	) (controlplane.OperationView, error)
	GetOperation(context.Context, string) (controlplane.OperationView, error)
	WaitOperation(
		context.Context,
		controlplane.WaitOperationInput,
	) (controlplane.OperationUpdate, error)
	CancelOperation(context.Context, string) (controlplane.OperationView, error)
}

type serviceClient struct {
	service   *controlplane.Service
	principal host.Principal
}

func (client *serviceClient) ListWorlds(
	_ context.Context,
) ([]controlplane.WorldView, error) {
	return client.service.ListWorlds(client.principal)
}

func (client *serviceClient) ListActors(
	_ context.Context,
	hostID, worldID string,
) ([]controlplane.ActorView, error) {
	return client.service.ListActors(client.principal, hostID, worldID)
}

func (client *serviceClient) GetActor(
	_ context.Context,
	hostID, worldID, actorID string,
) (controlplane.ActorView, error) {
	return client.service.GetActor(
		client.principal,
		hostID,
		worldID,
		actorID,
	)
}

func (client *serviceClient) WaitActor(
	ctx context.Context,
	input controlplane.WaitActorInput,
) (controlplane.ActorUpdate, error) {
	return client.service.WaitActor(ctx, client.principal, input)
}

func (client *serviceClient) ListActorOffers(
	_ context.Context,
	hostID, worldID, actorID string,
) ([]host.ActionOffer, error) {
	return client.service.ListActorOffers(
		client.principal,
		hostID,
		worldID,
		actorID,
	)
}

func (client *serviceClient) SendActorMessage(
	_ context.Context,
	input controlplane.ActorTextInput,
) (controlplane.OperationView, error) {
	return client.service.SendActorMessage(client.principal, input)
}

func (client *serviceClient) SendActorDirective(
	_ context.Context,
	input controlplane.ActorTextInput,
) (controlplane.OperationView, error) {
	return client.service.SendActorDirective(client.principal, input)
}

func (client *serviceClient) SubmitActorUtterance(
	_ context.Context,
	input controlplane.ActorUtteranceInput,
) (controlplane.OperationView, error) {
	return client.service.SubmitActorUtterance(client.principal, input)
}

func (client *serviceClient) ExecuteActorOffer(
	_ context.Context,
	input controlplane.ExecuteOfferInput,
) (controlplane.OperationView, error) {
	return client.service.ExecuteActorOffer(client.principal, input)
}

func (client *serviceClient) GetOperation(
	_ context.Context,
	operationID string,
) (controlplane.OperationView, error) {
	return client.service.GetOperation(client.principal, operationID)
}

func (client *serviceClient) WaitOperation(
	ctx context.Context,
	input controlplane.WaitOperationInput,
) (controlplane.OperationUpdate, error) {
	return client.service.WaitOperation(ctx, client.principal, input)
}

func (client *serviceClient) CancelOperation(
	_ context.Context,
	operationID string,
) (controlplane.OperationView, error) {
	return client.service.CancelOperation(client.principal, operationID)
}
