package runtime

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/sunrioa/rin/protocol"
)

const (
	maxMemories      = 128
	maxRecentActions = 32
	maxGoals         = 32
	maxBeliefs       = 256
	maxRecallCount   = 1_000_000
	maxProposals     = 64
	maxReceipts      = 1024
	maxArbitrations  = 32
	maxInt64         = int64(1<<63 - 1)
	minInt64         = int64(-1 << 63)
)

type createdPayload struct {
	Request protocol.CreateSessionRequest `json:"request"`
}

type observedPayload struct {
	Request protocol.ObserveRequest `json:"request"`
}

type proposedPayload struct {
	Proposal    protocol.ActionProposal `json:"proposal"`
	RequestHash string                  `json:"request_hash,omitempty"`
}

type committedPayload struct {
	Request protocol.CommitRequest `json:"request"`
}

type batchCommittedPayload struct {
	Request protocol.BatchCommitRequest `json:"request"`
}

type activityUpdatedPayload struct {
	Request protocol.SetActorActivityRequest `json:"request"`
}

type arbitratedPayload struct {
	Record protocol.ArbitrationRecord `json:"record"`
}

type restoredPayload struct {
	Snapshot protocol.Snapshot `json:"snapshot"`
}

func applyEvent(state protocol.SessionState, event protocol.EventRecord) (protocol.SessionState, error) {
	if err := verifyEvent(state, event); err != nil {
		return protocol.SessionState{}, err
	}
	var err error
	switch event.Type {
	case EventSessionCreated:
		state, err = applyCreated(state, event)
	case EventObserved:
		err = applyObserved(&state, event)
	case EventProposed:
		err = applyProposed(&state, event)
	case EventCommitted:
		err = applyCommitted(&state, event)
	case EventBatchCommitted:
		err = applyBatchCommitted(&state, event)
	case EventActivityUpdated:
		err = applyActivityUpdated(&state, event)
	case EventArbitrated:
		err = applyArbitrated(&state, event)
	case EventSessionRestored:
		state, err = applyRestored(state, event)
	default:
		err = fmt.Errorf("%w: unknown event type %q", ErrCorruptLog, event.Type)
	}
	if err != nil {
		return protocol.SessionState{}, err
	}
	state.Revision = event.Sequence
	state.HeadHash = event.Hash
	trimReceipts(&state)
	if err := protocol.ValidateSessionState(state); err != nil {
		return protocol.SessionState{}, fmt.Errorf(
			"%w: invalid state after %s: %v",
			ErrCorruptLog,
			event.Type,
			err,
		)
	}
	return state, nil
}

func applyCreated(state protocol.SessionState, event protocol.EventRecord) (protocol.SessionState, error) {
	if state.Revision != 0 || state.SessionID != "" {
		return protocol.SessionState{}, fmt.Errorf("%w: session.created is not first", ErrCorruptLog)
	}
	var payload createdPayload
	if err := json.Unmarshal(event.Data, &payload); err != nil {
		return protocol.SessionState{}, fmt.Errorf("%w: decode create payload: %v", ErrCorruptLog, err)
	}
	request := payload.Request
	if err := protocol.ValidateCreateSession(request); err != nil {
		return protocol.SessionState{}, fmt.Errorf("%w: invalid create payload: %v", ErrCorruptLog, err)
	}
	actors := make(map[string]protocol.ActorState, len(request.Actors))
	for _, seed := range request.Actors {
		if protocol.HasFeature(request.Features, protocol.FeatureOutcomeReporting) {
			seed.Goals = append([]protocol.Goal(nil), seed.Goals...)
			for index := range seed.Goals {
				seed.Goals[index].ProgressAccumulator = int64(seed.Goals[index].Progress)
				seed.Goals[index].StatusExplicit = seed.Goals[index].Status != "active"
			}
		}
		actors[seed.ID] = protocol.ActorState{
			ActorSeed:     seed,
			Beliefs:       make(map[string]protocol.Fact),
			NextThinkTick: 0,
		}
	}
	created := protocol.SessionState{
		ProtocolVersion: protocol.Version,
		SessionID:       request.SessionID,
		Binding:         request.Binding,
		Seed:            request.Seed,
		Features:        append([]string(nil), request.Features...),
		Actors:          actors,
		Proposals:       make(map[string]protocol.ActionProposal),
		Receipts: map[string]protocol.RequestReceipt{
			request.RequestID: {Kind: EventSessionCreated, EntityID: request.SessionID, Revision: event.Sequence},
		},
	}
	if protocol.HasFeature(request.Features, protocol.FeatureArbitration) {
		created.WorldRevision = 1
	}
	return created, nil
}

