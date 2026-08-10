package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	maxCheckpointUsage        = 8_192
	maxCheckpointReservations = maxAuthorizations
	maxCheckpointDeltas       = 128
	maxCheckpointKeyBytes     = 1_024
)

// SnapshotState returns a defensive, deterministic policy checkpoint. Usage
// includes active reservations so a crash cannot silently restore budget.
func (engine *Engine) SnapshotState() State {
	return engine.snapshotState(nil)
}

// SnapshotStateFor returns a checkpoint containing only reservations whose
// decisions have already been durably registered by the caller. Temporary
// in-flight reservations are removed from both the reservation list and usage.
func (engine *Engine) SnapshotStateFor(decisionIDs []string) State {
	included := make(map[string]struct{}, len(decisionIDs))
	for _, decisionID := range decisionIDs {
		included[decisionID] = struct{}{}
	}
	return engine.snapshotState(included)
}

func (engine *Engine) snapshotState(included map[string]struct{}) State {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	usage := make(map[string]budgetUsage, len(engine.usage))
	for key, value := range engine.usage {
		usage[key] = value
	}
	if included != nil {
		for decisionID, value := range engine.reservations {
			if _, keep := included[decisionID]; keep {
				continue
			}
			for _, delta := range value.deltas {
				current := usage[delta.key]
				current.actions -= delta.actions
				current.quantity -= delta.quantity
				if current.actions == 0 && current.quantity == 0 {
					delete(usage, delta.key)
				} else {
					usage[delta.key] = current
				}
			}
		}
	}
	state := State{
		Version:        StateVersion,
		PolicyRevision: engine.config.Revision,
		ConfigDigest:   engine.configDigest,
		Usage:          make([]UsageCheckpoint, 0, len(usage)),
		Reservations:   make([]ReservationCheckpoint, 0, len(engine.reservations)),
	}
	for key, value := range usage {
		state.Usage = append(state.Usage, UsageCheckpoint{
			Key: key, Actions: value.actions, Quantity: value.quantity,
		})
	}
	slices.SortFunc(state.Usage, func(left, right UsageCheckpoint) int {
		return strings.Compare(left.Key, right.Key)
	})
	for decisionID, value := range engine.reservations {
		if included != nil {
			if _, keep := included[decisionID]; !keep {
				continue
			}
		}
		checkpoint := ReservationCheckpoint{
			DecisionID: decisionID,
			Deltas:     make([]UsageCheckpoint, len(value.deltas)),
		}
		for index, delta := range value.deltas {
			checkpoint.Deltas[index] = UsageCheckpoint{
				Key: delta.key, Actions: delta.actions, Quantity: delta.quantity,
			}
		}
		slices.SortFunc(checkpoint.Deltas, func(left, right UsageCheckpoint) int {
			return strings.Compare(left.Key, right.Key)
		})
		state.Reservations = append(state.Reservations, checkpoint)
	}
	slices.SortFunc(state.Reservations, func(left, right ReservationCheckpoint) int {
		return strings.Compare(left.DecisionID, right.DecisionID)
	})
	return state
}

