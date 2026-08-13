package controlplane

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/policy"
)

// Options contains deterministic seams used by tests and embedders.
type Options struct {
	Now           func() time.Time
	Random        io.Reader
	MaxOperations int
	OperationTTL  time.Duration
	ActionHost    ActionHost
	PolicyEngine  *policy.Engine
	OutcomeSink   OutcomeSink
}

// Service owns host leases and principal-filtered read models.
type Service struct {
	mu             sync.RWMutex
	now            func() time.Time
	random         io.Reader
	hosts          map[string]*hostState
	controllers    map[actorControlKey]ControllerLease
	emergencyStops map[actorControlKey]ActorEmergencyStop
	actionHost     ActionHost
	policyEngine   *policy.Engine
	outcomeSink    OutcomeSink
	actionFlights  map[string]*actionFlight
	confirming     map[string]struct{}
	hostGateway    map[string]*hostGatewayState

	maxOperations             int
	operationTTL              time.Duration
	operations                map[string]*operationState
	requests                  map[string]string
	changed                   chan struct{}
	operationFile             *operationFile
	operationDirty            bool
	operationCheckpointDirty  bool
	operationTimelineSequence uint64
	closed                    bool
}

type hostState struct {
	registration HostRegistration
	lease        HostLease
	worlds       map[string]WorldPublication
}

type hostGatewayState struct {
	request  HostGatewayRequest
	attempts uint32
	result   chan hostGatewayResponse
}

type hostGatewayResponse struct {
	result HostGatewayResult
	err    error
}

// New creates an in-memory Control Plane service.
func New(options Options) *Service {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	random := options.Random
	if random == nil {
		random = rand.Reader
	}
	maxOperations := options.MaxOperations
	if maxOperations <= 0 {
		maxOperations = defaultMaxOperations
	}
	if maxOperations > hardMaxOperations {
		maxOperations = hardMaxOperations
	}
	operationTTL := options.OperationTTL
	if operationTTL <= 0 {
		operationTTL = defaultOperationTTL
	}
	service := &Service{
		now:            now,
		random:         random,
		hosts:          make(map[string]*hostState),
		maxOperations:  maxOperations,
		operationTTL:   operationTTL,
		operations:     make(map[string]*operationState),
		requests:       make(map[string]string),
		controllers:    make(map[actorControlKey]ControllerLease),
		emergencyStops: make(map[actorControlKey]ActorEmergencyStop),
		actionHost:     options.ActionHost,
		policyEngine:   options.PolicyEngine,
		outcomeSink:    options.OutcomeSink,
		actionFlights:  make(map[string]*actionFlight),
		confirming:     make(map[string]struct{}),
		hostGateway:    make(map[string]*hostGatewayState),
		changed:        make(chan struct{}),
	}
	if service.actionHost == nil {
		service.actionHost = &pollingActionHost{service: service}
	}
	return service
}

// Close flushes persistent operation state and releases the data-directory
// writer lock. In-memory services may also be closed.
func (service *Service) Close() error {
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.closed {
		return nil
	}
	persistErr := service.flushOperationsLocked()
	service.closed = true
	service.failAllHostGatewayLocked(ErrClosed)
	var closeErr error
	if service.operationFile != nil {
		closeErr = service.operationFile.close()
	}
	service.notifyLocked()
	return errors.Join(persistErr, closeErr)
}

