// Package agentapi exposes the engine-neutral application service for Rin's
// internal Agent Runtime. It contains no model credentials or game-specific
// behavior.
package agentapi

import (
	"context"
	"errors"
	"time"

	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/timeline"
)

const (
	ContractVersion = "rin.agent/v1"

	ScopeTaskRead    = "task.read"
	ScopeTaskExecute = "task.execute"
	ScopeTaskCancel  = "task.cancel"
)

var (
	ErrInvalid     = errors.New("agent API invalid value")
	ErrNotFound    = errors.New("agent API task not found")
	ErrForbidden   = errors.New("agent API forbidden")
	ErrConflict    = errors.New("agent API conflict")
	ErrCapacity    = errors.New("agent API capacity exceeded")
	ErrUnavailable = errors.New("agent API unavailable")
)

// TaskRuntime is the exact cognition surface used by the asynchronous service.
// Implementations retain all Controller, Policy, Operation, and budget rules.
type TaskRuntime interface {
	StartTask(context.Context, cognition.StartTaskInput) (cognition.TaskSession, error)
	GetTask(context.Context, string) (cognition.TaskSession, error)
	SnapshotTasks(context.Context) (cognition.TaskSnapshot, error)
	ResumeTask(context.Context, string) (cognition.TaskSession, error)
	CancelTask(context.Context, string) (cognition.TaskSession, error)
	RunTask(context.Context, string) (cognition.TaskSession, error)
	GetTaskTimeline(context.Context, timeline.Query) (timeline.Page, error)
	WaitTaskTimeline(context.Context, timeline.WaitInput) (timeline.Update, error)
}

// schedulingRuntime adds event-driven readiness without requiring custom task
// runtimes to expose their internal control ports.
type schedulingRuntime interface {
	TaskReady(context.Context, cognition.TaskSession) (bool, error)
	SchedulingEvents() (<-chan struct{}, <-chan struct{})
}

type signalTaskRuntime interface {
	StartSignalTask(
		context.Context,
		cognition.StartTaskInput,
		timeline.SignalContextRef,
	) (cognition.TaskSession, error)
}

type Options struct {
	Runtime           TaskRuntime
	WorkerCount       uint32
	QueueCapacity     uint32
	ReconcileInterval time.Duration
}

// TaskDispatch confirms durable task state and whether this call newly queued
// a worker. Scheduled is coordination metadata, never execution evidence.
type TaskDispatch struct {
	Task      cognition.TaskSession `json:"task"`
	Scheduled bool                  `json:"scheduled"`
}

type TaskTarget struct {
	TaskID string `json:"task_id"`
}

type TaskTimelineQuery = timeline.Query
type WaitTaskTimelineInput = timeline.WaitInput
type TaskTimelinePage = timeline.Page
type TaskTimelineUpdate = timeline.Update

type ClientInfo struct {
	ContractVersion string         `json:"contract_version"`
	Principal       host.Principal `json:"principal"`
}

// CompletionConfirmationInput binds caller acceptance to one review revision.
type CompletionConfirmationInput struct {
	TaskID           string `json:"task_id"`
	ExpectedRevision uint64 `json:"expected_revision"`
}
type completionRuntime interface {
	ConfirmTaskCompletion(context.Context, string, uint64) (cognition.TaskSession, error)
}
