package cognition

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"

	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/taskstate"
	"github.com/sunrioa/rin/timeline"
)

const TaskSnapshotVersion = "rin.cognition.tasks/v3"

var ErrTaskRevisionConflict = errors.New("cognition task revision conflict")

type TaskStatus string

const (
	TaskActive              TaskStatus = "active"
	TaskWaitingConfirmation TaskStatus = "waiting-confirmation"
	TaskCancelling          TaskStatus = "cancelling"
	TaskPaused              TaskStatus = "paused"
	TaskCompleted           TaskStatus = "completed"
	TaskFailed              TaskStatus = "failed"
	TaskOutcomeUnknown      TaskStatus = "outcome-unknown"
	TaskCancelled           TaskStatus = "cancelled"
)

type TaskBudget struct {
	MaxSteps       uint32 `json:"max_steps"`
	MaxModelCalls  uint32 `json:"max_model_calls"`
	MaxModelTokens uint64 `json:"max_model_tokens"`
	MaxActions     uint32 `json:"max_actions"`
}

type SkillLearningStatus string

const (
	SkillLearningPending SkillLearningStatus = "pending"
	SkillLearningDrafted SkillLearningStatus = "drafted"
	SkillLearningEnabled SkillLearningStatus = "enabled"
	SkillLearningSkipped SkillLearningStatus = "skipped"
	SkillLearningFailed  SkillLearningStatus = "failed"
)

type SkillLearningState struct {
	Status   SkillLearningStatus `json:"status"`
	Attempts uint32              `json:"attempts"`
	SkillID  string              `json:"skill_id,omitempty"`
	Digest   string              `json:"digest,omitempty"`
	Code     string              `json:"code,omitempty"`
}

// TaskOperationResult is the latest authoritative result available to the next
// decision round. Keeping one bounded result avoids replaying the task log.
type TaskOperationResult struct {
	OperationID string             `json:"operation_id"`
	Capability  host.CapabilityRef `json:"capability"`
	Status      string             `json:"status"`
	Summary     string             `json:"summary"`
	Output      json.RawMessage    `json:"output,omitempty"`
}

type TaskEvent struct {
	Sequence            uint64                      `json:"sequence"`
	Kind                string                      `json:"kind"`
	Step                uint32                      `json:"step"`
	Code                string                      `json:"code,omitempty"`
	Summary             string                      `json:"summary,omitempty"`
	OperationID         string                      `json:"operation_id,omitempty"`
	AtUnixMillis        int64                       `json:"at_unix_millis"`
	ObservationID       string                      `json:"observation_id,omitempty"`
	ObservationSequence uint64                      `json:"observation_sequence,omitempty"`
	Epoch               *host.Epoch                 `json:"epoch,omitempty"`
	Capability          *host.CapabilityRef         `json:"capability,omitempty"`
	SkillRefs           []timeline.SkillContextRef  `json:"skill_refs,omitempty"`
	MemoryContextRefs   []timeline.MemoryContextRef `json:"memory_context_refs,omitempty"`
	Model               *timeline.ModelUsage        `json:"model_usage,omitempty"`
	Policy              *timeline.PolicySummary     `json:"policy,omitempty"`
	Operation           *timeline.OperationSummary  `json:"operation,omitempty"`
	PlanID              string                      `json:"plan_id,omitempty"`
	PlanRevision        uint64                      `json:"plan_revision,omitempty"`
	PlanStepID          string                      `json:"plan_step_id,omitempty"`
	Signal              *timeline.SignalContextRef  `json:"signal,omitempty"`
}

