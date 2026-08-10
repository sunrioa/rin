package api

import _ "embed"

//go:embed control-openapi.json
var controlOpenAPIDocument []byte

//go:embed control-v2-fixtures.json
var controlV2Fixtures []byte

// ControlDocument returns a defensive copy of the authoritative Control API
// OpenAPI 3.1 document.
func ControlDocument() []byte {
	return append([]byte(nil), controlOpenAPIDocument...)
}

// ControlFixtures returns a defensive copy of the language-neutral V2 request
// corpus used by Control clients and contract tests.
func ControlFixtures() []byte {
	return append([]byte(nil), controlV2Fixtures...)
}

// ParseControlRoutes projects the Control Daemon's HTTP route inventory.
func ParseControlRoutes() ([]Route, error) {
	return parseRoutes(controlOpenAPIDocument)
}
