package runtime

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sunrioa/rin/protocol"
)

type managedSession struct {
	mu    sync.Mutex
	state protocol.SessionState
}

type Engine struct {
	mu       sync.RWMutex
	sessions map[string]*managedSession
	store    Store
	policy   Policy
	now      func() time.Time
}

func Open(store Store, policy Policy) (*Engine, error) {
	if store == nil || policy == nil {
		return nil, errors.New("store and policy are required")
	}
	engine := &Engine{
		sessions: make(map[string]*managedSession),
		store:    store,
		policy:   policy,
		now:      time.Now,
	}
	ids, err := store.ListSessions()
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	for _, id := range ids {
		events, err := store.Load(id)
		if err != nil {
			return nil, fmt.Errorf("load session %s: %w", id, err)
		}
		var state protocol.SessionState
		for _, event := range events {
			state, err = applyEvent(state, event)
			if err != nil {
				return nil, fmt.Errorf("replay session %s: %w", id, err)
			}
		}
		if state.SessionID == "" || state.SessionID != id {
			return nil, fmt.Errorf("replay session %s: %w", id, ErrCorruptLog)
		}
		engine.sessions[id] = &managedSession{state: state}
	}
	return engine, nil
}

func (e *Engine) CreateSession(request protocol.CreateSessionRequest) (protocol.MutationResult, error) {
	if err := protocol.ValidateCreateSession(request); err != nil {
		return protocol.MutationResult{}, validationError(err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if existing, found := e.sessions[request.SessionID]; found {
		existing.mu.Lock()
		defer existing.mu.Unlock()
		if duplicateReceipt(existing.state, request.RequestID, EventSessionCreated) {
			return mutationResult(existing.state, true), nil
		}
		return protocol.MutationResult{}, NewError("session_exists", "session already exists", ErrConflict)
	}
	payload := createdPayload{Request: request}
	event, err := newEvent(protocol.SessionState{}, EventSessionCreated, request.RequestID, payload, e.now())
	if err != nil {
		return protocol.MutationResult{}, NewError("event_encode_failed", "could not encode session event", err)
	}
	state, err := applyEvent(protocol.SessionState{}, event)
	if err != nil {
		return protocol.MutationResult{}, NewError("event_apply_failed", "could not initialize session", err)
	}
	if err := e.store.Create(request.SessionID, event); err != nil {
		return protocol.MutationResult{}, NewError("store_create_failed", "could not create session log", err)
	}
	e.sessions[request.SessionID] = &managedSession{state: state}
	return mutationResult(state, false), nil
}

func (e *Engine) Observe(request protocol.ObserveRequest) (protocol.MutationResult, error) {
	if err := protocol.ValidateObserve(request); err != nil {
		return protocol.MutationResult{}, validationError(err)
	}
	session, err := e.session(request.SessionID)
	if err != nil {
		return protocol.MutationResult{}, err
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if receipt, found := session.state.Receipts[request.RequestID]; found {
		if receipt.Kind == EventObserved {
			return mutationResult(session.state, true), nil
		}
		return protocol.MutationResult{}, requestConflict(request.RequestID)
	}
	if request.Tick < session.state.Tick {
		return protocol.MutationResult{}, NewFieldError("tick_regressed", "observation tick is older than session state", "tick", ErrConflict)
	}
	for _, actorID := range request.ObserverIDs {
		if _, exists := session.state.Actors[actorID]; !exists {
			return protocol.MutationResult{}, NewFieldError("unknown_actor", "observer is not registered", "observer_ids", ErrNotFound)
		}
	}
	if eventIDExists(session.state, request.EventID) {
		return protocol.MutationResult{}, NewFieldError("event_exists", "event id was already observed", "event_id", ErrConflict)
	}
	event, err := newEvent(session.state, EventObserved, request.RequestID, observedPayload{Request: request}, e.now())
	if err != nil {
		return protocol.MutationResult{}, NewError("event_encode_failed", "could not encode observation", err)
	}
	if err := e.appendAndApply(session, event); err != nil {
		return protocol.MutationResult{}, err
	}
	return mutationResult(session.state, false), nil
}

func (e *Engine) Propose(ctx context.Context, request protocol.ProposeRequest) (protocol.ActionProposal, bool, error) {
	if err := protocol.ValidatePropose(request); err != nil {
		return protocol.ActionProposal{}, false, validationError(err)
	}
	session, err := e.session(request.SessionID)
	if err != nil {
		return protocol.ActionProposal{}, false, err
	}
	session.mu.Lock()
	if receipt, found := session.state.Receipts[request.RequestID]; found {
		if receipt.Kind == EventProposed {
			proposal, exists := session.state.Proposals[receipt.EntityID]
			session.mu.Unlock()
			if !exists {
				return protocol.ActionProposal{}, false, NewError("proposal_missing", "idempotent proposal is no longer retained", ErrNotFound)
			}
			return proposal, true, nil
		}
		session.mu.Unlock()
		return protocol.ActionProposal{}, false, requestConflict(request.RequestID)
	}
	actor, exists := session.state.Actors[request.ActorID]
	if !exists {
		session.mu.Unlock()
		return protocol.ActionProposal{}, false, NewFieldError("unknown_actor", "actor is not registered", "actor_id", ErrNotFound)
	}
	if !actor.Enabled {
		session.mu.Unlock()
		return protocol.ActionProposal{}, false, NewError("actor_disabled", "actor is disabled", ErrConflict)
	}
	if request.Tick < session.state.Tick {
		session.mu.Unlock()
		return protocol.ActionProposal{}, false, NewFieldError("tick_regressed", "proposal tick is older than session state", "tick", ErrConflict)
	}
	if !request.Urgent && request.Tick < actor.NextThinkTick {
		session.mu.Unlock()
		return protocol.ActionProposal{}, false, NewError("actor_not_due", "actor is not scheduled to think yet", ErrNotDue)
	}
	stateCopy, err := clone(session.state)
	if err != nil {
		session.mu.Unlock()
		return protocol.ActionProposal{}, false, NewError("state_copy_failed", "could not prepare policy context", err)
	}
	baseRevision := session.state.Revision
	baseHash := session.state.HeadHash
	session.mu.Unlock()

	draft, err := e.policy.Propose(ctx, PolicyContext{State: stateCopy, Actor: actor, Request: request})
	if err != nil {
		if errors.Is(err, ErrNoSafeAction) {
			return protocol.ActionProposal{}, false, NewError("no_safe_action", "no candidate action satisfies the actor boundary", err)
		}
		return protocol.ActionProposal{}, false, NewError("policy_failed", "policy could not produce a proposal", err)
	}
	selected, err := validateDraft(request, actor, draft)
	if err != nil {
		return protocol.ActionProposal{}, false, err
	}

	session.mu.Lock()
	defer session.mu.Unlock()
	if receipt, found := session.state.Receipts[request.RequestID]; found {
		if receipt.Kind == EventProposed {
			proposal := session.state.Proposals[receipt.EntityID]
			return proposal, true, nil
		}
		return protocol.ActionProposal{}, false, requestConflict(request.RequestID)
	}
	if session.state.Revision != baseRevision || session.state.HeadHash != baseHash {
		return protocol.ActionProposal{}, false, NewError("state_changed", "session changed while policy was proposing; retry with a new request id", ErrStale)
	}
	proposalHash, err := hashJSON(struct {
		SessionID string `json:"session_id"`
		RequestID string `json:"request_id"`
		ActorID   string `json:"actor_id"`
		HeadHash  string `json:"head_hash"`
	}{request.SessionID, request.RequestID, request.ActorID, baseHash})
	if err != nil {
		return protocol.ActionProposal{}, false, NewError("proposal_id_failed", "could not identify proposal", err)
	}
	proposal := protocol.ActionProposal{
		ID:                "proposal." + proposalHash[:24],
		SessionID:         request.SessionID,
		RequestID:         request.RequestID,
		ActorID:           request.ActorID,
		Tick:              request.Tick,
		BasedOnRevision:   baseRevision,
		BasedOnHeadHash:   baseHash,
		CreatedRevision:   baseRevision + 1,
		Action:            selected,
		Stance:            draft.Stance,
		Summary:           draft.Summary,
		Rationale:         draft.Rationale,
		RecalledMemoryIDs: append([]string(nil), draft.RecalledMemoryIDs...),
		GoalID:            draft.GoalID,
		Status:            "pending",
	}
	event, err := newEvent(session.state, EventProposed, request.RequestID, proposedPayload{Proposal: proposal}, e.now())
	if err != nil {
		return protocol.ActionProposal{}, false, NewError("event_encode_failed", "could not encode proposal", err)
	}
	if err := e.appendAndApply(session, event); err != nil {
		return protocol.ActionProposal{}, false, err
	}
	return proposal, false, nil
}

func (e *Engine) Commit(request protocol.CommitRequest) (protocol.MutationResult, error) {
	if err := protocol.ValidateCommit(request); err != nil {
		return protocol.MutationResult{}, validationError(err)
	}
	session, err := e.session(request.SessionID)
	if err != nil {
		return protocol.MutationResult{}, err
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if receipt, found := session.state.Receipts[request.RequestID]; found {
		if receipt.Kind == EventCommitted {
			return mutationResult(session.state, true), nil
		}
		return protocol.MutationResult{}, requestConflict(request.RequestID)
	}
	proposal, exists := session.state.Proposals[request.ProposalID]
	if !exists {
		return protocol.MutationResult{}, NewFieldError("unknown_proposal", "proposal is not retained", "proposal_id", ErrNotFound)
	}
	if proposal.Status != "pending" {
		return protocol.MutationResult{}, NewFieldError("proposal_resolved", "proposal was already resolved", "proposal_id", ErrConflict)
	}
	if request.Accepted && proposal.CreatedRevision != session.state.Revision {
		return protocol.MutationResult{}, NewError("proposal_stale", "session changed after the proposal was created", ErrStale)
	}
	if request.Tick < session.state.Tick || request.Tick < proposal.Tick {
		return protocol.MutationResult{}, NewFieldError("tick_regressed", "commit tick is older than its proposal or session", "tick", ErrConflict)
	}
	actor := session.state.Actors[proposal.ActorID]
	for index, update := range request.GoalUpdates {
		if !goalExists(actor, update.GoalID) {
			return protocol.MutationResult{}, NewFieldError("unknown_goal", "goal update references an unknown goal", fmt.Sprintf("goal_updates[%d].goal_id", index), ErrNotFound)
		}
	}
	event, err := newEvent(session.state, EventCommitted, request.RequestID, committedPayload{Request: request}, e.now())
	if err != nil {
		return protocol.MutationResult{}, NewError("event_encode_failed", "could not encode action commit", err)
	}
	if err := e.appendAndApply(session, event); err != nil {
		return protocol.MutationResult{}, err
	}
	return mutationResult(session.state, false), nil
}

func (e *Engine) State(request protocol.SessionRequest) (protocol.SessionState, error) {
	if err := protocol.ValidateSessionRequest(request); err != nil {
		return protocol.SessionState{}, validationError(err)
	}
	session, err := e.session(request.SessionID)
	if err != nil {
		return protocol.SessionState{}, err
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	return clone(session.state)
}

func (e *Engine) Snapshot(request protocol.SessionRequest) (protocol.Snapshot, error) {
	if err := protocol.ValidateSessionRequest(request); err != nil {
		return protocol.Snapshot{}, validationError(err)
	}
	session, err := e.session(request.SessionID)
	if err != nil {
		return protocol.Snapshot{}, err
	}
	session.mu.Lock()
	snapshot, err := SnapshotOf(session.state)
	session.mu.Unlock()
	if err != nil {
		return protocol.Snapshot{}, NewError("snapshot_failed", "could not create snapshot", err)
	}
	if err := e.store.SaveSnapshot(request.SessionID, snapshot); err != nil {
		return protocol.Snapshot{}, NewError("snapshot_store_failed", "could not persist snapshot", err)
	}
	return snapshot, nil
}

func (e *Engine) Restore(request protocol.RestoreRequest) (protocol.MutationResult, error) {
	if err := protocol.ValidateRestore(request); err != nil {
		return protocol.MutationResult{}, validationError(err)
	}
	if err := ValidateSnapshot(request.Snapshot); err != nil {
		return protocol.MutationResult{}, err
	}
	e.mu.Lock()
	session, exists := e.sessions[request.SessionID]
	if !exists {
		if receipt, found := request.Snapshot.State.Receipts[request.RequestID]; found && receipt.Kind != EventSessionRestored {
			e.mu.Unlock()
			return protocol.MutationResult{}, requestConflict(request.RequestID)
		}
		event, err := newEvent(protocol.SessionState{}, EventSessionRestored, request.RequestID, restoredPayload{Snapshot: request.Snapshot}, e.now())
		if err != nil {
			e.mu.Unlock()
			return protocol.MutationResult{}, NewError("event_encode_failed", "could not encode restore", err)
		}
		state, err := applyEvent(protocol.SessionState{}, event)
		if err != nil {
			e.mu.Unlock()
			return protocol.MutationResult{}, NewError("event_apply_failed", "could not restore session", err)
		}
		if err := e.store.Create(request.SessionID, event); err != nil {
			e.mu.Unlock()
			return protocol.MutationResult{}, NewError("store_create_failed", "could not create restored session log", err)
		}
		e.sessions[request.SessionID] = &managedSession{state: state}
		e.mu.Unlock()
		return mutationResult(state, false), nil
	}
	e.mu.Unlock()

	session.mu.Lock()
	defer session.mu.Unlock()
	if receipt, found := session.state.Receipts[request.RequestID]; found {
		if receipt.Kind == EventSessionRestored {
			return mutationResult(session.state, true), nil
		}
		return protocol.MutationResult{}, requestConflict(request.RequestID)
	}
	if session.state.Binding != request.Snapshot.State.Binding {
		return protocol.MutationResult{}, NewFieldError("binding_mismatch", "snapshot belongs to different game content", "snapshot.state.binding", ErrConflict)
	}
	event, err := newEvent(session.state, EventSessionRestored, request.RequestID, restoredPayload{Snapshot: request.Snapshot}, e.now())
	if err != nil {
		return protocol.MutationResult{}, NewError("event_encode_failed", "could not encode restore", err)
	}
	if err := e.appendAndApply(session, event); err != nil {
		return protocol.MutationResult{}, err
	}
	return mutationResult(session.state, false), nil
}

func (e *Engine) DueAgents(request protocol.DueAgentsRequest) (protocol.DueAgentsResponse, error) {
	if err := protocol.ValidateDueAgents(request); err != nil {
		return protocol.DueAgentsResponse{}, validationError(err)
	}
	session, err := e.session(request.SessionID)
	if err != nil {
		return protocol.DueAgentsResponse{}, err
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	agents := make([]protocol.DueAgent, 0)
	for id, actor := range session.state.Actors {
		if actor.Enabled && actor.NextThinkTick <= request.Tick {
			agents = append(agents, protocol.DueAgent{ActorID: id, NextThinkTick: actor.NextThinkTick})
		}
	}
	sort.Slice(agents, func(i, j int) bool {
		if agents[i].NextThinkTick == agents[j].NextThinkTick {
			return agents[i].ActorID < agents[j].ActorID
		}
		return agents[i].NextThinkTick < agents[j].NextThinkTick
	})
	if len(agents) > request.Limit {
		agents = agents[:request.Limit]
	}
	return protocol.DueAgentsResponse{SessionID: request.SessionID, Tick: request.Tick, Agents: agents}, nil
}

func (e *Engine) session(id string) (*managedSession, error) {
	e.mu.RLock()
	session, exists := e.sessions[id]
	e.mu.RUnlock()
	if !exists {
		return nil, NewFieldError("session_not_found", "session does not exist", "session_id", ErrNotFound)
	}
	return session, nil
}

func (e *Engine) appendAndApply(session *managedSession, event protocol.EventRecord) error {
	state, err := applyEvent(session.state, event)
	if err != nil {
		return NewError("event_apply_failed", "event could not be applied", err)
	}
	if err := e.store.Append(session.state.SessionID, event); err != nil {
		return NewError("store_append_failed", "could not persist event", err)
	}
	session.state = state
	return nil
}

func mutationResult(state protocol.SessionState, duplicate bool) protocol.MutationResult {
	return protocol.MutationResult{SessionID: state.SessionID, Revision: state.Revision, HeadHash: state.HeadHash, Duplicate: duplicate}
}

func validationError(err error) error {
	var validation *protocol.ValidationError
	if errors.As(err, &validation) {
		return NewFieldError("invalid_request", validation.Message, validation.Field, err)
	}
	return NewError("invalid_request", "request is invalid", err)
}

func requestConflict(requestID string) error {
	return NewFieldError("request_id_conflict", "request id was already used for another operation", "request_id", fmt.Errorf("%w: %s", ErrConflict, requestID))
}

func duplicateReceipt(state protocol.SessionState, requestID, kind string) bool {
	receipt, exists := state.Receipts[requestID]
	return exists && receipt.Kind == kind
}

func eventIDExists(state protocol.SessionState, eventID string) bool {
	for _, actor := range state.Actors {
		for _, memory := range actor.Memories {
			if memory.EventID == eventID {
				return true
			}
		}
	}
	return false
}

func goalExists(actor protocol.ActorState, goalID string) bool {
	for _, goal := range actor.Goals {
		if goal.ID == goalID {
			return true
		}
	}
	return false
}

func validateDraft(request protocol.ProposeRequest, actor protocol.ActorState, draft ProposalDraft) (protocol.ActionSpec, error) {
	var selected protocol.ActionSpec
	found := false
	for _, action := range request.CandidateActions {
		if action.ID == draft.ActionID {
			selected = action
			found = true
			break
		}
	}
	if !found {
		return protocol.ActionSpec{}, NewFieldError("invalid_policy_output", "policy selected an action outside the candidate list", "action_id", ErrConflict)
	}
	if draft.Stance != "engage" && draft.Stance != "partial" && draft.Stance != "redirect" && draft.Stance != "refuse" && draft.Stance != "wait" {
		return protocol.ActionSpec{}, NewFieldError("invalid_policy_output", "policy returned an unsupported stance", "stance", ErrConflict)
	}
	if err := validatePolicyText("summary", draft.Summary, 500, true); err != nil {
		return protocol.ActionSpec{}, err
	}
	if err := validatePolicyText("rationale", draft.Rationale, 500, true); err != nil {
		return protocol.ActionSpec{}, err
	}
	if len(draft.RecalledMemoryIDs) > 8 {
		return protocol.ActionSpec{}, NewFieldError("invalid_policy_output", "policy recalled too many memories", "recalled_memory_ids", ErrConflict)
	}
	memoryIDs := make(map[string]struct{}, len(actor.Memories))
	for _, memory := range actor.Memories {
		memoryIDs[memory.ID] = struct{}{}
	}
	seen := make(map[string]struct{}, len(draft.RecalledMemoryIDs))
	for _, id := range draft.RecalledMemoryIDs {
		if _, exists := memoryIDs[id]; !exists {
			return protocol.ActionSpec{}, NewFieldError("invalid_policy_output", "policy referenced an unknown memory", "recalled_memory_ids", ErrConflict)
		}
		if _, exists := seen[id]; exists {
			return protocol.ActionSpec{}, NewFieldError("invalid_policy_output", "policy repeated a memory id", "recalled_memory_ids", ErrConflict)
		}
		seen[id] = struct{}{}
	}
	if draft.GoalID != "" && !goalExists(actor, draft.GoalID) {
		return protocol.ActionSpec{}, NewFieldError("invalid_policy_output", "policy referenced an unknown goal", "goal_id", ErrConflict)
	}
	return selected, nil
}

func validatePolicyText(field, value string, maximum int, required bool) error {
	if required && strings.TrimSpace(value) == "" {
		return NewFieldError("invalid_policy_output", "policy text is required", field, ErrConflict)
	}
	if !utf8.ValidString(value) || strings.ContainsRune(value, 0) || utf8.RuneCountInString(value) > maximum {
		return NewFieldError("invalid_policy_output", "policy text is invalid or too long", field, ErrConflict)
	}
	return nil
}
