package runtime

import (
	"encoding/json"
	"testing"
)

func TestInlineSnapshotLimitUsesCompactJSONBytes(t *testing.T) {
	snapshot, err := SnapshotOf(invariantSessionState(t))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if size, err := checkInlineSnapshotSize(snapshot, len(payload)); err != nil || size != len(payload) {
		t.Fatalf("exact inline limit: size=%d want=%d err=%v", size, len(payload), err)
	}
	if err := validateSnapshot(snapshot, len(payload)-1); ErrorCode(err) != "snapshot_too_large" {
		t.Fatalf("oversized otherwise-valid snapshot = %v, want snapshot_too_large", err)
	}
	if ErrorField(validateSnapshot(snapshot, len(payload)-1)) != "snapshot" {
		t.Fatal("snapshot size error did not identify the snapshot field")
	}
}

func TestSnapshotHashesAreIntegrityChecksNotAuthentication(t *testing.T) {
	snapshot, err := SnapshotOf(invariantSessionState(t))
	if err != nil {
		t.Fatal(err)
	}
	snapshot.State.Seed++
	snapshot.StateHash, err = hashJSON(snapshot.State)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateSnapshot(snapshot); err != nil {
		t.Fatalf("a coherently rehashed snapshot should remain structurally valid: %v", err)
	}
}
