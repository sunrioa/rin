package managementapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	rinapi "github.com/sunrioa/rin/api"
	"github.com/sunrioa/rin/cognition"
)

func TestManagementOpenAPIRoutesAreRegistered(t *testing.T) {
	personas, err := cognition.RestoreLocalPersonaProvider(cognition.DefaultPersonaSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	memory, err := cognition.NewLocalMemoryProvider(cognition.LocalMemoryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(personas, memory)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHTTPHandler(service, HTTPOptions{Token: "contract-test-token"})
	if err != nil {
		t.Fatal(err)
	}
	routes, err := rinapi.ParseManagementRoutes()
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range routes {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(route.Method, route.Path, nil)
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Errorf("%s %s is not registered: status=%d body=%s",
				route.Method, route.Path, response.Code, response.Body.String())
		}
	}
}
