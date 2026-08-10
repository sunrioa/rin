package cognition

import (
	"context"
	"errors"
)

func requireContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("policy context is required")
	}
	return nil
}