func applyObserved(state *protocol.SessionState, event protocol.EventRecord) error {
	var payload observedPayload
	if err := json.Unmarshal(event.Data, &payload); err != nil {
		return fmt.Errorf("%w: decode observe payload: %v", ErrCorruptLog, err)
	}
	request := payload.Request
	outcomeReporting := protocol.HasFeature(state.Features, protocol.FeatureOutcomeReporting)
	for _, actorID := range request.ObserverIDs {
		actor, exists := state.Actors[actorID]
		if !exists {
			return fmt.Errorf("%w: unknown observer %q", ErrCorruptLog, actorID)
		}
		memoryID, err := hashJSON(struct {
			SessionID string `json:"session_id"`
			ActorID   string `json:"actor_id"`
			EventID   string `json:"event_id"`
		}{state.SessionID, actorID, request.EventID})
		if err != nil {
			return err
		}
		actor.Memories = append(actor.Memories, protocol.Memory{
			ID:              "memory." + memoryID[:24],
			EventID:         request.EventID,
			Tick:            request.Tick,
			Summary:         request.Summary,
			Quote:           request.Quote,
			Tags:            append([]string(nil), request.Tags...),
			Importance:      request.Importance,
			CreatedRevision: event.Sequence,
		})
		if outcomeReporting {
			sortActorMemories(&actor)
		}
		if protocol.HasFeature(state.Features, protocol.FeatureMemoryArchive) {
			if err := compactActorMemories(state.SessionID, &actor, event.Sequence, state); err != nil {
				return err
			}
		} else if len(actor.Memories) > maxMemories {
			trimActorMemories(state, &actor)
		}
		applyFacts(
			&actor,
			request.Facts,
			request.EventID,
			request.Tick,
			event.Sequence,
			protocol.HasFeature(state.Features, protocol.FeatureBeliefConflicts),
			outcomeReporting,
		)
		state.Actors[actorID] = actor
	}
	if request.Tick > state.Tick {
		state.Tick = request.Tick
	}
	state.Receipts[request.RequestID] = protocol.RequestReceipt{Kind: EventObserved, EntityID: request.EventID, Revision: event.Sequence}
	return advanceWorldRevision(state)
}

func applyProposed(state *protocol.SessionState, event protocol.EventRecord) error {
	var payload proposedPayload
	if err := json.Unmarshal(event.Data, &payload); err != nil {
		return fmt.Errorf("%w: decode proposal payload: %v", ErrCorruptLog, err)
	}
	proposal := payload.Proposal
	if _, exists := state.Actors[proposal.ActorID]; !exists {
		return fmt.Errorf("%w: proposal actor is unknown", ErrCorruptLog)
	}
	state.Proposals[proposal.ID] = proposal
	trimProposals(state)
	state.Receipts[proposal.RequestID] = protocol.RequestReceipt{
		Kind:        EventProposed,
		EntityID:    proposal.ID,
		Revision:    event.Sequence,
		RequestHash: payload.RequestHash,
	}
	return nil
}

func applyCommitted(state *protocol.SessionState, event protocol.EventRecord) error {
	var payload committedPayload
	if err := json.Unmarshal(event.Data, &payload); err != nil {
		return fmt.Errorf("%w: decode commit payload: %v", ErrCorruptLog, err)
	}
	request := payload.Request
	item := protocol.CommitItem{
		ProposalID: request.ProposalID, EventID: request.EventID, Accepted: request.Accepted,
		Outcome: request.Outcome, Tags: request.Tags, Facts: request.Facts, GoalUpdates: request.GoalUpdates,
	}
	if err := applyCommitItem(state, item, request.Tick, event.Sequence); err != nil {
		return err
	}
	if request.Tick > state.Tick {
		state.Tick = request.Tick
	}
	state.Receipts[request.RequestID] = protocol.RequestReceipt{Kind: EventCommitted, EntityID: request.ProposalID, Revision: event.Sequence}
	return advanceWorldRevision(state)
}

