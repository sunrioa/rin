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
	RequestSchema string
}

var generatedContractRoutes = [...]ContractRoute{
	{OperationID: "health", Method: http.MethodGet, Path: "/health", SuccessStatus: http.StatusOK, RequestSchema: ""},
	{OperationID: "ready", Method: http.MethodGet, Path: "/ready", SuccessStatus: http.StatusOK, RequestSchema: ""},
	{OperationID: "metrics", Method: http.MethodGet, Path: "/metrics", SuccessStatus: http.StatusOK, RequestSchema: ""},
	{OperationID: "diagnostics", Method: http.MethodGet, Path: "/v2/diagnostics", SuccessStatus: http.StatusOK, RequestSchema: ""},
	{OperationID: "create_session", Method: http.MethodPost, Path: "/v2/session/create", SuccessStatus: http.StatusOK, RequestSchema: "CreateSessionRequest"},
	{OperationID: "observe", Method: http.MethodPost, Path: "/v2/session/observe", SuccessStatus: http.StatusOK, RequestSchema: "ObserveRequest"},
	{OperationID: "propose", Method: http.MethodPost, Path: "/v2/agent/propose", SuccessStatus: http.StatusOK, RequestSchema: "ProposeRequest"},
	{OperationID: "submit_proposal_job", Method: http.MethodPost, Path: "/v2/jobs/propose", SuccessStatus: http.StatusAccepted, RequestSchema: "ProposeRequest"},
	{OperationID: "get_proposal_job", Method: http.MethodGet, Path: "/v2/jobs/{job_id}", SuccessStatus: http.StatusOK, RequestSchema: ""},
	{OperationID: "cancel_proposal_job", Method: http.MethodDelete, Path: "/v2/jobs/{job_id}", SuccessStatus: http.StatusOK, RequestSchema: ""},
	{OperationID: "submit_generation_job", Method: http.MethodPost, Path: "/v2/generation/jobs", SuccessStatus: http.StatusAccepted, RequestSchema: "GenerationRequest"},
	{OperationID: "get_generation_job", Method: http.MethodGet, Path: "/v2/generation/jobs/{job_id}", SuccessStatus: http.StatusOK, RequestSchema: ""},
	{OperationID: "cancel_generation_job", Method: http.MethodDelete, Path: "/v2/generation/jobs/{job_id}", SuccessStatus: http.StatusOK, RequestSchema: ""},
	{OperationID: "set_actor_activity", Method: http.MethodPost, Path: "/v2/session/activity", SuccessStatus: http.StatusOK, RequestSchema: "SetActorActivityRequest"},
	{OperationID: "arbitrate", Method: http.MethodPost, Path: "/v2/world/arbitrate", SuccessStatus: http.StatusOK, RequestSchema: "ArbitrateRequest"},
	{OperationID: "state", Method: http.MethodPost, Path: "/v2/session/get", SuccessStatus: http.StatusOK, RequestSchema: "SessionRequest"},
	{OperationID: "session_stats", Method: http.MethodPost, Path: "/v2/session/stats", SuccessStatus: http.StatusOK, RequestSchema: "SessionRequest"},
	{OperationID: "archive_session", Method: http.MethodPost, Path: "/v2/session/archive", SuccessStatus: http.StatusOK, RequestSchema: "ArchiveSessionRequest"},
	{OperationID: "delete_session", Method: http.MethodPost, Path: "/v2/session/delete", SuccessStatus: http.StatusOK, RequestSchema: "DeleteSessionRequest"},
	{OperationID: "snapshot", Method: http.MethodPost, Path: "/v2/session/snapshot", SuccessStatus: http.StatusOK, RequestSchema: "SessionRequest"},
	{OperationID: "restore", Method: http.MethodPost, Path: "/v2/session/restore", SuccessStatus: http.StatusOK, RequestSchema: "RestoreRequest"},
	{OperationID: "export_session", Method: http.MethodPost, Path: "/v2/session/export", SuccessStatus: http.StatusOK, RequestSchema: "SessionRequest"},
	{OperationID: "import_session", Method: http.MethodPost, Path: "/v2/session/import", SuccessStatus: http.StatusOK, RequestSchema: ""},
	{OperationID: "timeline", Method: http.MethodPost, Path: "/v2/session/timeline", SuccessStatus: http.StatusOK, RequestSchema: "TimelineRequest"},
	{OperationID: "replay", Method: http.MethodPost, Path: "/v2/session/replay", SuccessStatus: http.StatusOK, RequestSchema: "ReplayRequest"},
	{OperationID: "due_agents", Method: http.MethodPost, Path: "/v2/scheduler/due", SuccessStatus: http.StatusOK, RequestSchema: "DueAgentsRequest"},
	{OperationID: "report_action", Method: http.MethodPost, Path: "/v2/action/report", SuccessStatus: http.StatusOK, RequestSchema: "ReportActionRequest"},
	{OperationID: "report_action_batch", Method: http.MethodPost, Path: "/v2/action/report-batch", SuccessStatus: http.StatusOK, RequestSchema: "BatchActionReportRequest"},
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

func contractRouteForRequest(request *http.Request) (ContractRoute, error) {
	route, ok := request.Context().Value(contractRouteContextKey{}).(ContractRoute)
	if !ok {
		return ContractRoute{}, fmt.Errorf("request is missing generated OpenAPI route metadata")
	}
	return route, nil
}

func contractSuccessStatus(request *http.Request) int {
	route, err := contractRouteForRequest(request)
	if err != nil {
		panic("httpapi: " + err.Error())
	}
	return route.SuccessStatus
}

func contractRequestSchema(request *http.Request) (string, error) {
	route, err := contractRouteForRequest(request)
	if err != nil {
		return "", err
	}
	if route.RequestSchema == "" {
		return "", fmt.Errorf(
			"OpenAPI operation %s has no application/json request schema",
			route.OperationID,
		)
	}
	return route.RequestSchema, nil
}

func (s *Server) registerContractRoutes(mux *http.ServeMux) {
	handlers := map[string]http.HandlerFunc{
		"health":                s.health,
		"ready":                 s.ready,
		"metrics":               s.metrics,
		"diagnostics":           s.diagnostics,
		"create_session":        s.createSession,
		"observe":               s.observe,
		"propose":               s.propose,
		"submit_proposal_job":   s.submitProposalJob,
		"get_proposal_job":      s.getProposalJob,
		"cancel_proposal_job":   s.cancelProposalJob,
		"submit_generation_job": s.submitGenerationJob,
		"get_generation_job":    s.getGenerationJob,
		"cancel_generation_job": s.cancelGenerationJob,
		"set_actor_activity":    s.setActorActivity,
		"arbitrate":             s.arbitrate,
		"state":                 s.getSession,
		"session_stats":         s.sessionStats,
		"archive_session":       s.archiveSession,
		"delete_session":        s.deleteSession,
		"snapshot":              s.snapshot,
		"restore":               s.restore,
		"export_session":        s.exportSession,
		"import_session":        s.importSession,
		"timeline":              s.timeline,
		"replay":                s.replay,
		"due_agents":            s.dueAgents,
		"report_action":         s.reportAction,
		"report_action_batch":   s.reportActionBatch,
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
