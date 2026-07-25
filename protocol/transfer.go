package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"time"
)

const (
	TransferVersion       = "rin.session-transfer/v1"
	TransferHashAlgorithm = "sha256"

	TransferFrameManifest = "manifest"
	TransferFrameEvent    = "event"
	TransferFrameComplete = "complete"
	TransferFrameError    = "error"

	// TransferControlFrameMaxBytes bounds manifest, complete, and error
	// frames independently from EventRecord payloads.
	TransferControlFrameMaxBytes = 32 * 1024
	// TransferEventFrameMaxBytes includes the Store's 64 MiB EventRecord
	// ceiling plus bounded framing and checksum members.
	TransferEventFrameMaxBytes = 64*1024*1024 + TransferControlFrameMaxBytes
)

// TransferManifest fixes the immutable boundary of one complete-lineage
// export. Version 1 starts at genesis; the start fields are explicit so a
// future incremental protocol cannot accidentally interpret a revision alone
// as a trusted base.
type TransferManifest struct {
	Type              string  `json:"type"`
	TransferVersion   string  `json:"transfer_version"`
	ProtocolVersion   string  `json:"protocol_version"`
	ProjectionVersion string  `json:"projection_version"`
	TransferID        string  `json:"transfer_id"`
	SessionID         string  `json:"session_id"`
	Binding           Binding `json:"binding"`
	StartRevision     uint64  `json:"start_revision"`
	StartHeadHash     string  `json:"start_head_hash"`
	TerminalRevision  uint64  `json:"terminal_revision"`
	TerminalHeadHash  string  `json:"terminal_head_hash"`
	EventCount        uint64  `json:"event_count"`
	LineageGeneration uint64  `json:"lineage_generation"`
	HashAlgorithm     string  `json:"hash_algorithm"`
}

// TransferEvent carries one original EventRecord plus a transport checksum.
// RecordSHA256 does not replace EventRecord.Hash: the former protects the
// transfer representation while the latter remains the authoritative chain.
type TransferEvent struct {
	Type         string      `json:"type"`
	Record       EventRecord `json:"record"`
	RecordSHA256 string      `json:"record_sha256"`
}

// TransferComplete is the only successful terminal frame. StreamSHA256 covers
// the canonical manifest and event frames, including one LF delimiter after
// each frame, and excludes this complete frame.
type TransferComplete struct {
	Type             string `json:"type"`
	TerminalRevision uint64 `json:"terminal_revision"`
	TerminalHeadHash string `json:"terminal_head_hash"`
	EventCount       uint64 `json:"event_count"`
	StreamSHA256     string `json:"stream_sha256"`
}

// TransferError is the only non-successful terminal response frame. It is
// emitted only when an HTTP export fails after the manifest has started.
type TransferError struct {
	Type  string      `json:"type"`
	Error ErrorDetail `json:"error"`
}

func ValidateTransferManifest(manifest TransferManifest) error {
	if manifest.Type != TransferFrameManifest {
		return transferValidationError("type", "must equal "+TransferFrameManifest)
	}
	if manifest.TransferVersion != TransferVersion {
		return transferValidationError(
			"transfer_version",
			"must equal "+TransferVersion,
		)
	}
	if err := validateVersion(manifest.ProtocolVersion); err != nil {
		return err
	}
	if err := validateText(
		"projection_version",
		manifest.ProjectionVersion,
		96,
		true,
	); err != nil {
		return err
	}
	if err := validateID("transfer_id", manifest.TransferID); err != nil {
		return err
	}
	if err := validateID("session_id", manifest.SessionID); err != nil {
		return err
	}
	if err := validateBinding("binding", manifest.Binding); err != nil {
		return err
	}
	if err := validateJSONSafeUnsigned(
		"start_revision",
		manifest.StartRevision,
	); err != nil {
		return err
	}
	if err := validateJSONSafeUnsigned(
		"terminal_revision",
		manifest.TerminalRevision,
	); err != nil {
		return err
	}
	if err := validateJSONSafeUnsigned("event_count", manifest.EventCount); err != nil {
		return err
	}
	if err := validateJSONSafeUnsigned(
		"lineage_generation",
		manifest.LineageGeneration,
	); err != nil {
		return err
	}
	if manifest.StartRevision != 0 || manifest.StartHeadHash != "" {
		return transferValidationError(
			"start_revision",
			"version 1 transfers must start at genesis",
		)
	}
	if manifest.EventCount == 0 {
		return transferValidationError(
			"event_count",
			"must be greater than zero",
		)
	}
	if manifest.TerminalRevision != manifest.EventCount {
		return transferValidationError(
			"terminal_revision",
			"must equal event_count for a genesis transfer",
		)
	}
	if !hashPattern.MatchString(manifest.TerminalHeadHash) {
		return transferValidationError(
			"terminal_head_hash",
			"must be a lowercase SHA-256 hash",
		)
	}
	if manifest.HashAlgorithm != TransferHashAlgorithm {
		return transferValidationError(
			"hash_algorithm",
			"must equal "+TransferHashAlgorithm,
		)
	}
	return nil
}

