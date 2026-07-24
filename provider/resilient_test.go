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

func (c *fakeClient) set(fn func(context.Context, CompletionRequest, int) (CompletionResponse, error)) {
	c.mu.Lock()
	c.fn = fn
	c.mu.Unlock()
}

type manualClock struct {
	mu  sync.Mutex
	now time.Time
}

func newManualClock() *manualClock {
	return &manualClock{now: time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC)}
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) Advance(duration time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(duration)
	c.mu.Unlock()
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

func TestNonRetryableAvailabilityDisposition(t *testing.T) {
	t.Run("local preflight is neutral while closed", func(t *testing.T) {
		client := &fakeClient{fn: func(_ context.Context, _ CompletionRequest, call int) (CompletionResponse, error) {
			switch call {
			case 1, 3:
				return CompletionResponse{}, &Error{Kind: "temporary", Retryable: true}
			case 2:
				return CompletionResponse{}, &Error{Kind: "invalid_schema"}
			default:
				return CompletionResponse{Content: "unexpected"}, nil
			}
		}}
		resilient := newTestResilient(t, client, ResilienceConfig{MaxAttempts: 1, FailureThreshold: 2})

		_, _ = resilient.Complete(context.Background(), CompletionRequest{})
		if _, err := resilient.Complete(context.Background(), CompletionRequest{}); err == nil {
			t.Fatal("expected local preflight error")
		}
		_, _ = resilient.Complete(context.Background(), CompletionRequest{})
		if _, err := resilient.Complete(context.Background(), CompletionRequest{}); !errors.Is(err, ErrCircuitOpen) {
			t.Fatalf("local preflight reset provider failure streak: %v", err)
		}
		if client.callCount() != 3 {
			t.Fatalf("open circuit reached client: calls=%d", client.callCount())
		}
	})

	t.Run("local preflight releases but does not close half-open", func(t *testing.T) {
		clock := newManualClock()
		client := &fakeClient{fn: func(_ context.Context, _ CompletionRequest, call int) (CompletionResponse, error) {
			switch call {
			case 1:
				return CompletionResponse{}, &Error{Kind: "temporary", Retryable: true}
			case 2:
				return CompletionResponse{}, &Error{Kind: "request_encode"}
			case 3:
				return CompletionResponse{}, &Error{Kind: "temporary", Retryable: true}
			default:
				return CompletionResponse{Content: "unexpected"}, nil
			}
		}}
		resilient := newTestResilient(t, client, ResilienceConfig{
			MaxAttempts: 1, FailureThreshold: 1, OpenDuration: time.Hour,
		})
		resilient.now = clock.Now

		_, _ = resilient.Complete(context.Background(), CompletionRequest{})
		clock.Advance(2 * time.Hour)
		if _, err := resilient.Complete(context.Background(), CompletionRequest{}); err == nil {
			t.Fatal("expected half-open local preflight error")
		}
		_, _ = resilient.Complete(context.Background(), CompletionRequest{})
		if _, err := resilient.Complete(context.Background(), CompletionRequest{}); !errors.Is(err, ErrCircuitOpen) {
			t.Fatalf("local preflight closed half-open circuit: %v", err)
		}
		if client.callCount() != 3 {
			t.Fatalf("reopened circuit reached client: calls=%d", client.callCount())
		}
	})

	t.Run("confirmed provider response resets availability failures", func(t *testing.T) {
		client := &fakeClient{fn: func(_ context.Context, _ CompletionRequest, call int) (CompletionResponse, error) {
			switch call {
			case 1, 3:
				return CompletionResponse{}, &Error{Kind: "temporary", Retryable: true}
			case 2:
				return CompletionResponse{}, &Error{
					Kind: "http", StatusCode: 401, ProviderReached: true,
				}
			default:
				return CompletionResponse{Content: "admitted"}, nil
			}
		}}
		resilient := newTestResilient(t, client, ResilienceConfig{MaxAttempts: 1, FailureThreshold: 2})

		_, _ = resilient.Complete(context.Background(), CompletionRequest{})
		if _, err := resilient.Complete(context.Background(), CompletionRequest{}); err == nil {
			t.Fatal("expected non-transient provider response")
		}
		_, _ = resilient.Complete(context.Background(), CompletionRequest{})
		response, err := resilient.Complete(context.Background(), CompletionRequest{})
		if err != nil || response.Content != "admitted" {
			t.Fatalf("confirmed provider response did not reset availability failures: %+v %v", response, err)
		}
	})
}

func TestCircuitHalfOpenAllowsOneProbeAndRecovers(t *testing.T) {
	clock := newManualClock()
	client := &fakeClient{fn: func(context.Context, CompletionRequest, int) (CompletionResponse, error) {
		return CompletionResponse{}, &Error{Kind: "temporary", Retryable: true}
	}}
	resilient := newTestResilient(t, client, ResilienceConfig{MaxAttempts: 1, FailureThreshold: 1, OpenDuration: time.Hour})
	resilient.now = clock.Now
	_, _ = resilient.Complete(context.Background(), CompletionRequest{})
	if _, err := resilient.Complete(context.Background(), CompletionRequest{}); !errors.Is(err, ErrCircuitOpen) || client.callCount() != 1 {
		t.Fatalf("circuit did not open: calls=%d err=%v", client.callCount(), err)
	}

	clock.Advance(2 * time.Hour)
	probeStarted := make(chan struct{})
	releaseProbe := make(chan struct{})
	client.set(func(context.Context, CompletionRequest, int) (CompletionResponse, error) {
		close(probeStarted)
		<-releaseProbe
		return CompletionResponse{Content: "recovered"}, nil
	})
	type completionResult struct {
		response CompletionResponse
		err      error
	}
	probeResult := make(chan completionResult, 1)
	go func() {
		response, err := resilient.Complete(context.Background(), CompletionRequest{})
		probeResult <- completionResult{response: response, err: err}
	}()
	<-probeStarted
	if _, err := resilient.Complete(context.Background(), CompletionRequest{}); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("concurrent half-open call should be rejected: %v", err)
	}
	close(releaseProbe)
	result := <-probeResult
	if result.err != nil || result.response.Content != "recovered" {
		t.Fatalf("half-open call did not recover: %+v %v", result.response, result.err)
	}
	client.set(func(context.Context, CompletionRequest, int) (CompletionResponse, error) {
		return CompletionResponse{Content: "recovered"}, nil
	})
	if response, err := resilient.Complete(context.Background(), CompletionRequest{}); err != nil || response.Content != "recovered" {
		t.Fatalf("closed circuit did not admit call: %+v %v", response, err)
	}
}

