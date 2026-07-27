// Package httpapi exposes a small JSON/HTTP adapter for the Rin runtime.
package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sunrioa/rin/generation"
	"github.com/sunrioa/rin/internal/jsonwire"
	"github.com/sunrioa/rin/jobs"
	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
)

const (
	DefaultMaxBodyBytes              int64 = 32 << 20
	DefaultRequestTimeout                  = 30 * time.Second
	DefaultTransferTimeout                 = 30 * time.Minute
	DefaultTransferInactivityTimeout       = 30 * time.Second
)

type Options struct {
	Token                     string
	MaxBodyBytes              int64
	RequestTimeout            time.Duration
	TransferTimeout           time.Duration
	TransferInactivityTimeout time.Duration
	Logger                    *slog.Logger
	Jobs                      *jobs.Manager
	Generation                *generation.Manager
	PolicyMode                string
}

type Server struct {
	engine                    *rinruntime.Engine
	token                     string
	maxBodyBytes              int64
	maxTransferBytes          uint64
	requestTimeout            time.Duration
	transferTimeout           time.Duration
	transferInactivityTimeout time.Duration
	logger                    *slog.Logger
	jobs                      *jobs.Manager
	generation                *generation.Manager
	policyMode                string
	handler                   http.Handler
	requests                  requestMetrics
}

type requestMetrics struct {
	total          atomic.Uint64
	inFlight       atomic.Int64
	responses4xx   atomic.Uint64
	responses5xx   atomic.Uint64
	durationMillis atomic.Uint64
	fallbackIDs    atomic.Uint64
}

type requestDiagnostics struct {
	Total                     uint64 `json:"total"`
	InFlight                  uint64 `json:"in_flight"`
	Responses4xx              uint64 `json:"responses_4xx"`
	Responses5xx              uint64 `json:"responses_5xx"`
	DurationMillisecondsTotal uint64 `json:"duration_milliseconds_total"`
}

type diagnosticsData struct {
	Status       string                  `json:"status"`
	Runtime      rinruntime.Diagnostics  `json:"runtime"`
	ProposalJobs jobs.Diagnostics        `json:"proposal_jobs"`
	Generation   *generation.Diagnostics `json:"generation,omitempty"`
	Requests     requestDiagnostics      `json:"requests"`
}

func New(engine *rinruntime.Engine, options Options) *Server {
	maximum := options.MaxBodyBytes
	if maximum <= 0 {
		maximum = DefaultMaxBodyBytes
	}
	requestTimeout := options.RequestTimeout
	if requestTimeout <= 0 {
		requestTimeout = DefaultRequestTimeout
	}
	transferTimeout := options.TransferTimeout
	if transferTimeout <= 0 {
		transferTimeout = DefaultTransferTimeout
	}
	transferInactivityTimeout := options.TransferInactivityTimeout
	if transferInactivityTimeout <= 0 {
		transferInactivityTimeout = DefaultTransferInactivityTimeout
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
		engine: engine, token: options.Token, maxBodyBytes: maximum,
		requestTimeout: requestTimeout, transferTimeout: transferTimeout,
		transferInactivityTimeout: transferInactivityTimeout,
		logger:                    logger, jobs: options.Jobs, generation: options.Generation,
		policyMode: policyMode,
	}
	if engine != nil {
		server.maxTransferBytes = engine.TransferLimits().MaxBytes
	}
	mux := http.NewServeMux()
	server.registerContractRoutes(mux)
	server.handler = server.secure(
		server.withDeadlines(server.instrument(server.authenticate(mux))),
	)
	return server
}

// NewProductionServer applies connection-level limits that do not conflict
// with Server's route-aware request and Transfer deadlines.
func NewProductionServer(address string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 * 1024,
	}
}

