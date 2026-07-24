// Code generated from api/openapi.json; DO NOT EDIT.

package httpapi

import (
	"context"
	"fmt"
	"net/http"
)

// ContractRoute is the generated HTTP projection of one OpenAPI operation.
// It is exported for conformance tooling; callers must treat returned values as
// immutable contract metadata.
type ContractRoute struct {
	OperationID   string
	Method        string
	Path          string
	SuccessStatus int
}

var generatedContractRoutes = [...]ContractRoute{
	{OperationID: "health", Method: http.MethodGet, Path: "/health", SuccessStatus: http.StatusOK},
	{OperationID: "create_session", Method: http.MethodPost, Path: "/v1/session/create", SuccessStatus: http.StatusOK},
	{OperationID: "observe", Method: http.MethodPost, Path: "/v1/session/observe", SuccessStatus: http.StatusOK},
	{OperationID: "propose", Method: http.MethodPost, Path: "/v1/agent/propose", SuccessStatus: http.StatusOK},
	{OperationID: "submit_proposal_job", Method: http.MethodPost, Path: "/v1/jobs/propose", SuccessStatus: http.StatusAccepted},
	{OperationID: "get_proposal_job", Method: http.MethodGet, Path: "/v1/jobs/{job_id}", SuccessStatus: http.StatusOK},
	{OperationID: "cancel_proposal_job", Method: http.MethodDelete, Path: "/v1/jobs/{job_id}", SuccessStatus: http.StatusOK},
	{OperationID: "submit_generation_job", Method: http.MethodPost, Path: "/v1/generation/jobs", SuccessStatus: http.StatusAccepted},
	{OperationID: "get_generation_job", Method: http.MethodGet, Path: "/v1/generation/jobs/{job_id}", SuccessStatus: http.StatusOK},
	{OperationID: "cancel_generation_job", Method: http.MethodDelete, Path: "/v1/generation/jobs/{job_id}", SuccessStatus: http.StatusOK},
	{OperationID: "commit", Method: http.MethodPost, Path: "/v1/action/commit", SuccessStatus: http.StatusOK},
	{OperationID: "commit_batch", Method: http.MethodPost, Path: "/v1/action/commit-batch", SuccessStatus: http.StatusOK},
	{OperationID: "set_actor_activity", Method: http.MethodPost, Path: "/v1/session/activity", SuccessStatus: http.StatusOK},
	{OperationID: "arbitrate", Method: http.MethodPost, Path: "/v1/world/arbitrate", SuccessStatus: http.StatusOK},
	{OperationID: "state", Method: http.MethodPost, Path: "/v1/session/get", SuccessStatus: http.StatusOK},
	{OperationID: "snapshot", Method: http.MethodPost, Path: "/v1/session/snapshot", SuccessStatus: http.StatusOK},
	{OperationID: "restore", Method: http.MethodPost, Path: "/v1/session/restore", SuccessStatus: http.StatusOK},
	{OperationID: "timeline", Method: http.MethodPost, Path: "/v1/session/timeline", SuccessStatus: http.StatusOK},
	{OperationID: "replay", Method: http.MethodPost, Path: "/v1/session/replay", SuccessStatus: http.StatusOK},
	{OperationID: "due_agents", Method: http.MethodPost, Path: "/v1/scheduler/due", SuccessStatus: http.StatusOK},
}

// ContractRoutes returns a defensive copy of the generated route inventory.
func ContractRoutes() []ContractRoute {
	return append([]ContractRoute(nil), generatedContractRoutes[:]...)
}

type contractRouteContextKey struct{}

func withContractRoute(route ContractRoute, handler http.HandlerFunc) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		ctx := context.WithValue(request.Context(), contractRouteContextKey{}, route)
		handler(response, request.WithContext(ctx))
	}
}

func contractSuccessStatus(request *http.Request) int {
	route, ok := request.Context().Value(contractRouteContextKey{}).(ContractRoute)
	if !ok {
		panic("httpapi: request is missing generated OpenAPI route metadata")
	}
	return route.SuccessStatus
}

func (s *Server) registerContractRoutes(mux *http.ServeMux) {
	handlers := map[string]http.HandlerFunc{
		"health":                s.health,
		"create_session":        s.createSession,
		"observe":               s.observe,
		"propose":               s.propose,
		"submit_proposal_job":   s.submitProposalJob,
		"get_proposal_job":      s.getProposalJob,
		"cancel_proposal_job":   s.cancelProposalJob,
		"submit_generation_job": s.submitGenerationJob,
		"get_generation_job":    s.getGenerationJob,
		"cancel_generation_job": s.cancelGenerationJob,
		"commit":                s.commit,
		"commit_batch":          s.commitBatch,
		"set_actor_activity":    s.setActorActivity,
		"arbitrate":             s.arbitrate,
		"state":                 s.getSession,
		"snapshot":              s.snapshot,
		"restore":               s.restore,
		"timeline":              s.timeline,
		"replay":                s.replay,
		"due_agents":            s.dueAgents,
	}
	if len(handlers) != len(generatedContractRoutes) {
		panic(fmt.Sprintf(
			"httpapi: generated contract has %d routes but server has %d handlers",
			len(generatedContractRoutes),
			len(handlers),
		))
	}
	seenPatterns := make(map[string]string, len(generatedContractRoutes))
	for _, route := range generatedContractRoutes {
		handler, exists := handlers[route.OperationID]
		if !exists {
			panic("httpapi: no handler for OpenAPI operation " + route.OperationID)
		}
		pattern := route.Method + " " + route.Path
		if previous, duplicate := seenPatterns[pattern]; duplicate {
			panic(fmt.Sprintf(
				"httpapi: OpenAPI operations %s and %s share route %s",
				previous,
				route.OperationID,
				pattern,
			))
		}
		seenPatterns[pattern] = route.OperationID
		mux.HandleFunc(pattern, withContractRoute(route, handler))
		delete(handlers, route.OperationID)
	}
	if len(handlers) != 0 {
		panic("httpapi: server has handlers absent from the generated OpenAPI route table")
	}
}
