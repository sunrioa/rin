package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"

	rinapi "github.com/sunrioa/rin/api"
	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
)

type contractShapeError struct {
	code    string
	field   string
	message string
}

// validateContractShape enforces OpenAPI required presence and closed input
// objects before encoding/json discards presence and unknown-member details.
func validateContractShape(
	payload []byte,
	schemaName string,
) (*contractShapeError, error) {
	if schemaName == "" {
		return nil, fmt.Errorf("OpenAPI request schema is empty")
	}
	shapeErr, err := rinapi.ValidateRequestShape(schemaName, payload)
	if err != nil {
		return nil, err
	}
	if shapeErr == nil {
		return nil, nil
	}
	code := "invalid_request"
	if shapeErr.Kind == rinapi.ShapeErrorUnknown {
		code = "invalid_json"
	}
	return &contractShapeError{
		code:    code,
		field:   shapeErr.Field,
		message: shapeErr.Message,
	}, nil
}

func validateInlineSnapshotWireSize(payload []byte, target any) *contractShapeError {
	if _, ok := target.(*protocol.RestoreRequest); !ok {
		return nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(payload, &object); err != nil {
		return nil
	}
	rawSnapshot, exists := object["snapshot"]
	if !exists {
		return nil
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, rawSnapshot); err != nil {
		return nil
	}
	size := compact.Len()
	if size <= rinruntime.MaxInlineSnapshotBytes {
		return nil
	}
	return &contractShapeError{
		code:  "snapshot_too_large",
		field: "snapshot",
		message: fmt.Sprintf(
			"compact snapshot is %d bytes and exceeds the %d-byte inline transport limit",
			size,
			rinruntime.MaxInlineSnapshotBytes,
		),
	}
}
