// Package taskstate owns engine-neutral, coarse-grained task plans.
// It never binds game targets, grants capabilities, or executes actions.
package taskstate

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/sunrioa/rin/host"
)

const SchemaVersion = "rin.task-plan/v1"

const (
	maxSteps          = 16
	maxConditions     = 16
	maxStepConditions = 8
	maxEvidence       = 64
	maxWireInteger    = uint64(1<<53 - 1)
)

var (
	ErrInvalid   = errors.New("invalid task plan")
	ErrNotFound  = errors.New("task plan not found")
	ErrConflict  = errors.New("task plan revision conflict")
	ErrCapacity  = errors.New("task plan capacity exceeded")
	ErrClosed    = errors.New("task plan store is closed")
	ErrLocked    = errors.New("task plan store is locked")
	ErrPersist   = errors.New("task plan persistence failed")
	ErrForbidden = errors.New("task plan operation is forbidden")
)

type PlanningMode string

const (
	PlanningDisabled PlanningMode = "disabled"
	PlanningAuto     PlanningMode = "auto"
	PlanningRequired PlanningMode = "required"
)

type ControllerSource string

const (
	ControllerInternal ControllerSource = "internal"
	ControllerExternal ControllerSource = "external"
)

type PlanStatus string

const (
	PlanPlanned   PlanStatus = "planned"
	PlanActive    PlanStatus = "active"
	PlanBlocked   PlanStatus = "blocked"
	PlanPaused    PlanStatus = "paused"
	PlanCompleted PlanStatus = "completed"
	PlanFailed    PlanStatus = "failed"
	PlanCancelled PlanStatus = "cancelled"
)

type StepStatus string

const (
	StepPending   StepStatus = "pending"
	StepActive    StepStatus = "active"
	StepBlocked   StepStatus = "blocked"
	StepCompleted StepStatus = "completed"
	StepFailed    StepStatus = "failed"
	StepSkipped   StepStatus = "skipped"
)

type EvidenceKind string

const (
	EvidenceOperationOutcome EvidenceKind = "operation-outcome"
	EvidenceObservationFact  EvidenceKind = "observation-fact"
)

type ReplanReason string

const (
	ReplanGoalChanged               ReplanReason = "goal-changed"
	ReplanRequiredCapabilityMissing ReplanReason = "required-capability-missing"
	ReplanFailureThresholdReached   ReplanReason = "failure-threshold-reached"
	ReplanEpochInvalidated          ReplanReason = "epoch-invalidated"
	ReplanManualAuthorized          ReplanReason = "manual-authorized"
)

type PlanCondition struct {
	ConditionID   string              `json:"condition_id"`
	Kind          EvidenceKind        `json:"kind"`
	Summary       string              `json:"summary"`
	Capability    *host.CapabilityRef `json:"capability"`
	FactID        string              `json:"fact_id"`
	FactValueJSON string              `json:"fact_value_json"`
}

type PlanEvidence struct {
	EvidenceID           string       `json:"evidence_id"`
	ConditionID          string       `json:"condition_id"`
	Kind                 EvidenceKind `json:"kind"`
	OperationID          string       `json:"operation_id,omitempty"`
	Epoch                host.Epoch   `json:"epoch"`
	ObservationSequence  uint64       `json:"observation_sequence"`
	Digest               string       `json:"digest"`
	RecordedAtUnixMillis int64        `json:"recorded_at_unix_millis"`
}

type PlanStep struct {
	StepID            string               `json:"step_id"`
	Title             string               `json:"title"`
	Objective         string               `json:"objective"`
	Status            StepStatus           `json:"status"`
	SuccessConditions []PlanCondition      `json:"success_conditions,omitempty"`
	CapabilityHints   []host.CapabilityRef `json:"capability_hints,omitempty"`
	Attempt           uint32               `json:"attempt"`
	MaxAttempts       uint32               `json:"max_attempts"`
	BlockedReason     string               `json:"blocked_reason,omitempty"`
	Evidence          []PlanEvidence       `json:"evidence_refs,omitempty"`
}

