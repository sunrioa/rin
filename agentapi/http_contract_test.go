package agentapi_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/sunrioa/rin/agentapi"
	rinapi "github.com/sunrioa/rin/api"
	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/timeline"
)

func TestAgentOpenAPIMatchesRegisteredRoutes(t *testing.T) {
	var document map[string]any
	if err := json.Unmarshal(rinapi.AgentDocument(), &document); err != nil {
		t.Fatalf("decode Agent OpenAPI: %v", err)
	}
	if document["openapi"] != "3.1.0" ||
		document["jsonSchemaDialect"] != "https://json-schema.org/draft/2020-12/schema" ||
		document["x-rin-contract-version"] != agentapi.ContractVersion ||
		document["x-rin-task-timeline-fixtures"] != "task-timeline-v1-fixtures.json" {
		t.Fatal("Agent OpenAPI identity does not match the runtime contract")
	}
	assertAgentReferencesResolve(t, document, document)

	openAPIRoutes, err := rinapi.ParseAgentRoutes()
	if err != nil {
		t.Fatal(err)
	}
	runtimeRoutes := agentapi.ContractRoutes()
	if len(openAPIRoutes) != 8 || len(runtimeRoutes) != len(openAPIRoutes) {
		t.Fatalf("route count: OpenAPI=%d runtime=%d, want 8", len(openAPIRoutes), len(runtimeRoutes))
	}
	runtimeByKey := make(map[string]agentapi.ContractRoute, len(runtimeRoutes))
	for _, route := range runtimeRoutes {
		key := route.Method + " " + route.Path
		if _, duplicate := runtimeByKey[key]; duplicate {
			t.Fatalf("duplicate runtime route %s", key)
		}
		runtimeByKey[key] = route
	}
	seenOperations := make(map[string]struct{}, len(openAPIRoutes))
	for _, route := range openAPIRoutes {
		key := route.Method + " " + route.Path
		runtimeRoute, exists := runtimeByKey[key]
		if !exists {
			t.Fatalf("OpenAPI route is not registered: %s", key)
		}
		if _, duplicate := seenOperations[route.OperationID]; duplicate {
			t.Fatalf("duplicate operationId %q", route.OperationID)
		}
		seenOperations[route.OperationID] = struct{}{}
		if runtimeRoute.OperationID != route.OperationID ||
			runtimeRoute.SuccessStatus != route.SuccessStatus {
			t.Fatalf("route mismatch: OpenAPI=%+v runtime=%+v", route, runtimeRoute)
		}
	}

	runtime := newFakeTaskRuntime()
	service := newTestAgentService(t, runtime, 1)
	defer service.Close()
	handler, err := agentapi.NewHTTPHandler(service, agentapi.HTTPOptions{
		Token: testAgentToken,
		ClientPrincipal: taskPrincipal(
			agentapi.ScopeTaskRead,
			agentapi.ScopeTaskExecute,
			agentapi.ScopeTaskCancel,
		),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range runtimeRoutes {
		body := bytes.NewReader(nil)
		if route.Method == http.MethodPost {
			body = bytes.NewReader([]byte(`{}`))
		}
		request := httptest.NewRequest(route.Method, route.Path, body)
		request.Header.Set("Authorization", "Bearer "+testAgentToken)
		if route.Method == http.MethodPost {
			request.Header.Set("Content-Type", "application/json")
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if !strings.HasPrefix(response.Header().Get("Content-Type"), "application/json") {
			t.Fatalf("%s is not registered by the JSON handler: status=%d", route.Path, response.Code)
		}
	}
}

func TestAgentOpenAPISchemaFieldsMatchGoDTOs(t *testing.T) {
	var document struct {
		Components struct {
			Schemas map[string]map[string]any `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(rinapi.AgentDocument(), &document); err != nil {
		t.Fatal(err)
	}
	types := map[string]any{
		"ClientInfo":            agentapi.ClientInfo{},
		"Principal":             host.Principal{},
		"StartTaskInput":        cognition.StartTaskInput{},
		"TaskTarget":            agentapi.TaskTarget{},
		"TaskBudget":            cognition.TaskBudget{},
		"TaskDispatch":          agentapi.TaskDispatch{},
		"TaskSession":           cognition.TaskSession{},
		"TaskEvent":             cognition.TaskEvent{},
		"ControllerLease":       controlplane.ControllerLease{},
		"Epoch":                 host.Epoch{},
		"CapabilityRef":         host.CapabilityRef{},
		"HostRef":               host.HostRef{},
		"ActionRequest":         host.ActionRequest{},
		"MemoryRecord":          cognition.MemoryRecord{},
		"MemoryNamespace":       cognition.MemoryNamespace{},
		"MemoryProvenance":      cognition.MemoryProvenance{},
		"Timepoint":             host.Timepoint{},
		"TaskTimelineQuery":     timeline.Query{},
		"WaitTaskTimelineInput": timeline.WaitInput{},
		"TaskTimelinePage":      timeline.Page{},
		"TaskTimelineUpdate":    timeline.Update{},
		"TaskTimelineEvent":     timeline.Event{},
		"MemoryContextRef":      timeline.MemoryContextRef{},
		"SkillContextRef":       timeline.SkillContextRef{},
		"ModelUsage":            timeline.ModelUsage{},
		"PolicySummary":         timeline.PolicySummary{},
		"OperationSummary":      timeline.OperationSummary{},
	}
	for name, value := range types {
		schema, exists := document.Components.Schemas[name]
		if !exists {
			t.Errorf("missing Agent schema %s", name)
			continue
		}
		properties, ok := schema["properties"].(map[string]any)
		if !ok {
			t.Errorf("schema %s has no properties", name)
			continue
		}
		want := jsonFieldNames(reflect.TypeOf(value))
		got := make([]string, 0, len(properties))
		for field := range properties {
			got = append(got, field)
		}
		sort.Strings(got)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("schema %s fields=%v, Go fields=%v", name, got, want)
		}
	}

	for _, name := range []string{
		"StartTaskInput", "TaskTarget", "TaskBudget",
		"TaskTimelineQuery", "WaitTaskTimelineInput",
	} {
		if document.Components.Schemas[name]["additionalProperties"] != false {
			t.Errorf("request schema %s must reject unknown fields", name)
		}
	}
	status := document.Components.Schemas["TaskSession"]["properties"].(map[string]any)["status"].(map[string]any)
	gotStatuses := stringValues(status["enum"].([]any))
	wantStatuses := []string{
		string(cognition.TaskActive), string(cognition.TaskWaitingConfirmation),
		string(cognition.TaskCancelling), string(cognition.TaskPaused),
		string(cognition.TaskCompleted), string(cognition.TaskFailed),
		string(cognition.TaskOutcomeUnknown), string(cognition.TaskCancelled),
	}
	sort.Strings(gotStatuses)
	sort.Strings(wantStatuses)
	if !reflect.DeepEqual(gotStatuses, wantStatuses) {
		t.Fatalf("TaskStatus enum=%v, want %v", gotStatuses, wantStatuses)
	}
}

func jsonFieldNames(value reflect.Type) []string {
	fields := make([]string, 0, value.NumField())
	for index := 0; index < value.NumField(); index++ {
		tag := strings.Split(value.Field(index).Tag.Get("json"), ",")[0]
		if tag != "" && tag != "-" {
			fields = append(fields, tag)
		}
	}
	sort.Strings(fields)
	return fields
}

func stringValues(values []any) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = value.(string)
	}
	return result
}

func assertAgentReferencesResolve(t *testing.T, root map[string]any, value any) {
	t.Helper()
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == "$ref" {
				reference, ok := child.(string)
				if !ok || !strings.HasPrefix(reference, "#/") {
					t.Fatalf("invalid Agent OpenAPI reference %#v", child)
				}
				if _, ok := resolveAgentPointer(root, reference); !ok {
					t.Fatalf("unresolved Agent OpenAPI reference %q", reference)
				}
				continue
			}
			assertAgentReferencesResolve(t, root, child)
		}
	case []any:
		for _, child := range typed {
			assertAgentReferencesResolve(t, root, child)
		}
	}
}

func resolveAgentPointer(root map[string]any, reference string) (any, bool) {
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
