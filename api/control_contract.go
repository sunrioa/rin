package api

import _ "embed"

//go:embed control-openapi.json
var controlOpenAPIDocument []byte

// ControlDocument returns a defensive copy of the authoritative Control API
// OpenAPI 3.1 document.
func ControlDocument() []byte {
	return append([]byte(nil), controlOpenAPIDocument...)
}

// ParseControlRoutes projects the Control Daemon's HTTP route inventory.
func ParseControlRoutes() ([]Route, error) {
	return parseRoutes(controlOpenAPIDocument)
}
