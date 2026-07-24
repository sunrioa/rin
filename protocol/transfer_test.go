package protocol_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sunrioa/rin/protocol"
)

func TestTransferFramesValidateAndHashDeterministically(t *testing.T) {
	record := validTransferRecord(t, 1, "", "a")
	recordHash, err := protocol.TransferEventRecordSHA256(record)
	if err != nil {
		t.Fatal(err)
	}
	frame := protocol.TransferEvent{
		Type:         protocol.TransferFrameEvent,
		Record:       record,
		RecordSHA256: recordHash,
	}
	manifest := validTransferManifest(record)

	first := protocol.NewTransferStreamHasher()
	if err := first.WriteManifest(manifest); err != nil {
		t.Fatal(err)
	}
	if err := first.WriteEvent(frame); err != nil {
		t.Fatal(err)
	}
	streamHash, err := first.SumSHA256()
	if err != nil {
		t.Fatal(err)
	}
	complete := protocol.TransferComplete{
		Type:             protocol.TransferFrameComplete,
		TerminalRevision: 1,
		TerminalHeadHash: record.Hash,
		EventCount:       1,
		StreamSHA256:     streamHash,
	}
	if err := first.VerifyComplete(complete, manifest); err != nil {
		t.Fatal(err)
	}

	second := protocol.NewTransferStreamHasher()
	if err := second.WriteManifest(manifest); err != nil {
		t.Fatal(err)
	}
	if err := second.WriteEvent(frame); err != nil {
		t.Fatal(err)
	}
	repeated, err := second.SumSHA256()
	if err != nil {
		t.Fatal(err)
	}
	if repeated != streamHash {
		t.Fatalf("stream hashes differ: %s != %s", repeated, streamHash)
	}
	const expectedRecordHash = "0ac451364e53dcfd27de5cefd5b08a2b3b1a07b61eadffe09c8f2ee258aa4866"
	const expectedStreamHash = "af5eca5a53ffe136f35656061f758fe0e45bd2b5a5d70f01d65bf53d2d8c50ba"
	if recordHash != expectedRecordHash {
		t.Fatalf("record hash = %s, want cross-language vector %s", recordHash, expectedRecordHash)
	}
	if streamHash != expectedStreamHash {
		t.Fatalf("stream hash = %s, want cross-language vector %s", streamHash, expectedStreamHash)
	}
}

func TestTransferManifestRejectsNonGenesisAndUnsafeBounds(t *testing.T) {
	record := validTransferRecord(t, 1, "", "b")
	valid := validTransferManifest(record)
	tests := []struct {
		name   string
		field  string
		mutate func(*protocol.TransferManifest)
	}{
		{
			name: "wrong frame type", field: "type",
			mutate: func(value *protocol.TransferManifest) { value.Type = "event" },
		},
		{
			name: "unsupported transfer version", field: "transfer_version",
			mutate: func(value *protocol.TransferManifest) {
				value.TransferVersion = "rin.session-transfer/v2"
			},
		},
		{
			name: "incremental start", field: "start_revision",
			mutate: func(value *protocol.TransferManifest) {
				value.StartRevision = 1
				value.StartHeadHash = strings.Repeat("b", 64)
			},
		},
		{
			name: "empty lineage", field: "event_count",
			mutate: func(value *protocol.TransferManifest) {
				value.EventCount = 0
				value.TerminalRevision = 0
			},
		},
		{
			name: "count mismatch", field: "terminal_revision",
			mutate: func(value *protocol.TransferManifest) {
				value.TerminalRevision = 2
			},
		},
		{
			name: "zero lineage generation", field: "lineage_generation",
			mutate: func(value *protocol.TransferManifest) {
				value.LineageGeneration = 0
			},
		},
		{
			name: "unsafe integer", field: "lineage_generation",
			mutate: func(value *protocol.TransferManifest) {
				value.LineageGeneration = protocol.MaxJSONSafeInteger + 1
			},
		},
		{
			name: "unknown hash algorithm", field: "hash_algorithm",
			mutate: func(value *protocol.TransferManifest) {
				value.HashAlgorithm = "sha512"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := valid
			test.mutate(&value)
			requireTransferValidationField(
				t,
				protocol.ValidateTransferManifest(value),
				test.field,
			)
		})
	}
}

