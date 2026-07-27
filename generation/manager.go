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

	"github.com/sunrioa/rin/internal/jsonwire"
	"github.com/sunrioa/rin/protocol"
	"github.com/sunrioa/rin/provider"
	rinruntime "github.com/sunrioa/rin/runtime"
)

var (
	ErrQueueFull   = errors.New("generation job queue is full")
	ErrClosed      = errors.New("generation job manager is closed")
	ErrOutputLimit = errors.New("generation output exceeds the configured limit")
	ErrMemoryLimit = errors.New("generation retained-memory limit reached")
)

// The terminal transition can add a timestamp plus a protocol-bounded error.
// Four KiB covers the worst-case UTF-8 encoding of those fields.
const generationJobTransitionReserve uint64 = 4 << 10

type Config struct {
	Workers          int
	QueueSize        int
	MaxJobs          int
	JobTTL           time.Duration
	CacheEntries     int
	CacheTTL         time.Duration
	MaxOutputBytes   int
	MaxRetainedBytes uint64
	CleanupInterval  time.Duration
}

type Manager struct {
	provider provider.StructuredGenerationProvider
	config   Config
	ctx      context.Context
	cancel   context.CancelFunc
	queue    chan string

	mu            sync.Mutex
	jobs          map[string]*jobState
	byRequest     map[string]string
	cache         map[string]cacheEntry
	retainedBytes uint64
	closed        bool
	now           func() time.Time
	wait          sync.WaitGroup
	done          chan struct{}
}

type jobState struct {
	public       protocol.GenerationJob
	request      protocol.GenerationRequest
	requestID    string
	requestHash  string
	semanticHash string
	requestBytes uint64
	publicBytes  uint64
	cancel       context.CancelFunc
	ctx          context.Context
	completedAt  time.Time
}

type cacheEntry struct {
	result        protocol.GenerationResult
	createdAt     time.Time
	retainedBytes uint64
}

type Diagnostics struct {
	Workers          int                         `json:"workers"`
	QueueDepth       int                         `json:"queue_depth"`
	QueueCapacity    int                         `json:"queue_capacity"`
	Retained         int                         `json:"retained"`
	MaxRetained      int                         `json:"max_retained"`
	CacheEntries     int                         `json:"cache_entries"`
	RetainedBytes    uint64                      `json:"retained_bytes"`
	MaxRetainedBytes uint64                      `json:"max_retained_bytes"`
	ByStatus         map[string]int              `json:"by_status"`
	Closed           bool                        `json:"closed"`
	Provider         provider.CircuitDiagnostics `json:"provider"`
}

func (m *Manager) Diagnostics() Diagnostics {
	m.mu.Lock()
	byStatus := make(map[string]int)
	for _, state := range m.jobs {
		byStatus[state.public.Status]++
	}
	result := Diagnostics{
		Workers:          m.config.Workers,
		QueueDepth:       len(m.queue),
		QueueCapacity:    cap(m.queue),
		Retained:         len(m.jobs),
		MaxRetained:      m.config.MaxJobs,
		CacheEntries:     len(m.cache),
		RetainedBytes:    m.retainedBytes,
		MaxRetainedBytes: m.config.MaxRetainedBytes,
		ByStatus:         byStatus,
		Closed:           m.closed,
		Provider:         provider.CircuitDiagnostics{State: "unavailable"},
	}
	m.mu.Unlock()
	if diagnostics, ok := m.provider.(interface {
		Diagnostics() provider.CircuitDiagnostics
	}); ok {
		result.Provider = diagnostics.Diagnostics()
	}
	return result
}

var genericJSONObjectSchema = json.RawMessage(`{"type":"object","additionalProperties":true}`)

