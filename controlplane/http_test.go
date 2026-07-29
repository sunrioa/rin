package controlplane

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sunrioa/rin/host"
)

const testControlToken = "0123456789abcdef0123456789abcdef"

func TestHTTPHandlerRegistersAndPublishes(t *testing.T) {
	service := New(Options{
		Now:    func() time.Time { return time.UnixMilli(1_000_000) },
		Random: bytes.NewReader(bytes.Repeat([]byte{5}, 64)),
	})
	handler, err := NewHTTPHandler(service, HTTPOptions{Token: testControlToken})
	if err != nil {
		t.Fatalf("NewHTTPHandler: %v", err)
	}

	lease := HostLease{}
	response := requestJSON(t, handler, "/control/v1/register", registration("instance.one"))
	if response.Code != http.StatusOK {
		t.Fatalf("register status = %d, body = %s", response.Code, response.Body)
	}
	if err := json.Unmarshal(response.Body.Bytes(), &lease); err != nil {
		t.Fatalf("decode lease: %v", err)
	}

	response = requestJSON(t, handler, "/control/v1/publish", publishRequest{
		HostID:      "test.host",
		LeaseID:     lease.LeaseID,
		Publication: worldPublication(1, "ready"),
	})
	if response.Code != http.StatusOK {
		t.Fatalf("publish status = %d, body = %s", response.Code, response.Body)
	}
	actors, err := service.ListActors(
		readPrincipal(), "test.host", "world.one",
	)
	if err != nil || len(actors) != 1 {
		t.Fatalf("ListActors = %#v, %v", actors, err)
	}
}

func TestHTTPHandlerRequiresTokenAndStrictJSON(t *testing.T) {
	service := New(Options{})
	handler, err := NewHTTPHandler(service, HTTPOptions{Token: testControlToken})
	if err != nil {
		t.Fatalf("NewHTTPHandler: %v", err)
	}

	unauthorized := httptest.NewRequest(
		http.MethodPost, "/control/v1/register",
		bytes.NewReader([]byte(`{}`)),
	)
	unauthorized.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, unauthorized)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", response.Code)
	}

	unknown := httptest.NewRequest(
		http.MethodPost, "/control/v1/register",
		bytes.NewReader([]byte(`{"unknown":true}`)),
	)
	unknown.Header.Set("Authorization", "Bearer "+testControlToken)
	unknown.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, unknown)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown-field status = %d, body = %s", response.Code, response.Body)
	}

	duplicate := httptest.NewRequest(
		http.MethodPost, "/control/v1/register",
		bytes.NewReader([]byte(`{"host_id":"one","host_id":"two"}`)),
	)
	duplicate.Header.Set("Authorization", "Bearer "+testControlToken)
	duplicate.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, duplicate)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("duplicate-field status = %d, body = %s", response.Code, response.Body)
	}
}

func requestJSON(
	t *testing.T,
	handler http.Handler,
	path string,
	value any,
) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
	request.Header.Set("Authorization", "Bearer "+testControlToken)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func readPrincipal() host.Principal {
	return host.Principal{
		ID:            "player.one",
		GrantedScopes: []string{ScopeActorRead},
	}
}
