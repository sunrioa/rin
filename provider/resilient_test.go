package provider

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeClient struct {
	mu    sync.Mutex
	calls int
	fn    func(context.Context, CompletionRequest, int) (CompletionResponse, error)
}

func (c *fakeClient) Complete(ctx context.Context, request CompletionRequest) (CompletionResponse, error) {
	c.mu.Lock()
	c.calls++
	call := c.calls
	fn := c.fn
	c.mu.Unlock()
	return fn(ctx, request, call)
}

func (c *fakeClient) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func TestResilientRetriesThenSucceeds(t *testing.T) {
	client := &fakeClient{fn: func(_ context.Context, _ CompletionRequest, call int) (CompletionResponse, error) {
		if call < 3 {
			return CompletionResponse{}, &Error{Kind: "temporary", Retryable: true}
		}
		return CompletionResponse{Content: "ok"}, nil
	}}
	resilient := newTestResilient(t, client, ResilienceConfig{MaxAttempts: 3, FailureThreshold: 2})
	result, err := resilient.Complete(context.Background(), CompletionRequest{})
	if err != nil || result.Content != "ok" || client.callCount() != 3 {
		t.Fatalf("result=%+v calls=%d err=%v", result, client.callCount(), err)
	}
}

func TestCircuitOpensAndRecoversHalfOpen(t *testing.T) {
	now := time.Now()
	client := &fakeClient{fn: func(context.Context, CompletionRequest, int) (CompletionResponse, error) {
		return CompletionResponse{}, &Error{Kind: "temporary", Retryable: true}
	}}
	resilient := newTestResilient(t, client, ResilienceConfig{MaxAttempts: 1, FailureThreshold: 2, OpenDuration: time.Hour})
	resilient.now = func() time.Time { return now }
	_, _ = resilient.Complete(context.Background(), CompletionRequest{})
	_, _ = resilient.Complete(context.Background(), CompletionRequest{})
	if _, err := resilient.Complete(context.Background(), CompletionRequest{}); !errors.Is(err, ErrCircuitOpen) || client.callCount() != 2 {
		t.Fatalf("circuit did not open: calls=%d err=%v", client.callCount(), err)
	}
	now = now.Add(2 * time.Hour)
	client.mu.Lock()
	client.fn = func(context.Context, CompletionRequest, int) (CompletionResponse, error) {
		return CompletionResponse{Content: "recovered"}, nil
	}
	client.mu.Unlock()
	result, err := resilient.Complete(context.Background(), CompletionRequest{})
	if err != nil || result.Content != "recovered" {
		t.Fatalf("half-open call did not recover: %+v %v", result, err)
	}
}

func TestNonRetryableAndAttemptTimeout(t *testing.T) {
	client := &fakeClient{fn: func(context.Context, CompletionRequest, int) (CompletionResponse, error) {
		return CompletionResponse{}, &Error{Kind: "auth", StatusCode: 401}
	}}
	resilient := newTestResilient(t, client, ResilienceConfig{MaxAttempts: 3})
	if _, err := resilient.Complete(context.Background(), CompletionRequest{}); err == nil || client.callCount() != 1 {
		t.Fatalf("non-retryable error retried: calls=%d err=%v", client.callCount(), err)
	}

	blocking := &fakeClient{fn: func(ctx context.Context, _ CompletionRequest, _ int) (CompletionResponse, error) {
		<-ctx.Done()
		return CompletionResponse{}, ctx.Err()
	}}
	resilient = newTestResilient(t, blocking, ResilienceConfig{MaxAttempts: 1, AttemptTimeout: 5 * time.Millisecond, TotalTimeout: 10 * time.Millisecond})
	if _, err := resilient.Complete(context.Background(), CompletionRequest{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected timeout, got %v", err)
	}
}

func newTestResilient(t *testing.T, client Client, config ResilienceConfig) *Resilient {
	t.Helper()
	if config.AttemptTimeout == 0 {
		config.AttemptTimeout = 100 * time.Millisecond
	}
	if config.TotalTimeout == 0 {
		config.TotalTimeout = time.Second
	}
	if config.InitialBackoff == 0 {
		config.InitialBackoff = time.Millisecond
	}
	if config.MaxBackoff == 0 {
		config.MaxBackoff = time.Millisecond
	}
	resilient, err := NewResilient(client, config)
	if err != nil {
		t.Fatal(err)
	}
	return resilient
}