// RestoreState installs one checkpoint into a fresh Engine. Outstanding
// reservations remain finalizable by DecisionID, but authorization and
// confirmation caches are deliberately not restored.
func (engine *Engine) RestoreState(state State) error {
	if err := ValidateState(state); err != nil {
		return err
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if state.PolicyRevision != engine.config.Revision {
		return fmt.Errorf(
			"policy state revision %d does not match active revision %d",
			state.PolicyRevision,
			engine.config.Revision,
		)
	}
	if state.ConfigDigest != engine.configDigest {
		return errors.New("policy state config digest does not match the active policy")
	}
	if len(engine.usage) != 0 || len(engine.reservations) != 0 ||
		len(engine.challenges) != 0 || len(engine.authorizations) != 0 ||
		len(engine.decisionKeys) != 0 {
		return errors.New("policy state can only be restored into a fresh Engine")
	}
	for _, checkpoint := range state.Usage {
		engine.usage[checkpoint.Key] = budgetUsage{
			actions: checkpoint.Actions, quantity: checkpoint.Quantity,
		}
	}
	for _, checkpoint := range state.Reservations {
		deltas := make([]budgetDelta, len(checkpoint.Deltas))
		for index, delta := range checkpoint.Deltas {
			deltas[index] = budgetDelta{
				key: delta.Key, actions: delta.Actions, quantity: delta.Quantity,
			}
		}
		engine.reservations[checkpoint.DecisionID] = reservation{deltas: deltas}
		engine.decisionKeys[checkpoint.DecisionID] =
			"restored\x00" + checkpoint.DecisionID
	}
	return nil
}

// ValidateState rejects ambiguous or unbounded policy checkpoints.
func ValidateState(state State) error {
	if state.Version != StateVersion {
		return fmt.Errorf("unsupported policy state version %q", state.Version)
	}
	if state.PolicyRevision == 0 || state.PolicyRevision > maxJSONSafeInteger {
		return errors.New("policy state revision is invalid")
	}
	if !digestPattern.MatchString(state.ConfigDigest) {
		return errors.New("policy state config_digest must be a lowercase SHA-256 digest")
	}
	if len(state.Usage) > maxCheckpointUsage {
		return errors.New("policy state contains too many usage buckets")
	}
	if len(state.Reservations) > maxCheckpointReservations {
		return errors.New("policy state contains too many reservations")
	}
	usage := make(map[string]budgetUsage, len(state.Usage))
	for index, checkpoint := range state.Usage {
		if err := validateCheckpointUsage(
			fmt.Sprintf("usage[%d]", index),
			checkpoint,
		); err != nil {
			return err
		}
		if _, duplicate := usage[checkpoint.Key]; duplicate {
			return errors.New("policy state contains duplicate usage keys")
		}
		usage[checkpoint.Key] = budgetUsage{
			actions: checkpoint.Actions, quantity: checkpoint.Quantity,
		}
	}
	reserved := make(map[string]budgetUsage, len(usage))
	decisions := make(map[string]struct{}, len(state.Reservations))
	for index, checkpoint := range state.Reservations {
		field := fmt.Sprintf("reservations[%d]", index)
		if err := validateID(field+".decision_id", checkpoint.DecisionID, false); err != nil {
			return err
		}
		if _, duplicate := decisions[checkpoint.DecisionID]; duplicate {
			return errors.New("policy state contains duplicate reservation decisions")
		}
		decisions[checkpoint.DecisionID] = struct{}{}
		if len(checkpoint.Deltas) == 0 || len(checkpoint.Deltas) > maxCheckpointDeltas {
			return fmt.Errorf("%s.deltas must contain between 1 and %d values", field, maxCheckpointDeltas)
		}
		keys := make(map[string]struct{}, len(checkpoint.Deltas))
		for deltaIndex, delta := range checkpoint.Deltas {
			if err := validateCheckpointUsage(
				fmt.Sprintf("%s.deltas[%d]", field, deltaIndex),
				delta,
			); err != nil {
				return err
			}
			if _, duplicate := keys[delta.Key]; duplicate {
				return fmt.Errorf("%s contains duplicate delta keys", field)
			}
			keys[delta.Key] = struct{}{}
			current, exists := usage[delta.Key]
			if !exists {
				return fmt.Errorf("%s references a missing usage bucket", field)
			}
			total := reserved[delta.Key]
			if maxJSONSafeInteger-total.actions < delta.Actions ||
				maxJSONSafeInteger-total.quantity < delta.Quantity {
				return fmt.Errorf("%s reservation total overflows", field)
			}
			total.actions += delta.Actions
			total.quantity += delta.Quantity
			if total.actions > current.actions || total.quantity > current.quantity {
				return fmt.Errorf("%s exceeds the corresponding usage bucket", field)
			}
			reserved[delta.Key] = total
		}
	}
	return nil
}

func configStateDigest(config Config) (string, error) {
	payload, err := json.Marshal(config)
	if err != nil {
		return "", fmt.Errorf("encode policy config digest: %w", err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func validateCheckpointUsage(field string, value UsageCheckpoint) error {
	if value.Key == "" || len(value.Key) > maxCheckpointKeyBytes ||
		!utf8.ValidString(value.Key) {
		return fmt.Errorf("%s.key must be bounded UTF-8", field)
	}
	if value.Actions > maxJSONSafeInteger || value.Quantity > maxJSONSafeInteger {
		return fmt.Errorf("%s counters must be JSON-safe integers", field)
	}
	if value.Actions == 0 && value.Quantity == 0 {
		return fmt.Errorf("%s must contain non-zero usage", field)
	}
	return nil
}
