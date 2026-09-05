package signalbox

import (
	"context"
	"sort"
	"time"
)

// DeliveryState is scheduler-owned diagnostic state retained for the lifetime
// of the bounded Signal inbox. Task context itself is durably stored in Tasks.
type DeliveryState struct {
	Status            string `json:"status,omitempty"`
	Reason            string `json:"reason,omitempty"`
	TaskID            string `json:"task_id,omitempty"`
	Attempts          uint32 `json:"attempts,omitempty"`
	RetryAtUnixMillis int64  `json:"retry_at_unix_millis,omitempty"`
}

// RecordDelivery never advances a shared cursor past a failed delivery. A retry
// remains in the inbox until it succeeds, reaches its limit, or expires.
func (store *Store) RecordDelivery(signal Signal, status, reason, taskID string) error {
	switch status {
	case "started", "attached", "merged", "dropped", "retry":
	default:
		return invalid("delivery.status", "is invalid")
	}
	if err := validateText("delivery.reason", reason, 128, false); err != nil {
		return err
	}
	if taskID != "" {
		if err := validateIdentifier("delivery.task_id", taskID, 128); err != nil {
			return err
		}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.readyLocked(); err != nil {
		return err
	}
	box := store.inboxes[keyOf(Target{signal.HostID, signal.WorldID, signal.ActorID})]
	if box == nil {
		return ErrNotFound
	}
	for i := range box.signals {
		current := &box.signals[i]
		if current.SignalID != signal.SignalID || current.Cursor != signal.Cursor {
			continue
		}
		if current.Delivery.Status != "" && current.Delivery.Status != "retry" {
			if current.Delivery.Status == status && current.Delivery.Reason == reason && current.Delivery.TaskID == taskID {
				return nil
			}
			return ErrInvalid
		}
		attempts := current.Delivery.Attempts + 1
		state := DeliveryState{Status: status, Reason: reason, TaskID: taskID, Attempts: attempts}
		if status == "retry" {
			if attempts >= 32 {
				state.Status, state.Reason = "dropped", "retry-limit"
			} else {
				delay := time.Second * time.Duration(1<<min(attempts-1, 3))
				state.RetryAtUnixMillis = store.now().Add(delay).UnixMilli()
			}
		}
		current.Delivery = state
		if err := store.persistInboxLocked(keyOf(Target{signal.HostID, signal.WorldID, signal.ActorID}), box); err != nil {
			return err
		}
		store.notifyLocked()
		return nil
	}
	return ErrNotFound
}

// WaitPending is the single internal Actor coordinator feed. Completed
// deliveries never replay; transient failures wake on their bounded retry timer.
func (store *Store) WaitPending(ctx context.Context, wait time.Duration) ([]Signal, error) {
	deadline := time.Now().Add(wait)
	for {
		store.mu.Lock()
		if err := store.readyLocked(); err != nil {
			store.mu.Unlock()
			return nil, err
		}
		now := store.now().UnixMilli()
		items := make([]Signal, 0)
		next := time.Until(deadline)
		for _, box := range store.inboxes {
			store.pruneLocked(box, now)
			for _, signal := range box.signals {
				if signal.Delivery.Status == "" || (signal.Delivery.Status == "retry" && signal.Delivery.RetryAtUnixMillis <= now) {
					items = append(items, signal)
				} else if signal.Delivery.Status == "retry" {
					next = min(next, time.Duration(signal.Delivery.RetryAtUnixMillis-now)*time.Millisecond)
				}
			}
		}
		wake := store.changed
		sort.Slice(items, func(i, j int) bool { return items[i].globalSequence < items[j].globalSequence })
		if len(items) > 256 {
			items = items[:256]
		}
		store.mu.Unlock()
		if len(items) > 0 || wait == 0 || time.Until(deadline) <= 0 {
			return cloneSignals(items), nil
		}
		timer := time.NewTimer(max(next, time.Millisecond))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}
