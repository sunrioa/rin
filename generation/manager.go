// Package generation runs bounded, provider-backed structured generation jobs.
package generation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sunrioa/rin/protocol"
	"github.com/sunrioa/rin/provider"
	rinruntime "github.com/sunrioa/rin/runtime"
)

var (
	ErrQueueFull   = errors.New("generation job queue is full")
	ErrClosed      = errors.New("generation job manager is closed")
	ErrOutputLimit = errors.New("generation output exceeds the configured limit")
)

type Config struct {
	Workers        int
	QueueSize      int
	MaxJobs        int
	JobTTL         time.Duration
	CacheEntries   int
	CacheTTL       time.Duration
	MaxOutputBytes int
}

type Manager struct {
	client provider.Client
	config Config
	ctx    context.Context
	cancel context.CancelFunc
	queue  chan string

	mu        sync.Mutex
	jobs      map[string]*jobState
	byRequest map[string]string
	cache     map[string]cacheEntry
	closed    bool
	now       func() time.Time
	wait      sync.WaitGroup
	done      chan struct{}
}

type jobState struct {
	public       protocol.GenerationJob
	request      protocol.GenerationRequest
	requestHash  string
	semanticHash string
	cancel       context.CancelFunc
	ctx          context.Context
	completedAt  time.Time
}

type cacheEntry struct {
	result    protocol.GenerationResult
	createdAt time.Time
}

var genericJSONObjectSchema = json.RawMessage(`{"type":"object","additionalProperties":true}`)

func New(client provider.Client, config Config) (*Manager, error) {
	if client == nil {
		return nil, errors.New("generation provider client is required")
	}
	if config.Workers <= 0 {
		config.Workers = 2
	}
	if config.Workers > 32 {
		return nil, errors.New("generation workers must not exceed 32")
	}
	if config.QueueSize <= 0 {
		config.QueueSize = 64
	}
	if config.QueueSize > 4096 {
		return nil, errors.New("generation queue size must not exceed 4096")
	}
	if config.MaxJobs <= 0 {
		config.MaxJobs = 512
	}
	if config.MaxJobs < config.QueueSize || config.MaxJobs > 16384 {
		return nil, errors.New("generation max jobs must be between queue size and 16384")
	}
	if config.JobTTL <= 0 {
		config.JobTTL = 30 * time.Minute
	}
	if config.CacheEntries <= 0 {
		config.CacheEntries = 256
	}
	if config.CacheEntries > 16384 {
		return nil, errors.New("generation cache entries must not exceed 16384")
	}
	if config.CacheTTL <= 0 {
		config.CacheTTL = 30 * time.Minute
	}
	if config.MaxOutputBytes <= 0 {
		config.MaxOutputBytes = 512 * 1024
	}
	if config.MaxOutputBytes < 1024 || config.MaxOutputBytes > 4*1024*1024 {
		return nil, errors.New("generation output limit must be between 1 KiB and 4 MiB")
	}

	ctx, cancel := context.WithCancel(context.Background())
	manager := &Manager{
		client: client, config: config, ctx: ctx, cancel: cancel,
		queue: make(chan string, config.QueueSize), jobs: make(map[string]*jobState),
		byRequest: make(map[string]string), cache: make(map[string]cacheEntry),
		now: time.Now, done: make(chan struct{}),
	}
	for index := 0; index < config.Workers; index++ {
		manager.wait.Add(1)
		go manager.worker()
	}
	go func() {
		manager.wait.Wait()
		close(manager.done)
	}()
	return manager, nil
}

