package agentapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/internal/httpjson"
)

const defaultHTTPMaxBodyBytes int64 = 1 << 20

type HTTPOptions struct {
	Token           string
	ClientPrincipal host.Principal
	MaxBodyBytes    int64
}

type HTTPHandler struct {
	client       *ClientService
	token        string
	maxBodyBytes int64
	handler      http.Handler
}

type errorResponse struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

func NewHTTPHandler(service *Service, options HTTPOptions) (*HTTPHandler, error) {
	if len(options.Token) < 32 {
		return nil, fmt.Errorf("%w: token must contain at least 32 bytes", ErrInvalid)
	}
	maximum := options.MaxBodyBytes
	if maximum == 0 {
		maximum = defaultHTTPMaxBodyBytes
	}
	if maximum < 1 || maximum > 8<<20 {
		return nil, fmt.Errorf("%w: max body must be between 1 byte and 8 MiB", ErrInvalid)
	}
	client, err := NewClientService(service, options.ClientPrincipal)
	if err != nil {
		return nil, err
	}
	server := &HTTPHandler{
		client: client, token: options.Token, maxBodyBytes: maximum,
	}
	mux := http.NewServeMux()
	server.registerContractRoutes(mux)
	server.handler = server.secure(mux)
	return server, nil
}

func (server *HTTPHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	server.handler.ServeHTTP(response, request)
}

func (server *HTTPHandler) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		response.Header().Set("X-Content-Type-Options", "nosniff")
		if !httpjson.Authorized(request, server.token) {
			response.Header().Set("WWW-Authenticate", "Bearer")
			writeHTTPError(response, http.StatusUnauthorized, "forbidden", "unauthorized")
			return
		}
		next.ServeHTTP(response, request)
	})
}

func (server *HTTPHandler) info(response http.ResponseWriter, request *http.Request) {
	info, err := server.client.Info(request.Context())
	if err != nil {
		writeTaskError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, info)
}

func (server *HTTPHandler) startTask(response http.ResponseWriter, request *http.Request) {
	var input cognition.StartTaskInput
	if err := server.decode(response, request, &input); err != nil {
		writeHTTPError(response, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	dispatch, err := server.client.StartTask(request.Context(), input)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	writeJSON(response, http.StatusAccepted, dispatch)
}

func (server *HTTPHandler) getTask(response http.ResponseWriter, request *http.Request) {
	var input TaskTarget
	if err := server.decode(response, request, &input); err != nil {
		writeHTTPError(response, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	task, err := server.client.GetTask(request.Context(), input.TaskID)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, task)
}

func (server *HTTPHandler) getTaskTimeline(
	response http.ResponseWriter,
	request *http.Request,
) {
	var input TaskTimelineQuery
	if err := server.decode(response, request, &input); err != nil {
		writeHTTPError(response, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	page, err := server.client.GetTaskTimeline(request.Context(), input)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, page)
}

func (server *HTTPHandler) waitTaskTimeline(
	response http.ResponseWriter,
	request *http.Request,
) {
	var input WaitTaskTimelineInput
	if err := server.decode(response, request, &input); err != nil {
		writeHTTPError(response, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	update, err := server.client.WaitTaskTimeline(request.Context(), input)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, update)
}

func (server *HTTPHandler) runTask(response http.ResponseWriter, request *http.Request) {
	server.dispatchTarget(response, request, server.client.RunTask)
}

func (server *HTTPHandler) resumeTask(response http.ResponseWriter, request *http.Request) {
	server.dispatchTarget(response, request, server.client.ResumeTask)
}

func (server *HTTPHandler) cancelTask(response http.ResponseWriter, request *http.Request) {
	server.dispatchTarget(response, request, server.client.CancelTask)
}

func (server *HTTPHandler) dispatchTarget(
	response http.ResponseWriter,
	request *http.Request,
	dispatch func(context.Context, string) (TaskDispatch, error),
) {
	var input TaskTarget
	if err := server.decode(response, request, &input); err != nil {
		writeHTTPError(response, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	result, err := dispatch(request.Context(), input.TaskID)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	writeJSON(response, http.StatusAccepted, result)
}

func (server *HTTPHandler) decode(
	response http.ResponseWriter,
	request *http.Request,
	target any,
) error {
	return httpjson.DecodeRequest(response, request, server.maxBodyBytes, target)
}

func writeTaskError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalid):
		writeHTTPError(response, http.StatusBadRequest, "invalid", err.Error())
	case errors.Is(err, ErrForbidden):
		writeHTTPError(response, http.StatusForbidden, "forbidden", err.Error())
	case errors.Is(err, ErrNotFound):
		writeHTTPError(response, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, ErrConflict):
		writeHTTPError(response, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, ErrCapacity):
		writeHTTPError(response, http.StatusTooManyRequests, "capacity", err.Error())
	case errors.Is(err, ErrUnavailable):
		writeHTTPError(response, http.StatusServiceUnavailable, "unavailable", err.Error())
	default:
		writeHTTPError(response, http.StatusInternalServerError, "internal", "internal error")
	}
}

func writeHTTPError(response http.ResponseWriter, status int, code, message string) {
	writeJSON(response, status, errorResponse{Error: message, Code: code})
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	httpjson.Write(response, status, value)
}

func (server *HTTPHandler) confirmTaskCompletion(response http.ResponseWriter, request *http.Request) {
	var input CompletionConfirmationInput
	if err := server.decode(response, request, &input); err != nil {
		writeHTTPError(response, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	result, err := server.client.ConfirmTaskCompletion(request.Context(), input)
	if err != nil {
		writeTaskError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, result)
}