func applyBatchCommitted(state *protocol.SessionState, event protocol.EventRecord) error {
	var payload batchCommittedPayload
	if err := json.Unmarshal(event.Data, &payload); err != nil {
		return fmt.Errorf("%w: decode batch commit payload: %v", ErrCorruptLog, err)
	}
	for _, item := range payload.Request.Items {
		if err := applyCommitItem(state, item, payload.Request.Tick, event.Sequence); err != nil {
			return err
		}
	}
	if payload.Request.Tick > state.Tick {
		state.Tick = payload.Request.Tick
	}
	state.Receipts[payload.Request.RequestID] = protocol.RequestReceipt{
		Kind: EventBatchCommitted, EntityID: payload.Request.SessionID, Revision: event.Sequence,
	}
	return advanceWorldRevision(state)
}

func applyCommitItem(state *protocol.SessionState, item protocol.CommitItem, tick int64, revision uint64) error {
	proposal, exists := state.Proposals[item.ProposalID]
	if !exists || proposal.Status != "pending" {
		return fmt.Errorf("%w: committed proposal is unavailable", ErrCorruptLog)
	}
	if item.Accepted {
		proposal.Status = "accepted"
	} else {
		proposal.Status = "rejected"
	}
	outcomeReporting := protocol.HasFeature(state.Features, protocol.FeatureOutcomeReporting)
	if outcomeReporting {
		proposal.OutcomeEventID = item.EventID
		proposal.OutcomeTick = tick
	}
	state.Proposals[proposal.ID] = proposal
	if !item.Accepted {
		return nil
	}
	actor := state.Actors[proposal.ActorID]
	if proposal.ProposedGoal != nil && !goalExists(actor, proposal.ProposedGoal.ID) {
		if len(actor.Goals) >= maxGoals {
			return fmt.Errorf("%w: actor goal capacity exceeded", ErrCorruptLog)
		}
		goal := *proposal.ProposedGoal
		if outcomeReporting {
			goal.UpdatedTick = tick
			goal.ProgressAccumulator = int64(goal.Progress)
			goal.StatusExplicit = false
			goal.StatusUpdatedTick = 0
			goal.StatusSourceEventID = ""
		}
		actor.Goals = append(actor.Goals, goal)
	}
	actor.RecentActions = append(actor.RecentActions, proposal)
	if outcomeReporting {
		sort.SliceStable(actor.RecentActions, func(i, j int) bool {
			if actor.RecentActions[i].OutcomeTick == actor.RecentActions[j].OutcomeTick {
				if actor.RecentActions[i].OutcomeEventID != actor.RecentActions[j].OutcomeEventID {
					return actor.RecentActions[i].OutcomeEventID < actor.RecentActions[j].OutcomeEventID
				}
				return actor.RecentActions[i].ID < actor.RecentActions[j].ID
			}
			return actor.RecentActions[i].OutcomeTick < actor.RecentActions[j].OutcomeTick
		})
	}
	if len(actor.RecentActions) > maxRecentActions {
		actor.RecentActions = append([]protocol.ActionProposal(nil), actor.RecentActions[len(actor.RecentActions)-maxRecentActions:]...)
	}
	if outcomeReporting && tick > maxInt64-actor.ThinkEveryTicks {
		return fmt.Errorf("%w: commit tick overflows next think tick", ErrCorruptLog)
	}
	nextThinkTick := tick + actor.ThinkEveryTicks
	if !outcomeReporting || nextThinkTick > actor.NextThinkTick {
		actor.NextThinkTick = nextThinkTick
	}
	markRecalled(&actor, proposal.RecalledMemoryIDs, tick, outcomeReporting)
	if item.Outcome != "" {
		memoryID, err := hashJSON(struct {
			ActorID string `json:"actor_id"`
			EventID string `json:"event_id"`
		}{proposal.ActorID, item.EventID})
		if err != nil {
			return err
		}
		actor.Memories = append(actor.Memories, protocol.Memory{
			ID: "memory." + memoryID[:24], EventID: item.EventID, Tick: tick,
			Summary: item.Outcome, Tags: append([]string(nil), item.Tags...),
			Importance: 3, CreatedRevision: revision,
		})
		if outcomeReporting {
			sortActorMemories(&actor)
		}
		if protocol.HasFeature(state.Features, protocol.FeatureMemoryArchive) {
			if err := compactActorMemories(state.SessionID, &actor, revision, state); err != nil {
				return err
			}
		} else if len(actor.Memories) > maxMemories {
			trimActorMemories(state, &actor)
		}
	}
	applyFacts(
		&actor,
		item.Facts,
		item.EventID,
		tick,
		revision,
		protocol.HasFeature(state.Features, protocol.FeatureBeliefConflicts),
		outcomeReporting,
	)
	if outcomeReporting {
		if err := applyGoalProgress(&actor, proposal.GoalID, 1, "", tick, item.EventID); err != nil {
			return err
		}
		for _, update := range item.GoalUpdates {
			if err := applyGoalProgress(&actor, update.GoalID, update.ProgressDelta, update.Status, tick, item.EventID); err != nil {
				return err
			}
		}
	} else {
		applyLegacyGoalProgress(&actor, proposal.GoalID, 1, "")
		for _, update := range item.GoalUpdates {
			applyLegacyGoalProgress(&actor, update.GoalID, update.ProgressDelta, update.Status)
		}
	}
	state.Actors[proposal.ActorID] = actor
	return nil
}

