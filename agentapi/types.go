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

type ClientInfo struct {
	ContractVersion string         `json:"contract_version"`
	Principal       host.Principal `json:"principal"`
}
