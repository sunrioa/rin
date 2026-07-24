package protocol

import (
	"strings"
	"testing"
)

func TestValidateIdentifierHistoryAcceptsCompleteAndLegacyAmbiguousEntries(t *testing.T) {
	history := validIdentifierHistory()
	if err := ValidateIdentifierHistory(history, "session.history"); err != nil {
		t.Fatalf("valid identifier history failed validation: %v", err)
	}
	replay := validIdentifierHistory()
	replayedProposal := replay.Requests["request.propose"]
	replayedProposal.ResultRevision = 10_000
	replayedProposal.Proposal.CreatedRevision = 10_000
	replay.Requests["request.propose"] = replayedProposal
	if err := ValidateIdentifierHistory(replay, "session.history"); err != nil {
		t.Fatalf("replay tombstone revision must not be bounded by snapshot state: %v", err)
	}

	legacy := IdentifierHistory{
		Version: IdentifierHistoryVersion,
		Requests: map[string]RequestIdentity{
			"request.legacy": {
				Kind:      "observation.recorded",
				Ambiguous: true,
			},
		},
		Events: map[string]EventIdentity{
			"event.legacy": {
				Kind:      "observation.recorded",
				Ambiguous: true,
			},
		},
	}
	if err := ValidateIdentifierHistory(legacy, "session.history"); err != nil {
		t.Fatalf("ambiguous legacy tombstones should allow unavailable fields: %v", err)
	}
}

func TestValidateIdentifierHistoryRejectsInvalidHashesAndKeys(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*IdentifierHistory)
		field  string
	}{
		{
			name: "unsupported version",
			mutate: func(history *IdentifierHistory) {
				history.Version = "identifier-history-v0"
			},
			field: "identifier_history.version",
		},
		{
			name: "unsafe request key",
			mutate: func(history *IdentifierHistory) {
				identity := history.Requests["request.observe"]
				delete(history.Requests, "request.observe")
				history.Requests["request/observe"] = identity
				history.Events["event.observe"] = EventIdentity{
					Kind:      "observation.recorded",
					RequestID: "request/observe",
					Revision:  2,
				}
			},
			field: "identifier_history.requests key",
		},
		{
			name: "missing request hash",
			mutate: func(history *IdentifierHistory) {
				identity := history.Requests["request.observe"]
				identity.RequestHash = ""
				history.Requests["request.observe"] = identity
			},
			field: "identifier_history.requests.request.observe.request_hash",
		},
		{
			name: "malformed request hash",
			mutate: func(history *IdentifierHistory) {
				identity := history.Requests["request.observe"]
				identity.RequestHash = "ABC"
				history.Requests["request.observe"] = identity
			},
			field: "identifier_history.requests.request.observe.request_hash",
		},
		{
			name: "malformed result head hash",
			mutate: func(history *IdentifierHistory) {
				identity := history.Requests["request.observe"]
				identity.ResultHeadHash = strings.Repeat("g", 64)
				history.Requests["request.observe"] = identity
			},
			field: "identifier_history.requests.request.observe.result_head_hash",
		},
		{
			name: "ambiguous non-empty request hash is still validated",
			mutate: func(history *IdentifierHistory) {
				identity := history.Requests["request.observe"]
				identity.Ambiguous = true
				identity.RequestHash = "legacy-hash"
				identity.ResultRevision = 0
				identity.ResultHeadHash = ""
				history.Requests["request.observe"] = identity
			},
			field: "identifier_history.requests.request.observe.request_hash",
		},
		{
			name: "ambiguous non-empty result head is still validated",
			mutate: func(history *IdentifierHistory) {
				identity := history.Requests["request.observe"]
				identity.Ambiguous = true
				identity.RequestHash = ""
				identity.ResultRevision = 0
				identity.ResultHeadHash = "legacy-head"
				history.Requests["request.observe"] = identity
			},
			field: "identifier_history.requests.request.observe.result_head_hash",
		},
		{
			name: "unsafe event key",
			mutate: func(history *IdentifierHistory) {
				identity := history.Events["event.observe"]
				delete(history.Events, "event.observe")
				history.Events["event/observe"] = identity
			},
			field: "identifier_history.events key",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			history := validIdentifierHistory()
			test.mutate(&history)
			err := ValidateIdentifierHistory(history, "session.history")
			assertIdentifierHistoryValidationField(t, err, test.field)
		})
	}
}