func TestStaleClosedCallCannotReviveHalfOpenCircuit(t *testing.T) {
	clock := newManualClock()
	staleStarted := make(chan struct{})
	releaseStale := make(chan struct{})
	probeStarted := make(chan struct{})
	releaseProbe := make(chan struct{})
	client := &fakeClient{fn: func(_ context.Context, _ CompletionRequest, call int) (CompletionResponse, error) {
		switch call {
		case 1:
			close(staleStarted)
			<-releaseStale
			return CompletionResponse{Content: "stale success"}, nil
		case 2:
			return CompletionResponse{}, &Error{Kind: "temporary", Retryable: true}
		case 3:
			close(probeStarted)
			<-releaseProbe
			return CompletionResponse{Content: "probe success"}, nil
		default:
			return CompletionResponse{Content: "closed"}, nil
		}
	}}
	resilient := newTestResilient(t, client, ResilienceConfig{MaxAttempts: 1, FailureThreshold: 1, OpenDuration: time.Hour})
	resilient.now = clock.Now

	staleResult := make(chan error, 1)
	go func() {
		_, err := resilient.Complete(context.Background(), CompletionRequest{})
		staleResult <- err
	}()
	<-staleStarted
	if _, err := resilient.Complete(context.Background(), CompletionRequest{}); err == nil {
		t.Fatal("expected concurrent call to open circuit")
	}
	clock.Advance(2 * time.Hour)

	probeResult := make(chan error, 1)
	go func() {
		_, err := resilient.Complete(context.Background(), CompletionRequest{})
		probeResult <- err
	}()
	<-probeStarted
	close(releaseStale)
	if err := <-staleResult; err != nil {
		t.Fatalf("stale admitted call should retain its own result: %v", err)
	}
	if _, err := resilient.Complete(context.Background(), CompletionRequest{}); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("stale success released active half-open probe: %v", err)
	}
	close(releaseProbe)
	if err := <-probeResult; err != nil {
		t.Fatalf("half-open probe failed: %v", err)
	}
}

