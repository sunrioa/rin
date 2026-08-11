package api

import _ "embed"

//go:embed agent-openapi.json
var agentOpenAPIDocument []byte

// AgentDocument returns a defensive copy of the authoritative Agent API
// OpenAPI 3.1 document.
func AgentDocument() []byte {
	return append([]byte(nil), agentOpenAPIDocument...)
}

// ParseAgentRoutes projects the Agent Daemon's HTTP route inventory.
func ParseAgentRoutes() ([]Route, error) {
	return parseRoutes(agentOpenAPIDocument)
}
