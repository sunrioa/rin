package controlplane

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/policy"
)

func TestV2PublicationExposesDefensiveObservationAndCapabilities(t *testing.T) {
	binder, _ := actionGatewayTestComponents(t, host.RiskLow, policy.ProfileOpen)
	service, lease, _ := operationTestService(t, Options{})
	publication := v2WorldPublication(binder.spec)
	if err := service.PublishWorld("test.host", lease.LeaseID, publication); err != nil {
		t.Fatalf("PublishWorld: %v", err)
	}
	principal := operationPrincipal(ScopeActorRead)
	target := testActorControlTarget()

	observation, err := service.GetObservation(principal, target)
	if err != nil || observation.ObservationID != "observation.actor.one.1" ||
		len(observation.Facts) != 1 {
		t.Fatalf("GetObservation = %#v, %v", observation, err)
	}
	observation.Payload[0] = '['
	observation.Facts[0].Value[0] = '['
	repeated, err := service.GetObservation(principal, target)
	if err != nil || string(repeated.Payload) != `{"activity":"idle"}` ||
		string(repeated.Facts[0].Value) != `"idle"` {
		t.Fatalf("observation was not defensively copied = %#v, %v", repeated, err)
	}

	catalog, err := service.ListCapabilities(principal, target)
	if err != nil || catalog.Revision != 1 || len(catalog.Specs) != 1 ||
		catalog.Specs[0].Digest != binder.spec.Digest {
		t.Fatalf("ListCapabilities = %#v, %v", catalog, err)
	}
	catalog.Specs[0].Input.Document[0] = '['
	described, err := service.DescribeCapability(principal, DescribeCapabilityInput{
		ActorControlTarget: target,
		Capability:         binder.spec.Capability,
	})
	if err != nil || described.Digest != binder.spec.Digest ||
		described.Input.Document[0] != '{' {
		t.Fatalf("DescribeCapability = %#v, %v", described, err)
	}
}

func TestV2PublicationRejectsMismatchedOrDuplicateDiscoveryData(t *testing.T) {
	binder, _ := actionGatewayTestComponents(t, host.RiskLow, policy.ProfileOpen)
	service, lease, _ := operationTestService(t, Options{})
	publication := v2WorldPublication(binder.spec)
	publication.Actors[0].Observation.HostID = "host.other"
	if err := service.PublishWorld(
		"test.host", lease.LeaseID, publication,
	); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mismatched observation error = %v", err)
	}
	publication = v2WorldPublication(binder.spec)
	publication.Actors[0].Capabilities.Specs = append(
		publication.Actors[0].Capabilities.Specs,
		binder.spec,
	)
	if err := service.PublishWorld(
		"test.host", lease.LeaseID, publication,
	); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate capability error = %v", err)
	}
}

func TestV2DiscoveryFailsClosedWithoutPublishedDiscovery(t *testing.T) {
	service, _, _ := operationTestService(t, Options{})
	principal := operationPrincipal(ScopeActorRead)
	if _, err := service.GetObservation(
		principal,
		testActorControlTarget(),
	); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("GetObservation missing discovery error = %v", err)
	}
	if _, err := service.ListCapabilities(
		principal,
		testActorControlTarget(),
	); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ListCapabilities missing discovery error = %v", err)
	}
}

func v2WorldPublication(spec host.CapabilitySpec) WorldPublication {
	publication := worldPublication(2, "ready")
	actor := &publication.Actors[0]
	actor.Observation = &host.ObservationEnvelope{
		ObservationID: "observation.actor.one.1",
		HostID:        "test.host",
		WorldID:       "world.one",
		ActorID:       "actor.one",
		Epoch:         testEpoch(),
		Sequence:      1,
		ObservedAt:    host.Timepoint{Clock: host.ClockStep, Value: 10},
		Schema: host.SchemaRef{
			ID:      "schema.actor.observation",
			Version: "1.0.0",
			SHA256:  strings.Repeat("a", 64),
		},
		Payload: json.RawMessage(`{"activity":"idle"}`),
		Facts: []host.ObservationFact{{
			FactID: "fact.actor.activity",
			Kind:   "actor.activity",
			Value:  json.RawMessage(`"idle"`),
		}},
	}
	actor.Capabilities = &host.CapabilitySnapshot{
		Revision: 1,
		Specs:    []host.CapabilitySpec{spec},
	}
	return publication
}
