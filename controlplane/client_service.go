package controlplane

import (
	"context"
	"fmt"

	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/timeline"
)

// ClientInfo describes the fixed identity exposed by one Control Daemon.
type ClientInfo struct {
	ContractVersion string         `json:"contract_version"`
	Principal       host.Principal `json:"principal"`
}

// ControlClient is the engine-neutral V2 application surface shared by HTTP,
// MCP, and direct embedders. Implementations must not infer permissions from
// model-authored fields.
type ControlClient interface {
	Info(context.Context) (ClientInfo, error)
	ListWorlds(context.Context) ([]WorldView, error)
	ListActors(context.Context, string, string) ([]ActorView, error)
	GetActor(context.Context, string, string, string) (ActorView, error)
	WaitActor(context.Context, WaitActorInput) (ActorUpdate, error)
	GetObservation(context.Context, ActorControlTarget) (host.ObservationEnvelope, error)
	ListCapabilities(context.Context, ActorControlTarget) (host.CapabilitySnapshot, error)
	DescribeCapability(context.Context, DescribeCapabilityInput) (host.CapabilitySpec, error)
	AcquireController(context.Context, AcquireControllerInput) (ControllerLease, error)
	RenewController(context.Context, RenewControllerInput) (ControllerLease, error)
	ReleaseController(context.Context, ReleaseControllerInput) error
	GetController(context.Context, ActorControlTarget) (ControllerLease, error)
	SubmitAction(context.Context, SubmitActionInput) (OperationView, error)
	ConfirmAction(context.Context, string) (OperationView, error)
	GetOperation(context.Context, string) (OperationView, error)
	WaitOperation(context.Context, WaitOperationInput) (OperationUpdate, error)
	GetTaskTimeline(context.Context, timeline.Query) (timeline.Page, error)
	WaitTaskTimeline(context.Context, timeline.WaitInput) (timeline.Update, error)
	CancelOperation(context.Context, string) (OperationView, error)
	SetEmergencyStop(context.Context, SetEmergencyStopInput) (ActorEmergencyStop, error)
}

// ClientService binds one validated Principal to a Service. Transports receive
// no caller-supplied Principal field and therefore cannot accidentally widen
// authority while decoding a request.
type ClientService struct {
	service   *Service
	principal host.Principal
}

func NewClientService(
	service *Service,
	principal host.Principal,
) (*ClientService, error) {
	if service == nil {
		return nil, fmt.Errorf("%w: service is required", ErrInvalid)
	}
	if err := host.ValidatePrincipal(principal); err != nil {
		return nil, fmt.Errorf("%w: principal: %v", ErrInvalid, err)
	}
	if !principalHasControlScope(principal) {
		return nil, fmt.Errorf(
			"%w: principal has no Control Plane scope",
			ErrInvalid,
		)
	}
	return &ClientService{
		service:   service,
		principal: clonePrincipalValue(principal),
	}, nil
}

func (client *ClientService) Info(context.Context) (ClientInfo, error) {
	return ClientInfo{
		ContractVersion: ContractVersion,
		Principal:       clonePrincipalValue(client.principal),
	}, nil
}

func (client *ClientService) ListWorlds(
	context.Context,
) ([]WorldView, error) {
	return client.service.ListWorlds(client.principal)
}

func (client *ClientService) ListActors(
	_ context.Context,
	hostID, worldID string,
) ([]ActorView, error) {
	return client.service.ListActors(client.principal, hostID, worldID)
}

func (client *ClientService) GetActor(
	_ context.Context,
	hostID, worldID, actorID string,
) (ActorView, error) {
	return client.service.GetActor(client.principal, hostID, worldID, actorID)
}

func (client *ClientService) WaitActor(
	ctx context.Context,
	input WaitActorInput,
) (ActorUpdate, error) {
	return client.service.WaitActor(ctx, client.principal, input)
}

func (client *ClientService) GetObservation(
	_ context.Context,
	target ActorControlTarget,
) (host.ObservationEnvelope, error) {
	return client.service.GetObservation(client.principal, target)
}

func (client *ClientService) ListCapabilities(
	_ context.Context,
	target ActorControlTarget,
) (host.CapabilitySnapshot, error) {
	return client.service.ListCapabilities(client.principal, target)
}

func (client *ClientService) DescribeCapability(
	_ context.Context,
	input DescribeCapabilityInput,
) (host.CapabilitySpec, error) {
	return client.service.DescribeCapability(client.principal, input)
}

func (client *ClientService) AcquireController(
	_ context.Context,
	input AcquireControllerInput,
) (ControllerLease, error) {
	return client.service.AcquireController(client.principal, input)
}

func (client *ClientService) RenewController(
	_ context.Context,
	input RenewControllerInput,
) (ControllerLease, error) {
	return client.service.RenewController(
		client.principal,
		input.ActorControlTarget,
		input.LeaseID,
		input.LeaseTTLMillis,
	)
}

func (client *ClientService) ReleaseController(
	_ context.Context,
	input ReleaseControllerInput,
) error {
	return client.service.ReleaseController(
		client.principal,
		input.ActorControlTarget,
		input.LeaseID,
	)
}

func (client *ClientService) GetController(
	_ context.Context,
	target ActorControlTarget,
) (ControllerLease, error) {
	return client.service.GetController(client.principal, target)
}

func (client *ClientService) SubmitAction(
	ctx context.Context,
	input SubmitActionInput,
) (OperationView, error) {
	return client.service.SubmitAction(ctx, client.principal, input)
}

func (client *ClientService) ConfirmAction(
	ctx context.Context,
	operationID string,
) (OperationView, error) {
	return client.service.ConfirmAction(ctx, client.principal, operationID)
}

func (client *ClientService) GetOperation(
	_ context.Context,
	operationID string,
) (OperationView, error) {
	return client.service.GetOperation(client.principal, operationID)
}

func (client *ClientService) WaitOperation(
	ctx context.Context,
	input WaitOperationInput,
) (OperationUpdate, error) {
	return client.service.WaitOperation(ctx, client.principal, input)
}

func (client *ClientService) GetTaskTimeline(
	_ context.Context,
	query timeline.Query,
) (timeline.Page, error) {
	return client.service.GetTaskTimeline(client.principal, query)
}

func (client *ClientService) WaitTaskTimeline(
	ctx context.Context,
	input timeline.WaitInput,
) (timeline.Update, error) {
	return client.service.WaitTaskTimeline(ctx, client.principal, input)
}

func (client *ClientService) CancelOperation(
	_ context.Context,
	operationID string,
) (OperationView, error) {
	return client.service.CancelOperation(client.principal, operationID)
}

func (client *ClientService) SetEmergencyStop(
	_ context.Context,
	input SetEmergencyStopInput,
) (ActorEmergencyStop, error) {
	return client.service.SetActorEmergencyStop(
		client.principal,
		input.ActorControlTarget,
		input.Active,
	)
}

var _ ControlClient = (*ClientService)(nil)
