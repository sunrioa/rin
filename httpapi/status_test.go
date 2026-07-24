package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
)

func TestErrorDetailOutputMatchesContractBounds(t *testing.T) {
	response := httptest.NewRecorder()
	server := &Server{}
	server.writeError(
		response,
		http.StatusBadRequest,
		strings.Repeat("a", protocol.ErrorCodeMaxLength+100),
		strings.Repeat("界", protocol.ErrorMessageMaxLength+100),
		strings.Repeat("f", protocol.ErrorFieldMaxLength+100),
	)
	var envelope protocol.APIResponse
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error == nil {
		t.Fatal("bounded response has no error detail")
	}
	if utf8.RuneCountInString(envelope.Error.Code) != protocol.ErrorCodeMaxLength ||
		utf8.RuneCountInString(envelope.Error.Message) != protocol.ErrorMessageMaxLength ||
		utf8.RuneCountInString(envelope.Error.Field) != protocol.ErrorFieldMaxLength {
		t.Fatalf("error detail was not bounded by the contract: %+v", envelope.Error)
	}
}

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