// StepDraft contains only caller-authored plan intent. Runtime status,
// attempts, blocking reasons, and evidence are always owned by Rin.
type StepDraft struct {
	StepID            string               `json:"step_id"`
	Title             string               `json:"title"`
	Objective         string               `json:"objective"`
	SuccessConditions []PlanCondition      `json:"success_conditions,omitempty"`
	CapabilityHints   []host.CapabilityRef `json:"capability_hints,omitempty"`
	MaxAttempts       uint32               `json:"max_attempts,omitempty"`
}

type PlanState struct {
	SchemaVersion              string           `json:"schema_version"`
	PlanID                     string           `json:"plan_id"`
	TaskID                     string           `json:"task_id"`
	SessionID                  string           `json:"session_id"`
	HostID                     string           `json:"host_id"`
	WorldID                    string           `json:"world_id"`
	ActorID                    string           `json:"actor_id"`
	ControllerID               string           `json:"controller_id"`
	ControllerSource           ControllerSource `json:"controller_source"`
	Goal                       string           `json:"goal"`
	GoalDigest                 string           `json:"goal_digest"`
	PlanningMode               PlanningMode     `json:"planning_mode"`
	Status                     PlanStatus       `json:"status"`
	Phase                      string           `json:"phase,omitempty"`
	CurrentStepID              string           `json:"current_step_id,omitempty"`
	Steps                      []PlanStep       `json:"steps"`
	SuccessConditions          []PlanCondition  `json:"success_conditions,omitempty"`
	Evidence                   []PlanEvidence   `json:"evidence_refs,omitempty"`
	Revision                   uint64           `json:"revision"`
	ReplanCount                uint32           `json:"replan_count"`
	MaxReplans                 uint32           `json:"max_replans"`
	ConsecutiveFailures        uint32           `json:"consecutive_failures"`
	LastFailureFamily          string           `json:"last_failure_family,omitempty"`
	LastOperationID            string           `json:"last_operation_id,omitempty"`
	LastOutcomeCode            string           `json:"last_outcome_code,omitempty"`
	BasedOnEpoch               host.Epoch       `json:"based_on_epoch"`
	BasedOnObservationSequence uint64           `json:"based_on_observation_sequence"`
	CreatedAtUnixMillis        int64            `json:"created_at_unix_millis"`
	UpdatedAtUnixMillis        int64            `json:"updated_at_unix_millis"`
}

type Draft struct {
	PlanID                     string           `json:"plan_id"`
	TaskID                     string           `json:"task_id"`
	SessionID                  string           `json:"session_id"`
	HostID                     string           `json:"host_id"`
	WorldID                    string           `json:"world_id"`
	ActorID                    string           `json:"actor_id"`
	ControllerID               string           `json:"controller_id"`
	ControllerSource           ControllerSource `json:"controller_source"`
	Goal                       string           `json:"goal"`
	PlanningMode               PlanningMode     `json:"planning_mode"`
	Phase                      string           `json:"phase,omitempty"`
	Steps                      []StepDraft      `json:"steps"`
	SuccessConditions          []PlanCondition  `json:"success_conditions,omitempty"`
	MaxReplans                 uint32           `json:"max_replans,omitempty"`
	BasedOnEpoch               host.Epoch       `json:"based_on_epoch"`
	BasedOnObservationSequence uint64           `json:"based_on_observation_sequence"`
}

type ReplanPolicy struct {
	FailureThreshold uint32 `json:"failure_threshold"`
	MaxReplans       uint32 `json:"max_replans"`
}

type ReplanInput struct {
	Reason                ReplanReason
	ConsecutiveFailures   uint32
	ReplanCount           uint32
	PlayerAuthorized      bool
	HasAuthoritativeProof bool
}

