package policy_test

import (
	"context"
	"encoding/json"
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

func (p *countingPolicy) Propose(ctx context.Context, _ rinruntime.DecisionContext) (rinruntime.DecisionDraft, error) {
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
			return rinruntime.DecisionDraft{}, ctx.Err()
		}
	}
	if p.err != nil {
		return rinruntime.DecisionDraft{}, p.err
	}
	return rinruntime.DecisionDraft{OfferID: "talk", Stance: "engage", PolicySource: "model"}, nil
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

func TestPoliciesRejectNilContextBeforeDependencies(t *testing.T) {
	underlying := &countingPolicy{}
	modelClient := &completionClient{}
	cached, err := policy.NewCached(
		underlying,
		policy.CacheConfig{MaxEntries: 4, TTL: time.Minute},
	)
	if err != nil {
		t.Fatal(err)
	}
	policies := []struct {
		name   string
		policy rinruntime.DecisionProvider
	}{
		{name: "deterministic", policy: policy.Deterministic{}},
		{name: "cached", policy: cached},
		{
			name: "failover",
			policy: policy.Failover{
				Primary: underlying, Fallback: underlying,
			},
		},
		{
			name:   "model",
			policy: policy.Model{GenerationProvider: modelClient},
		},
	}
	for _, test := range policies {
		t.Run(test.name, func(t *testing.T) {
			if _, err := test.policy.Propose(nil, modelInput()); err == nil {
				t.Fatal("Propose accepted a nil context")
			}
		})
	}
	if underlying.count() != 0 {
		t.Fatalf("nil context reached policy dependency %d times", underlying.count())
	}
	if modelClient.callCount() != 0 {
		t.Fatalf("nil context reached model provider %d times", modelClient.callCount())
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

func TestCachedPolicySeparatesLineageHeadAndActorState(t *testing.T) {
	underlying := &countingPolicy{}
	cached, err := policy.NewCached(
		underlying,
		policy.CacheConfig{MaxEntries: 8, TTL: time.Minute},
	)
	if err != nil {
		t.Fatal(err)
	}
	base := modelInput()
	base.LineageGeneration = 3
	base.State.WorldRevision = 7
	if _, err := cached.Propose(context.Background(), base); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*rinruntime.DecisionContext)
	}{
		{
			name: "lineage",
			mutate: func(input *rinruntime.DecisionContext) {
				input.LineageGeneration++
			},
		},
		{
			name: "head with repeated world revision",
			mutate: func(input *rinruntime.DecisionContext) {
				input.State.HeadHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			},
		},
		{
			name: "decision actor state",
			mutate: func(input *rinruntime.DecisionContext) {
				input.Actor.Memories[0].Summary = "The player interrupted."
			},
		},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := base
			input.Request.RequestID = "request.cache-boundary." + test.name
			input.Actor = cloneActorForCacheTest(t, base.Actor)
			test.mutate(&input)
			if _, err := cached.Propose(context.Background(), input); err != nil {
				t.Fatal(err)
			}
			if underlying.count() != index+2 {
				t.Fatalf(
					"distinct cache context reused a draft: calls=%d",
					underlying.count(),
				)
			}
		})
	}
}

func TestCachedPolicySeparatesAgencyContract(t *testing.T) {
	underlying := &countingPolicy{}
	cached, err := policy.NewCached(
		underlying,
		policy.CacheConfig{MaxEntries: 8, TTL: time.Minute},
	)
	if err != nil {
		t.Fatal(err)
	}
	input := modelInput()
	input.Agency = &protocol.AgencyDecision{
		Kind: protocol.TurnResponsive,
		Effective: protocol.AgencyPolicy{
			Initiative:           protocol.InitiativePassive,
			Obedience:            protocol.ObedienceObey,
			MessageCooldownTicks: 1200,
			MaxConsecutiveTurns:  2,
		},
	}
	if _, err := cached.Propose(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	changed := input
	agency := *input.Agency
	agency.Effective.Obedience = protocol.ObedienceIndependent
	changed.Agency = &agency
	changed.Request.RequestID = "request.changed-agency"
	if _, err := cached.Propose(context.Background(), changed); err != nil {
		t.Fatal(err)
	}
	if underlying.count() != 2 {
		t.Fatalf("different agency contracts shared a cache entry: %d", underlying.count())
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

func cloneActorForCacheTest(
	t *testing.T,
	actor protocol.ActorState,
) protocol.ActorState {
	t.Helper()
	payload, err := json.Marshal(actor)
	if err != nil {
		t.Fatal(err)
	}
	var cloned protocol.ActorState
	if err := json.Unmarshal(payload, &cloned); err != nil {
		t.Fatal(err)
	}
	return cloned
}