func TestTransferEventRejectsCorruptionAndSequenceGaps(t *testing.T) {
	record := validTransferRecord(t, 1, "", "c")
	digest, err := protocol.TransferEventRecordSHA256(record)
	if err != nil {
		t.Fatal(err)
	}
	valid := protocol.TransferEvent{
		Type:         protocol.TransferFrameEvent,
		Record:       record,
		RecordSHA256: digest,
	}
	tests := []struct {
		name   string
		field  string
		mutate func(*protocol.TransferEvent)
	}{
		{
			name: "first previous hash", field: "record.prev_hash",
			mutate: func(value *protocol.TransferEvent) {
				value.Record.PrevHash = strings.Repeat("a", 64)
			},
		},
		{
			name: "invalid timestamp", field: "record.recorded_at",
			mutate: func(value *protocol.TransferEvent) {
				value.Record.RecordedAt = "yesterday"
			},
		},
		{
			name: "invalid data", field: "record.data",
			mutate: func(value *protocol.TransferEvent) {
				value.Record.Data = json.RawMessage(`{"broken"`)
			},
		},
		{
			name: "record checksum mismatch", field: "record_sha256",
			mutate: func(value *protocol.TransferEvent) {
				value.Record.Data = json.RawMessage(`{"value":"changed"}`)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := valid
			test.mutate(&value)
			requireTransferValidationField(
				t,
				protocol.ValidateTransferEvent(value),
				test.field,
			)
		})
	}

	hasher := protocol.NewTransferStreamHasher()
	manifest := validTransferManifest(record)
	if err := hasher.WriteManifest(manifest); err != nil {
		t.Fatal(err)
	}
	gapRecord := validTransferRecord(t, 2, record.Hash, "d")
	gapHash, err := protocol.TransferEventRecordSHA256(gapRecord)
	if err != nil {
		t.Fatal(err)
	}
	requireTransferValidationField(
		t,
		hasher.WriteEvent(protocol.TransferEvent{
			Type:         protocol.TransferFrameEvent,
			Record:       gapRecord,
			RecordSHA256: gapHash,
		}),
		"record.sequence",
	)

	firstHasher := protocol.NewTransferStreamHasher()
	if err := firstHasher.WriteManifest(manifest); err != nil {
		t.Fatal(err)
	}
	firstDigest, err := protocol.TransferEventRecordSHA256(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := firstHasher.WriteEvent(protocol.TransferEvent{
		Type:         protocol.TransferFrameEvent,
		Record:       record,
		RecordSHA256: firstDigest,
	}); err != nil {
		t.Fatal(err)
	}
	secondRecord := validTransferRecord(
		t,
		2,
		strings.Repeat("f", 64),
		"e",
	)
	secondDigest, err := protocol.TransferEventRecordSHA256(secondRecord)
	if err != nil {
		t.Fatal(err)
	}
	requireTransferValidationField(
		t,
		firstHasher.WriteEvent(protocol.TransferEvent{
			Type:         protocol.TransferFrameEvent,
			Record:       secondRecord,
			RecordSHA256: secondDigest,
		}),
		"record.sequence",
	)

	twoEventManifest := manifest
	twoEventManifest.EventCount = 2
	twoEventManifest.TerminalRevision = 2
	twoEventManifest.TerminalHeadHash = secondRecord.Hash
	chainHasher := protocol.NewTransferStreamHasher()
	if err := chainHasher.WriteManifest(twoEventManifest); err != nil {
		t.Fatal(err)
	}
	if err := chainHasher.WriteEvent(protocol.TransferEvent{
		Type:         protocol.TransferFrameEvent,
		Record:       record,
		RecordSHA256: firstDigest,
	}); err != nil {
		t.Fatal(err)
	}
	requireTransferValidationField(
		t,
		chainHasher.WriteEvent(protocol.TransferEvent{
			Type:         protocol.TransferFrameEvent,
			Record:       secondRecord,
			RecordSHA256: secondDigest,
		}),
		"record.prev_hash",
	)
}

func TestTransferCompleteRejectsBoundaryAndStreamMismatch(t *testing.T) {
	record := validTransferRecord(t, 1, "", "e")
	manifest := validTransferManifest(record)
	hasher := protocol.NewTransferStreamHasher()
	if err := hasher.WriteManifest(manifest); err != nil {
		t.Fatal(err)
	}
	recordDigest, err := protocol.TransferEventRecordSHA256(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := hasher.WriteEvent(protocol.TransferEvent{
		Type:         protocol.TransferFrameEvent,
		Record:       record,
		RecordSHA256: recordDigest,
	}); err != nil {
		t.Fatal(err)
	}
	streamDigest, err := hasher.SumSHA256()
	if err != nil {
		t.Fatal(err)
	}
	valid := protocol.TransferComplete{
		Type:             protocol.TransferFrameComplete,
		TerminalRevision: manifest.TerminalRevision,
		TerminalHeadHash: manifest.TerminalHeadHash,
		EventCount:       manifest.EventCount,
		StreamSHA256:     streamDigest,
	}

	wrongBoundary := valid
	wrongBoundary.TerminalHeadHash = strings.Repeat("f", 64)
	requireTransferValidationField(
		t,
		hasher.VerifyComplete(wrongBoundary, manifest),
		"terminal_head_hash",
	)

	wrongStream := valid
	wrongStream.StreamSHA256 = strings.Repeat("0", 64)
	requireTransferValidationField(
		t,
		hasher.VerifyComplete(wrongStream, manifest),
		"stream_sha256",
	)
}

func validTransferManifest(record protocol.EventRecord) protocol.TransferManifest {
	return protocol.TransferManifest{
		Type:              protocol.TransferFrameManifest,
		TransferVersion:   protocol.TransferVersion,
		ProtocolVersion:   protocol.Version,
		ProjectionVersion: "rin.reducer-projection/v2",
		TransferID:        "transfer.test",
		SessionID:         "session.test",
		Binding: protocol.Binding{
			GameID:         "game.test",
			ContentID:      "content.test",
			ContentVersion: "1.0.0",
			ContentHash:    "sha256-test",
		},
		TerminalRevision:  1,
		TerminalHeadHash:  record.Hash,
		EventCount:        1,
		LineageGeneration: 1,
		HashAlgorithm:     protocol.TransferHashAlgorithm,
	}
}

func validTransferRecord(
	t *testing.T,
	sequence uint64,
	previousHash string,
	value string,
) protocol.EventRecord {
	t.Helper()
	record := protocol.EventRecord{
		Sequence:   sequence,
		Type:       "session.created",
		RequestID:  "request.test",
		PrevHash:   previousHash,
		RecordedAt: "2026-07-25T00:00:00Z",
		Data:       json.RawMessage(`{"value":"` + value + `"}`),
	}
	record.Hash = strings.Repeat("a", 64)
	if sequence > 1 {
		record.Hash = strings.Repeat("b", 64)
	}
	return record
}

func requireTransferValidationField(t *testing.T, err error, field string) {
	t.Helper()
	validation, ok := err.(*protocol.ValidationError)
	if !ok {
		t.Fatalf("expected validation error for %s, got %v", field, err)
	}
	if validation.Field != field {
		t.Fatalf("validation field = %q, want %q", validation.Field, field)
	}
}