func TestCanceledClosedCallCannotReleaseHalfOpenProbe(t *testing.T) {
	clock := newManualClock()
	legacyStarted := make(chan struct{})
	probeStarted := make(chan struct{})
	releaseProbe := make(chan struct{})
	client := &fakeClient{fn: func(ctx context.Context, _ CompletionRequest, call int) (CompletionResponse, error) {
		switch call {
		case 1:
			close(legacyStarted)
			<-ctx.Done()
			return CompletionResponse{}, ctx.Err()
		case 2:
			return CompletionResponse{}, &Error{Kind: "temporary", Retryable: true}
		case 3:
			close(probeStarted)
			<-releaseProbe
			return CompletionResponse{Content: "probe success"}, nil
		default:
			return CompletionResponse{Content: "closed"}, nil
		}
	}}
	resilient := newTestResilient(t, client, ResilienceConfig{MaxAttempts: 1, FailureThreshold: 1, OpenDuration: time.Hour})
	resilient.now = clock.Now

	legacyContext, cancelLegacy := context.WithCancel(context.Background())
	legacyResult := make(chan error, 1)
	go func() {
		_, err := resilient.Complete(legacyContext, CompletionRequest{})
		legacyResult <- err
	}()
	<-legacyStarted
	if _, err := resilient.Complete(context.Background(), CompletionRequest{}); err == nil {
		t.Fatal("expected concurrent call to open circuit")
	}
	clock.Advance(2 * time.Hour)

	probeResult := make(chan error, 1)
	go func() {
		_, err := resilient.Complete(context.Background(), CompletionRequest{})
		probeResult <- err
	}()
	<-probeStarted
	cancelLegacy()
	if err := <-legacyResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected legacy caller cancellation, got %v", err)
	}
	if _, err := resilient.Complete(context.Background(), CompletionRequest{}); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("legacy cancellation released active half-open probe: %v", err)
	}
	close(releaseProbe)
	if err := <-probeResult; err != nil {
		t.Fatalf("half-open probe failed: %v", err)
	}
}

func TestConsecutiveTotalTimeoutsOpenCircuit(t *testing.T) {
	clock := newManualClock()
	client := &fakeClient{fn: func(ctx context.Context, _ CompletionRequest, _ int) (CompletionResponse, error) {
		<-ctx.Done()
		return CompletionResponse{}, &Error{Kind: "late_non_retryable"}
	}}
	resilient := newTestResilient(t, client, ResilienceConfig{
		MaxAttempts:      1,
		AttemptTimeout:   10 * time.Millisecond,
		TotalTimeout:     10 * time.Millisecond,
		FailureThreshold: 2,
		OpenDuration:     time.Hour,
	})
	resilient.now = clock.Now

	for call := 0; call < 2; call++ {
		if _, err := resilient.Complete(context.Background(), CompletionRequest{}); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("call %d: expected total deadline, got %v", call+1, err)
		}
	}
	if _, err := resilient.Complete(context.Background(), CompletionRequest{}); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("consecutive total timeouts did not open circuit: %v", err)
	}
	if client.callCount() != 2 {
		t.Fatalf("open circuit reached client: calls=%d", client.callCount())
	}
}

func TestClientContextContractBoundsCooperativeTotalTimeout(t *testing.T) {
	clientReturned := make(chan struct{})
	client := &fakeClient{fn: func(ctx context.Context, _ CompletionRequest, _ int) (CompletionResponse, error) {
		<-ctx.Done()
		close(clientReturned)
		return CompletionResponse{}, ctx.Err()
	}}
	resilient := newTestResilient(t, client, ResilienceConfig{
		MaxAttempts:    1,
		AttemptTimeout: 10 * time.Millisecond,
		TotalTimeout:   10 * time.Millisecond,
	})

	started := time.Now()
	if _, err := resilient.Complete(context.Background(), CompletionRequest{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected cooperative total timeout, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("context-compliant client returned too slowly: %s", elapsed)
	}
	select {
	case <-clientReturned:
	default:
		t.Fatal("Complete returned before context-compliant client stopped")
	}
}

func TestLateSuccessAfterTotalTimeoutDoesNotReviveCircuit(t *testing.T) {
	client := &fakeClient{fn: func(ctx context.Context, _ CompletionRequest, _ int) (CompletionResponse, error) {
		<-ctx.Done()
		return CompletionResponse{Content: "too late"}, nil
	}}
	resilient := newTestResilient(t, client, ResilienceConfig{
		MaxAttempts:      1,
		AttemptTimeout:   10 * time.Millisecond,
		TotalTimeout:     10 * time.Millisecond,
		FailureThreshold: 1,
		OpenDuration:     time.Hour,
	})

	if response, err := resilient.Complete(context.Background(), CompletionRequest{}); !errors.Is(err, context.DeadlineExceeded) || response.Content != "" {
		t.Fatalf("late success escaped total budget: response=%+v err=%v", response, err)
	}
	if _, err := resilient.Complete(context.Background(), CompletionRequest{}); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("late success revived circuit: %v", err)
	}
}

