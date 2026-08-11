package cognition_test

import (
	"context"
	"errors"
	"testing"

	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
)

func TestControlEnvironmentReadsOnlyExactCurrentSnapshot(t *testing.T) {
	input := modelV2Input(t)
	readModel := &fakeControlReadModel{
		observation: input.Observation,
		catalog: host.CapabilitySnapshot{
			Revision: 1, Specs: []host.CapabilitySpec{agentCapabilitySpec(t)},
		},
	}
	principal := host.Principal{
		ID: "principal.internal", GrantedScopes: []string{controlplane.ScopeHostAdmin},
	}
	environment, err := cognition.NewControlEnvironment(readModel, principal)
	if err != nil {
		t.Fatal(err)
	}
	principal.GrantedScopes[0] = controlplane.ScopeActorRead
	query := host.ObservationQuery{
		QueryID: "query.agent.1", HostID: input.Observation.HostID,
		WorldID: input.Observation.WorldID, ActorID: input.Observation.ActorID,
		ExpectedEpoch: input.Observation.Epoch, Limit: 256,
	}
	observation, err := environment.Observe(context.Background(), query)
	if err != nil || observation.ObservationID != input.Observation.ObservationID {
		t.Fatalf("Observe = %+v, %v", observation, err)
	}
	if readModel.principal.GrantedScopes[0] != controlplane.ScopeHostAdmin {
		t.Fatalf("constructor retained mutable principal scopes: %+v", readModel.principal)
	}
	target := controlplane.ActorControlTarget{
		HostID: query.HostID, WorldID: query.WorldID, ActorID: query.ActorID,
	}
	catalog, err := environment.Capabilities(context.Background(), target)
	if err != nil || len(catalog.Specs) != 1 {
		t.Fatalf("Capabilities = %+v, %v", catalog, err)
	}

	stale := query
	stale.ExpectedEpoch.Timeline++
	if _, err := environment.Observe(context.Background(), stale); !errors.Is(err, controlplane.ErrStale) {
		t.Fatalf("stale epoch error = %v", err)
	}
	filtered := query
	filtered.Kinds = []string{"player.status"}
	if _, err := environment.Observe(context.Background(), filtered); err == nil {
		t.Fatal("Control read model silently ignored an unsupported filter")
	}
	paged := query
	paged.AfterSequence = query.ExpectedEpoch.Timeline
	if _, err := environment.Observe(context.Background(), paged); err == nil {
		t.Fatal("Control read model silently ignored an unsupported sequence cursor")
	}
}

type fakeControlReadModel struct {
	observation host.ObservationEnvelope
	catalog     host.CapabilitySnapshot
	principal   host.Principal
}

func (model *fakeControlReadModel) GetObservation(
	principal host.Principal,
	_ controlplane.ActorControlTarget,
) (host.ObservationEnvelope, error) {
	model.principal = principal
	return model.observation, nil
}

func (model *fakeControlReadModel) ListCapabilities(
	principal host.Principal,
	_ controlplane.ActorControlTarget,
) (host.CapabilitySnapshot, error) {
	model.principal = principal
	return model.catalog, nil
}