func NewPlan(draft Draft, nowUnixMillis int64) (PlanState, error) {
	if nowUnixMillis < 0 {
		return PlanState{}, invalid("created_at", "must not be negative")
	}
	state := PlanState{
		SchemaVersion: SchemaVersion,
		PlanID:        draft.PlanID, TaskID: draft.TaskID, SessionID: draft.SessionID,
		HostID: draft.HostID, WorldID: draft.WorldID, ActorID: draft.ActorID,
		ControllerID: draft.ControllerID, ControllerSource: draft.ControllerSource,
		Goal: strings.TrimSpace(draft.Goal), PlanningMode: draft.PlanningMode,
		Status: PlanActive, Phase: strings.TrimSpace(draft.Phase),
		Steps: draftSteps(draft.Steps), SuccessConditions: cloneConditions(draft.SuccessConditions),
		MaxReplans: draft.MaxReplans, BasedOnEpoch: draft.BasedOnEpoch,
		BasedOnObservationSequence: draft.BasedOnObservationSequence,
		Revision:                   1, CreatedAtUnixMillis: nowUnixMillis, UpdatedAtUnixMillis: nowUnixMillis,
	}
	if state.MaxReplans == 0 {
		state.MaxReplans = 3
	}
	state.GoalDigest = digestText(state.Goal)
	for index := range state.Steps {
		state.Steps[index].Status = StepPending
		state.Steps[index].Attempt = 0
		state.Steps[index].BlockedReason = ""
		state.Steps[index].Evidence = nil
		if state.Steps[index].MaxAttempts == 0 {
			state.Steps[index].MaxAttempts = 3
		}
	}
	if len(state.Steps) != 0 {
		state.Steps[0].Status = StepActive
		state.CurrentStepID = state.Steps[0].StepID
		if state.Phase == "" {
			state.Phase = state.Steps[0].Title
		}
	}
	if err := ValidatePlan(state); err != nil {
		return PlanState{}, err
	}
	return clonePlan(state), nil
}

func ApplyEvidence(state PlanState, evidence PlanEvidence, nowUnixMillis int64) (PlanState, bool, error) {
	if err := ValidatePlan(state); err != nil {
		return PlanState{}, false, err
	}
	if state.Status != PlanActive && state.Status != PlanBlocked {
		return PlanState{}, false, fmt.Errorf("%w: plan is not active", ErrConflict)
	}
	if err := validateEvidence(evidence, state); err != nil {
		return PlanState{}, false, err
	}
	if findEvidence(state.Evidence, evidence.EvidenceID) || evidenceInSteps(state.Steps, evidence.EvidenceID) {
		return clonePlan(state), false, nil
	}
	next := clonePlan(state)
	stepIndex := currentStepIndex(next)
	if stepIndex >= 0 && conditionInList(
		next.Steps[stepIndex].SuccessConditions, evidence.ConditionID, evidence.Kind,
	) {
		next.Steps[stepIndex].Evidence = append(next.Steps[stepIndex].Evidence, evidence)
	} else if conditionInList(next.SuccessConditions, evidence.ConditionID, evidence.Kind) {
		next.Evidence = append(next.Evidence, evidence)
	} else {
		return PlanState{}, false, fmt.Errorf("%w: evidence does not match a current condition", ErrInvalid)
	}
	advanced := false
	if stepIndex >= 0 && conditionsSatisfied(
		next.Steps[stepIndex].SuccessConditions, next.Steps[stepIndex].Evidence,
	) {
		next.Steps[stepIndex].Status = StepCompleted
		next.Steps[stepIndex].BlockedReason = ""
		next.ConsecutiveFailures = 0
		next.LastFailureFamily = ""
		next.CurrentStepID = ""
		for index := stepIndex + 1; index < len(next.Steps); index++ {
			if next.Steps[index].Status == StepPending {
				next.Steps[index].Status = StepActive
				next.CurrentStepID = next.Steps[index].StepID
				next.Phase = next.Steps[index].Title
				next.Status = PlanActive
				break
			}
		}
		advanced = true
	}
	if next.CurrentStepID == "" && allStepsTerminalSuccess(next.Steps) &&
		conditionsSatisfied(next.SuccessConditions, next.Evidence) {
		next.Status = PlanCompleted
	}
	next.Revision++
	next.UpdatedAtUnixMillis = nowUnixMillis
	if err := ValidatePlan(next); err != nil {
		return PlanState{}, false, err
	}
	return next, advanced, nil
}

