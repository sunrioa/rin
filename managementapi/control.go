package managementapi

import (
	"context"
	"errors"
	"strings"

	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
)

type RuntimeSnapshot struct {
	Worlds []controlplane.WorldView `json:"worlds"`
	Actors []controlplane.ActorView `json:"actors"`
}

type OperationListOutput struct {
	Operations []controlplane.OperationView `json:"operations"`
}

type OperationControlInput struct {
	OperationID string `json:"operation_id"`
	Action      string `json:"action"`
}

type ActorControlInput struct {
	controlplane.ActorControlTarget
	Action         string `json:"action"`
	ControllerID   string `json:"controller_id,omitempty"`
	LeaseID        string `json:"lease_id,omitempty"`
	LeaseTTLMillis uint32 `json:"lease_ttl_millis,omitempty"`
}

type ActorControlOutput struct {
	Action        string                           `json:"action"`
	Lease         *controlplane.ControllerLease    `json:"lease,omitempty"`
	EmergencyStop *controlplane.ActorEmergencyStop `json:"emergency_stop,omitempty"`
}

func (service *Service) ConfigureControl(
	control *controlplane.Service,
	principal host.Principal,
) error {
	if control == nil {
		return errors.New("control service is required")
	}
	if err := host.ValidatePrincipal(principal); err != nil {
		return err
	}
	service.control = control
	service.controlPrincipal = principal
	return nil
}

func (service *Service) RuntimeSnapshot(ctx context.Context) (RuntimeSnapshot, error) {
	if service.control == nil {
		return RuntimeSnapshot{}, ErrControlUnavailable
	}
	if err := ctx.Err(); err != nil {
		return RuntimeSnapshot{}, err
	}
	worlds, err := service.control.ListWorlds(service.controlPrincipal)
	if err != nil {
		return RuntimeSnapshot{}, err
	}
	actors := make([]controlplane.ActorView, 0)
	for _, world := range worlds {
		listed, listErr := service.control.ListActors(
			service.controlPrincipal, world.HostID, world.WorldID,
		)
		if listErr != nil {
			return RuntimeSnapshot{}, listErr
		}
		actors = append(actors, listed...)
	}
	return RuntimeSnapshot{Worlds: worlds, Actors: actors}, nil
}

func (service *Service) ListOperations(
	ctx context.Context,
	input controlplane.ListOperationsInput,
) (OperationListOutput, error) {
	if service.control == nil {
		return OperationListOutput{}, ErrControlUnavailable
	}
	if err := ctx.Err(); err != nil {
		return OperationListOutput{}, err
	}
	operations, err := service.control.ListOperations(service.controlPrincipal, input)
	if err != nil {
		return OperationListOutput{}, err
	}
	return OperationListOutput{Operations: operations}, nil
}

func (service *Service) ControlOperation(
	ctx context.Context,
	input OperationControlInput,
) (controlplane.OperationView, error) {
	if service.control == nil {
		return controlplane.OperationView{}, ErrControlUnavailable
	}
	if err := ctx.Err(); err != nil {
		return controlplane.OperationView{}, err
	}
	switch strings.TrimSpace(input.Action) {
	case "cancel":
		return service.control.CancelOperation(
			service.controlPrincipal, strings.TrimSpace(input.OperationID),
		)
	case "confirm":
		return service.control.ConfirmAction(
			ctx, service.controlPrincipal, strings.TrimSpace(input.OperationID),
		)
	default:
		return controlplane.OperationView{}, errors.New(
			"operation action must be cancel or confirm",
		)
	}
}

func (service *Service) ControlActor(
	ctx context.Context,
	input ActorControlInput,
) (ActorControlOutput, error) {
	if service.control == nil {
		return ActorControlOutput{}, ErrControlUnavailable
	}
	if err := ctx.Err(); err != nil {
		return ActorControlOutput{}, err
	}
	input.Action = strings.TrimSpace(input.Action)
	switch input.Action {
	case "acquire":
		if input.ControllerID == "" {
			input.ControllerID = "controller.rin-console"
		}
		if input.LeaseTTLMillis == 0 {
			input.LeaseTTLMillis = 300_000
		}
		lease, err := service.control.AcquireController(
			service.controlPrincipal,
			controlplane.AcquireControllerInput{
				ActorControlTarget: input.ActorControlTarget,
				ControllerID:       input.ControllerID,
				LeaseTTLMillis:     input.LeaseTTLMillis,
			},
		)
		if err != nil {
			return ActorControlOutput{}, err
		}
		return ActorControlOutput{Action: input.Action, Lease: &lease}, err
	case "renew":
		if input.LeaseTTLMillis == 0 {
			input.LeaseTTLMillis = 300_000
		}
		lease, err := service.control.RenewController(
			service.controlPrincipal,
			input.ActorControlTarget,
			strings.TrimSpace(input.LeaseID),
			input.LeaseTTLMillis,
		)
		if err != nil {
			return ActorControlOutput{}, err
		}
		return ActorControlOutput{Action: input.Action, Lease: &lease}, nil
	case "release":
		err := service.control.ReleaseController(
			service.controlPrincipal, input.ActorControlTarget,
			strings.TrimSpace(input.LeaseID),
		)
		return ActorControlOutput{Action: input.Action}, err
	case "emergency-stop", "clear-emergency-stop":
		stop, err := service.control.SetActorEmergencyStop(
			service.controlPrincipal, input.ActorControlTarget,
			input.Action == "emergency-stop",
		)
		if err != nil {
			return ActorControlOutput{}, err
		}
		return ActorControlOutput{Action: input.Action, EmergencyStop: &stop}, err
	default:
		return ActorControlOutput{}, errors.New(
			"actor action must be acquire, renew, release, emergency-stop, or clear-emergency-stop",
		)
	}
}
