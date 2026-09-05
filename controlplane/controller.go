package controlplane

import (
	"fmt"

	"github.com/sunrioa/rin/host"
)

const (
	minControllerLeaseTTLMillis = 5_000
	maxControllerLeaseTTLMillis = 300_000
)

type actorControlKey struct {
	hostID  string
	worldID string
	actorID string
}

// AcquireController acquires or idempotently renews the sole deliberative
// controller lease for an Actor. The Host's current authority projection is
// checked before a lease can be issued.
func (service *Service) AcquireController(
	principal host.Principal,
	input AcquireControllerInput,
) (ControllerLease, error) {
	if err := validateAcquireControllerInput(input); err != nil {
		return ControllerLease{}, err
	}
	if err := host.ValidatePrincipal(principal); err != nil {
		return ControllerLease{}, fmt.Errorf("%w: principal: %v", ErrInvalid, err)
	}

	service.mu.Lock()
	defer service.mu.Unlock()
	actor, err := service.authorizeActorLocked(
		principal,
		input.HostID,
		input.WorldID,
		input.ActorID,
		ScopeActorControl,
	)
	if err != nil {
		return ControllerLease{}, err
	}
	authority := effectiveAuthority(actor)
	if !controllerPrincipalAllowed(principal, authority) {
		return ControllerLease{}, ErrForbidden
	}
	key := controlKey(input.ActorControlTarget)
	now := service.now().UnixMilli()
	service.expireControllerLocked(key, now)
	if current, exists := service.controllers[key]; exists {
		if current.ControllerID != input.ControllerID ||
			current.PrincipalID != principal.ID ||
			current.Source != authority.Source ||
			current.PersonaMode != authority.PersonaMode ||
			current.AuthorityRevision != authority.Revision ||
			current.Epoch != actor.Epoch {
			return ControllerLease{}, ErrLeaseConflict
		}
		current.ExpiresAtUnixMillis = now + int64(input.LeaseTTLMillis)
		service.controllers[key] = current
		service.markOperationsDirtyLocked()
		service.notifyActorChangedLocked(input.ActorControlTarget)
		return current, service.persistOperationsLocked()
	}
	leaseID, err := service.newID("controller-lease")
	if err != nil {
		return ControllerLease{}, err
	}
	lease := ControllerLease{
		LeaseID:              leaseID,
		ControllerID:         input.ControllerID,
		PrincipalID:          principal.ID,
		HostID:               input.HostID,
		WorldID:              input.WorldID,
		ActorID:              input.ActorID,
		Source:               authority.Source,
		PersonaMode:          authority.PersonaMode,
		AuthorityRevision:    authority.Revision,
		Epoch:                actor.Epoch,
		AcquiredAtUnixMillis: now,
		ExpiresAtUnixMillis:  now + int64(input.LeaseTTLMillis),
	}
	service.controllers[key] = lease
	service.markOperationsDirtyLocked()
	service.notifyActorChangedLocked(input.ActorControlTarget)
	return lease, service.persistOperationsLocked()
}