func ApplyFailure(
	state PlanState,
	operationID string,
	family string,
	code string,
	nowUnixMillis int64,
) (PlanState, error) {
	if err := ValidatePlan(state); err != nil {
		return PlanState{}, err
	}
	if err := validateText("operation_id", operationID, 256, true); err != nil {
		return PlanState{}, err
	}
	if err := validateText("failure_family", family, 128, true); err != nil {
		return PlanState{}, err
	}
	if err := validateText("outcome_code", code, 128, false); err != nil {
		return PlanState{}, err
	}
	next := clonePlan(state)
	index := currentStepIndex(next)
	if index < 0 || next.Status != PlanActive {
		return PlanState{}, fmt.Errorf("%w: plan has no active step", ErrConflict)
	}
	next.Steps[index].Attempt++
	next.LastOperationID = operationID
	next.LastOutcomeCode = code
	if next.LastFailureFamily == family {
		next.ConsecutiveFailures++
	} else {
		next.LastFailureFamily = family
		next.ConsecutiveFailures = 1
	}
	if next.Steps[index].Attempt >= next.Steps[index].MaxAttempts {
		next.Steps[index].Status = StepBlocked
		next.Steps[index].BlockedReason = code
		next.Status = PlanBlocked
	}
	next.Revision++
	next.UpdatedAtUnixMillis = nowUnixMillis
	if err := ValidatePlan(next); err != nil {
		return PlanState{}, err
	}
	return next, nil
}

func ShouldReplan(policy ReplanPolicy, input ReplanInput) bool {
	threshold := policy.FailureThreshold
	if threshold == 0 {
		threshold = 3
	}
	maximum := policy.MaxReplans
	if maximum == 0 {
		maximum = 3
	}
	if input.ReplanCount >= maximum {
		return false
	}
	switch input.Reason {
	case ReplanGoalChanged, ReplanManualAuthorized:
		return input.PlayerAuthorized
	case ReplanFailureThresholdReached:
		return input.HasAuthoritativeProof && input.ConsecutiveFailures >= threshold
	case ReplanRequiredCapabilityMissing, ReplanEpochInvalidated:
		return input.HasAuthoritativeProof
	default:
		return false
	}
}

