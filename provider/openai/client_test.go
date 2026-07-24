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
	"sync/atomic"
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
		if request.Header.Get("User-Agent") != "rin/0.4" {
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
	var providerError *provider.Error
	if !errors.As(err, &providerError) || !providerError.ProviderReached {
		t.Fatalf("HTTP response must confirm provider availability: %#v", err)
	}
	for _, forbidden := range []string{"fixture-secret", "secret prompt", "fixture detail"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("error leaked %q: %v", forbidden, err)
		}
	}
}

func TestResponseLimitDoesNotRetryAndTransportFailure(t *testing.T) {
	large := bytes.Repeat([]byte("x"), 32)
	var oversizedCalls atomic.Int32
	client, _ := New(Config{
		BaseURL: "http://127.0.0.1:1/v1", Model: "local", MaxResponseBytes: 8,
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			oversizedCalls.Add(1)
			return response(http.StatusOK, large, nil), nil
		})},
	})
	resilient, err := provider.NewResilient(client, provider.ResilienceConfig{
		MaxAttempts:      3,
		AttemptTimeout:   100 * time.Millisecond,
		TotalTimeout:     time.Second,
		InitialBackoff:   time.Millisecond,
		MaxBackoff:       time.Millisecond,
		FailureThreshold: 3,
		OpenDuration:     time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = resilient.Complete(context.Background(), provider.CompletionRequest{})
	if err == nil || provider.IsRetryable(err) {
		t.Fatalf("oversized response should be non-retryable: %v", err)
	}
	var providerError *provider.Error
	if !errors.As(err, &providerError) || !providerError.ProviderReached {
		t.Fatalf("oversized provider response must confirm availability: %#v", err)
	}
	if oversizedCalls.Load() != 1 {
		t.Fatalf("oversized response used %d attempts, want 1", oversizedCalls.Load())
	}
	client, _ = New(Config{
		BaseURL: "http://127.0.0.1:1/v1", Model: "local",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("dial failed with fixture-secret")
		})},
	})
	_, err = client.Complete(context.Background(), provider.CompletionRequest{})
	if !provider.IsRetryable(err) || strings.Contains(err.Error(), "fixture-secret") {
		t.Fatalf("unsafe transport error: %v", err)
	}
}

func TestPreflightErrorDoesNotClaimProviderAvailability(t *testing.T) {
	var transportCalls atomic.Int32
	client, err := New(Config{
		BaseURL: "https://models.example/v1",
		Model:   "fixture",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			transportCalls.Add(1)
			return response(http.StatusOK, nil, nil), nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Complete(context.Background(), provider.CompletionRequest{
		Schema: &provider.ResponseSchema{Name: "invalid", Schema: json.RawMessage(`{`)},
	})
	var providerError *provider.Error
	if !errors.As(err, &providerError) || providerError.Kind != "invalid_schema" {
		t.Fatalf("expected invalid_schema, got %#v", err)
	}
	if providerError.ProviderReached {
		t.Fatal("local schema validation claimed provider availability")
	}
	if transportCalls.Load() != 0 {
		t.Fatalf("preflight error reached transport: calls=%d", transportCalls.Load())
	}
}

func TestCompleteHonorsClientContextContract(t *testing.T) {
	transportStarted := make(chan struct{})
	transportReturned := make(chan struct{})
	client, err := New(Config{
		BaseURL: "https://models.example/v1",
		Model:   "fixture",
		HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			close(transportStarted)
			<-request.Context().Done()
			close(transportReturned)
			return nil, request.Context().Err()
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	callContext, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := client.Complete(callContext, provider.CompletionRequest{})
		result <- err
	}()
	<-transportStarted
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected caller cancellation, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("OpenAI client did not return promptly after ctx.Done")
	}
	select {
	case <-transportReturned:
	default:
		t.Fatal("OpenAI transport outlived Complete")
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
