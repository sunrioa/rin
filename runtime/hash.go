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

type checkpointHashInput struct {
	FormatVersion     string            `json:"format_version"`
	ProjectionVersion string            `json:"projection_version"`
	SessionID         string            `json:"session_id"`
	Revision          uint64            `json:"revision"`
	HeadHash          string            `json:"head_hash"`
	LineageEpoch      uint64            `json:"lineage_epoch"`
	Snapshot          protocol.Snapshot `json:"snapshot"`
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

// VerifyEventRecord verifies one canonical EventRecord against an immutable
// chain anchor. It is exported so optional ranged Store implementations do not
// have to duplicate the event-hash serialization contract.
func VerifyEventRecord(
	previousRevision uint64,
	previousHash string,
	event protocol.EventRecord,
) error {
	return verifyEvent(protocol.SessionState{
		Revision: previousRevision,
		HeadHash: previousHash,
	}, event)
}

func SnapshotOf(state protocol.SessionState) (protocol.Snapshot, error) {
	return snapshotWithIdentifiers(state, identifiersFromState(state))
}

func snapshotWithIdentifiers(
	state protocol.SessionState,
	identifiers protocol.IdentifierHistory,
) (protocol.Snapshot, error) {
	return buildSnapshotWithIdentifiers(state, identifiers, true)
}

func buildSnapshotWithIdentifiers(
	state protocol.SessionState,
	identifiers protocol.IdentifierHistory,
	enforceInlineLimit bool,
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
	snapshot := protocol.Snapshot{
		ProtocolVersion:       protocol.Version,
		StateHash:             hash,
		State:                 copyState,
		IdentifierHistory:     &copyIdentifiers,
		IdentifierHistoryHash: identifierHash,
	}
	if enforceInlineLimit {
		if size, err := checkInlineSnapshotSize(snapshot, MaxInlineSnapshotBytes); err != nil {
			return protocol.Snapshot{}, snapshotTooLargeError(size, err)
		}
	}
	return snapshot, nil
}

// BuildCheckpoint creates a validated runtime replay cache without applying the
// public inline Snapshot size ceiling.
func BuildCheckpoint(
	state protocol.SessionState,
	identifiers protocol.IdentifierHistory,
	lineageEpoch uint64,
) (Checkpoint, error) {
	snapshot, err := buildSnapshotWithIdentifiers(state, identifiers, false)
	if err != nil {
		return Checkpoint{}, err
	}
	checkpoint := Checkpoint{
		FormatVersion:     CheckpointFormatVersion,
		ProjectionVersion: ReducerProjectionVersion,
		SessionID:         snapshot.State.SessionID,
		Revision:          snapshot.State.Revision,
		HeadHash:          snapshot.State.HeadHash,
		LineageEpoch:      lineageEpoch,
		Snapshot:          snapshot,
	}
	checkpoint.Checksum, err = checkpointChecksum(checkpoint)
	if err != nil {
		return Checkpoint{}, err
	}
	return checkpoint, nil
}

// ValidateCheckpoint validates the derived wrapper, complete Identifier
// History, reducer compatibility, and wrapper checksum. It deliberately does
// not enforce the public inline Snapshot size ceiling.
func ValidateCheckpoint(checkpoint Checkpoint) error {
	if checkpoint.FormatVersion != CheckpointFormatVersion {
		return fmt.Errorf("checkpoint format %q is unsupported", checkpoint.FormatVersion)
	}
	if checkpoint.ProjectionVersion != ReducerProjectionVersion {
		return fmt.Errorf("checkpoint projection %q is unsupported", checkpoint.ProjectionVersion)
	}
	if checkpoint.SessionID == "" ||
		checkpoint.SessionID != checkpoint.Snapshot.State.SessionID {
		return fmt.Errorf("checkpoint session id does not match snapshot")
	}
	if checkpoint.Revision == 0 ||
		checkpoint.Revision != checkpoint.Snapshot.State.Revision {
		return fmt.Errorf("checkpoint revision does not match snapshot")
	}
	if checkpoint.HeadHash == "" ||
		checkpoint.HeadHash != checkpoint.Snapshot.State.HeadHash {
		return fmt.Errorf("checkpoint head hash does not match snapshot")
	}
	if checkpoint.Snapshot.IdentifierHistory == nil {
		return fmt.Errorf("checkpoint requires complete identifier history projection")
	}
	if err := validateSnapshotContents(checkpoint.Snapshot); err != nil {
		return fmt.Errorf("checkpoint snapshot is invalid: %w", err)
	}
	expected, err := checkpointChecksum(checkpoint)
	if err != nil {
		return fmt.Errorf("checkpoint cannot be hashed: %w", err)
	}
	if checkpoint.Checksum == "" || checkpoint.Checksum != expected {
		return fmt.Errorf("checkpoint checksum does not match")
	}
	return nil
}

func checkpointChecksum(checkpoint Checkpoint) (string, error) {
	return hashJSON(checkpointHashInput{
		FormatVersion:     checkpoint.FormatVersion,
		ProjectionVersion: checkpoint.ProjectionVersion,
		SessionID:         checkpoint.SessionID,
		Revision:          checkpoint.Revision,
		HeadHash:          checkpoint.HeadHash,
		LineageEpoch:      checkpoint.LineageEpoch,
		Snapshot:          checkpoint.Snapshot,
	})
}

func ValidateSnapshot(snapshot protocol.Snapshot) error {
	return validateSnapshot(snapshot, MaxInlineSnapshotBytes)
}

func validateSnapshot(snapshot protocol.Snapshot, maximumInlineBytes int) error {
	if err := validateSnapshotHeader(snapshot); err != nil {
		return err
	}
	if size, err := checkInlineSnapshotSize(snapshot, maximumInlineBytes); err != nil {
		return snapshotTooLargeErrorForLimit(size, maximumInlineBytes, err)
	}
	return validateSnapshotBody(snapshot)
}

func validateSnapshotHeader(snapshot protocol.Snapshot) error {
	if snapshot.ProtocolVersion != protocol.Version {
		return NewFieldError("invalid_snapshot", "snapshot protocol version is unsupported", "snapshot.protocol_version", nil)
	}
	if snapshot.State.ProtocolVersion != protocol.Version {
		return NewFieldError("invalid_snapshot", "snapshot state protocol version is unsupported", "snapshot.state.protocol_version", nil)
	}
	if snapshot.State.SessionID == "" {
		return NewFieldError("invalid_snapshot", "snapshot session id is required", "snapshot.state.session_id", nil)
	}
	return nil
}

// validateSnapshotContents deliberately excludes the inline transport limit.
// Durable restore events written by an older release may contain a Snapshot
// larger than today's inline contract and must remain replayable. New
// Snapshot, Replay, and Restore API boundaries call ValidateSnapshot first.
func validateSnapshotContents(snapshot protocol.Snapshot) error {
	if err := validateSnapshotHeader(snapshot); err != nil {
		return err
	}
	return validateSnapshotBody(snapshot)
}

func validateSnapshotBody(snapshot protocol.Snapshot) error {
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
		legacyIdentifiers := identifiersFromState(snapshot.State)
		if err := protocol.ValidateIdentifierHistory(
			legacyIdentifiers,
			snapshot.State.SessionID,
		); err != nil {
			return NewError(
				"invalid_snapshot",
				"legacy snapshot cannot seed valid identifier history",
				err,
			)
		}
		if err := validateIdentifiersCoverState(legacyIdentifiers, snapshot.State); err != nil {
			return NewError(
				"invalid_snapshot",
				"legacy snapshot identifier projection does not cover retained state",
				err,
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
		if receipt.RequestHash != "" &&
			identity.RequestHash != "" &&
			identity.RequestHash != receipt.RequestHash {
			return fmt.Errorf("identifier history hash does not match retained request id %q", requestID)
		}
		if receipt.Revision != 0 &&
			identity.ResultRevision != 0 &&
			identity.ResultRevision != receipt.Revision {
			return fmt.Errorf("identifier history revision does not match retained request id %q", requestID)
		}
		if receipt.Revision == state.Revision {
			if identity.Ambiguous {
				if identity.ResultHeadHash != "" && identity.ResultHeadHash != state.HeadHash {
					return fmt.Errorf("identifier history head does not match the current request id %q", requestID)
				}
			} else {
				if identity.ResultRevision != state.Revision {
					return fmt.Errorf("identifier history is missing the current revision for request id %q", requestID)
				}
				if identity.ResultHeadHash != state.HeadHash {
					return fmt.Errorf("identifier history head does not match the current request id %q", requestID)
				}
			}
		}
		switch receipt.Kind {
		case EventObserved:
			event, found := history.Events[receipt.EntityID]
			if !found {
				return fmt.Errorf("identifier history is missing observation event id %q", receipt.EntityID)
			}
			if event.Kind != "" && event.Kind != EventObserved {
				return fmt.Errorf("identifier history kind does not match observation event id %q", receipt.EntityID)
			}
			if event.RequestID != "" && event.RequestID != requestID {
				return fmt.Errorf("identifier history request does not match observation event id %q", receipt.EntityID)
			}
			if receipt.Revision != 0 &&
				event.Revision != 0 &&
				event.Revision != receipt.Revision {
				return fmt.Errorf("identifier history revision does not match observation event id %q", receipt.EntityID)
			}
		case EventProposed:
			if identity.Proposal != nil {
				if receipt.EntityID != identity.Proposal.ID {
					return fmt.Errorf("identifier history proposal does not match retained request id %q", requestID)
				}
			} else if !identity.Ambiguous {
				return fmt.Errorf("identifier history proposal does not match retained request id %q", requestID)
			}
		case EventArbitrated:
			if identity.Arbitration != nil {
				if receipt.EntityID != identity.Arbitration.ID {
					return fmt.Errorf("identifier history arbitration does not match retained request id %q", requestID)
				}
			} else if !identity.Ambiguous {
				return fmt.Errorf("identifier history arbitration does not match retained request id %q", requestID)
			}
		}
	}
	for _, proposal := range state.Proposals {
		if err := validateRetainedProposalIdentity(history, proposal); err != nil {
			return err
		}
		if err := validateRetainedEventKind(
			history,
			proposal.OutcomeEventID,
			"proposal outcome",
			EventCommitted,
			EventBatchCommitted,
		); err != nil {
			return err
		}
	}
	for _, actor := range state.Actors {
		for _, goal := range actor.Goals {
			if err := validateRetainedEventKind(
				history,
				goal.StatusSourceEventID,
				"goal status",
				EventCommitted,
				EventBatchCommitted,
			); err != nil {
				return err
			}
		}
		for _, memory := range actor.Memories {
			if err := validateRetainedEventKind(
				history,
				memory.EventID,
				"memory",
				EventObserved,
				EventCommitted,
				EventBatchCommitted,
			); err != nil {
				return err
			}
		}
		for _, summary := range actor.MemorySummaries {
			for _, eventID := range summary.SourceEventIDs {
				if err := validateRetainedEventKind(
					history,
					eventID,
					"memory summary",
					EventObserved,
					EventCommitted,
					EventBatchCommitted,
				); err != nil {
					return err
				}
			}
		}
		for _, proposal := range actor.RecentActions {
			if err := validateRetainedProposalIdentity(history, proposal); err != nil {
				return err
			}
			if err := validateRetainedEventKind(
				history,
				proposal.OutcomeEventID,
				"recent action outcome",
				EventCommitted,
				EventBatchCommitted,
			); err != nil {
				return err
			}
		}
		for _, fact := range actor.Beliefs {
			if err := validateRetainedEventKind(
				history,
				fact.SourceEventID,
				"belief",
				EventObserved,
				EventCommitted,
				EventBatchCommitted,
			); err != nil {
				return err
			}
		}
		for _, set := range actor.BeliefSets {
			for _, claim := range set.Claims {
				if err := validateRetainedEventKind(
					history,
					claim.Fact.SourceEventID,
					"belief claim",
					EventObserved,
					EventCommitted,
					EventBatchCommitted,
				); err != nil {
					return err
				}
			}
		}
	}
	for _, record := range state.Arbitrations {
		identity := history.Requests[record.RequestID]
		if identity.Arbitration == nil {
			if identity.Ambiguous {
				continue
			}
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

func validateRetainedEventKind(
	history protocol.IdentifierHistory,
	eventID string,
	source string,
	allowed ...string,
) error {
	if eventID == "" {
		return nil
	}
	identity, found := history.Events[eventID]
	if !found {
		return fmt.Errorf("identifier history is missing %s event id %q", source, eventID)
	}
	if identity.Kind == "" {
		return nil
	}
	for _, kind := range allowed {
		if identity.Kind == kind {
			return nil
		}
	}
	return fmt.Errorf("identifier history kind %q is invalid for %s event id %q", identity.Kind, source, eventID)
}

func validateRetainedProposalIdentity(
	history protocol.IdentifierHistory,
	proposal protocol.ActionProposal,
) error {
	identity := history.Requests[proposal.RequestID]
	if identity.Proposal == nil {
		if identity.Ambiguous {
			return nil
		}
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