// RenewController extends an exact live controller lease.
func (service *Service) RenewController(
	principal host.Principal,
	target ActorControlTarget,
	leaseID string,
	ttlMillis uint32,
) (ControllerLease, error) {
	if err := validateActorControlTarget(target); err != nil {
		return ControllerLease{}, err
	}
	if err := validateID("lease_id", leaseID); err != nil {
		return ControllerLease{}, err
	}
	if err := validateControllerLeaseTTL(ttlMillis); err != nil {
		return ControllerLease{}, err
	}
	if err := host.ValidatePrincipal(principal); err != nil {
		return ControllerLease{}, fmt.Errorf("%w: principal: %v", ErrInvalid, err)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	key := controlKey(target)
	now := service.now().UnixMilli()
	service.expireControllerLocked(key, now)
	lease, exists := service.controllers[key]
	if !exists || lease.LeaseID != leaseID {
		return ControllerLease{}, ErrLeaseExpired
	}
	if lease.PrincipalID != principal.ID && !hasScope(principal, ScopeHostAdmin) {
		return ControllerLease{}, ErrForbidden
	}
	actor, err := service.authorizeActorLocked(
		principal, target.HostID, target.WorldID, target.ActorID, ScopeActorControl,
	)
	if err != nil {
		return ControllerLease{}, err
	}
	if !controllerMatchesActor(lease, actor) {
		service.invalidateControllerLocked(key, now)
		return ControllerLease{}, ErrLeaseExpired
	}
	lease.ExpiresAtUnixMillis = now + int64(ttlMillis)
	service.controllers[key] = lease
	service.markOperationsDirtyLocked()
	service.notifyActorChangedLocked(target)
	return lease, service.persistOperationsLocked()
}

func (service *Service) authorizeActorSnapshotLocked(
	principal host.Principal,
	target ActorControlTarget,
	requiredScope string,
) (ActorPublication, error) {
	_, world, err := service.findWorldLocked(target.HostID, target.WorldID)
	if err != nil {
		return ActorPublication{}, err
	}
	for _, actor := range world.Actors {
		if actor.ActorID != target.ActorID {
			continue
		}
		if !canAccessActor(principal, actor, requiredScope) {
			return ActorPublication{}, ErrForbidden
		}
		return actor, nil
	}
	return ActorPublication{}, ErrNotFound
}

// ReleaseController relinquishes an exact controller lease.
func (service *Service) ReleaseController(
	principal host.Principal,
	target ActorControlTarget,
	leaseID string,
) error {
	if err := validateActorControlTarget(target); err != nil {
		return err
	}
	if err := validateID("lease_id", leaseID); err != nil {
		return err
	}
	if err := host.ValidatePrincipal(principal); err != nil {
		return fmt.Errorf("%w: principal: %v", ErrInvalid, err)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	key := controlKey(target)
	lease, exists := service.controllers[key]
	if !exists || lease.LeaseID != leaseID {
		return ErrLeaseExpired
	}
	if lease.PrincipalID != principal.ID && !hasScope(principal, ScopeHostAdmin) {
		return ErrForbidden
	}
	service.invalidateControllerLocked(key, service.now().UnixMilli())
	service.notifyActorChangedLocked(target)
	return service.persistOperationsLocked()
}

// GetController returns the live controller lease visible to a principal.
func (service *Service) GetController(
	principal host.Principal,
	target ActorControlTarget,
) (ControllerLease, error) {
	if err := validateActorControlTarget(target); err != nil {
		return ControllerLease{}, err
	}
	if err := host.ValidatePrincipal(principal); err != nil {
		return ControllerLease{}, fmt.Errorf("%w: principal: %v", ErrInvalid, err)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if _, err := service.authorizeActorLocked(
		principal, target.HostID, target.WorldID, target.ActorID, ScopeActorRead,
	); err != nil {
		return ControllerLease{}, err
	}
	key := controlKey(target)
	service.expireControllerLocked(key, service.now().UnixMilli())
	lease, exists := service.controllers[key]
	if !exists {
		return ControllerLease{}, ErrNotFound
	}
	return lease, service.persistOperationsLocked()
}

// SetActorEmergencyStop changes the owner safety latch. External controllers
// that do not own the Actor cannot clear or assert this latch themselves.
func (service *Service) SetActorEmergencyStop(
	principal host.Principal,
	target ActorControlTarget,
	active bool,
) (ActorEmergencyStop, error) {
	if err := validateActorControlTarget(target); err != nil {
		return ActorEmergencyStop{}, err
	}
	if err := host.ValidatePrincipal(principal); err != nil {
		return ActorEmergencyStop{}, fmt.Errorf("%w: principal: %v", ErrInvalid, err)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	actor, err := service.authorizeActorSnapshotLocked(
		principal, target, ScopeActorControl,
	)
	if err != nil {
		return ActorEmergencyStop{}, err
	}
	if principal.ID != actor.OwnerPrincipalID && !hasScope(principal, ScopeHostAdmin) {
		return ActorEmergencyStop{}, ErrForbidden
	}
	key := controlKey(target)
	current := service.emergencyStops[key]
	if current.Active == active && current.Revision != 0 {
		return current, nil
	}
	current = ActorEmergencyStop{
		ActorControlTarget:   target,
		Active:               active,
		Revision:             current.Revision + 1,
		UpdatedByPrincipalID: principal.ID,
		UpdatedAtUnixMillis:  service.now().UnixMilli(),
	}
	service.emergencyStops[key] = current
	if active {
		service.cancelActorOperationsLocked(key, current.UpdatedAtUnixMillis)
	}
	service.markOperationsDirtyLocked()
	service.notifyActorChangedLocked(target)
	return current, service.persistOperationsLocked()
}

func controllerPrincipalAllowed(principal host.Principal, authority DecisionAuthority) bool {
	if authority.Source == DecisionInternal {
		return hasScope(principal, ScopeHostAdmin)
	}
	return authority.Source == DecisionExternal &&
		authority.ControllerPrincipalID == principal.ID
}

func controllerMatchesActor(lease ControllerLease, actor ActorPublication) bool {
	authority := effectiveAuthority(actor)
	return lease.ActorID == actor.ActorID && lease.Epoch == actor.Epoch &&
		lease.Source == authority.Source &&
		lease.PersonaMode == authority.PersonaMode &&
		lease.AuthorityRevision == authority.Revision &&
		(authority.Source != DecisionExternal ||
			lease.PrincipalID == authority.ControllerPrincipalID)
}

func (service *Service) expireControllerLocked(key actorControlKey, now int64) {
	lease, exists := service.controllers[key]
	if !exists || lease.ExpiresAtUnixMillis > now {
		return
	}
	service.invalidateControllerLocked(key, now)
}

func (service *Service) expireControllersLocked(now int64) {
	for key := range service.controllers {
		service.expireControllerLocked(key, now)
	}
}

func (service *Service) reconcileControllersLocked(
	hostID, worldID string,
	publication WorldPublication,
) {
	actors := make(map[string]ActorPublication, len(publication.Actors))
	for _, actor := range publication.Actors {
		actors[actor.ActorID] = actor
	}
	now := service.now().UnixMilli()
	for key, lease := range service.controllers {
		if key.hostID != hostID || key.worldID != worldID {
			continue
		}
		actor, exists := actors[key.actorID]
		if !exists || !controllerMatchesActor(lease, actor) {
			service.invalidateControllerLocked(key, now)
		}
	}
}

func (service *Service) invalidateControllerLocked(key actorControlKey, now int64) {
	lease, exists := service.controllers[key]
	if !exists {
		return
	}
	delete(service.controllers, key)
	service.fenceControllerOperationsLocked(lease.LeaseID, now)
	service.markOperationsDirtyLocked()
}

func (service *Service) fenceControllerOperationsLocked(leaseID string, now int64) {
	for _, operation := range service.operations {
		if operation.request.Binding == nil ||
			operation.request.Binding.ControllerLeaseID != leaseID ||
			completeOperation(operation) {
			continue
		}
		if operation.attempts == 0 && operation.ack == nil {
			operation.status = OperationStale
			service.finalizeOperationPolicyLocked(operation, false)
		} else {
			operation.cancel = true
		}
		operation.updatedAt = now
		service.recordOperationTimelineLocked(operation)
	}
}

func (service *Service) cancelActorOperationsLocked(key actorControlKey, now int64) {
	for _, operation := range service.operations {
		if operation.request.HostID != key.hostID ||
			operation.request.WorldID != key.worldID ||
			operation.request.ActorID != key.actorID ||
			completeOperation(operation) {
			continue
		}
		if operation.attempts == 0 && operation.ack == nil {
			operation.status = OperationCancelled
			service.finalizeOperationPolicyLocked(operation, false)
		} else {
			operation.cancel = true
		}
		operation.updatedAt = now
		service.recordOperationTimelineLocked(operation)
	}
}

func controlKey(target ActorControlTarget) actorControlKey {
	return actorControlKey{hostID: target.HostID, worldID: target.WorldID, actorID: target.ActorID}
}

func validateActorControlTarget(target ActorControlTarget) error {
	if err := validateID("host_id", target.HostID); err != nil {
		return err
	}
	if err := validateID("world_id", target.WorldID); err != nil {
		return err
	}
	return validateID("actor_id", target.ActorID)
}

func validateAcquireControllerInput(input AcquireControllerInput) error {
	if err := validateActorControlTarget(input.ActorControlTarget); err != nil {
		return err
	}
	if err := validateID("controller_id", input.ControllerID); err != nil {
		return err
	}
	return validateControllerLeaseTTL(input.LeaseTTLMillis)
}

func validateControllerLeaseTTL(value uint32) error {
	if value < minControllerLeaseTTLMillis || value > maxControllerLeaseTTLMillis {
		return invalid("lease_ttl_millis", "must be between 5000 and 300000")
	}
	return nil
}
