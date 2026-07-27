package httpapi

import (
	"context"
	"net/http/httptest"
	"testing"

	rinapi "github.com/sunrioa/rin/api"
)

func TestGeneratedRequestSchemasResolve(t *testing.T) {
	for _, route := range generatedContractRoutes {
		if route.RequestSchema == "" {
			continue
		}
		if _, err := rinapi.ValidateRequestShape(route.RequestSchema, []byte(`{}`)); err != nil {
			t.Errorf("%s request schema %q: %v", route.OperationID, route.RequestSchema, err)
		}
	}
}

func TestContractRequestSchemaUsesRouteMetadata(t *testing.T) {
	request := httptest.NewRequest("POST", "/v2/session/create", nil)
	if _, err := contractRequestSchema(request); err == nil {
		t.Fatal("request without generated route metadata was accepted")
	}
	request = request.WithContext(context.WithValue(
		request.Context(),
		contractRouteContextKey{},
		ContractRoute{
			OperationID:   "create_session",
			RequestSchema: "CreateSessionRequest",
		},
	))
	schema, err := contractRequestSchema(request)
	if err != nil || schema != "CreateSessionRequest" {
		t.Fatalf("schema=%q err=%v", schema, err)
	}
}
