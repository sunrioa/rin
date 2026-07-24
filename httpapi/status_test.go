package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
)

func TestRecoveryErrorsAlwaysMapToInternalServerError(t *testing.T) {
	causes := map[string]error{
		"not found":        rinruntime.ErrNotFound,
		"conflict":         rinruntime.ErrConflict,
		"validation":       &protocol.ValidationError{Field: "session_id", Message: "invalid"},
		"context canceled": context.Canceled,
	}
	for _, code := range []string{"store_load_failed", "replay_failed"} {
		for causeName, cause := range causes {
			t.Run(code+"/"+causeName, func(t *testing.T) {
				response := httptest.NewRecorder()
				server := &Server{}
				server.respond(
					response,
					nil,
					rinruntime.NewError(code, "durable recovery failed", cause),
				)

				if response.Code != http.StatusInternalServerError {
					t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
				}
				var envelope protocol.APIResponse
				if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
					t.Fatal(err)
				}
				if envelope.Error == nil || envelope.Error.Code != code {
					t.Fatalf("error = %+v, want code %q", envelope.Error, code)
				}
			})
		}
	}
}
