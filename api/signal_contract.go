package api

import _ "embed"

//go:embed signal-openapi.json
var signalOpenAPIDocument []byte

// SignalDocument returns a defensive copy of the Signal API contract.
func SignalDocument() []byte {
	return append([]byte(nil), signalOpenAPIDocument...)
}

func ParseSignalRoutes() ([]Route, error) {
	return parseRoutes(signalOpenAPIDocument)
}