func applyActivityUpdated(state *protocol.SessionState, event protocol.EventRecord) error {
	var payload activityUpdatedPayload
	if err := json.Unmarshal(event.Data, &payload); err != nil {
		return fmt.Errorf("%w: decode activity payload: %v", ErrCorruptLog, err)
	}
	for _, update := range payload.Request.Updates {
		actor, exists := state.Actors[update.ActorID]
		if !exists {
			return fmt.Errorf("%w: activity actor is unknown", ErrCorruptLog)
		}
		actor.Activity = &protocol.ActorActivity{
			RegionID: update.RegionID, State: update.State, Reason: update.Reason,
			UpdatedTick: payload.Request.Tick, UpdatedRevision: event.Sequence,
		}
		state.Actors[update.ActorID] = actor
	}
	if payload.Request.Tick > state.Tick {
		state.Tick = payload.Request.Tick
	}
	state.Receipts[payload.Request.RequestID] = protocol.RequestReceipt{
		Kind: EventActivityUpdated, EntityID: payload.Request.SessionID, Revision: event.Sequence,
	}
	return advanceWorldRevision(state)
}

func applyArbitrated(state *protocol.SessionState, event protocol.EventRecord) error {
	var payload arbitratedPayload
	if err := json.Unmarshal(event.Data, &payload); err != nil {
		return fmt.Errorf("%w: decode arbitration payload: %v", ErrCorruptLog, err)
	}
	state.Arbitrations = append(state.Arbitrations, payload.Record)
	if len(state.Arbitrations) > maxArbitrations {
		state.Arbitrations = append([]protocol.ArbitrationRecord(nil), state.Arbitrations[len(state.Arbitrations)-maxArbitrations:]...)
	}
	state.Receipts[payload.Record.RequestID] = protocol.RequestReceipt{
		Kind: EventArbitrated, EntityID: payload.Record.ID, Revision: event.Sequence,
	}
	return nil
}

func applyRestored(current protocol.SessionState, event protocol.EventRecord) (protocol.SessionState, error) {
	var payload restoredPayload
	if err := json.Unmarshal(event.Data, &payload); err != nil {
		return protocol.SessionState{}, fmt.Errorf("%w: decode restore payload: %v", ErrCorruptLog, err)
	}
	if err := ValidateSnapshot(payload.Snapshot); err != nil {
		return protocol.SessionState{}, fmt.Errorf("%w: %v", ErrCorruptLog, err)
	}
	restored, err := clone(payload.Snapshot.State)
	if err != nil {
		return protocol.SessionState{}, err
	}
	if current.SessionID != "" && (restored.SessionID != current.SessionID || restored.Binding != current.Binding) {
		return protocol.SessionState{}, fmt.Errorf("%w: restore binding mismatch", ErrCorruptLog)
	}
	outcomeReporting := protocol.HasFeature(restored.Features, protocol.FeatureOutcomeReporting)
	if !outcomeReporting {
		// Preserve the historical reducer for pre-feature logs.
		restored.Proposals = make(map[string]protocol.ActionProposal)
	} else {
		// Pending proposals are retained so an Outcome Outbox captured with the
		// same game save can report actions the authoritative game already
		// applied before the save. Resolved proposals are already projected into
		// actor state and do not need to cross the event-chain boundary.
		for proposalID, proposal := range restored.Proposals {
			if proposal.Status != "pending" {
				delete(restored.Proposals, proposalID)
			}
		}
		if restored.Proposals == nil {
			restored.Proposals = make(map[string]protocol.ActionProposal)
		}
	}
	advanceRestoredWorldRevision(&restored)
	rebaseRestoredRevisions(&restored, event.Sequence, event.Sequence-1, event.PrevHash)
	if restored.Receipts == nil {
		restored.Receipts = make(map[string]protocol.RequestReceipt)
	}
	// Receipt revisions belong to the event chain that produced the Snapshot.
	// Mark imported entries as historical so capacity trimming keeps the new
	// restore receipt and later acknowledgements first.
	for requestID, receipt := range restored.Receipts {
		receipt.Revision = 0
		restored.Receipts[requestID] = receipt
	}
	restored.Receipts[event.RequestID] = protocol.RequestReceipt{Kind: EventSessionRestored, EntityID: restored.SessionID, Revision: event.Sequence}
	return restored, nil
}

