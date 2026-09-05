package managementapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"slices"
	"strings"

	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/taskstate"
	"github.com/sunrioa/rin/timeline"
)

var ErrTasksUnavailable = errors.New("internal Agent Runtime is not enabled")

type TaskManager interface {
	StartTask(context.Context, cognition.StartTaskInput) (cognition.TaskSession, error)
	SnapshotTasks(context.Context) (cognition.TaskSnapshot, error)
	GetTask(context.Context, string) (cognition.TaskSession, error)
	GetTaskTimeline(context.Context, timeline.Query) (timeline.Page, error)
	RunTask(context.Context, string) (cognition.TaskSession, error)
	ResumeTask(context.Context, string) (cognition.TaskSession, error)
	CancelTask(context.Context, string) (cognition.TaskSession, error)
}

type TaskStartInput struct {
	Completion          cognition.TaskCompletionPolicy `json:"completion,omitempty"`
	TaskID              string                         `json:"task_id,omitempty"`
	HostID              string                         `json:"host_id"`
	WorldID             string                         `json:"world_id"`
	ActorID             string                         `json:"actor_id"`
	Goal                string                         `json:"goal"`
	PlanningMode        string                         `json:"planning_mode,omitempty"`
	Tags                []string                       `json:"tags,omitempty"`
	AllowedCapabilities []string                       `json:"allowed_capabilities,omitempty"`
}

type TaskListInput struct {
	Status string `json:"status,omitempty"`
	Limit  uint32 `json:"limit,omitempty"`
}

type TaskSummary struct {
	Revision             uint64                          `json:"revision,omitempty"`
	Completion           *cognition.TaskCompletionPolicy `json:"completion,omitempty"`
	CompletionRequested  bool                            `json:"completion_requested,omitempty"`
	TaskID               string                          `json:"task_id"`
	HostID               string                          `json:"host_id"`
	AdapterID            string                          `json:"adapter_id,omitempty"`
	WorldID              string                          `json:"world_id"`
	ActorID              string                          `json:"actor_id"`
	Goal                 string                          `json:"goal"`
	Tags                 []string                        `json:"tags,omitempty"`
	Status               string                          `json:"status"`
	PauseCode            string                          `json:"pause_code,omitempty"`
	PlanningMode         string                          `json:"planning_mode"`
	ControllerSource     string                          `json:"controller_source,omitempty"`
	TaskControlAvailable bool                            `json:"task_control_available"`
	PlanID               string                          `json:"plan_id,omitempty"`
	PlanRevision         uint64                          `json:"plan_revision,omitempty"`
	CurrentPlanStepID    string                          `json:"current_plan_step_id,omitempty"`
	Step                 uint32                          `json:"step"`
	MaxSteps             uint32                          `json:"max_steps"`
	ModelCalls           uint32                          `json:"model_calls"`
	ModelTokens          uint64                          `json:"model_tokens"`
	ActionCount          uint32                          `json:"action_count"`
	PendingOperationID   string                          `json:"pending_operation_id,omitempty"`
	CreatedAtUnixMillis  int64                           `json:"created_at_unix_millis"`
	UpdatedAtUnixMillis  int64                           `json:"updated_at_unix_millis"`
}

type TaskListOutput struct {
	Revision uint64        `json:"revision"`
	Tasks    []TaskSummary `json:"tasks"`
}

type TaskGetInput struct {
	TaskID      string `json:"task_id"`
	AfterCursor string `json:"after_cursor,omitempty"`
	Limit       uint32 `json:"limit,omitempty"`
}

type TaskDetail struct {
	Task     TaskSummary          `json:"task"`
	Plan     *taskstate.PlanState `json:"plan,omitempty"`
	Timeline timeline.Page        `json:"timeline"`
}

type TaskControlInput struct {
	ExpectedRevision uint64 `json:"expected_revision,omitempty"`
	TaskID           string `json:"task_id"`
	Action           string `json:"action"`
}

