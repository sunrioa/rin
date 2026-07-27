package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/sunrioa/rin/generation"
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

func TestSessionQuotaMapsToInsufficientStorage(t *testing.T) {
	response := httptest.NewRecorder()
	server := &Server{}
	server.respond(
		response,
		nil,
		nil,
		rinruntime.NewError(
			"session_quota_exceeded",
			"Session storage limit reached",
			rinruntime.ErrConflict,
		),
	)
	if response.Code != http.StatusInsufficientStorage {
		t.Fatalf(
			"status = %d, want %d",
			response.Code,
			http.StatusInsufficientStorage,
		)
	}
}

func TestTransferResourceErrorsMapToBoundedStatuses(t *testing.T) {
	tests := []struct {
		code   string
		status int
	}{
		{code: "transfer_too_large", status: http.StatusRequestEntityTooLarge},
		{code: "transfer_event_limit", status: http.StatusRequestEntityTooLarge},
		{code: "transfer_capacity", status: http.StatusTooManyRequests},
		{code: "transfer_in_progress", status: http.StatusConflict},
		{code: "state_too_large", status: http.StatusRequestEntityTooLarge},
	}
	for _, test := range tests {
		t.Run(test.code, func(t *testing.T) {
			response := httptest.NewRecorder()
			server := &Server{}
			server.respond(
				response,
				nil,
				nil,
				rinruntime.NewError(
					test.code,
					"Transfer resource limit reached",
					rinruntime.ErrConflict,
				),
			)
			if response.Code != test.status {
				t.Fatalf(
					"status = %d, want %d",
					response.Code,
					test.status,
				)
			}
		})
	}
}

func TestGenerationMemoryLimitMapsToTooManyRequests(t *testing.T) {
	response := httptest.NewRecorder()
	server := &Server{}
	server.respond(
		response,
		nil,
		nil,
		rinruntime.NewError(
			"generation_memory_limit",
			"generation retained-memory capacity is full",
			generation.ErrMemoryLimit,
		),
	)
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf(
			"status = %d, want %d",
			response.Code,
			http.StatusTooManyRequests,
		)
	}
}

func TestTransferAndOrdinaryRequestsUseSeparateDeadlines(t *testing.T) {
	server := &Server{
		requestTimeout:            2 * time.Second,
		transferTimeout:           9 * time.Second,
		transferInactivityTimeout: time.Second,
	}
	tests := []struct {
		path string
		want time.Duration
	}{
		{path: "/v2/session/get", want: server.requestTimeout},
		{path: "/v2/session/export", want: server.transferTimeout},
		{path: "/v2/session/import", want: server.transferTimeout},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			var remaining time.Duration
			handler := server.withDeadlines(http.HandlerFunc(func(
				_ http.ResponseWriter,
				request *http.Request,
			) {
				deadline, ok := request.Context().Deadline()
				if !ok {
					t.Fatal("request context has no deadline")
				}
				remaining = time.Until(deadline)
			}))
			handler.ServeHTTP(
				httptest.NewRecorder(),
				httptest.NewRequest(http.MethodPost, test.path, nil),
			)
			if remaining < test.want-250*time.Millisecond ||
				remaining > test.want {
				t.Fatalf(
					"deadline remaining = %s, want approximately %s",
					remaining,
					test.want,
				)
			}
		})
	}
}

func TestTransferDeadlinesRefreshAroundBodyAndResponseIO(t *testing.T) {
	server := &Server{
		requestTimeout:            2 * time.Second,
		transferTimeout:           9 * time.Second,
		transferInactivityTimeout: time.Second,
	}
	recorder := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	handler := server.withDeadlines(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		if _, err := io.ReadAll(request.Body); err != nil {
			t.Fatal(err)
		}
		_, _ = response.Write([]byte("ok"))
	}))
	handler.ServeHTTP(
		recorder,
		httptest.NewRequest(
			http.MethodPost,
			"/v2/session/import",
			strings.NewReader("frame\n"),
		),
	)
	if len(recorder.readDeadlines) < 2 {
		t.Fatalf(
			"read deadline updates = %d, want initial plus Body.Read",
			len(recorder.readDeadlines),
		)
	}
	if len(recorder.writeDeadlines) < 2 {
		t.Fatalf(
			"write deadline updates = %d, want initial plus response Write",
			len(recorder.writeDeadlines),
		)
	}
	for _, deadline := range append(
		append([]time.Time(nil), recorder.readDeadlines...),
		recorder.writeDeadlines...,
	) {
		remaining := time.Until(deadline)
		if remaining < 750*time.Millisecond ||
			remaining > server.transferInactivityTimeout {
			t.Fatalf(
				"rolling deadline remaining = %s, want approximately %s",
				remaining,
				server.transferInactivityTimeout,
			)
		}
	}
}

type deadlineRecorder struct {
	*httptest.ResponseRecorder
	readDeadlines  []time.Time
	writeDeadlines []time.Time
}

func (r *deadlineRecorder) SetReadDeadline(deadline time.Time) error {
	r.readDeadlines = append(r.readDeadlines, deadline)
	return nil
}

func (r *deadlineRecorder) SetWriteDeadline(deadline time.Time) error {
	r.writeDeadlines = append(r.writeDeadlines, deadline)
	return nil
}

func TestClosedRuntimeMapsToServiceUnavailable(t *testing.T) {
	response := httptest.NewRecorder()
	server := &Server{}
	server.respond(
		response,
		nil,
		nil,
		rinruntime.NewError(
			"runtime_closed",
			"runtime is closed",
			rinruntime.ErrClosed,
		),
	)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf(
			"status = %d, want %d",
			response.Code,
			http.StatusServiceUnavailable,
		)
	}
}
