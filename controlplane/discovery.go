package controlplane

import (
	"fmt"

	"github.com/sunrioa/rin/host"
)

// GetObservation returns the latest complete V2 observation published by the
// authoritative Host. It is a read model, not an execution authorization.
func (service *Service) GetObservation(
	principal host.Principal,
	target ActorControlTarget,
) (host.ObservationEnvelope, error) {
	if err := validateActorControlTarget(target); err != nil {
		return host.ObservationEnvelope{}, err
	}
	if err := host.ValidatePrincipal(principal); err != nil {
		return host.ObservationEnvelope{}, fmt.Errorf(
			"%w: principal: %v",
			ErrInvalid,
			err,
		)
	}
	service.mu.RLock()
	defer service.mu.RUnlock()
	current, _, err := service.findWorldLocked(target.HostID, target.WorldID)
	if err != nil {
		return host.ObservationEnvelope{}, err
	}
	if current.lease.ExpiresAtUnixMillis <= service.now().UnixMilli() {
		return host.ObservationEnvelope{}, ErrUnavailable
	}
	actor, err := service.authorizeActorSnapshotLocked(
		principal,
		target,
		ScopeActorRead,
	)
	if err != nil {
		return host.ObservationEnvelope{}, err
	}
	if actor.Observation == nil {
		return host.ObservationEnvelope{}, fmt.Errorf(
			"%w: Host has not published a V2 observation",
			ErrUnavailable,
		)
	}
	return cloneObservationEnvelope(*actor.Observation), nil
}

// ListCapabilities returns the latest sealed V2 catalog published for one
// Actor. Discovery does not acquire control or authorize an action.
func (service *Service) ListCapabilities(
	principal host.Principal,
	target ActorControlTarget,
) (host.CapabilitySnapshot, error) {
	if err := validateActorControlTarget(target); err != nil {
		return host.CapabilitySnapshot{}, err
	}
	if err := host.ValidatePrincipal(principal); err != nil {
		return host.CapabilitySnapshot{}, fmt.Errorf(
			"%w: principal: %v",
			ErrInvalid,
			err,
		)
	}
	service.mu.RLock()
	defer service.mu.RUnlock()
	current, _, err := service.findWorldLocked(target.HostID, target.WorldID)
	if err != nil {
		return host.CapabilitySnapshot{}, err
	}
	if current.lease.ExpiresAtUnixMillis <= service.now().UnixMilli() {
		return host.CapabilitySnapshot{}, ErrUnavailable
	}
	actor, err := service.authorizeActorSnapshotLocked(
		principal,
		target,
		ScopeActorRead,
	)
	if err != nil {
		return host.CapabilitySnapshot{}, err
	}
	if actor.Capabilities == nil {
		return host.CapabilitySnapshot{}, fmt.Errorf(
			"%w: Host has not published a V2 capability catalog",
			ErrUnavailable,
		)
	}
	return cloneCapabilitySnapshot(*actor.Capabilities), nil
}

// DescribeCapability returns one exact spec from the current Actor catalog.
func (service *Service) DescribeCapability(
	principal host.Principal,
	input DescribeCapabilityInput,
) (host.CapabilitySpec, error) {
	if err := input.Capability.Validate("capability"); err != nil {
		return host.CapabilitySpec{}, invalid("capability", err.Error())
	}
	snapshot, err := service.ListCapabilities(
		principal,
		input.ActorControlTarget,
	)
	if err != nil {
		return host.CapabilitySpec{}, err
	}
	for _, spec := range snapshot.Specs {
		if spec.Capability == input.Capability {
			return cloneCapabilitySnapshot(host.CapabilitySnapshot{
				Specs: []host.CapabilitySpec{spec},
			}).Specs[0], nil
		}
	}
	return host.CapabilitySpec{}, ErrNotFound
}
