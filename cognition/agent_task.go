package cognition

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
)

const TaskSnapshotVersion = "rin.cognition.tasks/v1"

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

type TaskEvent struct {
	Kind         string `json:"kind"`
	Step         uint32 `json:"step"`
	Code         string `json:"code,omitempty"`
	Summary      string `json:"summary,omitempty"`
	OperationID  string `json:"operation_id,omitempty"`
	AtUnixMillis int64  `json:"at_unix_millis"`
}

// TaskSession contains every decision-side value needed to resume without
// regenerating or mutating an already selected action.
type TaskSession struct {
	TaskID       string   `json:"task_id"`
	SessionID    string   `json:"session_id"`
	HostID       string   `json:"host_id"`
	WorldID      string   `json:"world_id"`
	ActorID      string   `json:"actor_id"`
	ControllerID string   `json:"controller_id"`
	Goal         string   `json:"goal"`
	Tags         []string `json:"tags,omitempty"`

	Status    TaskStatus `json:"status"`
	PauseCode string     `json:"pause_code,omitempty"`
	Revision  uint64     `json:"revision"`
	Step      uint32     `json:"step"`
	Budget    TaskBudget `json:"budget"`

	ModelCalls  uint32 `json:"model_calls"`
	ModelTokens uint64 `json:"model_tokens"`
	ActionCount uint32 `json:"action_count"`

	ControllerLease    controlplane.ControllerLease `json:"controller_lease"`
	PendingAction      *host.ActionRequest          `json:"pending_action,omitempty"`
	PendingOperationID string                       `json:"pending_operation_id,omitempty"`
	PendingMemories    []MemoryRecord               `json:"pending_memories,omitempty"`

	LastObservationID   string      `json:"last_observation_id,omitempty"`
	LastObservationSeq  uint64      `json:"last_observation_sequence,omitempty"`
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
	var err error
	if task.Tags, err = normalizeProviderIDs("tags", task.Tags, 32); err != nil {
		return TaskSession{}, err
	}
	if !validTaskStatus(task.Status) || task.Revision == 0 {
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
		task.PendingAction = &request
	} else if task.PendingOperationID != "" || len(task.PendingMemories) != 0 {
		return TaskSession{}, errors.New("pending operation or memories require a pending action")
	}
	if task.PendingOperationID != "" {
		if err := validateProviderID("pending_operation_id", task.PendingOperationID); err != nil {
			return TaskSession{}, err
		}
	}
	if task.Status == TaskWaitingConfirmation && task.PendingOperationID == "" {
		return TaskSession{}, errors.New("waiting task has no pending operation")
	}
	if task.Status == TaskCancelling &&
		(task.PendingAction == nil || task.PendingOperationID == "") {
		return TaskSession{}, errors.New("cancelling task requires a pending operation")
	}
	if task.Status == TaskOutcomeUnknown &&
		(task.PendingAction == nil || task.PendingOperationID == "") {
		return TaskSession{}, errors.New("outcome-unknown task requires reconciliation state")
	}
	if (task.Status == TaskCompleted || task.Status == TaskCancelled) && task.PendingAction != nil {
		return TaskSession{}, errors.New("completed or cancelled task must not retain a pending action")
	}
	if len(task.PendingMemories) > 8 {
		return TaskSession{}, errors.New("task has too many pending memories")
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
		if task.LastObservationSeq == 0 {
			return TaskSession{}, errors.New("last observation sequence is missing")
		}
	} else if task.LastObservationSeq != 0 {
		return TaskSession{}, errors.New("last observation id is missing")
	}
	if len(task.History) > 512 {
		return TaskSession{}, errors.New("task history exceeds 512 events")
	}
	task.History = append([]TaskEvent(nil), task.History...)
	for index, event := range task.History {
		if err := validateProviderID(fmt.Sprintf("history[%d].kind", index), event.Kind); err != nil {
			return TaskSession{}, err
		}
		if event.Code != "" {
			if err := validateProviderID(fmt.Sprintf("history[%d].code", index), event.Code); err != nil {
				return TaskSession{}, err
			}
		}
		if err := validateProviderText(fmt.Sprintf("history[%d].summary", index), event.Summary, 500, false); err != nil {
			return TaskSession{}, err
		}
		if event.OperationID != "" {
			if err := validateProviderID(fmt.Sprintf("history[%d].operation_id", index), event.OperationID); err != nil {
				return TaskSession{}, err
			}
		}
		if event.Step > task.Budget.MaxSteps || event.AtUnixMillis < 0 {
			return TaskSession{}, errors.New("task history event is out of bounds")
		}
	}
	if task.CreatedAtUnixMillis < 0 || task.UpdatedAtUnixMillis < task.CreatedAtUnixMillis {
		return TaskSession{}, errors.New("task timestamps are invalid")
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
	if lease.AuthorityRevision == 0 || lease.AcquiredAtUnixMillis < 0 {
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
	if task.PendingAction != nil {
		request := cloneTaskActionRequest(*task.PendingAction)
		task.PendingAction = &request
	}
	pendingMemories := task.PendingMemories
	task.PendingMemories = make([]MemoryRecord, len(pendingMemories))
	for index, record := range pendingMemories {
		task.PendingMemories[index] = cloneMemoryRecord(record)
	}
	task.History = append([]TaskEvent(nil), task.History...)
	return task
}

func cloneTaskActionRequest(request host.ActionRequest) host.ActionRequest {
	request.Arguments = append([]byte(nil), request.Arguments...)
	request.Targets = append([]host.HostRef(nil), request.Targets...)
	return request
}

func appendTaskEvent(task *TaskSession, event TaskEvent) {
	if len(task.History) == 512 {
		copy(task.History, task.History[1:])
		task.History = task.History[:511]
	}
	task.History = append(task.History, event)
}
