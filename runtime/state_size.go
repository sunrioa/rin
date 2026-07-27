package runtime

import (
	"encoding/json"

	"github.com/sunrioa/rin/protocol"
)

const (
	// DefaultMaxSessionStateBytes leaves at least half of the bundled SDKs'
	// 32 MiB response budget for the API envelope and additive fields.
	DefaultMaxSessionStateBytes      uint64 = 16 << 20
	MaxConfigurableSessionStateBytes uint64 = 24 << 20
)

func ensureSessionStateSize(
	state protocol.SessionState,
	maximum uint64,
) error {
	encoded, err := json.Marshal(state)
	if err != nil {
		return NewError(
			"state_encode_failed",
			"Session State could not be encoded",
			err,
		)
	}
	if uint64(len(encoded)) > maximum {
		return NewError(
			"state_too_large",
			"Session State exceeds the configured byte limit",
			ErrConflict,
		)
	}
	return nil
}
