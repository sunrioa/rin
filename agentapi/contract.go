package agentapi

import (
	"fmt"
	"net/http"
)

// ContractRoute is the runtime projection of one Agent OpenAPI operation.
type ContractRoute struct {
	OperationID   string
	Method        string
	Path          string
	SuccessStatus int
}

var contractRoutes = [...]ContractRoute{
	{OperationID: "agent_v1_info", Method: http.MethodGet, Path: "/agent/v1/info", SuccessStatus: http.StatusOK},
	{OperationID: "agent_v1_start_task", Method: http.MethodPost, Path: "/agent/v1/tasks/start", SuccessStatus: http.StatusAccepted},
	{OperationID: "agent_v1_get_task", Method: http.MethodPost, Path: "/agent/v1/tasks/get", SuccessStatus: http.StatusOK},
	{OperationID: "agent_v1_run_task", Method: http.MethodPost, Path: "/agent/v1/tasks/run", SuccessStatus: http.StatusAccepted},
	{OperationID: "agent_v1_resume_task", Method: http.MethodPost, Path: "/agent/v1/tasks/resume", SuccessStatus: http.StatusAccepted},
	{OperationID: "agent_v1_cancel_task", Method: http.MethodPost, Path: "/agent/v1/tasks/cancel", SuccessStatus: http.StatusAccepted},
	{OperationID: "agent_v1_get_task_timeline", Method: http.MethodPost, Path: "/agent/v1/tasks/timeline/get", SuccessStatus: http.StatusOK},
	{OperationID: "agent_v1_wait_task_timeline", Method: http.MethodPost, Path: "/agent/v1/tasks/timeline/wait", SuccessStatus: http.StatusOK},
}

// ContractRoutes returns a defensive copy of the Agent HTTP route inventory.
func ContractRoutes() []ContractRoute {
	return append([]ContractRoute(nil), contractRoutes[:]...)
}

func (server *HTTPHandler) registerContractRoutes(mux *http.ServeMux) {
	handlers := map[string]http.HandlerFunc{
		"agent_v1_info":               server.info,
		"agent_v1_start_task":         server.startTask,
		"agent_v1_get_task":           server.getTask,
		"agent_v1_run_task":           server.runTask,
		"agent_v1_resume_task":        server.resumeTask,
		"agent_v1_cancel_task":        server.cancelTask,
		"agent_v1_get_task_timeline":  server.getTaskTimeline,
		"agent_v1_wait_task_timeline": server.waitTaskTimeline,
	}
	if len(handlers) != len(contractRoutes) {
		panic(fmt.Sprintf(
			"agentapi: %d handlers do not match %d contract routes",
			len(handlers), len(contractRoutes),
		))
	}
	seen := make(map[string]struct{}, len(contractRoutes))
	for _, route := range contractRoutes {
		handler := handlers[route.OperationID]
		if handler == nil {
			panic("agentapi: missing handler for " + route.OperationID)
		}
		pattern := route.Method + " " + route.Path
		if _, duplicate := seen[pattern]; duplicate {
			panic("agentapi: duplicate route " + pattern)
		}
		seen[pattern] = struct{}{}
		mux.HandleFunc(pattern, handler)
	}
}
