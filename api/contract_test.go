package api_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	rinapi "github.com/sunrioa/rin/api"
	"github.com/sunrioa/rin/httpapi"
	"github.com/sunrioa/rin/protocol"
)

func TestContractMetadataAndRoutesMatchGeneratedRuntime(t *testing.T) {
	metadata, err := rinapi.ParseMetadata()
	if err != nil {
		t.Fatal(err)
	}
	if metadata.ReleaseVersion != protocol.ContractReleaseVersion ||
		metadata.ProtocolVersion != protocol.Version ||
		metadata.ReleaseStatus != protocol.ContractReleaseStatus ||
		metadata.LuantiRelease != protocol.ContractLuantiRelease {
		t.Fatalf("contract metadata does not match generated constants: %+v", metadata)
	}
	openAPIRoutes, err := rinapi.ParseRoutes()
	if err != nil {
		t.Fatal(err)
	}
	runtimeRoutes := httpapi.ContractRoutes()
	if len(openAPIRoutes) != 28 || len(runtimeRoutes) != len(openAPIRoutes) {
		t.Fatalf("route count: OpenAPI=%d runtime=%d, want 28", len(openAPIRoutes), len(runtimeRoutes))
	}
	runtimeByKey := make(map[string]httpapi.ContractRoute, len(runtimeRoutes))
	for _, route := range runtimeRoutes {
		key := route.Method + " " + route.Path
		if _, duplicate := runtimeByKey[key]; duplicate {
			t.Fatalf("duplicate runtime route %s", key)
		}
		runtimeByKey[key] = route
	}
	seenOperations := make(map[string]bool, len(openAPIRoutes))
	for _, route := range openAPIRoutes {
		key := route.Method + " " + route.Path
		runtimeRoute, exists := runtimeByKey[key]
		if !exists {
			t.Errorf("OpenAPI route is not registered: %s", key)
			continue
		}
		if seenOperations[route.OperationID] {
			t.Errorf("duplicate OpenAPI operationId %q", route.OperationID)
		}
		seenOperations[route.OperationID] = true
		if runtimeRoute.OperationID != route.OperationID ||
			runtimeRoute.SuccessStatus != route.SuccessStatus {
			t.Errorf("route projection mismatch: OpenAPI=%+v runtime=%+v", route, runtimeRoute)
		}
	}
}