// RegisterHost acquires a host publication lease. Re-registering the same live
// instance is idempotent and renews its existing lease.
func (service *Service) RegisterHost(request HostRegistration) (HostLease, error) {
	if err := validateRegistration(request); err != nil {
		return HostLease{}, err
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if err := service.persistOperationsLocked(); err != nil {
		return HostLease{}, err
	}

	now := service.now().UnixMilli()
	if current, exists := service.hosts[request.HostID]; exists {
		live := current.lease.ExpiresAtUnixMillis > now
		if live && current.registration.InstanceID != request.InstanceID {
			return HostLease{}, fmt.Errorf("%w: host %s is already live",
				ErrLeaseConflict, request.HostID)
		}
		if live {
			if !reflect.DeepEqual(current.registration.Manifest, request.Manifest) {
				return HostLease{}, fmt.Errorf("%w: live host manifest changed",
					ErrLeaseConflict)
			}
			current.registration.LeaseTTLMillis = request.LeaseTTLMillis
			current.lease.ExpiresAtUnixMillis =
				now + int64(request.LeaseTTLMillis)
			return current.lease, nil
		}
		service.expireHostOperationsLocked(request.HostID, now)
		service.failHostGatewayLocked(request.HostID, ErrUnavailable)
		if err := service.persistOperationsLocked(); err != nil {
			return HostLease{}, err
		}
	}

	leaseID, err := service.newID("lease")
	if err != nil {
		return HostLease{}, err
	}
	lease := HostLease{
		HostID:              request.HostID,
		InstanceID:          request.InstanceID,
		LeaseID:             leaseID,
		ExpiresAtUnixMillis: now + int64(request.LeaseTTLMillis),
	}
	service.hosts[request.HostID] = &hostState{
		registration: cloneRegistration(request),
		lease:        lease,
		worlds:       make(map[string]WorldPublication),
	}
	return lease, nil
}

// RenewHost extends a current lease by its registered duration.
func (service *Service) RenewHost(hostID, leaseID string) (HostLease, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if err := service.persistOperationsLocked(); err != nil {
		return HostLease{}, err
	}
	current, err := service.requireLeaseLocked(hostID, leaseID)
	if err != nil {
		return HostLease{}, err
	}
	current.lease.ExpiresAtUnixMillis = service.now().UnixMilli() +
		int64(current.registration.LeaseTTLMillis)
	return current.lease, nil
}

// UnregisterHost releases a current lease while retaining an offline read model.
func (service *Service) UnregisterHost(hostID, leaseID string) error {
	service.mu.Lock()
	defer service.mu.Unlock()
	if err := service.persistOperationsLocked(); err != nil {
		return err
	}
	current, err := service.requireLeaseLocked(hostID, leaseID)
	if err != nil {
		return err
	}
	current.lease.ExpiresAtUnixMillis = service.now().UnixMilli()
	service.expireHostOperationsLocked(
		hostID,
		current.lease.ExpiresAtUnixMillis,
	)
	service.failHostGatewayLocked(hostID, ErrUnavailable)
	service.notifyLocked()
	return service.persistOperationsLocked()
}

// PublishWorld atomically replaces one world's actor and offer read model.
func (service *Service) PublishWorld(
	hostID, leaseID string,
	publication WorldPublication,
) error {
	service.mu.Lock()
	defer service.mu.Unlock()
	current, err := service.requireLeaseLocked(hostID, leaseID)
	if err != nil {
		return err
	}
	if err := validatePublication(
		publication,
		current.registration.Manifest,
		hostID,
	); err != nil {
		return err
	}
	if existing, exists := current.worlds[publication.WorldID]; exists {
		if publication.Sequence < existing.Sequence {
			return fmt.Errorf("%w: publication sequence moved backwards", ErrStale)
		}
		if publication.Sequence == existing.Sequence {
			if reflect.DeepEqual(existing, publication) {
				return nil
			}
			return fmt.Errorf("%w: publication changed without a new sequence", ErrStale)
		}
	} else if len(current.worlds) >= maxWorldsPerHost {
		return invalid("world_id", "host already contains 64 worlds")
	}
	service.fenceSupersededAuthorityLocked(
		hostID,
		publication.WorldID,
		publication,
	)
	service.reconcileControllersLocked(
		hostID,
		publication.WorldID,
		publication,
	)
	current.worlds[publication.WorldID] = clonePublication(publication)
	service.notifyLocked()
	return nil
}

// ListWorlds returns deterministic principal-visible world views, including
// retained offline worlds.
func (service *Service) ListWorlds(principal host.Principal) ([]WorldView, error) {
	if err := host.ValidatePrincipal(principal); err != nil {
		return nil, fmt.Errorf("%w: principal: %v", ErrInvalid, err)
	}
	service.mu.RLock()
	defer service.mu.RUnlock()
	now := service.now().UnixMilli()
	result := make([]WorldView, 0)
	for hostID, current := range service.hosts {
		online := current.lease.ExpiresAtUnixMillis > now
		for _, world := range current.worlds {
			if !hasScope(principal, ScopeHostAdmin) &&
				!publicationVisible(principal, world) {
				continue
			}
			result = append(result, WorldView{
				HostID:               hostID,
				WorldID:              world.WorldID,
				DisplayName:          world.DisplayName,
				Sequence:             world.Sequence,
				Online:               online,
				LeaseExpiresAtMillis: current.lease.ExpiresAtUnixMillis,
			})
		}
	}
	slices.SortFunc(result, func(left, right WorldView) int {
		if left.HostID != right.HostID {
			return compare(left.HostID, right.HostID)
		}
		return compare(left.WorldID, right.WorldID)
	})
	return result, nil
}

// ListActors returns deterministic principal-visible actors in one world.
func (service *Service) ListActors(
	principal host.Principal,
	hostID, worldID string,
) ([]ActorView, error) {
	if err := host.ValidatePrincipal(principal); err != nil {
		return nil, fmt.Errorf("%w: principal: %v", ErrInvalid, err)
	}
	service.mu.RLock()
	defer service.mu.RUnlock()
	current, world, err := service.findWorldLocked(hostID, worldID)
	if err != nil {
		return nil, err
	}
	online := current.lease.ExpiresAtUnixMillis > service.now().UnixMilli()
	result := make([]ActorView, 0, len(world.Actors))
	for _, actor := range world.Actors {
		if canAccessActor(principal, actor, ScopeActorRead) {
			result = append(result, service.actorViewLocked(
				hostID, worldID, current.lease, online, actor,
			))
		}
	}
	slices.SortFunc(result, func(left, right ActorView) int {
		return compare(left.ActorID, right.ActorID)
	})
	return result, nil
}

// GetActor returns one actor or a non-enumerating forbidden error.
func (service *Service) GetActor(
	principal host.Principal,
	hostID, worldID, actorID string,
) (ActorView, error) {
	if err := host.ValidatePrincipal(principal); err != nil {
		return ActorView{}, fmt.Errorf("%w: principal: %v", ErrInvalid, err)
	}
	service.mu.RLock()
	defer service.mu.RUnlock()
	return service.getActorLocked(principal, hostID, worldID, actorID)
}

// WaitActor waits for a newer actor observation or authority revision. It uses
// the same visibility rules as GetActor and never creates a second event log.
func (service *Service) WaitActor(
	ctx context.Context,
	principal host.Principal,
	input WaitActorInput,
) (ActorUpdate, error) {
	if err := host.ValidatePrincipal(principal); err != nil {
		return ActorUpdate{}, fmt.Errorf(
			"%w: principal: %v", ErrInvalid, err,
		)
	}
	if input.WaitMillis > 25_000 {
		return ActorUpdate{}, invalid(
			"wait_millis", "must not exceed 25000",
		)
	}
	timer := time.NewTimer(time.Duration(input.WaitMillis) * time.Millisecond)
	defer timer.Stop()
	for {
		service.mu.RLock()
		if service.closed {
			service.mu.RUnlock()
			return ActorUpdate{}, ErrUnavailable
		}
		view, err := service.getActorLocked(
			principal,
			input.HostID,
			input.WorldID,
			input.ActorID,
		)
		changed := service.changed
		service.mu.RUnlock()
		if err != nil {
			return ActorUpdate{}, err
		}
		cursorChanged := actorCursorChanged(view, input)
		if cursorChanged || input.WaitMillis == 0 {
			return ActorUpdate{Actor: view, Changed: cursorChanged}, nil
		}
		select {
		case <-ctx.Done():
			return ActorUpdate{}, ctx.Err()
		case <-timer.C:
			service.mu.RLock()
			view, err = service.getActorLocked(
				principal,
				input.HostID,
				input.WorldID,
				input.ActorID,
			)
			service.mu.RUnlock()
			if err != nil {
				return ActorUpdate{}, err
			}
			cursorChanged = actorCursorChanged(view, input)
			return ActorUpdate{Actor: view, Changed: cursorChanged}, nil
		case <-changed:
		}
	}
}

func actorCursorChanged(view ActorView, input WaitActorInput) bool {
	leaseID := ""
	if view.Controller != nil {
		leaseID = view.Controller.LeaseID
	}
	return view.ObservationSeq != input.AfterObservationSeq ||
		view.Authority.Revision != input.AfterAuthorityRevision ||
		leaseID != input.AfterControllerLeaseID ||
		view.EmergencyStopRevision != input.AfterEmergencyStopRevision
}

func (service *Service) getActorLocked(
	principal host.Principal,
	hostID, worldID, actorID string,
) (ActorView, error) {
	current, world, err := service.findWorldLocked(hostID, worldID)
	if err != nil {
		return ActorView{}, err
	}
	for _, actor := range world.Actors {
		if actor.ActorID != actorID {
			continue
		}
		if !canAccessActor(principal, actor, ScopeActorRead) {
			return ActorView{}, ErrForbidden
		}
		online := current.lease.ExpiresAtUnixMillis > service.now().UnixMilli()
		return service.actorViewLocked(hostID, worldID, current.lease, online, actor), nil
	}
	return ActorView{}, ErrNotFound
}

func (service *Service) requireLeaseLocked(
	hostID, leaseID string,
) (*hostState, error) {
	current, exists := service.hosts[hostID]
	if !exists || current.lease.LeaseID != leaseID {
		return nil, ErrLeaseExpired
	}
	if current.lease.ExpiresAtUnixMillis <= service.now().UnixMilli() {
		return nil, ErrLeaseExpired
	}
	return current, nil
}

func (service *Service) findWorldLocked(
	hostID, worldID string,
) (*hostState, WorldPublication, error) {
	current, exists := service.hosts[hostID]
	if !exists {
		return nil, WorldPublication{}, ErrNotFound
	}
	world, exists := current.worlds[worldID]
	if !exists {
		return nil, WorldPublication{}, ErrNotFound
	}
	return current, world, nil
}

func (service *Service) newID(prefix string) (string, error) {
	value := make([]byte, 16)
	if _, err := io.ReadFull(service.random, value); err != nil {
		return "", fmt.Errorf("create control plane identifier: %w", err)
	}
	return prefix + "." + hex.EncodeToString(value), nil
}

func cloneRegistration(value HostRegistration) HostRegistration {
	cloned := value
	cloned.Manifest.ClockModes =
		append([]host.ClockMode(nil), value.Manifest.ClockModes...)
	cloned.Manifest.DecisionModes =
		append([]host.DecisionMode(nil), value.Manifest.DecisionModes...)
	return cloned
}

func clonePublication(value WorldPublication) WorldPublication {
	cloned := value
	cloned.Actors = make([]ActorPublication, len(value.Actors))
	for index, actor := range value.Actors {
		cloned.Actors[index] = actor
		if actor.Authority != nil {
			authority := *actor.Authority
			cloned.Actors[index].Authority = &authority
		}
		cloned.Actors[index].State =
			append(json.RawMessage(nil), actor.State...)
		if actor.Observation != nil {
			observation := cloneObservationEnvelope(*actor.Observation)
			cloned.Actors[index].Observation = &observation
		}
		if actor.Capabilities != nil {
			capabilities := cloneCapabilitySnapshot(*actor.Capabilities)
			cloned.Actors[index].Capabilities = &capabilities
		}
	}
	return cloned
}

func cloneObservationEnvelope(value host.ObservationEnvelope) host.ObservationEnvelope {
	value.Payload = append(json.RawMessage(nil), value.Payload...)
	value.Facts = append([]host.ObservationFact(nil), value.Facts...)
	for index := range value.Facts {
		value.Facts[index].Tags = append([]string(nil), value.Facts[index].Tags...)
		value.Facts[index].Value = append(
			json.RawMessage(nil),
			value.Facts[index].Value...,
		)
		if value.Facts[index].Subject != nil {
			subject := *value.Facts[index].Subject
			value.Facts[index].Subject = &subject
		}
	}
	value.Resources = append([]host.ObservationResource(nil), value.Resources...)
	for index := range value.Resources {
		value.Resources[index].Tags = append(
			[]string(nil),
			value.Resources[index].Tags...,
		)
		value.Resources[index].Attributes = append(
			json.RawMessage(nil),
			value.Resources[index].Attributes...,
		)
	}
	value.Artifacts = append([]host.ObservationArtifact(nil), value.Artifacts...)
	return value
}

func cloneCapabilitySnapshot(value host.CapabilitySnapshot) host.CapabilitySnapshot {
	value.Specs = append([]host.CapabilitySpec(nil), value.Specs...)
	for index := range value.Specs {
		value.Specs[index].Input.Document = append(
			json.RawMessage(nil),
			value.Specs[index].Input.Document...,
		)
		value.Specs[index].Output.Document = append(
			json.RawMessage(nil),
			value.Specs[index].Output.Document...,
		)
		value.Specs[index].EffectSchema.Document = append(
			json.RawMessage(nil),
			value.Specs[index].EffectSchema.Document...,
		)
		value.Specs[index].RequiredScopes = append(
			[]string(nil),
			value.Specs[index].RequiredScopes...,
		)
	}
	return value
}

func (service *Service) actorViewLocked(
	hostID, worldID string,
	lease HostLease,
	online bool,
	actor ActorPublication,
) ActorView {
	key := actorControlKey{hostID: hostID, worldID: worldID, actorID: actor.ActorID}
	var controller *ControllerLease
	if current, exists := service.controllers[key]; exists &&
		current.ExpiresAtUnixMillis > service.now().UnixMilli() {
		cloned := current
		controller = &cloned
	}
	stop := service.emergencyStops[key]
	return ActorView{
		HostID:                hostID,
		WorldID:               worldID,
		ActorID:               actor.ActorID,
		OwnerPrincipalID:      actor.OwnerPrincipalID,
		DisplayName:           actor.DisplayName,
		ObservationSeq:        actor.ObservationSeq,
		Epoch:                 actor.Epoch,
		Authority:             effectiveAuthority(actor),
		Controller:            controller,
		EmergencyStopped:      stop.Active,
		EmergencyStopRevision: stop.Revision,
		State:                 append(json.RawMessage(nil), actor.State...),
		Online:                online,
		LeaseExpiresAtMillis:  lease.ExpiresAtUnixMillis,
	}
}

func effectiveAuthority(actor ActorPublication) DecisionAuthority {
	return *actor.Authority
}

func authorityAllowsExternal(
	actor ActorPublication,
	principalID string,
) bool {
	authority := effectiveAuthority(actor)
	return authority.Source == DecisionExternal &&
		authority.ControllerPrincipalID == principalID
}

func (service *Service) fenceSupersededAuthorityLocked(
	hostID, worldID string,
	publication WorldPublication,
) {
	revisions := make(map[string]uint64, len(publication.Actors))
	for _, actor := range publication.Actors {
		revisions[actor.ActorID] = effectiveAuthority(actor).Revision
	}
	now := service.now().UnixMilli()
	changed := false
	for _, operation := range service.operations {
		if operation.request.HostID != hostID ||
			operation.request.WorldID != worldID ||
			completeOperation(operation) ||
			(operation.ack != nil && operation.ack.Accepted) ||
			operation.request.Binding == nil {
			continue
		}
		revision, exists := revisions[operation.request.ActorID]
		if exists &&
			revision == operation.request.Binding.AuthorityRevision {
			continue
		}
		if operation.attempts == 0 {
			operation.status = OperationStale
			service.finalizeOperationPolicyLocked(operation, false)
		} else {
			operation.cancel = true
		}
		operation.updatedAt = now
		service.recordOperationTimelineLocked(operation)
		changed = true
	}
	if changed {
		service.markOperationsDirtyLocked()
	}
}

func publicationVisible(principal host.Principal, world WorldPublication) bool {
	for _, actor := range world.Actors {
		if canAccessActor(principal, actor, ScopeActorRead) {
			return true
		}
	}
	return false
}

func compare(left, right string) int {
	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}
