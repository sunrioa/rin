package protocol

func ValidateTimeline(request TimelineRequest) error {
	if err := validateVersion(request.ProtocolVersion); err != nil {
		return err
	}
	if err := validateJSONSafeUnsigned("after_revision", request.AfterRevision); err != nil {
		return err
	}
	if err := validateID("session_id", request.SessionID); err != nil {
		return err
	}
	if request.Limit < 1 || request.Limit > 256 {
		return &ValidationError{Field: "limit", Message: "must be between 1 and 256"}
	}
	return nil
}

func ValidateReplay(request ReplayRequest) error {
	if err := validateVersion(request.ProtocolVersion); err != nil {
		return err
	}
	if err := validateJSONSafeUnsigned("revision", request.Revision); err != nil {
		return err
	}
	if err := validateID("session_id", request.SessionID); err != nil {
		return err
	}
	if request.Revision == 0 {
		return &ValidationError{Field: "revision", Message: "must be greater than zero"}
	}
	return nil
}
