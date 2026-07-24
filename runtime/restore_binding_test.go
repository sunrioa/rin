package runtime_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/sunrioa/rin/policy"
	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
	"github.com/sunrioa/rin/store"
)

func TestRestoreExpectedBindingRejectsEveryMismatchedDimension(t *testing.T) {
	mutations := []struct {
		name   string
		mutate func(*protocol.Binding)
	}{
		{
			name: "game_id",
			mutate: func(binding *protocol.Binding) {
				binding.GameID = "game.other"
			},
		},
		{
			name: "content_id",
			mutate: func(binding *protocol.Binding) {
				binding.ContentID = "content.other"
			},
		},
		{
			name: "content_version",
			mutate: func(binding *protocol.Binding) {
				binding.ContentVersion = "2.0.0"
			},
		},
		{
			name: "content_hash",
			mutate: func(binding *protocol.Binding) {
				binding.ContentHash = "sha256-other"
			},
		},
	}

	for _, test := range mutations {
		t.Run("fresh/"+test.name, func(t *testing.T) {
			sessionID := "session.restore-binding-fresh-" + test.name
			snapshot := bindingSnapshot(t, sessionID, createRequest(sessionID).Binding)
			expected := snapshot.State.Binding
			test.mutate(&expected)
			target := newEngine(t, store.NewMemory(), policy.Deterministic{})

			_, err := target.Restore(protocol.RestoreRequest{
				ProtocolVersion: protocol.Version,
				SessionID:       sessionID,
				RequestID:       "restore.binding-fresh-" + test.name,
				ExpectedBinding: expected,
				Snapshot:        snapshot,
			})
			assertBindingMismatch(t, err)
			if _, stateErr := target.State(sessionRequest(sessionID)); rinruntime.ErrorCode(stateErr) != "session_not_found" {
				t.Fatalf("failed fresh restore registered a session: %v", stateErr)
			}
		})

		t.Run("existing/"+test.name, func(t *testing.T) {
			sessionID := "session.restore-binding-existing-" + test.name
			target := newEngine(t, store.NewMemory(), policy.Deterministic{})
			if _, err := target.CreateSession(createRequest(sessionID)); err != nil {
				t.Fatal(err)
			}
			before, err := target.State(sessionRequest(sessionID))
			if err != nil {
				t.Fatal(err)
			}
			importedBinding := before.Binding
			test.mutate(&importedBinding)
			snapshot := bindingSnapshot(t, sessionID, importedBinding)

			_, err = target.Restore(protocol.RestoreRequest{
				ProtocolVersion: protocol.Version,
				SessionID:       sessionID,
				RequestID:       "restore.binding-existing-" + test.name,
				ExpectedBinding: importedBinding,
				Snapshot:        snapshot,
			})
			assertBindingMismatch(t, err)
			after, stateErr := target.State(sessionRequest(sessionID))
			if stateErr != nil {
				t.Fatal(stateErr)
			}
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("binding rejection mutated existing state:\nbefore=%+v\nafter=%+v", before, after)
			}
		})
	}
}

func TestRestoreExpectedBindingAllowsFreshAndExistingExactRetries(t *testing.T) {
	t.Run("fresh", func(t *testing.T) {
		const sessionID = "session.restore-binding-exact-fresh"
		snapshot := bindingSnapshot(t, sessionID, createRequest(sessionID).Binding)
		target := newEngine(t, store.NewMemory(), policy.Deterministic{})
		request := protocol.RestoreRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       sessionID,
			RequestID:       "restore.binding-exact-fresh",
			ExpectedBinding: snapshot.State.Binding,
			Snapshot:        snapshot,
		}
		first, err := target.Restore(request)
		if err != nil {
			t.Fatal(err)
		}
		repeated, err := target.Restore(request)
		if err != nil || !repeated.Duplicate ||
			repeated.Revision != first.Revision ||
			repeated.HeadHash != first.HeadHash {
			t.Fatalf("fresh exact retry mismatch: first=%+v repeated=%+v err=%v", first, repeated, err)
		}
	})

	t.Run("existing", func(t *testing.T) {
		const sessionID = "session.restore-binding-exact-existing"
		target := newEngine(t, store.NewMemory(), policy.Deterministic{})
		if _, err := target.CreateSession(createRequest(sessionID)); err != nil {
			t.Fatal(err)
		}
		snapshot, err := target.Snapshot(sessionRequest(sessionID))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := target.Observe(observeRequest(
			sessionID,
			"observe.restore-binding-exact-existing",
			"event.restore-binding-exact-existing",
			1,
		)); err != nil {
			t.Fatal(err)
		}
		request := protocol.RestoreRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       sessionID,
			RequestID:       "restore.binding-exact-existing",
			ExpectedBinding: snapshot.State.Binding,
			Snapshot:        snapshot,
		}
		first, err := target.Restore(request)
		if err != nil {
			t.Fatal(err)
		}
		repeated, err := target.Restore(request)
		if err != nil || !repeated.Duplicate ||
			repeated.Revision != first.Revision ||
			repeated.HeadHash != first.HeadHash {
			t.Fatalf("existing exact retry mismatch: first=%+v repeated=%+v err=%v", first, repeated, err)
		}
	})
}

func bindingSnapshot(
	t *testing.T,
	sessionID string,
	binding protocol.Binding,
) protocol.Snapshot {
	t.Helper()
	source := newEngine(t, store.NewMemory(), policy.Deterministic{})
	request := createRequest(sessionID)
	request.Binding = binding
	if _, err := source.CreateSession(request); err != nil {
		t.Fatal(err)
	}
	snapshot, err := source.Snapshot(sessionRequest(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func assertBindingMismatch(t *testing.T, err error) {
	t.Helper()
	if rinruntime.ErrorCode(err) != "binding_mismatch" ||
		rinruntime.ErrorField(err) != "expected_binding" ||
		!errors.Is(err, rinruntime.ErrConflict) {
		t.Fatalf(
			"restore error = %v (code=%q field=%q), want binding_mismatch on expected_binding",
			err,
			rinruntime.ErrorCode(err),
			rinruntime.ErrorField(err),
		)
	}
}
