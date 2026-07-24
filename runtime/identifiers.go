package runtime

import (
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/sunrioa/rin/protocol"
)

func newIdentifierHistory(complete bool) protocol.IdentifierHistory {
	return protocol.IdentifierHistory{
		Version:          protocol.IdentifierHistoryVersion,
		CoverageComplete: complete,
		Requests:         make(map[string]protocol.RequestIdentity),
		Events:           make(map[string]protocol.EventIdentity),
	}
}

func normalizeIdentifierHistory(history *protocol.IdentifierHistory) {
	if history.Version == "" {
		history.Version = protocol.IdentifierHistoryVersion
	}
	if history.Requests == nil {
		history.Requests = make(map[string]protocol.RequestIdentity)
	}
	if history.Events == nil {
		history.Events = make(map[string]protocol.EventIdentity)
	}
}

func cloneIdentifierHistory(history protocol.IdentifierHistory) (protocol.IdentifierHistory, error) {
	copyHistory, err := clone(history)
	if err != nil {
		return protocol.IdentifierHistory{}, err
	}
	normalizeIdentifierHistory(&copyHistory)
	return copyHistory, nil
}

// identifiersFromState recovers only identifiers that remain in the bounded
// public projection. It is deliberately marked incomplete: a state-only
// legacy Snapshot cannot prove which identifiers were evicted before export.
func identifiersFromState(state protocol.SessionState) protocol.IdentifierHistory {
	history := newIdentifierHistory(false)
	addRequest := func(requestID, kind string) {
		if requestID == "" {
			return
		}
		if existing, found := history.Requests[requestID]; found {
			existing.Ambiguous = true
			if existing.Kind != kind {
				existing.Kind = ""
			}
			history.Requests[requestID] = existing
			return
		}
		history.Requests[requestID] = protocol.RequestIdentity{Kind: kind, Ambiguous: true}
	}
	for requestID, receipt := range state.Receipts {
		history.Requests[requestID] = protocol.RequestIdentity{
			Kind:           receipt.Kind,
			RequestHash:    receipt.RequestHash,
			ResultRevision: receipt.Revision,
			Ambiguous:      true,
		}
		if receipt.Kind == EventObserved && receipt.EntityID != "" {
			history.Events[receipt.EntityID] = protocol.EventIdentity{
				Kind: EventObserved, RequestID: requestID, Revision: receipt.Revision, Ambiguous: true,
			}
		}
	}
	for _, proposal := range state.Proposals {
		addRequest(proposal.RequestID, EventProposed)
	}
	for _, arbitration := range state.Arbitrations {
		addRequest(arbitration.RequestID, EventArbitrated)
	}
	for _, actor := range state.Actors {
		for _, proposal := range actor.RecentActions {
			addRequest(proposal.RequestID, EventProposed)
		}
	}
	addStateEventIdentifiers(&history, state)
	return history
}

func addStateEventIdentifiers(history *protocol.IdentifierHistory, state protocol.SessionState) {
	add := func(eventID string) {
		if eventID == "" {
			return
		}
		if existing, found := history.Events[eventID]; found {
			existing.Ambiguous = true
			history.Events[eventID] = existing
			return
		}
		history.Events[eventID] = protocol.EventIdentity{Ambiguous: true}
	}
	for _, proposal := range state.Proposals {
		add(proposal.OutcomeEventID)
	}
	for _, actor := range state.Actors {
		for _, goal := range actor.Goals {
			add(goal.StatusSourceEventID)
		}
		for _, memory := range actor.Memories {
			add(memory.EventID)
		}
		for _, summary := range actor.MemorySummaries {
			for _, eventID := range summary.SourceEventIDs {
				add(eventID)
			}
		}
		for _, proposal := range actor.RecentActions {
			add(proposal.OutcomeEventID)
		}
		for _, fact := range actor.Beliefs {
			add(fact.SourceEventID)
		}
		for _, set := range actor.BeliefSets {
			for _, claim := range set.Claims {
				add(claim.Fact.SourceEventID)
			}
		}
	}
}

func requestDigest(request any) (string, error) {
	return hashJSON(request)
}

