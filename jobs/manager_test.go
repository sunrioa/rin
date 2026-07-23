package jobs_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/sunrioa/rin/jobs"
	"github.com/sunrioa/rin/policy"
	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
	"github.com/sunrioa/rin/store"
)

func TestProposalJobSucceedsAndIsIdempotent(t *testing.T) {
	engine := jobEngine(t, policy.Deterministic{}, "session.jobs")
	manager := jobManager(t, engine, jobs.Config{Workers: 1, QueueSize: 4, MaxJobs: 8})
	defer closeManager(t, manager)
	request := jobRequest("session.jobs", "request.jobs")
	submission, err := manager.Submit(request)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := manager.Submit(request)
	if err != nil || !duplicate.Duplicate || duplicate.JobID != submission.JobID {
		t.Fatalf("unexpected duplicate: %+v err=%v", duplicate, err)
	}
	result := waitJob(t, manager, submission.JobID)
	if result.Status != "succeeded" || result.Proposal == nil || result.Proposal.PolicySource != "deterministic" {
		t.Fatalf("unexpected job result: %+v", result)
	}
}

func TestProposalJobCancellation(t *testing.T) {
	blocking := newBlockingPolicy()
	engine := jobEngine(t, blocking, "session.cancel")
	manager := jobManager(t, engine, jobs.Config{Workers: 1, QueueSize: 2, MaxJobs: 4})
	defer closeManager(t, manager)
	submission, err := manager.Submit(jobRequest("session.cancel", "request.cancel"))
	if err != nil {
		t.Fatal(err)
	}
	blocking.waitStarted(t)
	result, err := manager.Cancel(submission.JobID)
	if err != nil || result.Status != "canceled" {
		t.Fatalf("cancel result: %+v err=%v", result, err)
	}
	result = waitJob(t, manager, submission.JobID)
	if result.Status != "canceled" || result.Error == nil || result.Error.Code != "job_canceled" {
		t.Fatalf("unexpected canceled job: %+v", result)
	}
}

