package api

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"
)

func TestManagementContractRoutes(t *testing.T) {
	routes, err := ParseManagementRoutes()
	if err != nil {
		t.Fatal(err)
	}
	want := []Route{
		{OperationID: "management_v1_outcome_backlog", Method: "GET", Path: "/management/v1/outcomes/backlog", SuccessStatus: 200},
		{OperationID: "management_v1_retry_outcome", Method: "POST", Path: "/management/v1/outcomes/retry", SuccessStatus: 200},
		{OperationID: "management_v1_get_agent_config", Method: "GET", Path: "/management/v1/agent/config", SuccessStatus: 200},
		{OperationID: "management_v1_save_agent_config", Method: "PUT", Path: "/management/v1/agent/config", SuccessStatus: 200},
		{OperationID: "management_v1_control_actor", Method: "POST", Path: "/management/v1/actors/control", SuccessStatus: 200},
		{OperationID: "management_v1_diagnostics", Method: "GET", Path: "/management/v1/diagnostics", SuccessStatus: 200},
		{OperationID: "management_v1_info", Method: "GET", Path: "/management/v1/info", SuccessStatus: 200},
		{OperationID: "management_v1_forget_memory", Method: "POST", Path: "/management/v1/memories/forget", SuccessStatus: 200},
		{OperationID: "management_v1_list_memories", Method: "POST", Path: "/management/v1/memories/list", SuccessStatus: 200},
		{OperationID: "management_v1_save_memory", Method: "POST", Path: "/management/v1/memories/save", SuccessStatus: 200},
		{OperationID: "management_v1_control_operation", Method: "POST", Path: "/management/v1/operations/control", SuccessStatus: 200},
		{OperationID: "management_v1_list_operations", Method: "POST", Path: "/management/v1/operations/list", SuccessStatus: 200},
		{OperationID: "management_v1_get_personas", Method: "GET", Path: "/management/v1/personas", SuccessStatus: 200},
		{OperationID: "management_v1_replace_personas", Method: "PUT", Path: "/management/v1/personas", SuccessStatus: 200},
		{OperationID: "management_v1_get_policy_config", Method: "GET", Path: "/management/v1/policy/config", SuccessStatus: 200},
		{OperationID: "management_v1_save_policy_config", Method: "PUT", Path: "/management/v1/policy/config", SuccessStatus: 200},
		{OperationID: "management_v1_runtime", Method: "GET", Path: "/management/v1/runtime", SuccessStatus: 200},
		{OperationID: "management_v1_get_skill", Method: "POST", Path: "/management/v1/skills/get", SuccessStatus: 200},
		{OperationID: "management_v1_import_skill", Method: "POST", Path: "/management/v1/skills/import", SuccessStatus: 200},
		{OperationID: "management_v1_list_skills", Method: "POST", Path: "/management/v1/skills/list", SuccessStatus: 200},
		{OperationID: "management_v1_reload_skills", Method: "POST", Path: "/management/v1/skills/reload", SuccessStatus: 200},
		{OperationID: "management_v1_remove_skill", Method: "POST", Path: "/management/v1/skills/remove", SuccessStatus: 200},
		{OperationID: "management_v1_save_skill", Method: "POST", Path: "/management/v1/skills/save", SuccessStatus: 200},
		{OperationID: "management_v1_control_task", Method: "POST", Path: "/management/v1/tasks/control", SuccessStatus: 200},
		{OperationID: "management_v1_get_task", Method: "POST", Path: "/management/v1/tasks/get", SuccessStatus: 200},
		{OperationID: "management_v1_list_tasks", Method: "POST", Path: "/management/v1/tasks/list", SuccessStatus: 200},
		{OperationID: "management_v1_start_task", Method: "POST", Path: "/management/v1/tasks/start", SuccessStatus: 200},
	}
	slices.SortFunc(want, func(left, right Route) int {
		if left.Path < right.Path {
			return -1
		}
		if left.Path > right.Path {
			return 1
		}
		if left.Method < right.Method {
			return -1
		}
		if left.Method > right.Method {
			return 1
		}
		return 0
	})
	if !slices.Equal(routes, want) {
		t.Fatalf("routes = %#v, want %#v", routes, want)
	}
}

func TestManagementContractDocumentIsStrictAndDefensive(t *testing.T) {
	var document struct {
		OpenAPI           string                                `json:"openapi"`
		JSONSchemaDialect string                                `json:"jsonSchemaDialect"`
		Security          []map[string][]string                 `json:"security"`
		Paths             map[string]map[string]json.RawMessage `json:"paths"`
		Components        struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(ManagementDocument(), &document); err != nil {
		t.Fatal(err)
	}
	if document.OpenAPI != "3.1.0" || document.JSONSchemaDialect == "" {
		t.Fatalf("unexpected OpenAPI dialect: %#v", document)
	}
	if len(document.Security) != 1 || document.Security[0]["bearerAuth"] == nil {
		t.Fatalf("management contract must require bearerAuth: %#v", document.Security)
	}
	if len(document.Paths) != 24 || len(document.Components.Schemas) == 0 {
		t.Fatalf("contract inventory is incomplete: paths=%d schemas=%d", len(document.Paths), len(document.Components.Schemas))
	}
	for path, methods := range document.Paths {
		for method, raw := range methods {
			var operation struct {
				OperationID string                     `json:"operationId"`
				Responses   map[string]json.RawMessage `json:"responses"`
			}
			if err := json.Unmarshal(raw, &operation); err != nil {
				t.Fatalf("decode %s %s: %v", method, path, err)
			}
			if operation.OperationID == "" || operation.Responses["200"] == nil || operation.Responses["default"] == nil {
				t.Fatalf("incomplete operation %s %s: %#v", method, path, operation)
			}
		}
	}
	first := ManagementDocument()
	first[0] = 'x'
	if bytes.Equal(first, ManagementDocument()) {
		t.Fatal("ManagementDocument returned shared storage")
	}
}

func TestManagementContractKeepsAgentSecretsWriteOnly(t *testing.T) {
	var document map[string]any
	if err := json.Unmarshal(ManagementDocument(), &document); err != nil {
		t.Fatal(err)
	}
	components := document["components"].(map[string]any)
	schemas := components["schemas"].(map[string]any)
	request := schemas["AgentConfigSaveRequest"].(map[string]any)
	properties := request["properties"].(map[string]any)
	required := request["required"].([]any)
	if !slices.Contains(required, any("model")) || !slices.Contains(required, any("memory")) {
		t.Fatalf("Agent config request required fields = %#v", required)
	}
	for _, name := range []string{"api_key", "embedding_api_key"} {
		property := properties[name].(map[string]any)
		if property["writeOnly"] != true {
			t.Fatalf("%s is not writeOnly", name)
		}
	}
}
