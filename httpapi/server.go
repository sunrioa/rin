// Package httpapi exposes a small JSON/HTTP adapter for the Rin runtime.
package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"

	"github.com/sunrioa/rin/generation"
	"github.com/sunrioa/rin/jobs"
	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
)

const DefaultMaxBodyBytes int64 = 32 << 20

type Options struct {
	Token        string
	MaxBodyBytes int64
	Logger       *slog.Logger
	Jobs         *jobs.Manager
	Generation   *generation.Manager
	PolicyMode   string
}

type Server struct {
	engine       *rinruntime.Engine
	token        string
	maxBodyBytes int64
	logger       *slog.Logger
	jobs         *jobs.Manager
	generation   *generation.Manager
	policyMode   string
	handler      http.Handler
}

func New(engine *rinruntime.Engine, options Options) *Server {
	maximum := options.MaxBodyBytes
	if maximum <= 0 {
		maximum = DefaultMaxBodyBytes
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	policyMode := options.PolicyMode
	if policyMode == "" {
		policyMode = "deterministic"
	}
	server := &Server{
		engine: engine, token: options.Token, maxBodyBytes: maximum, logger: logger,
		jobs: options.Jobs, generation: options.Generation, policyMode: policyMode,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", server.health)
	mux.HandleFunc("POST /v1/session/create", server.createSession)
	mux.HandleFunc("POST /v1/session/observe", server.observe)
	mux.HandleFunc("POST /v1/agent/propose", server.propose)
	mux.HandleFunc("POST /v1/action/commit", server.commit)
	mux.HandleFunc("POST /v1/session/get", server.getSession)
	mux.HandleFunc("POST /v1/session/snapshot", server.snapshot)
	mux.HandleFunc("POST /v1/session/restore", server.restore)
	mux.HandleFunc("POST /v1/scheduler/due", server.dueAgents)
	mux.HandleFunc("POST /v1/jobs/propose", server.submitProposalJob)
	mux.HandleFunc("GET /v1/jobs/{job_id}", server.getProposalJob)
	mux.HandleFunc("DELETE /v1/jobs/{job_id}", server.cancelProposalJob)
	mux.HandleFunc("POST /v1/generation/jobs", server.submitGenerationJob)
	mux.HandleFunc("GET /v1/generation/jobs/{job_id}", server.getGenerationJob)
	mux.HandleFunc("DELETE /v1/generation/jobs/{job_id}", server.cancelGenerationJob)
	server.handler = server.secure(server.authenticate(mux))
	return server
}

func (s *Server) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	s.handler.ServeHTTP(response, request)
}

func (s *Server) health(response http.ResponseWriter, _ *http.Request) {
	s.write(response, http.StatusOK, protocol.APIResponse{
		OK: true,
		Data: map[string]any{
			"status": "ok", "protocol_version": protocol.Version,
			"policy_mode": s.policyMode, "async_jobs": s.jobs != nil,
			"structured_generation": s.generation != nil,
		},
	})
}

func (s *Server) createSession(response http.ResponseWriter, request *http.Request) {
	var input protocol.CreateSessionRequest
	if !s.decode(response, request, &input) {
		return
	}
	result, err := s.engine.CreateSession(input)
	s.respond(response, result, err)
}

func (s *Server) observe(response http.ResponseWriter, request *http.Request) {
	var input protocol.ObserveRequest
	if !s.decode(response, request, &input) {
		return
	}
	result, err := s.engine.Observe(input)
	s.respond(response, result, err)
}

func (s *Server) propose(response http.ResponseWriter, request *http.Request) {
	var input protocol.ProposeRequest
	if !s.decode(response, request, &input) {
		return
	}
	proposal, duplicate, err := s.engine.Propose(request.Context(), input)
	s.respond(response, protocol.ProposalResult{Proposal: proposal, Duplicate: duplicate}, err)
}

func (s *Server) commit(response http.ResponseWriter, request *http.Request) {
	var input protocol.CommitRequest
	if !s.decode(response, request, &input) {
		return
	}
	result, err := s.engine.Commit(input)
	s.respond(response, result, err)
}

func (s *Server) getSession(response http.ResponseWriter, request *http.Request) {
	var input protocol.SessionRequest
	if !s.decode(response, request, &input) {
		return
	}
	result, err := s.engine.State(input)
	s.respond(response, result, err)
}

func (s *Server) snapshot(response http.ResponseWriter, request *http.Request) {
	var input protocol.SessionRequest
	if !s.decode(response, request, &input) {
		return
	}
	result, err := s.engine.Snapshot(input)
	s.respond(response, result, err)
}

func (s *Server) restore(response http.ResponseWriter, request *http.Request) {
	var input protocol.RestoreRequest
	if !s.decode(response, request, &input) {
		return
	}
	result, err := s.engine.Restore(input)
	s.respond(response, result, err)
}

func (s *Server) dueAgents(response http.ResponseWriter, request *http.Request) {
	var input protocol.DueAgentsRequest
	if !s.decode(response, request, &input) {
		return
	}
	result, err := s.engine.DueAgents(input)
	s.respond(response, result, err)
}

func (s *Server) submitProposalJob(response http.ResponseWriter, request *http.Request) {
	if s.jobs == nil {
		s.writeError(response, http.StatusServiceUnavailable, "jobs_unavailable", "asynchronous proposal jobs are unavailable", "")
		return
	}
	var input protocol.ProposeRequest
	if !s.decode(response, request, &input) {
		return
	}
	result, err := s.jobs.Submit(input)
	if err != nil {
		s.respond(response, nil, err)
		return
	}
	s.write(response, http.StatusAccepted, protocol.APIResponse{OK: true, Data: result})
}

func (s *Server) getProposalJob(response http.ResponseWriter, request *http.Request) {
	if s.jobs == nil {
		s.writeError(response, http.StatusServiceUnavailable, "jobs_unavailable", "asynchronous proposal jobs are unavailable", "")
		return
	}
	result, err := s.jobs.Get(request.PathValue("job_id"))
	s.respond(response, result, err)
}

func (s *Server) cancelProposalJob(response http.ResponseWriter, request *http.Request) {
	if s.jobs == nil {
		s.writeError(response, http.StatusServiceUnavailable, "jobs_unavailable", "asynchronous proposal jobs are unavailable", "")
		return
	}
	result, err := s.jobs.Cancel(request.PathValue("job_id"))
	s.respond(response, result, err)
}

func (s *Server) submitGenerationJob(response http.ResponseWriter, request *http.Request) {
	if s.generation == nil {
		s.writeError(response, http.StatusServiceUnavailable, "generation_unavailable", "structured generation is unavailable", "")
		return
	}
	var input protocol.GenerationRequest
	if !s.decode(response, request, &input) {
		return
	}
	result, err := s.generation.Submit(input)
	if err != nil {
		s.respond(response, nil, err)
		return
	}
	s.write(response, http.StatusAccepted, protocol.APIResponse{OK: true, Data: result})
}

func (s *Server) getGenerationJob(response http.ResponseWriter, request *http.Request) {
	if s.generation == nil {
		s.writeError(response, http.StatusServiceUnavailable, "generation_unavailable", "structured generation is unavailable", "")
		return
	}
	result, err := s.generation.Get(request.PathValue("job_id"))
	s.respond(response, result, err)
}

func (s *Server) cancelGenerationJob(response http.ResponseWriter, request *http.Request) {
	if s.generation == nil {
		s.writeError(response, http.StatusServiceUnavailable, "generation_unavailable", "structured generation is unavailable", "")
		return
	}
	result, err := s.generation.Cancel(request.PathValue("job_id"))
	s.respond(response, result, err)
}

func (s *Server) decode(response http.ResponseWriter, request *http.Request, target any) bool {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		s.writeError(response, http.StatusUnsupportedMediaType, "invalid_content_type", "Content-Type must be application/json", "")
		return false
	}
	request.Body = http.MaxBytesReader(response, request.Body, s.maxBodyBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.writeError(response, http.StatusRequestEntityTooLarge, "body_too_large", "request body exceeds the configured limit", "")
			return false
		}
		s.writeError(response, http.StatusBadRequest, "invalid_json", "request body must be one valid JSON object with known fields", "")
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		s.writeError(response, http.StatusBadRequest, "invalid_json", "request body must contain exactly one JSON object", "")
		return false
	}
	return true
}