func TestCallerCancellationDoesNotCountAsProviderFailure(t *testing.T) {
	started := make(chan struct{})
	client := &fakeClient{fn: func(ctx context.Context, _ CompletionRequest, _ int) (CompletionResponse, error) {
		close(started)
		<-ctx.Done()
		return CompletionResponse{}, ctx.Err()
	}}
	resilient := newTestResilient(t, client, ResilienceConfig{MaxAttempts: 1, FailureThreshold: 1})
	callContext, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := resilient.Complete(callContext, CompletionRequest{})
		result <- err
	}()
	<-started
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected caller cancellation, got %v", err)
	}

	client.set(func(context.Context, CompletionRequest, int) (CompletionResponse, error) {
		return CompletionResponse{Content: "still admitted"}, nil
	})
	response, err := resilient.Complete(context.Background(), CompletionRequest{})
	if err != nil || response.Content != "still admitted" {
		t.Fatalf("caller cancellation opened circuit: response=%+v err=%v", response, err)
	}
}

func TestCallerCancellationReleasesHalfOpenProbe(t *testing.T) {
	clock := newManualClock()
	client := &fakeClient{fn: func(context.Context, CompletionRequest, int) (CompletionResponse, error) {
		return CompletionResponse{}, &Error{Kind: "temporary", Retryable: true}
	}}
	resilient := newTestResilient(t, client, ResilienceConfig{MaxAttempts: 1, FailureThreshold: 1, OpenDuration: time.Hour})
	resilient.now = clock.Now
	_, _ = resilient.Complete(context.Background(), CompletionRequest{})
	clock.Advance(2 * time.Hour)

	probeStarted := make(chan struct{})
	client.set(func(ctx context.Context, _ CompletionRequest, _ int) (CompletionResponse, error) {
		close(probeStarted)
		<-ctx.Done()
		return CompletionResponse{}, ctx.Err()
	})
	probeContext, cancelProbe := context.WithCancel(context.Background())
	probeResult := make(chan error, 1)
	go func() {
		_, err := resilient.Complete(probeContext, CompletionRequest{})
		probeResult <- err
	}()
	<-probeStarted
	cancelProbe()
	if err := <-probeResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled half-open probe, got %v", err)
	}

	client.set(func(context.Context, CompletionRequest, int) (CompletionResponse, error) {
		return CompletionResponse{Content: "recovered"}, nil
	})
	result, err := resilient.Complete(context.Background(), CompletionRequest{})
	if err != nil || result.Content != "recovered" {
		t.Fatalf("canceled probe left half-open slot occupied: %+v %v", result, err)
	}
}

func TestNonRetryableAndAttemptTimeout(t *testing.T) {
	client := &fakeClient{fn: func(context.Context, CompletionRequest, int) (CompletionResponse, error) {
		return CompletionResponse{}, &Error{Kind: "auth", StatusCode: 401, ProviderReached: true}
	}}
	resilient := newTestResilient(t, client, ResilienceConfig{MaxAttempts: 3})
	if _, err := resilient.Complete(context.Background(), CompletionRequest{}); err == nil || client.callCount() != 1 {
		t.Fatalf("non-retryable error retried: calls=%d err=%v", client.callCount(), err)
	}

	blocking := &fakeClient{fn: func(ctx context.Context, _ CompletionRequest, _ int) (CompletionResponse, error) {
		<-ctx.Done()
		return CompletionResponse{Content: "late attempt"}, nil
	}}
	resilient = newTestResilient(t, blocking, ResilienceConfig{
		MaxAttempts:      2,
		AttemptTimeout:   5 * time.Millisecond,
		TotalTimeout:     100 * time.Millisecond,
		FailureThreshold: 1,
	})
	if response, err := resilient.Complete(context.Background(), CompletionRequest{}); !errors.Is(err, context.DeadlineExceeded) || response.Content != "" {
		t.Fatalf("expected attempt timeout, got response=%+v err=%v", response, err)
	}
	if blocking.callCount() != 2 {
		t.Fatalf("attempt timeout should retry within total budget: calls=%d", blocking.callCount())
	}
	if _, err := resilient.Complete(context.Background(), CompletionRequest{}); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("exhausted attempt timeouts should count as one failed call: %v", err)
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
