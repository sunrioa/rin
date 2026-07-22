package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	copyState, err := clone(state)
	if err != nil {
		return protocol.Snapshot{}, err
	}
	hash, err := hashJSON(copyState)
	if err != nil {
		return protocol.Snapshot{}, err
	}
	return protocol.Snapshot{
		ProtocolVersion: protocol.Version,
		StateHash:       hash,
		State:           copyState,
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