func New(
	generationProvider provider.StructuredGenerationProvider,
	config Config,
) (*Manager, error) {
	if generationProvider == nil {
		return nil, errors.New("structured generation provider is required")
	}
	config, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	manager := &Manager{
		provider: generationProvider, config: config, ctx: ctx, cancel: cancel,
		queue: make(chan string, config.QueueSize), jobs: make(map[string]*jobState),
		byRequest: make(map[string]string), cache: make(map[string]cacheEntry),
		now: time.Now, done: make(chan struct{}),
	}
	for index := 0; index < config.Workers; index++ {
		manager.wait.Add(1)
		go manager.worker()
	}
	manager.wait.Add(1)
	go manager.cleanupWorker()
	go func() {
		manager.wait.Wait()
		close(manager.done)
	}()
	return manager, nil
}

// ValidateConfig applies the same limits as New without starting workers.
func ValidateConfig(config Config) error {
	_, err := normalizeConfig(config)
	return err
}

func normalizeConfig(config Config) (Config, error) {
	if config.Workers <= 0 {
		config.Workers = 2
	}
	if config.Workers > 32 {
		return Config{}, errors.New("generation workers must not exceed 32")
	}
	if config.QueueSize <= 0 {
		config.QueueSize = 64
	}
	if config.QueueSize > 4096 {
		return Config{}, errors.New("generation queue size must not exceed 4096")
	}
	if config.MaxJobs <= 0 {
		config.MaxJobs = 512
	}
	if config.MaxJobs < config.QueueSize || config.MaxJobs > 16384 {
		return Config{}, errors.New("generation max jobs must be between queue size and 16384")
	}
	if config.JobTTL <= 0 {
		config.JobTTL = 30 * time.Minute
	}
	if config.CacheEntries <= 0 {
		config.CacheEntries = 256
	}
	if config.CacheEntries > 16384 {
		return Config{}, errors.New("generation cache entries must not exceed 16384")
	}
	if config.CacheTTL <= 0 {
		config.CacheTTL = 30 * time.Minute
	}
	if config.MaxOutputBytes <= 0 {
		config.MaxOutputBytes = 512 * 1024
	}
	if config.MaxOutputBytes < 1024 || config.MaxOutputBytes > 4*1024*1024 {
		return Config{}, errors.New(
			"generation output limit must be between 1 KiB and 4 MiB",
		)
	}
	if config.MaxRetainedBytes == 0 {
		config.MaxRetainedBytes = 64 << 20
	}
	minimumRetained := uint64(config.MaxOutputBytes)*2 + (64 << 10)
	if config.MaxRetainedBytes < minimumRetained ||
		config.MaxRetainedBytes > 1<<30 {
		return Config{}, errors.New(
			"generation retained-memory limit must fit two outputs plus 64 KiB and not exceed 1 GiB",
		)
	}
	if config.CleanupInterval == 0 {
		config.CleanupInterval = time.Minute
	}
	if config.CleanupInterval < 10*time.Millisecond ||
		config.CleanupInterval > time.Hour {
		return Config{}, errors.New(
			"generation cleanup interval must be between 10 ms and 1 hour",
		)
	}
	return config, nil
}

