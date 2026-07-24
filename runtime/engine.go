package runtime

import (
	"bytes"
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
	mu                 sync.Mutex
	state              protocol.SessionState
	uncertainProposals map[string]uncertainProposalAppend
}

type uncertainProposalAppend struct {
	event       protocol.EventRecord
	requestHash string
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
		return protocol.MutationResult{}, eventEncodeError(err, "could not encode session event")
	}
	state, err := e.createAndConfirm(request.SessionID, event)
	if err != nil {
		return protocol.MutationResult{}, err
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
	if err := worldRevisionAdvanceError(session.state); err != nil {
		return protocol.MutationResult{}, err
	}
	if !protocol.HasFeature(session.state.Features, protocol.FeatureOutcomeReporting) &&
		request.Tick < session.state.Tick {
		return protocol.MutationResult{}, NewFieldError("tick_regressed", "observation tick is older than session state", "tick", ErrConflict)
	}
	for _, actorID := range request.ObserverIDs {
		if _, exists := session.state.Actors[actorID]; !exists {
			return protocol.MutationResult{}, NewFieldError("unknown_actor", "observer is not registered", "observer_ids", ErrNotFound)
		}
	}
	if err := validateFactVisibility(session.state, request.Facts, "facts"); err != nil {
		return protocol.MutationResult{}, err
	}
	if eventIDExists(session.state, request.EventID) {
		return protocol.MutationResult{}, NewFieldError("event_exists", "event id was already observed", "event_id", ErrConflict)
	}
	event, err := newEvent(session.state, EventObserved, request.RequestID, observedPayload{Request: request}, e.now())
	if err != nil {
		return protocol.MutationResult{}, eventEncodeError(err, "could not encode observation")
	}
	if err := e.appendAndApply(session, event); err != nil {
		return protocol.MutationResult{}, err
	}
	return mutationResult(session.state, false), nil
}

