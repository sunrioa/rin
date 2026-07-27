package host

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

func TestRegistrySealsOffersAndRejectsTOCTOU(t *testing.T) {
	manifest := testManifest()
	registry, err := NewRegistry(manifest)
	if err != nil {
		t.Fatal(err)
	}
	descriptor := testDescriptor(t)
	sealed, err := registry.Register(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	if sealed.Digest == "" || sealed.RequiredScopes[0] != "rin.npc.move" {
		t.Fatalf("descriptor was not normalized: %+v", sealed)
	}
	const expectedDigest = "544a1b121a839171d1ce3d8d07dee434078a4e757c87641bee9126a97b18fca2"
	if sealed.Digest != expectedDigest {
		t.Fatalf("descriptor digest = %s, want %s", sealed.Digest, expectedDigest)
	}
	if snapshot := registry.Snapshot(); snapshot.Revision != 1 ||
		len(snapshot.Descriptors) != 1 {
		t.Fatalf("unexpected registry snapshot: %+v", snapshot)
	}
	if _, err := registry.Register(sealed); err != nil {
		t.Fatalf("idempotent registration failed: %v", err)
	}
	if registry.Snapshot().Revision != 1 {
		t.Fatal("idempotent registration advanced revision")
	}

	epoch := testEpoch()
	offer := ActionOffer{
		OfferID:          "offer.move.1",
		DecisionWindowID: "window.1",
		ActorID:          "npc.guide",
		Capability:       sealed.Capability,
		DescriptorDigest: sealed.Digest,
		Description:      "Move to the dock.",
		Arguments:        json.RawMessage(`{"target":"dock"}`),
		ExpectedEpoch:    epoch,
		ObservationSeq:   7,
		Deadline:         Timepoint{Clock: ClockRealtime, Value: 20_000},
	}
	if err := registry.ValidateOffer(offer, Timepoint{Clock: ClockRealtime, Value: 10_000}, epoch); err != nil {
		t.Fatalf("valid offer rejected: %v", err)
	}
	invocation, err := registry.NewInvocation(
		offer,
		"operation.move.1",
		Timepoint{Clock: ClockRealtime, Value: 10_000},
		Timepoint{Clock: ClockRealtime, Value: 15_000},
		epoch,
	)
	if err != nil {
		t.Fatal(err)
	}
	principal := Principal{
		ID:            "principal.guide",
		GrantedScopes: []string{"rin.npc.move"},
	}
	if err := registry.AuthorizeInvocation(
		invocation,
		Timepoint{Clock: ClockRealtime, Value: 12_000},
		epoch,
		principal,
	); err != nil {
		t.Fatalf("valid invocation rejected: %v", err)
	}
	if err := registry.AuthorizeInvocation(
		invocation,
		Timepoint{Clock: ClockRealtime, Value: 12_000},
		epoch,
		Principal{ID: "principal.viewer"},
	); err == nil {
		t.Fatal("invocation without its required scope was authorized")
	}
	if err := registry.ValidateOutput(
		sealed.Capability,
		sealed.Digest,
		[]byte(`{"state":"reached"}`),
	); err != nil {
		t.Fatalf("valid output rejected: %v", err)
	}

	stale := epoch
	stale.World++
	if err := registry.ValidateOffer(offer, Timepoint{Clock: ClockRealtime, Value: 10_000}, stale); err == nil {
		t.Fatal("stale epoch offer accepted")
	}
	if err := registry.ValidateOffer(offer, Timepoint{Clock: ClockRealtime, Value: 20_000}, epoch); err == nil {
		t.Fatal("expired offer accepted")
	}
	invalidArguments := offer
	invalidArguments.Arguments = json.RawMessage(`{"target":7}`)
	if err := registry.ValidateOffer(invalidArguments, Timepoint{Clock: ClockRealtime, Value: 10_000}, epoch); err == nil {
		t.Fatal("schema-invalid arguments accepted")
	}
	changedDigest := offer
	changedDigest.DescriptorDigest = strings.Repeat("0", 64)
	if err := registry.ValidateOffer(changedDigest, Timepoint{Clock: ClockRealtime, Value: 10_000}, epoch); err == nil {
		t.Fatal("descriptor digest mismatch accepted")
	}

	if !registry.Unregister(sealed.Capability) {
		t.Fatal("registered capability was not removed")
	}
	if err := registry.AuthorizeInvocation(
		invocation,
		Timepoint{Clock: ClockRealtime, Value: 12_000},
		epoch,
		principal,
	); err == nil {
		t.Fatal("invocation remained authorized after capability revocation")
	}
}

func TestRegistryDoesNotExposeMutableDescriptorState(t *testing.T) {
	registry, err := NewRegistry(testManifest())
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := registry.Register(testDescriptor(t))
	if err != nil {
		t.Fatal(err)
	}
	sealed.RequiredScopes[0] = "rin.elevated"
	sealed.Input.Document[0] = 'x'

	resolved, ok := registry.Resolve(sealed.Capability)
	if !ok {
		t.Fatal("registered descriptor disappeared")
	}
	if resolved.RequiredScopes[0] != "rin.npc.move" ||
		resolved.Input.Document[0] == 'x' {
		t.Fatalf("caller mutated registry state: %+v", resolved)
	}
}

func TestRegistryConcurrentDiscoveryAndRevocation(t *testing.T) {
	registry, err := NewRegistry(testManifest())
	if err != nil {
		t.Fatal(err)
	}
	descriptor := testDescriptor(t)
	sealed, err := registry.Register(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	offer := ActionOffer{
		OfferID:          "offer.concurrent.1",
		DecisionWindowID: "window.concurrent.1",
		ActorID:          "npc.guide",
		Capability:       sealed.Capability,
		DescriptorDigest: sealed.Digest,
		Description:      "Move to the dock.",
		Arguments:        json.RawMessage(`{"target":"dock"}`),
		ExpectedEpoch:    testEpoch(),
		ObservationSeq:   1,
		Deadline:         Timepoint{Clock: ClockRealtime, Value: 20_000},
	}

	var wait sync.WaitGroup
	for range 16 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for range 100 {
				_, _ = registry.Resolve(sealed.Capability)
				_ = registry.Snapshot()
				_ = registry.ValidateOffer(
					offer,
					Timepoint{Clock: ClockRealtime, Value: 10_000},
					offer.ExpectedEpoch,
				)
			}
		}()
	}
	wait.Add(1)
	go func() {
		defer wait.Done()
		for range 50 {
			registry.Unregister(sealed.Capability)
			_, _ = registry.Register(descriptor)
		}
	}()
	wait.Wait()
}