func checkedRequestDigest(stored string, request any) (string, error) {
	derived, err := requestDigest(request)
	if err != nil {
		return "", err
	}
	if stored != "" && stored != derived {
		return "", fmt.Errorf("%w: request hash does not match event payload", ErrCorruptLog)
	}
	return derived, nil
}

func requestIdentityFromEvent(event protocol.EventRecord) (protocol.RequestIdentity, []identifiedEvent, error) {
	identity := protocol.RequestIdentity{
		Kind:           event.Type,
		ResultRevision: event.Sequence,
		ResultHeadHash: event.Hash,
	}
	var (
		hash   string
		events []identifiedEvent
		err    error
	)
	switch event.Type {
	case EventSessionCreated:
		var payload createdPayload
		if err = json.Unmarshal(event.Data, &payload); err == nil {
			err = requireEventRequestID(event, payload.Request.RequestID)
		}
		if err == nil {
			hash, err = checkedRequestDigest(payload.RequestHash, payload.Request)
		}
	case EventObserved:
		var payload observedPayload
		if err = json.Unmarshal(event.Data, &payload); err == nil {
			err = requireEventRequestID(event, payload.Request.RequestID)
		}
		if err == nil {
			hash, err = checkedRequestDigest(payload.RequestHash, payload.Request)
		}
		events = append(events, identifiedEvent{id: payload.Request.EventID, kind: event.Type})
	case EventProposed:
		var payload proposedPayload
		if err = json.Unmarshal(event.Data, &payload); err == nil {
			err = requireEventRequestID(event, payload.Proposal.RequestID)
		}
		hash = payload.RequestHash
		proposal := payload.Proposal
		identity.Proposal = &proposal
	case EventCommitted:
		var payload committedPayload
		if err = json.Unmarshal(event.Data, &payload); err == nil {
			err = requireEventRequestID(event, payload.Request.RequestID)
		}
		if err == nil {
			hash, err = checkedRequestDigest(payload.RequestHash, payload.Request)
		}
		events = append(events, identifiedEvent{id: payload.Request.EventID, kind: event.Type})
	case EventBatchCommitted:
		var payload batchCommittedPayload
		if err = json.Unmarshal(event.Data, &payload); err == nil {
			err = requireEventRequestID(event, payload.Request.RequestID)
		}
		if err == nil {
			hash, err = checkedRequestDigest(payload.RequestHash, payload.Request)
		}
		for _, item := range payload.Request.Items {
			events = append(events, identifiedEvent{id: item.EventID, kind: event.Type})
		}
	case EventActivityUpdated:
		var payload activityUpdatedPayload
		if err = json.Unmarshal(event.Data, &payload); err == nil {
			err = requireEventRequestID(event, payload.Request.RequestID)
		}
		if err == nil {
			hash, err = checkedRequestDigest(payload.RequestHash, payload.Request)
		}
	case EventArbitrated:
		var payload arbitratedPayload
		if err = json.Unmarshal(event.Data, &payload); err == nil {
			err = requireEventRequestID(event, payload.Record.RequestID)
		}
		hash = payload.RequestHash
		record := payload.Record
		identity.Arbitration = &record
	case EventSessionRestored:
		var payload restoredPayload
		if err = json.Unmarshal(event.Data, &payload); err == nil {
			request := protocol.RestoreRequest{
				ProtocolVersion: protocol.Version,
				SessionID:       payload.Snapshot.State.SessionID,
				RequestID:       event.RequestID,
				Snapshot:        payload.Snapshot,
			}
			hash, err = checkedRequestDigest(payload.RequestHash, request)
		}
	default:
		err = fmt.Errorf("%w: unknown event type %q", ErrCorruptLog, event.Type)
	}
	if err != nil {
		return protocol.RequestIdentity{}, nil, fmt.Errorf("%w: decode identifier metadata: %v", ErrCorruptLog, err)
	}
	identity.RequestHash = hash
	identity.Ambiguous = hash == ""
	return identity, events, nil
}

type identifiedEvent struct {
	id   string
	kind string
}

func requireEventRequestID(event protocol.EventRecord, requestID string) error {
	if requestID != event.RequestID {
		return fmt.Errorf("%w: event request id does not match payload", ErrCorruptLog)
	}
	return nil
}

