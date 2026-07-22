package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sunrioa/rin/provider"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestCompleteSendsSchemaAndDecodesFixture(t *testing.T) {
	fixture := readFixture(t, "chat_success.json")
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://models.example/v1/chat/completions" {
			t.Fatalf("unexpected URL: %s", request.URL)
		}
		if request.Header.Get("Authorization") != "Bearer fixture-secret" {
			t.Fatalf("missing authorization header")
		}
		if request.Header.Get("User-Agent") != "rin/0.3" {
			t.Fatalf("unexpected user agent: %s", request.Header.Get("User-Agent"))
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		format := body["response_format"].(map[string]any)
		if format["type"] != "json_schema" {
			t.Fatalf("unexpected response format: %+v", format)
		}
		return response(http.StatusOK, fixture, nil), nil
	})
	client, err := New(Config{
		BaseURL: "https://models.example/v1", APIKey: "fixture-secret", Model: "fixture-model",
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Complete(context.Background(), provider.CompletionRequest{
		Messages:    []provider.Message{{Role: "system", Content: "system"}, {Role: "user", Content: "data"}},
		Schema:      &provider.ResponseSchema{Name: "proposal", Strict: true, Schema: json.RawMessage(`{"type":"object"}`)},
		Temperature: 0.2, MaxTokens: 700,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Model != "fixture-model" || result.FinishReason != "stop" || result.Usage.TotalTokens != 168 || !strings.Contains(result.Content, `"action_id":"talk"`) {
		t.Fatalf("unexpected completion: %+v", result)
	}
}

func TestHTTPErrorIsRetryableWithoutLeakingBodyOrKey(t *testing.T) {
	fixture := readFixture(t, "error_rate_limit.json")
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusTooManyRequests, fixture, http.Header{"Retry-After": []string{"2"}}), nil
	})
	client, err := New(Config{BaseURL: "https://models.example/v1", APIKey: "fixture-secret", Model: "fixture", HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Complete(context.Background(), provider.CompletionRequest{Messages: []provider.Message{{Role: "user", Content: "secret prompt"}}})
	if !provider.IsRetryable(err) || provider.RetryDelay(err) != 2*time.Second {
		t.Fatalf("unexpected provider error: %v", err)
	}
	for _, forbidden := range []string{"fixture-secret", "secret prompt", "fixture detail"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("error leaked %q: %v", forbidden, err)
		}
	}
}

func TestResponseLimitAndTransportFailure(t *testing.T) {
	large := bytes.Repeat([]byte("x"), 32)
	client, _ := New(Config{
		BaseURL: "http://127.0.0.1:1/v1", Model: "local", MaxResponseBytes: 8,
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return response(http.StatusOK, large, nil), nil
		})},
	})
	if _, err := client.Complete(context.Background(), provider.CompletionRequest{}); !provider.IsRetryable(err) {
		t.Fatalf("oversized response should be retryable: %v", err)
	}
	client, _ = New(Config{
		BaseURL: "http://127.0.0.1:1/v1", Model: "local",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("dial failed with fixture-secret")
		})},
	})
	_, err := client.Complete(context.Background(), provider.CompletionRequest{})
	if !provider.IsRetryable(err) || strings.Contains(err.Error(), "fixture-secret") {
		t.Fatalf("unsafe transport error: %v", err)
	}
}

func TestConfigValidationAndRetryAfterDate(t *testing.T) {
	invalid := []Config{
		{BaseURL: "file:///tmp/model", Model: "x"},
		{BaseURL: "https://user@example.com/v1", Model: "x"},
		{BaseURL: "https://example.com/v1"},
		{BaseURL: "https://example.com/v1", Model: "x", ResponseFormat: "yaml"},
		{BaseURL: "https://example.com/v1?target=other", Model: "x"},
	}
	client, err := New(Config{BaseURL: "https://example.com/v1", Model: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.httpClient.CheckRedirect(nil, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("redirects must be disabled: %v", err)
	}
	for _, config := range invalid {
		if _, err := New(config); err == nil {
			t.Fatalf("config should fail: %+v", config)
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	delay := parseRetryAfter(now.Add(3*time.Second).Format(http.TimeFormat), now)
	if delay != 3*time.Second {
		t.Fatalf("unexpected date delay: %s", delay)
	}
}

func response(status int, body []byte, headers http.Header) *http.Response {
	if headers == nil {
		headers = make(http.Header)
	}
	return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(bytes.NewReader(body))}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	payload, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}
