package story_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/examples/adapters/story"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/policy"
	"github.com/sunrioa/rin/sdk/hostkit"
	"github.com/sunrioa/rin/sdk/hostkit/conformance"
)

func TestStoryAdapterConformance(t *testing.T) {
	adapter, err := story.New()
	if err != nil {
		t.Fatal(err)
	}
	report, err := conformance.Run(context.Background(), conformance.Scenario{
		Adapter:   adapter,
		Target:    story.Target(),
		Principal: storyPrincipal(),
		BuildAction: func(
			catalog host.CapabilitySnapshot,
			snapshot controlplane.ActionHostSnapshot,
			requestID, idempotencyKey string,
		) (host.ActionRequest, error) {
			return storyRequest(
				catalog, snapshot, story.CapabilitySpeak,
				json.RawMessage(`{"text":"I found the missing photograph."}`),
				requestID, idempotencyKey,
			)
		},
		BuildCancellable: func(
			catalog host.CapabilitySnapshot,
			snapshot controlplane.ActionHostSnapshot,
			requestID, idempotencyKey string,
		) (host.ActionRequest, error) {
			return storyRequest(
				catalog, snapshot, story.CapabilityWait,
				json.RawMessage(`{}`), requestID, idempotencyKey,
			)
		},
		AdvanceObservation: adapter.AdvanceObservation,
		RestartHost:        adapter.RestartHost,
		StateDigest:        adapter.StateDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.CapabilityCount != 4 || report.EffectCount != 2 ||
		!report.IdempotentReplay || !report.StaleRejected ||
		!report.RestartRejected || !report.CancellationWorks {
		t.Fatalf("conformance report = %#v", report)
	}
}

func TestStoryCharacterBoundaryUsesPolicyDecision(t *testing.T) {
	adapter, err := story.New()
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := hostkit.NewAdapterCoordinator(
		context.Background(), adapter, directDispatcher{},
	)
	if err != nil {
		t.Fatal(err)
	}
	target := storyControlTarget()
	snapshot, err := coordinator.SnapshotAction(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	request, err := storyRequest(
		coordinator.Capabilities(), snapshot, story.CapabilityChangeTopic,
		json.RawMessage(`{"topic":"sealed-letter"}`),
		"request.story.boundary", "action.story.boundary",
	)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := coordinator.BindAction(context.Background(), target, request)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := story.NewPolicy()
	if err != nil {
		t.Fatal(err)
	}
	decision, err := engine.Evaluate(binding.Action, policy.Context{
		Now: binding.Snapshot.Now, CurrentEpoch: binding.Snapshot.Epoch,
		Principal: storyPrincipal(), ServerID: story.HostID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Result != policy.Deny || decision.ReasonCode != "story.character_boundary" {
		t.Fatalf("boundary decision = %#v", decision)
	}
	if adapter.State().Topic != story.TopicPhotographs {
		t.Fatal("denied character boundary changed the story")
	}
}

type directDispatcher struct{}

func (directDispatcher) Dispatch(
	ctx context.Context,
	work func(context.Context) error,
) error {
	return work(ctx)
}

func storyPrincipal() host.Principal {
	return host.Principal{
		ID: "player.story.one",
		GrantedScopes: []string{
			controlplane.ScopeActorControl,
			controlplane.ScopeActorExecute,
			controlplane.ScopeActorRead,
			controlplane.ScopeOperationCancel,
		},
	}
}

func storyRequest(
	catalog host.CapabilitySnapshot,
	snapshot controlplane.ActionHostSnapshot,
	capability string,
	arguments json.RawMessage,
	requestID, idempotencyKey string,
) (host.ActionRequest, error) {
	index := slices.IndexFunc(catalog.Specs, func(spec host.CapabilitySpec) bool {
		return spec.Capability.ID == capability
	})
	if index < 0 {
		return host.ActionRequest{}, &missingCapabilityError{capability: capability}
	}
	spec := catalog.Specs[index]
	return host.ActionRequest{
		RequestID:      requestID,
		ControllerID:   "controller.story.external",
		ActorID:        story.ActorID,
		Capability:     spec.Capability,
		SpecDigest:     spec.Digest,
		Arguments:      append(json.RawMessage(nil), arguments...),
		ExpectedEpoch:  snapshot.Epoch,
		ObservationSeq: snapshot.ObservationSeq,
		IdempotencyKey: idempotencyKey,
	}, nil
}

type missingCapabilityError struct {
	capability string
}

func (err *missingCapabilityError) Error() string {
	return "missing story capability: " + err.capability
}

func storyControlTarget() controlplane.ActorControlTarget {
	return controlplane.ActorControlTarget{
		HostID: story.HostID, WorldID: story.WorldID, ActorID: story.ActorID,
	}
}
