package controlplane

import "errors"

var (
	ErrInvalid       = errors.New("invalid control plane value")
	ErrNotFound      = errors.New("control plane value not found")
	ErrForbidden     = errors.New("control plane access forbidden")
	ErrLeaseConflict = errors.New("control plane lease conflict")
	ErrLeaseExpired  = errors.New("control plane lease expired")
	ErrStale         = errors.New("stale control plane value")
	ErrUnavailable   = errors.New("control plane host unavailable")
	ErrConflict      = errors.New("control plane value conflict")
	ErrCapacity      = errors.New("control plane capacity exceeded")
)
