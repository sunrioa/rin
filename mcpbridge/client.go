package mcpbridge

import (
	"context"

	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/skillapi"
	"github.com/sunrioa/rin/taskstate"
)

// ControlClient is the same V2 application contract used by HTTP. MCP is a
// thin transport and owns no separate authorization or execution semantics.
type ControlClient = controlplane.ControlClient

type SkillClient interface {
	List(context.Context, skillapi.ListInput) (skillapi.ListOutput, error)
	Get(context.Context, skillapi.GetInput) (skillapi.GetOutput, error)
	Save(context.Context, skillapi.SaveInput) (skillapi.GetOutput, error)
	Reload(context.Context) (skillapi.ReloadOutput, error)
}

type PlanClient = taskstate.PlanClient
