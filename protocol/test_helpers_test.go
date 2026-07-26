package protocol

import (
	"encoding/json"
	"strings"

	"github.com/sunrioa/rin/host"
)

func testProtocolEpoch(sessionID string) Epoch {
	return Epoch{
		SessionID: sessionID,
		WorldID:   "world.test",
		Host:      1,
		World:     1,
		Timeline:  1,
	}
}

func testDecisionWindow(sessionID, actorID string, observationSeq uint64) DecisionWindow {
	epoch := testProtocolEpoch(sessionID)
	return DecisionWindow{
		ID:             "window.test",
		Mode:           host.DecisionSequential,
		Epoch:          epoch,
		ObservationSeq: observationSeq,
		OpenedAt:       Timepoint{Clock: host.ClockStep, Value: 1},
		Deadline:       Timepoint{Clock: host.ClockStep, Value: 10},
		ActorIDs:       []string{actorID},
	}
}

func testActionOffer(sessionID, actorID, offerID string, observationSeq uint64) ActionOffer {
	window := testDecisionWindow(sessionID, actorID, observationSeq)
	return ActionOffer{
		OfferID:          offerID,
		DecisionWindowID: window.ID,
		ActorID:          actorID,
		Capability:       CapabilityRef{ID: "rin.test." + offerID, Version: "1.0.0"},
		DescriptorDigest: strings.Repeat("a", 64),
		Description:      "Test " + offerID + " offer.",
		Arguments:        json.RawMessage(`{}`),
		ExpectedEpoch:    window.Epoch,
		ObservationSeq:   window.ObservationSeq,
		Deadline:         window.Deadline,
	}
}