func (e *Engine) Propose(ctx context.Context, request protocol.ProposeRequest) (protocol.ActionProposal, bool, error) {
	if err := ctx.Err(); err != nil {
		return protocol.ActionProposal{}, false, NewError("proposal_canceled", "proposal request was canceled", err)
	}
	if err := protocol.ValidatePropose(request); err != nil {
		return protocol.ActionProposal{}, false, validationError(err)
	}
	requestSnapshot, err := clone(request)
	if err != nil {
		return protocol.ActionProposal{}, false, NewError(
			"request_copy_failed",
			"could not prepare an isolated policy request",
			err,
		)
	}
	requestHash, err := hashJSON(requestSnapshot)
	if err != nil {
		return protocol.ActionProposal{}, false, NewError(
			"request_encode_failed",
			"could not identify proposal request",
			err,
		)
	}
	session, err := e.session(request.SessionID)
	if err != nil {
		return protocol.ActionProposal{}, false, err
	}
	session.mu.Lock()
	if receipt, found := session.state.Receipts[request.RequestID]; found {
		if receipt.Kind == EventProposed {
			if receipt.RequestHash != "" && receipt.RequestHash != requestHash {
				session.mu.Unlock()
				return protocol.ActionProposal{}, false, requestConflict(request.RequestID)
			}
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
	if uncertain, found := session.uncertainProposals[request.RequestID]; found {
		if uncertain.requestHash != requestHash {
			session.mu.Unlock()
			return protocol.ActionProposal{}, false, requestConflict(request.RequestID)
		}
		if err := e.appendAndApply(session, uncertain.event); err != nil {
			session.mu.Unlock()
			return protocol.ActionProposal{}, false, err
		}
		delete(session.uncertainProposals, request.RequestID)
		receipt := session.state.Receipts[request.RequestID]
		proposal, exists := session.state.Proposals[receipt.EntityID]
		session.mu.Unlock()
		if !exists {
			return protocol.ActionProposal{}, false, NewError(
				"proposal_missing",
				"reconciled proposal is no longer retained",
				ErrNotFound,
			)
		}
		return proposal, false, nil
	}
	if len(session.uncertainProposals) > 0 {
		session.mu.Unlock()
		return protocol.ActionProposal{}, false, unresolvedProposalError()
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
	if len(request.CandidateGoals) > 0 && !protocol.HasFeature(session.state.Features, protocol.FeatureGoalCandidates) {
		session.mu.Unlock()
		return protocol.ActionProposal{}, false, NewFieldError("feature_not_enabled", "candidate goals require goal-candidates-v1", "candidate_goals", ErrConflict)
	}
	for index, goal := range requestSnapshot.CandidateGoals {
		if goalExists(actor, goal.ID) {
			session.mu.Unlock()
			return protocol.ActionProposal{}, false, NewFieldError("goal_exists", "candidate goal is already part of actor state", fmt.Sprintf("candidate_goals[%d].id", index), ErrConflict)
		}
		if pendingProposedGoalReserved(session.state, actor.ID, goal.ID, "") {
			session.mu.Unlock()
			return protocol.ActionProposal{}, false, NewFieldError(
				"goal_exists",
				"candidate goal is already reserved by a pending proposal",
				fmt.Sprintf("candidate_goals[%d].id", index),
				ErrConflict,
			)
		}
	}
	if !canRetainAnotherProposal(session.state) {
		session.mu.Unlock()
		return protocol.ActionProposal{}, false, NewError(
			"proposal_capacity",
			"all retained proposal slots are pending; report or reject an outcome before proposing again",
			ErrConflict,
		)
	}
	if actor.Activity != nil && actor.Activity.State == "dormant" {
		session.mu.Unlock()
		return protocol.ActionProposal{}, false, NewError("actor_dormant", "actor is dormant and must be woken by the game", ErrNotDue)
	}
	if request.Tick < session.state.Tick {
		session.mu.Unlock()
		return protocol.ActionProposal{}, false, NewFieldError("tick_regressed", "proposal tick is older than session state", "tick", ErrConflict)
	}
	if !request.Urgent && request.Tick < actor.NextThinkTick {
		session.mu.Unlock()
		return protocol.ActionProposal{}, false, NewError("actor_not_due", "actor is not scheduled to think yet", ErrNotDue)
	}
	if session.state.Revision == ^uint64(0) {
		session.mu.Unlock()
		return protocol.ActionProposal{}, false, NewFieldError(
			"revision_overflow",
			"session revision is exhausted",
			"revision",
			ErrConflict,
		)
	}
	validationActor, err := clone(actor)
	if err != nil {
		session.mu.Unlock()
		return protocol.ActionProposal{}, false, NewError(
			"state_copy_failed",
			"could not prepare an isolated actor validation context",
			err,
		)
	}
	stateCopy, err := clone(session.state)
	if err != nil {
		session.mu.Unlock()
		return protocol.ActionProposal{}, false, NewError("state_copy_failed", "could not prepare policy context", err)
	}
	policyActor, exists := stateCopy.Actors[request.ActorID]
	if !exists {
		session.mu.Unlock()
		return protocol.ActionProposal{}, false, NewFieldError("unknown_actor", "actor is not registered", "actor_id", ErrNotFound)
	}
	policyRequest, err := clone(requestSnapshot)
	if err != nil {
		session.mu.Unlock()
		return protocol.ActionProposal{}, false, NewError("request_copy_failed", "could not prepare policy context", err)
	}
	baseRevision := session.state.Revision
	baseHash := session.state.HeadHash
	baseWorldRevision := session.state.WorldRevision
	arbitrationEnabled := protocol.HasFeature(session.state.Features, protocol.FeatureArbitration)
	session.mu.Unlock()

	draft, err := e.policy.Propose(ctx, PolicyContext{State: stateCopy, Actor: policyActor, Request: policyRequest})
	if err != nil {
		if errors.Is(err, ErrNoSafeAction) {
			return protocol.ActionProposal{}, false, NewError("no_safe_action", "no candidate action satisfies the actor boundary", err)
		}
		return protocol.ActionProposal{}, false, NewError("policy_failed", "policy could not produce a proposal", err)
	}
	if err := ctx.Err(); err != nil {
		return protocol.ActionProposal{}, false, NewError("proposal_canceled", "proposal request was canceled", err)
	}
	selected, proposedGoal, err := validateDraft(requestSnapshot, validationActor, draft)
	if err != nil {
		return protocol.ActionProposal{}, false, err
	}

	session.mu.Lock()
	defer session.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return protocol.ActionProposal{}, false, NewError("proposal_canceled", "proposal request was canceled", err)
	}
	if receipt, found := session.state.Receipts[request.RequestID]; found {
		if receipt.Kind == EventProposed {
			if receipt.RequestHash != "" && receipt.RequestHash != requestHash {
				return protocol.ActionProposal{}, false, requestConflict(request.RequestID)
			}
			proposal, exists := session.state.Proposals[receipt.EntityID]
			if !exists {
				return protocol.ActionProposal{}, false, NewError("proposal_missing", "idempotent proposal is no longer retained", ErrNotFound)
			}
			return proposal, true, nil
		}
		return protocol.ActionProposal{}, false, requestConflict(request.RequestID)
	}
	if uncertain, found := session.uncertainProposals[request.RequestID]; found {
		if uncertain.requestHash != requestHash {
			return protocol.ActionProposal{}, false, requestConflict(request.RequestID)
		}
		if err := e.appendAndApply(session, uncertain.event); err != nil {
			return protocol.ActionProposal{}, false, err
		}
		delete(session.uncertainProposals, request.RequestID)
		receipt := session.state.Receipts[request.RequestID]
		proposal, exists := session.state.Proposals[receipt.EntityID]
		if !exists {
			return protocol.ActionProposal{}, false, NewError(
				"proposal_missing",
				"reconciled proposal is no longer retained",
				ErrNotFound,
			)
		}
		return proposal, false, nil
	}
	if len(session.uncertainProposals) > 0 {
		return protocol.ActionProposal{}, false, unresolvedProposalError()
	}
	worldChanged := arbitrationEnabled && session.state.WorldRevision != baseWorldRevision
	legacyChanged := !arbitrationEnabled && (session.state.Revision != baseRevision || session.state.HeadHash != baseHash)
	if worldChanged || legacyChanged {
		return protocol.ActionProposal{}, false, NewError("state_changed", "session changed while policy was proposing; retry with a new request id", ErrStale)
	}
	if !canRetainAnotherProposal(session.state) {
		return protocol.ActionProposal{}, false, NewError(
			"proposal_capacity",
			"all retained proposal slots are pending; report or reject an outcome before proposing again",
			ErrConflict,
		)
	}
	if proposedGoal != nil {
		if err := validateProposedGoalReservation(
			session.state,
			request.ActorID,
			proposedGoal.ID,
			"",
			"proposed_goal.id",
		); err != nil {
			return protocol.ActionProposal{}, false, err
		}
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
		ID:                   "proposal." + proposalHash[:24],
		SessionID:            request.SessionID,
		RequestID:            request.RequestID,
		ActorID:              request.ActorID,
		Tick:                 request.Tick,
		BasedOnRevision:      baseRevision,
		BasedOnHeadHash:      baseHash,
		BasedOnWorldRevision: baseWorldRevision,
		CreatedRevision:      session.state.Revision + 1,
		Action:               selected,
		Stance:               draft.Stance,
		Summary:              draft.Summary,
		Rationale:            draft.Rationale,
		PolicySource:         policySource(draft.PolicySource),
		RecalledMemoryIDs:    append([]string(nil), draft.RecalledMemoryIDs...),
		GoalID:               draft.GoalID,
		ProposedGoal:         proposedGoal,
		Status:               "pending",
	}
	event, err := newEvent(
		session.state,
		EventProposed,
		request.RequestID,
		proposedPayload{Proposal: proposal, RequestHash: requestHash},
		e.now(),
	)
	if err != nil {
		return protocol.ActionProposal{}, false, eventEncodeError(err, "could not encode proposal")
	}
	if err := e.appendAndApply(session, event); err != nil {
		if ErrorCode(err) == "proposal_outcome_unknown" {
			if session.uncertainProposals == nil {
				session.uncertainProposals = make(map[string]uncertainProposalAppend)
			}
			session.uncertainProposals[request.RequestID] = uncertainProposalAppend{
				event:       event,
				requestHash: requestHash,
			}
		}
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
	if err := worldRevisionAdvanceError(session.state); err != nil {
		return protocol.MutationResult{}, err
	}
	proposal, exists := session.state.Proposals[request.ProposalID]
	if !exists {
		return protocol.MutationResult{}, NewFieldError("unknown_proposal", "proposal is not retained", "proposal_id", ErrNotFound)
	}
	if proposal.Status != "pending" {
		return protocol.MutationResult{}, NewFieldError("proposal_resolved", "proposal was already resolved", "proposal_id", ErrConflict)
	}
	outcomeReporting := protocol.HasFeature(session.state.Features, protocol.FeatureOutcomeReporting)
	if outcomeReporting {
		// Commit is the game authority's report of an outcome that it has
		// already applied or rejected. State may advance before that report
		// arrives, so only reject an impossible occurrence time.
		if request.Tick < proposal.Tick {
			return protocol.MutationResult{}, NewFieldError("tick_regressed", "commit tick is older than its proposal", "tick", ErrConflict)
		}
	} else {
		// Preserve pre-feature event-log and API semantics for existing
		// sessions. New integrations opt in through outcome-reporting-v1.
		worldRevisionMismatch := proposal.BasedOnWorldRevision > 0 &&
			proposal.BasedOnWorldRevision != session.state.WorldRevision
		legacyRevisionMismatch := proposal.BasedOnWorldRevision == 0 &&
			proposal.CreatedRevision != session.state.Revision
		if request.Accepted && (worldRevisionMismatch || legacyRevisionMismatch) {
			return protocol.MutationResult{}, NewError("proposal_stale", "session changed after the proposal was created", ErrStale)
		}
		if request.Tick < session.state.Tick || request.Tick < proposal.Tick {
			return protocol.MutationResult{}, NewFieldError("tick_regressed", "commit tick is older than its proposal or session", "tick", ErrConflict)
		}
	}
	if outcomeReporting {
		if !request.Accepted && (len(request.Facts) > 0 || len(request.GoalUpdates) > 0) {
			return protocol.MutationResult{}, NewFieldError(
				"rejected_outcome_updates",
				"rejected outcomes cannot carry facts or goal updates; report observations separately",
				"accepted",
				ErrConflict,
			)
		}
		if duplicateGoalUpdate(request.GoalUpdates) {
			return protocol.MutationResult{}, NewFieldError(
				"duplicate_goal_update",
				"goal updates must contain at most one entry per goal",
				"goal_updates",
				ErrConflict,
			)
		}
	}
	if err := validateFactVisibility(session.state, request.Facts, "facts"); err != nil {
		return protocol.MutationResult{}, err
	}
	if eventIDExists(session.state, request.EventID) {
		return protocol.MutationResult{}, NewFieldError("event_exists", "event id was already observed or reported", "event_id", ErrConflict)
	}
	actor := session.state.Actors[proposal.ActorID]
	if request.Accepted && request.Tick > maxInt64-actor.ThinkEveryTicks {
		return protocol.MutationResult{}, NewFieldError("tick_overflow", "commit tick cannot be scheduled safely", "tick", ErrConflict)
	}
	if request.Accepted && proposal.ProposedGoal != nil {
		if err := validateProposedGoalReservation(
			session.state,
			proposal.ActorID,
			proposal.ProposedGoal.ID,
			proposal.ID,
			"proposal_id",
		); err != nil {
			return protocol.MutationResult{}, err
		}
	}
	for index, update := range request.GoalUpdates {
		if !goalExists(actor, update.GoalID) && (proposal.ProposedGoal == nil || proposal.ProposedGoal.ID != update.GoalID) {
			return protocol.MutationResult{}, NewFieldError("unknown_goal", "goal update references an unknown goal", fmt.Sprintf("goal_updates[%d].goal_id", index), ErrNotFound)
		}
	}
	event, err := newEvent(session.state, EventCommitted, request.RequestID, committedPayload{Request: request}, e.now())
	if err != nil {
		return protocol.MutationResult{}, eventEncodeError(err, "could not encode action commit")
	}
	if err := e.appendAndApply(session, event); err != nil {
		return protocol.MutationResult{}, err
	}
	return mutationResult(session.state, false), nil
}

func (e *Engine) CommitBatch(request protocol.BatchCommitRequest) (protocol.MutationResult, error) {
	if err := protocol.ValidateBatchCommit(request); err != nil {
		return protocol.MutationResult{}, validationError(err)
	}
	session, err := e.session(request.SessionID)
	if err != nil {
		return protocol.MutationResult{}, err
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if !protocol.HasFeature(session.state.Features, protocol.FeatureArbitration) {
		return protocol.MutationResult{}, NewError("feature_not_enabled", "batch commit requires arbitration-v1", ErrConflict)
	}
	if receipt, found := session.state.Receipts[request.RequestID]; found {
		if receipt.Kind == EventBatchCommitted {
			return mutationResult(session.state, true), nil
		}
		return protocol.MutationResult{}, requestConflict(request.RequestID)
	}
	if err := worldRevisionAdvanceError(session.state); err != nil {
		return protocol.MutationResult{}, err
	}
	outcomeReporting := protocol.HasFeature(session.state.Features, protocol.FeatureOutcomeReporting)
	if !outcomeReporting && request.Tick < session.state.Tick {
		return protocol.MutationResult{}, NewFieldError("tick_regressed", "batch commit tick is older than session state", "tick", ErrConflict)
	}
	actors := make(map[string]struct{}, len(request.Items))
	eventIDs := make(map[string]struct{}, len(request.Items))
	var baseWorldRevision uint64
	for index, item := range request.Items {
		proposal, exists := session.state.Proposals[item.ProposalID]
		if !exists {
			return protocol.MutationResult{}, NewFieldError("unknown_proposal", "batch item references an unknown proposal", fmt.Sprintf("items[%d].proposal_id", index), ErrNotFound)
		}
		if proposal.Status != "pending" {
			return protocol.MutationResult{}, NewFieldError("proposal_resolved", "batch item references a resolved proposal", fmt.Sprintf("items[%d].proposal_id", index), ErrConflict)
		}
		if outcomeReporting {
			if proposal.BasedOnWorldRevision == 0 {
				return protocol.MutationResult{}, NewFieldError("proposal_base_mismatch", "batch proposals must identify one world revision", "items", ErrConflict)
			}
			if index == 0 {
				baseWorldRevision = proposal.BasedOnWorldRevision
			} else if proposal.BasedOnWorldRevision != baseWorldRevision {
				return protocol.MutationResult{}, NewFieldError("proposal_base_mismatch", "batch proposals were created from different world revisions", "items", ErrConflict)
			}
		} else if proposal.BasedOnWorldRevision == 0 ||
			proposal.BasedOnWorldRevision != session.state.WorldRevision {
			return protocol.MutationResult{}, NewError("proposal_stale", "batch contains a proposal from another world revision", ErrStale)
		}
		if request.Tick < proposal.Tick {
			return protocol.MutationResult{}, NewFieldError("tick_regressed", "batch commit tick is older than a proposal", "tick", ErrConflict)
		}
		if outcomeReporting {
			if !item.Accepted && (len(item.Facts) > 0 || len(item.GoalUpdates) > 0) {
				return protocol.MutationResult{}, NewFieldError(
					"rejected_outcome_updates",
					"rejected outcomes cannot carry facts or goal updates; report observations separately",
					fmt.Sprintf("items[%d].accepted", index),
					ErrConflict,
				)
			}
			if duplicateGoalUpdate(item.GoalUpdates) {
				return protocol.MutationResult{}, NewFieldError(
					"duplicate_goal_update",
					"goal updates must contain at most one entry per goal",
					fmt.Sprintf("items[%d].goal_updates", index),
					ErrConflict,
				)
			}
		}
		if err := validateFactVisibility(
			session.state,
			item.Facts,
			fmt.Sprintf("items[%d].facts", index),
		); err != nil {
			return protocol.MutationResult{}, err
		}
		if _, duplicate := actors[proposal.ActorID]; duplicate {
			return protocol.MutationResult{}, NewFieldError("duplicate_actor", "batch may contain at most one proposal per actor", "items", ErrConflict)
		}
		actors[proposal.ActorID] = struct{}{}
		if eventIDExists(session.state, item.EventID) {
			return protocol.MutationResult{}, NewFieldError("event_exists", "batch event id was already observed", fmt.Sprintf("items[%d].event_id", index), ErrConflict)
		}
		if _, duplicate := eventIDs[item.EventID]; duplicate {
			return protocol.MutationResult{}, NewFieldError("event_exists", "batch event ids must be unique", fmt.Sprintf("items[%d].event_id", index), ErrConflict)
		}
		eventIDs[item.EventID] = struct{}{}
		actor := session.state.Actors[proposal.ActorID]
		if item.Accepted && request.Tick > maxInt64-actor.ThinkEveryTicks {
			return protocol.MutationResult{}, NewFieldError("tick_overflow", "batch commit tick cannot be scheduled safely", "tick", ErrConflict)
		}
		if item.Accepted && proposal.ProposedGoal != nil {
			if err := validateProposedGoalReservation(
				session.state,
				proposal.ActorID,
				proposal.ProposedGoal.ID,
				proposal.ID,
				fmt.Sprintf("items[%d].proposal_id", index),
			); err != nil {
				return protocol.MutationResult{}, err
			}
		}
		for goalIndex, update := range item.GoalUpdates {
			if !goalExists(actor, update.GoalID) && (proposal.ProposedGoal == nil || proposal.ProposedGoal.ID != update.GoalID) {
				return protocol.MutationResult{}, NewFieldError("unknown_goal", "goal update references an unknown goal", fmt.Sprintf("items[%d].goal_updates[%d].goal_id", index, goalIndex), ErrNotFound)
			}
		}
	}
	event, err := newEvent(session.state, EventBatchCommitted, request.RequestID, batchCommittedPayload{Request: request}, e.now())
	if err != nil {
		return protocol.MutationResult{}, eventEncodeError(err, "could not encode batch commit")
	}
	if err := e.appendAndApply(session, event); err != nil {
		return protocol.MutationResult{}, err
	}
	return mutationResult(session.state, false), nil
}

func (e *Engine) SetActorActivity(request protocol.SetActorActivityRequest) (protocol.MutationResult, error) {
	if err := protocol.ValidateSetActorActivity(request); err != nil {
		return protocol.MutationResult{}, validationError(err)
	}
	session, err := e.session(request.SessionID)
	if err != nil {
		return protocol.MutationResult{}, err
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if !protocol.HasFeature(session.state.Features, protocol.FeatureActorActivity) {
		return protocol.MutationResult{}, NewError("feature_not_enabled", "actor activity requires actor-activity-v1", ErrConflict)
	}
	if receipt, found := session.state.Receipts[request.RequestID]; found {
		if receipt.Kind == EventActivityUpdated {
			return mutationResult(session.state, true), nil
		}
		return protocol.MutationResult{}, requestConflict(request.RequestID)
	}
	if err := worldRevisionAdvanceError(session.state); err != nil {
		return protocol.MutationResult{}, err
	}
	if request.Tick < session.state.Tick {
		return protocol.MutationResult{}, NewFieldError("tick_regressed", "activity tick is older than session state", "tick", ErrConflict)
	}
	for index, update := range request.Updates {
		if _, exists := session.state.Actors[update.ActorID]; !exists {
			return protocol.MutationResult{}, NewFieldError("unknown_actor", "activity update references an unknown actor", fmt.Sprintf("updates[%d].actor_id", index), ErrNotFound)
		}
	}
	event, err := newEvent(session.state, EventActivityUpdated, request.RequestID, activityUpdatedPayload{Request: request}, e.now())
	if err != nil {
		return protocol.MutationResult{}, eventEncodeError(err, "could not encode actor activity")
	}
	if err := e.appendAndApply(session, event); err != nil {
		return protocol.MutationResult{}, err
	}
	return mutationResult(session.state, false), nil
}

func (e *Engine) Arbitrate(request protocol.ArbitrateRequest) (protocol.ArbitrationRecord, bool, error) {
	if err := protocol.ValidateArbitrate(request); err != nil {
		return protocol.ArbitrationRecord{}, false, validationError(err)
	}
	session, err := e.session(request.SessionID)
	if err != nil {
		return protocol.ArbitrationRecord{}, false, err
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if !protocol.HasFeature(session.state.Features, protocol.FeatureArbitration) {
		return protocol.ArbitrationRecord{}, false, NewError("feature_not_enabled", "world arbitration requires arbitration-v1", ErrConflict)
	}
	if receipt, found := session.state.Receipts[request.RequestID]; found {
		if receipt.Kind != EventArbitrated {
			return protocol.ArbitrationRecord{}, false, requestConflict(request.RequestID)
		}
		for _, record := range session.state.Arbitrations {
			if record.ID == receipt.EntityID {
				return record, true, nil
			}
		}
		return protocol.ArbitrationRecord{}, false, NewError("arbitration_missing", "idempotent arbitration is no longer retained", ErrNotFound)
	}
	if request.Tick < session.state.Tick {
		return protocol.ArbitrationRecord{}, false, NewFieldError("tick_regressed", "arbitration tick is older than session state", "tick", ErrConflict)
	}
	proposals := make([]protocol.ActionProposal, 0, len(request.ProposalIDs))
	actors := make(map[string]struct{}, len(request.ProposalIDs))
	for index, proposalID := range request.ProposalIDs {
		proposal, exists := session.state.Proposals[proposalID]
		if !exists {
			return protocol.ArbitrationRecord{}, false, NewFieldError("unknown_proposal", "arbitration references an unknown proposal", fmt.Sprintf("proposal_ids[%d]", index), ErrNotFound)
		}
		if proposal.Status != "pending" {
			return protocol.ArbitrationRecord{}, false, NewFieldError("proposal_resolved", "arbitration requires pending proposals", fmt.Sprintf("proposal_ids[%d]", index), ErrConflict)
		}
		if proposal.BasedOnWorldRevision == 0 || proposal.BasedOnWorldRevision != session.state.WorldRevision {
			return protocol.ArbitrationRecord{}, false, NewError("proposal_stale", "arbitration contains a proposal from another world revision", ErrStale)
		}
		if _, duplicate := actors[proposal.ActorID]; duplicate {
			return protocol.ArbitrationRecord{}, false, NewFieldError("duplicate_actor", "arbitration may contain at most one proposal per actor", "proposal_ids", ErrConflict)
		}
		actors[proposal.ActorID] = struct{}{}
		proposals = append(proposals, proposal)
	}
	decisions := arbitrateProposals(session.state, proposals, request.ExclusiveTargetIDs)
	recordHash, err := hashJSON(struct {
		SessionID     string `json:"session_id"`
		RequestID     string `json:"request_id"`
		WorldRevision uint64 `json:"world_revision"`
	}{request.SessionID, request.RequestID, session.state.WorldRevision})
	if err != nil {
		return protocol.ArbitrationRecord{}, false, NewError("arbitration_id_failed", "could not identify arbitration", err)
	}
	record := protocol.ArbitrationRecord{
		ID: "arbitration." + recordHash[:24], RequestID: request.RequestID, Tick: request.Tick,
		BasedOnWorldRevision: session.state.WorldRevision, CreatedRevision: session.state.Revision + 1,
		Decisions: decisions,
	}
	event, err := newEvent(session.state, EventArbitrated, request.RequestID, arbitratedPayload{Record: record}, e.now())
	if err != nil {
		return protocol.ArbitrationRecord{}, false, eventEncodeError(err, "could not encode arbitration")
	}
	if err := e.appendAndApply(session, event); err != nil {
		return protocol.ArbitrationRecord{}, false, err
	}
	return record, false, nil
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
			return protocol.MutationResult{}, eventEncodeError(err, "could not encode restore")
		}
		state, err := e.createAndConfirm(request.SessionID, event)
		if err != nil {
			e.mu.Unlock()
			return protocol.MutationResult{}, err
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
		return protocol.MutationResult{}, eventEncodeError(err, "could not encode restore")
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
	regions := make(map[string]struct{}, len(request.RegionIDs))
	for _, regionID := range request.RegionIDs {
		regions[regionID] = struct{}{}
	}
	agents := make([]protocol.DueAgent, 0)
	for id, actor := range session.state.Actors {
		regionID := ""
		if actor.Activity != nil {
			if actor.Activity.State == "dormant" {
				continue
			}
			regionID = actor.Activity.RegionID
		}
		if len(regions) > 0 {
			if _, included := regions[regionID]; !included {
				continue
			}
		}
		if actor.Enabled && actor.NextThinkTick <= request.Tick {
			agents = append(agents, protocol.DueAgent{ActorID: id, NextThinkTick: actor.NextThinkTick, RegionID: regionID})
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

func arbitrateProposals(state protocol.SessionState, proposals []protocol.ActionProposal, exclusiveTargetIDs []string) []protocol.ArbitrationDecision {
	exclusive := make(map[string]struct{}, len(exclusiveTargetIDs))
	for _, targetID := range exclusiveTargetIDs {
		exclusive[targetID] = struct{}{}
	}
	values := append([]protocol.ActionProposal(nil), proposals...)
	sort.Slice(values, func(i, j int) bool {
		leftPriority := proposalGoalPriority(state.Actors[values[i].ActorID], values[i])
		rightPriority := proposalGoalPriority(state.Actors[values[j].ActorID], values[j])
		if leftPriority != rightPriority {
			return leftPriority > rightPriority
		}
		if values[i].Tick != values[j].Tick {
			return values[i].Tick < values[j].Tick
		}
		if values[i].ActorID != values[j].ActorID {
			return values[i].ActorID < values[j].ActorID
		}
		return values[i].ID < values[j].ID
	})
	claimed := make(map[string]string)
	decisions := make([]protocol.ArbitrationDecision, 0, len(values))
	for _, proposal := range values {
		conflicts := make([]string, 0)
		claims := make([]string, 0)
		for _, targetID := range proposal.Action.TargetIDs {
			if _, isExclusive := exclusive[targetID]; !isExclusive {
				continue
			}
			claims = append(claims, targetID)
			if winnerID, occupied := claimed[targetID]; occupied {
				conflicts = append(conflicts, winnerID)
			}
		}
		conflicts = uniqueSorted(conflicts)
		decision := protocol.ArbitrationDecision{
			ProposalID: proposal.ID, ActorID: proposal.ActorID,
			Status: "selected", Reason: "No higher-priority proposal claimed the same exclusive target.",
		}
		if len(conflicts) > 0 {
			decision.Status = "deferred"
			decision.Reason = "A higher-priority proposal already claimed an exclusive target."
			decision.ConflictingProposalIDs = conflicts
		} else {
			for _, targetID := range claims {
				claimed[targetID] = proposal.ID
			}
		}
		decisions = append(decisions, decision)
	}
	return decisions
}

func proposalGoalPriority(actor protocol.ActorState, proposal protocol.ActionProposal) int {
	for _, goal := range actor.Goals {
		if goal.ID == proposal.GoalID {
			return goal.Priority
		}
	}
	if proposal.ProposedGoal != nil && proposal.ProposedGoal.ID == proposal.GoalID {
		return proposal.ProposedGoal.Priority
	}
	return 0
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func duplicateGoalUpdate(updates []protocol.GoalUpdate) bool {
	seen := make(map[string]struct{}, len(updates))
	for _, update := range updates {
		if _, exists := seen[update.GoalID]; exists {
			return true
		}
		seen[update.GoalID] = struct{}{}
	}
	return false
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
	if len(session.uncertainProposals) > 0 && !isUncertainProposalRetry(session, event) {
		return unresolvedProposalError()
	}
	candidate, err := clone(session.state)
	if err != nil {
		return NewError("state_copy_failed", "could not prepare an isolated state transition", err)
	}
	// JSON cloning intentionally drops empty `omitempty` maps. Reducers require
	// these indexes to be writable even before their first entry is recorded.
	if candidate.Proposals == nil {
		candidate.Proposals = make(map[string]protocol.ActionProposal)
	}
	if candidate.Receipts == nil {
		candidate.Receipts = make(map[string]protocol.RequestReceipt)
	}
	state, err := applyEvent(candidate, event)
	if err != nil {
		return NewError("event_apply_failed", "event could not be applied", err)
	}
	if appendErr := e.store.Append(session.state.SessionID, event); appendErr != nil {
		events, loadErr := e.store.Load(session.state.SessionID)
		if loadErr == nil {
			tail, reconciledState, matched, reconcileErr := reconcileTail(session.state, events, event)
			if reconcileErr != nil {
				if event.Type == EventProposed {
					return NewError(
						"proposal_outcome_unknown",
						"persisted proposal event could not be reconciled; retry the same request id",
						errors.Join(appendErr, reconcileErr),
					)
				}
				return NewError("event_apply_failed", "persisted event could not be reconciled", reconcileErr)
			}
			if matched {
				// Append may report a post-write Sync/Close failure. Standard
				// stores make an exact append idempotent, so retry the persisted
				// bytes to confirm durability. A later client retry can also
				// reconcile the same logical event even though RecordedAt and
				// Hash were regenerated.
				if retryErr := e.store.Append(session.state.SessionID, tail); retryErr == nil {
					session.state = reconciledState
					return nil
				} else {
					if event.Type == EventProposed {
						return NewError(
							"proposal_outcome_unknown",
							"proposal event may be durable but could not be confirmed; retry the same request id",
							errors.Join(appendErr, retryErr),
						)
					}
					return NewError(
						"store_append_failed",
						"event was written but its durable append could not be confirmed",
						errors.Join(appendErr, retryErr),
					)
				}
			}
		}
		if loadErr != nil {
			appendErr = errors.Join(appendErr, loadErr)
			if event.Type == EventProposed {
				return NewError(
					"proposal_outcome_unknown",
					"proposal persistence could not be determined; retry the same request id",
					appendErr,
				)
			}
		}
		return NewError("store_append_failed", "could not persist event", appendErr)
	}
	session.state = state
	return nil
}

func isUncertainProposalRetry(session *managedSession, event protocol.EventRecord) bool {
	for _, uncertain := range session.uncertainProposals {
		if EventRecordsExactlyEqual(uncertain.event, event) {
			return true
		}
	}
	return false
}

func unresolvedProposalError() error {
	return NewError(
		"proposal_outcome_unknown",
		"session has an unresolved proposal append; retry the same proposal request id before mutating it",
		ErrConflict,
	)
}

func (e *Engine) createAndConfirm(
	sessionID string,
	event protocol.EventRecord,
) (protocol.SessionState, error) {
	candidate, applyErr := applyEvent(protocol.SessionState{}, event)
	if applyErr != nil {
		return protocol.SessionState{}, NewError(
			"event_apply_failed",
			"session event could not be applied",
			applyErr,
		)
	}
	createErr := e.store.Create(sessionID, event)
	if createErr == nil {
		return candidate, nil
	}
	events, loadErr := e.store.Load(sessionID)
	if loadErr == nil && len(events) == 1 {
		persisted := events[0]
		sameSequenceAndHash := persisted.Sequence == event.Sequence && persisted.Hash == event.Hash
		if sameSequenceAndHash || eventsLogicallyEqual(persisted, event) {
			reconciled, applyErr := applyEvent(protocol.SessionState{}, persisted)
			if applyErr != nil {
				return protocol.SessionState{}, NewError(
					"event_apply_failed",
					"persisted session event could not be reconciled",
					applyErr,
				)
			}
			if retryErr := e.store.Create(sessionID, persisted); retryErr == nil {
				return reconciled, nil
			} else {
				return protocol.SessionState{}, NewError(
					"store_create_failed",
					"session event was written but its durable create could not be confirmed",
					errors.Join(createErr, retryErr),
				)
			}
		}
	}
	if loadErr != nil {
		createErr = errors.Join(createErr, loadErr)
	}
	return protocol.SessionState{}, NewError("store_create_failed", "could not create session log", createErr)
}

func reconcileTail(
	current protocol.SessionState,
	events []protocol.EventRecord,
	event protocol.EventRecord,
) (protocol.EventRecord, protocol.SessionState, bool, error) {
	if len(events) == 0 {
		return protocol.EventRecord{}, protocol.SessionState{}, false, nil
	}
	tail := events[len(events)-1]
	sameSequenceAndHash := tail.Sequence == event.Sequence && tail.Hash == event.Hash
	if !sameSequenceAndHash && !eventsLogicallyEqual(tail, event) {
		return protocol.EventRecord{}, protocol.SessionState{}, false, nil
	}
	reconciled, err := clone(current)
	if err != nil {
		return protocol.EventRecord{}, protocol.SessionState{}, false, err
	}
	if reconciled.Proposals == nil {
		reconciled.Proposals = make(map[string]protocol.ActionProposal)
	}
	if reconciled.Receipts == nil {
		reconciled.Receipts = make(map[string]protocol.RequestReceipt)
	}
	reconciled, err = applyEvent(reconciled, tail)
	if err != nil {
		return protocol.EventRecord{}, protocol.SessionState{}, false, err
	}
	return tail, reconciled, true, nil
}

// EventRecordsExactlyEqual defines durable Store idempotency for an
// EventRecord. Data is intentionally compared as persisted bytes rather than
// as semantically equivalent JSON.
func EventRecordsExactlyEqual(left, right protocol.EventRecord) bool {
	return left.Sequence == right.Sequence &&
		left.Type == right.Type &&
		left.RequestID == right.RequestID &&
		left.PrevHash == right.PrevHash &&
		left.Hash == right.Hash &&
		left.RecordedAt == right.RecordedAt &&
		bytes.Equal(left.Data, right.Data)
}

func eventsLogicallyEqual(left, right protocol.EventRecord) bool {
	return left.Sequence == right.Sequence &&
		left.Type == right.Type &&
		left.RequestID == right.RequestID &&
		left.PrevHash == right.PrevHash &&
		bytes.Equal(left.Data, right.Data)
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
	for _, proposal := range state.Proposals {
		if proposal.OutcomeEventID == eventID {
			return true
		}
	}
	for _, receipt := range state.Receipts {
		if receipt.Kind == EventObserved && receipt.EntityID == eventID {
			return true
		}
	}
	for _, actor := range state.Actors {
		for _, goal := range actor.Goals {
			if goal.StatusSourceEventID == eventID {
				return true
			}
		}
		for _, memory := range actor.Memories {
			if memory.EventID == eventID {
				return true
			}
		}
		for _, summary := range actor.MemorySummaries {
			if contains(summary.SourceEventIDs, eventID) {
				return true
			}
		}
		for _, proposal := range actor.RecentActions {
			if proposal.OutcomeEventID == eventID {
				return true
			}
		}
		for _, fact := range actor.Beliefs {
			if fact.SourceEventID == eventID {
				return true
			}
		}
		for _, set := range actor.BeliefSets {
			for _, claim := range set.Claims {
				if claim.Fact.SourceEventID == eventID {
					return true
				}
			}
		}
	}
	return false
}

func validateFactVisibility(state protocol.SessionState, facts []protocol.Fact, field string) error {
	for factIndex, fact := range facts {
		for actorIndex, actorID := range fact.Visibility {
			if _, exists := state.Actors[actorID]; !exists {
				return NewFieldError(
					"unknown_actor",
					"fact visibility references an unregistered actor",
					fmt.Sprintf("%s[%d].visibility[%d]", field, factIndex, actorIndex),
					ErrNotFound,
				)
			}
		}
	}
	return nil
}

func worldRevisionAdvanceError(state protocol.SessionState) error {
	if protocol.HasFeature(state.Features, protocol.FeatureArbitration) &&
		state.WorldRevision == ^uint64(0) {
		return NewFieldError(
			"world_revision_overflow",
			"world revision is exhausted",
			"world_revision",
			ErrConflict,
		)
	}
	return nil
}

func goalExists(actor protocol.ActorState, goalID string) bool {
	for _, goal := range actor.Goals {
		if goal.ID == goalID {
			return true
		}
	}
	return false
}

func pendingProposedGoalReserved(
	state protocol.SessionState,
	actorID string,
	goalID string,
	excludedProposalID string,
) bool {
	for proposalID, proposal := range state.Proposals {
		if proposalID == excludedProposalID ||
			proposal.ActorID != actorID ||
			proposal.Status != "pending" ||
			proposal.ProposedGoal == nil {
			continue
		}
		if proposal.ProposedGoal.ID == goalID {
			return true
		}
	}
	return false
}

func validateProposedGoalReservation(
	state protocol.SessionState,
	actorID string,
	goalID string,
	excludedProposalID string,
	field string,
) error {
	actor, exists := state.Actors[actorID]
	if !exists {
		return NewFieldError("unknown_actor", "proposal actor is not registered", field, ErrNotFound)
	}
	if goalExists(actor, goalID) {
		return NewFieldError("goal_exists", "proposed goal is already part of actor state", field, ErrConflict)
	}
	reservedIDs := make(map[string]struct{})
	for proposalID, proposal := range state.Proposals {
		if proposalID == excludedProposalID ||
			proposal.ActorID != actorID ||
			proposal.Status != "pending" ||
			proposal.ProposedGoal == nil {
			continue
		}
		if proposal.ProposedGoal.ID == goalID {
			return NewFieldError("goal_exists", "proposed goal is already reserved by a pending proposal", field, ErrConflict)
		}
		reservedIDs[proposal.ProposedGoal.ID] = struct{}{}
	}
	if len(actor.Goals)+len(reservedIDs)+1 > 32 {
		return NewFieldError(
			"goal_capacity",
			"actor cannot retain more than 32 goals including pending goal reservations",
			field,
			ErrConflict,
		)
	}
	return nil
}

func canRetainAnotherProposal(state protocol.SessionState) bool {
	if len(state.Proposals) < maxProposals {
		return true
	}
	for _, proposal := range state.Proposals {
		if proposal.Status != "pending" {
			return true
		}
	}
	return false
}

func eventEncodeError(err error, message string) error {
	if ErrorCode(err) == "revision_overflow" {
		return err
	}
	return NewError("event_encode_failed", message, err)
}

func validateDraft(request protocol.ProposeRequest, actor protocol.ActorState, draft ProposalDraft) (protocol.ActionSpec, *protocol.Goal, error) {
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
		return protocol.ActionSpec{}, nil, NewFieldError("invalid_policy_output", "policy selected an action outside the candidate list", "action_id", ErrConflict)
	}
	if draft.Stance != "engage" && draft.Stance != "partial" && draft.Stance != "redirect" && draft.Stance != "refuse" && draft.Stance != "wait" {
		return protocol.ActionSpec{}, nil, NewFieldError("invalid_policy_output", "policy returned an unsupported stance", "stance", ErrConflict)
	}
	if err := validatePolicyText("summary", draft.Summary, 500, true); err != nil {
		return protocol.ActionSpec{}, nil, err
	}
	if err := validatePolicyText("rationale", draft.Rationale, 500, true); err != nil {
		return protocol.ActionSpec{}, nil, err
	}
	if draft.PolicySource != "" && !validPolicySource(draft.PolicySource) {
		return protocol.ActionSpec{}, nil, NewFieldError("invalid_policy_output", "policy source is invalid", "policy_source", ErrConflict)
	}
	if len(draft.RecalledMemoryIDs) > 8 {
		return protocol.ActionSpec{}, nil, NewFieldError("invalid_policy_output", "policy recalled too many memories", "recalled_memory_ids", ErrConflict)
	}
	memoryIDs := make(map[string]struct{}, len(actor.Memories)+len(actor.MemorySummaries))
	for _, memory := range actor.Memories {
		memoryIDs[memory.ID] = struct{}{}
	}
	for _, summary := range actor.MemorySummaries {
		memoryIDs[summary.ID] = struct{}{}
	}
	seen := make(map[string]struct{}, len(draft.RecalledMemoryIDs))
	for _, id := range draft.RecalledMemoryIDs {
		if _, exists := memoryIDs[id]; !exists {
			return protocol.ActionSpec{}, nil, NewFieldError("invalid_policy_output", "policy referenced an unknown memory", "recalled_memory_ids", ErrConflict)
		}
		if _, exists := seen[id]; exists {
			return protocol.ActionSpec{}, nil, NewFieldError("invalid_policy_output", "policy repeated a memory id", "recalled_memory_ids", ErrConflict)
		}
		seen[id] = struct{}{}
	}
	var proposedGoal *protocol.Goal
	if draft.GoalID != "" && !goalExists(actor, draft.GoalID) {
		for index := range request.CandidateGoals {
			if request.CandidateGoals[index].ID == draft.GoalID {
				goal := request.CandidateGoals[index]
				proposedGoal = &goal
				break
			}
		}
		if proposedGoal == nil {
			return protocol.ActionSpec{}, nil, NewFieldError("invalid_policy_output", "policy referenced an unknown goal", "goal_id", ErrConflict)
		}
	}
	return selected, proposedGoal, nil
}

func policySource(value string) string {
	if value == "" {
		return "custom"
	}
	return value
}

func validPolicySource(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if !((character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '.' || character == '_' || character == '-') {
			return false
		}
	}
	return true
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
