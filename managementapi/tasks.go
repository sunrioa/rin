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
	TaskID              string   `json:"task_id,omitempty"`
	HostID              string   `json:"host_id"`
	WorldID             string   `json:"world_id"`
	ActorID             string   `json:"actor_id"`
	Goal                string   `json:"goal"`
	PlanningMode        string   `json:"planning_mode,omitempty"`
	Tags                []string `json:"tags,omitempty"`
	AllowedCapabilities []string `json:"allowed_capabilities,omitempty"`
}

type TaskListInput struct {
	Status string `json:"status,omitempty"`
	Limit  uint32 `json:"limit,omitempty"`
}

type TaskSummary struct {
	TaskID              string   `json:"task_id"`
	HostID              string   `json:"host_id"`
	WorldID             string   `json:"world_id"`
	ActorID             string   `json:"actor_id"`
	Goal                string   `json:"goal"`
	Tags                []string `json:"tags,omitempty"`
	Status              string   `json:"status"`
	PauseCode           string   `json:"pause_code,omitempty"`
	PlanningMode        string   `json:"planning_mode"`
	PlanID              string   `json:"plan_id,omitempty"`
	PlanRevision        uint64   `json:"plan_revision,omitempty"`
	CurrentPlanStepID   string   `json:"current_plan_step_id,omitempty"`
	Step                uint32   `json:"step"`
	MaxSteps            uint32   `json:"max_steps"`
	ModelCalls          uint32   `json:"model_calls"`
	ModelTokens         uint64   `json:"model_tokens"`
	ActionCount         uint32   `json:"action_count"`
	PendingOperationID  string   `json:"pending_operation_id,omitempty"`
	CreatedAtUnixMillis int64    `json:"created_at_unix_millis"`
	UpdatedAtUnixMillis int64    `json:"updated_at_unix_millis"`
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
	Task     TaskSummary   `json:"task"`
	Timeline timeline.Page `json:"timeline"`
}

type TaskControlInput struct {
	TaskID string `json:"task_id"`
	Action string `json:"action"`
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
	if mode != taskstate.PlanningAuto && mode != taskstate.PlanningRequired {
		return TaskSummary{}, errors.New("Console task planning_mode must be auto or required")
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
		PlanningMode:        mode,
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
	if service.tasks == nil {
		return TaskListOutput{}, ErrTasksUnavailable
	}
	input.Status = strings.TrimSpace(input.Status)
	if input.Limit == 0 {
		input.Limit = 100
	}
	if input.Limit > 500 {
		return TaskListOutput{}, errors.New("task list limit exceeds 500")
	}
	snapshot, err := service.tasks.SnapshotTasks(ctx)
	if err != nil {
		return TaskListOutput{}, err
	}
	tasks := make([]TaskSummary, 0, min(len(snapshot.Tasks), int(input.Limit)))
	for _, task := range snapshot.Tasks {
		if input.Status != "" && string(task.Status) != input.Status {
			continue
		}
		tasks = append(tasks, taskSummary(task))
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
	if service.tasks == nil {
		return TaskDetail{}, ErrTasksUnavailable
	}
	task, err := service.tasks.GetTask(ctx, strings.TrimSpace(input.TaskID))
	if err != nil {
		return TaskDetail{}, err
	}
	page, err := service.tasks.GetTaskTimeline(ctx, timeline.Query{
		TaskID: task.TaskID, AfterCursor: strings.TrimSpace(input.AfterCursor), Limit: input.Limit,
	})
	if err != nil {
		return TaskDetail{}, err
	}
	return TaskDetail{Task: taskSummary(task), Timeline: page}, nil
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
	case "run":
		task, err = service.tasks.RunTask(ctx, taskID)
	case "resume":
		task, err = service.tasks.ResumeTask(ctx, taskID)
	case "cancel":
		task, err = service.tasks.CancelTask(ctx, taskID)
	default:
		return TaskSummary{}, errors.New("task action must be run, resume, or cancel")
	}
	if err != nil {
		return TaskSummary{}, err
	}
	return taskSummary(task), nil
}

func taskSummary(task cognition.TaskSession) TaskSummary {
	return TaskSummary{
		TaskID: task.TaskID, HostID: task.HostID, WorldID: task.WorldID, ActorID: task.ActorID,
		Goal: task.Goal, Tags: append([]string(nil), task.Tags...),
		Status: string(task.Status), PauseCode: task.PauseCode,
		PlanningMode: string(task.PlanningMode), PlanID: task.PlanID,
		PlanRevision: task.PlanRevision, CurrentPlanStepID: task.CurrentPlanStepID,
		Step: task.Step, MaxSteps: task.Budget.MaxSteps, ModelCalls: task.ModelCalls,
		ModelTokens: task.ModelTokens, ActionCount: task.ActionCount,
		PendingOperationID:  task.PendingOperationID,
		CreatedAtUnixMillis: task.CreatedAtUnixMillis, UpdatedAtUnixMillis: task.UpdatedAtUnixMillis,
	}
}

func newTaskID() (string, error) {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", err
	}
	return "task.console." + hex.EncodeToString(suffix[:]), nil
}
