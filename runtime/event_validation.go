package runtime

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/internal/jsonwire"
	"github.com/sunrioa/rin/protocol"
)

func decodeAndValidateEventPayload(
	state protocol.SessionState,
	event protocol.EventRecord,
) (any, error) {
	if err := protocol.ValidateEventRecord(event); err != nil {
		return nil, corruptEvent("invalid event envelope", err)
	}

	switch event.Type {
	case EventSessionCreated:
		var payload createdPayload
		if err := decodeEventPayload(event.Data, &payload); err != nil {
			return nil, corruptEvent("decode create payload", err)
		}
		if err := protocol.ValidateCreateSession(payload.Request); err != nil {
			return nil, corruptEvent("invalid create payload", err)
		}
		if err := validateEventRequestID(event, payload.Request.RequestID); err != nil {
			return nil, err
		}
		return payload, nil

	case EventObserved:
		var payload observedPayload
		if err := decodeEventPayload(event.Data, &payload); err != nil {
			return nil, corruptEvent("decode observe payload", err)
		}
		if err := protocol.ValidateObserve(payload.Request); err != nil {
			return nil, corruptEvent("invalid observe payload", err)
		}
		if err := validateMutationEvent(state, event, payload.Request.SessionID, payload.Request.RequestID); err != nil {
			return nil, err
		}
		return payload, nil

	case EventProposed:
		var payload proposedPayload
		if err := decodeEventPayload(event.Data, &payload); err != nil {
			return nil, corruptEvent("decode proposal payload", err)
		}
		proposal := payload.Proposal
		if err := validateMutationEvent(state, event, proposal.SessionID, proposal.RequestID); err != nil {
			return nil, err
		}
		if proposal.CreatedRevision != event.Sequence ||
			proposal.BasedOnRevision != state.Revision ||
			proposal.BasedOnHeadHash != state.HeadHash {
			return nil, corruptEvent("invalid proposal payload", errors.New(
				"proposal revision anchor must match the preceding event",
			))
		}
		if proposal.DecisionWindow.Epoch.SessionID != state.SessionID ||
			proposal.Action.ExpectedEpoch.SessionID != state.SessionID {
			return nil, corruptEvent("invalid proposal payload", errors.New(
				"proposal epochs must belong to the event Session",
			))
		}
		if err := host.ValidateActionOffer(proposal.Action); err != nil {
			return nil, corruptEvent("invalid proposal action", err)
		}
		return payload, nil

	case EventActionReported:
		var payload actionReportedPayload
		if err := decodeEventPayload(event.Data, &payload); err != nil {
			return nil, corruptEvent("decode action report payload", err)
		}
		if err := protocol.ValidateReportAction(payload.Request); err != nil {
			return nil, corruptEvent("invalid action report payload", err)
		}
		if err := validateMutationEvent(state, event, payload.Request.SessionID, payload.Request.RequestID); err != nil {
			return nil, err
		}
		return payload, nil

	case EventActionBatchReported:
		var payload actionBatchReportedPayload
		if err := decodeEventPayload(event.Data, &payload); err != nil {
			return nil, corruptEvent("decode batch action report payload", err)
		}
		if err := protocol.ValidateBatchActionReport(payload.Request); err != nil {
			return nil, corruptEvent("invalid batch action report payload", err)
		}
		if err := validateMutationEvent(state, event, payload.Request.SessionID, payload.Request.RequestID); err != nil {
			return nil, err
		}
		return payload, nil

	case EventActivityUpdated:
		var payload activityUpdatedPayload
		if err := decodeEventPayload(event.Data, &payload); err != nil {
			return nil, corruptEvent("decode activity payload", err)
		}
		if err := protocol.ValidateSetActorActivity(payload.Request); err != nil {
			return nil, corruptEvent("invalid activity payload", err)
		}
		if err := validateMutationEvent(state, event, payload.Request.SessionID, payload.Request.RequestID); err != nil {
			return nil, err
		}
		return payload, nil

	case EventAgencyUpdated:
		var payload agencyUpdatedPayload
		if err := decodeEventPayload(event.Data, &payload); err != nil {
			return nil, corruptEvent("decode agency payload", err)
		}
		if err := protocol.ValidateSetActorAgency(payload.Request); err != nil {
			return nil, corruptEvent("invalid agency payload", err)
		}
		if err := validateMutationEvent(state, event, payload.Request.SessionID, payload.Request.RequestID); err != nil {
			return nil, err
		}
		return payload, nil

	case EventArbitrated:
		var payload arbitratedPayload
		if err := decodeEventPayload(event.Data, &payload); err != nil {
			return nil, corruptEvent("decode arbitration payload", err)
		}
		if err := validateEventRequestID(event, payload.Record.RequestID); err != nil {
			return nil, err
		}
		if state.SessionID == "" {
			return nil, corruptEvent("invalid arbitration payload", errors.New("Session is not initialized"))
		}
		if payload.Record.CreatedRevision != event.Sequence {
			return nil, corruptEvent("invalid arbitration payload", errors.New(
				"created_revision must equal the event sequence",
			))
		}
		return payload, nil

	case EventSessionRestored:
		var payload restoredPayload
		if err := decodeEventPayload(event.Data, &payload); err != nil {
			return nil, corruptEvent("decode restore payload", err)
		}
		if err := validateSnapshotContents(payload.Snapshot); err != nil {
			return nil, corruptEvent("invalid restore payload", err)
		}
		return payload, nil

	default:
		return nil, fmt.Errorf("%w: unknown event type %q", ErrCorruptLog, event.Type)
	}
}

func decodeEventPayload(data json.RawMessage, target any) error {
	if err := jsonwire.Validate(data); err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func validateMutationEvent(
	state protocol.SessionState,
	event protocol.EventRecord,
	sessionID string,
	requestID string,
) error {
	if state.SessionID == "" || sessionID != state.SessionID {
		return corruptEvent("invalid event Session", errors.New(
			"payload session_id must equal the current Session",
		))
	}
	return validateEventRequestID(event, requestID)
}

func validateEventRequestID(event protocol.EventRecord, requestID string) error {
	if event.RequestID != requestID {
		return corruptEvent("invalid event request identity", errors.New(
			"payload request_id must equal the event request_id",
		))
	}
	return nil
}

func corruptEvent(message string, err error) error {
	return fmt.Errorf("%w: %s: %v", ErrCorruptLog, message, err)
}