func ValidateTransferEvent(frame TransferEvent) error {
	if frame.Type != TransferFrameEvent {
		return transferValidationError("type", "must equal "+TransferFrameEvent)
	}
	if frame.Record.Sequence == 0 {
		return transferValidationError(
			"record.sequence",
			"must be greater than zero",
		)
	}
	if err := validateJSONSafeUnsigned(
		"record.sequence",
		frame.Record.Sequence,
	); err != nil {
		return err
	}
	if err := validateID("record.type", frame.Record.Type); err != nil {
		return err
	}
	if err := validateID("record.request_id", frame.Record.RequestID); err != nil {
		return err
	}
	if frame.Record.Sequence == 1 {
		if frame.Record.PrevHash != "" {
			return transferValidationError(
				"record.prev_hash",
				"must be empty for the first event",
			)
		}
	} else if !hashPattern.MatchString(frame.Record.PrevHash) {
		return transferValidationError(
			"record.prev_hash",
			"must be a lowercase SHA-256 hash",
		)
	}
	if !hashPattern.MatchString(frame.Record.Hash) {
		return transferValidationError(
			"record.hash",
			"must be a lowercase SHA-256 hash",
		)
	}
	if _, err := time.Parse(time.RFC3339Nano, frame.Record.RecordedAt); err != nil {
		return transferValidationError(
			"record.recorded_at",
			"must be an RFC 3339 timestamp",
		)
	}
	if len(frame.Record.Data) == 0 || !json.Valid(frame.Record.Data) {
		return transferValidationError(
			"record.data",
			"must contain one valid JSON value",
		)
	}
	if !hashPattern.MatchString(frame.RecordSHA256) {
		return transferValidationError(
			"record_sha256",
			"must be a lowercase SHA-256 hash",
		)
	}
	expected, err := TransferEventRecordSHA256(frame.Record)
	if err != nil {
		return fmt.Errorf("hash transfer event record: %w", err)
	}
	if frame.RecordSHA256 != expected {
		return transferValidationError(
			"record_sha256",
			"does not match record",
		)
	}
	return nil
}

func ValidateTransferComplete(complete TransferComplete) error {
	if complete.Type != TransferFrameComplete {
		return transferValidationError("type", "must equal "+TransferFrameComplete)
	}
	if err := validateJSONSafeUnsigned(
		"terminal_revision",
		complete.TerminalRevision,
	); err != nil {
		return err
	}
	if err := validateJSONSafeUnsigned("event_count", complete.EventCount); err != nil {
		return err
	}
	if complete.TerminalRevision == 0 {
		return transferValidationError(
			"terminal_revision",
			"must be greater than zero",
		)
	}
	if !hashPattern.MatchString(complete.TerminalHeadHash) {
		return transferValidationError(
			"terminal_head_hash",
			"must be a lowercase SHA-256 hash",
		)
	}
	if !hashPattern.MatchString(complete.StreamSHA256) {
		return transferValidationError(
			"stream_sha256",
			"must be a lowercase SHA-256 hash",
		)
	}
	return nil
}

func ValidateTransferError(frame TransferError) error {
	if frame.Type != TransferFrameError {
		return transferValidationError("type", "must equal "+TransferFrameError)
	}
	if !validErrorCode(frame.Error.Code) {
		return transferValidationError("error.code", "must be a valid error code")
	}
	if frame.Error.Message == "" {
		return transferValidationError("error.message", "is required")
	}
	if len([]rune(frame.Error.Code)) > ErrorCodeMaxLength {
		return transferValidationError("error.code", "exceeds the maximum length")
	}
	if len([]rune(frame.Error.Message)) > ErrorMessageMaxLength {
		return transferValidationError("error.message", "exceeds the maximum length")
	}
	if len([]rune(frame.Error.Field)) > ErrorFieldMaxLength {
		return transferValidationError("error.field", "exceeds the maximum length")
	}
	return nil
}

// ValidateTransferCompleteAgainstManifest verifies the repeated immutable
// boundary. StreamSHA256 is compared by TransferStreamHasher.VerifyComplete.
func ValidateTransferCompleteAgainstManifest(
	complete TransferComplete,
	manifest TransferManifest,
) error {
	if err := ValidateTransferManifest(manifest); err != nil {
		return err
	}
	if err := ValidateTransferComplete(complete); err != nil {
		return err
	}
	if complete.TerminalRevision != manifest.TerminalRevision {
		return transferValidationError(
			"terminal_revision",
			"must match manifest",
		)
	}
	if complete.TerminalHeadHash != manifest.TerminalHeadHash {
		return transferValidationError(
			"terminal_head_hash",
			"must match manifest",
		)
	}
	if complete.EventCount != manifest.EventCount {
		return transferValidationError("event_count", "must match manifest")
	}
	return nil
}

