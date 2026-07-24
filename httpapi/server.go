// Package httpapi exposes a small JSON/HTTP adapter for the Rin runtime.
package httpapi

import (
	"bytes"
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
	"github.com/sunrioa/rin/internal/jsonwire"
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
	server.registerContractRoutes(mux)
	server.handler = server.secure(server.authenticate(mux))
	return server
}

func (s *Server) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	s.handler.ServeHTTP(response, request)
}

func (s *Server) health(response http.ResponseWriter, request *http.Request) {
	s.write(response, contractSuccessStatus(request), protocol.APIResponse{
		OK: true,
		Data: map[string]any{
			"status": "ok", "protocol_version": protocol.Version,
			"release_version": protocol.ContractReleaseVersion,
			"release_status":  protocol.ContractReleaseStatus,
			"policy_mode":     s.policyMode, "async_jobs": s.jobs != nil,
			"structured_generation": s.generation != nil,
			"features":              protocol.SupportedFeatures(),
		},
	})
}

func (s *Server) createSession(response http.ResponseWriter, request *http.Request) {
	var input protocol.CreateSessionRequest
	if !s.decode(response, request, &input) {
		return
	}
	result, err := s.engine.CreateSession(input)
	s.respond(response, request, result, err)
}

func (s *Server) observe(response http.ResponseWriter, request *http.Request) {
	var input protocol.ObserveRequest
	if !s.decode(response, request, &input) {
		return
	}
	result, err := s.engine.Observe(input)
	s.respond(response, request, result, err)
}

func (s *Server) propose(response http.ResponseWriter, request *http.Request) {
	var input protocol.ProposeRequest
	if !s.decode(response, request, &input) {
		return
	}
	proposal, duplicate, err := s.engine.Propose(request.Context(), input)
	s.respond(response, request, protocol.ProposalResult{Proposal: proposal, Duplicate: duplicate}, err)
}

func (s *Server) commit(response http.ResponseWriter, request *http.Request) {
	var input protocol.CommitRequest
	if !s.decode(response, request, &input) {
		return
	}
	result, err := s.engine.Commit(input)
	s.respond(response, request, result, err)
}

func (s *Server) commitBatch(response http.ResponseWriter, request *http.Request) {
	var input protocol.BatchCommitRequest
	if !s.decode(response, request, &input) {
		return
	}
	result, err := s.engine.CommitBatch(input)
	s.respond(response, request, result, err)
}

func (s *Server) setActorActivity(response http.ResponseWriter, request *http.Request) {
	var input protocol.SetActorActivityRequest
	if !s.decode(response, request, &input) {
		return
	}
	result, err := s.engine.SetActorActivity(input)
	s.respond(response, request, result, err)
}

func (s *Server) arbitrate(response http.ResponseWriter, request *http.Request) {
	var input protocol.ArbitrateRequest
	if !s.decode(response, request, &input) {
		return
	}
	record, duplicate, err := s.engine.Arbitrate(input)
	s.respond(response, request, protocol.ArbitrationResult{Record: record, Duplicate: duplicate}, err)
}

func (s *Server) getSession(response http.ResponseWriter, request *http.Request) {
	var input protocol.SessionRequest
	if !s.decode(response, request, &input) {
		return
	}
	result, err := s.engine.State(input)
	s.respond(response, request, result, err)
}

func (s *Server) snapshot(response http.ResponseWriter, request *http.Request) {
	var input protocol.SessionRequest
	if !s.decode(response, request, &input) {
		return
	}
	result, err := s.engine.Snapshot(input)
	s.respond(response, request, result, err)
}

func (s *Server) restore(response http.ResponseWriter, request *http.Request) {
	var input protocol.RestoreRequest
	if !s.decode(response, request, &input) {
		return
	}
	result, err := s.engine.Restore(input)
	s.respond(response, request, result, err)
}

func (s *Server) timeline(response http.ResponseWriter, request *http.Request) {
	var input protocol.TimelineRequest
	if !s.decode(response, request, &input) {
		return
	}
	result, err := s.engine.Timeline(input)
	s.respond(response, request, result, err)
}

func (s *Server) replay(response http.ResponseWriter, request *http.Request) {
	var input protocol.ReplayRequest
	if !s.decode(response, request, &input) {
		return
	}
	result, err := s.engine.Replay(input)
	s.respond(response, request, result, err)
}

func (s *Server) dueAgents(response http.ResponseWriter, request *http.Request) {
	var input protocol.DueAgentsRequest
	if !s.decode(response, request, &input) {
		return
	}
	result, err := s.engine.DueAgents(input)
	s.respond(response, request, result, err)
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
		s.respond(response, request, nil, err)
		return
	}
	s.write(response, contractSuccessStatus(request), protocol.APIResponse{OK: true, Data: result})
}

func (s *Server) getProposalJob(response http.ResponseWriter, request *http.Request) {
	jobID, ok := s.pathIdentifier(response, request.PathValue("job_id"))
	if !ok {
		return
	}
	if s.jobs == nil {
		s.writeError(response, http.StatusServiceUnavailable, "jobs_unavailable", "asynchronous proposal jobs are unavailable", "")
		return
	}
	result, err := s.jobs.Get(jobID)
	s.respond(response, request, result, err)
}

