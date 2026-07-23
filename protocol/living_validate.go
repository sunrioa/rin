package protocol

import "fmt"

func ValidateSetActorActivity(request SetActorActivityRequest) error {
	if err := validateVersion(request.ProtocolVersion); err != nil {
		return err
	}
	for field, value := range map[string]string{"session_id": request.SessionID, "request_id": request.RequestID} {
		if err := validateID(field, value); err != nil {
			return err
		}
	}
	if request.Tick < 0 {
		return &ValidationError{Field: "tick", Message: "must not be negative"}
	}
	if len(request.Updates) == 0 || len(request.Updates) > 128 {
		return &ValidationError{Field: "updates", Message: "must contain 1-128 actor updates"}
	}
	seen := make(map[string]struct{}, len(request.Updates))
	for index, update := range request.Updates {
		field := fmt.Sprintf("updates[%d]", index)
		if err := validateID(field+".actor_id", update.ActorID); err != nil {
			return err
		}
		if update.RegionID != "" {
			if err := validateID(field+".region_id", update.RegionID); err != nil {
				return err
			}
		}
		if update.State != "awake" && update.State != "dormant" {
			return &ValidationError{Field: field + ".state", Message: "must be awake or dormant"}
		}
		if err := validateText(field+".reason", update.Reason, 300, false); err != nil {
			return err
		}
		if _, exists := seen[update.ActorID]; exists {
			return &ValidationError{Field: "updates", Message: "actor ids must be unique"}
		}
		seen[update.ActorID] = struct{}{}
	}
	return nil
}

func ValidateArbitrate(request ArbitrateRequest) error {
	if err := validateVersion(request.ProtocolVersion); err != nil {
		return err
	}
	for field, value := range map[string]string{"session_id": request.SessionID, "request_id": request.RequestID} {
		if err := validateID(field, value); err != nil {
			return err
		}
	}
	if request.Tick < 0 {
		return &ValidationError{Field: "tick", Message: "must not be negative"}
	}
	if len(request.ProposalIDs) == 0 || len(request.ProposalIDs) > 64 {
		return &ValidationError{Field: "proposal_ids", Message: "must contain 1-64 proposal ids"}
	}
	if err := validateTags("proposal_ids", request.ProposalIDs, 64); err != nil {
		return err
	}
	return validateTags("exclusive_target_ids", request.ExclusiveTargetIDs, 64)
}

func ValidateBatchCommit(request BatchCommitRequest) error {
	if err := validateVersion(request.ProtocolVersion); err != nil {
		return err
	}
	for field, value := range map[string]string{"session_id": request.SessionID, "request_id": request.RequestID} {
		if err := validateID(field, value); err != nil {
			return err
		}
	}
	if request.Tick < 0 {
		return &ValidationError{Field: "tick", Message: "must not be negative"}
	}
	if len(request.Items) == 0 || len(request.Items) > 64 {
		return &ValidationError{Field: "items", Message: "must contain 1-64 commit items"}
	}
	proposalIDs := make(map[string]struct{}, len(request.Items))
	eventIDs := make(map[string]struct{}, len(request.Items))
	for index, item := range request.Items {
		field := fmt.Sprintf("items[%d]", index)
		if err := validateCommitItem(field, item); err != nil {
			return err
		}
		if _, exists := proposalIDs[item.ProposalID]; exists {
			return &ValidationError{Field: "items", Message: "proposal ids must be unique"}
		}
		if _, exists := eventIDs[item.EventID]; exists {
			return &ValidationError{Field: "items", Message: "event ids must be unique"}
		}
		proposalIDs[item.ProposalID] = struct{}{}
		eventIDs[item.EventID] = struct{}{}
	}
	return nil
}

func validateCommitItem(field string, item CommitItem) error {
	if err := validateID(field+".proposal_id", item.ProposalID); err != nil {
		return err
	}
	if err := validateID(field+".event_id", item.EventID); err != nil {
		return err
	}
	if err := validateText(field+".outcome", item.Outcome, 1000, item.Accepted); err != nil {
		return err
	}
	if err := validateTags(field+".tags", item.Tags, 32); err != nil {
		return err
	}
	if len(item.Facts) > 64 || len(item.GoalUpdates) > 32 {
		return &ValidationError{Field: field, Message: "contains too many updates"}
	}
	for index, fact := range item.Facts {
		if err := validateRequestFact(fmt.Sprintf("%s.facts[%d]", field, index), fact); err != nil {
			return err
		}
	}
	for index, update := range item.GoalUpdates {
		base := fmt.Sprintf("%s.goal_updates[%d]", field, index)
		if err := validateID(base+".goal_id", update.GoalID); err != nil {
			return err
		}
		if update.ProgressDelta < -1000 || update.ProgressDelta > 1000 {
			return &ValidationError{Field: base + ".progress_delta", Message: "must be between -1000 and 1000"}
		}
		if update.Status != "" && update.Status != "active" && update.Status != "completed" && update.Status != "released" {
			return &ValidationError{Field: base + ".status", Message: "must be active, completed, or released"}
		}
	}
	return nil
}