func TestOpenAPIReferencesInputsAndResponseEvolutionRules(t *testing.T) {
	var document map[string]any
	if err := json.Unmarshal(rinapi.Document(), &document); err != nil {
		t.Fatal(err)
	}
	if document["openapi"] != "3.1.0" ||
		document["jsonSchemaDialect"] != "https://json-schema.org/draft/2020-12/schema" {
		t.Fatalf("unexpected OpenAPI dialect identity")
	}
	health := document["paths"].(map[string]any)["/health"].(map[string]any)["get"].(map[string]any)
	healthSecurity, ok := health["security"].([]any)
	if !ok || len(healthSecurity) != 0 {
		t.Fatalf("health must explicitly disable authentication in OpenAPI")
	}
	rootSecurity, ok := document["security"].([]any)
	if !ok || len(rootSecurity) != 2 ||
		len(rootSecurity[0].(map[string]any)) != 0 ||
		rootSecurity[1].(map[string]any)["bearerAuth"] == nil {
		t.Fatalf("OpenAPI must describe bearer authentication as optional server configuration")
	}
	var references []string
	collectReferences(document, &references)
	for _, reference := range references {
		if _, ok := resolveJSONPointer(document, reference); !ok {
			t.Errorf("unresolved local reference %q", reference)
		}
	}

	schemas := document["components"].(map[string]any)["schemas"].(map[string]any)
	closedInputs := []string{
		"Binding", "BoundaryInput", "GoalSeedInput", "ActorSeedInput", "FactInput",
		"ActionOfferInput", "CreateSessionRequest", "ObserveRequest", "ProposeRequest",
		"GoalUpdateInput", "ReportActionRequest", "ActionReportInput", "BatchActionReportRequest",
		"ActorActivityUpdateInput", "SetActorActivityRequest", "ArbitrateRequest",
		"SessionRequest", "ArchiveSessionRequest", "DeleteSessionRequest",
		"RestoreRequest", "TimelineRequest", "ReplayRequest",
		"DueAgentsRequest", "GenerationMessageInput", "GenerationRequest",
	}
	for _, name := range closedInputs {
		schema := schemas[name].(map[string]any)
		if schema["additionalProperties"] != false {
			t.Errorf("request/input schema %s must be closed", name)
		}
	}
	zeroDefaultOptional := map[string][]string{
		"BoundaryInput":            {"trigger_tags"},
		"GoalSeedInput":            {"progress"},
		"ActorSeedInput":           {"enabled"},
		"FactInput":                {"confidence"},
		"CreateSessionRequest":     {"seed"},
		"ObserveRequest":           {"tick"},
		"ProposeRequest":           {"tick"},
		"GoalUpdateInput":          {"progress_delta"},
		"ReportActionRequest":      {"tick"},
		"BatchActionReportRequest": {"tick"},
		"SetActorActivityRequest":  {"tick"},
		"ArbitrateRequest":         {"tick"},
		"DueAgentsRequest":         {"tick"},
		"GenerationRequest":        {"temperature"},
	}
	for schemaName, fields := range zeroDefaultOptional {
		required := requiredSet(schemas[schemaName].(map[string]any))
		for _, field := range fields {
			if required[field] {
				t.Errorf("%s.%s must retain typed zero-value omission compatibility", schemaName, field)
			}
		}
	}
	for schemaName, field := range map[string]string{
		"ReportActionRequest": "report",
		"ActionReportInput":   "decision",
	} {
		if !requiredSet(schemas[schemaName].(map[string]any))[field] {
			t.Errorf("%s.%s must be explicitly required", schemaName, field)
		}
	}
	additiveState := []string{
		"BindingState", "BoundaryState", "ActionOfferState", "GoalState", "FactState",
		"ActorState", "ActionProposalState", "SessionState", "Snapshot",
		"MutationResult", "ProposalJob", "GenerationJob", "ErrorResponse",
	}
	for _, name := range additiveState {
		schema := schemas[name].(map[string]any)
		if schema["additionalProperties"] == false {
			t.Errorf("response/state schema %s must permit additive fields", name)
		}
	}
	unsigned := schemas["JSONSafeUnsignedInteger"].(map[string]any)
	signed := schemas["JSONSafeSignedInteger"].(map[string]any)
	if unsigned["maximum"] != float64(protocol.MaxJSONSafeInteger) ||
		signed["minimum"] != -float64(protocol.MaxJSONSafeInteger) ||
		signed["maximum"] != float64(protocol.MaxJSONSafeInteger) {
		t.Fatalf("JSON integer schemas do not use the generated safe ceiling")
	}
	positive, ok := schemas["JSONSafePositiveInteger"].(map[string]any)
	if !ok {
		t.Fatalf("missing JSONSafePositiveInteger schema")
	}
	mutationRevision := schemas["MutationResult"].(map[string]any)["properties"].(map[string]any)["revision"].(map[string]any)
	if positive["minimum"] != float64(1) ||
		positive["maximum"] != float64(protocol.MaxJSONSafeInteger) ||
		mutationRevision["$ref"] != "#/components/schemas/JSONSafePositiveInteger" {
		t.Fatalf("MutationResult revision must use the positive JSON-safe integer schema")
	}
	errorProperties := schemas["ErrorDetail"].(map[string]any)["properties"].(map[string]any)
	if errorProperties["code"].(map[string]any)["maxLength"] != float64(protocol.ErrorCodeMaxLength) ||
		errorProperties["message"].(map[string]any)["maxLength"] != float64(protocol.ErrorMessageMaxLength) ||
		errorProperties["field"].(map[string]any)["maxLength"] != float64(protocol.ErrorFieldMaxLength) {
		t.Fatalf("ErrorDetail schemas do not use the generated output bounds")
	}
	if document["x-rin-example-fixtures"] != "examples/contract_examples.json" {
		t.Fatalf("OpenAPI does not publish its example fixture location")
	}
}