func ValidatePlan(state PlanState) error {
	if state.SchemaVersion != SchemaVersion {
		return invalid("schema_version", "is unsupported")
	}
	for field, value := range map[string]string{
		"plan_id": state.PlanID, "task_id": state.TaskID, "session_id": state.SessionID,
		"host_id": state.HostID, "world_id": state.WorldID, "actor_id": state.ActorID,
		"controller_id": state.ControllerID,
	} {
		if err := validateText(field, value, 256, true); err != nil {
			return err
		}
	}
	if err := validateText("goal", state.Goal, 2_000, true); err != nil {
		return err
	}
	if state.GoalDigest != digestText(state.Goal) {
		return invalid("goal_digest", "does not match goal")
	}
	if !validPlanningMode(state.PlanningMode) || !validControllerSource(state.ControllerSource) ||
		!validPlanStatus(state.Status) {
		return invalid("enum", "contains an unsupported value")
	}
	if len(state.Steps) == 0 || len(state.Steps) > maxSteps {
		return invalid("steps", "must contain between 1 and 16 steps")
	}
	if len(state.SuccessConditions) > maxConditions || len(state.Evidence) > maxEvidence {
		return invalid("conditions", "exceed their bounds")
	}
	if state.Revision == 0 || state.Revision > maxWireInteger || state.MaxReplans > 32 ||
		state.ReplanCount > state.MaxReplans || state.BasedOnObservationSequence == 0 ||
		state.BasedOnObservationSequence > maxWireInteger || state.CreatedAtUnixMillis < 0 ||
		state.UpdatedAtUnixMillis < state.CreatedAtUnixMillis {
		return invalid("revision", "or plan counters are invalid")
	}
	if err := state.BasedOnEpoch.Validate("based_on_epoch"); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	conditionIDs := make(map[string]EvidenceKind)
	if err := validateConditions(state.SuccessConditions, conditionIDs); err != nil {
		return err
	}
	stepIDs := make(map[string]struct{}, len(state.Steps))
	active := 0
	for index := range state.Steps {
		step := state.Steps[index]
		if err := (host.PlanStepRef{
			PlanID: state.PlanID, PlanRevision: state.Revision, StepID: step.StepID,
		}).Validate("plan_step_ref"); err != nil {
			return fmt.Errorf("%w: steps[%d]: %v", ErrInvalid, index, err)
		}
		if err := validateStep(step, conditionIDs); err != nil {
			return fmt.Errorf("%w: steps[%d]: %v", ErrInvalid, index, err)
		}
		if _, duplicate := stepIDs[step.StepID]; duplicate {
			return invalid("steps", "contain duplicate step IDs")
		}
		stepIDs[step.StepID] = struct{}{}
		if step.Status == StepActive {
			active++
			if state.CurrentStepID != step.StepID {
				return invalid("current_step_id", "does not match active step")
			}
		}
	}
	if state.Status == PlanCompleted || state.Status == PlanFailed || state.Status == PlanCancelled {
		if active != 0 || state.CurrentStepID != "" {
			return invalid("status", "terminal plan cannot retain an active step")
		}
	} else if state.Status == PlanActive && active != 1 {
		waitingForPlanEvidence := active == 0 && allStepsTerminalSuccess(state.Steps) &&
			!conditionsSatisfied(state.SuccessConditions, state.Evidence)
		if !waitingForPlanEvidence {
			return invalid("steps", "active plan must have one active step or await final evidence")
		}
	}
	for _, evidence := range state.Evidence {
		if err := validateEvidence(evidence, state); err != nil {
			return err
		}
	}
	return nil
}

func validateStep(step PlanStep, conditionIDs map[string]EvidenceKind) error {
	if err := validateText("step_id", step.StepID, 128, true); err != nil {
		return err
	}
	if err := validateText("title", step.Title, 200, true); err != nil {
		return err
	}
	if err := validateText("objective", step.Objective, 1_000, true); err != nil {
		return err
	}
	if !validStepStatus(step.Status) || step.MaxAttempts == 0 || step.MaxAttempts > 32 ||
		step.Attempt > step.MaxAttempts || len(step.SuccessConditions) == 0 ||
		len(step.SuccessConditions) > maxStepConditions ||
		len(step.CapabilityHints) > 32 || len(step.Evidence) > maxEvidence {
		return invalid("step", "status, bounds, or attempts are invalid")
	}
	if err := validateText("blocked_reason", step.BlockedReason, 500, false); err != nil {
		return err
	}
	if err := validateConditions(step.SuccessConditions, conditionIDs); err != nil {
		return err
	}
	seenCapabilities := make(map[host.CapabilityRef]struct{}, len(step.CapabilityHints))
	for index, ref := range step.CapabilityHints {
		if err := ref.Validate(fmt.Sprintf("capability_hints[%d]", index)); err != nil {
			return err
		}
		if _, duplicate := seenCapabilities[ref]; duplicate {
			return invalid("capability_hints", "contain duplicates")
		}
		seenCapabilities[ref] = struct{}{}
	}
	for _, condition := range step.SuccessConditions {
		if condition.Capability == nil {
			continue
		}
		if _, exists := seenCapabilities[*condition.Capability]; !exists {
			return invalid("condition.capability", "must also appear in capability_hints")
		}
	}
	return nil
}

