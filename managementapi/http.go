package managementapi

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/sunrioa/rin/cognition"
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
		writeJSON(response, http.StatusOK, map[string]any{
			"contract_version": "rin.management/v1",
			"features":         []string{"personas", "memory-cards"},
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
	decoder := json.NewDecoder(io.LimitReader(request.Body, maxRequestBytes+1))
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
	} else if errors.Is(err, ErrTasksUnavailable) {
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
