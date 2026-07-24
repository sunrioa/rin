package protocol

import "fmt"

const IdentifierHistoryVersion = "identifier-history-v1"

const (
	identifierHistoryCreateKind      = "session.created"
	identifierHistoryObservationKind = "observation.recorded"
	identifierHistoryProposalKind    = "proposal.created"
	identifierHistoryCommitKind      = "action.committed"
	identifierHistoryBatchKind       = "action.batch-committed"
	identifierHistoryActivityKind    = "actor.activity-updated"
	identifierHistoryArbitrationKind = "world.arbitrated"
	identifierHistoryRestoreKind     = "session.restored"
)

// IdentifierHistory is the durable identity ledger carried by a Snapshot.
// It is separate from bounded State projections so a retained identifier does
// not become reusable merely because its Receipt, Proposal, or Arbitration was
// evicted.
type IdentifierHistory struct {
	Version          string                     `json:"version"`
	CoverageComplete bool                       `json:"coverage_complete"`
	Requests         map[string]RequestIdentity `json:"requests,omitempty"`
	Events           map[string]EventIdentity   `json:"events,omitempty"`
}

type RequestIdentity struct {
	Kind           string             `json:"kind"`
	RequestHash    string             `json:"request_hash,omitempty"`
	ResultRevision uint64             `json:"result_revision,omitempty"`
	ResultHeadHash string             `json:"result_head_hash,omitempty"`
	Proposal       *ActionProposal    `json:"proposal,omitempty"`
	Arbitration    *ArbitrationRecord `json:"arbitration,omitempty"`
	Ambiguous      bool               `json:"ambiguous,omitempty"`
}

type EventIdentity struct {
	Kind      string `json:"kind"`
	RequestID string `json:"request_id,omitempty"`
	Revision  uint64 `json:"revision,omitempty"`
	Ambiguous bool   `json:"ambiguous,omitempty"`
}