func (s *Server) cancelProposalJob(response http.ResponseWriter, request *http.Request) {
	jobID, ok := s.pathIdentifier(response, request.PathValue("job_id"))
	if !ok {
		return
	}
	if s.jobs == nil {
		s.writeError(response, http.StatusServiceUnavailable, "jobs_unavailable", "asynchronous proposal jobs are unavailable", "")
		return
	}
	result, err := s.jobs.Cancel(jobID)
	s.respond(response, request, result, err)
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
		s.respond(response, request, nil, err)
		return
	}
	s.write(response, contractSuccessStatus(request), protocol.APIResponse{OK: true, Data: result})
}

func (s *Server) getGenerationJob(response http.ResponseWriter, request *http.Request) {
	jobID, ok := s.pathIdentifier(response, request.PathValue("job_id"))
	if !ok {
		return
	}
	if s.generation == nil {
		s.writeError(response, http.StatusServiceUnavailable, "generation_unavailable", "structured generation is unavailable", "")
		return
	}
	result, err := s.generation.Get(jobID)
	s.respond(response, request, result, err)
}

func (s *Server) cancelGenerationJob(response http.ResponseWriter, request *http.Request) {
	jobID, ok := s.pathIdentifier(response, request.PathValue("job_id"))
	if !ok {
		return
	}
	if s.generation == nil {
		s.writeError(response, http.StatusServiceUnavailable, "generation_unavailable", "structured generation is unavailable", "")
		return
	}
	result, err := s.generation.Cancel(jobID)
	s.respond(response, request, result, err)
}

func (s *Server) decode(response http.ResponseWriter, request *http.Request, target any) bool {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		s.writeError(response, http.StatusUnsupportedMediaType, "invalid_content_type", "Content-Type must be application/json", "")
		return false
	}
	request.Body = http.MaxBytesReader(response, request.Body, s.maxBodyBytes)
	payload, err := io.ReadAll(request.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.writeError(response, http.StatusRequestEntityTooLarge, "body_too_large", "request body exceeds the configured limit", "")
			return false
		}
		s.writeError(response, http.StatusBadRequest, "invalid_json", "request body could not be read", "")
		return false
	}
	if !jsonwire.Valid(payload) {
		s.writeError(response, http.StatusBadRequest, "invalid_json", "request body must be valid UTF-8 JSON", "")
		return false
	}
	shapeErr, contractErr := validateContractShape(payload, target)
	if contractErr != nil {
		s.logger.Error("request contract validation failed", "error", contractErr)
		s.writeError(response, http.StatusInternalServerError, "internal_error", "request contract validation is unavailable", "")
		return false
	}
	if shapeErr != nil {
		s.writeError(
			response,
			http.StatusBadRequest,
			shapeErr.code,
			shapeErr.message,
			shapeErr.field,
		)
		return false
	}
	if sizeErr := validateInlineSnapshotWireSize(payload, target); sizeErr != nil {
		s.writeError(
			response,
			http.StatusRequestEntityTooLarge,
			sizeErr.code,
			sizeErr.message,
			sizeErr.field,
		)
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(target); err != nil {
		s.writeError(response, http.StatusBadRequest, "invalid_json", "request body must be one valid JSON object matching the request schema", "")
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		s.writeError(response, http.StatusBadRequest, "invalid_json", "request body must contain exactly one JSON object", "")
		return false
	}
	return true
}

func (s *Server) pathIdentifier(response http.ResponseWriter, value string) (string, bool) {
	if err := protocol.ValidateIdentifier("job_id", value); err != nil {
		var validation *protocol.ValidationError
		if errors.As(err, &validation) {
			s.writeError(response, http.StatusBadRequest, "invalid_request", validation.Message, validation.Field)
		} else {
			s.writeError(response, http.StatusBadRequest, "invalid_request", "job_id is invalid", "job_id")
		}
		return "", false
	}
	return value, true
}

func (s *Server) respond(response http.ResponseWriter, request *http.Request, data any, err error) {
	if err == nil {
		s.write(response, contractSuccessStatus(request), protocol.APIResponse{OK: true, Data: data})
		return
	}
	code := rinruntime.ErrorCode(err)
	status := http.StatusInternalServerError
	switch {
	case code == "store_load_failed", code == "replay_failed":
		// Durable recovery failures must never inherit a lower-level sentinel's
		// client-facing status. In particular, ErrNotFound beneath
		// store_load_failed describes a missing/corrupt durable resource, not a
		// confirmed absent Session.
		status = http.StatusInternalServerError
	case code == "snapshot_too_large":
		status = http.StatusRequestEntityTooLarge
	case protocol.IsValidationError(err),
		code == "invalid_request",
		code == "invalid_snapshot":
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
	if code == "internal_error" {
		s.logger.Error("request failed", "error", err)
	}
	s.writeError(response, status, code, err.Error(), rinruntime.ErrorField(err))
}

func (s *Server) writeError(response http.ResponseWriter, status int, code, message, field string) {
	s.write(response, status, protocol.APIResponse{
		OK:    false,
		Error: protocol.NewErrorDetail(code, message, field),
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
