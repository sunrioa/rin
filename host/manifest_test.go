package host

import (
	"strings"
	"testing"
)

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
	if err := ValidateActionOutcome(ActionOutcome{
		OperationID: "operation.move.1",
		Status:      ActionOutcomeUnknown,
		Code:        "host.outcome_unknown",
		Summary:     "The Host cannot prove whether the effect completed.",
		Epoch:       testEpoch(),
		WorldSeq:    7,
		OccurredAt:  Timepoint{Clock: ClockStep, Value: 8},
	}); err != nil {
		t.Fatalf("authoritative outcome-unknown rejected: %v", err)
	}
}

func TestCapabilityVersionIsExactSemVer(t *testing.T) {
	for _, version := range []string{
		"1", "1.0", "01.0.0", "1.0.0-01", "1.0.0-alpha..1",
		"1.0.0+build..1", "v1.0.0",
	} {
		ref := CapabilityRef{ID: "rin.test.capability", Version: version}
		if err := ref.Validate("capability"); err == nil {
			t.Fatalf("invalid SemVer accepted: %q", version)
		}
	}
	for _, version := range []string{
		"0.0.0", "1.2.3", "1.2.3-alpha.1", "1.2.3-alpha+build.01",
	} {
		ref := CapabilityRef{ID: "rin.test.capability", Version: version}
		if err := ref.Validate("capability"); err != nil {
			t.Fatalf("valid SemVer %q rejected: %v", version, err)
		}
	}
}

func TestHostIdentifiersUseNinetySixByteCeiling(t *testing.T) {
	reference := CapabilityRef{
		ID:      "a." + strings.Repeat("b", 94),
		Version: "1.0.0",
	}
	if err := reference.Validate("capability"); err != nil {
		t.Fatalf("96-byte identifier rejected: %v", err)
	}
	reference.ID += "c"
	if err := reference.Validate("capability"); err == nil {
		t.Fatal("97-byte identifier was accepted")
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

func testEpoch() Epoch {
	return Epoch{
		SessionID: "session.test",
		WorldID:   "world.test",
		Host:      1,
		World:     2,
		Timeline:  3,
	}
}