func (m *Manager) Submit(request protocol.GenerationRequest) (protocol.GenerationJobSubmission, error) {
	if err := protocol.ValidateGeneration(request); err != nil {
		return protocol.GenerationJobSubmission{}, rinruntime.NewFieldError("invalid_request", err.Error(), validationField(err), err)
	}
	requestHash, semanticHash, requestBytes, err := hashRequests(request)
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
		request: request, requestID: request.RequestID,
		requestHash: requestHash, semanticHash: semanticHash,
		requestBytes: requestBytes + generationJobTransitionReserve,
	}
	if cached, ok := m.cache[semanticHash]; ok && now.Sub(cached.createdAt) < m.config.CacheTTL {
		result := cloneResult(cached.result)
		result.CacheHit = true
		state.public.Status = "succeeded"
		state.public.StartedAt = state.public.SubmittedAt
		state.public.FinishedAt = state.public.SubmittedAt
		state.public.Result = &result
		state.completedAt = now
		state.request = protocol.GenerationRequest{}
		state.requestBytes = 0
		publicBytes, err := encodedSize(state.public)
		if err != nil {
			return protocol.GenerationJobSubmission{},
				rinruntime.NewError(
					"job_encode_failed",
					"could not size generation job",
					err,
				)
		}
		state.publicBytes = publicBytes
		if !m.ensureRetainedCapacity(
			0,
			publicBytes,
			"",
			semanticHash,
		) {
			return protocol.GenerationJobSubmission{},
				generationMemoryLimitError()
		}
		m.addJob(jobID, state)
		m.byRequest[request.RequestID] = jobID
		return protocol.GenerationJobSubmission{ProtocolVersion: protocol.Version, JobID: jobID, Status: "succeeded"}, nil
	}

	jobContext, cancel := context.WithCancel(m.ctx)
	state.cancel = cancel
	state.ctx = jobContext
	publicBytes, err := encodedSize(state.public)
	if err != nil {
		cancel()
		return protocol.GenerationJobSubmission{},
			rinruntime.NewError(
				"job_encode_failed",
				"could not size generation job",
				err,
			)
	}
	state.publicBytes = publicBytes
	if !m.ensureRetainedCapacity(
		0,
		state.requestBytes+state.publicBytes,
		"",
		"",
	) {
		cancel()
		return protocol.GenerationJobSubmission{},
			generationMemoryLimitError()
	}
	m.addJob(jobID, state)
	m.byRequest[request.RequestID] = jobID
	select {
	case m.queue <- jobID:
		return protocol.GenerationJobSubmission{ProtocolVersion: protocol.Version, JobID: jobID, Status: "queued"}, nil
	default:
		m.deleteJob(jobID, state)
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
	public := state.public
	public.Status = "canceled"
	public.FinishedAt = now.UTC().Format(time.RFC3339Nano)
	public.Error = protocol.NewErrorDetail(
		"job_canceled",
		"generation job was canceled",
		"",
	)
	if !m.replaceJob(jobID, state, public, true) {
		return protocol.GenerationJob{}, generationMemoryLimitError()
	}
	state.completedAt = now
	return cloneJob(state.public), nil
}