func rebaseRestoredRevisions(
	state *protocol.SessionState,
	createdRevision uint64,
	basedOnRevision uint64,
	basedOnHeadHash string,
) {
	for actorID, actor := range state.Actors {
		for index := range actor.Memories {
			actor.Memories[index].CreatedRevision = createdRevision
		}
		for index := range actor.MemorySummaries {
			actor.MemorySummaries[index].CreatedRevision = createdRevision
		}
		for key, set := range actor.BeliefSets {
			for index := range set.Claims {
				set.Claims[index].ObservedRevision = createdRevision
			}
			actor.BeliefSets[key] = set
		}
		for index := range actor.RecentActions {
			rebaseProposal(
				&actor.RecentActions[index],
				state,
				createdRevision,
				basedOnRevision,
				basedOnHeadHash,
			)
		}
		if actor.Activity != nil {
			activity := *actor.Activity
			activity.UpdatedRevision = createdRevision
			actor.Activity = &activity
		}
		state.Actors[actorID] = actor
	}
	for proposalID, proposal := range state.Proposals {
		rebaseProposal(&proposal, state, createdRevision, basedOnRevision, basedOnHeadHash)
		state.Proposals[proposalID] = proposal
	}
	for index := range state.Arbitrations {
		state.Arbitrations[index].CreatedRevision = createdRevision
	}
}

func rebaseProposal(
	proposal *protocol.ActionProposal,
	state *protocol.SessionState,
	createdRevision uint64,
	basedOnRevision uint64,
	basedOnHeadHash string,
) {
	proposal.BasedOnRevision = basedOnRevision
	proposal.BasedOnHeadHash = basedOnHeadHash
	proposal.CreatedRevision = createdRevision
	if protocol.HasFeature(state.Features, protocol.FeatureArbitration) {
		proposal.BasedOnWorldRevision = state.WorldRevision
	} else {
		proposal.BasedOnWorldRevision = 0
	}
}

func advanceWorldRevision(state *protocol.SessionState) error {
	if !protocol.HasFeature(state.Features, protocol.FeatureArbitration) {
		return nil
	}
	if state.WorldRevision == ^uint64(0) {
		return fmt.Errorf("%w: world revision overflow", ErrCorruptLog)
	}
	state.WorldRevision++
	return nil
}

func advanceRestoredWorldRevision(state *protocol.SessionState) {
	if !protocol.HasFeature(state.Features, protocol.FeatureArbitration) {
		return
	}
	// A successfully exported Snapshot must remain restorable even at the
	// uint64 ceiling. The new event-chain generation still invalidates stale
	// proposal bases; only the imported world counter saturates here.
	if state.WorldRevision < ^uint64(0) {
		state.WorldRevision++
	}
}