func (m *Manager) Submit(request protocol.GenerationRequest) (protocol.GenerationJobSubmission, error) {
	if err := protocol.ValidateGeneration(request); err != nil {
		return protocol.GenerationJobSubmission{}, rinruntime.NewFieldError("invalid_request", err.Error(), validationField(err), err)
	}
	requestHash, semanticHash, err := hashRequests(request)
	if err != nil {
		return protocol.GenerationJobSubmission{}, rinruntime.NewError("job_encode_failed", "could not encode generation job", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return protocol.GenerationJobSubmission{}, rinruntime.NewError("generation_closed", "generation job manager is closed", ErrClosed)
	}
	now := m.now()
	m.cleanup(now)
	if existingID, exists := m.byRequest[request.RequestID]; exists {
		existing := m.jobs[existingID]
		if existing.requestHash != requestHash {
			return protocol.GenerationJobSubmission{}, rinruntime.NewFieldError("request_id_conflict", "request id was already used with a different generation payload", "request_id", rinruntime.ErrConflict)
		}
		return protocol.GenerationJobSubmission{
			ProtocolVersion: protocol.Version, JobID: existing.public.JobID,
			Status: existing.public.Status, Duplicate: true,
		}, nil
	}
	if len(m.jobs) >= m.config.MaxJobs {
		return protocol.GenerationJobSubmission{}, rinruntime.NewError("generation_capacity", "generation job capacity is full", ErrQueueFull)
	}

	jobID := "gen." + requestHash[:24]
	state := &jobState{
		public: protocol.GenerationJob{
			ProtocolVersion: protocol.Version, JobID: jobID, RequestID: request.RequestID,
			Kind: request.Kind, ContextHash: request.ContextHash, Status: "queued",
			SubmittedAt: now.UTC().Format(time.RFC3339Nano),
		},
		request: request, requestHash: requestHash, semanticHash: semanticHash,
	}
	if cached, ok := m.cache[semanticHash]; ok && now.Sub(cached.createdAt) < m.config.CacheTTL {
		result := cloneResult(cached.result)
		result.CacheHit = true
		state.public.Status = "succeeded"
		state.public.StartedAt = state.public.SubmittedAt
		state.public.FinishedAt = state.public.SubmittedAt
		state.public.Result = &result
		state.completedAt = now
		m.jobs[jobID] = state
		m.byRequest[request.RequestID] = jobID
		return protocol.GenerationJobSubmission{ProtocolVersion: protocol.Version, JobID: jobID, Status: "succeeded"}, nil
	}

	jobContext, cancel := context.WithCancel(m.ctx)
	state.cancel = cancel
	state.ctx = jobContext
	m.jobs[jobID] = state
	m.byRequest[request.RequestID] = jobID
	select {
	case m.queue <- jobID:
		return protocol.GenerationJobSubmission{ProtocolVersion: protocol.Version, JobID: jobID, Status: "queued"}, nil
	default:
		delete(m.jobs, jobID)
		delete(m.byRequest, request.RequestID)
		cancel()
		return protocol.GenerationJobSubmission{}, rinruntime.NewError("generation_queue_full", "generation job queue is full", ErrQueueFull)
	}
}

func (m *Manager) Get(jobID string) (protocol.GenerationJob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, exists := m.jobs[jobID]
	if !exists {
		return protocol.GenerationJob{}, rinruntime.NewFieldError("job_not_found", "generation job does not exist", "job_id", rinruntime.ErrNotFound)
	}
	return cloneJob(state.public), nil
}

func (m *Manager) Cancel(jobID string) (protocol.GenerationJob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, exists := m.jobs[jobID]
	if !exists {
		return protocol.GenerationJob{}, rinruntime.NewFieldError("job_not_found", "generation job does not exist", "job_id", rinruntime.ErrNotFound)
	}
	if terminal(state.public.Status) {
		return cloneJob(state.public), nil
	}
	state.cancel()
	now := m.now()
	state.public.Status = "canceled"
	state.public.FinishedAt = now.UTC().Format(time.RFC3339Nano)
	state.public.Error = &protocol.ErrorDetail{Code: "job_canceled", Message: "generation job was canceled"}
	state.completedAt = now
	return cloneJob(state.public), nil
}

func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	if !m.closed {
		m.closed = true
		m.cancel()
		now := m.now()
		for _, state := range m.jobs {
			if !terminal(state.public.Status) {
				state.cancel()
				state.public.Status = "canceled"
				state.public.FinishedAt = now.UTC().Format(time.RFC3339Nano)
				state.public.Error = &protocol.ErrorDetail{Code: "generation_closed", Message: "generation job manager stopped"}
				state.completedAt = now
			}
		}
	}
	m.mu.Unlock()
	select {
	case <-m.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) worker() {
	defer m.wait.Done()
	for {
		select {
		case <-m.ctx.Done():
			return
		case jobID := <-m.queue:
			m.run(jobID)
		}
	}
}

func (m *Manager) run(jobID string) {
	m.mu.Lock()
	state, exists := m.jobs[jobID]
	if !exists || state.public.Status != "queued" {
		m.mu.Unlock()
		return
	}
	state.public.Status = "running"
	state.public.StartedAt = m.now().UTC().Format(time.RFC3339Nano)
	request := state.request
	jobContext := state.ctx
	m.mu.Unlock()

	messages := make([]provider.Message, len(request.Messages))
	for index, message := range request.Messages {
		messages[index] = provider.Message{Role: message.Role, Content: message.Content}
	}
	response, err := m.client.Complete(jobContext, provider.CompletionRequest{
		Messages: messages,
		Schema: &provider.ResponseSchema{
			Name:   "rin_" + strings.ReplaceAll(request.Kind, "-", "_"),
			Strict: false,
			Schema: append(json.RawMessage(nil), genericJSONObjectSchema...),
		},
		Temperature: request.Temperature,
		MaxTokens:   request.MaxTokens,
	})
	now := m.now()

	m.mu.Lock()
	defer m.mu.Unlock()
	state, exists = m.jobs[jobID]
	if !exists {
		return
	}
	if err == nil {
		result, validationErr := m.validateResult(response)
		if validationErr != nil {
			err = validationErr
		} else if state.public.Status != "canceled" {
			state.public.Status = "succeeded"
			state.public.Result = &result
			state.public.Error = nil
			m.cache[state.semanticHash] = cacheEntry{result: cloneResult(result), createdAt: now}
			m.trimCache(now)
		}
	}
	if err != nil && state.public.Status != "canceled" {
		if errors.Is(err, context.Canceled) {
			state.public.Status = "canceled"
		} else {
			state.public.Status = "failed"
		}
		state.public.Error = jobError(err)
	}
	state.public.FinishedAt = now.UTC().Format(time.RFC3339Nano)
	state.completedAt = now
}

func (m *Manager) validateResult(response provider.CompletionResponse) (protocol.GenerationResult, error) {
	content := strings.TrimSpace(response.Content)
	if content == "" || !utf8.ValidString(content) || strings.ContainsRune(content, 0) {
		return protocol.GenerationResult{}, rinruntime.NewError("invalid_generation", "provider returned invalid generation content", nil)
	}
	if len([]byte(content)) > m.config.MaxOutputBytes {
		return protocol.GenerationResult{}, rinruntime.NewError("generation_too_large", "provider generation exceeded the output limit", ErrOutputLimit)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &object); err != nil || object == nil {
		return protocol.GenerationResult{}, rinruntime.NewError("invalid_generation_json", "provider generation was not one JSON object", err)
	}
	return protocol.GenerationResult{
		Content:      content,
		Model:        safeMetadata(response.Model, 160),
		FinishReason: safeMetadata(response.FinishReason, 96),
		PromptTokens: nonNegative(response.Usage.PromptTokens),
		OutputTokens: nonNegative(response.Usage.CompletionTokens),
		TotalTokens:  nonNegative(response.Usage.TotalTokens),
	}, nil
}

func (m *Manager) cleanup(now time.Time) {
	finished := make([]struct {
		id string
		at time.Time
	}, 0)
	for id, state := range m.jobs {
		if terminal(state.public.Status) && !state.completedAt.IsZero() {
			if now.Sub(state.completedAt) >= m.config.JobTTL {
				m.deleteJob(id, state)
				continue
			}
			finished = append(finished, struct {
				id string
				at time.Time
			}{id: id, at: state.completedAt})
		}
	}
	if len(m.jobs) >= m.config.MaxJobs {
		sort.Slice(finished, func(i, j int) bool {
			if finished[i].at.Equal(finished[j].at) {
				return finished[i].id < finished[j].id
			}
			return finished[i].at.Before(finished[j].at)
		})
		for len(m.jobs) >= m.config.MaxJobs && len(finished) > 0 {
			id := finished[0].id
			m.deleteJob(id, m.jobs[id])
			finished = finished[1:]
		}
	}
	m.trimCache(now)
}

func (m *Manager) trimCache(now time.Time) {
	type cached struct {
		hash string
		at   time.Time
	}
	entries := make([]cached, 0, len(m.cache))
	for hash, entry := range m.cache {
		if now.Sub(entry.createdAt) >= m.config.CacheTTL {
			delete(m.cache, hash)
			continue
		}
		entries = append(entries, cached{hash: hash, at: entry.createdAt})
	}
	if len(m.cache) <= m.config.CacheEntries {
		return
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].at.Equal(entries[j].at) {
			return entries[i].hash < entries[j].hash
		}
		return entries[i].at.Before(entries[j].at)
	})
	for len(m.cache) > m.config.CacheEntries && len(entries) > 0 {
		delete(m.cache, entries[0].hash)
		entries = entries[1:]
	}
}