// ValidateIdentifierHistory validates the intrinsic structure and associations
// of a Snapshot identifier ledger. Result revisions deliberately have no upper
// bound tied to the Snapshot State revision: a replay snapshot may retain
// tombstones produced later in the local event chain.
func ValidateIdentifierHistory(history IdentifierHistory, sessionID string) error {
	const base = "identifier_history"

	if err := validateID(base+".session_id", sessionID); err != nil {
		return err
	}
	if history.Version != IdentifierHistoryVersion {
		return &ValidationError{
			Field:   base + ".version",
			Message: "must equal " + IdentifierHistoryVersion,
		}
	}

	for requestID, identity := range history.Requests {
		field := base + ".requests." + requestID
		if err := validateID(base+".requests key", requestID); err != nil {
			return err
		}
		if identity.Kind == "" {
			if !identity.Ambiguous {
				return &ValidationError{
					Field:   field + ".kind",
					Message: "is required unless the identity is ambiguous",
				}
			}
		} else {
			if err := validateID(field+".kind", identity.Kind); err != nil {
				return err
			}
			if !validIdentifierRequestKind(identity.Kind) {
				return &ValidationError{
					Field:   field + ".kind",
					Message: "must be a supported durable mutation kind",
				}
			}
		}
		if identity.RequestHash == "" {
			if !identity.Ambiguous {
				return &ValidationError{
					Field:   field + ".request_hash",
					Message: "is required unless the identity is ambiguous",
				}
			}
		} else if !hashPattern.MatchString(identity.RequestHash) {
			return &ValidationError{
				Field:   field + ".request_hash",
				Message: "must be a lowercase SHA-256 hash",
			}
		}
		if identity.ResultRevision == 0 && !identity.Ambiguous {
			return &ValidationError{
				Field:   field + ".result_revision",
				Message: "must be greater than zero unless the identity is ambiguous",
			}
		}
		if identity.ResultHeadHash == "" {
			if !identity.Ambiguous {
				return &ValidationError{
					Field:   field + ".result_head_hash",
					Message: "is required unless the identity is ambiguous",
				}
			}
		} else if !hashPattern.MatchString(identity.ResultHeadHash) {
			return &ValidationError{
				Field:   field + ".result_head_hash",
				Message: "must be a lowercase SHA-256 hash",
			}
		}
		if identity.Proposal != nil && identity.Arbitration != nil {
			return &ValidationError{
				Field:   field,
				Message: "must not contain both proposal and arbitration results",
			}
		}

		switch identity.Kind {
		case identifierHistoryProposalKind:
			if identity.Arbitration != nil {
				return &ValidationError{
					Field:   field + ".arbitration",
					Message: "does not match the request kind",
				}
			}
			if identity.Proposal == nil {
				if !identity.Ambiguous {
					return &ValidationError{
						Field:   field + ".proposal",
						Message: "is required for a non-ambiguous proposal identity",
					}
				}
			} else if err := validateHistoryProposal(field+".proposal", requestID, sessionID, identity); err != nil {
				return err
			}
		case identifierHistoryArbitrationKind:
			if identity.Proposal != nil {
				return &ValidationError{
					Field:   field + ".proposal",
					Message: "does not match the request kind",
				}
			}
			if identity.Arbitration == nil {
				if !identity.Ambiguous {
					return &ValidationError{
						Field:   field + ".arbitration",
						Message: "is required for a non-ambiguous arbitration identity",
					}
				}
			} else if err := validateHistoryArbitration(field+".arbitration", requestID, identity); err != nil {
				return err
			}
		default:
			if identity.Proposal != nil || identity.Arbitration != nil {
				return &ValidationError{
					Field:   field,
					Message: "typed results require a matching proposal or arbitration kind",
				}
			}
		}
	}

	for eventID, identity := range history.Events {
		field := base + ".events." + eventID
		if err := validateID(base+".events key", eventID); err != nil {
			return err
		}
		if identity.Kind == "" {
			if !identity.Ambiguous {
				return &ValidationError{
					Field:   field + ".kind",
					Message: "is required unless the identity is ambiguous",
				}
			}
		} else {
			if err := validateID(field+".kind", identity.Kind); err != nil {
				return err
			}
			if !validIdentifierEventKind(identity.Kind) {
				return &ValidationError{
					Field:   field + ".kind",
					Message: "must be observation.recorded, action.committed, or action.batch-committed",
				}
			}
		}
		if identity.RequestID == "" {
			if !identity.Ambiguous {
				return &ValidationError{
					Field:   field + ".request_id",
					Message: "is required unless the identity is ambiguous",
				}
			}
		} else {
			if err := validateID(field+".request_id", identity.RequestID); err != nil {
				return err
			}
			request, exists := history.Requests[identity.RequestID]
			if !exists {
				if !identity.Ambiguous || history.CoverageComplete {
					return &ValidationError{
						Field:   field + ".request_id",
						Message: "must reference a request in identifier history",
					}
				}
			} else {
				if request.Kind != "" && identity.Kind != "" && request.Kind != identity.Kind {
					return &ValidationError{
						Field:   field + ".kind",
						Message: "must match the referenced request kind",
					}
				}
				if identity.Revision != 0 &&
					request.ResultRevision != 0 &&
					identity.Revision != request.ResultRevision {
					return &ValidationError{
						Field:   field + ".revision",
						Message: "must match the referenced request result revision",
					}
				}
			}
		}
		if identity.Revision == 0 && !identity.Ambiguous {
			return &ValidationError{
				Field:   field + ".revision",
				Message: "must be greater than zero unless the identity is ambiguous",
			}
		}
	}
	return nil
}

func validIdentifierRequestKind(kind string) bool {
	switch kind {
	case identifierHistoryCreateKind,
		identifierHistoryObservationKind,
		identifierHistoryProposalKind,
		identifierHistoryCommitKind,
		identifierHistoryBatchKind,
		identifierHistoryActivityKind,
		identifierHistoryArbitrationKind,
		identifierHistoryRestoreKind:
		return true
	default:
		return false
	}
}