func (s *Server) respond(response http.ResponseWriter, data any, err error) {
	if err == nil {
		s.write(response, http.StatusOK, protocol.APIResponse{OK: true, Data: data})
		return
	}
	status := http.StatusInternalServerError
	switch {
	case protocol.IsValidationError(err), rinruntime.ErrorCode(err) == "invalid_request":
		status = http.StatusBadRequest
	case errors.Is(err, rinruntime.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, rinruntime.ErrNoSafeAction):
		status = http.StatusUnprocessableEntity
	case errors.Is(err, rinruntime.ErrConflict), errors.Is(err, rinruntime.ErrStale), errors.Is(err, rinruntime.ErrNotDue):
		status = http.StatusConflict
	case errors.Is(err, jobs.ErrQueueFull):
		status = http.StatusTooManyRequests
	case errors.Is(err, jobs.ErrClosed):
		status = http.StatusServiceUnavailable
	case errors.Is(err, generation.ErrQueueFull):
		status = http.StatusTooManyRequests
	case errors.Is(err, generation.ErrClosed):
		status = http.StatusServiceUnavailable
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		status = http.StatusRequestTimeout
	}
	code := rinruntime.ErrorCode(err)
	if code == "internal_error" {
		s.logger.Error("request failed", "error", err)
	}
	s.writeError(response, status, code, err.Error(), rinruntime.ErrorField(err))
}

func (s *Server) writeError(response http.ResponseWriter, status int, code, message, field string) {
	s.write(response, status, protocol.APIResponse{
		OK:    false,
		Error: &protocol.ErrorDetail{Code: code, Message: message, Field: field},
	})
}

func (s *Server) write(response http.ResponseWriter, status int, value protocol.APIResponse) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	if err := json.NewEncoder(response).Encode(value); err != nil {
		s.logger.Error("write response", "error", err)
	}
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/health" || s.token == "" {
			next.ServeHTTP(response, request)
			return
		}
		provided := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
		valid := len(provided) == len(s.token) && subtle.ConstantTimeCompare([]byte(provided), []byte(s.token)) == 1
		if !valid {
			response.Header().Set("WWW-Authenticate", "Bearer")
			s.writeError(response, http.StatusUnauthorized, "unauthorized", "a valid bearer token is required", "")
			return
		}
		next.ServeHTTP(response, request)
	})
}

func (s *Server) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		response.Header().Set("X-Content-Type-Options", "nosniff")
		response.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(response, request)
	})
}
