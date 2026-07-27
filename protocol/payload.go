package protocol

import (
	"encoding/json"

	"github.com/sunrioa/rin/host"
)

// NewHostValidatedPayload validates and copies one Host observation payload.
// Non-Go adapters must enforce the same schema check before sending Observe.
func NewHostValidatedPayload(
	reference HostSchemaRef,
	schema host.Schema,
	data []byte,
) (HostValidatedPayload, error) {
	payload := HostValidatedPayload{
		Schema: reference,
		Data:   json.RawMessage(data),
	}
	if err := validateHostValidatedPayload("payload", payload); err != nil {
		return HostValidatedPayload{}, err
	}
	if schema.SHA256 != reference.Digest {
		return HostValidatedPayload{}, &ValidationError{
			Field:   "payload.schema.digest",
			Message: "must match the Host schema digest",
		}
	}
	if err := schema.ValidateInstance(data); err != nil {
		return HostValidatedPayload{}, &ValidationError{
			Field:   "payload.data",
			Message: "does not conform to the Host schema: " + err.Error(),
		}
	}
	payload.Data = append(json.RawMessage(nil), data...)
	return payload, nil
}