// TransferEventRecordSHA256 hashes the compact JSON encoding of the complete
// EventRecord. Go's encoding/json provides stable struct field ordering and
// deterministic map-key ordering; implementations in other languages must
// reproduce the byte shape specified in docs/session-transfer.md.
func TransferEventRecordSHA256(record EventRecord) (string, error) {
	payload, err := json.Marshal(record)
	if err != nil {
		return "", err
	}
	return sha256Hex(payload), nil
}

// TransferStreamHasher incrementally hashes validated manifest and event
// frames without retaining them. It deliberately cannot accept a complete
// frame, preventing a checksum from recursively covering itself.
type TransferStreamHasher struct {
	digest          hash.Hash
	manifestWritten bool
	manifest        TransferManifest
	eventCount      uint64
	previousHash    string
}

func NewTransferStreamHasher() *TransferStreamHasher {
	return &TransferStreamHasher{digest: sha256.New()}
}

func (h *TransferStreamHasher) WriteManifest(manifest TransferManifest) error {
	if h == nil || h.digest == nil {
		return errors.New("transfer stream hasher is not initialized")
	}
	if h.manifestWritten {
		return errors.New("transfer manifest has already been hashed")
	}
	if err := ValidateTransferManifest(manifest); err != nil {
		return err
	}
	if err := h.writeCanonicalFrame(manifest); err != nil {
		return err
	}
	h.manifestWritten = true
	h.manifest = manifest
	return nil
}

func (h *TransferStreamHasher) WriteEvent(frame TransferEvent) error {
	if h == nil || h.digest == nil {
		return errors.New("transfer stream hasher is not initialized")
	}
	if !h.manifestWritten {
		return errors.New("transfer manifest must be hashed before events")
	}
	if err := ValidateTransferEvent(frame); err != nil {
		return err
	}
	if h.eventCount >= h.manifest.EventCount {
		return transferValidationError(
			"record.sequence",
			"exceeds the manifest event_count",
		)
	}
	if frame.Record.Sequence != h.eventCount+1 {
		return transferValidationError(
			"record.sequence",
			fmt.Sprintf("must equal %d", h.eventCount+1),
		)
	}
	if frame.Record.PrevHash != h.previousHash {
		return transferValidationError(
			"record.prev_hash",
			"does not match the preceding event hash",
		)
	}
	if err := h.writeCanonicalFrame(frame); err != nil {
		return err
	}
	h.eventCount++
	h.previousHash = frame.Record.Hash
	return nil
}

func (h *TransferStreamHasher) SumSHA256() (string, error) {
	if h == nil || h.digest == nil {
		return "", errors.New("transfer stream hasher is not initialized")
	}
	if !h.manifestWritten {
		return "", errors.New("transfer manifest has not been hashed")
	}
	return hex.EncodeToString(h.digest.Sum(nil)), nil
}

func (h *TransferStreamHasher) VerifyComplete(
	complete TransferComplete,
	manifest TransferManifest,
) error {
	if h == nil || h.digest == nil {
		return errors.New("transfer stream hasher is not initialized")
	}
	if err := ValidateTransferCompleteAgainstManifest(complete, manifest); err != nil {
		return err
	}
	if h.eventCount != manifest.EventCount {
		return transferValidationError(
			"event_count",
			"does not match the number of hashed event frames",
		)
	}
	if h.manifest != manifest {
		return errors.New("transfer complete manifest differs from hashed manifest")
	}
	if h.previousHash != manifest.TerminalHeadHash {
		return transferValidationError(
			"terminal_head_hash",
			"does not match the final event frame",
		)
	}
	actual, err := h.SumSHA256()
	if err != nil {
		return err
	}
	if complete.StreamSHA256 != actual {
		return transferValidationError(
			"stream_sha256",
			"does not match manifest and event frames",
		)
	}
	return nil
}

func (h *TransferStreamHasher) writeCanonicalFrame(frame any) error {
	payload, err := json.Marshal(frame)
	if err != nil {
		return fmt.Errorf("encode canonical transfer frame: %w", err)
	}
	if _, err := h.digest.Write(payload); err != nil {
		return fmt.Errorf("hash canonical transfer frame: %w", err)
	}
	if _, err := h.digest.Write([]byte{'\n'}); err != nil {
		return fmt.Errorf("hash transfer frame delimiter: %w", err)
	}
	return nil
}

func sha256Hex(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func transferValidationError(field, message string) error {
	return &ValidationError{Field: field, Message: message}
}
