package controlplane

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	rinapi "github.com/sunrioa/rin/api"
)

func TestControlOpenAPIReferencesEveryDaemonRoute(t *testing.T) {
	var document map[string]any
	if err := json.Unmarshal(rinapi.ControlDocument(), &document); err != nil {
		t.Fatalf("decode Control OpenAPI: %v", err)
	}
	if document["openapi"] != "3.1.0" ||
		document["jsonSchemaDialect"] !=
			"https://json-schema.org/draft/2020-12/schema" {
		t.Fatal("Control OpenAPI uses the wrong dialect")
	}
	assertControlReferencesResolve(t, document, document)
	routes, err := rinapi.ParseControlRoutes()
	if err != nil {
		t.Fatalf("ParseControlRoutes: %v", err)
	}
	if len(routes) != 21 {
		t.Fatalf("Control route count = %d, want 21", len(routes))
	}

	service := New(Options{})
	principal := operationPrincipal(
		ScopeActorRead,
		ScopeActorConverse,
		ScopeActorDirect,
		ScopeActorSpeak,
		ScopeActorExecute,
		ScopeOperationCancel,
	)
	handler, err := NewHTTPHandler(service, HTTPOptions{
		Token:           testControlToken,
		ClientPrincipal: &principal,
	})
	if err != nil {
		t.Fatalf("NewHTTPHandler: %v", err)
	}
	for _, route := range routes {
		var body *bytes.Reader
		if route.Method == http.MethodPost {
			body = bytes.NewReader([]byte(`{}`))
		} else {
			body = bytes.NewReader(nil)
		}
		request := httptest.NewRequest(route.Method, route.Path, body)
		if route.Method == http.MethodPost {
			request.Header.Set("Content-Type", "application/json")
		}
		if route.Path != "/control/v1/health" {
			request.Header.Set("Authorization", "Bearer "+testControlToken)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if !strings.HasPrefix(
			response.Header().Get("Content-Type"),
			"application/json",
		) {
			t.Errorf(
				"%s %s is not registered by the JSON handler: status=%d body=%q",
				route.Method,
				route.Path,
				response.Code,
				response.Body.String(),
			)
		}
	}
}

func assertControlReferencesResolve(
	t *testing.T,
	root map[string]any,
	value any,
) {
	t.Helper()
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == "$ref" {
				reference, ok := child.(string)
				if !ok || !strings.HasPrefix(reference, "#/") {
					t.Fatalf("invalid Control OpenAPI reference %#v", child)
				}
				if _, ok := resolveControlPointer(root, reference); !ok {
					t.Fatalf("unresolved Control OpenAPI reference %q", reference)
				}
				continue
			}
			assertControlReferencesResolve(t, root, child)
		}
	case []any:
		for _, child := range typed {
			assertControlReferencesResolve(t, root, child)
		}
	}
}

func resolveControlPointer(root map[string]any, reference string) (any, bool) {
	var current any = root
	for _, token := range strings.Split(strings.TrimPrefix(reference, "#/"), "/") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		token = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
		current, ok = object[token]
		if !ok {
			return nil, false
		}
	}
	return current, true
}