func (service *Service) StartTask(
	ctx context.Context,
	input TaskStartInput,
) (TaskSummary, error) {
	if service.tasks == nil {
		return TaskSummary{}, ErrTasksUnavailable
	}
	input.TaskID = strings.TrimSpace(input.TaskID)
	if input.TaskID == "" {
		var err error
		input.TaskID, err = newTaskID()
		if err != nil {
			return TaskSummary{}, err
		}
	}
	mode := taskstate.PlanningMode(strings.TrimSpace(input.PlanningMode))
	if mode == "" {
		mode = taskstate.PlanningRequired
	}
	if mode != taskstate.PlanningAuto && mode != taskstate.PlanningRequired && mode != taskstate.PlanningDisabled {
		return TaskSummary{}, errors.New("Console task planning_mode must be disabled, auto, or required")
	}
	tags := []string{"console", "long-goal"}
	for _, tag := range input.Tags {
		tag = strings.TrimSpace(tag)
		if tag != "" && !slices.Contains(tags, tag) {
			tags = append(tags, tag)
		}
	}
	task, err := service.tasks.StartTask(ctx, cognition.StartTaskInput{
		TaskID: input.TaskID, HostID: strings.TrimSpace(input.HostID),
		WorldID: strings.TrimSpace(input.WorldID), ActorID: strings.TrimSpace(input.ActorID),
		ControllerID: "controller.rin-console", Goal: strings.TrimSpace(input.Goal),
		Tags:                tags,
		AllowedCapabilities: append([]string(nil), input.AllowedCapabilities...),
		PlanningMode:        mode, Completion: input.Completion,
		Budget: cognition.TaskBudget{
			MaxSteps: 512, MaxModelCalls: 1_024,
			MaxModelTokens: 2_000_000, MaxActions: 512,
		},
	})
	if err != nil {
		return TaskSummary{}, err
	}
	return taskSummary(task), nil
}

func (service *Service) ListTasks(
	ctx context.Context,
	input TaskListInput,
) (TaskListOutput, error) {
	if service.tasks == nil && service.plans == nil {
		return TaskListOutput{}, ErrTasksUnavailable
	}
	input.Status = strings.TrimSpace(input.Status)
	if input.Limit == 0 {
		input.Limit = 100
	}
	if input.Limit > 500 {
		return TaskListOutput{}, errors.New("task list limit exceeds 500")
	}
	snapshot := cognition.TaskSnapshot{}
	if service.tasks != nil {
		var err error
		snapshot, err = service.tasks.SnapshotTasks(ctx)
		if err != nil {
			return TaskListOutput{}, err
		}
	}
	tasks := make([]TaskSummary, 0, min(len(snapshot.Tasks), int(input.Limit)))
	knownTaskIDs := make(map[string]struct{}, len(snapshot.Tasks))
	for _, task := range snapshot.Tasks {
		knownTaskIDs[task.TaskID] = struct{}{}
		if input.Status != "" && string(task.Status) != input.Status {
			continue
		}
		tasks = append(tasks, taskSummary(task))
	}
	if service.plans != nil {
		plans, err := service.plans.List(ctx)
		if err != nil {
			return TaskListOutput{}, err
		}
		for _, plan := range plans {
			if _, exists := knownTaskIDs[plan.TaskID]; exists {
				continue
			}
			summary := planTaskSummary(plan)
			if input.Status != "" && summary.Status != input.Status {
				continue
			}
			tasks = append(tasks, summary)
			if plan.Revision > snapshot.Revision {
				snapshot.Revision = plan.Revision
			}
		}
	}
	slices.SortFunc(tasks, func(left, right TaskSummary) int {
		if left.UpdatedAtUnixMillis > right.UpdatedAtUnixMillis {
			return -1
		}
		if left.UpdatedAtUnixMillis < right.UpdatedAtUnixMillis {
			return 1
		}
		return strings.Compare(left.TaskID, right.TaskID)
	})
	if len(tasks) > int(input.Limit) {
		tasks = tasks[:input.Limit]
	}
	return TaskListOutput{Revision: snapshot.Revision, Tasks: tasks}, nil
}

func (service *Service) GetTask(
	ctx context.Context,
	input TaskGetInput,
) (TaskDetail, error) {
	if service.tasks == nil && service.plans == nil {
		return TaskDetail{}, ErrTasksUnavailable
	}
	taskID := strings.TrimSpace(input.TaskID)
	if service.tasks != nil {
		task, err := service.tasks.GetTask(ctx, taskID)
		if err == nil {
			return service.internalTaskDetail(ctx, task, input)
		}
		if !errors.Is(err, cognition.ErrProviderNotFound) {
			return TaskDetail{}, err
		}
	}
	if service.plans == nil {
		return TaskDetail{}, cognition.ErrProviderNotFound
	}
	plans, err := service.plans.List(ctx)
	if err != nil {
		return TaskDetail{}, err
	}
	for _, plan := range plans {
		if plan.TaskID != taskID && plan.PlanID != taskID {
			continue
		}
		return TaskDetail{
			Task: planTaskSummary(plan), Plan: &plan,
			Timeline: timeline.Page{
				ContractVersion: timeline.ContractVersion, TaskID: plan.TaskID,
				Goal: plan.Goal, Status: string(plan.Status), Events: []timeline.Event{},
			},
		}, nil
	}
	return TaskDetail{}, cognition.ErrProviderNotFound
}

