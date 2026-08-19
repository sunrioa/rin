package managementapi

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/controlplane"
)

const maxRequestBytes int64 = 1 << 20

type HTTPOptions struct {
	Token string
}

func NewHTTPHandler(service *Service, options HTTPOptions) (http.Handler, error) {
	if service == nil || strings.TrimSpace(options.Token) == "" {
		return nil, errors.New("management service and bearer token are required")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /management/v1/info", secure(options.Token, func(response http.ResponseWriter, _ *http.Request) {
		features := []string{"personas", "memory-cards", "long-goals"}
		if service.skills != nil {
			features = append(features, "skills")
		}
		if service.control != nil {
			features = append(features, "runtime", "operations", "actor-control")
		}
		if service.diagnostics != nil {
			features = append(features, "diagnostics", "configuration", "mcp-install")
		}
		if service.agentConfig != nil {
			features = append(features, "agent-config")
		}
		if service.policyConfig != nil {
			features = append(features, "policy-config")
		}
		writeJSON(response, http.StatusOK, map[string]any{
			"contract_version": "rin.management/v1",
			"features":         features,
		})
	}))
	mux.HandleFunc("GET /management/v1/personas", secure(options.Token, func(response http.ResponseWriter, request *http.Request) {
		snapshot, err := service.PersonaSnapshot(request.Context())
		writeResult(response, snapshot, err)
	}))
	mux.HandleFunc("PUT /management/v1/personas", secure(options.Token, func(response http.ResponseWriter, request *http.Request) {
		var snapshot cognition.PersonaSnapshot
		if err := decodeJSON(request, &snapshot); err != nil {
			writeError(response, http.StatusBadRequest, err)
			return
		}
		updated, err := service.ReplacePersonas(request.Context(), snapshot)
		writeResult(response, updated, err)
	}))
	mux.HandleFunc("POST /management/v1/memories/list", secure(options.Token, func(response http.ResponseWriter, request *http.Request) {
		var input MemoryListRequest
		if err := decodeJSON(request, &input); err != nil {
			writeError(response, http.StatusBadRequest, err)
			return
		}
		result, err := service.ListMemories(request.Context(), input)
		writeResult(response, result, err)
	}))
	mux.HandleFunc("POST /management/v1/memories/save", secure(options.Token, func(response http.ResponseWriter, request *http.Request) {
		var input MemoryCardInput
		if err := decodeJSON(request, &input); err != nil {
			writeError(response, http.StatusBadRequest, err)
			return
		}
		result, err := service.SaveMemoryCard(request.Context(), input)
		writeResult(response, result, err)
	}))
	mux.HandleFunc("POST /management/v1/memories/forget", secure(options.Token, func(response http.ResponseWriter, request *http.Request) {
		var input MemoryForgetInput
		if err := decodeJSON(request, &input); err != nil {
			writeError(response, http.StatusBadRequest, err)
			return
		}
		err := service.ForgetMemory(request.Context(), input)
		writeResult(response, map[string]bool{"forgotten": err == nil}, err)
	}))
	mux.HandleFunc("POST /management/v1/tasks/list", secure(options.Token, func(response http.ResponseWriter, request *http.Request) {
		var input TaskListInput
		if err := decodeJSON(request, &input); err != nil {
			writeError(response, http.StatusBadRequest, err)
			return
		}
		result, err := service.ListTasks(request.Context(), input)
		writeResult(response, result, err)
	}))
	mux.HandleFunc("POST /management/v1/tasks/start", secure(options.Token, func(response http.ResponseWriter, request *http.Request) {
		var input TaskStartInput
		if err := decodeJSON(request, &input); err != nil {
			writeError(response, http.StatusBadRequest, err)
			return
		}
		result, err := service.StartTask(request.Context(), input)
		writeResult(response, result, err)
	}))
	mux.HandleFunc("POST /management/v1/tasks/get", secure(options.Token, func(response http.ResponseWriter, request *http.Request) {
		var input TaskGetInput
		if err := decodeJSON(request, &input); err != nil {
			writeError(response, http.StatusBadRequest, err)
			return
		}
		result, err := service.GetTask(request.Context(), input)
		writeResult(response, result, err)
	}))
	mux.HandleFunc("POST /management/v1/tasks/control", secure(options.Token, func(response http.ResponseWriter, request *http.Request) {
		var input TaskControlInput
		if err := decodeJSON(request, &input); err != nil {
			writeError(response, http.StatusBadRequest, err)
			return
		}
		result, err := service.ControlTask(request.Context(), input)
		writeResult(response, result, err)
	}))
	mux.HandleFunc("POST /management/v1/skills/list", secure(options.Token, func(response http.ResponseWriter, request *http.Request) {
		var input SkillListInput
		if err := decodeJSON(request, &input); err != nil {
			writeError(response, http.StatusBadRequest, err)
			return
		}
		result, err := service.ListSkills(request.Context(), input)
		writeResult(response, result, err)
	}))
	mux.HandleFunc("POST /management/v1/skills/get", secure(options.Token, func(response http.ResponseWriter, request *http.Request) {
		var input SkillGetInput
		if err := decodeJSON(request, &input); err != nil {
			writeError(response, http.StatusBadRequest, err)
			return
		}
		result, err := service.GetSkill(request.Context(), input)
		writeResult(response, result, err)
	}))
	mux.HandleFunc("POST /management/v1/skills/save", secure(options.Token, func(response http.ResponseWriter, request *http.Request) {
		var input SkillSaveInput
		if err := decodeJSON(request, &input); err != nil {
			writeError(response, http.StatusBadRequest, err)
			return
		}
		result, err := service.SaveSkill(request.Context(), input)
		writeResult(response, result, err)
	}))
	mux.HandleFunc("POST /management/v1/skills/reload", secure(options.Token, func(response http.ResponseWriter, request *http.Request) {
		if err := requireEmptyJSON(request); err != nil {
			writeError(response, http.StatusBadRequest, err)
			return
		}
		result, err := service.ReloadSkills(request.Context())
		writeResult(response, result, err)
	}))
	mux.HandleFunc("POST /management/v1/skills/import", secure(options.Token, func(response http.ResponseWriter, request *http.Request) {
		var input SkillImportInput
		if err := decodeJSON(request, &input); err != nil {
			writeError(response, http.StatusBadRequest, err)
			return
		}
		result, err := service.ImportSkill(request.Context(), input)
		writeResult(response, result, err)
	}))
	mux.HandleFunc("POST /management/v1/skills/remove", secure(options.Token, func(response http.ResponseWriter, request *http.Request) {
		var input SkillRemoveInput
		if err := decodeJSON(request, &input); err != nil {
			writeError(response, http.StatusBadRequest, err)
			return
		}
		result, err := service.RemoveSkill(request.Context(), input)
		writeResult(response, result, err)
	}))
	mux.HandleFunc("GET /management/v1/runtime", secure(options.Token, func(response http.ResponseWriter, request *http.Request) {
		result, err := service.RuntimeSnapshot(request.Context())
		writeResult(response, result, err)
	}))
	mux.HandleFunc("GET /management/v1/diagnostics", secure(options.Token, func(response http.ResponseWriter, request *http.Request) {
		result, err := service.Diagnostics(request.Context())
		writeResult(response, result, err)
	}))
	mux.HandleFunc("GET /management/v1/agent/config", secure(options.Token, func(response http.ResponseWriter, request *http.Request) {
		result, err := service.AgentConfig(request.Context())
		writeResult(response, result, err)
	}))
	mux.HandleFunc("PUT /management/v1/agent/config", secure(options.Token, func(response http.ResponseWriter, request *http.Request) {
		var input AgentConfigSaveRequest
		if err := decodeJSON(request, &input); err != nil {
			writeError(response, http.StatusBadRequest, err)
			return
		}
		result, err := service.SaveAgentConfig(request.Context(), input)
		writeResult(response, result, err)
	}))
	mux.HandleFunc("GET /management/v1/policy/config", secure(options.Token, func(response http.ResponseWriter, request *http.Request) {
		result, err := service.PolicyConfig(request.Context())
		writeResult(response, result, err)
	}))
	mux.HandleFunc("PUT /management/v1/policy/config", secure(options.Token, func(response http.ResponseWriter, request *http.Request) {
		var input PolicyConfigSaveRequest
		if err := decodeJSON(request, &input); err != nil {
			writeError(response, http.StatusBadRequest, err)
			return
		}
		result, err := service.SavePolicyConfig(request.Context(), input)
		writeResult(response, result, err)
	}))
	mux.HandleFunc("POST /management/v1/operations/list", secure(options.Token, func(response http.ResponseWriter, request *http.Request) {
		var input controlplane.ListOperationsInput
		if err := decodeJSON(request, &input); err != nil {
			writeError(response, http.StatusBadRequest, err)
			return
		}
		result, err := service.ListOperations(request.Context(), input)
		writeResult(response, result, err)
	}))
	mux.HandleFunc("POST /management/v1/operations/control", secure(options.Token, func(response http.ResponseWriter, request *http.Request) {
		var input OperationControlInput
		if err := decodeJSON(request, &input); err != nil {
			writeError(response, http.StatusBadRequest, err)
			return
		}
		result, err := service.ControlOperation(request.Context(), input)
		writeResult(response, result, err)
	}))
	mux.HandleFunc("POST /management/v1/actors/control", secure(options.Token, func(response http.ResponseWriter, request *http.Request) {
		var input ActorControlInput
		if err := decodeJSON(request, &input); err != nil {
			writeError(response, http.StatusBadRequest, err)
			return
		}
		result, err := service.ControlActor(request.Context(), input)
		writeResult(response, result, err)
	}))
	return mux, nil
}