// TaskSession contains every decision-side value needed to resume without
// regenerating or mutating an already selected action.
type TaskSession struct {
	TaskID              string                 `json:"task_id"`
	SessionID           string                 `json:"session_id"`
	HostID              string                 `json:"host_id"`
	WorldID             string                 `json:"world_id"`
	ActorID             string                 `json:"actor_id"`
	ControllerID        string                 `json:"controller_id"`
	Goal                string                 `json:"goal"`
	Tags                []string               `json:"tags,omitempty"`
	AllowedCapabilities []string               `json:"allowed_capabilities,omitempty"`
	PlanningMode        taskstate.PlanningMode `json:"planning_mode"`
	PlanID              string                 `json:"plan_id,omitempty"`
	PlanRevision        uint64                 `json:"plan_revision,omitempty"`
	CurrentPlanStepID   string                 `json:"current_plan_step_id,omitempty"`

	Status    TaskStatus `json:"status"`
	PauseCode string     `json:"pause_code,omitempty"`
	Revision  uint64     `json:"revision"`
	Step      uint32     `json:"step"`
	Budget    TaskBudget `json:"budget"`

	ModelCalls  uint32 `json:"model_calls"`
	ModelTokens uint64 `json:"model_tokens"`
	ActionCount uint32 `json:"action_count"`

	ControllerLease     controlplane.ControllerLease `json:"controller_lease"`
	PendingAction       *host.ActionRequest          `json:"pending_action,omitempty"`
	PendingActionMacro  bool                         `json:"pending_action_is_macro,omitempty"`
	PendingOperationID  string                       `json:"pending_operation_id,omitempty"`
	MacroOperationID    string                       `json:"macro_operation_id,omitempty"`
	PendingMemories     []MemoryRecord               `json:"pending_memories,omitempty"`
	SkillLearning       *SkillLearningState          `json:"skill_learning,omitempty"`
	LastOperationResult *TaskOperationResult         `json:"last_operation_result,omitempty"`

	LastObservationID   string      `json:"last_observation_id,omitempty"`
	LastObservationSeq  uint64      `json:"last_observation_sequence,omitempty"`
	EventSequence       uint64      `json:"event_sequence"`
	History             []TaskEvent `json:"history,omitempty"`
	CreatedAtUnixMillis int64       `json:"created_at_unix_millis"`
	UpdatedAtUnixMillis int64       `json:"updated_at_unix_millis"`
}

type TaskSnapshot struct {
	Version  string        `json:"version"`
	Revision uint64        `json:"revision"`
	Tasks    []TaskSession `json:"tasks"`
}

type TaskStore interface {
	Create(context.Context, TaskSession) (TaskSession, error)
	Load(context.Context, string) (TaskSession, error)
	CompareAndSwap(context.Context, uint64, TaskSession) (TaskSession, error)
	Snapshot(context.Context) (TaskSnapshot, error)
}

type LocalTaskStore struct {
	mu       sync.RWMutex
	revision uint64
	maxTasks uint32
	tasks    map[string]TaskSession
}

func NewLocalTaskStore(maxTasks uint32) (*LocalTaskStore, error) {
	if maxTasks == 0 {
		maxTasks = 1_024
	}
	if maxTasks > 100_000 {
		return nil, errors.New("task store capacity is too large")
	}
	return &LocalTaskStore{
		revision: 1, maxTasks: maxTasks, tasks: make(map[string]TaskSession),
	}, nil
}

func RestoreLocalTaskStore(maxTasks uint32, snapshot TaskSnapshot) (*LocalTaskStore, error) {
	store, err := NewLocalTaskStore(maxTasks)
	if err != nil {
		return nil, err
	}
	if snapshot.Version != TaskSnapshotVersion || snapshot.Revision == 0 {
		return nil, errors.New("task snapshot version or revision is invalid")
	}
	if len(snapshot.Tasks) > int(store.maxTasks) {
		return nil, ErrProviderCapacity
	}
	for index, task := range snapshot.Tasks {
		sealed, err := sealTaskSession(task)
		if err != nil {
			return nil, fmt.Errorf("tasks[%d]: %w", index, err)
		}
		if sealed.Revision == 0 {
			return nil, fmt.Errorf("tasks[%d] has no revision", index)
		}
		if _, exists := store.tasks[sealed.TaskID]; exists {
			return nil, fmt.Errorf("tasks[%d]: %w", index, ErrProviderConflict)
		}
		store.tasks[sealed.TaskID] = sealed
	}
	store.revision = snapshot.Revision
	return store, nil
}

