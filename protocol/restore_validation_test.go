package protocol_test

import (
	"testing"

	"github.com/sunrioa/rin/protocol"
)

func TestRestoreValidationRequiresExpectedBinding(t *testing.T) {
	binding := protocol.Binding{
		GameID:         "game.test",
		ContentID:      "content.base",
		ContentVersion: "1.0.0",
		ContentHash:    "sha256-test",
	}
	request := protocol.RestoreRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       "session.restore-validation",
		RequestID:       "restore.validation",
		Snapshot: protocol.Snapshot{
			State: protocol.SessionState{
				SessionID: "session.restore-validation",
				Binding:   binding,
			},
		},
	}

	err := protocol.ValidateRestore(request)
	if err == nil {
		t.Fatal("restore without expected_binding unexpectedly validated")
	}
	validation, ok := err.(*protocol.ValidationError)
	if !ok || validation.Field != "expected_binding.game_id" {
		t.Fatalf("missing expected_binding error = %#v", err)
	}

	request.ExpectedBinding = binding
	if err := protocol.ValidateRestore(request); err != nil {
		t.Fatalf("valid expected_binding was rejected: %v", err)
	}
}