func TestProposalJobCancelWaitsForPersistedProposal(t *testing.T) {
	eventStore := newBlockingProposalAppendStore(store.NewMemory())
	engine, err := rinruntime.Open(eventStore, policy.Deterministic{})
	if err != nil {
		t.Fatal(err)
	}
	const sessionID = "session.cancel-persist-race"
	if _, err := engine.CreateSession(protocol.CreateSessionRequest{
		ProtocolVersion: protocol.Version,
		RequestID:       "create." + sessionID,
		SessionID:       sessionID,
		Binding: protocol.Binding{
			GameID: "game.jobs", ContentID: "base", ContentVersion: "1", ContentHash: "hash",
		},
		Actors: []protocol.ActorSeed{{
			ID: "npc.jobs", Kind: "npc", DisplayName: "Jobs NPC", Enabled: true, ThinkEveryTicks: 1,
			Goals: []protocol.Goal{{
				ID: "goal.jobs", Description: "Respond", Priority: 1,
				PreferredActions: []string{"talk"}, TargetProgress: 2, Status: "active",
			}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	manager := jobManager(t, engine, jobs.Config{Workers: 1, QueueSize: 2, MaxJobs: 4})
	defer closeManager(t, manager)
	defer eventStore.release()

	submission, err := manager.Submit(jobRequest(sessionID, "request.cancel-persist-race"))
	if err != nil {
		t.Fatal(err)
	}
	eventStore.waitStarted(t)
	type cancelResult struct {
		job protocol.ProposalJob
		err error
	}
	resultChannel := make(chan cancelResult, 1)
	go func() {
		job, cancelErr := manager.Cancel(submission.JobID)
		resultChannel <- cancelResult{job: job, err: cancelErr}
	}()
	select {
	case result := <-resultChannel:
		t.Fatalf("Cancel returned before the durable Proposal settled: %+v err=%v", result.job, result.err)
	case <-time.After(25 * time.Millisecond):
	}

	eventStore.release()
	select {
	case result := <-resultChannel:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.job.Status != "succeeded" || result.job.Proposal == nil {
			t.Fatalf("Cancel hid a Proposal that won the persistence race: %+v", result.job)
		}
	case <-time.After(time.Second):
		t.Fatal("Cancel did not return after the Proposal append completed")
	}
}

func TestProposalJobExposesUnknownOutcomeAndSameRequestRecovers(t *testing.T) {
	eventStore := newUnknownProposalAppendStore(store.NewMemory())
	engine, err := rinruntime.Open(eventStore, policy.Deterministic{})
	if err != nil {
		t.Fatal(err)
	}
	const sessionID = "session.jobs-unknown"
	if _, err := engine.CreateSession(protocol.CreateSessionRequest{
		ProtocolVersion: protocol.Version,
		RequestID:       "create." + sessionID,
		SessionID:       sessionID,
		Binding: protocol.Binding{
			GameID: "game.jobs", ContentID: "base", ContentVersion: "1", ContentHash: "hash",
		},
		Actors: []protocol.ActorSeed{{
			ID: "npc.jobs", Kind: "npc", DisplayName: "Jobs NPC", Enabled: true, ThinkEveryTicks: 1,
			Goals: []protocol.Goal{{
				ID: "goal.jobs", Description: "Respond", Priority: 1,
				PreferredActions: []string{"talk"}, TargetProgress: 2, Status: "active",
			}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	manager := jobManager(t, engine, jobs.Config{Workers: 1, QueueSize: 4, MaxJobs: 8})
	defer closeManager(t, manager)
	request := jobRequest(sessionID, "request.jobs-unknown")

	eventStore.failPostWriteAndConfirmation()
	submission, err := manager.Submit(request)
	if err != nil {
		t.Fatal(err)
	}
	failed := waitJob(t, manager, submission.JobID)
	if failed.Status != "failed" || failed.Error == nil ||
		failed.Error.Code != "proposal_outcome_unknown" {
		t.Fatalf("GET hid the uncertain Proposal outcome: %+v", failed)
	}
	fromGet, err := manager.Get(submission.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if fromGet.Error == nil || fromGet.Error.Code != "proposal_outcome_unknown" {
		t.Fatalf("GET error code = %+v, want proposal_outcome_unknown", fromGet.Error)
	}
	fromCancel, err := manager.Cancel(submission.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if fromCancel.Status != "failed" || fromCancel.Error == nil ||
		fromCancel.Error.Code != "proposal_outcome_unknown" {
		t.Fatalf("Cancel hid the uncertain Proposal outcome: %+v", fromCancel)
	}

	retry, err := manager.Submit(request)
	if err != nil {
		t.Fatalf("same request should be allowed to coordinate recovery: %v", err)
	}
	if !retry.Duplicate || retry.JobID != submission.JobID || retry.Status != "queued" {
		t.Fatalf("unexpected recovery submission: first=%+v retry=%+v", submission, retry)
	}
	recovered := waitJob(t, manager, retry.JobID)
	if recovered.Status != "succeeded" || recovered.Proposal == nil ||
		recovered.Proposal.RequestID != request.RequestID || recovered.Error != nil {
		t.Fatalf("same-request recovery did not return the persisted Proposal: %+v", recovered)
	}
	events, err := eventStore.Load(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[1].Type != rinruntime.EventProposed {
		t.Fatalf("same-request recovery should retain one Proposal event: %+v", events)
	}
}

func TestProposalJobBecomesStaleWhenStateChanges(t *testing.T) {
	blocking := newBlockingPolicy()
	engine := jobEngine(t, blocking, "session.stale-job")
	manager := jobManager(t, engine, jobs.Config{Workers: 1, QueueSize: 2, MaxJobs: 4})
	defer closeManager(t, manager)
	submission, err := manager.Submit(jobRequest("session.stale-job", "request.stale-job"))
	if err != nil {
		t.Fatal(err)
	}
	blocking.waitStarted(t)
	_, err = engine.Observe(protocol.ObserveRequest{
		ProtocolVersion: protocol.Version, SessionID: "session.stale-job", RequestID: "observe.stale-job",
		EventID: "event.stale-job", Tick: 0, ObserverIDs: []string{"npc.jobs"}, Source: "game", Kind: "world",
		Summary: "The world changed while the model was thinking.", Importance: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	blocking.release()
	result := waitJob(t, manager, submission.JobID)
	if result.Status != "stale" || result.Error == nil || result.Error.Code != "state_changed" {
		t.Fatalf("unexpected stale job: %+v", result)
	}
}

func TestProposalJobQueueCapacityAndRequestConflict(t *testing.T) {
	blocking := newBlockingPolicy()
	engine := jobEngine(t, blocking, "session.capacity")
	manager := jobManager(t, engine, jobs.Config{Workers: 1, QueueSize: 1, MaxJobs: 2})
	defer closeManager(t, manager)
	first := jobRequest("session.capacity", "request.capacity.1")
	if _, err := manager.Submit(first); err != nil {
		t.Fatal(err)
	}
	blocking.waitStarted(t)
	second := jobRequest("session.capacity", "request.capacity.2")
	if _, err := manager.Submit(second); err != nil {
		t.Fatal(err)
	}
	third := jobRequest("session.capacity", "request.capacity.3")
	if _, err := manager.Submit(third); !errors.Is(err, jobs.ErrQueueFull) {
		t.Fatalf("expected capacity error, got %v", err)
	}
	changed := second
	changed.Intent = "different payload"
	if _, err := manager.Submit(changed); !errors.Is(err, rinruntime.ErrConflict) {
		t.Fatalf("expected request conflict, got %v", err)
	}
	blocking.release()
}

type blockingPolicy struct {
	started        chan struct{}
	releaseChannel chan struct{}
	once           sync.Once
}

type blockingProposalAppendStore struct {
	rinruntime.Store

	started     chan struct{}
	releaseOnce sync.Once
	startOnce   sync.Once
	releaseCh   chan struct{}
}

type unknownProposalAppendStore struct {
	rinruntime.Store

	mu        sync.Mutex
	failStage int
}

func newBlockingProposalAppendStore(delegate rinruntime.Store) *blockingProposalAppendStore {
	return &blockingProposalAppendStore{
		Store: delegate, started: make(chan struct{}), releaseCh: make(chan struct{}),
	}
}

func newUnknownProposalAppendStore(delegate rinruntime.Store) *unknownProposalAppendStore {
	return &unknownProposalAppendStore{Store: delegate}
}

func (s *blockingProposalAppendStore) Append(sessionID string, event protocol.EventRecord) error {
	if event.Type == rinruntime.EventProposed {
		s.startOnce.Do(func() { close(s.started) })
		<-s.releaseCh
	}
	return s.Store.Append(sessionID, event)
}

func (s *unknownProposalAppendStore) failPostWriteAndConfirmation() {
	s.mu.Lock()
	s.failStage = 1
	s.mu.Unlock()
}

func (s *unknownProposalAppendStore) Append(sessionID string, event protocol.EventRecord) error {
	if event.Type != rinruntime.EventProposed {
		return s.Store.Append(sessionID, event)
	}
	s.mu.Lock()
	stage := s.failStage
	if stage == 1 {
		s.failStage = 2
	} else if stage == 2 {
		s.failStage = 0
	}
	s.mu.Unlock()
	switch stage {
	case 1:
		if err := s.Store.Append(sessionID, event); err != nil {
			return err
		}
		return errUnknownProposalAppend
	case 2:
		return errUnknownProposalAppend
	default:
		return s.Store.Append(sessionID, event)
	}
}

func (s *blockingProposalAppendStore) waitStarted(t *testing.T) {
	t.Helper()
	select {
	case <-s.started:
	case <-time.After(time.Second):
		t.Fatal("proposal append did not start")
	}
}

func (s *blockingProposalAppendStore) release() {
	s.releaseOnce.Do(func() { close(s.releaseCh) })
}

var errUnknownProposalAppend = errors.New("injected uncertain Proposal append")

func newBlockingPolicy() *blockingPolicy {
	return &blockingPolicy{started: make(chan struct{}), releaseChannel: make(chan struct{})}
}

func (p *blockingPolicy) Propose(ctx context.Context, _ rinruntime.PolicyContext) (rinruntime.ProposalDraft, error) {
	p.once.Do(func() { close(p.started) })
	select {
	case <-p.releaseChannel:
		return rinruntime.ProposalDraft{ActionID: "talk", Stance: "engage", Summary: "reply", Rationale: "allowed", PolicySource: "test"}, nil
	case <-ctx.Done():
		return rinruntime.ProposalDraft{}, ctx.Err()
	}
}

func (p *blockingPolicy) waitStarted(t *testing.T) {
	t.Helper()
	select {
	case <-p.started:
	case <-time.After(time.Second):
		t.Fatal("blocking policy did not start")
	}
}

func (p *blockingPolicy) release() {
	select {
	case <-p.releaseChannel:
	default:
		close(p.releaseChannel)
	}
}

func jobManager(t *testing.T, engine *rinruntime.Engine, config jobs.Config) *jobs.Manager {
	t.Helper()
	manager, err := jobs.New(engine, config)
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func closeManager(t *testing.T, manager *jobs.Manager) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func waitJob(t *testing.T, manager *jobs.Manager, id string) protocol.ProposalJob {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		job, err := manager.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status == "succeeded" || job.Status == "failed" || job.Status == "stale" || job.Status == "canceled" {
			return job
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("job did not finish")
	return protocol.ProposalJob{}
}

func jobEngine(t *testing.T, selectedPolicy rinruntime.Policy, sessionID string) *rinruntime.Engine {
	t.Helper()
	engine, err := rinruntime.Open(store.NewMemory(), selectedPolicy)
	if err != nil {
		t.Fatal(err)
	}
	_, err = engine.CreateSession(protocol.CreateSessionRequest{
		ProtocolVersion: protocol.Version, RequestID: "create." + sessionID, SessionID: sessionID,
		Binding: protocol.Binding{GameID: "game.jobs", ContentID: "base", ContentVersion: "1", ContentHash: "hash"},
		Actors: []protocol.ActorSeed{{
			ID: "npc.jobs", Kind: "npc", DisplayName: "Jobs NPC", Enabled: true, ThinkEveryTicks: 1,
			Goals: []protocol.Goal{{ID: "goal.jobs", Description: "Respond", Priority: 1, PreferredActions: []string{"talk"}, TargetProgress: 2, Status: "active"}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func jobRequest(sessionID, requestID string) protocol.ProposeRequest {
	return protocol.ProposeRequest{
		ProtocolVersion: protocol.Version, SessionID: sessionID, RequestID: requestID,
		ActorID: "npc.jobs", Tick: 0, Intent: "Respond",
		CandidateActions: []protocol.ActionSpec{{ID: "talk", Kind: "dialogue", Description: "say something"}},
	}
}