func applyFacts(
	actor *protocol.ActorState,
	facts []protocol.Fact,
	eventID string,
	tick int64,
	revision uint64,
	preserveConflicts bool,
	outcomeReporting bool,
) {
	if actor.Beliefs == nil {
		actor.Beliefs = make(map[string]protocol.Fact)
	}
	if preserveConflicts && actor.BeliefSets == nil {
		actor.BeliefSets = make(map[string]protocol.BeliefSet)
	}
	for _, fact := range facts {
		if len(fact.Visibility) > 0 && !contains(fact.Visibility, actor.ID) {
			continue
		}
		fact.SourceEventID = eventID
		if outcomeReporting {
			fact.ObservedTick = tick
		}
		key := fact.SubjectID + ":" + fact.Predicate
		if !preserveConflicts {
			if outcomeReporting {
				if current, exists := actor.Beliefs[key]; exists {
					if current.ObservedTick > fact.ObservedTick ||
						(current.ObservedTick == fact.ObservedTick &&
							current.SourceEventID > fact.SourceEventID) {
						continue
					}
				}
			}
			actor.Beliefs[key] = fact
			continue
		}
		set := actor.BeliefSets[key]
		set.SubjectID = fact.SubjectID
		set.Predicate = fact.Predicate
		updated := false
		for index := range set.Claims {
			if set.Claims[index].Fact.SourceEventID == eventID {
				set.Claims[index] = protocol.BeliefClaim{Fact: fact, ObservedRevision: revision}
				updated = true
				break
			}
		}
		if !updated {
			set.Claims = append(set.Claims, protocol.BeliefClaim{Fact: fact, ObservedRevision: revision})
		}
		trimBeliefClaims(&set, outcomeReporting)
		selected := selectBeliefClaim(set.Claims, outcomeReporting)
		set.SelectedSourceEventID = selected.Fact.SourceEventID
		set.Conflicted = beliefObjectCount(set.Claims) > 1
		actor.BeliefSets[key] = set
		actor.Beliefs[key] = selected.Fact
	}
	trimBeliefs(actor, preserveConflicts)
}

func trimBeliefs(actor *protocol.ActorState, preserveConflicts bool) {
	if len(actor.Beliefs) <= maxBeliefs {
		return
	}
	keys := make([]string, 0, len(actor.Beliefs))
	for key := range actor.Beliefs {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		left := actor.Beliefs[keys[i]]
		right := actor.Beliefs[keys[j]]
		if left.ObservedTick != right.ObservedTick {
			return left.ObservedTick < right.ObservedTick
		}
		if left.SourceEventID != right.SourceEventID {
			return left.SourceEventID < right.SourceEventID
		}
		return keys[i] < keys[j]
	})
	for _, key := range keys[:len(keys)-maxBeliefs] {
		delete(actor.Beliefs, key)
		if preserveConflicts {
			delete(actor.BeliefSets, key)
		}
	}
}

func trimBeliefClaims(set *protocol.BeliefSet, outcomeReporting bool) {
	if len(set.Claims) <= 8 {
		return
	}
	if !outcomeReporting {
		sort.Slice(set.Claims, func(i, j int) bool {
			if set.Claims[i].Fact.Confidence == set.Claims[j].Fact.Confidence {
				if set.Claims[i].ObservedRevision == set.Claims[j].ObservedRevision {
					return set.Claims[i].Fact.SourceEventID < set.Claims[j].Fact.SourceEventID
				}
				return set.Claims[i].ObservedRevision > set.Claims[j].ObservedRevision
			}
			return set.Claims[i].Fact.Confidence > set.Claims[j].Fact.Confidence
		})
		set.Claims = append([]protocol.BeliefClaim(nil), set.Claims[:8]...)
		return
	}
	sort.Slice(set.Claims, func(i, j int) bool {
		if set.Claims[i].Fact.ObservedTick == set.Claims[j].Fact.ObservedTick {
			if set.Claims[i].Fact.Confidence == set.Claims[j].Fact.Confidence {
				if set.Claims[i].Fact.SourceEventID == set.Claims[j].Fact.SourceEventID {
					return set.Claims[i].ObservedRevision > set.Claims[j].ObservedRevision
				}
				return set.Claims[i].Fact.SourceEventID > set.Claims[j].Fact.SourceEventID
			}
			return set.Claims[i].Fact.Confidence > set.Claims[j].Fact.Confidence
		}
		return set.Claims[i].Fact.ObservedTick > set.Claims[j].Fact.ObservedTick
	})
	set.Claims = append([]protocol.BeliefClaim(nil), set.Claims[:8]...)
}