func validIdentifierEventKind(kind string) bool {
	switch kind {
	case identifierHistoryObservationKind,
		identifierHistoryCommitKind,
		identifierHistoryBatchKind:
		return true
	default:
		return false
	}
}

func validateHistoryProposal(
	field string,
	requestID string,
	sessionID string,
	identity RequestIdentity,
) error {
	proposal := identity.Proposal
	for suffix, value := range map[string]string{
		".id":         proposal.ID,
		".session_id": proposal.SessionID,
		".request_id": proposal.RequestID,
		".actor_id":   proposal.ActorID,
	} {
		if err := validateID(field+suffix, value); err != nil {
			return err
		}
	}
	if proposal.SessionID != sessionID {
		return &ValidationError{
			Field:   field + ".session_id",
			Message: "must match the identifier history session",
		}
	}
	if proposal.RequestID != requestID {
		return &ValidationError{
			Field:   field + ".request_id",
			Message: "must match the request map key",
		}
	}
	if proposal.CreatedRevision == 0 {
		return &ValidationError{
			Field:   field + ".created_revision",
			Message: "must be greater than zero",
		}
	}
	if identity.ResultRevision != 0 && proposal.CreatedRevision != identity.ResultRevision {
		return &ValidationError{
			Field:   field + ".created_revision",
			Message: "must match the request result revision",
		}
	}
	if proposal.Tick < 0 {
		return &ValidationError{Field: field + ".tick", Message: "must not be negative"}
	}
	if proposal.BasedOnRevision == 0 || proposal.BasedOnRevision >= proposal.CreatedRevision {
		return &ValidationError{
			Field:   field + ".based_on_revision",
			Message: "must reference a revision before created_revision",
		}
	}
	if !hashPattern.MatchString(proposal.BasedOnHeadHash) {
		return &ValidationError{
			Field:   field + ".based_on_head_hash",
			Message: "must be a lowercase SHA-256 hash",
		}
	}
	if err := validateAction(field+".action", proposal.Action); err != nil {
		return err
	}
	switch proposal.Stance {
	case "engage", "partial", "redirect", "refuse", "wait":
	default:
		return &ValidationError{
			Field:   field + ".stance",
			Message: "must be engage, partial, redirect, refuse, or wait",
		}
	}
	if err := validateText(field+".summary", proposal.Summary, 500, true); err != nil {
		return err
	}
	if err := validateText(field+".rationale", proposal.Rationale, 500, true); err != nil {
		return err
	}
	if proposal.PolicySource != "" {
		if err := validateID(field+".policy_source", proposal.PolicySource); err != nil {
			return err
		}
	}
	if err := validateTags(field+".recalled_memory_ids", proposal.RecalledMemoryIDs, 8); err != nil {
		return err
	}
	if proposal.GoalID != "" {
		if err := validateID(field+".goal_id", proposal.GoalID); err != nil {
			return err
		}
	}
	if proposal.ProposedGoal != nil {
		if err := validateGoal(field+".proposed_goal", *proposal.ProposedGoal); err != nil {
			return err
		}
		if proposal.GoalID != proposal.ProposedGoal.ID ||
			proposal.ProposedGoal.Progress != 0 ||
			proposal.ProposedGoal.Status != "active" ||
			proposal.ProposedGoal.UpdatedTick != 0 ||
			proposal.ProposedGoal.ProgressAccumulator != 0 ||
			proposal.ProposedGoal.StatusExplicit ||
			proposal.ProposedGoal.StatusUpdatedTick != 0 ||
			proposal.ProposedGoal.StatusSourceEventID != "" {
			return &ValidationError{
				Field:   field + ".proposed_goal",
				Message: "must be an unmodified candidate matching goal_id",
			}
		}
	}
	if proposal.Status != "pending" {
		return &ValidationError{
			Field:   field + ".status",
			Message: "an original proposal result must be pending",
		}
	}
	if proposal.OutcomeEventID != "" || proposal.OutcomeTick != 0 {
		return &ValidationError{
			Field:   field + ".outcome_event_id",
			Message: "an original proposal result cannot contain an outcome",
		}
	}
	return nil
}

