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
	maxProposals     = 64
	maxReceipts      = 1024
)

type createdPayload struct {
	Request protocol.CreateSessionRequest `json:"request"`
}

type observedPayload struct {
	Request protocol.ObserveRequest `json:"request"`
}

type proposedPayload struct {
	Proposal protocol.ActionProposal `json:"proposal"`
}

type committedPayload struct {
	Request protocol.CommitRequest `json:"request"`
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
		actors[seed.ID] = protocol.ActorState{
			ActorSeed:     seed,
			Beliefs:       make(map[string]protocol.Fact),
			NextThinkTick: 0,
		}
	}
	return protocol.SessionState{
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
	}, nil
}

func applyObserved(state *protocol.SessionState, event protocol.EventRecord) error {
	var payload observedPayload
	if err := json.Unmarshal(event.Data, &payload); err != nil {
		return fmt.Errorf("%w: decode observe payload: %v", ErrCorruptLog, err)
	}
	request := payload.Request
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
		if protocol.HasFeature(state.Features, protocol.FeatureMemoryArchive) {
			if err := compactActorMemories(state.SessionID, &actor, event.Sequence); err != nil {
				return err
			}
		} else if len(actor.Memories) > maxMemories {
			actor.Memories = append([]protocol.Memory(nil), actor.Memories[len(actor.Memories)-maxMemories:]...)
		}
		applyFacts(&actor, request.Facts, request.EventID, event.Sequence, protocol.HasFeature(state.Features, protocol.FeatureBeliefConflicts))
		state.Actors[actorID] = actor
	}
	if request.Tick > state.Tick {
		state.Tick = request.Tick
	}
	state.Receipts[request.RequestID] = protocol.RequestReceipt{Kind: EventObserved, EntityID: request.EventID, Revision: event.Sequence}
	return nil
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
	state.Receipts[proposal.RequestID] = protocol.RequestReceipt{Kind: EventProposed, EntityID: proposal.ID, Revision: event.Sequence}
	return nil
}

func applyCommitted(state *protocol.SessionState, event protocol.EventRecord) error {
	var payload committedPayload
	if err := json.Unmarshal(event.Data, &payload); err != nil {
		return fmt.Errorf("%w: decode commit payload: %v", ErrCorruptLog, err)
	}
	request := payload.Request
	proposal, exists := state.Proposals[request.ProposalID]
	if !exists || proposal.Status != "pending" {
		return fmt.Errorf("%w: committed proposal is unavailable", ErrCorruptLog)
	}
	if request.Accepted {
		proposal.Status = "accepted"
	} else {
		proposal.Status = "rejected"
	}
	state.Proposals[proposal.ID] = proposal
	actor := state.Actors[proposal.ActorID]
	if request.Accepted {
		actor.RecentActions = append(actor.RecentActions, proposal)
		if len(actor.RecentActions) > maxRecentActions {
			actor.RecentActions = append([]protocol.ActionProposal(nil), actor.RecentActions[len(actor.RecentActions)-maxRecentActions:]...)
		}
		actor.NextThinkTick = request.Tick + actor.ThinkEveryTicks
		markRecalled(&actor, proposal.RecalledMemoryIDs, request.Tick)
		if request.Outcome != "" {
			memoryID, err := hashJSON(struct {
				ActorID string `json:"actor_id"`
				EventID string `json:"event_id"`
			}{proposal.ActorID, request.EventID})
			if err != nil {
				return err
			}
			actor.Memories = append(actor.Memories, protocol.Memory{
				ID:              "memory." + memoryID[:24],
				EventID:         request.EventID,
				Tick:            request.Tick,
				Summary:         request.Outcome,
				Tags:            append([]string(nil), request.Tags...),
				Importance:      3,
				CreatedRevision: event.Sequence,
			})
			if protocol.HasFeature(state.Features, protocol.FeatureMemoryArchive) {
				if err := compactActorMemories(state.SessionID, &actor, event.Sequence); err != nil {
					return err
				}
			} else if len(actor.Memories) > maxMemories {
				actor.Memories = append([]protocol.Memory(nil), actor.Memories[len(actor.Memories)-maxMemories:]...)
			}
		}
		applyFacts(&actor, request.Facts, request.EventID, event.Sequence, protocol.HasFeature(state.Features, protocol.FeatureBeliefConflicts))
		applyGoalProgress(&actor, proposal.GoalID, 1, "")
		for _, update := range request.GoalUpdates {
			applyGoalProgress(&actor, update.GoalID, update.ProgressDelta, update.Status)
		}
		state.Actors[proposal.ActorID] = actor
	}
	if request.Tick > state.Tick {
		state.Tick = request.Tick
	}
	state.Receipts[request.RequestID] = protocol.RequestReceipt{Kind: EventCommitted, EntityID: proposal.ID, Revision: event.Sequence}
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
	restored.Proposals = make(map[string]protocol.ActionProposal)
	if restored.Receipts == nil {
		restored.Receipts = make(map[string]protocol.RequestReceipt)
	}
	restored.Receipts[event.RequestID] = protocol.RequestReceipt{Kind: EventSessionRestored, EntityID: restored.SessionID, Revision: event.Sequence}
	return restored, nil
}

func applyFacts(actor *protocol.ActorState, facts []protocol.Fact, eventID string, revision uint64, preserveConflicts bool) {
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
		key := fact.SubjectID + ":" + fact.Predicate
		if !preserveConflicts {
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
		trimBeliefClaims(&set)
		selected := selectBeliefClaim(set.Claims)
		set.SelectedSourceEventID = selected.Fact.SourceEventID
		set.Conflicted = beliefObjectCount(set.Claims) > 1
		actor.BeliefSets[key] = set
		actor.Beliefs[key] = selected.Fact
	}
}

func trimBeliefClaims(set *protocol.BeliefSet) {
	if len(set.Claims) <= 8 {
		return
	}
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
}

func selectBeliefClaim(claims []protocol.BeliefClaim) protocol.BeliefClaim {
	values := append([]protocol.BeliefClaim(nil), claims...)
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

func beliefObjectCount(claims []protocol.BeliefClaim) int {
	objects := make(map[string]struct{}, len(claims))
	for _, claim := range claims {
		objects[claim.Fact.Object] = struct{}{}
	}
	return len(objects)
}

func applyGoalProgress(actor *protocol.ActorState, goalID string, delta int, status string) {
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

func markRecalled(actor *protocol.ActorState, ids []string, tick int64) {
	selected := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		selected[id] = struct{}{}
	}
	for index := range actor.Memories {
		if _, exists := selected[actor.Memories[index].ID]; exists {
			actor.Memories[index].RecallCount++
			actor.Memories[index].LastRecalledTick = tick
		}
	}
	for index := range actor.MemorySummaries {
		if _, exists := selected[actor.MemorySummaries[index].ID]; exists {
			actor.MemorySummaries[index].RecallCount++
			actor.MemorySummaries[index].LastRecalledTick = tick
		}
	}
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
