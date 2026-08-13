package signalbox

import (
	"context"
	"errors"

	"github.com/sunrioa/rin/controlplane"
)

type Service struct {
	store   *Store
	control *controlplane.Service
	client  *controlplane.ClientService
}

func NewService(
	store *Store,
	control *controlplane.Service,
	client *controlplane.ClientService,
) (*Service, error) {
	if store == nil || control == nil || client == nil {
		return nil, invalid("service", "requires store, Control Plane, and client")
	}
	return &Service{store: store, control: control, client: client}, nil
}

func (service *Service) ConfigureHost(
	ctx context.Context,
	input HostSettingsInput,
) (Settings, error) {
	if err := requireContext(ctx); err != nil {
		return Settings{}, err
	}
	target := Target{HostID: input.HostID, WorldID: input.WorldID, ActorID: input.ActorID}
	if _, err := service.control.ValidateHostActorSnapshot(
		input.HostID, input.LeaseID, input.WorldID, input.ActorID,
	); err != nil {
		return Settings{}, normalizeControlError(err)
	}
	return service.store.Configure(target, input.Settings)
}

func (service *Service) PublishHost(
	ctx context.Context,
	input HostPublishInput,
) (PublishResult, error) {
	if err := requireContext(ctx); err != nil {
		return PublishResult{}, err
	}
	if input.HostID != input.Signal.HostID {
		return PublishResult{}, ErrInvalid
	}
	if input.Signal.SchemaVersion != SchemaVersion {
		return PublishResult{}, ErrInvalid
	}
	snapshot, err := service.control.ValidateHostActorSnapshot(
		input.HostID, input.LeaseID, input.Signal.WorldID, input.Signal.ActorID,
	)
	if err != nil {
		return PublishResult{}, normalizeControlError(err)
	}
	if snapshot.Epoch != input.Signal.Epoch ||
		snapshot.ObservationSequence != input.Signal.ObservationSequence {
		return PublishResult{}, ErrInvalid
	}
	return service.store.Publish(input.Signal)
}

func (service *Service) List(ctx context.Context, input ListInput) (Page, error) {
	if err := service.authorizeRead(ctx, input.Target); err != nil {
		return Page{}, err
	}
	return service.store.List(input)
}

func (service *Service) Wait(ctx context.Context, input WaitInput) (Update, error) {
	if err := service.authorizeRead(ctx, input.Target); err != nil {
		return Update{}, err
	}
	return service.store.Wait(ctx, input)
}

func (service *Service) authorizeRead(ctx context.Context, target Target) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if err := ValidateTarget(target); err != nil {
		return err
	}
	_, err := service.client.GetActor(ctx, target.HostID, target.WorldID, target.ActorID)
	return normalizeControlError(err)
}

func requireContext(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalid
	}
	return ctx.Err()
}

func normalizeControlError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, controlplane.ErrForbidden):
		return ErrForbidden
	case errors.Is(err, controlplane.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, controlplane.ErrInvalid):
		return ErrInvalid
	default:
		return err
	}
}