func TestContractExamplesStrictGoRoundTripAndPresence(t *testing.T) {
	payload, err := os.ReadFile("examples/contract_examples.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fixtures); err != nil {
		t.Fatal(err)
	}
	type fixtureCase struct {
		newValue func() any
		validate func(any) error
	}
	cases := map[string]fixtureCase{
		"CreateSessionRequest": {
			newValue: func() any { return &protocol.CreateSessionRequest{} },
			validate: func(value any) error {
				return protocol.ValidateCreateSession(*value.(*protocol.CreateSessionRequest))
			},
		},
		"ObserveRequest": {
			newValue: func() any { return &protocol.ObserveRequest{} },
			validate: func(value any) error {
				return protocol.ValidateObserve(*value.(*protocol.ObserveRequest))
			},
		},
		"ProposeRequest": {
			newValue: func() any { return &protocol.ProposeRequest{} },
			validate: func(value any) error {
				return protocol.ValidatePropose(*value.(*protocol.ProposeRequest))
			},
		},
		"ReportActionRequest": {
			newValue: func() any { return &protocol.ReportActionRequest{} },
			validate: func(value any) error {
				return protocol.ValidateReportAction(*value.(*protocol.ReportActionRequest))
			},
		},
		"BatchActionReportRequest": {
			newValue: func() any { return &protocol.BatchActionReportRequest{} },
			validate: func(value any) error {
				return protocol.ValidateBatchActionReport(*value.(*protocol.BatchActionReportRequest))
			},
		},
		"SessionRequest": {
			newValue: func() any { return &protocol.SessionRequest{} },
			validate: func(value any) error {
				return protocol.ValidateSessionRequest(*value.(*protocol.SessionRequest))
			},
		},
		"RestoreRequest": {
			newValue: func() any { return &protocol.RestoreRequest{} },
			validate: func(value any) error {
				request := value.(*protocol.RestoreRequest)
				if err := protocol.ValidateRestore(*request); err != nil {
					return err
				}
				return protocol.ValidateSessionState(request.Snapshot.State)
			},
		},
		"GenerationRequest": {
			newValue: func() any { return &protocol.GenerationRequest{} },
			validate: func(value any) error {
				return protocol.ValidateGeneration(*value.(*protocol.GenerationRequest))
			},
		},
		"ErrorResponse": {
			newValue: func() any { return &protocol.APIResponse{} },
			validate: func(value any) error {
				response := value.(*protocol.APIResponse)
				if response.OK || response.Error == nil {
					return errors.New("error response must contain ok=false and error")
				}
				return nil
			},
		},
		"ProposalJob": {
			newValue: func() any { return &protocol.ProposalJob{} },
			validate: func(value any) error {
				job := value.(*protocol.ProposalJob)
				if job.Status != "failed" || job.Error == nil || job.FinishedAt == "" {
					return errors.New("terminal failed job must expose its data.error and finished_at")
				}
				return nil
			},
		},
	}
	if len(fixtures) != len(cases) {
		t.Fatalf("example fixture count=%d, want %d", len(fixtures), len(cases))
	}
	names := make([]string, 0, len(cases))
	for name := range cases {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		testCase := cases[name]
		t.Run(name, func(t *testing.T) {
			raw, exists := fixtures[name]
			if !exists {
				t.Fatalf("missing example fixture")
			}
			if requiredErr, err := rinapi.ValidateRequestShape(name, raw); err != nil {
				t.Fatal(err)
			} else if requiredErr != nil {
				t.Fatalf("example violates schema presence: %v", requiredErr)
			}
			first := testCase.newValue()
			decodeStrict(t, raw, first)
			if err := testCase.validate(first); err != nil {
				t.Fatalf("Go validation: %v", err)
			}
			roundTrip, err := json.Marshal(first)
			if err != nil {
				t.Fatal(err)
			}
			if requiredErr, err := rinapi.ValidateRequestShape(name, roundTrip); err != nil {
				t.Fatal(err)
			} else if requiredErr != nil {
				t.Fatalf("Go round-trip violates schema presence: %v", requiredErr)
			}
			second := testCase.newValue()
			decodeStrict(t, roundTrip, second)
			if !reflect.DeepEqual(first, second) {
				t.Fatalf("Go round-trip changed value:\\nfirst=%#v\\nsecond=%#v", first, second)
			}
		})
	}
}