func (s *Server) withDeadlines(next http.Handler) http.Handler {
	return http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		transfer := isTransferPath(request.URL.Path)
		timeout := s.requestTimeout
		if transfer {
			timeout = s.transferTimeout
		}
		ctx, cancel := context.WithTimeout(request.Context(), timeout)
		defer cancel()

		controller := http.NewResponseController(response)
		deadline, hasDeadline := ctx.Deadline()
		if transfer {
			response = &activityDeadlineResponseWriter{
				ResponseWriter: response,
				controller:     controller,
				inactivity:     s.transferInactivityTimeout,
				overall:        deadline,
			}
			if request.Body != nil {
				request.Body = &activityDeadlineBody{
					ReadCloser: request.Body,
					controller: controller,
					inactivity: s.transferInactivityTimeout,
					overall:    deadline,
				}
			}
			_ = controller.SetReadDeadline(
				activityDeadline(s.transferInactivityTimeout, deadline),
			)
			_ = controller.SetWriteDeadline(
				activityDeadline(s.transferInactivityTimeout, deadline),
			)
		} else if hasDeadline {
			_ = controller.SetReadDeadline(deadline)
			_ = controller.SetWriteDeadline(deadline)
		}
		next.ServeHTTP(response, request.WithContext(ctx))
	})
}

type activityDeadlineBody struct {
	io.ReadCloser
	controller *http.ResponseController
	inactivity time.Duration
	overall    time.Time
}

func (b *activityDeadlineBody) Read(payload []byte) (int, error) {
	_ = b.controller.SetReadDeadline(
		activityDeadline(b.inactivity, b.overall),
	)
	return b.ReadCloser.Read(payload)
}

type activityDeadlineResponseWriter struct {
	http.ResponseWriter
	controller *http.ResponseController
	inactivity time.Duration
	overall    time.Time
}

func (w *activityDeadlineResponseWriter) WriteHeader(status int) {
	_ = w.controller.SetWriteDeadline(
		activityDeadline(w.inactivity, w.overall),
	)
	w.ResponseWriter.WriteHeader(status)
}

func (w *activityDeadlineResponseWriter) Write(payload []byte) (int, error) {
	_ = w.controller.SetWriteDeadline(
		activityDeadline(w.inactivity, w.overall),
	)
	return w.ResponseWriter.Write(payload)
}

func (w *activityDeadlineResponseWriter) Flush() {
	_ = w.controller.SetWriteDeadline(
		activityDeadline(w.inactivity, w.overall),
	)
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *activityDeadlineResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func activityDeadline(inactivity time.Duration, overall time.Time) time.Time {
	deadline := time.Now().Add(inactivity)
	if !overall.IsZero() && overall.Before(deadline) {
		return overall
	}
	return deadline
}

func isTransferPath(path string) bool {
	return path == "/v2/session/export" || path == "/v2/session/import"
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
			"recommended_features":  protocol.RecommendedFeatures(),
		},
	})
}

func (s *Server) ready(response http.ResponseWriter, request *http.Request) {
	if err := s.readinessError(); err != nil {
		s.writeError(
			response,
			http.StatusServiceUnavailable,
			"not_ready",
			"the Sidecar is not ready",
			"",
		)
		return
	}
	s.write(response, contractSuccessStatus(request), protocol.APIResponse{
		OK: true,
		Data: map[string]string{
			"status": "ready",
		},
	})
}

func (s *Server) diagnostics(
	response http.ResponseWriter,
	request *http.Request,
) {
	if err := s.engine.Ready(); err != nil {
		s.writeError(
			response,
			http.StatusInternalServerError,
			"diagnostics_unavailable",
			"Store diagnostics are unavailable",
			"",
		)
		return
	}
	data := diagnosticsData{
		Status:   "ready",
		Runtime:  s.engine.Diagnostics(),
		Requests: s.requestDiagnostics(),
	}
	if s.jobs != nil {
		data.ProposalJobs = s.jobs.Diagnostics()
	}
	if s.generation != nil {
		generationDiagnostics := s.generation.Diagnostics()
		data.Generation = &generationDiagnostics
	}
	if err := s.readinessError(); err != nil {
		data.Status = "degraded"
	}
	s.write(
		response,
		contractSuccessStatus(request),
		protocol.APIResponse{OK: true, Data: data},
	)
}

