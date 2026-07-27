package host

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHostSequenceFieldsRequirePositiveJSONSafeIntegers(t *testing.T) {
	const unsafe = uint64(maxInteroperableInteger) + 1

	epoch := testEpoch()
	epoch.Host = unsafe
	if err := epoch.Validate("epoch"); err == nil {
		t.Fatal("unsafe epoch generation was accepted")
	}

	offer := ActionOffer{
		OfferID:          "offer.test",
		DecisionWindowID: "window.test",
		ActorID:          "npc.test",
		Capability:       CapabilityRef{ID: "rin.test.wait", Version: "1.0.0"},
		DescriptorDigest: strings.Repeat("a", 64),
		Description:      "Wait.",
		Arguments:        json.RawMessage(`{}`),
		ExpectedEpoch:    testEpoch(),
		ObservationSeq:   unsafe,
		Deadline:         Timepoint{Clock: ClockStep, Value: 2},
	}
	if err := ValidateActionOffer(offer); err == nil {
		t.Fatal("unsafe observation sequence was accepted")
	}

	run := ActionRun{
		OperationID: "operation.test",
		Status:      ActionRunning,
		ProgressSeq: unsafe,
		UpdatedAt:   Timepoint{Clock: ClockStep, Value: 1},
	}
	if err := ValidateActionRun(run); err == nil {
		t.Fatal("unsafe progress sequence was accepted")
	}
	run.ProgressSeq = 0
	if err := ValidateActionRun(run); err == nil {
		t.Fatal("zero progress sequence was accepted")
	}

	outcome := ActionOutcome{
		OperationID: "operation.test",
		Status:      ActionSucceeded,
		Summary:     "Done.",
		Epoch:       testEpoch(),
		WorldSeq:    unsafe,
		OccurredAt:  Timepoint{Clock: ClockStep, Value: 1},
	}
	if err := ValidateActionOutcome(outcome); err == nil {
		t.Fatal("unsafe world sequence was accepted")
	}
}