func (m *Manager) Close(ctx context.Context) error {
	if ctx == nil {
		return errors.New("generation manager close context is required")
	}
	m.mu.Lock()
	if !m.closed {
		m.closed = true
		m.cancel()
		now := m.now()
		for _, state := range m.jobs {
			if !terminal(state.public.Status) {
				state.cancel()
				public := state.public
				public.Status = "canceled"
				public.FinishedAt = now.UTC().Format(time.RFC3339Nano)
				public.Error = protocol.NewErrorDetail(
					"generation_closed",
					"generation job manager stopped",
					"",
				)
				_ = m.replaceJob(
					state.public.JobID,
					state,
					public,
					true,
				)
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

func (m *Manager) cleanupWorker() {
	defer m.wait.Done()
	ticker := time.NewTicker(m.config.CleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case now := <-ticker.C:
			m.mu.Lock()
			m.cleanup(now)
			m.mu.Unlock()
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
	public := state.public
	public.Status = "running"
	public.StartedAt = m.now().UTC().Format(time.RFC3339Nano)
	if !m.replaceJob(jobID, state, public, false) {
		failed := state.public
		failed.Status = "failed"
		failed.FinishedAt = m.now().UTC().Format(time.RFC3339Nano)
		failed.Error = protocol.NewErrorDetail(
			"generation_memory_limit",
			"generation retained-memory capacity is full",
			"",
		)
		_ = m.replaceJob(jobID, state, failed, true)
		state.completedAt = m.now()
		m.mu.Unlock()
		return
	}
	request := state.request
	jobContext := state.ctx
	m.mu.Unlock()

	messages := make([]provider.Message, len(request.Messages))
	for index, message := range request.Messages {
		messages[index] = provider.Message{Role: message.Role, Content: message.Content}
	}
	response, err := m.provider.Complete(jobContext, provider.CompletionRequest{
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
	if state.public.Status == "canceled" {
		return
	}
	if err == nil {
		result, validationErr := m.validateResult(response)
		if validationErr != nil {
			err = validationErr
		} else {
			public := state.public
			public.Status = "succeeded"
			public.Result = &result
			public.Error = nil
			public.FinishedAt = now.UTC().Format(time.RFC3339Nano)
			if m.replaceSuccessfulJob(
				jobID,
				state,
				public,
				result,
				now,
			) {
				state.completedAt = now
				m.trimCache(now)
				return
			}
			err = generationMemoryLimitError()
		}
	}
	if err != nil {
		public := state.public
		if errors.Is(err, context.Canceled) {
			public.Status = "canceled"
		} else {
			public.Status = "failed"
		}
		public.Error = jobError(err)
		public.FinishedAt = now.UTC().Format(time.RFC3339Nano)
		// Admission reserves enough bytes for bounded terminal metadata, and
		// clearing the request releases additional capacity.
		_ = m.replaceJob(jobID, state, public, true)
	}
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
	if !jsonwire.Valid([]byte(content)) {
		return protocol.GenerationResult{}, rinruntime.NewError("invalid_generation_json", "provider generation was not strict UTF-8 JSON", nil)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &object); err != nil || object == nil {
		return protocol.GenerationResult{}, rinruntime.NewError("invalid_generation_json", "provider generation was not one JSON object", err)
	}
	return protocol.GenerationResult{
		Content:      content,
		Model:        safeMetadata(response.Model, 160),
		FinishReason: safeMetadata(response.FinishReason, 96),
		PromptTokens: boundedTokenCount(response.Usage.PromptTokens),
		OutputTokens: boundedTokenCount(response.Usage.CompletionTokens),
		TotalTokens:  boundedTokenCount(response.Usage.TotalTokens),
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
			m.deleteCache(hash)
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
		m.deleteCache(entries[0].hash)
		entries = entries[1:]
	}
}

func (m *Manager) deleteJob(id string, state *jobState) {
	delete(m.jobs, id)
	delete(m.byRequest, state.requestID)
	m.retainedBytes -= state.requestBytes + state.publicBytes
}

func (m *Manager) addJob(id string, state *jobState) {
	m.jobs[id] = state
	m.retainedBytes += state.requestBytes + state.publicBytes
}

func (m *Manager) replaceJob(
	id string,
	state *jobState,
	public protocol.GenerationJob,
	clearRequest bool,
) bool {
	publicBytes, err := encodedSize(public)
	if err != nil {
		return false
	}
	replaced := state.publicBytes
	if clearRequest {
		replaced += state.requestBytes
	} else {
		replaced += generationJobTransitionReserve
		publicBytes += generationJobTransitionReserve
	}
	if !m.ensureRetainedCapacity(replaced, publicBytes, id, "") {
		return false
	}
	m.retainedBytes -= replaced
	m.retainedBytes += publicBytes
	state.public = public
	state.publicBytes = publicBytes
	if !clearRequest {
		state.publicBytes -= generationJobTransitionReserve
	}
	if clearRequest {
		state.request = protocol.GenerationRequest{}
		state.requestBytes = 0
	}
	return true
}

func (m *Manager) replaceSuccessfulJob(
	id string,
	state *jobState,
	public protocol.GenerationJob,
	result protocol.GenerationResult,
	now time.Time,
) bool {
	publicBytes, err := encodedSize(public)
	if err != nil {
		return false
	}
	cacheBytes, err := encodedSize(result)
	if err != nil {
		return false
	}
	replaced := state.publicBytes + state.requestBytes
	if existing, ok := m.cache[state.semanticHash]; ok {
		replaced += existing.retainedBytes
	}
	needed := publicBytes + cacheBytes
	if !m.ensureRetainedCapacity(
		replaced,
		needed,
		id,
		state.semanticHash,
	) {
		return false
	}
	m.retainedBytes -= state.publicBytes + state.requestBytes
	state.public = public
	state.publicBytes = publicBytes
	state.request = protocol.GenerationRequest{}
	state.requestBytes = 0
	m.retainedBytes += publicBytes
	m.putCache(
		state.semanticHash,
		cacheEntry{
			result:        cloneResult(result),
			createdAt:     now,
			retainedBytes: cacheBytes,
		},
	)
	return true
}

func (m *Manager) putCache(hash string, entry cacheEntry) {
	m.deleteCache(hash)
	m.cache[hash] = entry
	m.retainedBytes += entry.retainedBytes
}

func (m *Manager) deleteCache(hash string) {
	entry, exists := m.cache[hash]
	if !exists {
		return
	}
	delete(m.cache, hash)
	m.retainedBytes -= entry.retainedBytes
}

func (m *Manager) ensureRetainedCapacity(
	replaced uint64,
	needed uint64,
	preserveJob string,
	preserveCache string,
) bool {
	fits := func() bool {
		if replaced > m.retainedBytes ||
			needed > m.config.MaxRetainedBytes {
			return false
		}
		base := m.retainedBytes - replaced
		return base <= m.config.MaxRetainedBytes-needed
	}
	if fits() {
		return true
	}
	type retainedEntry struct {
		id string
		at time.Time
	}
	caches := make([]retainedEntry, 0, len(m.cache))
	for hash, entry := range m.cache {
		if hash != preserveCache {
			caches = append(
				caches,
				retainedEntry{id: hash, at: entry.createdAt},
			)
		}
	}
	sort.Slice(caches, func(i, j int) bool {
		if caches[i].at.Equal(caches[j].at) {
			return caches[i].id < caches[j].id
		}
		return caches[i].at.Before(caches[j].at)
	})
	for _, entry := range caches {
		m.deleteCache(entry.id)
		if fits() {
			return true
		}
	}
	jobs := make([]retainedEntry, 0, len(m.jobs))
	for id, state := range m.jobs {
		if id != preserveJob &&
			terminal(state.public.Status) &&
			!state.completedAt.IsZero() {
			jobs = append(
				jobs,
				retainedEntry{id: id, at: state.completedAt},
			)
		}
	}
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].at.Equal(jobs[j].at) {
			return jobs[i].id < jobs[j].id
		}
		return jobs[i].at.Before(jobs[j].at)
	})
	for _, entry := range jobs {
		m.deleteJob(entry.id, m.jobs[entry.id])
		if fits() {
			return true
		}
	}
	return false
}

func encodedSize(value any) (uint64, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return 0, err
	}
	return uint64(len(payload)), nil
}

func hashRequests(
	request protocol.GenerationRequest,
) (string, string, uint64, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return "", "", 0, err
	}
	requestDigest := sha256.Sum256(payload)
	semantic := request
	semantic.RequestID = "semantic"
	semanticPayload, err := json.Marshal(semantic)
	if err != nil {
		return "", "", 0, err
	}
	semanticDigest := sha256.Sum256(semanticPayload)
	return hex.EncodeToString(requestDigest[:]),
		hex.EncodeToString(semanticDigest[:]),
		uint64(len(payload)),
		nil
}

func generationMemoryLimitError() error {
	return rinruntime.NewError(
		"generation_memory_limit",
		"generation retained-memory capacity is full",
		ErrMemoryLimit,
	)
}

func jobError(err error) *protocol.ErrorDetail {
	code := rinruntime.ErrorCode(err)
	if errors.Is(err, context.Canceled) {
		code = "job_canceled"
	}
	return protocol.NewErrorDetail(code, err.Error(), rinruntime.ErrorField(err))
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

func boundedTokenCount(value int) int {
	if value < 0 {
		return 0
	}
	maximum := int64(protocol.MaxJSONSafeInteger)
	if int64(value) > maximum {
		return int(maximum)
	}
	return value
}