func (service *Service) internalTaskDetail(
	ctx context.Context,
	task cognition.TaskSession,
	input TaskGetInput,
) (TaskDetail, error) {
	page, err := service.tasks.GetTaskTimeline(ctx, timeline.Query{
		TaskID: task.TaskID, AfterCursor: strings.TrimSpace(input.AfterCursor), Limit: input.Limit,
	})
	if err != nil {
		return TaskDetail{}, err
	}
	var plan *taskstate.PlanState
	if task.PlanID != "" {
		if service.plans == nil {
			return TaskDetail{}, ErrPlansUnavailable
		}
		stored, err := service.plans.Get(ctx, task.PlanID)
		if err != nil {
			return TaskDetail{}, err
		}
		plan = &stored
	}
	return TaskDetail{Task: taskSummary(task), Plan: plan, Timeline: page}, nil
}

func (service *Service) ControlTask(
	ctx context.Context,
	input TaskControlInput,
) (TaskSummary, error) {
	if service.tasks == nil {
		return TaskSummary{}, ErrTasksUnavailable
	}
	taskID := strings.TrimSpace(input.TaskID)
	var task cognition.TaskSession
	var err error
	switch strings.TrimSpace(input.Action) {
	case "confirm-completion":
		confirmer, ok := service.tasks.(interface {
			ConfirmTaskCompletion(context.Context, string, uint64) (cognition.TaskSession, error)
		})
		if !ok {
			return TaskSummary{}, ErrTasksUnavailable
		}
		if input.ExpectedRevision == 0 {
			return TaskSummary{}, errors.New("completion confirmation requires expected_revision")
		}
		task, err = confirmer.ConfirmTaskCompletion(ctx, taskID, input.ExpectedRevision)
	case "run":
		task, err = service.tasks.RunTask(ctx, taskID)
	case "resume":
		task, err = service.tasks.ResumeTask(ctx, taskID)
	case "cancel":
		task, err = service.tasks.CancelTask(ctx, taskID)
	default:
		return TaskSummary{}, errors.New("task action must be run, resume, cancel, or confirm-completion")
	}
	if err != nil {
		return TaskSummary{}, err
	}
	return taskSummary(task), nil
}

func taskSummary(task cognition.TaskSession) TaskSummary {
	completion := task.Completion
	return TaskSummary{
		Revision: task.Revision, Completion: &completion, CompletionRequested: task.CompletionRequested,
		TaskID: task.TaskID, HostID: task.HostID, AdapterID: task.AdapterID,
		WorldID: task.WorldID, ActorID: task.ActorID,
		Goal: task.Goal, Tags: append([]string(nil), task.Tags...),
		Status: string(task.Status), PauseCode: task.PauseCode,
		PlanningMode: string(task.PlanningMode), ControllerSource: "internal",
		TaskControlAvailable: true, PlanID: task.PlanID,
		PlanRevision: task.PlanRevision, CurrentPlanStepID: task.CurrentPlanStepID,
		Step: task.Step, MaxSteps: task.Budget.MaxSteps, ModelCalls: task.ModelCalls,
		ModelTokens: task.ModelTokens, ActionCount: task.ActionCount,
		PendingOperationID:  task.PendingOperationID,
		CreatedAtUnixMillis: task.CreatedAtUnixMillis, UpdatedAtUnixMillis: task.UpdatedAtUnixMillis,
	}
}

func planTaskSummary(plan taskstate.PlanState) TaskSummary {
	completed := uint32(0)
	for _, step := range plan.Steps {
		if step.Status == taskstate.StepCompleted || step.Status == taskstate.StepSkipped {
			completed++
		}
	}
	return TaskSummary{
		TaskID: plan.TaskID, HostID: plan.HostID, WorldID: plan.WorldID,
		ActorID: plan.ActorID, Goal: plan.Goal, Status: string(plan.Status),
		PlanningMode: string(plan.PlanningMode), ControllerSource: string(plan.ControllerSource),
		PlanID: plan.PlanID, PlanRevision: plan.Revision,
		CurrentPlanStepID: plan.CurrentStepID, Step: completed, MaxSteps: uint32(len(plan.Steps)),
		CreatedAtUnixMillis: plan.CreatedAtUnixMillis, UpdatedAtUnixMillis: plan.UpdatedAtUnixMillis,
	}
}

func newTaskID() (string, error) {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", err
	}
	return "task.console." + hex.EncodeToString(suffix[:]), nil
}
