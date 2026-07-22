package policy_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/sunrioa/rin/policy"
	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
)

type countingPolicy struct {
	mu      sync.Mutex
	calls   int
	started chan struct{}
	release chan struct{}
	err     error
}

func (p *countingPolicy) Propose(ctx context.Context, _ rinruntime.PolicyContext) (rinruntime.ProposalDraft, error) {
	p.mu.Lock()
	p.calls++
	call := p.calls
	p.mu.Unlock()
	if call == 1 && p.started != nil {
		close(p.started)
	}
	if p.release != nil {
		select {
		case <-p.release:
		case <-ctx.Done():
			return rinruntime.ProposalDraft{}, ctx.Err()
		}
	}
	if p.err != nil {
		return rinruntime.ProposalDraft{}, p.err
	}
	return rinruntime.ProposalDraft{ActionID: "talk", Stance: "engage", Summary: "summary", Rationale: "rationale", PolicySource: "model"}, nil
}

func (p *countingPolicy) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func TestCachedPolicyReusesSemanticRequest(t *testing.T) {
	underlying := &countingPolicy{}
	cached, err := policy.NewCached(underlying, policy.CacheConfig{MaxEntries: 4, TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	input := modelInput()
	first, err := cached.Propose(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	input.Request.RequestID = "request.retry"
	second, err := cached.Propose(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if underlying.count() != 1 || first.PolicySource != "model" || second.PolicySource != "model-cache" {
		t.Fatalf("cache miss: calls=%d first=%+v second=%+v", underlying.count(), first, second)
	}
}

func TestCachedPolicySeparatesCandidateGoalContracts(t *testing.T) {
	underlying := &countingPolicy{}
	cached, _ := policy.NewCached(underlying, policy.CacheConfig{MaxEntries: 4, TTL: time.Minute})
	input := modelInput()
	input.State.WorldRevision = 3
	input.Request.CandidateGoals = []protocol.Goal{{
		ID: "goal.first", Description: "First candidate.", Priority: 3, TargetProgress: 2, Status: "active",
	}}
	if _, err := cached.Propose(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	input.Request.RequestID = "request.changed-goal"
	input.Request.CandidateGoals[0].ID = "goal.second"
	if _, err := cached.Propose(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if underlying.count() != 2 {
		t.Fatalf("different candidate goal contracts shared a cache entry: %d", underlying.count())
	}
}

func TestCachedPolicyCollapsesConcurrentCalls(t *testing.T) {
	underlying := &countingPolicy{started: make(chan struct{}), release: make(chan struct{})}
	cached, _ := policy.NewCached(underlying, policy.CacheConfig{MaxEntries: 8, TTL: time.Minute})
	const callers = 8
	errorsChannel := make(chan error, callers)
	for index := 0; index < callers; index++ {
		go func(index int) {
			input := modelInput()
			input.Request.RequestID = "concurrent." + string(rune('a'+index))
			_, err := cached.Propose(context.Background(), input)
			errorsChannel <- err
		}(index)
	}
	select {
	case <-underlying.started:
	case <-time.After(time.Second):
		t.Fatal("underlying policy did not start")
	}
	close(underlying.release)
	for index := 0; index < callers; index++ {
		if err := <-errorsChannel; err != nil {
			t.Fatal(err)
		}
	}
	if underlying.count() != 1 {
		t.Fatalf("expected one underlying call, got %d", underlying.count())
	}
}

func TestCachedPolicyDoesNotCacheFailures(t *testing.T) {
	underlying := &countingPolicy{err: errors.New("failed")}
	cached, _ := policy.NewCached(underlying, policy.CacheConfig{})
	for index := 0; index < 2; index++ {
		if _, err := cached.Propose(context.Background(), modelInput()); err == nil {
			t.Fatal("expected policy failure")
		}
	}
	if underlying.count() != 2 {
		t.Fatalf("failures were cached: %d", underlying.count())
	}
}
