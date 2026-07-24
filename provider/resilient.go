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
	circuitGeneration   uint64
	now                 func() time.Time
}

type circuitPermit struct {
	generation uint64
	halfOpen   bool
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
	if err := ctx.Err(); err != nil {
		return CompletionResponse{}, err
	}
	permit, err := r.beforeCall()
	if err != nil {
		return CompletionResponse{}, err
	}
	if err := ctx.Err(); err != nil {
		r.releaseHalfOpen(permit)
		return CompletionResponse{}, err
	}
	callContext, cancelCall := context.WithTimeout(ctx, r.config.TotalTimeout)
	defer cancelCall()

	var lastError error
	for attempt := 0; attempt < r.config.MaxAttempts; attempt++ {
		if callerError := ctx.Err(); callerError != nil {
			r.releaseHalfOpen(permit)
			return CompletionResponse{}, callerError
		}
		if callContext.Err() != nil {
			r.recordFailure(permit)
			return CompletionResponse{}, context.DeadlineExceeded
		}
		attemptContext, cancelAttempt := context.WithTimeout(callContext, r.config.AttemptTimeout)
		response, err := r.client.Complete(attemptContext, request)
		attemptContextError := attemptContext.Err()
		cancelAttempt()

		if callerError := ctx.Err(); callerError != nil {
			r.releaseHalfOpen(permit)
			return CompletionResponse{}, callerError
		}
		if callContext.Err() != nil {
			r.recordFailure(permit)
			return CompletionResponse{}, context.DeadlineExceeded
		}
		if attemptContextError != nil {
			// The caller and total budget are still live, so this is the
			// per-attempt deadline. A client result returned after that
			// deadline, including a success, is not an in-budget result.
			err = context.DeadlineExceeded
		} else if err == nil {
			r.recordSuccess(permit)
			return response, nil
		}
		lastError = err
		if !retryable(err) {
			if confirmsProviderAvailability(err) {
				r.recordSuccess(permit)
			} else {
				r.recordNeutral(permit)
			}
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
			if callerError := ctx.Err(); callerError != nil {
				r.releaseHalfOpen(permit)
				return CompletionResponse{}, callerError
			}
			r.recordFailure(permit)
			return CompletionResponse{}, context.DeadlineExceeded
		}
	}
	r.recordFailure(permit)
	return CompletionResponse{}, lastError
}

func retryable(err error) bool {
	if IsRetryable(err) {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded)
}

func confirmsProviderAvailability(err error) bool {
	var providerError *Error
	return errors.As(err, &providerError) && providerError.ProviderReached
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

func (r *Resilient) beforeCall() (circuitPermit, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	if !r.openUntil.IsZero() && now.Before(r.openUntil) {
		return circuitPermit{}, ErrCircuitOpen
	}
	if !r.openUntil.IsZero() {
		if r.halfOpen {
			return circuitPermit{}, ErrCircuitOpen
		}
		r.halfOpen = true
		return circuitPermit{generation: r.circuitGeneration, halfOpen: true}, nil
	}
	return circuitPermit{generation: r.circuitGeneration}, nil
}

func (r *Resilient) recordSuccess(permit circuitPermit) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if permit.generation != r.circuitGeneration {
		return
	}
	if permit.halfOpen {
		if !r.halfOpen {
			return
		}
		r.circuitGeneration++
	} else if !r.openUntil.IsZero() || r.halfOpen {
		return
	}
	r.consecutiveFailures = 0
	r.openUntil = time.Time{}
	r.halfOpen = false
}

func (r *Resilient) recordFailure(permit circuitPermit) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if permit.generation != r.circuitGeneration {
		return
	}
	if permit.halfOpen {
		if !r.halfOpen {
			return
		}
		r.consecutiveFailures++
		r.halfOpen = false
		r.openUntil = r.now().Add(r.config.OpenDuration)
		r.circuitGeneration++
		return
	}
	if !r.openUntil.IsZero() || r.halfOpen {
		return
	}
	r.consecutiveFailures++
	if r.consecutiveFailures >= r.config.FailureThreshold {
		r.openUntil = r.now().Add(r.config.OpenDuration)
		r.circuitGeneration++
	}
}

func (r *Resilient) releaseHalfOpen(permit circuitPermit) {
	r.mu.Lock()
	if permit.halfOpen && permit.generation == r.circuitGeneration {
		r.halfOpen = false
	}
	r.mu.Unlock()
}

func (r *Resilient) recordNeutral(permit circuitPermit) {
	r.releaseHalfOpen(permit)
}
