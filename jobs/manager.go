// Package jobs runs proposal policies outside a game engine's main thread.
package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
)

var (
	ErrQueueFull = errors.New("proposal job queue is full")
	ErrClosed    = errors.New("proposal job manager is closed")
)

type Config struct {
	Workers   int
	QueueSize int
	MaxJobs   int
	JobTTL    time.Duration
}

type Manager struct {
	engine *rinruntime.Engine
	config Config
	ctx    context.Context
	cancel context.CancelFunc
	queue  chan string

	mu        sync.Mutex
	jobs      map[string]*jobState
	byRequest map[string]string
	closed    bool
	now       func() time.Time
	wait      sync.WaitGroup
	done      chan struct{}
}

type jobState struct {
	public      protocol.ProposalJob
	request     protocol.ProposeRequest
	requestHash string
	cancel      context.CancelFunc
	ctx         context.Context
	completedAt time.Time
}

func New(engine *rinruntime.Engine, config Config) (*Manager, error) {
	if engine == nil {
		return nil, errors.New("job manager engine is required")
	}
	if config.Workers <= 0 {
		config.Workers = 2
	}
	if config.Workers > 32 {
		return nil, errors.New("job workers must not exceed 32")
	}
	if config.QueueSize <= 0 {
		config.QueueSize = 64
	}
	if config.QueueSize > 4096 {
		return nil, errors.New("job queue size must not exceed 4096")
	}
	if config.MaxJobs <= 0 {
		config.MaxJobs = 512
	}
	if config.MaxJobs < config.QueueSize || config.MaxJobs > 16384 {
		return nil, errors.New("max jobs must be between queue size and 16384")
	}
	if config.JobTTL <= 0 {
		config.JobTTL = 30 * time.Minute
	}
	ctx, cancel := context.WithCancel(context.Background())
	manager := &Manager{
		engine: engine, config: config, ctx: ctx, cancel: cancel,
		queue: make(chan string, config.QueueSize), jobs: make(map[string]*jobState),
		byRequest: make(map[string]string), now: time.Now, done: make(chan struct{}),
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

func (m *Manager) Submit(request protocol.ProposeRequest) (protocol.ProposalJobSubmission, error) {
	if err := protocol.ValidatePropose(request); err != nil {
		return protocol.ProposalJobSubmission{}, rinruntime.NewFieldError("invalid_request", err.Error(), validationField(err), err)
	}
	requestHash, err := hashRequest(request)
	if err != nil {
		return protocol.ProposalJobSubmission{}, rinruntime.NewError("job_encode_failed", "could not encode proposal job", err)
	}
	requestKey := request.SessionID + "\x00" + request.RequestID
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return protocol.ProposalJobSubmission{}, rinruntime.NewError("jobs_closed", "proposal job manager is closed", ErrClosed)
	}
	now := m.now()
	m.cleanup(now)
	if existingID, exists := m.byRequest[requestKey]; exists {
		existing := m.jobs[existingID]
		if existing.requestHash != requestHash {
			return protocol.ProposalJobSubmission{}, rinruntime.NewFieldError("request_id_conflict", "request id was already used with a different proposal payload", "request_id", rinruntime.ErrConflict)
		}
		return protocol.ProposalJobSubmission{
			ProtocolVersion: protocol.Version, JobID: existing.public.JobID, Status: existing.public.Status, Duplicate: true,
		}, nil
	}
	if len(m.jobs) >= m.config.MaxJobs {
		return protocol.ProposalJobSubmission{}, rinruntime.NewError("jobs_capacity", "proposal job capacity is full", ErrQueueFull)
	}
	jobID := "job." + requestHash[:24]
	jobContext, cancel := context.WithCancel(m.ctx)
	state := &jobState{
		public: protocol.ProposalJob{
			ProtocolVersion: protocol.Version, JobID: jobID, SessionID: request.SessionID,
			RequestID: request.RequestID, Status: "queued", SubmittedAt: now.UTC().Format(time.RFC3339Nano),
		},
		request: request, requestHash: requestHash, cancel: cancel, ctx: jobContext,
	}
	m.jobs[jobID] = state
	m.byRequest[requestKey] = jobID
	select {
	case m.queue <- jobID:
		return protocol.ProposalJobSubmission{ProtocolVersion: protocol.Version, JobID: jobID, Status: "queued"}, nil
	default:
		delete(m.jobs, jobID)
		delete(m.byRequest, requestKey)
		cancel()
		return protocol.ProposalJobSubmission{}, rinruntime.NewError("jobs_queue_full", "proposal job queue is full", ErrQueueFull)
	}
}

func (m *Manager) Get(jobID string) (protocol.ProposalJob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, exists := m.jobs[jobID]
	if !exists {
		return protocol.ProposalJob{}, rinruntime.NewFieldError("job_not_found", "proposal job does not exist", "job_id", rinruntime.ErrNotFound)
	}
	return cloneJob(state.public), nil
}

func (m *Manager) Cancel(jobID string) (protocol.ProposalJob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, exists := m.jobs[jobID]
	if !exists {
		return protocol.ProposalJob{}, rinruntime.NewFieldError("job_not_found", "proposal job does not exist", "job_id", rinruntime.ErrNotFound)
	}
	if terminal(state.public.Status) {
		return cloneJob(state.public), nil
	}
	state.cancel()
	now := m.now()
	state.public.Status = "canceled"
	state.public.FinishedAt = now.UTC().Format(time.RFC3339Nano)
	state.public.Error = &protocol.ErrorDetail{Code: "job_canceled", Message: "proposal job was canceled"}
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
				state.public.Error = &protocol.ErrorDetail{Code: "jobs_closed", Message: "proposal job manager stopped"}
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

	proposal, duplicate, err := m.engine.Propose(jobContext, request)
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	state, exists = m.jobs[jobID]
	if !exists {
		return
	}
	if err == nil {
		state.public.Status = "succeeded"
		state.public.Proposal = &proposal
		state.public.Duplicate = duplicate
		state.public.Error = nil
	} else if state.public.Status != "canceled" {
		switch {
		case errors.Is(err, rinruntime.ErrStale):
			state.public.Status = "stale"
		case errors.Is(err, context.Canceled):
			state.public.Status = "canceled"
		default:
			state.public.Status = "failed"
		}
		state.public.Error = jobError(err)
	}
	state.public.FinishedAt = now.UTC().Format(time.RFC3339Nano)
	state.completedAt = now
}

func (m *Manager) cleanup(now time.Time) {
	type finishedJob struct {
		id string
		at time.Time
	}
	finished := make([]finishedJob, 0)
	for id, state := range m.jobs {
		if terminal(state.public.Status) && !state.completedAt.IsZero() {
			if now.Sub(state.completedAt) >= m.config.JobTTL {
				m.deleteJob(id, state)
				continue
			}
			finished = append(finished, finishedJob{id: id, at: state.completedAt})
		}
	}
	if len(m.jobs) < m.config.MaxJobs {
		return
	}
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

func (m *Manager) deleteJob(id string, state *jobState) {
	delete(m.jobs, id)
	delete(m.byRequest, state.request.SessionID+"\x00"+state.request.RequestID)
}

func hashRequest(request protocol.ProposeRequest) (string, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func jobError(err error) *protocol.ErrorDetail {
	code := rinruntime.ErrorCode(err)
	if errors.Is(err, context.Canceled) {
		code = "job_canceled"
	}
	return &protocol.ErrorDetail{Code: code, Message: err.Error(), Field: rinruntime.ErrorField(err)}
}

func terminal(status string) bool {
	return status == "succeeded" || status == "failed" || status == "stale" || status == "canceled"
}

func cloneJob(job protocol.ProposalJob) protocol.ProposalJob {
	if job.Proposal != nil {
		proposal := *job.Proposal
		proposal.RecalledMemoryIDs = append([]string(nil), proposal.RecalledMemoryIDs...)
		job.Proposal = &proposal
	}
	if job.Error != nil {
		detail := *job.Error
		job.Error = &detail
	}
	return job
}

func validationField(err error) string {
	var validation *protocol.ValidationError
	if errors.As(err, &validation) {
		return validation.Field
	}
	return ""
}