type identifierEventDelta struct {
	imported *protocol.IdentifierHistory
	request  protocol.RequestIdentity
	events   []identifiedEvent
	event    protocol.EventRecord
}

// prepareIdentifierEvent validates the permanent identifier projection
// without copying or mutating the existing ledger. Normal events are O(1);
// Restore is O(the imported history), which is required to validate its union.
func prepareIdentifierEvent(
	current protocol.IdentifierHistory,
	event protocol.EventRecord,
) (identifierEventDelta, error) {
	delta := identifierEventDelta{event: event}
	if event.Type == EventSessionRestored {
		var payload restoredPayload
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			return identifierEventDelta{}, fmt.Errorf("%w: decode restore identifiers: %v", ErrCorruptLog, err)
		}
		imported := identifiersFromState(payload.Snapshot.State)
		if payload.Snapshot.IdentifierHistory != nil {
			var err error
			imported, err = cloneIdentifierHistory(*payload.Snapshot.IdentifierHistory)
			if err != nil {
				return identifierEventDelta{}, err
			}
		}
		if err := validateIdentifierMerge(current, imported); err != nil {
			return identifierEventDelta{}, err
		}
		delta.imported = &imported
	}
	identity, eventIDs, err := requestIdentityFromEvent(event)
	if err != nil {
		return identifierEventDelta{}, err
	}
	delta.request = identity
	delta.events = eventIDs
	return delta, nil
}

// applyIdentifierDelta publishes a previously validated delta. Ledger entries
// are immutable after insertion, so this mutates only the two owning maps and
// avoids quadratic full-history cloning during normal writes and replay.
func applyIdentifierDelta(history *protocol.IdentifierHistory, delta identifierEventDelta) {
	normalizeIdentifierHistory(history)
	if delta.imported != nil {
		history.CoverageComplete = history.CoverageComplete && delta.imported.CoverageComplete
		for requestID, value := range delta.imported.Requests {
			existing, found := history.Requests[requestID]
			if !found {
				history.Requests[requestID] = value
				continue
			}
			if !reflect.DeepEqual(existing, value) {
				addRequestIdentity(history, requestID, value)
			}
		}
		for eventID, value := range delta.imported.Events {
			existing, found := history.Events[eventID]
			if !found {
				history.Events[eventID] = value
				continue
			}
			if !reflect.DeepEqual(existing, value) {
				addEventIdentity(history, eventID, value)
			}
		}
	}
	addRequestIdentity(history, delta.event.RequestID, delta.request)
	for _, value := range delta.events {
		addEventIdentity(history, value.id, protocol.EventIdentity{
			Kind: value.kind, RequestID: delta.event.RequestID, Revision: delta.event.Sequence,
		})
	}
}

func validateIdentifierMerge(
	current protocol.IdentifierHistory,
	imported protocol.IdentifierHistory,
) error {
	for requestID, value := range imported.Requests {
		existing, found := current.Requests[requestID]
		if !found || reflect.DeepEqual(existing, value) || existing.Ambiguous || value.Ambiguous {
			continue
		}
		return fmt.Errorf(
			"%w: request id %q identifies different historical operations",
			ErrCorruptLog,
			requestID,
		)
	}
	for eventID, value := range imported.Events {
		existing, found := current.Events[eventID]
		if !found || reflect.DeepEqual(existing, value) || existing.Ambiguous || value.Ambiguous {
			continue
		}
		return fmt.Errorf(
			"%w: event id %q identifies different historical events",
			ErrCorruptLog,
			eventID,
		)
	}
	return nil
}

func addRequestIdentity(history *protocol.IdentifierHistory, requestID string, value protocol.RequestIdentity) {
	if existing, found := history.Requests[requestID]; found {
		existing.Ambiguous = true
		existing.RequestHash = ""
		existing.ResultRevision = 0
		existing.ResultHeadHash = ""
		existing.Proposal = nil
		existing.Arbitration = nil
		if existing.Kind != value.Kind {
			existing.Kind = ""
		}
		history.Requests[requestID] = existing
		return
	}
	history.Requests[requestID] = value
}

