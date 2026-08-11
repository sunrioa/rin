package controlplane

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	rinapi "github.com/sunrioa/rin/api"
	"github.com/sunrioa/rin/release"
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
	info, ok := document["info"].(map[string]any)
	if !ok || info["version"] != release.Version {
		t.Fatalf("Control OpenAPI release version = %#v, want %q", info["version"], release.Version)
	}
	assertControlReferencesResolve(t, document, document)
	routes, err := rinapi.ParseControlRoutes()
	if err != nil {
		t.Fatalf("ParseControlRoutes: %v", err)
	}
	if len(routes) != 28 {
		t.Fatalf("Control route count = %d, want 28", len(routes))
	}
	wantRoutes := map[string]struct{}{
		"GET /control/v2/health":               {},
		"POST /control/v2/host/register":       {},
		"POST /control/v2/host/renew":          {},
		"POST /control/v2/host/unregister":     {},
		"POST /control/v2/host/publish":        {},
		"POST /control/v2/host/poll":           {},
		"POST /control/v2/host/gateway-result": {},
		"POST /control/v2/host/ack":            {},
		"POST /control/v2/host/run":            {},
		"POST /control/v2/host/outcome":        {},
		"GET /control/v2/info":                 {},
		"POST /control/v2/worlds":              {},
		"POST /control/v2/actors":              {},
		"POST /control/v2/actor":               {},
		"POST /control/v2/wait-actor":          {},
		"POST /control/v2/observe":             {},
		"POST /control/v2/capabilities":        {},
		"POST /control/v2/capability":          {},
		"POST /control/v2/controllers/acquire": {},
		"POST /control/v2/controllers/renew":   {},
		"POST /control/v2/controllers/release": {},
		"POST /control/v2/controllers/get":     {},
		"POST /control/v2/actions/submit":      {},
		"POST /control/v2/actions/confirm":     {},
		"POST /control/v2/operations/get":      {},
		"POST /control/v2/operations/wait":     {},
		"POST /control/v2/operations/cancel":   {},
		"POST /control/v2/emergency-stop":      {},
	}
	operationIDs := make(map[string]struct{}, len(routes))
	for _, route := range routes {
		key := route.Method + " " + route.Path
		if _, exists := wantRoutes[key]; !exists {
			t.Fatalf("unexpected Control route %s", key)
		}
		delete(wantRoutes, key)
		if _, duplicate := operationIDs[route.OperationID]; duplicate {
			t.Fatalf("duplicate operationId %q", route.OperationID)
		}
		operationIDs[route.OperationID] = struct{}{}
	}
	if len(wantRoutes) != 0 {
		t.Fatalf("missing Control routes: %#v", wantRoutes)
	}

	service := New(Options{})
	principal := operationPrincipal(
		ScopeActorRead,
		ScopeActorControl,
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
		if route.Path != "/control/v2/health" {
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

func TestControlV2FixturesMatchRuntimeInputs(t *testing.T) {
	var document map[string]any
	if err := json.Unmarshal(rinapi.ControlDocument(), &document); err != nil {
		t.Fatal(err)
	}
	if document["x-rin-example-fixtures"] != "control-v2-fixtures.json" {
		t.Fatal("Control OpenAPI does not publish its V2 fixture location")
	}
	var fixtures struct {
		ContractVersion    string                  `json:"contract_version"`
		ActorTarget        ActorControlTarget      `json:"actor_target"`
		WorldTarget        worldTargetRequest      `json:"world_target"`
		WaitActor          WaitActorInput          `json:"wait_actor"`
		DescribeCapability DescribeCapabilityInput `json:"describe_capability"`
		AcquireController  AcquireControllerInput  `json:"acquire_controller"`
		RenewController    RenewControllerInput    `json:"renew_controller"`
		ReleaseController  ReleaseControllerInput  `json:"release_controller"`
		SubmitAction       SubmitActionInput       `json:"submit_action"`
		OperationTarget    operationTargetRequest  `json:"operation_target"`
		WaitOperation      WaitOperationInput      `json:"wait_operation"`
		EmergencyStop      SetEmergencyStopInput   `json:"emergency_stop"`
	}
	decoder := json.NewDecoder(bytes.NewReader(rinapi.ControlFixtures()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fixtures); err != nil {
		t.Fatalf("decode Control V2 fixtures: %v", err)
	}
	if fixtures.ContractVersion != ContractVersion {
		t.Fatalf("fixture contract = %q", fixtures.ContractVersion)
	}
	for name, target := range map[string]ActorControlTarget{
		"actor_target":        fixtures.ActorTarget,
		"describe_capability": fixtures.DescribeCapability.ActorControlTarget,
		"acquire_controller":  fixtures.AcquireController.ActorControlTarget,
		"renew_controller":    fixtures.RenewController.ActorControlTarget,
		"release_controller":  fixtures.ReleaseController.ActorControlTarget,
		"emergency_stop":      fixtures.EmergencyStop.ActorControlTarget,
	} {
		if err := validateActorControlTarget(target); err != nil {
			t.Fatalf("%s fixture: %v", name, err)
		}
	}
	if err := validateAcquireControllerInput(fixtures.AcquireController); err != nil {
		t.Fatalf("acquire_controller fixture: %v", err)
	}
	if err := validateSubmitActionInput(fixtures.SubmitAction); err != nil {
		t.Fatalf("submit_action fixture: %v", err)
	}
	if fixtures.WaitActor.WaitMillis > 25_000 ||
		fixtures.WaitOperation.WaitMillis > 25_000 ||
		fixtures.OperationTarget.OperationID == "" {
		t.Fatal("bounded wait or operation fixture is invalid")
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