func TestValidateIdentifierHistoryRejectsInvalidResultAssociations(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*IdentifierHistory)
		field  string
	}{
		{
			name: "missing result revision",
			mutate: func(history *IdentifierHistory) {
				identity := history.Requests["request.observe"]
				identity.ResultRevision = 0
				history.Requests["request.observe"] = identity
			},
			field: "identifier_history.requests.request.observe.result_revision",
		},
		{
			name: "proposal and arbitration are mutually exclusive",
			mutate: func(history *IdentifierHistory) {
				identity := history.Requests["request.propose"]
				identity.Arbitration = &ArbitrationRecord{
					ID:              "arbitration.invalid",
					RequestID:       "request.propose",
					CreatedRevision: 3,
				}
				history.Requests["request.propose"] = identity
			},
			field: "identifier_history.requests.request.propose",
		},
		{
			name: "proposal requires matching kind",
			mutate: func(history *IdentifierHistory) {
				identity := history.Requests["request.propose"]
				identity.Kind = "observation.recorded"
				history.Requests["request.propose"] = identity
			},
			field: "identifier_history.requests.request.propose",
		},
		{
			name: "proposal request must match key",
			mutate: func(history *IdentifierHistory) {
				identity := history.Requests["request.propose"]
				identity.Proposal.RequestID = "request.other"
				history.Requests["request.propose"] = identity
			},
			field: "identifier_history.requests.request.propose.proposal.request_id",
		},
		{
			name: "proposal session must match",
			mutate: func(history *IdentifierHistory) {
				identity := history.Requests["request.propose"]
				identity.Proposal.SessionID = "session.other"
				history.Requests["request.propose"] = identity
			},
			field: "identifier_history.requests.request.propose.proposal.session_id",
		},
		{
			name: "proposal revision must match result",
			mutate: func(history *IdentifierHistory) {
				identity := history.Requests["request.propose"]
				identity.Proposal.CreatedRevision++
				history.Requests["request.propose"] = identity
			},
			field: "identifier_history.requests.request.propose.proposal.created_revision",
		},
		{
			name: "proposal action must be valid",
			mutate: func(history *IdentifierHistory) {
				identity := history.Requests["request.propose"]
				identity.Proposal.Action = ActionSpec{}
				history.Requests["request.propose"] = identity
			},
			field: "identifier_history.requests.request.propose.proposal.action.id",
		},
		{
			name: "original proposal must remain pending",
			mutate: func(history *IdentifierHistory) {
				identity := history.Requests["request.propose"]
				identity.Proposal.Status = "accepted"
				history.Requests["request.propose"] = identity
			},
			field: "identifier_history.requests.request.propose.proposal.status",
		},
		{
			name: "arbitration request must match key",
			mutate: func(history *IdentifierHistory) {
				identity := history.Requests["request.arbitrate"]
				identity.Arbitration.RequestID = "request.other"
				history.Requests["request.arbitrate"] = identity
			},
			field: "identifier_history.requests.request.arbitrate.arbitration.request_id",
		},
		{
			name: "arbitration decisions must be valid",
			mutate: func(history *IdentifierHistory) {
				identity := history.Requests["request.arbitrate"]
				identity.Arbitration.Decisions[0].Status = "unknown"
				history.Requests["request.arbitrate"] = identity
			},
			field: "identifier_history.requests.request.arbitrate.arbitration.decisions[0].status",
		},
		{
			name: "event request must exist when complete",
			mutate: func(history *IdentifierHistory) {
				history.Events["event.observe"] = EventIdentity{
					Kind:      "observation.recorded",
					RequestID: "request.missing",
					Revision:  2,
				}
			},
			field: "identifier_history.events.event.observe.request_id",
		},
		{
			name: "event kind must match request",
			mutate: func(history *IdentifierHistory) {
				identity := history.Events["event.observe"]
				identity.Kind = "action.committed"
				history.Events["event.observe"] = identity
			},
			field: "identifier_history.events.event.observe.kind",
		},
		{
			name: "event revision must match request",
			mutate: func(history *IdentifierHistory) {
				identity := history.Events["event.observe"]
				identity.Revision++
				history.Events["event.observe"] = identity
			},
			field: "identifier_history.events.event.observe.revision",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			history := validIdentifierHistory()
			test.mutate(&history)
			err := ValidateIdentifierHistory(history, "session.history")
			assertIdentifierHistoryValidationField(t, err, test.field)
		})
	}
}

func validIdentifierHistory() IdentifierHistory {
	requestHash := strings.Repeat("a", 64)
	headHash := strings.Repeat("b", 64)
	return IdentifierHistory{
		Version:          IdentifierHistoryVersion,
		CoverageComplete: true,
		Requests: map[string]RequestIdentity{
			"request.observe": {
				Kind:           "observation.recorded",
				RequestHash:    requestHash,
				ResultRevision: 2,
				ResultHeadHash: headHash,
			},
			"request.propose": {
				Kind:           identifierHistoryProposalKind,
				RequestHash:    requestHash,
				ResultRevision: 3,
				ResultHeadHash: headHash,
				Proposal: &ActionProposal{
					ID:              "proposal.history",
					SessionID:       "session.history",
					RequestID:       "request.propose",
					ActorID:         "actor.history",
					BasedOnRevision: 2,
					BasedOnHeadHash: headHash,
					CreatedRevision: 3,
					Action:          ActionSpec{ID: "wait", Kind: "wait", Description: "Wait safely."},
					Stance:          "wait",
					Summary:         "Wait.",
					Rationale:       "Waiting is allowed.",
					Status:          "pending",
				},
			},
			"request.arbitrate": {
				Kind:           identifierHistoryArbitrationKind,
				RequestHash:    requestHash,
				ResultRevision: 4,
				ResultHeadHash: headHash,
				Arbitration: &ArbitrationRecord{
					ID:                   "arbitration.history",
					RequestID:            "request.arbitrate",
					BasedOnWorldRevision: 1,
					CreatedRevision:      4,
					Decisions: []ArbitrationDecision{{
						ProposalID: "proposal.history",
						ActorID:    "actor.history",
						Status:     "selected",
						Reason:     "No conflict.",
					}},
				},
			},
		},
		Events: map[string]EventIdentity{
			"event.observe": {
				Kind:      "observation.recorded",
				RequestID: "request.observe",
				Revision:  2,
			},
		},
	}
}

func assertIdentifierHistoryValidationField(t *testing.T, err error, field string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected validation error for %s", field)
	}
	validation, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("error type = %T, want *ValidationError: %v", err, err)
	}
	if validation.Field != field {
		t.Fatalf("validation field = %q, want %q: %v", validation.Field, field, err)
	}
}
