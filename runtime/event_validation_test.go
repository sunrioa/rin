package runtime

import (
	"errors"
	"testing"

	"github.com/sunrioa/rin/protocol"
)

func TestApplyEventRejectsInvalidAcceptedReportWithoutPanicking(t *testing.T) {
	state := invariantSessionState(t)
	proposal := invariantProposal(state, "proposal.malicious", "pending", nil)
	state.Proposals[proposal.ID] = proposal

	request := protocol.ReportActionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       state.SessionID,
		RequestID:       "report.malicious",
		Tick:            1,
		Report: protocol.ActionReport{
			ProposalID: proposal.ID,
			EventID:    "event.malicious",
			Decision:   protocol.ActionAccepted,
			Summary:    "Missing the required lifecycle records.",
		},
	}
	event := invariantEvent(
		t,
		state,
		EventActionReported,
		request.RequestID,
		actionReportedPayload{Request: request},
		2,
	)

	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("malicious event panicked: %v", recovered)
		}
	}()
	if _, err := applyEvent(state, event); !errors.Is(err, ErrCorruptLog) {
		t.Fatalf("applyEvent error = %v, want ErrCorruptLog", err)
	}
}
