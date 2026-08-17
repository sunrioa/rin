package cognition

import "errors"

var (
	ErrMemoryStoreLocked      = errors.New("cognition memory store is already locked")
	ErrMemoryStoreClosed      = errors.New("cognition memory store is closed")
	ErrMemoryStorePersistence = errors.New("cognition memory store persistence failed")
)
