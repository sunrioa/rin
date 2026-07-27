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
	canonicalizeIdentifierProposalPresentation(&copyHistory)
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
		resultHeadHash := ""
		if receipt.Revision == state.Revision {
			resultHeadHash = state.HeadHash
		}
		history.Requests[requestID] = protocol.RequestIdentity{
			Kind:           receipt.Kind,
			RequestHash:    receipt.RequestHash,
			ResultRevision: receipt.Revision,
			ResultHeadHash: resultHeadHash,
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
		add(proposal.LastReportEventID)
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
			add(proposal.LastReportEventID)
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

// legacyRestoreRequest preserves the exact Restore request JSON shape whose
// digest was stored by events written before expected_binding became
// mandatory. Field order is part of that digest, so replay must not synthesize
// a new-schema field.
type legacyRestoreRequest struct {
	ProtocolVersion string            `json:"protocol_version"`
	SessionID       string            `json:"session_id"`
	RequestID       string            `json:"request_id"`
	Snapshot        protocol.Snapshot `json:"snapshot"`
}

func legacyRestoreRequestDigest(request protocol.RestoreRequest) (string, error) {
	return requestDigest(legacyRestoreRequest{
		ProtocolVersion: request.ProtocolVersion,
		SessionID:       request.SessionID,
		RequestID:       request.RequestID,
		Snapshot:        request.Snapshot,
	})
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
		canonicalizeProposalPresentation(&proposal)
		identity.Proposal = &proposal
	case EventActionReported:
		var payload actionReportedPayload
		if err = json.Unmarshal(event.Data, &payload); err == nil {
			err = requireEventRequestID(event, payload.Request.RequestID)
		}
		if err == nil {
			hash, err = checkedRequestDigest(payload.RequestHash, payload.Request)
		}
		events = append(events, identifiedEvent{id: payload.Request.Report.EventID, kind: event.Type})
	case EventActionBatchReported:
		var payload actionBatchReportedPayload
		if err = json.Unmarshal(event.Data, &payload); err == nil {
			err = requireEventRequestID(event, payload.Request.RequestID)
		}
		if err == nil {
			hash, err = checkedRequestDigest(payload.RequestHash, payload.Request)
		}
		for _, report := range payload.Request.Reports {
			events = append(events, identifiedEvent{id: report.EventID, kind: event.Type})
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
			if payload.ExpectedBinding == nil {
				request := legacyRestoreRequest{
					ProtocolVersion: protocol.Version,
					SessionID:       payload.Snapshot.State.SessionID,
					RequestID:       event.RequestID,
					Snapshot:        payload.Snapshot,
				}
				hash, err = checkedRequestDigest(payload.RequestHash, request)
			} else {
				request := protocol.RestoreRequest{
					ProtocolVersion: protocol.Version,
					SessionID:       payload.Snapshot.State.SessionID,
					RequestID:       event.RequestID,
					ExpectedBinding: *payload.ExpectedBinding,
					Snapshot:        payload.Snapshot,
				}
				hash, err = checkedRequestDigest(payload.RequestHash, request)
			}
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
	delta, err := decodeIdentifierEventDelta(event)
	if err != nil {
		return identifierEventDelta{}, err
	}
	if delta.imported != nil {
		if err := validateIdentifierMerge(current, *delta.imported); err != nil {
			return identifierEventDelta{}, err
		}
	}
	return delta, nil
}

func prepareLedgerIdentifierEvent(
	current identifierLedger,
	event protocol.EventRecord,
) (identifierLedger, error) {
	delta, err := prepareLedgerIdentifierDelta(current, event)
	if err != nil {
		return identifierLedger{}, err
	}
	return current.withDelta(delta)
}

func prepareLedgerIdentifierDelta(
	current identifierLedger,
	event protocol.EventRecord,
) (identifierEventDelta, error) {
	delta, err := decodeIdentifierEventDelta(event)
	if err != nil {
		return identifierEventDelta{}, err
	}
	if delta.imported != nil {
		if err := validateIdentifierLedgerMerge(
			current,
			*delta.imported,
		); err != nil {
			return identifierEventDelta{}, err
		}
	}
	return delta, nil
}

func decodeIdentifierEventDelta(
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

func validateIdentifierLedgerMerge(
	current identifierLedger,
	imported protocol.IdentifierHistory,
) error {
	for requestID, value := range imported.Requests {
		existing, found, err := current.request(requestID)
		if err != nil {
			return err
		}
		if !found ||
			reflect.DeepEqual(existing, value) ||
			existing.Ambiguous ||
			value.Ambiguous {
			continue
		}
		return fmt.Errorf(
			"%w: request id %q identifies different historical operations",
			ErrCorruptLog,
			requestID,
		)
	}
	for eventID, value := range imported.Events {
		existing, found, err := current.event(eventID)
		if err != nil {
			return err
		}
		if !found ||
			reflect.DeepEqual(existing, value) ||
			existing.Ambiguous ||
			value.Ambiguous {
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
		history.Requests[requestID] = mergeRequestIdentity(existing, value)
		return
	}
	history.Requests[requestID] = value
}

func addEventIdentity(history *protocol.IdentifierHistory, eventID string, value protocol.EventIdentity) {
	if eventID == "" {
		return
	}
	if existing, found := history.Events[eventID]; found {
		history.Events[eventID] = mergeEventIdentity(existing, value)
		return
	}
	history.Events[eventID] = value
}

func identifierLedgerRequest(
	ledger identifierLedger,
	requestID, kind, digest string,
) (protocol.RequestIdentity, bool, error) {
	identity, found, err := ledger.request(requestID)
	if err != nil {
		return protocol.RequestIdentity{}, false, err
	}
	if !found {
		return protocol.RequestIdentity{}, false, nil
	}
	if identity.Ambiguous ||
		identity.Kind != kind ||
		identity.RequestHash == "" ||
		identity.RequestHash != digest {
		return protocol.RequestIdentity{}, true, requestConflict(requestID)
	}
	return identity, true, nil
}

func mutationResultFromIdentity(
	sessionID string,
	identity protocol.RequestIdentity,
	duplicate bool,
) protocol.MutationResult {
	return protocol.NewMutationResult(
		sessionID,
		identity.ResultRevision,
		identity.ResultHeadHash,
		duplicate,
	)
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
	var history protocol.IdentifierHistory
	if snapshot.IdentifierHistory == nil {
		history = identifiersFromState(snapshot.State)
	} else {
		var err error
		history, err = cloneIdentifierHistory(*snapshot.IdentifierHistory)
		if err != nil {
			return protocol.IdentifierHistory{}, err
		}
	}
	if err := protocol.ValidateIdentifierHistory(history, snapshot.State.SessionID); err != nil {
		return protocol.IdentifierHistory{}, err
	}
	state, err := clone(snapshot.State)
	if err != nil {
		return protocol.IdentifierHistory{}, err
	}
	canonicalizeStateProposalPresentation(&state)
	if err := validateIdentifiersCoverState(history, state); err != nil {
		return protocol.IdentifierHistory{}, err
	}
	return history, nil
}
