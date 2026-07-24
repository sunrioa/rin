package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/sunrioa/rin/protocol"
)

type managedSession struct {
	mu                 sync.Mutex
	id                 string
	loaded             bool
	state              protocol.SessionState
	identifiers        protocol.IdentifierHistory
	uncertainMutations map[string]uncertainMutationAppend
	lineageEpoch       uint64

	checkpointMu      sync.Mutex
	checkpointRunning bool
	checkpointPending *checkpointCapture
}

type uncertainMutationAppend struct {
	event       protocol.EventRecord
	requestHash string
}

type sessionLifecycleGate struct {
	mu   sync.Mutex
	refs int
}

type Engine struct {
	mu             sync.RWMutex
	sessions       map[string]*managedSession
	pendingCreates map[string]uncertainMutationAppend
	lifecycleMu    sync.Mutex
	lifecycleGates map[string]*sessionLifecycleGate
	store          Store
	policy         Policy
	now            func() time.Time
}

func Open(store Store, policy Policy) (*Engine, error) {
	if store == nil || policy == nil {
		return nil, errors.New("store and policy are required")
	}
	engine := &Engine{
		sessions:       make(map[string]*managedSession),
		pendingCreates: make(map[string]uncertainMutationAppend),
		lifecycleGates: make(map[string]*sessionLifecycleGate),
		store:          store,
		policy:         policy,
		now:            time.Now,
	}
	ids, err := store.ListSessions()
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	for _, id := range ids {
		if _, exists := engine.sessions[id]; exists {
			return nil, fmt.Errorf("list sessions: duplicate session id %q", id)
		}
		engine.sessions[id] = &managedSession{id: id}
	}
	return engine, nil
}

// VerifyAll eagerly loads and verifies every known Session. Open itself is
// intentionally lazy so one large or damaged Session does not bind startup
// latency or availability for unrelated Sessions.
func (e *Engine) VerifyAll() error {
	e.mu.RLock()
	ids := make([]string, 0, len(e.sessions))
	for id := range e.sessions {
		ids = append(ids, id)
	}
	e.mu.RUnlock()
	sort.Strings(ids)
	for _, id := range ids {
		if err := e.verifySessionFromGenesis(id); err != nil {
			return fmt.Errorf("verify session %s: %w", id, err)
		}
	}
	return nil
}

func (e *Engine) CreateSession(request protocol.CreateSessionRequest) (protocol.MutationResult, error) {
	if err := protocol.ValidateCreateSession(request); err != nil {
		return protocol.MutationResult{}, validationError(err)
	}
	requestHash, err := requestDigest(request)
	if err != nil {
		return protocol.MutationResult{}, NewError("request_encode_failed", "could not identify create request", err)
	}
	unlockLifecycle := e.lockSessionLifecycle(request.SessionID)
	defer unlockLifecycle()

	e.mu.RLock()
	uncertain, uncertainFound := e.pendingCreates[request.SessionID]
	existing, exists := e.sessions[request.SessionID]
	e.mu.RUnlock()
	if uncertainFound {
		if uncertain.event.Type != EventSessionCreated ||
			uncertain.event.RequestID != request.RequestID ||
			uncertain.requestHash != requestHash {
			if uncertain.event.RequestID == request.RequestID {
				return protocol.MutationResult{}, requestConflict(request.RequestID)
			}
			return protocol.MutationResult{}, unresolvedCreateError(request.SessionID)
		}
		state, identifiers, confirmErr := e.createAndConfirm(request.SessionID, uncertain.event)
		if confirmErr != nil {
			return protocol.MutationResult{}, confirmErr
		}
		managed := &managedSession{
			id: request.SessionID, loaded: true, state: state, identifiers: identifiers,
		}
		managed.mu.Lock()
		e.queueCheckpointLocked(managed)
		managed.mu.Unlock()
		e.mu.Lock()
		delete(e.pendingCreates, request.SessionID)
		e.sessions[request.SessionID] = managed
		e.mu.Unlock()
		identity := identifiers.Requests[request.RequestID]
		return mutationResultFromIdentity(state.SessionID, identity, false), nil
	}
	if exists {
		if err := e.ensureLoaded(existing); err != nil {
			return protocol.MutationResult{}, err
		}
		existing.mu.Lock()
		defer existing.mu.Unlock()
		identity, used, lookupErr := identifierRequest(
			existing.identifiers,
			request.RequestID,
			EventSessionCreated,
			requestHash,
		)
		if lookupErr != nil {
			return protocol.MutationResult{}, lookupErr
		}
		if used {
			return mutationResultFromIdentity(existing.state.SessionID, identity, true), nil
		}
		return protocol.MutationResult{}, NewError("session_exists", "session already exists", ErrConflict)
	}
	payload := createdPayload{Request: request, RequestHash: requestHash}
	event, err := newEvent(protocol.SessionState{}, EventSessionCreated, request.RequestID, payload, e.now())
	if err != nil {
		return protocol.MutationResult{}, eventEncodeError(err, "could not encode session event")
	}
	state, identifiers, err := e.createAndConfirm(request.SessionID, event)
	if err != nil {
		if ErrorCode(err) == "mutation_outcome_unknown" {
			e.mu.Lock()
			e.pendingCreates[request.SessionID] = uncertainMutationAppend{
				event: event, requestHash: requestHash,
			}
			e.mu.Unlock()
		}
		return protocol.MutationResult{}, err
	}
	managed := &managedSession{
		id: request.SessionID, loaded: true, state: state, identifiers: identifiers,
	}
	managed.mu.Lock()
	e.queueCheckpointLocked(managed)
	managed.mu.Unlock()
	e.mu.Lock()
	e.sessions[request.SessionID] = managed
	e.mu.Unlock()
	return mutationResultFromIdentity(state.SessionID, identifiers.Requests[request.RequestID], false), nil
}

