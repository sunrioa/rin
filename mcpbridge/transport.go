// Package mcpbridge exposes Rin Control Plane operations through the official
// MCP Go SDK.
package mcpbridge

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const ProtocolVersion = "2026-07-28"

// StrictTransport prevents the SDK from advertising or accepting an older MCP
// protocol revision.
type StrictTransport struct {
	Base mcp.Transport
}

func (transport StrictTransport) Connect(
	ctx context.Context,
) (mcp.Connection, error) {
	if transport.Base == nil {
		return nil, errors.New("missing MCP transport")
	}
	return transport.Base.Connect(ctx)
}

func (StrictTransport) SupportsProtocolVersion(version string) bool {
	return version == ProtocolVersion
}
