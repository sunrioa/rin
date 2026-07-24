package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"github.com/sunrioa/rin/protocol"
)

type eventHashInput struct {
	Sequence   uint64          `json:"sequence"`
	Type       string          `json:"type"`
	RequestID  string          `json:"request_id"`
	PrevHash   string          `json:"prev_hash"`
	RecordedAt string          `json:"recorded_at"`
	Data       json.RawMessage `json:"data"`
}

func hashJSON(value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func newEvent(state protocol.SessionState, eventType, requestID string, payload any, now time.Time) (protocol.EventRecord, error) {
	if state.Revision == ^uint64(0) {
		return protocol.EventRecord{}, NewFieldError(
			"revision_overflow",
			"session revision is exhausted",
			"revision",
			ErrConflict,
		)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return protocol.EventRecord{}, err
	}
	event := protocol.EventRecord{
		Sequence:   state.Revision + 1,
		Type:       eventType,
		RequestID:  requestID,
		PrevHash:   state.HeadHash,
		RecordedAt: now.UTC().Format(time.RFC3339Nano),
		Data:       data,
	}
	event.Hash, err = eventHash(event)
	if err != nil {
		return protocol.EventRecord{}, err
	}
	return event, nil
}

func eventHash(event protocol.EventRecord) (string, error) {
	return hashJSON(eventHashInput{
		Sequence:   event.Sequence,
		Type:       event.Type,
		RequestID:  event.RequestID,
		PrevHash:   event.PrevHash,
		RecordedAt: event.RecordedAt,
		Data:       event.Data,
	})
}

func verifyEvent(previous protocol.SessionState, event protocol.EventRecord) error {
	if previous.Revision == ^uint64(0) {
		return fmt.Errorf("%w: revision %d cannot be followed by another event", ErrCorruptLog, previous.Revision)
	}
	if event.Sequence != previous.Revision+1 {
		return fmt.Errorf("%w: sequence %d follows revision %d", ErrCorruptLog, event.Sequence, previous.Revision)
	}
	if event.PrevHash != previous.HeadHash {
		return fmt.Errorf("%w: previous hash mismatch at sequence %d", ErrCorruptLog, event.Sequence)
	}
	expected, err := eventHash(event)
	if err != nil {
		return err
	}
	if expected != event.Hash {
		return fmt.Errorf("%w: hash mismatch at sequence %d", ErrCorruptLog, event.Sequence)
	}
	return nil
}

func SnapshotOf(state protocol.SessionState) (protocol.Snapshot, error) {
	return snapshotWithIdentifiers(state, identifiersFromState(state))
}

func snapshotWithIdentifiers(
	state protocol.SessionState,
	identifiers protocol.IdentifierHistory,
) (protocol.Snapshot, error) {
	copyState, err := clone(state)
	if err != nil {
		return protocol.Snapshot{}, err
	}
	if err := protocol.ValidateSessionState(copyState); err != nil {
		return protocol.Snapshot{}, fmt.Errorf("validate snapshot state: %w", err)
	}
	hash, err := hashJSON(copyState)
	if err != nil {
		return protocol.Snapshot{}, err
	}
	copyIdentifiers, err := cloneIdentifierHistory(identifiers)
	if err != nil {
		return protocol.Snapshot{}, err
	}
	if err := protocol.ValidateIdentifierHistory(copyIdentifiers, copyState.SessionID); err != nil {
		return protocol.Snapshot{}, fmt.Errorf("validate snapshot identifier history: %w", err)
	}
	if err := validateIdentifiersCoverState(copyIdentifiers, copyState); err != nil {
		return protocol.Snapshot{}, err
	}
	identifierHash, err := hashJSON(copyIdentifiers)
	if err != nil {
		return protocol.Snapshot{}, err
	}
	return protocol.Snapshot{
		ProtocolVersion:       protocol.Version,
		StateHash:             hash,
		State:                 copyState,
		IdentifierHistory:     &copyIdentifiers,
		IdentifierHistoryHash: identifierHash,
	}, nil
}

func ValidateSnapshot(snapshot protocol.Snapshot) error {
	if snapshot.ProtocolVersion != protocol.Version {
		return NewFieldError("invalid_snapshot", "snapshot protocol version is unsupported", "snapshot.protocol_version", nil)
	}
	if snapshot.State.ProtocolVersion != protocol.Version {
		return NewFieldError("invalid_snapshot", "snapshot state protocol version is unsupported", "snapshot.state.protocol_version", nil)
	}
	if snapshot.State.SessionID == "" {
		return NewFieldError("invalid_snapshot", "snapshot session id is required", "snapshot.state.session_id", nil)
	}
	if err := protocol.ValidateSessionState(snapshot.State); err != nil {
		return NewError("invalid_snapshot", "snapshot contains invalid session state", err)
	}
	expected, err := hashJSON(snapshot.State)
	if err != nil {
		return NewError("invalid_snapshot", "snapshot state cannot be hashed", err)
	}
	if expected != snapshot.StateHash {
		return NewFieldError("invalid_snapshot", "snapshot state hash does not match", "snapshot.state_hash", ErrCorruptLog)
	}
	if snapshot.IdentifierHistory == nil {
		if snapshot.IdentifierHistoryHash != "" {
			return NewFieldError(
				"invalid_snapshot",
				"snapshot identifier history hash requires identifier history",
				"snapshot.identifier_history_hash",
				ErrCorruptLog,
			)
		}
		return nil
	}
	if err := protocol.ValidateIdentifierHistory(*snapshot.IdentifierHistory, snapshot.State.SessionID); err != nil {
		return NewError("invalid_snapshot", "snapshot contains invalid identifier history", err)
	}
	if err := validateIdentifiersCoverState(*snapshot.IdentifierHistory, snapshot.State); err != nil {
		return NewError("invalid_snapshot", "snapshot identifier history does not cover retained state", err)
	}
	expectedIdentifiers, err := hashJSON(*snapshot.IdentifierHistory)
	if err != nil {
		return NewError("invalid_snapshot", "snapshot identifier history cannot be hashed", err)
	}
	if expectedIdentifiers != snapshot.IdentifierHistoryHash {
		return NewFieldError(
			"invalid_snapshot",
			"snapshot identifier history hash does not match",
			"snapshot.identifier_history_hash",
			ErrCorruptLog,
		)
	}
	return nil
}

func validateIdentifiersCoverState(
	history protocol.IdentifierHistory,
	state protocol.SessionState,
) error {
	retained := identifiersFromState(state)
	for requestID, retainedIdentity := range retained.Requests {
		identity, found := history.Requests[requestID]
		if !found {
			return fmt.Errorf("identifier history is missing retained request id %q", requestID)
		}
		if identity.Kind != "" &&
			retainedIdentity.Kind != "" &&
			identity.Kind != retainedIdentity.Kind {
			return fmt.Errorf("identifier history kind does not match retained request id %q", requestID)
		}
	}
	for requestID, receipt := range state.Receipts {
		identity := history.Requests[requestID]
		if identity.Ambiguous {
			continue
		}
		if receipt.RequestHash != "" && identity.RequestHash != receipt.RequestHash {
			return fmt.Errorf("identifier history hash does not match retained request id %q", requestID)
		}
		if receipt.Revision != 0 && identity.ResultRevision != receipt.Revision {
			return fmt.Errorf("identifier history revision does not match retained request id %q", requestID)
		}
		if receipt.Revision != 0 &&
			receipt.Revision == state.Revision &&
			identity.ResultHeadHash != state.HeadHash {
			return fmt.Errorf("identifier history head does not match the current request id %q", requestID)
		}
		switch receipt.Kind {
		case EventProposed:
			if identity.Proposal == nil || receipt.EntityID != identity.Proposal.ID {
				return fmt.Errorf("identifier history proposal does not match retained request id %q", requestID)
			}
		case EventArbitrated:
			if identity.Arbitration == nil || receipt.EntityID != identity.Arbitration.ID {
				return fmt.Errorf("identifier history arbitration does not match retained request id %q", requestID)
			}
		}
	}
	for _, proposal := range state.Proposals {
		if err := validateRetainedProposalIdentity(history, proposal); err != nil {
			return err
		}
	}
	for _, actor := range state.Actors {
		for _, proposal := range actor.RecentActions {
			if err := validateRetainedProposalIdentity(history, proposal); err != nil {
				return err
			}
		}
	}
	for _, record := range state.Arbitrations {
		identity := history.Requests[record.RequestID]
		if identity.Ambiguous {
			continue
		}
		if identity.Arbitration == nil {
			return fmt.Errorf("identifier history is missing retained arbitration result %q", record.RequestID)
		}
		comparable := record
		comparable.CreatedRevision = identity.Arbitration.CreatedRevision
		if !reflect.DeepEqual(comparable, *identity.Arbitration) {
			return fmt.Errorf("identifier history result does not match retained arbitration %q", record.ID)
		}
	}
	for eventID := range retained.Events {
		if _, found := history.Events[eventID]; !found {
			return fmt.Errorf("identifier history is missing retained event id %q", eventID)
		}
	}
	return nil
}

func validateRetainedProposalIdentity(
	history protocol.IdentifierHistory,
	proposal protocol.ActionProposal,
) error {
	identity := history.Requests[proposal.RequestID]
	if identity.Ambiguous {
		return nil
	}
	if identity.Proposal == nil {
		return fmt.Errorf("identifier history is missing retained proposal result %q", proposal.RequestID)
	}
	original := *identity.Proposal
	comparable := proposal
	// Restore rebases chain-local revision fields, outcome reporting updates the
	// status fields, and memory compaction may replace recalled memory IDs with
	// summary IDs. All other proposal fields are immutable result data.
	comparable.BasedOnRevision = original.BasedOnRevision
	comparable.BasedOnHeadHash = original.BasedOnHeadHash
	comparable.BasedOnWorldRevision = original.BasedOnWorldRevision
	comparable.CreatedRevision = original.CreatedRevision
	comparable.RecalledMemoryIDs = original.RecalledMemoryIDs
	comparable.Status = original.Status
	comparable.OutcomeEventID = original.OutcomeEventID
	comparable.OutcomeTick = original.OutcomeTick
	if !reflect.DeepEqual(comparable, original) {
		return fmt.Errorf("identifier history result does not match retained proposal %q", proposal.ID)
	}
	return nil
}

func clone[T any](value T) (T, error) {
	var result T
	payload, err := json.Marshal(value)
	if err != nil {
		return result, err
	}
	err = json.Unmarshal(payload, &result)
	return result, err
}