func (m *Manager) deleteJob(id string, state *jobState) {
	delete(m.jobs, id)
	delete(m.byRequest, state.request.RequestID)
}

func hashRequests(request protocol.GenerationRequest) (string, string, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return "", "", err
	}
	requestDigest := sha256.Sum256(payload)
	semantic := request
	semantic.RequestID = "semantic"
	semanticPayload, err := json.Marshal(semantic)
	if err != nil {
		return "", "", err
	}
	semanticDigest := sha256.Sum256(semanticPayload)
	return hex.EncodeToString(requestDigest[:]), hex.EncodeToString(semanticDigest[:]), nil
}

func jobError(err error) *protocol.ErrorDetail {
	code := rinruntime.ErrorCode(err)
	if errors.Is(err, context.Canceled) {
		code = "job_canceled"
	}
	return &protocol.ErrorDetail{Code: code, Message: err.Error(), Field: rinruntime.ErrorField(err)}
}

func terminal(status string) bool {
	return status == "succeeded" || status == "failed" || status == "canceled"
}

func cloneJob(job protocol.GenerationJob) protocol.GenerationJob {
	if job.Result != nil {
		result := cloneResult(*job.Result)
		job.Result = &result
	}
	if job.Error != nil {
		detail := *job.Error
		job.Error = &detail
	}
	return job
}

func cloneResult(result protocol.GenerationResult) protocol.GenerationResult { return result }

func validationField(err error) string {
	var validation *protocol.ValidationError
	if errors.As(err, &validation) {
		return validation.Field
	}
	return ""
}

func safeMetadata(value string, maximum int) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\x00", ""))
	if len([]rune(value)) > maximum {
		value = string([]rune(value)[:maximum])
	}
	return value
}

func nonNegative(value int) int {
	if value < 0 {
		return 0
	}
	return value
}