func TestOptionalZeroDefaultsMatchTypedValidatorSemantics(t *testing.T) {
	payload, err := os.ReadFile("examples/contract_examples.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fixtures); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		schemaName string
		mutate     func(map[string]any)
	}{
		{"seed", "CreateSessionRequest", func(value map[string]any) { delete(value, "seed") }},
		{"enabled", "CreateSessionRequest", func(value map[string]any) {
			delete(value["actors"].([]any)[0].(map[string]any), "enabled")
		}},
		{"empty trigger tags", "CreateSessionRequest", func(value map[string]any) {
			actor := value["actors"].([]any)[0].(map[string]any)
			delete(actor["boundaries"].([]any)[0].(map[string]any), "trigger_tags")
		}},
		{"observe tick", "ObserveRequest", func(value map[string]any) { delete(value, "tick") }},
		{"confidence", "ObserveRequest", func(value map[string]any) {
			delete(value["facts"].([]any)[0].(map[string]any), "confidence")
		}},
		{"propose tick", "ProposeRequest", func(value map[string]any) { delete(value, "tick") }},
		{"action report tick", "ReportActionRequest", func(value map[string]any) { delete(value, "tick") }},
		{"batch tick", "BatchActionReportRequest", func(value map[string]any) { delete(value, "tick") }},
		{"generation temperature", "GenerationRequest", func(value map[string]any) {
			delete(value, "temperature")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var value map[string]any
			if err := json.Unmarshal(fixtures[test.schemaName], &value); err != nil {
				t.Fatal(err)
			}
			test.mutate(value)
			mutated, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			requiredErr, err := rinapi.ValidateRequestShape(test.schemaName, mutated)
			if err != nil {
				t.Fatal(err)
			}
			if requiredErr != nil {
				t.Fatalf("legal default omission conflicts with OpenAPI: %v", requiredErr)
			}
		})
	}
}

func TestActionDecisionRemainsExplicitlyRequired(t *testing.T) {
	tests := []struct {
		name       string
		schemaName string
		payload    string
		field      string
	}{
		{
			"report missing",
			"ReportActionRequest",
			`{"protocol_version":"rin.protocol/v2"}`,
			"session_id",
		},
		{
			"report decision missing",
			"ReportActionRequest",
			`{"protocol_version":"rin.protocol/v2","session_id":"s","request_id":"r","report":{"proposal_id":"p","event_id":"e","summary":""}}`,
			"report.decision",
		},
		{
			"report decision null",
			"ReportActionRequest",
			`{"protocol_version":"rin.protocol/v2","session_id":"s","request_id":"r","report":{"proposal_id":"p","event_id":"e","decision":null,"summary":""}}`,
			"report.decision",
		},
		{
			"batch decision missing",
			"BatchActionReportRequest",
			`{"protocol_version":"rin.protocol/v2","session_id":"s","request_id":"r","reports":[{"proposal_id":"p","event_id":"e","summary":""}]}`,
			"reports[0].decision",
		},
		{
			"batch decision null",
			"BatchActionReportRequest",
			`{"protocol_version":"rin.protocol/v2","session_id":"s","request_id":"r","reports":[{"proposal_id":"p","event_id":"e","decision":null,"summary":""}]}`,
			"reports[0].decision",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requiredErr, err := rinapi.ValidateRequestShape(test.schemaName, []byte(test.payload))
			if err != nil {
				t.Fatal(err)
			}
			if requiredErr == nil || requiredErr.Field != test.field {
				t.Fatalf("presence error=%v, want field %s", requiredErr, test.field)
			}
		})
	}
	for _, decision := range []string{"accepted", "rejected"} {
		payload := []byte(
			`{"protocol_version":"rin.protocol/v2","session_id":"s","request_id":"r",` +
				`"report":{"proposal_id":"p","event_id":"e","decision":"` + decision + `","summary":""}}`,
		)
		if requiredErr, err := rinapi.ValidateRequestShape("ReportActionRequest", payload); err != nil {
			t.Fatal(err)
		} else if requiredErr != nil {
			t.Fatalf("explicit decision=%s rejected: %v", decision, requiredErr)
		}
	}
}