func validateConditions(conditions []PlanCondition, all map[string]EvidenceKind) error {
	for _, condition := range conditions {
		if err := validateText("condition_id", condition.ConditionID, 128, true); err != nil {
			return err
		}
		if !validEvidenceKind(condition.Kind) {
			return invalid("condition.kind", "is unsupported")
		}
		if err := validateText("condition.summary", condition.Summary, 500, true); err != nil {
			return err
		}
		switch condition.Kind {
		case EvidenceOperationOutcome:
			if condition.Capability == nil || condition.FactID != "" || condition.FactValueJSON != "" {
				return invalid("condition", "operation outcomes require one capability selector")
			}
			if err := condition.Capability.Validate("condition.capability"); err != nil {
				return err
			}
		case EvidenceObservationFact:
			if condition.Capability != nil ||
				validateText("condition.fact_id", condition.FactID, 128, true) != nil ||
				validateScalarJSON(condition.FactValueJSON) != nil {
				return invalid("condition", "observation facts require one fact_id and scalar value selector")
			}
		}
		if _, duplicate := all[condition.ConditionID]; duplicate {
			return invalid("conditions", "contain duplicate IDs")
		}
		all[condition.ConditionID] = condition.Kind
	}
	return nil
}

func validateEvidence(evidence PlanEvidence, state PlanState) error {
	for field, value := range map[string]string{
		"evidence_id": evidence.EvidenceID, "condition_id": evidence.ConditionID,
		"digest": evidence.Digest,
	} {
		if err := validateText(field, value, 256, true); err != nil {
			return err
		}
	}
	if len(evidence.Digest) != 64 {
		return invalid("digest", "must be SHA-256")
	}
	if !validEvidenceKind(evidence.Kind) || evidence.ObservationSequence == 0 ||
		evidence.ObservationSequence > maxWireInteger || evidence.RecordedAtUnixMillis < 0 {
		return invalid("evidence", "contains invalid values")
	}
	if err := evidence.Epoch.Validate("evidence.epoch"); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if evidence.Epoch != state.BasedOnEpoch || evidence.ObservationSequence < state.BasedOnObservationSequence {
		return invalid("evidence", "is stale for this plan")
	}
	if evidence.Kind == EvidenceOperationOutcome {
		if err := validateText("operation_id", evidence.OperationID, 256, true); err != nil {
			return err
		}
	} else if evidence.OperationID != "" {
		return invalid("operation_id", "is only valid for operation outcome evidence")
	}
	return nil
}

func conditionInList(conditions []PlanCondition, id string, kind EvidenceKind) bool {
	return slices.ContainsFunc(conditions, func(condition PlanCondition) bool {
		return condition.ConditionID == id && condition.Kind == kind
	})
}

func conditionsSatisfied(conditions []PlanCondition, evidence []PlanEvidence) bool {
	for _, condition := range conditions {
		if !slices.ContainsFunc(evidence, func(item PlanEvidence) bool {
			return item.ConditionID == condition.ConditionID && item.Kind == condition.Kind
		}) {
			return false
		}
	}
	return true
}

func currentStepIndex(state PlanState) int {
	for index := range state.Steps {
		if state.Steps[index].StepID == state.CurrentStepID {
			return index
		}
	}
	return -1
}

func allStepsTerminalSuccess(steps []PlanStep) bool {
	return !slices.ContainsFunc(steps, func(step PlanStep) bool {
		return step.Status != StepCompleted && step.Status != StepSkipped
	})
}

func evidenceInSteps(steps []PlanStep, id string) bool {
	return slices.ContainsFunc(steps, func(step PlanStep) bool { return findEvidence(step.Evidence, id) })
}

func findEvidence(items []PlanEvidence, id string) bool {
	return slices.ContainsFunc(items, func(item PlanEvidence) bool { return item.EvidenceID == id })
}

func clonePlan(state PlanState) PlanState {
	state.Steps = cloneSteps(state.Steps)
	state.SuccessConditions = cloneConditions(state.SuccessConditions)
	state.Evidence = append([]PlanEvidence(nil), state.Evidence...)
	return state
}

func cloneSteps(steps []PlanStep) []PlanStep {
	result := make([]PlanStep, len(steps))
	for index := range steps {
		result[index] = steps[index]
		result[index].SuccessConditions = cloneConditions(steps[index].SuccessConditions)
		result[index].CapabilityHints = append([]host.CapabilityRef(nil), steps[index].CapabilityHints...)
		result[index].Evidence = append([]PlanEvidence(nil), steps[index].Evidence...)
	}
	return result
}

