package httpapi_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/httpapi"
	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
	"github.com/sunrioa/rin/store"
)

func TestOperationalEndpointsAndRequestCorrelation(t *testing.T) {
	var logs bytes.Buffer
	server := newServer(t, httpapi.Options{
		Token:  "secret-token",
		Logger: slog.New(slog.NewJSONHandler(&logs, nil)),
	})

	ready := httptest.NewRecorder()
	server.ServeHTTP(
		ready,
		loopbackRequest(http.MethodGet, "/ready", nil),
	)
	if ready.Code != http.StatusOK {
		t.Fatalf("ready status=%d body=%s", ready.Code, ready.Body.String())
	}

	unauthorizedMetrics := httptest.NewRecorder()
	server.ServeHTTP(
		unauthorizedMetrics,
		loopbackRequest(http.MethodGet, "/metrics", nil),
	)
	if unauthorizedMetrics.Code != http.StatusUnauthorized {
		t.Fatalf("metrics without token status=%d", unauthorizedMetrics.Code)
	}

	metricsRequest := loopbackRequest(http.MethodGet, "/metrics", nil)
	metricsRequest.Header.Set("Authorization", "Bearer secret-token")
	metricsRequest.Header.Set("Rin-Request-ID", "game.request-42")
	metrics := httptest.NewRecorder()
	server.ServeHTTP(metrics, metricsRequest)
	if metrics.Code != http.StatusOK ||
		metrics.Header().Get("Rin-Request-ID") != "game.request-42" ||
		!strings.Contains(metrics.Body.String(), "rin_http_requests_total") ||
		!strings.Contains(metrics.Body.String(), "rin_uncertainty_barriers") ||
		!strings.Contains(
			metrics.Body.String(),
			"rin_scrub_completed_cycles_total",
		) {
		t.Fatalf("unexpected metrics response: %d %q", metrics.Code, metrics.Body.String())
	}

	diagnosticsRequest := loopbackRequest(
		http.MethodGet,
		"/v2/diagnostics",
		nil,
	)
	diagnosticsRequest.Header.Set("Authorization", "Bearer secret-token")
	diagnosticsRequest.Header.Set("Rin-Request-ID", "unsafe request id")
	diagnostics := httptest.NewRecorder()
	server.ServeHTTP(diagnostics, diagnosticsRequest)
	if diagnostics.Code != http.StatusOK {
		t.Fatalf(
			"diagnostics status=%d body=%s",
			diagnostics.Code,
			diagnostics.Body.String(),
		)
	}
	if requestID := diagnostics.Header().Get("Rin-Request-ID"); requestID == "" || requestID == "unsafe request id" {
		t.Fatalf("unsafe correlation ID was not replaced: %q", requestID)
	}
	var envelope struct {
		OK   bool `json:"ok"`
		Data struct {
			Status  string                 `json:"status"`
			Runtime rinruntime.Diagnostics `json:"runtime"`
		} `json:"data"`
	}
	if err := json.Unmarshal(diagnostics.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.OK || envelope.Data.Status != "ready" {
		t.Fatalf("unexpected diagnostics: %+v", envelope)
	}
	if strings.Contains(logs.String(), "secret-token") ||
		!strings.Contains(logs.String(), `"request_id":"game.request-42"`) ||
		!strings.Contains(logs.String(), `"route":"GET /metrics"`) {
		t.Fatalf("unsafe or incomplete structured request log: %s", logs.String())
	}
}

func TestReadinessFailsWhenStoreCannotBeListed(t *testing.T) {
	eventStore := &readinessFailureStore{Store: store.NewMemory()}
	engine, err := rinruntime.Open(eventStore, cognition.Deterministic{})
	if err != nil {
		t.Fatal(err)
	}
	eventStore.fail = true
	server := httpapi.New(engine, httpapi.Options{})
	response := httptest.NewRecorder()
	server.ServeHTTP(
		response,
		loopbackRequest(http.MethodGet, "/ready", nil),
	)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("ready status=%d body=%s", response.Code, response.Body.String())
	}
	var envelope protocol.APIResponse
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error == nil || envelope.Error.Code != "not_ready" {
		t.Fatalf("unexpected readiness error: %+v", envelope.Error)
	}
}

type readinessFailureStore struct {
	rinruntime.Store
	fail bool
}

func (s *readinessFailureStore) ListSessions() ([]string, error) {
	if s.fail {
		return nil, errors.New("injected readiness failure")
	}
	return s.Store.ListSessions()
}