func (e *Engine) Observe(request protocol.ObserveRequest) (protocol.MutationResult, error) {
	if err := protocol.ValidateObserve(request); err != nil {
		return protocol.MutationResult{}, validationError(err)
	}
	requestHash, err := requestDigest(request)
	if err != nil {
		return protocol.MutationResult{}, NewError("request_encode_failed", "could not identify observation request", err)
	}
	session, err := e.mutationSession(request.SessionID)
	if err != nil {
		return protocol.MutationResult{}, err
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	identity, handled, duplicate, err := e.resolveMutationRetry(
		session,
		request.RequestID,
		EventObserved,
		requestHash,
	)
	if err != nil {
		return protocol.MutationResult{}, err
	}
	if handled {
		return mutationResultFromIdentity(session.state.SessionID, identity, duplicate), nil
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
	if identifierEventExists(session.identifiers, request.EventID) {
		return protocol.MutationResult{}, NewFieldError("event_exists", "event id was already observed", "event_id", ErrConflict)
	}
	event, err := newEvent(
		session.state,
		EventObserved,
		request.RequestID,
		observedPayload{Request: request, RequestHash: requestHash},
		e.now(),
	)
	if err != nil {
		return protocol.MutationResult{}, eventEncodeError(err, "could not encode observation")
	}
	if err := e.appendAndApply(session, event); err != nil {
		return protocol.MutationResult{}, err
	}
	return mutationResultFromIdentity(
		session.state.SessionID,
		session.identifiers.Requests[request.RequestID],
		false,
	), nil
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
	session, err := e.mutationSession(request.SessionID)
	if err != nil {
		return protocol.ActionProposal{}, false, err
	}
	session.mu.Lock()
	identity, handled, duplicate, err := e.resolveMutationRetry(
		session,
		request.RequestID,
		EventProposed,
		requestHash,
	)
	if err != nil {
		session.mu.Unlock()
		return protocol.ActionProposal{}, false, err
	}
	if handled {
		proposal, resultErr := proposalFromIdentity(identity)
		session.mu.Unlock()
		return proposal, duplicate, resultErr
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
	baseLineageEpoch := session.lineageEpoch
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
	selected, proposedGoal, boundaryID, err := validateDraft(requestSnapshot, validationActor, draft)
	if err != nil {
		return protocol.ActionProposal{}, false, err
	}
	summary, rationale := playerFacingProposalText(selected, draft.Stance)

	session.mu.Lock()
	defer session.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return protocol.ActionProposal{}, false, NewError("proposal_canceled", "proposal request was canceled", err)
	}
	identity, handled, duplicate, err = e.resolveMutationRetry(
		session,
		request.RequestID,
		EventProposed,
		requestHash,
	)
	if err != nil {
		return protocol.ActionProposal{}, false, err
	}
	if handled {
		proposal, resultErr := proposalFromIdentity(identity)
		return proposal, duplicate, resultErr
	}
	worldChanged := arbitrationEnabled && session.state.WorldRevision != baseWorldRevision
	legacyChanged := !arbitrationEnabled && (session.state.Revision != baseRevision || session.state.HeadHash != baseHash)
	if session.lineageEpoch != baseLineageEpoch || worldChanged || legacyChanged {
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
		Summary:              summary,
		Rationale:            rationale,
		PolicySource:         policySource(draft.PolicySource),
		RecalledMemoryIDs:    append([]string(nil), draft.RecalledMemoryIDs...),
		GoalID:               draft.GoalID,
		BoundaryID:           boundaryID,
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
		return protocol.ActionProposal{}, false, err
	}
	result, resultErr := proposalFromIdentity(session.identifiers.Requests[request.RequestID])
	return result, false, resultErr
}

func (e *Engine) Commit(request protocol.CommitRequest) (protocol.MutationResult, error) {
	if err := protocol.ValidateCommit(request); err != nil {
		return protocol.MutationResult{}, validationError(err)
	}
	requestHash, err := requestDigest(request)
	if err != nil {
		return protocol.MutationResult{}, NewError("request_encode_failed", "could not identify commit request", err)
	}
	session, err := e.mutationSession(request.SessionID)
	if err != nil {
		return protocol.MutationResult{}, err
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	identity, handled, duplicate, err := e.resolveMutationRetry(
		session,
		request.RequestID,
		EventCommitted,
		requestHash,
	)
	if err != nil {
		return protocol.MutationResult{}, err
	}
	if handled {
		return mutationResultFromIdentity(session.state.SessionID, identity, duplicate), nil
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
	if identifierEventExists(session.identifiers, request.EventID) {
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
	event, err := newEvent(
		session.state,
		EventCommitted,
		request.RequestID,
		committedPayload{Request: request, RequestHash: requestHash},
		e.now(),
	)
	if err != nil {
		return protocol.MutationResult{}, eventEncodeError(err, "could not encode action commit")
	}
	if err := e.appendAndApply(session, event); err != nil {
		return protocol.MutationResult{}, err
	}
	return mutationResultFromIdentity(
		session.state.SessionID,
		session.identifiers.Requests[request.RequestID],
		false,
	), nil
}

func (e *Engine) CommitBatch(request protocol.BatchCommitRequest) (protocol.MutationResult, error) {
	if err := protocol.ValidateBatchCommit(request); err != nil {
		return protocol.MutationResult{}, validationError(err)
	}
	requestHash, err := requestDigest(request)
	if err != nil {
		return protocol.MutationResult{}, NewError("request_encode_failed", "could not identify batch commit request", err)
	}
	session, err := e.mutationSession(request.SessionID)
	if err != nil {
		return protocol.MutationResult{}, err
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	identity, handled, duplicate, err := e.resolveMutationRetry(
		session,
		request.RequestID,
		EventBatchCommitted,
		requestHash,
	)
	if err != nil {
		return protocol.MutationResult{}, err
	}
	if handled {
		return mutationResultFromIdentity(session.state.SessionID, identity, duplicate), nil
	}
	if !protocol.HasFeature(session.state.Features, protocol.FeatureArbitration) {
		return protocol.MutationResult{}, NewError("feature_not_enabled", "batch commit requires arbitration-v1", ErrConflict)
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
		if identifierEventExists(session.identifiers, item.EventID) {
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
	event, err := newEvent(
		session.state,
		EventBatchCommitted,
		request.RequestID,
		batchCommittedPayload{Request: request, RequestHash: requestHash},
		e.now(),
	)
	if err != nil {
		return protocol.MutationResult{}, eventEncodeError(err, "could not encode batch commit")
	}
	if err := e.appendAndApply(session, event); err != nil {
		return protocol.MutationResult{}, err
	}
	return mutationResultFromIdentity(
		session.state.SessionID,
		session.identifiers.Requests[request.RequestID],
		false,
	), nil
}

func (e *Engine) SetActorActivity(request protocol.SetActorActivityRequest) (protocol.MutationResult, error) {
	if err := protocol.ValidateSetActorActivity(request); err != nil {
		return protocol.MutationResult{}, validationError(err)
	}
	requestHash, err := requestDigest(request)
	if err != nil {
		return protocol.MutationResult{}, NewError("request_encode_failed", "could not identify activity request", err)
	}
	session, err := e.mutationSession(request.SessionID)
	if err != nil {
		return protocol.MutationResult{}, err
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	identity, handled, duplicate, err := e.resolveMutationRetry(
		session,
		request.RequestID,
		EventActivityUpdated,
		requestHash,
	)
	if err != nil {
		return protocol.MutationResult{}, err
	}
	if handled {
		return mutationResultFromIdentity(session.state.SessionID, identity, duplicate), nil
	}
	if !protocol.HasFeature(session.state.Features, protocol.FeatureActorActivity) {
		return protocol.MutationResult{}, NewError("feature_not_enabled", "actor activity requires actor-activity-v1", ErrConflict)
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
	event, err := newEvent(
		session.state,
		EventActivityUpdated,
		request.RequestID,
		activityUpdatedPayload{Request: request, RequestHash: requestHash},
		e.now(),
	)
	if err != nil {
		return protocol.MutationResult{}, eventEncodeError(err, "could not encode actor activity")
	}
	if err := e.appendAndApply(session, event); err != nil {
		return protocol.MutationResult{}, err
	}
	return mutationResultFromIdentity(
		session.state.SessionID,
		session.identifiers.Requests[request.RequestID],
		false,
	), nil
}

func (e *Engine) Arbitrate(request protocol.ArbitrateRequest) (protocol.ArbitrationRecord, bool, error) {
	if err := protocol.ValidateArbitrate(request); err != nil {
		return protocol.ArbitrationRecord{}, false, validationError(err)
	}
	requestHash, err := requestDigest(request)
	if err != nil {
		return protocol.ArbitrationRecord{}, false, NewError("request_encode_failed", "could not identify arbitration request", err)
	}
	session, err := e.mutationSession(request.SessionID)
	if err != nil {
		return protocol.ArbitrationRecord{}, false, err
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	identity, handled, duplicate, err := e.resolveMutationRetry(
		session,
		request.RequestID,
		EventArbitrated,
		requestHash,
	)
	if err != nil {
		return protocol.ArbitrationRecord{}, false, err
	}
	if handled {
		record, resultErr := arbitrationFromIdentity(identity)
		return record, duplicate, resultErr
	}
	if !protocol.HasFeature(session.state.Features, protocol.FeatureArbitration) {
		return protocol.ArbitrationRecord{}, false, NewError("feature_not_enabled", "world arbitration requires arbitration-v1", ErrConflict)
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
	event, err := newEvent(
		session.state,
		EventArbitrated,
		request.RequestID,
		arbitratedPayload{Record: record, RequestHash: requestHash},
		e.now(),
	)
	if err != nil {
		return protocol.ArbitrationRecord{}, false, eventEncodeError(err, "could not encode arbitration")
	}
	if err := e.appendAndApply(session, event); err != nil {
		return protocol.ArbitrationRecord{}, false, err
	}
	result, resultErr := arbitrationFromIdentity(session.identifiers.Requests[request.RequestID])
	return result, false, resultErr
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
	state, err := clone(session.state)
	if err != nil {
		return protocol.SessionState{}, err
	}
	canonicalizeStateProposalPresentation(&state)
	return state, nil
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
	snapshot, err := snapshotWithIdentifiers(session.state, session.identifiers)
	session.mu.Unlock()
	if err != nil {
		if ErrorCode(err) == "snapshot_too_large" {
			return protocol.Snapshot{}, err
		}
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
	if request.ExpectedBinding != request.Snapshot.State.Binding {
		return protocol.MutationResult{}, NewFieldError(
			"binding_mismatch",
			"snapshot does not match the caller's expected game content",
			"expected_binding",
			ErrConflict,
		)
	}
	requestHash, err := requestDigest(request)
	if err != nil {
		return protocol.MutationResult{}, NewError("request_encode_failed", "could not identify restore request", err)
	}
	legacyRequestHash, err := legacyRestoreRequestDigest(request)
	if err != nil {
		return protocol.MutationResult{}, NewError("request_encode_failed", "could not identify legacy restore request", err)
	}
	importedIdentifiers, err := identifiersForSnapshot(request.Snapshot)
	if err != nil {
		return protocol.MutationResult{}, NewError("invalid_snapshot", "could not read snapshot identifier history", err)
	}
	if _, collision := importedIdentifiers.Requests[request.RequestID]; collision {
		return protocol.MutationResult{}, requestConflict(request.RequestID)
	}
	unlockLifecycle := e.lockSessionLifecycle(request.SessionID)
	defer unlockLifecycle()
	e.mu.RLock()
	uncertain, uncertainFound := e.pendingCreates[request.SessionID]
	session, exists := e.sessions[request.SessionID]
	e.mu.RUnlock()
	if uncertainFound {
		if uncertain.event.Type != EventSessionRestored ||
			uncertain.event.RequestID != request.RequestID ||
			uncertain.requestHash != requestHash {
			if uncertain.event.RequestID == request.RequestID {
				return protocol.MutationResult{}, requestConflict(request.RequestID)
			}
			return protocol.MutationResult{}, unresolvedCreateError(request.SessionID)
		}
		state, identifiers, confirmErr := e.createAndConfirm(request.SessionID, uncertain.event)
		if confirmErr != nil {
			return protocol.MutationResult{}, confirmErr
		}
		managed := &managedSession{
			id: request.SessionID, loaded: true,
			state: state, identifiers: identifiers, lineageEpoch: 1,
		}
		managed.mu.Lock()
		e.queueCheckpointLocked(managed)
		managed.mu.Unlock()
		e.mu.Lock()
		delete(e.pendingCreates, request.SessionID)
		e.sessions[request.SessionID] = managed
		e.mu.Unlock()
		return mutationResultFromIdentity(
			state.SessionID,
			identifiers.Requests[request.RequestID],
			false,
		), nil
	}
	if !exists {
		if _, mergeErr := mergeIdentifierHistories(newIdentifierHistory(true), importedIdentifiers); mergeErr != nil {
			return protocol.MutationResult{}, NewError(
				"identifier_history_conflict",
				"snapshot identifier history conflicts with the target lineage",
				errors.Join(ErrConflict, mergeErr),
			)
		}
		event, err := newEvent(
			protocol.SessionState{},
			EventSessionRestored,
			request.RequestID,
			restoredPayload{
				Snapshot:        request.Snapshot,
				ExpectedBinding: &request.ExpectedBinding,
				RequestHash:     requestHash,
			},
			e.now(),
		)
		if err != nil {
			return protocol.MutationResult{}, eventEncodeError(err, "could not encode restore")
		}
		state, identifiers, err := e.createAndConfirm(request.SessionID, event)
		if err != nil {
			if ErrorCode(err) == "mutation_outcome_unknown" {
				e.mu.Lock()
				e.pendingCreates[request.SessionID] = uncertainMutationAppend{
					event: event, requestHash: requestHash,
				}
				e.mu.Unlock()
			}
			return protocol.MutationResult{}, err
		}
		managed := &managedSession{
			id: request.SessionID, loaded: true,
			state: state, identifiers: identifiers, lineageEpoch: 1,
		}
		managed.mu.Lock()
		e.queueCheckpointLocked(managed)
		managed.mu.Unlock()
		e.mu.Lock()
		e.sessions[request.SessionID] = managed
		e.mu.Unlock()
		return mutationResultFromIdentity(
			state.SessionID,
			identifiers.Requests[request.RequestID],
			false,
		), nil
	}
	if err := e.ensureLoaded(session); err != nil {
		return protocol.MutationResult{}, err
	}

	session.mu.Lock()
	defer session.mu.Unlock()
	if session.state.Binding != request.ExpectedBinding {
		return protocol.MutationResult{}, NewFieldError(
			"binding_mismatch",
			"expected binding does not match the existing session",
			"expected_binding",
			ErrConflict,
		)
	}
	identity, handled, duplicate, err := e.resolveMutationRetry(
		session,
		request.RequestID,
		EventSessionRestored,
		requestHash,
		legacyRequestHash,
	)
	if err != nil {
		return protocol.MutationResult{}, err
	}
	if handled {
		return mutationResultFromIdentity(session.state.SessionID, identity, duplicate), nil
	}
	if _, mergeErr := mergeIdentifierHistories(session.identifiers, importedIdentifiers); mergeErr != nil {
		return protocol.MutationResult{}, NewError(
			"identifier_history_conflict",
			"snapshot identifier history conflicts with the current lineage",
			errors.Join(ErrConflict, mergeErr),
		)
	}
	event, err := newEvent(
		session.state,
		EventSessionRestored,
		request.RequestID,
		restoredPayload{
			Snapshot:        request.Snapshot,
			ExpectedBinding: &request.ExpectedBinding,
			RequestHash:     requestHash,
		},
		e.now(),
	)
	if err != nil {
		return protocol.MutationResult{}, eventEncodeError(err, "could not encode restore")
	}
	if err := e.appendAndApply(session, event); err != nil {
		return protocol.MutationResult{}, err
	}
	return mutationResultFromIdentity(
		session.state.SessionID,
		session.identifiers.Requests[request.RequestID],
		false,
	), nil
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
	if err := e.ensureLoaded(session); err != nil {
		return nil, err
	}
	return session, nil
}

// lockSessionLifecycle serializes Create and Restore lifecycle decisions for
// one Session ID without making unrelated Session creation or recovery wait.
// Reference counting keeps a gate reachable until its final waiter releases
// it, so deleting an idle entry can never split one ID across two mutexes.
func (e *Engine) lockSessionLifecycle(id string) func() {
	e.lifecycleMu.Lock()
	gate := e.lifecycleGates[id]
	if gate == nil {
		gate = &sessionLifecycleGate{}
		e.lifecycleGates[id] = gate
	}
	gate.refs++
	e.lifecycleMu.Unlock()

	gate.mu.Lock()
	return func() {
		gate.mu.Unlock()

		e.lifecycleMu.Lock()
		gate.refs--
		if gate.refs == 0 && e.lifecycleGates[id] == gate {
			delete(e.lifecycleGates, id)
		}
		e.lifecycleMu.Unlock()
	}
}

func (e *Engine) mutationSession(id string) (*managedSession, error) {
	e.mu.RLock()
	session, exists := e.sessions[id]
	_, pending := e.pendingCreates[id]
	e.mu.RUnlock()
	if exists {
		if err := e.ensureLoaded(session); err != nil {
			return nil, err
		}
		return session, nil
	}
	if pending {
		return nil, unresolvedCreateError(id)
	}
	return nil, NewFieldError("session_not_found", "session does not exist", "session_id", ErrNotFound)
}

// resolveMutationRetry must be called with session.mu held. It checks the
// permanent event-derived index first, then gives an uncertain append exactly
// one safe path to confirmation. A different request cannot overtake a
// possibly durable tail.
func (e *Engine) resolveMutationRetry(
	session *managedSession,
	requestID, kind, requestHash string,
	compatibleRequestHashes ...string,
) (protocol.RequestIdentity, bool, bool, error) {
	identity, used, err := identifierRequest(session.identifiers, requestID, kind, requestHash)
	if err != nil {
		for _, compatibleHash := range compatibleRequestHashes {
			if compatibleHash == requestHash {
				continue
			}
			compatibleIdentity, compatibleUsed, compatibleErr := identifierRequest(
				session.identifiers,
				requestID,
				kind,
				compatibleHash,
			)
			if compatibleErr == nil && compatibleUsed {
				return compatibleIdentity, true, true, nil
			}
		}
		return protocol.RequestIdentity{}, false, false, err
	}
	if used {
		return identity, true, true, nil
	}
	if uncertain, found := session.uncertainMutations[requestID]; found {
		if uncertain.event.Type != kind || uncertain.requestHash != requestHash {
			return protocol.RequestIdentity{}, false, false, requestConflict(requestID)
		}
		if err := e.appendAndApply(session, uncertain.event); err != nil {
			return protocol.RequestIdentity{}, false, false, err
		}
		delete(session.uncertainMutations, requestID)
		return session.identifiers.Requests[requestID], true, false, nil
	}
	if len(session.uncertainMutations) > 0 {
		return protocol.RequestIdentity{}, false, false, unresolvedMutationError(session)
	}
	return protocol.RequestIdentity{}, false, false, nil
}

func (e *Engine) appendAndApply(session *managedSession, event protocol.EventRecord) error {
	if len(session.uncertainMutations) > 0 && !isUncertainMutationRetry(session, event) {
		return unresolvedMutationError(session)
	}
	candidate, err := clone(session.state)
	if err != nil {
		return NewError("state_copy_failed", "could not prepare an isolated state transition", err)
	}
	// JSON cloning intentionally drops empty `omitempty` maps. Reducers require
	// these indexes to be writable even before their first entry is recorded.
	normalizeWritableState(&candidate)
	state, err := applyEvent(candidate, event)
	if err != nil {
		return NewError("event_apply_failed", "event could not be applied", err)
	}
	identifierDelta, err := prepareIdentifierEvent(session.identifiers, event)
	if err != nil {
		return NewError("event_apply_failed", "event identifiers could not be applied", err)
	}
	if appendErr := e.store.Append(session.state.SessionID, event); appendErr != nil {
		events, loadErr := e.store.Load(session.state.SessionID)
		if loadErr == nil {
			tail, reconciledState, reconciledIdentifiers, matched, reconcileErr := reconcileTail(
				session.state,
				session.identifiers,
				events,
				event,
			)
			if reconcileErr != nil {
				return e.rememberUncertainMutation(
					session,
					event,
					errors.Join(appendErr, reconcileErr),
				)
			}
			if matched {
				// Append may report a post-write Sync/Close failure. Standard
				// stores make an exact append idempotent, so retry the persisted
				// bytes to confirm durability. A later client retry can also
				// reconcile the same logical event even though RecordedAt and
				// Hash were regenerated.
				if retryErr := e.store.Append(session.state.SessionID, tail); retryErr == nil {
					session.state = reconciledState
					applyIdentifierDelta(&session.identifiers, reconciledIdentifiers)
					advanceLineageEpoch(session, tail)
					e.maybeSaveCheckpoint(session, tail)
					return nil
				} else {
					return e.rememberUncertainMutation(
						session,
						event,
						errors.Join(appendErr, retryErr),
					)
				}
			}
			if logMatchesStateTail(events, session.state) {
				return NewError("store_append_failed", "could not persist event", appendErr)
			}
		}
		if loadErr != nil {
			appendErr = errors.Join(appendErr, loadErr)
		}
		return e.rememberUncertainMutation(session, event, appendErr)
	}
	session.state = state
	applyIdentifierDelta(&session.identifiers, identifierDelta)
	advanceLineageEpoch(session, event)
	e.maybeSaveCheckpoint(session, event)
	return nil
}

func advanceLineageEpoch(session *managedSession, event protocol.EventRecord) {
	if event.Type == EventSessionRestored && session.lineageEpoch != ^uint64(0) {
		session.lineageEpoch++
	}
}

func restoredEventCount(events []protocol.EventRecord) uint64 {
	var count uint64
	for _, event := range events {
		if event.Type == EventSessionRestored && count != ^uint64(0) {
			count++
		}
	}
	return count
}

func isUncertainMutationRetry(session *managedSession, event protocol.EventRecord) bool {
	for _, uncertain := range session.uncertainMutations {
		if EventRecordsExactlyEqual(uncertain.event, event) {
			return true
		}
	}
	return false
}

func (e *Engine) rememberUncertainMutation(
	session *managedSession,
	event protocol.EventRecord,
	cause error,
) error {
	identity, _, identityErr := requestIdentityFromEvent(event)
	if identityErr != nil {
		cause = errors.Join(cause, identityErr)
	}
	if session.uncertainMutations == nil {
		session.uncertainMutations = make(map[string]uncertainMutationAppend)
	}
	session.uncertainMutations[event.RequestID] = uncertainMutationAppend{
		event: event, requestHash: identity.RequestHash,
	}
	if event.Type == EventProposed {
		return NewError(
			"proposal_outcome_unknown",
			"proposal event may be durable but could not be confirmed; retry the same request id and payload",
			cause,
		)
	}
	return NewError(
		"mutation_outcome_unknown",
		"mutation may be durable but could not be confirmed; retry the same request id and payload",
		cause,
	)
}

func unresolvedMutationError(session *managedSession) error {
	for _, uncertain := range session.uncertainMutations {
		if uncertain.event.Type == EventProposed {
			return NewError(
				"proposal_outcome_unknown",
				"session has an unresolved proposal append; retry the same proposal request id and payload before mutating it",
				ErrConflict,
			)
		}
	}
	return NewError(
		"mutation_outcome_unknown",
		"session has an unresolved mutation append; retry the same request id and payload before mutating it",
		ErrConflict,
	)
}

func unresolvedCreateError(sessionID string) error {
	return NewFieldError(
		"mutation_outcome_unknown",
		"session creation or restore has an unresolved durable outcome; retry the same request",
		"session_id",
		fmt.Errorf("%w: %s", ErrConflict, sessionID),
	)
}

func logMatchesStateTail(events []protocol.EventRecord, state protocol.SessionState) bool {
	if len(events) == 0 {
		return state.Revision == 0 && state.HeadHash == ""
	}
	tail := events[len(events)-1]
	return tail.Sequence == state.Revision && tail.Hash == state.HeadHash
}

func (e *Engine) createAndConfirm(
	sessionID string,
	event protocol.EventRecord,
) (protocol.SessionState, protocol.IdentifierHistory, error) {
	candidate, applyErr := applyEvent(protocol.SessionState{}, event)
	if applyErr != nil {
		return protocol.SessionState{}, protocol.IdentifierHistory{}, NewError(
			"event_apply_failed",
			"session event could not be applied",
			applyErr,
		)
	}
	identifiers := newIdentifierHistory(true)
	identifierDelta, identityErr := prepareIdentifierEvent(identifiers, event)
	if identityErr != nil {
		return protocol.SessionState{}, protocol.IdentifierHistory{}, NewError(
			"event_apply_failed",
			"session event identifiers could not be applied",
			identityErr,
		)
	}
	applyIdentifierDelta(&identifiers, identifierDelta)
	createErr := e.store.Create(sessionID, event)
	if createErr == nil {
		return candidate, identifiers, nil
	}
	events, loadErr := e.store.Load(sessionID)
	if loadErr == nil && len(events) == 1 {
		persisted := events[0]
		sameSequenceAndHash := persisted.Sequence == event.Sequence && persisted.Hash == event.Hash
		if sameSequenceAndHash || eventsLogicallyEqual(persisted, event) {
			reconciled, applyErr := applyEvent(protocol.SessionState{}, persisted)
			if applyErr != nil {
				return protocol.SessionState{}, protocol.IdentifierHistory{}, NewError(
					"event_apply_failed",
					"persisted session event could not be reconciled",
					applyErr,
				)
			}
			reconciledIdentifiers := newIdentifierHistory(true)
			reconciledIdentifierDelta, identityErr := prepareIdentifierEvent(reconciledIdentifiers, persisted)
			if identityErr != nil {
				return protocol.SessionState{}, protocol.IdentifierHistory{}, NewError(
					"event_apply_failed",
					"persisted session identifiers could not be reconciled",
					identityErr,
				)
			}
			applyIdentifierDelta(&reconciledIdentifiers, reconciledIdentifierDelta)
			if retryErr := e.store.Create(sessionID, persisted); retryErr == nil {
				return reconciled, reconciledIdentifiers, nil
			} else {
				return protocol.SessionState{}, protocol.IdentifierHistory{}, NewError(
					"mutation_outcome_unknown",
					"session event may be durable but could not be confirmed; retry the same request",
					errors.Join(createErr, retryErr),
				)
			}
		}
		return protocol.SessionState{}, protocol.IdentifierHistory{}, NewError(
			"store_create_failed",
			"could not create session log because a different log already exists",
			errors.Join(createErr, ErrConflict),
		)
	}
	if loadErr != nil {
		if errors.Is(loadErr, ErrNotFound) {
			return protocol.SessionState{}, protocol.IdentifierHistory{}, NewError(
				"store_create_failed",
				"session event was not persisted",
				createErr,
			)
		}
		createErr = errors.Join(createErr, loadErr)
	}
	return protocol.SessionState{}, protocol.IdentifierHistory{}, NewError(
		"mutation_outcome_unknown",
		"session event may be durable but could not be confirmed; retry the same request",
		createErr,
	)
}

func reconcileTail(
	current protocol.SessionState,
	currentIdentifiers protocol.IdentifierHistory,
	events []protocol.EventRecord,
	event protocol.EventRecord,
) (
	protocol.EventRecord,
	protocol.SessionState,
	identifierEventDelta,
	bool,
	error,
) {
	if len(events) == 0 {
		return protocol.EventRecord{}, protocol.SessionState{}, identifierEventDelta{}, false, nil
	}
	tail := events[len(events)-1]
	sameSequenceAndHash := tail.Sequence == event.Sequence && tail.Hash == event.Hash
	if !sameSequenceAndHash && !eventsLogicallyEqual(tail, event) {
		return protocol.EventRecord{}, protocol.SessionState{}, identifierEventDelta{}, false, nil
	}
	reconciled, err := clone(current)
	if err != nil {
		return protocol.EventRecord{}, protocol.SessionState{}, identifierEventDelta{}, false, err
	}
	normalizeWritableState(&reconciled)
	reconciled, err = applyEvent(reconciled, tail)
	if err != nil {
		return protocol.EventRecord{}, protocol.SessionState{}, identifierEventDelta{}, false, err
	}
	reconciledIdentifiers, err := prepareIdentifierEvent(currentIdentifiers, tail)
	if err != nil {
		return protocol.EventRecord{}, protocol.SessionState{}, identifierEventDelta{}, false, err
	}
	return tail, reconciled, reconciledIdentifiers, true, nil
}

func normalizeWritableState(state *protocol.SessionState) {
	if state.Actors == nil {
		state.Actors = make(map[string]protocol.ActorState)
	}
	if state.Proposals == nil {
		state.Proposals = make(map[string]protocol.ActionProposal)
	}
	if state.Receipts == nil {
		state.Receipts = make(map[string]protocol.RequestReceipt)
	}
	for actorID, actor := range state.Actors {
		if actor.Beliefs == nil {
			actor.Beliefs = make(map[string]protocol.Fact)
		}
		if actor.BeliefSets == nil {
			actor.BeliefSets = make(map[string]protocol.BeliefSet)
		}
		state.Actors[actorID] = actor
	}
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

func validateDraft(request protocol.ProposeRequest, actor protocol.ActorState, draft ProposalDraft) (protocol.ActionSpec, *protocol.Goal, string, error) {
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
		return protocol.ActionSpec{}, nil, "", NewFieldError("invalid_policy_output", "policy selected an action outside the candidate list", "action_id", ErrConflict)
	}
	if draft.Stance != "engage" && draft.Stance != "partial" && draft.Stance != "redirect" && draft.Stance != "refuse" && draft.Stance != "wait" {
		return protocol.ActionSpec{}, nil, "", NewFieldError("invalid_policy_output", "policy returned an unsupported stance", "stance", ErrConflict)
	}
	if draft.PolicySource != "" && !validPolicySource(draft.PolicySource) {
		return protocol.ActionSpec{}, nil, "", NewFieldError("invalid_policy_output", "policy source is invalid", "policy_source", ErrConflict)
	}
	if len(draft.RecalledMemoryIDs) > 8 {
		return protocol.ActionSpec{}, nil, "", NewFieldError("invalid_policy_output", "policy recalled too many memories", "recalled_memory_ids", ErrConflict)
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
			return protocol.ActionSpec{}, nil, "", NewFieldError("invalid_policy_output", "policy referenced an unknown memory", "recalled_memory_ids", ErrConflict)
		}
		if _, exists := seen[id]; exists {
			return protocol.ActionSpec{}, nil, "", NewFieldError("invalid_policy_output", "policy repeated a memory id", "recalled_memory_ids", ErrConflict)
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
			return protocol.ActionSpec{}, nil, "", NewFieldError("invalid_policy_output", "policy referenced an unknown goal", "goal_id", ErrConflict)
		}
	}
	boundary, boundaryTriggered := triggeredActorBoundary(actor.Boundaries, request.Tags)
	if !boundaryTriggered {
		if draft.BoundaryID != "" {
			return protocol.ActionSpec{}, nil, "", NewFieldError("invalid_policy_output", "policy referenced a boundary that was not triggered", "boundary_id", ErrConflict)
		}
		return selected, proposedGoal, "", nil
	}
	if selected.ID != boundary.Response && selected.Kind != boundary.Response {
		return protocol.ActionSpec{}, nil, "", NewFieldError("invalid_policy_output", "policy selected an action that does not satisfy the triggered boundary", "action_id", ErrConflict)
	}
	if draft.Stance != boundary.Response {
		return protocol.ActionSpec{}, nil, "", NewFieldError("invalid_policy_output", "policy stance does not match the triggered boundary response", "stance", ErrConflict)
	}
	if draft.BoundaryID != "" && draft.BoundaryID != boundary.ID {
		return protocol.ActionSpec{}, nil, "", NewFieldError("invalid_policy_output", "policy referenced the wrong triggered boundary", "boundary_id", ErrConflict)
	}
	return selected, proposedGoal, boundary.ID, nil
}

func triggeredActorBoundary(boundaries []protocol.Boundary, tags []string) (protocol.Boundary, bool) {
	tagSet := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		tagSet[tag] = struct{}{}
	}
	for _, boundary := range boundaries {
		for _, trigger := range boundary.TriggerTags {
			if _, exists := tagSet[trigger]; exists {
				return boundary, true
			}
		}
	}
	return protocol.Boundary{}, false
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
