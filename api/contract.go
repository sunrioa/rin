// Package api exposes the embedded, language-neutral Rin HTTP contract.
package api

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

//go:embed openapi.json
var openAPIDocument []byte

// Document returns a defensive copy of the authoritative OpenAPI 3.1 document.
func Document() []byte {
	return append([]byte(nil), openAPIDocument...)
}

// Metadata is the release and wire-compatibility identity carried by OpenAPI.
type Metadata struct {
	ReleaseVersion  string
	ProtocolVersion string
	ReleaseStatus   string
	LuantiRelease   int
}

// Route is one operation projected from the authoritative OpenAPI paths map.
type Route struct {
	OperationID   string
	Method        string
	Path          string
	SuccessStatus int
}

// ParseMetadata reads the small set of contract fields used by generators and
// tests without exposing a partial OpenAPI object model.
func ParseMetadata() (Metadata, error) {
	var document struct {
		Info struct {
			Version string `json:"version"`
		} `json:"info"`
		ProtocolVersion string `json:"x-rin-protocol-version"`
		ReleaseStatus   string `json:"x-rin-release-status"`
		LuantiRelease   int    `json:"x-rin-luanti-release"`
	}
	if err := json.Unmarshal(openAPIDocument, &document); err != nil {
		return Metadata{}, fmt.Errorf("decode embedded OpenAPI metadata: %w", err)
	}
	return Metadata{
		ReleaseVersion:  document.Info.Version,
		ProtocolVersion: document.ProtocolVersion,
		ReleaseStatus:   document.ReleaseStatus,
		LuantiRelease:   document.LuantiRelease,
	}, nil
}

// ParseRoutes projects method, path, operationId, and the sole successful status
// from the authoritative OpenAPI document.
func ParseRoutes() ([]Route, error) {
	var document struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(openAPIDocument, &document); err != nil {
		return nil, fmt.Errorf("decode embedded OpenAPI paths: %w", err)
	}
	routes := make([]Route, 0, 20)
	for path, pathItem := range document.Paths {
		for method, rawOperation := range pathItem {
			switch method {
			case "delete", "get", "head", "options", "patch", "post", "put", "trace":
			default:
				continue
			}
			var operation struct {
				OperationID string                     `json:"operationId"`
				Responses   map[string]json.RawMessage `json:"responses"`
			}
			if err := json.Unmarshal(rawOperation, &operation); err != nil {
				return nil, fmt.Errorf("decode OpenAPI operation %s %s: %w", method, path, err)
			}
			if operation.OperationID == "" {
				continue
			}
			successStatus := 0
			for status := range operation.Responses {
				code, err := strconv.Atoi(status)
				if err != nil || code < 200 || code >= 300 {
					continue
				}
				if successStatus != 0 {
					return nil, fmt.Errorf("operation %s has more than one 2xx response", operation.OperationID)
				}
				successStatus = code
			}
			if successStatus == 0 {
				return nil, fmt.Errorf("operation %s has no 2xx response", operation.OperationID)
			}
			routes = append(routes, Route{
				OperationID:   operation.OperationID,
				Method:        strings.ToUpper(method),
				Path:          path,
				SuccessStatus: successStatus,
			})
		}
	}
	sort.Slice(routes, func(left, right int) bool {
		if routes[left].Path == routes[right].Path {
			return routes[left].Method < routes[right].Method
		}
		return routes[left].Path < routes[right].Path
	})
	return routes, nil
}