func TestRequestShapeFollowsOpenAPIAdditionalProperties(t *testing.T) {
	payload, err := os.ReadFile("examples/contract_examples.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fixtures); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		schemaName string
		field      string
		mutate     func(map[string]any)
	}{
		{
			name: "closed request root", schemaName: "CreateSessionRequest", field: "future",
			mutate: func(value map[string]any) { value["future"] = true },
		},
		{
			name: "closed request binding", schemaName: "CreateSessionRequest", field: "binding.future",
			mutate: func(value map[string]any) {
				value["binding"].(map[string]any)["future"] = true
			},
		},
		{
			name: "closed nested input", schemaName: "CreateSessionRequest", field: "actors[0].future",
			mutate: func(value map[string]any) {
				value["actors"].([]any)[0].(map[string]any)["future"] = true
			},
		},
		{
			name: "closed expected binding", schemaName: "RestoreRequest", field: "expected_binding.future",
			mutate: func(value map[string]any) {
				value["expected_binding"].(map[string]any)["future"] = true
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var value map[string]any
			if err := json.Unmarshal(fixtures[test.schemaName], &value); err != nil {
				t.Fatal(err)
			}
			test.mutate(value)
			mutated, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			shapeErr, err := rinapi.ValidateRequestShape(test.schemaName, mutated)
			if err != nil {
				t.Fatal(err)
			}
			if shapeErr == nil ||
				shapeErr.Kind != rinapi.ShapeErrorUnknown ||
				shapeErr.Field != test.field {
				t.Fatalf("shape error=%v, want unknown field %s", shapeErr, test.field)
			}
		})
	}

	var restore map[string]any
	if err := json.Unmarshal(fixtures["RestoreRequest"], &restore); err != nil {
		t.Fatal(err)
	}
	snapshot := restore["snapshot"].(map[string]any)
	state := snapshot["state"].(map[string]any)
	snapshot["future_snapshot"] = map[string]any{"version": 2}
	state["future_state"] = "preserved by a tolerant client"
	state["binding"].(map[string]any)["future_binding"] = true
	state["actors"].(map[string]any)["actor.mira"].(map[string]any)["future_actor"] = true
	additive, err := json.Marshal(restore)
	if err != nil {
		t.Fatal(err)
	}
	if shapeErr, err := rinapi.ValidateRequestShape("RestoreRequest", additive); err != nil {
		t.Fatal(err)
	} else if shapeErr != nil {
		t.Fatalf("OpenAPI-additive Snapshot/State member was rejected: %v", shapeErr)
	}

	delete(
		state["actors"].(map[string]any)["actor.mira"].(map[string]any),
		"enabled",
	)
	missingKnown, err := json.Marshal(restore)
	if err != nil {
		t.Fatal(err)
	}
	if shapeErr, err := rinapi.ValidateRequestShape("RestoreRequest", missingKnown); err != nil {
		t.Fatal(err)
	} else if shapeErr == nil ||
		shapeErr.Kind != rinapi.ShapeErrorRequired ||
		shapeErr.Field != "snapshot.state.actors.actor.mira.enabled" {
		t.Fatalf("missing known open-state field error=%v", shapeErr)
	}
}

func TestOptionalNonNullableFieldsRejectNull(t *testing.T) {
	payload := []byte(`{
		"protocol_version":"rin.protocol/v1",
		"request_id":"request.null-seed",
		"session_id":"session.null-seed",
		"binding":{
			"game_id":"game.example",
			"content_id":"base",
			"content_version":"1",
			"content_hash":"hash"
		},
		"seed":null,
		"actors":[]
	}`)
	shapeErr, err := rinapi.ValidateRequestShape("CreateSessionRequest", payload)
	if err != nil {
		t.Fatal(err)
	}
	if shapeErr == nil ||
		shapeErr.Kind != rinapi.ShapeErrorRequired ||
		shapeErr.Field != "seed" ||
		shapeErr.Message != "must not be null" {
		t.Fatalf("nullable shape error=%v, want seed must not be null", shapeErr)
	}
}

func decodeStrict(t *testing.T, payload []byte, target any) {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		t.Fatal(err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatalf("example contains trailing JSON: %v", err)
	}
}

func collectReferences(value any, references *[]string) {
	switch typed := value.(type) {
	case []any:
		for _, member := range typed {
			collectReferences(member, references)
		}
	case map[string]any:
		if reference, ok := typed["$ref"].(string); ok {
			*references = append(*references, reference)
		}
		for _, member := range typed {
			collectReferences(member, references)
		}
	}
}

func resolveJSONPointer(document map[string]any, reference string) (any, bool) {
	if !strings.HasPrefix(reference, "#/") {
		return nil, false
	}
	var current any = document
	for _, encoded := range strings.Split(strings.TrimPrefix(reference, "#/"), "/") {
		part := strings.ReplaceAll(strings.ReplaceAll(encoded, "~1", "/"), "~0", "~")
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func requiredSet(schema map[string]any) map[string]bool {
	result := make(map[string]bool)
	for _, member := range schema["required"].([]any) {
		result[member.(string)] = true
	}
	return result
}
