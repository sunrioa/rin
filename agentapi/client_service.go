package agentapi

import (
	"context"
	"fmt"

	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/timeline"
)

// TaskClient is the application contract shared by direct calls, HTTP, and
// MCP. It never accepts a caller-authored Principal in a request body.
type TaskClient interface {
	Info(context.Context) (ClientInfo, error)
	StartTask(context.Context, cognition.StartTaskInput) (TaskDispatch, error)
	GetTask(context.Context, string) (cognition.TaskSession, error)
	RunTask(context.Context, string) (TaskDispatch, error)
	ResumeTask(context.Context, string) (TaskDispatch, error)
	CancelTask(context.Context, string) (TaskDispatch, error)
	GetTaskTimeline(context.Context, timeline.Query) (timeline.Page, error)
	WaitTaskTimeline(context.Context, timeline.WaitInput) (timeline.Update, error)
}

// ClientService binds one trusted Principal to the task application service.
type ClientService struct {
	service   *Service
	principal host.Principal
}

func NewClientService(service *Service, principal host.Principal) (*ClientService, error) {
	if service == nil {
		return nil, fmt.Errorf("%w: service is required", ErrInvalid)
	}
	if err := host.ValidatePrincipal(principal); err != nil {
		return nil, fmt.Errorf("%w: principal: %v", ErrInvalid, err)
	}
	if !principalHasTaskScope(principal) {
		return nil, fmt.Errorf("%w: principal has no task scope", ErrInvalid)
	}
	return &ClientService{service: service, principal: cloneTaskPrincipal(principal)}, nil
}

func (client *ClientService) Info(context.Context) (ClientInfo, error) {
	return ClientInfo{
		ContractVersion: ContractVersion,
		Principal:       cloneTaskPrincipal(client.principal),
	}, nil
}

func (client *ClientService) StartTask(
	ctx context.Context,
	input cognition.StartTaskInput,
) (TaskDispatch, error) {
	return client.service.StartTask(ctx, client.principal, input)
}

func (client *ClientService) GetTask(
	ctx context.Context,
	taskID string,
) (cognition.TaskSession, error) {
	return client.service.GetTask(ctx, client.principal, taskID)
}

func (client *ClientService) RunTask(
	ctx context.Context,
	taskID string,
) (TaskDispatch, error) {
	return client.service.RunTask(ctx, client.principal, taskID)
}

func (client *ClientService) ResumeTask(
	ctx context.Context,
	taskID string,
) (TaskDispatch, error) {
	return client.service.ResumeTask(ctx, client.principal, taskID)
}

func (client *ClientService) CancelTask(
	ctx context.Context,
	taskID string,
) (TaskDispatch, error) {
	return client.service.CancelTask(ctx, client.principal, taskID)
}

func (client *ClientService) GetTaskTimeline(
	ctx context.Context,
	query timeline.Query,
) (timeline.Page, error) {
	return client.service.GetTaskTimeline(ctx, client.principal, query)
}

func (client *ClientService) WaitTaskTimeline(
	ctx context.Context,
	input timeline.WaitInput,
) (timeline.Update, error) {
	return client.service.WaitTaskTimeline(ctx, client.principal, input)
}

func principalHasTaskScope(principal host.Principal) bool {
	for _, scope := range []string{
		ScopeTaskRead, ScopeTaskExecute, ScopeTaskCancel, controlplane.ScopeHostAdmin,
	} {
		if hasScope(principal, scope) {
			return true
		}
	}
	return false
}

func cloneTaskPrincipal(principal host.Principal) host.Principal {
	principal.GrantedScopes = append([]string(nil), principal.GrantedScopes...)
	return principal
}

var _ TaskClient = (*ClientService)(nil)