func selectBeliefClaim(claims []protocol.BeliefClaim, outcomeReporting bool) protocol.BeliefClaim {
	values := append([]protocol.BeliefClaim(nil), claims...)
	if !outcomeReporting {
		sort.Slice(values, func(i, j int) bool {
			if values[i].Fact.Confidence == values[j].Fact.Confidence {
				if values[i].ObservedRevision == values[j].ObservedRevision {
					if values[i].Fact.Object == values[j].Fact.Object {
						return values[i].Fact.SourceEventID < values[j].Fact.SourceEventID
					}
					return values[i].Fact.Object < values[j].Fact.Object
				}
				return values[i].ObservedRevision > values[j].ObservedRevision
			}
			return values[i].Fact.Confidence > values[j].Fact.Confidence
		})
		return values[0]
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].Fact.ObservedTick == values[j].Fact.ObservedTick {
			if values[i].Fact.Confidence == values[j].Fact.Confidence {
				if values[i].Fact.SourceEventID == values[j].Fact.SourceEventID {
					if values[i].Fact.Object == values[j].Fact.Object {
						return values[i].ObservedRevision > values[j].ObservedRevision
					}
					return values[i].Fact.Object > values[j].Fact.Object
				}
				return values[i].Fact.SourceEventID > values[j].Fact.SourceEventID
			}
			return values[i].Fact.Confidence > values[j].Fact.Confidence
		}
		return values[i].Fact.ObservedTick > values[j].Fact.ObservedTick
	})
	return values[0]
}

func beliefObjectCount(claims []protocol.BeliefClaim) int {
	objects := make(map[string]struct{}, len(claims))
	for _, claim := range claims {
		objects[claim.Fact.Object] = struct{}{}
	}
	return len(objects)
}

func applyGoalProgress(actor *protocol.ActorState, goalID string, delta int, status string, tick int64, eventID string) error {
	if goalID == "" {
		return nil
	}
	for index := range actor.Goals {
		goal := &actor.Goals[index]
		if goal.ID != goalID {
			continue
		}
		accumulator := goal.ProgressAccumulator
		// Snapshots created before occurrence metadata used Progress as the
		// only stored value. Adopt it as the accumulator on first mutation.
		if accumulator == 0 && goal.Progress != 0 {
			accumulator = int64(goal.Progress)
		}
		change := int64(delta)
		if (change > 0 && accumulator > maxInt64-change) ||
			(change < 0 && accumulator < minInt64-change) {
			return fmt.Errorf("%w: goal progress accumulator overflow", ErrCorruptLog)
		}
		accumulator += change
		goal.ProgressAccumulator = accumulator
		if accumulator < 0 {
			goal.Progress = 0
		} else if accumulator > int64(goal.TargetProgress) {
			goal.Progress = goal.TargetProgress
		} else {
			goal.Progress = int(accumulator)
		}
		if status != "" && shouldReplaceGoalStatus(*goal, tick, eventID) {
			goal.Status = status
			goal.StatusExplicit = true
			goal.StatusUpdatedTick = tick
			goal.StatusSourceEventID = eventID
		} else if status == "" && !goal.StatusExplicit {
			if goal.Progress >= goal.TargetProgress {
				goal.Status = "completed"
			} else {
				goal.Status = "active"
			}
		}
		if tick > goal.UpdatedTick {
			goal.UpdatedTick = tick
		}
		return nil
	}
	return nil
}

func shouldReplaceGoalStatus(goal protocol.Goal, tick int64, eventID string) bool {
	if !goal.StatusExplicit || tick > goal.StatusUpdatedTick {
		return true
	}
	return tick == goal.StatusUpdatedTick && eventID > goal.StatusSourceEventID
}

func applyLegacyGoalProgress(actor *protocol.ActorState, goalID string, delta int, status string) {
	if goalID == "" {
		return
	}
	for index := range actor.Goals {
		goal := &actor.Goals[index]
		if goal.ID != goalID {
			continue
		}
		goal.Progress += delta
		if goal.Progress < 0 {
			goal.Progress = 0
		}
		if goal.Progress > goal.TargetProgress {
			goal.Progress = goal.TargetProgress
		}
		if status != "" {
			goal.Status = status
		} else if goal.Progress >= goal.TargetProgress {
			goal.Status = "completed"
		}
		return
	}
}

