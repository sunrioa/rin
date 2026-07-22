package policy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	rinruntime "github.com/sunrioa/rin/runtime"
)

type CacheConfig struct {
	MaxEntries int
	TTL        time.Duration
}

type Cached struct {
	policy rinruntime.Policy
	config CacheConfig

	mu       sync.Mutex
	entries  map[string]cacheEntry
	inflight map[string]*cacheCall
	now      func() time.Time
}

type cacheEntry struct {
	draft     rinruntime.ProposalDraft
	createdAt time.Time
	expiresAt time.Time
}

type cacheCall struct {
	done  chan struct{}
	draft rinruntime.ProposalDraft
	err   error
}

func NewCached(selectedPolicy rinruntime.Policy, config CacheConfig) (*Cached, error) {
	if selectedPolicy == nil {
		return nil, errors.New("cached policy is required")
	}
	if config.MaxEntries <= 0 {
		config.MaxEntries = 256
	}
	if config.MaxEntries > 4096 {
		return nil, errors.New("cache max entries must not exceed 4096")
	}
	if config.TTL <= 0 {
		config.TTL = 10 * time.Minute
	}
	return &Cached{
		policy: selectedPolicy, config: config,
		entries: make(map[string]cacheEntry), inflight: make(map[string]*cacheCall), now: time.Now,
	}, nil
}

func (p *Cached) Propose(ctx context.Context, input rinruntime.PolicyContext) (rinruntime.ProposalDraft, error) {
	key, err := proposalCacheKey(input)
	if err != nil {
		return rinruntime.ProposalDraft{}, err
	}
	p.mu.Lock()
	now := p.now()
	p.removeExpired(now)
	if entry, exists := p.entries[key]; exists {
		draft := cloneDraft(entry.draft)
		draft.PolicySource = cacheSource(draft.PolicySource)
		p.mu.Unlock()
		return draft, nil
	}
	if call, exists := p.inflight[key]; exists {
		p.mu.Unlock()
		select {
		case <-call.done:
			draft := cloneDraft(call.draft)
			if call.err == nil {
				draft.PolicySource = cacheSource(draft.PolicySource)
			}
			return draft, call.err
		case <-ctx.Done():
			return rinruntime.ProposalDraft{}, ctx.Err()
		}
	}
	call := &cacheCall{done: make(chan struct{})}
	p.inflight[key] = call
	p.mu.Unlock()

	draft, callError := p.policy.Propose(ctx, input)
	p.mu.Lock()
	call.draft = cloneDraft(draft)
	call.err = callError
	if callError == nil {
		completedAt := p.now()
		p.entries[key] = cacheEntry{draft: cloneDraft(draft), createdAt: completedAt, expiresAt: completedAt.Add(p.config.TTL)}
		p.enforceLimit()
	}
	delete(p.inflight, key)
	close(call.done)
	p.mu.Unlock()
	return draft, callError
}

func proposalCacheKey(input rinruntime.PolicyContext) (string, error) {
	payload, err := json.Marshal(struct {
		SessionID      string `json:"session_id"`
		StateVersion   any    `json:"state_version"`
		ActorID        string `json:"actor_id"`
		Tick           int64  `json:"tick"`
		Intent         string `json:"intent"`
		Tags           any    `json:"tags"`
		Actions        any    `json:"actions"`
		CandidateGoals any    `json:"candidate_goals"`
		Urgent         bool   `json:"urgent"`
	}{
		SessionID: input.State.SessionID, StateVersion: policyStateVersion(input), ActorID: input.Actor.ID,
		Tick: input.Request.Tick, Intent: input.Request.Intent, Tags: input.Request.Tags,
		Actions: input.Request.CandidateActions, CandidateGoals: input.Request.CandidateGoals, Urgent: input.Request.Urgent,
	})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func policyStateVersion(input rinruntime.PolicyContext) any {
	if input.State.WorldRevision > 0 {
		return struct {
			WorldRevision uint64 `json:"world_revision"`
		}{WorldRevision: input.State.WorldRevision}
	}
	return struct {
		HeadHash string `json:"head_hash"`
	}{HeadHash: input.State.HeadHash}
}

func (p *Cached) removeExpired(now time.Time) {
	for key, entry := range p.entries {
		if !now.Before(entry.expiresAt) {
			delete(p.entries, key)
		}
	}
}

func (p *Cached) enforceLimit() {
	if len(p.entries) <= p.config.MaxEntries {
		return
	}
	type item struct {
		key string
		at  time.Time
	}
	items := make([]item, 0, len(p.entries))
	for key, entry := range p.entries {
		items = append(items, item{key: key, at: entry.createdAt})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].at.Equal(items[j].at) {
			return items[i].key < items[j].key
		}
		return items[i].at.Before(items[j].at)
	})
	for len(p.entries) > p.config.MaxEntries {
		delete(p.entries, items[0].key)
		items = items[1:]
	}
}

func cloneDraft(draft rinruntime.ProposalDraft) rinruntime.ProposalDraft {
	draft.RecalledMemoryIDs = append([]string(nil), draft.RecalledMemoryIDs...)
	return draft
}

func cacheSource(source string) string {
	if source == "model" {
		return "model-cache"
	}
	if source == "boundary-guard" {
		return "boundary-guard-cache"
	}
	if source == "" {
		return "cache"
	}
	if len(source) > 58 {
		return "cache"
	}
	return source + "-cache"
}