func secure(token string, next http.HandlerFunc) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		authorization := request.Header.Get("Authorization")
		provided := strings.TrimPrefix(authorization, "Bearer ")
		if !strings.HasPrefix(authorization, "Bearer ") ||
			subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
			writeError(response, http.StatusUnauthorized, errors.New("bearer token is invalid"))
			return
		}
		next(response, request)
	}
}

func decodeJSON(request *http.Request, target any) error {
	defer request.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(request.Body, maxRequestBytes+1))
	if err != nil {
		return err
	}
	if int64(len(payload)) > maxRequestBytes {
		return errors.New("request body exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("request must contain one JSON value")
	}
	return nil
}

func requireEmptyJSON(request *http.Request) error {
	var value map[string]any
	if err := decodeJSON(request, &value); err != nil {
		return err
	}
	if len(value) != 0 {
		return errors.New("request must contain an empty object")
	}
	return nil
}

func writeResult(response http.ResponseWriter, value any, err error) {
	if err == nil {
		writeJSON(response, http.StatusOK, value)
		return
	}
	status := http.StatusBadRequest
	if errors.Is(err, cognition.ErrPersonaConflict) {
		status = http.StatusConflict
	} else if errors.Is(err, cognition.ErrProviderNotFound) {
		status = http.StatusNotFound
	} else if errors.Is(err, ErrTasksUnavailable) || errors.Is(err, ErrSkillsUnavailable) {
		status = http.StatusServiceUnavailable
	} else if errors.Is(err, ErrDiagnosticsUnavailable) {
		status = http.StatusServiceUnavailable
	} else if errors.Is(err, ErrAgentConfigUnavailable) {
		status = http.StatusServiceUnavailable
	} else if errors.Is(err, ErrInvalidAgentConfig) {
		status = http.StatusBadRequest
	} else if errors.Is(err, ErrPolicyConfigUnavailable) {
		status = http.StatusServiceUnavailable
	} else if errors.Is(err, ErrInvalidPolicyConfig) {
		status = http.StatusBadRequest
	} else if errors.Is(err, ErrPolicyConfigConflict) {
		status = http.StatusConflict
	} else if errors.Is(err, controlplane.ErrForbidden) {
		status = http.StatusForbidden
	} else if errors.Is(err, controlplane.ErrNotFound) {
		status = http.StatusNotFound
	} else if errors.Is(err, controlplane.ErrConflict) ||
		errors.Is(err, controlplane.ErrLeaseConflict) ||
		errors.Is(err, controlplane.ErrLeaseExpired) {
		status = http.StatusConflict
	} else if errors.Is(err, controlplane.ErrUnavailable) ||
		errors.Is(err, ErrControlUnavailable) {
		status = http.StatusServiceUnavailable
	}
	writeError(response, status, err)
}

func writeError(response http.ResponseWriter, status int, err error) {
	writeJSON(response, status, map[string]any{
		"error": map[string]string{"message": err.Error()},
	})
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
