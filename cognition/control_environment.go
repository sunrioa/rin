package cognition

import (
	"context"
	"errors"
	"fmt"

	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
)

// ControlReadModel is the principal-aware Control Plane projection needed by
// the internal runtime. It exposes no Host binding or execution entrypoint.
type ControlReadModel interface {
	GetObservation(host.Principal, controlplane.ActorControlTarget) (host.ObservationEnvelope, error)
	ListCapabilities(host.Principal, controlplane.ActorControlTarget) (host.CapabilitySnapshot, error)
}

// ControlEnvironment adapts the latest complete Control Plane publication to
// AgentEnvironment. Paging and filtering must happen at the Host contract;
// this read-model adapter intentionally accepts only a full current snapshot.
type ControlEnvironment struct {
	readModel ControlReadModel
	principal host.Principal
}

func NewControlEnvironment(
	readModel ControlReadModel,
	principal host.Principal,
) (*ControlEnvironment, error) {
	if readModel == nil {
		return nil, errors.New("control read model is required")
	}
	if err := host.ValidatePrincipal(principal); err != nil {
		return nil, fmt.Errorf("principal: %w", err)
	}
	return &ControlEnvironment{readModel: readModel, principal: cloneProviderPrincipal(principal)}, nil
}

func (environment *ControlEnvironment) Observe(
	ctx context.Context,
	query host.ObservationQuery,
) (host.ObservationEnvelope, error) {
	if err := requireMemoryContext(ctx); err != nil {
		return host.ObservationEnvelope{}, err
	}
	if err := host.ValidateObservationQuery(query); err != nil {
		return host.ObservationEnvelope{}, err
	}
	if query.AfterSequence != 0 || len(query.Kinds) != 0 ||
		query.ContinuationToken != "" || query.Limit != 256 {
		return host.ObservationEnvelope{}, errors.New(
			"control environment supports only the latest complete observation",
		)
	}
	target := controlplane.ActorControlTarget{
		HostID: query.HostID, WorldID: query.WorldID, ActorID: query.ActorID,
	}
	observation, err := environment.readModel.GetObservation(environment.principal, target)
	if err != nil {
		return host.ObservationEnvelope{}, err
	}
	if err := host.ValidateObservationEnvelope(observation); err != nil {
		return host.ObservationEnvelope{}, err
	}
	if observation.HostID != query.HostID || observation.WorldID != query.WorldID ||
		observation.ActorID != query.ActorID || observation.Epoch != query.ExpectedEpoch {
		return host.ObservationEnvelope{}, controlplane.ErrStale
	}
	return observation, nil
}

func (environment *ControlEnvironment) Capabilities(
	ctx context.Context,
	target controlplane.ActorControlTarget,
) (host.CapabilitySnapshot, error) {
	if err := requireMemoryContext(ctx); err != nil {
		return host.CapabilitySnapshot{}, err
	}
	return environment.readModel.ListCapabilities(environment.principal, target)
}

func cloneProviderPrincipal(principal host.Principal) host.Principal {
	principal.GrantedScopes = append([]string(nil), principal.GrantedScopes...)
	return principal
}

var _ AgentEnvironment = (*ControlEnvironment)(nil)
var _ ControlReadModel = (*controlplane.Service)(nil)
