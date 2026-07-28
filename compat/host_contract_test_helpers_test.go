package compat_test

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/protocol"
)

func compatEpoch(sessionID string) protocol.Epoch {
	return protocol.Epoch{
		SessionID: sessionID,
		WorldID:   "world.compat",
		Host:      1,
		World:     1,
		Timeline:  1,
	}
}

func compatPropose(
	sessionID, requestID, actorID string,
	tick int64,
	offerID string,
) protocol.ProposeRequest {
	epoch := compatEpoch(sessionID)
	window := protocol.DecisionWindow{
		ID:             fmt.Sprintf("window.%s.%d", requestID, tick),
		Mode:           host.DecisionSequential,
		Epoch:          epoch,
		ObservationSeq: uint64(tick) + 1,
		OpenedAt:       protocol.Timepoint{Clock: host.ClockStep, Value: tick},
		Deadline:       protocol.Timepoint{Clock: host.ClockStep, Value: tick + 100},
		ActorIDs:       []string{actorID},
	}
	return protocol.ProposeRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
		RequestID:       requestID,
		ActorID:         actorID,
		Tick:            tick,
		Intent:          "Choose an authored compatibility action.",
		DecisionWindow:  window,
		Offers: []protocol.ActionOffer{{
			OfferID:          offerID,
			DecisionWindowID: window.ID,
			ActorID:          actorID,
			Capability:       protocol.CapabilityRef{ID: "rin.compat." + offerID, Version: "1.0.0"},
			DescriptorDigest: strings.Repeat("a", 64),
			Description:      "Compatibility action.",
			Arguments:        json.RawMessage(`{}`),
			ExpectedEpoch:    epoch,
			ObservationSeq:   window.ObservationSeq,
			Deadline:         window.Deadline,
		}},
	}
}

func compatSuccessfulReport(
	proposal protocol.ActionProposal,
	requestID, eventID string,
	tick int64,
	summary string,
) protocol.ReportActionRequest {
	invocation := protocol.ActionInvocation{
		OperationID:      "operation." + eventID,
		OfferID:          proposal.Action.OfferID,
		DecisionWindowID: proposal.Action.DecisionWindowID,
		ActorID:          proposal.Action.ActorID,
		Capability:       proposal.Action.Capability,
		DescriptorDigest: proposal.Action.DescriptorDigest,
		Arguments:        append(json.RawMessage(nil), proposal.Action.Arguments...),
		Targets:          append([]protocol.HostRef(nil), proposal.Action.Targets...),
		ExpectedEpoch:    proposal.Action.ExpectedEpoch,
		ObservationSeq:   proposal.Action.ObservationSeq,
		Deadline:         proposal.Action.Deadline,
	}
	run := protocol.ActionRun{
		OperationID: invocation.OperationID,
		Status:      host.ActionSucceeded,
		ProgressSeq: 1,
		Progress:    100,
		UpdatedAt:   protocol.Timepoint{Clock: host.ClockStep, Value: tick},
	}
	outcome := protocol.ActionOutcome{
		OperationID: invocation.OperationID,
		Status:      host.ActionSucceeded,
		Summary:     summary,
		Epoch:       invocation.ExpectedEpoch,
		WorldSeq:    1,
		OccurredAt:  protocol.Timepoint{Clock: host.ClockStep, Value: tick},
	}
	return protocol.ReportActionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       proposal.SessionID,
		RequestID:       requestID,
		Tick:            tick,
		Report: protocol.ActionReport{
			ProposalID: proposal.ID,
			EventID:    eventID,
			Decision:   protocol.ActionAccepted,
			Invocation: &invocation,
			Run:        &run,
			Outcome:    &outcome,
			Summary:    summary,
		},
	}
}
