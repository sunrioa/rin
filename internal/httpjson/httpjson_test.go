package httpjson

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeRequestBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, body, media string
		limit             int64
		valid             bool
	}{
		{"valid", `{"name":"ok"}`, "application/json; charset=utf-8", 13, true},
		{"oversized", `{"name":"ok"} `, "application/json", 13, false},
		{"missing media", `{}`, "", 1024, false},
		{"wrong media", `{}`, "text/plain", 1024, false},
		{"duplicate", `{"name":"a","name":"b"}`, "application/json", 1024, false},
		{"trailing", `{} {}`, "application/json", 1024, false},
		{"unknown", `{"extra":1}`, "application/json", 1024, false},
		{"surrogate", `{"name":"\ud800"}`, "application/json", 1024, false},
		{"invalid utf8", "{\"name\":\"\xff\"}", "application/json", 1024, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
			request.Header.Set("Content-Type", tc.media)
			var target struct {
				Name string `json:"name"`
			}
			err := DecodeRequest(httptest.NewRecorder(), request, tc.limit, &target)
			if (err == nil) != tc.valid {
				t.Fatalf("decode error = %v, valid = %v", err, tc.valid)
			}
		})
	}
}

func TestAuthorizedRequiresBearer(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	for _, header := range []string{"Bearer " + token, token, "", "Basic " + token, "Bearer wrong", "Bearer " + token + " "} {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.Header.Set("Authorization", header)
		if got := Authorized(request, token); got != (header == "Bearer "+token) {
			t.Fatalf("authorization %q = %v", header, got)
		}
	}
}

func TestWriteHandlesEncodingFailureBeforeStatus(t *testing.T) {
	response := httptest.NewRecorder()
	Write(response, http.StatusOK, make(chan int))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", response.Code)
	}
	response = httptest.NewRecorder()
	Write(response, http.StatusCreated, map[string]string{"id": "one"})
	if response.Code != http.StatusCreated || response.Header().Get("Content-Type") != "application/json" || response.Body.String() != "{\"id\":\"one\"}\n" {
		t.Fatalf("response = %#v", response)
	}
}
