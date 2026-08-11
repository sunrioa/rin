package api

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Route is one operation projected from an authoritative OpenAPI paths map.
type Route struct {
	OperationID   string
	Method        string
	Path          string
	SuccessStatus int
}

func parseRoutes(documentBytes []byte) ([]Route, error) {
	var document struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(documentBytes, &document); err != nil {
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
				OperationID: operation.OperationID,
				Method:      strings.ToUpper(method), Path: path,
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