func markRecalled(actor *protocol.ActorState, ids []string, tick int64, outcomeReporting bool) {
	selected := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		selected[id] = struct{}{}
	}
	for index := range actor.Memories {
		if _, exists := selected[actor.Memories[index].ID]; exists {
			if actor.Memories[index].RecallCount < maxRecallCount {
				actor.Memories[index].RecallCount++
			}
			if !outcomeReporting || tick > actor.Memories[index].LastRecalledTick {
				actor.Memories[index].LastRecalledTick = tick
			}
		}
	}
	for index := range actor.MemorySummaries {
		if _, exists := selected[actor.MemorySummaries[index].ID]; exists {
			if actor.MemorySummaries[index].RecallCount < maxRecallCount {
				actor.MemorySummaries[index].RecallCount++
			}
			if !outcomeReporting || tick > actor.MemorySummaries[index].LastRecalledTick {
				actor.MemorySummaries[index].LastRecalledTick = tick
			}
		}
	}
}

func trimActorMemories(state *protocol.SessionState, actor *protocol.ActorState) {
	if len(actor.Memories) <= maxMemories {
		return
	}
	removedCount := len(actor.Memories) - maxMemories
	replacements := make(map[string]string, removedCount)
	for _, memory := range actor.Memories[:removedCount] {
		replacements[memory.ID] = ""
	}
	actor.Memories = append([]protocol.Memory(nil), actor.Memories[removedCount:]...)
	rewriteRecalledMemoryReferences(state, actor, replacements)
}

func rewriteRecalledMemoryReferences(
	state *protocol.SessionState,
	actor *protocol.ActorState,
	replacements map[string]string,
) {
	for index := range actor.RecentActions {
		actor.RecentActions[index].RecalledMemoryIDs = rewriteMemoryIDs(
			actor.RecentActions[index].RecalledMemoryIDs,
			replacements,
		)
	}
	if state == nil {
		return
	}
	for proposalID, proposal := range state.Proposals {
		if proposal.ActorID != actor.ID {
			continue
		}
		proposal.RecalledMemoryIDs = rewriteMemoryIDs(proposal.RecalledMemoryIDs, replacements)
		state.Proposals[proposalID] = proposal
	}
}

func rewriteMemoryIDs(ids []string, replacements map[string]string) []string {
	if len(ids) == 0 {
		return nil
	}
	rewritten := make([]string, 0, min(len(ids), 8))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if replacement, exists := replacements[id]; exists {
			id = replacement
		}
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		rewritten = append(rewritten, id)
		if len(rewritten) == 8 {
			break
		}
	}
	if len(rewritten) == 0 {
		return nil
	}
	return rewritten
}

func sortActorMemories(actor *protocol.ActorState) {
	sort.SliceStable(actor.Memories, func(i, j int) bool {
		if actor.Memories[i].Tick == actor.Memories[j].Tick {
			if actor.Memories[i].EventID != actor.Memories[j].EventID {
				return actor.Memories[i].EventID < actor.Memories[j].EventID
			}
			return actor.Memories[i].ID < actor.Memories[j].ID
		}
		return actor.Memories[i].Tick < actor.Memories[j].Tick
	})
}

func trimProposals(state *protocol.SessionState) {
	if len(state.Proposals) <= maxProposals {
		return
	}
	values := make([]protocol.ActionProposal, 0, len(state.Proposals))
	for _, proposal := range state.Proposals {
		values = append(values, proposal)
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].CreatedRevision == values[j].CreatedRevision {
			return values[i].ID < values[j].ID
		}
		return values[i].CreatedRevision < values[j].CreatedRevision
	})
	for _, proposal := range values {
		if len(state.Proposals) <= maxProposals {
			break
		}
		if proposal.Status != "pending" {
			delete(state.Proposals, proposal.ID)
		}
	}
}

func trimReceipts(state *protocol.SessionState) {
	if len(state.Receipts) <= maxReceipts {
		return
	}
	type item struct {
		id string
		protocol.RequestReceipt
	}
	items := make([]item, 0, len(state.Receipts))
	for id, receipt := range state.Receipts {
		items = append(items, item{id: id, RequestReceipt: receipt})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Revision == items[j].Revision {
			return items[i].id < items[j].id
		}
		return items[i].Revision < items[j].Revision
	})
	for len(state.Receipts) > maxReceipts {
		delete(state.Receipts, items[0].id)
		items = items[1:]
	}
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
