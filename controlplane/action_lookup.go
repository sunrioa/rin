package controlplane

import (
	"fmt"

	"github.com/sunrioa/rin/host"
)

// FindActionOperation is a process-local recovery port. It resolves an exact
// submitted intent without binding, authorizing, or dispatching another action.
func (service *Service) FindActionOperation(principal host.Principal, input SubmitActionInput) (OperationView, error) {
	if err := host.ValidatePrincipal(principal); err != nil {
		return OperationView{}, fmt.Errorf("%w: principal: %v", ErrInvalid, err)
	}
	principal = clonePrincipalValue(principal)
	if !hasScope(principal, ScopeActorExecute) && !hasScope(principal, ScopeHostAdmin) {
		return OperationView{}, ErrForbidden
	}
	if err := validateSubmitActionInput(input); err != nil {
		return OperationView{}, err
	}
	digest, err := host.ActionRequestDigest(input.Request)
	if err != nil {
		return OperationView{}, err
	}
	key := actionOperationRequestKey(principal.ID, input.Request.IdempotencyKey)
	fingerprint := actionSubmissionFingerprint(digest, input.ParentOperationID, principal)
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.closed {
		return OperationView{}, ErrClosed
	}
	if view, found, err := service.idempotentActionLocked(key, fingerprint); found || err != nil {
		return view, err
	}
	if _, inflight := service.actionFlights[key]; inflight {
		return OperationView{}, ErrUnavailable
	}
	return OperationView{}, ErrNotFound
}

// Changes is a process-local notification, not an authorization or read API.
// Capture the channel before checking state to avoid a missed wakeup.
func (service *Service) Changes() <-chan struct{} {
	service.mu.RLock()
	defer service.mu.RUnlock()
	return service.changed
}
