package provider

import (
	"context"
	"errors"
	"sync"
	"time"
)

var ErrCircuitOpen = errors.New("provider circuit is open")

type ResilienceConfig struct {
	MaxAttempts      int
	AttemptTimeout   time.Duration
	TotalTimeout     time.Duration
	InitialBackoff   time.Duration
	MaxBackoff       time.Duration
	FailureThreshold int
	OpenDuration     time.Duration
}

type Resilient struct {
	client Client
	config ResilienceConfig

	mu                  sync.Mutex
	consecutiveFailures int
	openUntil           time.Time
	halfOpen            bool
	now                 func() time.Time
}

func NewResilient(client Client, config ResilienceConfig) (*Resilient, error) {
	if client == nil {
		return nil, errors.New("provider client is required")
	}
	if config.MaxAttempts <= 0 {
		config.MaxAttempts = 2
	}
	if config.MaxAttempts > 5 {
		return nil, errors.New("max attempts must not exceed 5")
	}
	if config.AttemptTimeout <= 0 {
		config.AttemptTimeout = 15 * time.Second
	}
	if config.TotalTimeout <= 0 {
		config.TotalTimeout = 25 * time.Second
	}
	if config.TotalTimeout < config.AttemptTimeout {
		return nil, errors.New("total timeout must be at least the attempt timeout")
	}
	if config.InitialBackoff <= 0 {
		config.InitialBackoff = 150 * time.Millisecond
	}
	if config.MaxBackoff <= 0 {
		config.MaxBackoff = 2 * time.Second
	}
	if config.MaxBackoff < config.InitialBackoff {
		return nil, errors.New("max backoff must be at least initial backoff")
	}
	if config.FailureThreshold <= 0 {
		config.FailureThreshold = 3
	}
	if config.OpenDuration <= 0 {
		config.OpenDuration = 20 * time.Second
	}
	return &Resilient{client: client, config: config, now: time.Now}, nil
}

func (r *Resilient) Complete(ctx context.Context, request CompletionRequest) (CompletionResponse, error) {
	if err := r.beforeCall(); err != nil {
		return CompletionResponse{}, err
	}
	callContext, cancelCall := context.WithTimeout(ctx, r.config.TotalTimeout)
	defer cancelCall()

	var lastError error
	for attempt := 0; attempt < r.config.MaxAttempts; attempt++ {
		attemptContext, cancelAttempt := context.WithTimeout(callContext, r.config.AttemptTimeout)
		response, err := r.client.Complete(attemptContext, request)
		cancelAttempt()
		if err == nil {
			r.recordSuccess()
			return response, nil
		}
		lastError = err
		if ctx.Err() != nil {
			r.releaseHalfOpen()
			return CompletionResponse{}, ctx.Err()
		}
		if !retryable(err, callContext) {
			r.recordSuccess()
			return CompletionResponse{}, err
		}
		if attempt+1 >= r.config.MaxAttempts {
			break
		}
		delay := RetryDelay(err)
		if delay <= 0 {
			delay = r.config.InitialBackoff << attempt
		}
		if delay > r.config.MaxBackoff {
			delay = r.config.MaxBackoff
		}
		if err := sleepContext(callContext, delay); err != nil {
			lastError = err
			break
		}
	}
	r.recordFailure()
	if callContext.Err() != nil && ctx.Err() == nil {
		return CompletionResponse{}, context.DeadlineExceeded
	}
	return CompletionResponse{}, lastError
}

func retryable(err error, callContext context.Context) bool {
	if IsRetryable(err) {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded) && callContext.Err() == nil
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Resilient) beforeCall() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	if !r.openUntil.IsZero() && now.Before(r.openUntil) {
		return ErrCircuitOpen
	}
	if !r.openUntil.IsZero() {
		if r.halfOpen {
			return ErrCircuitOpen
		}
		r.halfOpen = true
	}
	return nil
}

func (r *Resilient) recordSuccess() {
	r.mu.Lock()
	r.consecutiveFailures = 0
	r.openUntil = time.Time{}
	r.halfOpen = false
	r.mu.Unlock()
}

func (r *Resilient) recordFailure() {
	r.mu.Lock()
	r.consecutiveFailures++
	r.halfOpen = false
	if r.consecutiveFailures >= r.config.FailureThreshold {
		r.openUntil = r.now().Add(r.config.OpenDuration)
	}
	r.mu.Unlock()
}

func (r *Resilient) releaseHalfOpen() {
	r.mu.Lock()
	r.halfOpen = false
	r.mu.Unlock()
}