func TestManifestAndActionRunInvariants(t *testing.T) {
	manifest := testManifest()
	if err := ValidateHostManifest(manifest); err != nil {
		t.Fatal(err)
	}
	inflated := manifest
	inflated.Durability.Profile = DurabilityTransactional
	if err := ValidateHostManifest(inflated); err == nil {
		t.Fatal("inflated durability claim accepted")
	}
	client := manifest
	client.Authority = AuthorityClientAdvisory
	client.Durability = Durability{
		Profile:              DurabilityIdempotent,
		StableIdentity:       true,
		DurableBeforeNetwork: true,
		DurableOutbox:        true,
		IdempotentApply:      true,
	}
	if err := ValidateHostManifest(client); err == nil {
		t.Fatal("client-advisory host claimed durable mutation authority")
	}

	if !CanTransitionActionRun(ActionQueued, ActionRunning) ||
		!CanTransitionActionRun(ActionRunning, ActionOutcomeUnknown) ||
		!CanTransitionActionRun(ActionOutcomeUnknown, ActionSucceeded) {
		t.Fatal("valid action lifecycle transition rejected")
	}
	if CanTransitionActionRun(ActionSucceeded, ActionRunning) ||
		CanTransitionActionRun(ActionFailed, ActionSucceeded) {
		t.Fatal("terminal action lifecycle transition accepted")
	}
	if err := ValidateActionRun(ActionRun{
		OperationID: "operation.move.1",
		Status:      ActionSucceeded,
		ProgressSeq: 3,
		Progress:    100,
		UpdatedAt:   Timepoint{Clock: ClockRealtime, Value: 12_000},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCapabilityVersionIsExactSemVer(t *testing.T) {
	for _, version := range []string{
		"1",
		"1.0",
		"01.0.0",
		"1.0.0-01",
		"1.0.0-alpha..1",
		"1.0.0+build..1",
		"v1.0.0",
	} {
		ref := CapabilityRef{ID: "rin.test.capability", Version: version}
		if err := ref.Validate("capability"); err == nil {
			t.Fatalf("invalid SemVer accepted: %q", version)
		}
	}
	for _, version := range []string{
		"0.0.0",
		"1.2.3",
		"1.2.3-alpha.1",
		"1.2.3-alpha+build.01",
	} {
		ref := CapabilityRef{ID: "rin.test.capability", Version: version}
		if err := ref.Validate("capability"); err != nil {
			t.Fatalf("valid SemVer %q rejected: %v", version, err)
		}
	}
}

func testManifest() HostManifest {
	return HostManifest{
		ContractVersion:     ContractVersion,
		AdapterID:           "rin.test",
		AdapterVersion:      "1.0.0",
		EngineID:            "test",
		EngineVersion:       "1",
		Runtime:             "go",
		Platform:            "windows",
		Headless:            true,
		Authority:           AuthorityServer,
		Deployment:          DeploymentDedicatedServer,
		Control:             ControlSemantic,
		ClockModes:          []ClockMode{ClockStep, ClockRealtime},
		DecisionModes:       []DecisionMode{DecisionSequential, DecisionAsynchronous},
		MaxConcurrentActors: 100,
		Durability:          Durability{Profile: DurabilityAdvisory, StableIdentity: true},
	}
}

func testDescriptor(t *testing.T) CapabilityDescriptor {
	t.Helper()
	input, err := NewSchema([]byte(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{"target":{"type":"string","minLength":1}},
		"required":["target"],
		"additionalProperties":false
	}`))
	if err != nil {
		t.Fatal(err)
	}
	output, err := NewSchema([]byte(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{"state":{"enum":["reached","unreachable","interrupted"]}},
		"required":["state"],
		"additionalProperties":false
	}`))
	if err != nil {
		t.Fatal(err)
	}
	return CapabilityDescriptor{
		Capability:         CapabilityRef{ID: "rin.navigation.move-to", Version: "1.0.0"},
		Description:        "Move through the host navigation system.",
		Input:              input,
		Output:             output,
		Effect:             EffectWorldMutation,
		Execution:          ExecutionLongRunning,
		Risk:               RiskModerate,
		RequiredDurability: DurabilityAdvisory,
		RequiredScopes:     []string{"rin.npc.move"},
		ExecutionBudget:    Duration{Clock: ClockRealtime, Value: 10_000},
		MaxInputBytes:      1024,
		MaxOutputBytes:     1024,
		Cancellation:       CancellationCooperative,
	}
}

func testEpoch() Epoch {
	return Epoch{
		SessionID: "session.test",
		WorldID:   "world.test",
		Host:      1,
		World:     2,
		Timeline:  3,
	}
}
