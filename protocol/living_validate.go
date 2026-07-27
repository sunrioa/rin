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
	if err := validateJSONSafeTick("tick", request.Tick); err != nil {
		return err
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
	if err := validateJSONSafeTick("tick", request.Tick); err != nil {
		return err
	}
	if len(request.ProposalIDs) == 0 || len(request.ProposalIDs) > 64 {
		return &ValidationError{Field: "proposal_ids", Message: "must contain 1-64 proposal ids"}
	}
	if err := validateTags("proposal_ids", request.ProposalIDs, 64); err != nil {
		return err
	}
	return validateTags("exclusive_target_ids", request.ExclusiveTargetIDs, 64)
}

func ValidateBatchActionReport(request BatchActionReportRequest) error {
	if err := validateVersion(request.ProtocolVersion); err != nil {
		return err
	}
	for field, value := range map[string]string{"session_id": request.SessionID, "request_id": request.RequestID} {
		if err := validateID(field, value); err != nil {
			return err
		}
	}
	if err := validateJSONSafeTick("tick", request.Tick); err != nil {
		return err
	}
	if len(request.Reports) == 0 || len(request.Reports) > 64 {
		return &ValidationError{Field: "reports", Message: "must contain 1-64 action reports"}
	}
	proposalIDs := make(map[string]struct{}, len(request.Reports))
	eventIDs := make(map[string]struct{}, len(request.Reports))
	for index, report := range request.Reports {
		field := fmt.Sprintf("reports[%d]", index)
		if err := validateActionReport(field, report, request.SessionID); err != nil {
			return err
		}
		if _, exists := proposalIDs[report.ProposalID]; exists {
			return &ValidationError{Field: "reports", Message: "proposal ids must be unique"}
		}
		if _, exists := eventIDs[report.EventID]; exists {
			return &ValidationError{Field: "reports", Message: "event ids must be unique"}
		}
		proposalIDs[report.ProposalID] = struct{}{}
		eventIDs[report.EventID] = struct{}{}
	}
	return nil
}