func draftSteps(steps []StepDraft) []PlanStep {
	result := make([]PlanStep, len(steps))
	for index := range steps {
		result[index] = PlanStep{
			StepID: steps[index].StepID, Title: steps[index].Title,
			Objective:         steps[index].Objective,
			SuccessConditions: cloneConditions(steps[index].SuccessConditions),
			CapabilityHints:   append([]host.CapabilityRef(nil), steps[index].CapabilityHints...),
			MaxAttempts:       steps[index].MaxAttempts,
		}
	}
	return result
}

func cloneConditions(items []PlanCondition) []PlanCondition {
	result := append([]PlanCondition(nil), items...)
	for index := range result {
		if result[index].Capability != nil {
			capability := *result[index].Capability
			result[index].Capability = &capability
		}
	}
	return result
}

// OperationConditionIDs returns only current-step conditions whose declared
// capability can be proven by the selected Host action.
func OperationConditionIDs(state PlanState, capability host.CapabilityRef) []string {
	index := currentStepIndex(state)
	if index < 0 {
		return nil
	}
	result := make([]string, 0, len(state.Steps[index].SuccessConditions))
	for _, condition := range state.Steps[index].SuccessConditions {
		if condition.Kind == EvidenceOperationOutcome && condition.Capability != nil &&
			*condition.Capability == capability {
			result = append(result, condition.ConditionID)
		}
	}
	for _, condition := range state.SuccessConditions {
		if condition.Kind == EvidenceOperationOutcome && condition.Capability != nil &&
			*condition.Capability == capability {
			result = append(result, condition.ConditionID)
		}
	}
	return result
}

// ObservationConditionMatches verifies both the Host fact identity and its
// exact scalar value before it can become plan evidence.
func ObservationConditionMatches(condition PlanCondition, fact host.ObservationFact) bool {
	if condition.Kind != EvidenceObservationFact || condition.FactID != fact.FactID {
		return false
	}
	var expected bytes.Buffer
	if json.Compact(&expected, []byte(condition.FactValueJSON)) != nil {
		return false
	}
	var actual bytes.Buffer
	if json.Compact(&actual, fact.Value) != nil {
		return false
	}
	return bytes.Equal(expected.Bytes(), actual.Bytes())
}

func digestText(value string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return hex.EncodeToString(digest[:])
}

func validateText(field, value string, maximum int, required bool) error {
	if !utf8.ValidString(value) || strings.ContainsRune(value, 0) || len(value) > maximum ||
		(required && strings.TrimSpace(value) == "") {
		return invalid(field, "is invalid or exceeds its bound")
	}
	return nil
}

func invalid(field, message string) error {
	return fmt.Errorf("%w: %s %s", ErrInvalid, field, message)
}

func validateScalarJSON(value string) error {
	if len(value) == 0 || len(value) > 1_024 || !utf8.ValidString(value) {
		return ErrInvalid
	}
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return ErrInvalid
	}
	switch decoded.(type) {
	case nil, bool, string, json.Number:
		return nil
	default:
		return ErrInvalid
	}
}

func validPlanningMode(value PlanningMode) bool {
	return value == PlanningDisabled || value == PlanningAuto || value == PlanningRequired
}

func validControllerSource(value ControllerSource) bool {
	return value == ControllerInternal || value == ControllerExternal
}

func validPlanStatus(value PlanStatus) bool {
	switch value {
	case PlanPlanned, PlanActive, PlanBlocked, PlanPaused, PlanCompleted, PlanFailed, PlanCancelled:
		return true
	default:
		return false
	}
}

func validStepStatus(value StepStatus) bool {
	switch value {
	case StepPending, StepActive, StepBlocked, StepCompleted, StepFailed, StepSkipped:
		return true
	default:
		return false
	}
}

func validEvidenceKind(value EvidenceKind) bool {
	switch value {
	case EvidenceOperationOutcome, EvidenceObservationFact:
		return true
	default:
		return false
	}
}
