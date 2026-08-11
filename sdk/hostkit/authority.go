package hostkit

import (
	"context"
	"errors"
)

// AuthorityDispatcher marshals adapter work onto the game-owned authority
// thread. Network and model work must remain outside this callback.
type AuthorityDispatcher interface {
	Dispatch(context.Context, func(context.Context) error) error
}

func requireContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("HostKit context is required")
	}
	return nil
}
