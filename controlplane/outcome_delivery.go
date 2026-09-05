package controlplane

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/sunrioa/rin/host"
)

const (
	outcomeRetryInterval  = time.Second
	outcomeAttemptTimeout = 5 * time.Second
	maxOutcomeSubscribers = 64
)

// Each subscriber has a stable ID and an independent worker. Its acknowledgement
// is persisted with the authoritative Outcome. false means pending, true means
// delivered; subscribers MUST make RecordOutcome idempotent by OperationID.
func (service *Service) startOutcomeDelivery() {
	ctx, cancel := context.WithCancel(context.Background())
	service.outcomeCancel = cancel
	for id, sink := range service.outcomeSinks {
		service.outcomeWG.Add(1)
		go service.deliverOutcomes(ctx, id, sink)
	}
}

func (service *Service) queueOutcomeDeliveryLocked(operation *operationState) {
	if operation.outcome == nil || operation.request.ActionRequest == nil {
		return
	}
	if operation.outcomeDelivery == nil {
		operation.outcomeDelivery = make(map[string]bool)
	}
	for id := range service.outcomeSinks {
		if _, known := operation.outcomeDelivery[id]; !known {
			operation.outcomeDelivery[id] = false
			operation.persistenceRevision++
			service.markOperationsDirtyLocked()
		}
	}
}

func hasPendingOutcome(operation *operationState) bool {
	for _, delivered := range operation.outcomeDelivery {
		if !delivered {
			return true
		}
	}
	return false
}

func (service *Service) deliverOutcomes(ctx context.Context, subscriber string, sink OutcomeSink) {
	defer service.outcomeWG.Done()
	ticker := time.NewTicker(outcomeRetryInterval)
	defer ticker.Stop()
	retryAfter := make(map[string]time.Time)
	for ctx.Err() == nil {
		// Capture before scanning. A commit during delivery wakes the next pass.
		changed := service.Changes()
		service.mu.Lock()
		var ids []string
		durable := service.operationSQLite != nil
		if !service.closed && service.persistOperationsLocked() == nil {
			if durable {
				ids, _ = service.operationSQLite.pendingOutcomeIDs(subscriber, time.Now().UnixMilli())
			} else {
				for id, operation := range service.operations {
					delivered, registered := operation.outcomeDelivery[subscriber]
					if registered && !delivered {
						ids = append(ids, id)
					}
				}
			}
		}
		service.mu.Unlock()
		slices.Sort(ids)
		for _, id := range ids {
			if ctx.Err() != nil {
				return
			}
			if !durable && time.Now().Before(retryAfter[id]) {
				continue
			}
			if err := service.deliverOutcome(ctx, subscriber, sink, id); err != nil {
				if durable {
					service.mu.Lock()
					if !service.closed {
						now := time.Now()
						_, _ = service.operationSQLite.db.Exec(`UPDATE outcome_backlog SET attempts=min(attempts+1,9007199254740991),last_attempt_at=?,next_attempt_at=?,last_error='subscriber-failed' WHERE operation_id=? AND subscriber=?`, now.UnixMilli(), now.Add(outcomeRetryInterval).UnixMilli(), id, subscriber)
					}
					service.mu.Unlock()
				} else {
					retryAfter[id] = time.Now().Add(outcomeRetryInterval)
				}
			} else {
				delete(retryAfter, id)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-changed:
		}
	}
}

func (service *Service) deliverOutcome(ctx context.Context, subscriber string, sink OutcomeSink, id string) error {
	service.mu.Lock()
	if service.closed {
		service.mu.Unlock()
		return nil
	}
	operation, lookupErr := service.lookupOperationLocked(id)
	if lookupErr != nil || operation.outcome == nil {
		service.mu.Unlock()
		return lookupErr
	}
	delivered, registered := operation.outcomeDelivery[subscriber]
	if !registered || delivered {
		service.mu.Unlock()
		return nil
	}
	// Do not expose an Outcome whose authoritative commit failed.
	if err := service.persistOperationsLocked(); err != nil {
		service.mu.Unlock()
		return err
	}
	evidence := operationOutcomeEvidence(operation, *operation.outcome)
	service.mu.Unlock()
	if evidence == nil {
		return errors.New("pending outcome has no evidence")
	}
	attempt, cancel := context.WithTimeout(ctx, outcomeAttemptTimeout)
	err := recordOutcomeSafely(attempt, sink, *evidence)
	cancel()
	if err != nil {
		return err
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	operation, err = service.lookupOperationLocked(id)
	if err != nil {
		return err
	}
	if service.operations[id] == nil {
		return service.ackArchivedOutcomeLocked(operation, subscriber)
	}
	service.recordOperationChangeLocked(id)
	operation.outcomeDelivery[subscriber] = true
	operation.persistenceRevision++
	service.markOperationsDirtyLocked()
	if err := service.persistOperationsLocked(); err != nil {
		// A failed acknowledgement is retried even in this process. A crash before
		// commit also replays it; the sink's idempotency makes both paths safe.
		operation.outcomeDelivery[subscriber] = false
		operation.persistenceRevision++
		service.markOperationsDirtyLocked()
		return err
	}
	return nil
}

func recordOutcomeSafely(ctx context.Context, sink OutcomeSink, evidence OutcomeEvidence) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("outcome subscriber panicked: %v", recovered)
		}
	}()
	return sink.RecordOutcome(ctx, evidence)
}

// OutcomeProjectionPending is a process-local readiness check. Optional memory
// delivery never gates a task; a task with a Plan waits for the task-plan sink.
func (service *Service) OutcomeProjectionPending(principal host.Principal, operationID, subscriber string) (bool, error) {
	if err := host.ValidatePrincipal(principal); err != nil {
		return false, fmt.Errorf("%w: principal: %v", ErrInvalid, err)
	}
	service.mu.RLock()
	defer service.mu.RUnlock()
	if service.closed {
		return false, ErrClosed
	}
	operation, err := service.lookupOperationLocked(operationID)
	if err != nil {
		return false, err
	}
	if principal.ID != operation.request.Principal.ID && !hasScope(principal, ScopeHostAdmin) {
		return false, ErrForbidden
	}
	delivered, registered := operation.outcomeDelivery[subscriber]
	return registered && !delivered, nil
}

func configuredOutcomeSinks(options Options) map[string]OutcomeSink {
	sinks := maps.Clone(options.OutcomeSinks)
	if sinks == nil {
		sinks = make(map[string]OutcomeSink)
	}
	// Retain the existing single-sink API for embedders. Named subscribers are
	// preferred when projections need independent acknowledgements.
	if options.OutcomeSink != nil {
		sinks["default"] = options.OutcomeSink
	}
	for name, sink := range sinks {
		if sink == nil {
			delete(sinks, name)
		}
	}
	return sinks
}

func validateOutcomeSinks(options Options) error {
	if options.OutcomeSink != nil && options.OutcomeSinks["default"] != nil {
		return fmt.Errorf("%w: legacy OutcomeSink conflicts with subscriber default", ErrInvalid)
	}
	sinks := configuredOutcomeSinks(options)
	if len(sinks) > maxOutcomeSubscribers {
		return fmt.Errorf("%w: too many outcome subscribers", ErrInvalid)
	}
	for id := range sinks {
		if err := validateID("outcome subscriber", id); err != nil {
			return err
		}
	}
	return nil
}