func (s *Server) metrics(response http.ResponseWriter, _ *http.Request) {
	runtimeDiagnostics := s.engine.Diagnostics()
	jobDiagnostics := jobs.Diagnostics{}
	if s.jobs != nil {
		jobDiagnostics = s.jobs.Diagnostics()
	}
	requestDiagnostics := s.requestDiagnostics()
	response.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	response.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(
		response,
		"# TYPE rin_http_requests_total counter\n"+
			"rin_http_requests_total %d\n"+
			"# TYPE rin_http_requests_in_flight gauge\n"+
			"rin_http_requests_in_flight %d\n"+
			"# TYPE rin_http_responses_4xx_total counter\n"+
			"rin_http_responses_4xx_total %d\n"+
			"# TYPE rin_http_responses_5xx_total counter\n"+
			"rin_http_responses_5xx_total %d\n"+
			"# TYPE rin_http_request_duration_milliseconds_total counter\n"+
			"rin_http_request_duration_milliseconds_total %d\n"+
			"# TYPE rin_sessions_known gauge\n"+
			"rin_sessions_known %d\n"+
			"# TYPE rin_sessions_unreadable_known gauge\n"+
			"rin_sessions_unreadable_known %d\n"+
			"# TYPE rin_uncertainty_barriers gauge\n"+
			"rin_uncertainty_barriers %d\n"+
			"# TYPE rin_checkpoint_failures_total counter\n"+
			"rin_checkpoint_failures_total %d\n"+
			"# TYPE rin_checkpoint_quota_skips_total counter\n"+
			"rin_checkpoint_quota_skips_total %d\n"+
			"# TYPE rin_proposal_queue_depth gauge\n"+
			"rin_proposal_queue_depth %d\n"+
			"# TYPE rin_proposal_queue_capacity gauge\n"+
			"rin_proposal_queue_capacity %d\n"+
			"# TYPE rin_proposal_jobs_retained gauge\n"+
			"rin_proposal_jobs_retained %d\n",
		requestDiagnostics.Total,
		requestDiagnostics.InFlight,
		requestDiagnostics.Responses4xx,
		requestDiagnostics.Responses5xx,
		requestDiagnostics.DurationMillisecondsTotal,
		runtimeDiagnostics.KnownSessions,
		runtimeDiagnostics.KnownCorruptSessions,
		runtimeDiagnostics.PendingUncertaintyBarriers,
		runtimeDiagnostics.CheckpointFailures,
		runtimeDiagnostics.CheckpointQuotaSkips,
		jobDiagnostics.QueueDepth,
		jobDiagnostics.QueueCapacity,
		jobDiagnostics.Retained,
	)
	if s.generation != nil {
		generationDiagnostics := s.generation.Diagnostics()
		providerOpen := 0
		if generationDiagnostics.Provider.State != "closed" {
			providerOpen = 1
		}
		_, _ = fmt.Fprintf(
			response,
			"# TYPE rin_generation_queue_depth gauge\n"+
				"rin_generation_queue_depth %d\n"+
				"# TYPE rin_generation_queue_capacity gauge\n"+
				"rin_generation_queue_capacity %d\n"+
				"# TYPE rin_generation_jobs_retained gauge\n"+
				"rin_generation_jobs_retained %d\n"+
				"# TYPE rin_generation_retained_bytes gauge\n"+
				"rin_generation_retained_bytes %d\n"+
				"# TYPE rin_generation_max_retained_bytes gauge\n"+
				"rin_generation_max_retained_bytes %d\n"+
				"# TYPE rin_provider_circuit_not_closed gauge\n"+
				"rin_provider_circuit_not_closed %d\n",
			generationDiagnostics.QueueDepth,
			generationDiagnostics.QueueCapacity,
			generationDiagnostics.Retained,
			generationDiagnostics.RetainedBytes,
			generationDiagnostics.MaxRetainedBytes,
			providerOpen,
		)
	}
}