func addEventIdentity(history *protocol.IdentifierHistory, eventID string, value protocol.EventIdentity) {
	if eventID == "" {
		return
	}
	if existing, found := history.Events[eventID]; found {
		existing.Ambiguous = true
		if existing.Kind != value.Kind {
			existing.Kind = ""
		}
		if existing.RequestID != value.RequestID {
			existing.RequestID = ""
		}
		existing.Revision = 0
		history.Events[eventID] = existing
		return
	}
	history.Events[eventID] = value
}

func mergeIdentifierHistories(
	current protocol.IdentifierHistory,
	imported protocol.IdentifierHistory,
) (protocol.IdentifierHistory, error) {
	result, err := cloneIdentifierHistory(current)
	if err != nil {
		return protocol.IdentifierHistory{}, err
	}
	normalizeIdentifierHistory(&imported)
	result.CoverageComplete = current.CoverageComplete && imported.CoverageComplete
	for requestID, value := range imported.Requests {
		existing, found := result.Requests[requestID]
		if !found {
			result.Requests[requestID] = value
			continue
		}
		if reflect.DeepEqual(existing, value) {
			continue
		}
		if existing.Ambiguous || value.Ambiguous {
			addRequestIdentity(&result, requestID, value)
			continue
		}
		return protocol.IdentifierHistory{}, fmt.Errorf(
			"%w: request id %q identifies different historical operations",
			ErrCorruptLog,
			requestID,
		)
	}
	for eventID, value := range imported.Events {
		existing, found := result.Events[eventID]
		if !found {
			result.Events[eventID] = value
			continue
		}
		if reflect.DeepEqual(existing, value) {
			continue
		}
		if existing.Ambiguous || value.Ambiguous {
			addEventIdentity(&result, eventID, value)
			continue
		}
		return protocol.IdentifierHistory{}, fmt.Errorf(
			"%w: event id %q identifies different historical events",
			ErrCorruptLog,
			eventID,
		)
	}
	return result, nil
}

func identifierRequest(
	history protocol.IdentifierHistory,
	requestID, kind, digest string,
) (protocol.RequestIdentity, bool, error) {
	identity, found := history.Requests[requestID]
	if !found {
		return protocol.RequestIdentity{}, false, nil
	}
	if identity.Ambiguous || identity.Kind != kind || identity.RequestHash == "" || identity.RequestHash != digest {
		return protocol.RequestIdentity{}, true, requestConflict(requestID)
	}
	return identity, true, nil
}

func identifierEventExists(history protocol.IdentifierHistory, eventID string) bool {
	_, exists := history.Events[eventID]
	return exists
}

func mutationResultFromIdentity(
	sessionID string,
	identity protocol.RequestIdentity,
	duplicate bool,
) protocol.MutationResult {
	return protocol.MutationResult{
		SessionID: sessionID,
		Revision:  identity.ResultRevision,
		HeadHash:  identity.ResultHeadHash,
		Duplicate: duplicate,
	}
}

func proposalFromIdentity(identity protocol.RequestIdentity) (protocol.ActionProposal, error) {
	if identity.Proposal == nil {
		return protocol.ActionProposal{}, NewError(
			"idempotency_result_unavailable",
			"the original proposal result is unavailable; use a new request id",
			ErrCorruptLog,
		)
	}
	proposal, err := clone(*identity.Proposal)
	if err != nil {
		return protocol.ActionProposal{}, NewError(
			"idempotency_result_unavailable",
			"the original proposal result could not be copied",
			err,
		)
	}
	return proposal, nil
}

func arbitrationFromIdentity(identity protocol.RequestIdentity) (protocol.ArbitrationRecord, error) {
	if identity.Arbitration == nil {
		return protocol.ArbitrationRecord{}, NewError(
			"idempotency_result_unavailable",
			"the original arbitration result is unavailable; use a new request id",
			ErrCorruptLog,
		)
	}
	record, err := clone(*identity.Arbitration)
	if err != nil {
		return protocol.ArbitrationRecord{}, NewError(
			"idempotency_result_unavailable",
			"the original arbitration result could not be copied",
			err,
		)
	}
	return record, nil
}

func identifiersForSnapshot(snapshot protocol.Snapshot) (protocol.IdentifierHistory, error) {
	if snapshot.IdentifierHistory == nil {
		return identifiersFromState(snapshot.State), nil
	}
	return cloneIdentifierHistory(*snapshot.IdentifierHistory)
}
