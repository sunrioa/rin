package extension

import (
	"context"
	"errors"
)

func requireContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("extension context is required")
	}
	return nil
}