func (s *Server) readinessError() error {
	if err := s.engine.Ready(); err != nil {
		return err
	}
	if s.jobs != nil && s.jobs.Diagnostics().Closed {
		return jobs.ErrClosed
	}
	if s.generation != nil && s.generation.Diagnostics().Closed {
		return generation.ErrClosed
	}
	return nil
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

func (s *Server) reportAction(response http.ResponseWriter, request *http.Request) {
	var input protocol.ReportActionRequest
	if !s.decode(response, request, &input) {
		return
	}
	result, err := s.engine.ReportAction(input)
	s.respond(response, request, result, err)
}

func (s *Server) reportActionBatch(response http.ResponseWriter, request *http.Request) {
	var input protocol.BatchActionReportRequest
	if !s.decode(response, request, &input) {
		return
	}
	result, err := s.engine.ReportActionBatch(input)
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

func (s *Server) sessionStats(response http.ResponseWriter, request *http.Request) {
	var input protocol.SessionRequest
	if !s.decode(response, request, &input) {
		return
	}
	result, err := s.engine.SessionStats(input)
	s.respond(response, request, result, err)
}

func (s *Server) archiveSession(response http.ResponseWriter, request *http.Request) {
	var input protocol.ArchiveSessionRequest
	if !s.decode(response, request, &input) {
		return
	}
	result, err := s.engine.ArchiveSession(input)
	s.respond(response, request, result, err)
}

func (s *Server) deleteSession(response http.ResponseWriter, request *http.Request) {
	var input protocol.DeleteSessionRequest
	if !s.decode(response, request, &input) {
		return
	}
	result, err := s.engine.DeleteSession(input)
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
	result, err := s.jobs.Cancel(request.Context(), jobID)
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
	case code == "snapshot_too_large", code == "state_too_large":
		status = http.StatusRequestEntityTooLarge
	case code == "transfer_too_large", code == "transfer_event_limit":
		status = http.StatusRequestEntityTooLarge
	case code == "transfer_capacity":
		status = http.StatusTooManyRequests
	case code == "session_quota_exceeded":
		status = http.StatusInsufficientStorage
	case protocol.IsValidationError(err),
		code == "invalid_request",
		code == "invalid_snapshot",
		code == "transfer_replay_failed":
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
	case errors.Is(err, generation.ErrMemoryLimit):
		status = http.StatusTooManyRequests
	case errors.Is(err, generation.ErrClosed):
		status = http.StatusServiceUnavailable
	case errors.Is(err, rinruntime.ErrClosed):
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
		if request.URL.Path == "/health" ||
			request.URL.Path == "/ready" ||
			s.token == "" {
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

type observedResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *observedResponseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *observedResponseWriter) Write(payload []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(payload)
}

func (w *observedResponseWriter) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *observedResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (s *Server) instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		started := time.Now()
		requestID := request.Header.Get("Rin-Request-ID")
		if !validRequestCorrelationID(requestID) {
			requestID = s.newRequestCorrelationID()
		}
		response.Header().Set("Rin-Request-ID", requestID)
		s.requests.total.Add(1)
		s.requests.inFlight.Add(1)
		observed := &observedResponseWriter{ResponseWriter: response}
		next.ServeHTTP(observed, request)
		s.requests.inFlight.Add(-1)
		status := observed.status
		if status == 0 {
			status = http.StatusOK
		}
		switch {
		case status >= 500:
			s.requests.responses5xx.Add(1)
		case status >= 400:
			s.requests.responses4xx.Add(1)
		}
		duration := time.Since(started)
		s.requests.durationMillis.Add(uint64(duration.Milliseconds()))
		s.logger.Info(
			"http request",
			"request_id", requestID,
			"method", request.Method,
			"route", request.Pattern,
			"status", status,
			"duration_ms", duration.Milliseconds(),
		)
	})
}

func validRequestCorrelationID(value string) bool {
	if value == "" || len(value) > 96 {
		return false
	}
	for _, character := range value {
		switch {
		case character >= 'a' && character <= 'z',
			character >= 'A' && character <= 'Z',
			character >= '0' && character <= '9',
			character == '.',
			character == '_',
			character == '-':
		default:
			return false
		}
	}
	return true
}

func (s *Server) newRequestCorrelationID() string {
	var random [16]byte
	if _, err := rand.Read(random[:]); err == nil {
		return "req." + hex.EncodeToString(random[:])
	}
	return fmt.Sprintf("req.fallback.%d", s.requests.fallbackIDs.Add(1))
}

func (s *Server) requestDiagnostics() requestDiagnostics {
	inFlight := s.requests.inFlight.Load()
	if inFlight < 0 {
		inFlight = 0
	}
	return requestDiagnostics{
		Total:                     boundedMetric(s.requests.total.Load()),
		InFlight:                  boundedMetric(uint64(inFlight)),
		Responses4xx:              boundedMetric(s.requests.responses4xx.Load()),
		Responses5xx:              boundedMetric(s.requests.responses5xx.Load()),
		DurationMillisecondsTotal: boundedMetric(s.requests.durationMillis.Load()),
	}
}

func boundedMetric(value uint64) uint64 {
	if value > uint64(protocol.MaxJSONSafeInteger) {
		return uint64(protocol.MaxJSONSafeInteger)
	}
	return value
}

func (s *Server) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		response.Header().Set("X-Content-Type-Options", "nosniff")
		response.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(response, request)
	})
}