func (store *LocalTaskStore) Create(
	ctx context.Context,
	task TaskSession,
) (TaskSession, error) {
	if err := requireMemoryContext(ctx); err != nil {
		return TaskSession{}, err
	}
	if task.Revision != 0 {
		return TaskSession{}, errors.New("new task revision must be zero")
	}
	task.Revision = 1
	sealed, err := sealTaskSession(task)
	if err != nil {
		return TaskSession{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, exists := store.tasks[sealed.TaskID]; exists {
		return TaskSession{}, ErrProviderConflict
	}
	if len(store.tasks) >= int(store.maxTasks) {
		return TaskSession{}, ErrProviderCapacity
	}
	store.tasks[sealed.TaskID] = sealed
	store.revision++
	return cloneTaskSession(sealed), nil
}

func (store *LocalTaskStore) Load(
	ctx context.Context,
	taskID string,
) (TaskSession, error) {
	if err := requireMemoryContext(ctx); err != nil {
		return TaskSession{}, err
	}
	if err := validateTaskID(taskID); err != nil {
		return TaskSession{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	task, exists := store.tasks[taskID]
	if !exists {
		return TaskSession{}, ErrProviderNotFound
	}
	return cloneTaskSession(task), nil
}

func (store *LocalTaskStore) CompareAndSwap(
	ctx context.Context,
	expectedRevision uint64,
	task TaskSession,
) (TaskSession, error) {
	if err := requireMemoryContext(ctx); err != nil {
		return TaskSession{}, err
	}
	if expectedRevision == 0 || task.Revision != expectedRevision {
		return TaskSession{}, errors.New("task update must carry the expected revision")
	}
	task.Revision++
	sealed, err := sealTaskSession(task)
	if err != nil {
		return TaskSession{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	current, exists := store.tasks[sealed.TaskID]
	if !exists {
		return TaskSession{}, ErrProviderNotFound
	}
	if current.Revision != expectedRevision {
		return TaskSession{}, ErrTaskRevisionConflict
	}
	store.tasks[sealed.TaskID] = sealed
	store.revision++
	return cloneTaskSession(sealed), nil
}

func (store *LocalTaskStore) Snapshot(ctx context.Context) (TaskSnapshot, error) {
	if err := requireMemoryContext(ctx); err != nil {
		return TaskSnapshot{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	snapshot := TaskSnapshot{
		Version: TaskSnapshotVersion, Revision: store.revision,
		Tasks: make([]TaskSession, 0, len(store.tasks)),
	}
	for _, task := range store.tasks {
		snapshot.Tasks = append(snapshot.Tasks, cloneTaskSession(task))
	}
	slices.SortFunc(snapshot.Tasks, func(left, right TaskSession) int {
		return compareString(left.TaskID, right.TaskID)
	})
	return snapshot, nil
}

func sealTaskSession(task TaskSession) (TaskSession, error) {
	if err := validateTaskID(task.TaskID); err != nil {
		return TaskSession{}, err
	}
	for field, value := range map[string]string{
		"session_id": task.SessionID, "host_id": task.HostID, "world_id": task.WorldID,
		"actor_id": task.ActorID, "controller_id": task.ControllerID,
	} {
		if err := validateProviderID(field, value); err != nil {
			return TaskSession{}, err
		}
	}
	if err := validateProviderText("goal", task.Goal, 2_000, true); err != nil {
		return TaskSession{}, err
	}
	if task.PlanningMode == "" {
		task.PlanningMode = taskstate.PlanningDisabled
	}
	switch task.PlanningMode {
	case taskstate.PlanningDisabled, taskstate.PlanningAuto, taskstate.PlanningRequired:
	default:
		return TaskSession{}, errors.New("task planning mode is invalid")
	}
	if task.PlanID == "" {
		if task.PlanRevision != 0 || task.CurrentPlanStepID != "" {
			return TaskSession{}, errors.New("plan revision or step requires plan_id")
		}
	} else {
		if task.PlanningMode == taskstate.PlanningDisabled || task.PlanRevision == 0 {
			return TaskSession{}, errors.New("task plan reference is invalid")
		}
		stepID := task.CurrentPlanStepID
		if stepID == "" {
			stepID = "step.terminal"
		}
		if err := (host.PlanStepRef{
			PlanID: task.PlanID, PlanRevision: task.PlanRevision, StepID: stepID,
		}).Validate("plan_ref"); err != nil {
			return TaskSession{}, err
		}
	}
	var err error
	if task.Tags, err = normalizeProviderIDs("tags", task.Tags, 32); err != nil {
		return TaskSession{}, err
	}
	if task.AllowedCapabilities, err = normalizeProviderIDs(
		"allowed_capabilities",
		task.AllowedCapabilities,
		128,
	); err != nil {
		return TaskSession{}, err
	}
	if !validTaskStatus(task.Status) || task.Revision == 0 || task.Revision > maxProviderWireInteger {
		return TaskSession{}, errors.New("task status or revision is invalid")
	}
	if err := validateProviderText("pause_code", task.PauseCode, 96, false); err != nil {
		return TaskSession{}, err
	}
	switch task.Status {
	case TaskPaused, TaskFailed, TaskOutcomeUnknown:
		if task.PauseCode == "" {
			return TaskSession{}, errors.New("paused or failed task requires a status code")
		}
	case TaskActive, TaskWaitingConfirmation, TaskCancelling, TaskCompleted, TaskCancelled:
		if task.PauseCode != "" {
			return TaskSession{}, errors.New("non-paused task must not retain a pause code")
		}
	}
	budget, err := normalizeTaskBudget(task.Budget)
	if err != nil {
		return TaskSession{}, err
	}
	task.Budget = budget
	if task.Step > budget.MaxSteps || task.ModelCalls > budget.MaxModelCalls ||
		task.ModelTokens > budget.MaxModelTokens || task.ActionCount > budget.MaxActions {
		return TaskSession{}, errors.New("task usage exceeds its budget")
	}
	if err := validateTaskLease(task); err != nil {
		return TaskSession{}, err
	}
	if task.PendingAction != nil {
		request := cloneTaskActionRequest(*task.PendingAction)
		if err := host.ValidateActionRequest(request); err != nil {
			return TaskSession{}, fmt.Errorf("pending_action: %w", err)
		}
		if request.TaskID != task.TaskID || request.ControllerID != task.ControllerID ||
			request.ActorID != task.ActorID {
			return TaskSession{}, errors.New("pending action does not belong to the task")
		}
		if request.PlanStep != nil && (request.PlanStep.PlanID != task.PlanID ||
			request.PlanStep.PlanRevision != task.PlanRevision ||
			request.PlanStep.StepID != task.CurrentPlanStepID) {
			return TaskSession{}, errors.New("pending action does not match the task plan step")
		}
		if !taskAllowsCapability(task, request.Capability.ID) {
			return TaskSession{}, errors.New("pending action exceeds the task capability scope")
		}
		task.PendingAction = &request
	} else if task.PendingActionMacro || task.PendingOperationID != "" ||
		len(task.PendingMemories) != 0 {
		return TaskSession{}, errors.New("pending operation or memories require a pending action")
	}
	if task.PendingActionMacro && task.MacroOperationID != "" {
		return TaskSession{}, errors.New("nested pending macros are not supported")
	}
	if task.PendingOperationID != "" {
		if err := validateProviderID("pending_operation_id", task.PendingOperationID); err != nil {
			return TaskSession{}, err
		}
	}
	if task.MacroOperationID != "" {
		if err := validateProviderID("macro_operation_id", task.MacroOperationID); err != nil {
			return TaskSession{}, err
		}
	}
	if task.Status == TaskWaitingConfirmation && task.PendingOperationID == "" &&
		task.MacroOperationID == "" {
		return TaskSession{}, errors.New("waiting task has no pending operation")
	}
	if task.Status == TaskCancelling && task.MacroOperationID == "" &&
		(task.PendingAction == nil || task.PendingOperationID == "") {
		return TaskSession{}, errors.New("cancelling task requires a pending or macro operation")
	}
	if task.Status == TaskOutcomeUnknown && task.MacroOperationID == "" &&
		(task.PendingAction == nil || task.PendingOperationID == "") {
		return TaskSession{}, errors.New("outcome-unknown task requires reconciliation state")
	}
	if (task.Status == TaskCompleted || task.Status == TaskFailed || task.Status == TaskCancelled) &&
		(task.PendingAction != nil || task.MacroOperationID != "") {
		return TaskSession{}, errors.New("terminal task must not retain an active operation")
	}
	if len(task.PendingMemories) > 8 {
		return TaskSession{}, errors.New("task has too many pending memories")
	}
	if task.SkillLearning != nil {
		learning := *task.SkillLearning
		if err := validateSkillLearningState(learning); err != nil {
			return TaskSession{}, err
		}
		task.SkillLearning = &learning
	}
	if task.LastOperationResult != nil {
		result, err := sealTaskOperationResult(*task.LastOperationResult)
		if err != nil {
			return TaskSession{}, fmt.Errorf("last_operation_result: %w", err)
		}
		task.LastOperationResult = &result
	}
	task.PendingMemories = append([]MemoryRecord(nil), task.PendingMemories...)
	for index, record := range task.PendingMemories {
		sealed, err := sealMemoryRecord(record)
		if err != nil {
			return TaskSession{}, fmt.Errorf("pending_memories[%d]: %w", index, err)
		}
		if sealed.Namespace.SessionID != task.SessionID || sealed.Namespace.ActorID != task.ActorID ||
			sealed.Namespace.ControllerID != task.ControllerID || sealed.Namespace.Domain != MemoryControllerBelief {
			return TaskSession{}, errors.New("pending memory is outside the task's belief namespace")
		}
		task.PendingMemories[index] = sealed
	}
	if task.LastObservationID != "" {
		if err := validateProviderID("last_observation_id", task.LastObservationID); err != nil {
			return TaskSession{}, err
		}
		if task.LastObservationSeq == 0 || task.LastObservationSeq > maxProviderWireInteger {
			return TaskSession{}, errors.New("last observation sequence is invalid")
		}
	} else if task.LastObservationSeq != 0 {
		return TaskSession{}, errors.New("last observation id is missing")
	}
	if len(task.History) > 512 {
		return TaskSession{}, errors.New("task history exceeds 512 events")
	}
	legacySequence := task.EventSequence == 0 && len(task.History) != 0
	task.History = append([]TaskEvent(nil), task.History...)
	for index, event := range task.History {
		if legacySequence {
			event.Sequence = uint64(index + 1)
		}
		sealed, err := sealTaskEvent(task, event, index)
		if err != nil {
			return TaskSession{}, err
		}
		if index > 0 && sealed.Sequence <= task.History[index-1].Sequence {
			return TaskSession{}, errors.New("task history sequence must increase")
		}
		task.History[index] = sealed
	}
	if legacySequence {
		task.EventSequence = uint64(len(task.History))
	}
	if len(task.History) != 0 && task.EventSequence < task.History[len(task.History)-1].Sequence {
		return TaskSession{}, errors.New("task event sequence predates history")
	}
	if task.EventSequence > maxProviderWireInteger {
		return TaskSession{}, errors.New("task event sequence is out of bounds")
	}
	if task.CreatedAtUnixMillis < 0 || task.UpdatedAtUnixMillis < task.CreatedAtUnixMillis ||
		task.CreatedAtUnixMillis > maxProviderWireInteger ||
		task.UpdatedAtUnixMillis > maxProviderWireInteger {
		return TaskSession{}, errors.New("task timestamps are invalid")
	}
	if err := validateTaskTimelineHistory(task); err != nil {
		return TaskSession{}, fmt.Errorf("task timeline: %w", err)
	}
	return cloneTaskSession(task), nil
}

func validateTaskLease(task TaskSession) error {
	lease := task.ControllerLease
	for field, value := range map[string]string{
		"controller_lease.lease_id":      lease.LeaseID,
		"controller_lease.controller_id": lease.ControllerID,
		"controller_lease.principal_id":  lease.PrincipalID,
		"controller_lease.host_id":       lease.HostID,
		"controller_lease.world_id":      lease.WorldID,
		"controller_lease.actor_id":      lease.ActorID,
	} {
		if err := validateProviderID(field, value); err != nil {
			return err
		}
	}
	if lease.ControllerID != task.ControllerID || lease.HostID != task.HostID ||
		lease.WorldID != task.WorldID || lease.ActorID != task.ActorID ||
		lease.Epoch.SessionID != task.SessionID || lease.ExpiresAtUnixMillis <= lease.AcquiredAtUnixMillis {
		return errors.New("controller lease does not belong to the task")
	}
	if err := lease.Epoch.Validate("controller_lease.epoch"); err != nil {
		return err
	}
	if lease.AuthorityRevision == 0 || lease.AuthorityRevision > maxProviderWireInteger ||
		lease.AcquiredAtUnixMillis < 0 || lease.AcquiredAtUnixMillis > maxProviderWireInteger ||
		lease.ExpiresAtUnixMillis > maxProviderWireInteger {
		return errors.New("controller lease revision or timestamps are invalid")
	}
	if lease.Source != controlplane.DecisionInternal && lease.Source != controlplane.DecisionExternal {
		return errors.New("controller lease has an invalid decision source")
	}
	if lease.PersonaMode != controlplane.PersonaCharacterBound &&
		lease.PersonaMode != controlplane.PersonaAgentAvatar {
		return errors.New("controller lease has an invalid persona mode")
	}
	return nil
}

func normalizeTaskBudget(budget TaskBudget) (TaskBudget, error) {
	if budget.MaxSteps == 0 {
		budget.MaxSteps = 64
	}
	if budget.MaxModelCalls == 0 {
		budget.MaxModelCalls = 128
	}
	if budget.MaxModelTokens == 0 {
		budget.MaxModelTokens = 250_000
	}
	if budget.MaxActions == 0 {
		budget.MaxActions = 64
	}
	if budget.MaxSteps > 10_000 || budget.MaxModelCalls > 20_000 ||
		budget.MaxModelTokens > 100_000_000 || budget.MaxActions > 10_000 {
		return TaskBudget{}, errors.New("task budget exceeds its bounds")
	}
	return budget, nil
}

func validTaskStatus(status TaskStatus) bool {
	switch status {
	case TaskActive, TaskWaitingConfirmation, TaskCancelling, TaskPaused, TaskCompleted,
		TaskFailed, TaskOutcomeUnknown, TaskCancelled:
		return true
	default:
		return false
	}
}

func terminalTaskStatus(status TaskStatus) bool {
	switch status {
	case TaskCompleted, TaskFailed, TaskOutcomeUnknown, TaskCancelled:
		return true
	default:
		return false
	}
}

func validateTaskID(taskID string) error {
	if len(taskID) > 64 {
		return errors.New("task_id must contain at most 64 bytes")
	}
	return validateProviderID("task_id", taskID)
}

func cloneTaskSession(task TaskSession) TaskSession {
	task.Tags = append([]string(nil), task.Tags...)
	task.AllowedCapabilities = append([]string(nil), task.AllowedCapabilities...)
	if task.SkillLearning != nil {
		learning := *task.SkillLearning
		task.SkillLearning = &learning
	}
	if task.LastOperationResult != nil {
		result := *task.LastOperationResult
		result.Output = append(json.RawMessage(nil), result.Output...)
		task.LastOperationResult = &result
	}
	if task.PendingAction != nil {
		request := cloneTaskActionRequest(*task.PendingAction)
		task.PendingAction = &request
	}
	pendingMemories := task.PendingMemories
	task.PendingMemories = make([]MemoryRecord, len(pendingMemories))
	for index, record := range pendingMemories {
		task.PendingMemories[index] = cloneMemoryRecord(record)
	}
	history := task.History
	task.History = make([]TaskEvent, len(history))
	for index, event := range history {
		task.History[index] = cloneTaskEvent(event)
	}
	return task
}

func sealTaskOperationResult(result TaskOperationResult) (TaskOperationResult, error) {
	if err := validateProviderID("operation_id", result.OperationID); err != nil {
		return TaskOperationResult{}, err
	}
	if err := result.Capability.Validate("capability"); err != nil {
		return TaskOperationResult{}, err
	}
	if err := validateProviderID("status", result.Status); err != nil {
		return TaskOperationResult{}, err
	}
	if err := validateProviderText("summary", result.Summary, 2_000, true); err != nil {
		return TaskOperationResult{}, err
	}
	if len(result.Output) > 65_536 {
		return TaskOperationResult{}, errors.New("operation output exceeds 65536 bytes")
	}
	if len(result.Output) != 0 {
		var object map[string]any
		decoder := json.NewDecoder(bytes.NewReader(result.Output))
		decoder.UseNumber()
		if err := decoder.Decode(&object); err != nil || object == nil {
			return TaskOperationResult{}, errors.New("operation output must be one JSON object")
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			return TaskOperationResult{}, errors.New("operation output must contain one JSON value")
		}
		canonical, err := json.Marshal(object)
		if err != nil {
			return TaskOperationResult{}, errors.New("operation output is not serializable")
		}
		result.Output = canonical
	}
	return result, nil
}

func validateSkillLearningState(state SkillLearningState) error {
	if state.Attempts == 0 || state.Attempts > 3 {
		return errors.New("skill learning attempts are invalid")
	}
	switch state.Status {
	case SkillLearningPending:
		if state.SkillID != "" || state.Digest != "" || state.Code != "" {
			return errors.New("pending skill learning contains a result")
		}
	case SkillLearningDrafted, SkillLearningEnabled:
		if err := validateProviderID("skill_learning.skill_id", state.SkillID); err != nil {
			return err
		}
		if !providerDigestPattern.MatchString(state.Digest) || state.Code != "" {
			return errors.New("completed skill learning result is invalid")
		}
	case SkillLearningSkipped, SkillLearningFailed:
		if err := validateProviderID("skill_learning.code", state.Code); err != nil {
			return err
		}
		if state.SkillID != "" || state.Digest != "" {
			return errors.New("unsuccessful skill learning contains a skill")
		}
	default:
		return errors.New("skill learning status is invalid")
	}
	return nil
}

func taskAllowsCapability(task TaskSession, capabilityID string) bool {
	return len(task.AllowedCapabilities) == 0 ||
		slices.Contains(task.AllowedCapabilities, capabilityID)
}

func cloneTaskActionRequest(request host.ActionRequest) host.ActionRequest {
	request.Arguments = append([]byte(nil), request.Arguments...)
	request.Targets = append([]host.HostRef(nil), request.Targets...)
	if request.PlanStep != nil {
		planStep := *request.PlanStep
		request.PlanStep = &planStep
	}
	return request
}

func appendTaskEvent(task *TaskSession, event TaskEvent) {
	task.EventSequence++
	event.Sequence = task.EventSequence
	if len(task.History) == 512 {
		copy(task.History, task.History[1:])
		task.History = task.History[:511]
	}
	task.History = append(task.History, cloneTaskEvent(event))
}

func sealTaskEvent(task TaskSession, event TaskEvent, index int) (TaskEvent, error) {
	field := fmt.Sprintf("history[%d]", index)
	if event.Sequence == 0 || event.Sequence > maxProviderWireInteger {
		return TaskEvent{}, fmt.Errorf("%s sequence is invalid", field)
	}
	if err := validateProviderID(field+".kind", event.Kind); err != nil {
		return TaskEvent{}, err
	}
	if event.Code != "" {
		if err := validateProviderID(field+".code", event.Code); err != nil {
			return TaskEvent{}, err
		}
	}
	if err := validateProviderText(field+".summary", event.Summary, 500, false); err != nil {
		return TaskEvent{}, err
	}
	if event.OperationID != "" {
		if err := validateProviderID(field+".operation_id", event.OperationID); err != nil {
			return TaskEvent{}, err
		}
	}
	if event.ObservationID != "" {
		if err := validateProviderID(field+".observation_id", event.ObservationID); err != nil {
			return TaskEvent{}, err
		}
	}
	if event.ObservationSequence > maxProviderWireInteger {
		return TaskEvent{}, fmt.Errorf("%s observation sequence is invalid", field)
	}
	if (event.ObservationSequence == 0) != (event.Epoch == nil) {
		return TaskEvent{}, fmt.Errorf("%s observation sequence and epoch must appear together", field)
	}
	if event.Epoch != nil {
		if err := event.Epoch.Validate(field + ".epoch"); err != nil {
			return TaskEvent{}, err
		}
	}
	if event.Capability != nil {
		if err := event.Capability.Validate(field + ".capability"); err != nil {
			return TaskEvent{}, fmt.Errorf("%s.capability: %w", field, err)
		}
	}
	if event.Signal != nil {
		if err := timeline.ValidateSignalContextRef(*event.Signal); err != nil {
			return TaskEvent{}, err
		}
	}
	if len(event.SkillRefs) > 64 || len(event.MemoryContextRefs) > 64 {
		return TaskEvent{}, fmt.Errorf("%s context references exceed the limit", field)
	}
	if event.Model != nil {
		if err := validateProviderText(field+".model", event.Model.Model, 200, false); err != nil {
			return TaskEvent{}, err
		}
	}
	if event.Step > task.Budget.MaxSteps || event.AtUnixMillis < 0 ||
		event.AtUnixMillis > maxProviderWireInteger {
		return TaskEvent{}, errors.New("task history event is out of bounds")
	}
	return cloneTaskEvent(event), nil
}

func cloneTaskEvent(event TaskEvent) TaskEvent {
	if event.Epoch != nil {
		epoch := *event.Epoch
		event.Epoch = &epoch
	}
	if event.Capability != nil {
		capability := *event.Capability
		event.Capability = &capability
	}
	if event.Signal != nil {
		signal := *event.Signal
		event.Signal = &signal
	}
	event.SkillRefs = append([]timeline.SkillContextRef(nil), event.SkillRefs...)
	event.MemoryContextRefs = append([]timeline.MemoryContextRef(nil), event.MemoryContextRefs...)
	if event.Model != nil {
		model := *event.Model
		model.LatencyMillis = cloneOptionalUint64(model.LatencyMillis)
		model.PromptTokens = cloneOptionalUint64(model.PromptTokens)
		model.CompletionTokens = cloneOptionalUint64(model.CompletionTokens)
		model.TotalTokens = cloneOptionalUint64(model.TotalTokens)
		model.CacheHitTokens = cloneOptionalUint64(model.CacheHitTokens)
		model.CacheMissTokens = cloneOptionalUint64(model.CacheMissTokens)
		event.Model = &model
	}
	if event.Policy != nil {
		policy := *event.Policy
		policy.MatchedRuleIDs = append([]string(nil), policy.MatchedRuleIDs...)
		event.Policy = &policy
	}
	if event.Operation != nil {
		operation := *event.Operation
		event.Operation = &operation
	}
	return event
}

func cloneOptionalUint64(value *uint64) *uint64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
