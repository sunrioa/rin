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