func validateHistoryArbitration(
	field string,
	requestID string,
	identity RequestIdentity,
) error {
	record := identity.Arbitration
	if err := validateID(field+".id", record.ID); err != nil {
		return err
	}
	if err := validateID(field+".request_id", record.RequestID); err != nil {
		return err
	}
	if record.RequestID != requestID {
		return &ValidationError{
			Field:   field + ".request_id",
			Message: "must match the request map key",
		}
	}
	if record.CreatedRevision == 0 {
		return &ValidationError{
			Field:   field + ".created_revision",
			Message: "must be greater than zero",
		}
	}
	if identity.ResultRevision != 0 && record.CreatedRevision != identity.ResultRevision {
		return &ValidationError{
			Field:   field + ".created_revision",
			Message: "must match the request result revision",
		}
	}
	if record.Tick < 0 {
		return &ValidationError{
			Field:   field + ".tick",
			Message: "must not be negative",
		}
	}
	if record.BasedOnWorldRevision == 0 {
		return &ValidationError{
			Field:   field + ".based_on_world_revision",
			Message: "must be greater than zero",
		}
	}
	if len(record.Decisions) == 0 || len(record.Decisions) > 64 {
		return &ValidationError{
			Field:   field + ".decisions",
			Message: "must contain 1-64 decisions",
		}
	}
	proposalIDs := make(map[string]struct{}, len(record.Decisions))
	actorIDs := make(map[string]struct{}, len(record.Decisions))
	statuses := make(map[string]string, len(record.Decisions))
	for index, decision := range record.Decisions {
		base := fmt.Sprintf("%s.decisions[%d]", field, index)
		if err := validateID(base+".proposal_id", decision.ProposalID); err != nil {
			return err
		}
		if err := validateID(base+".actor_id", decision.ActorID); err != nil {
			return err
		}
		if decision.Status != "selected" && decision.Status != "deferred" {
			return &ValidationError{
				Field:   base + ".status",
				Message: "must be selected or deferred",
			}
		}
		if err := validateText(base+".reason", decision.Reason, 300, true); err != nil {
			return err
		}
		if err := validateTags(
			base+".conflicting_proposal_ids",
			decision.ConflictingProposalIDs,
			64,
		); err != nil {
			return err
		}
		if _, exists := proposalIDs[decision.ProposalID]; exists {
			return &ValidationError{
				Field:   field + ".decisions",
				Message: "proposal ids must be unique",
			}
		}
		proposalIDs[decision.ProposalID] = struct{}{}
		if _, exists := actorIDs[decision.ActorID]; exists {
			return &ValidationError{
				Field:   field + ".decisions",
				Message: "actor ids must be unique",
			}
		}
		actorIDs[decision.ActorID] = struct{}{}
		statuses[decision.ProposalID] = decision.Status
		if decision.Status == "selected" && len(decision.ConflictingProposalIDs) != 0 {
			return &ValidationError{
				Field:   base + ".conflicting_proposal_ids",
				Message: "selected decisions cannot have conflicts",
			}
		}
		if decision.Status == "deferred" && len(decision.ConflictingProposalIDs) == 0 {
			return &ValidationError{
				Field:   base + ".conflicting_proposal_ids",
				Message: "deferred decisions must identify a conflict",
			}
		}
	}
	for index, decision := range record.Decisions {
		base := fmt.Sprintf("%s.decisions[%d].conflicting_proposal_ids", field, index)
		for conflictIndex, proposalID := range decision.ConflictingProposalIDs {
			if proposalID == decision.ProposalID {
				return &ValidationError{
					Field:   fmt.Sprintf("%s[%d]", base, conflictIndex),
					Message: "cannot reference the decision itself",
				}
			}
			status, exists := statuses[proposalID]
			if !exists || status != "selected" {
				return &ValidationError{
					Field:   fmt.Sprintf("%s[%d]", base, conflictIndex),
					Message: "must reference a selected decision",
				}
			}
		}
	}
	return nil
}
