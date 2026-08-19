package api

import _ "embed"

//go:embed management-openapi.json
var managementOpenAPIDocument []byte

// ManagementDocument returns a defensive copy of the authoritative local
// Management API OpenAPI 3.1 document.
func ManagementDocument() []byte {
	return append([]byte(nil), managementOpenAPIDocument...)
}

// ParseManagementRoutes projects the Management API's HTTP route inventory.
func ParseManagementRoutes() ([]Route, error) {
	return parseRoutes(managementOpenAPIDocument)
}
