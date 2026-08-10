package mcpbridge

import "github.com/sunrioa/rin/controlplane"

// ControlClient is the same V2 application contract used by HTTP. MCP is a
// thin transport and owns no separate authorization or execution semantics.
type ControlClient = controlplane.ControlClient
