package generation_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sunrioa/rin/generation"
	"github.com/sunrioa/rin/protocol"
	"github.com/sunrioa/rin/provider"
	rinruntime "github.com/sunrioa/rin/runtime"
)

func TestGenerationJobSucceedsIsIdempotentAndCaches(t *testing.T) {
	client := &fixtureClient{response: provider.CompletionResponse{
		Content: `{"narration":"The rain stopped."}`, Model: "fixture-model", FinishReason: "stop",
		Usage: provider.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}}
	manager := newManager(t, client, generation.Config{Workers: 1, QueueSize: 4, MaxJobs: 8})
	defer closeManager(t, manager)
	request := generationRequest("request.scene.1")
	submission, err := manager.Submit(request)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := manager.Submit(request)
	if err != nil || !duplicate.Duplicate || duplicate.JobID != submission.JobID {
		t.Fatalf("unexpected duplicate: %+v err=%v", duplicate, err)
	}
	job := waitJob(t, manager, submission.JobID)
	if job.Result == nil || job.Result.Content != client.response.Content || job.Result.CacheHit {
		t.Fatalf("unexpected result: %+v", job)
	}

	cachedRequest := request
	cachedRequest.RequestID = "request.scene.2"
	cached, err := manager.Submit(cachedRequest)
	if err != nil || cached.Status != "succeeded" {
		t.Fatalf("cached submit: %+v err=%v", cached, err)
	}
	cachedJob, err := manager.Get(cached.JobID)
	if err != nil || cachedJob.Result == nil || !cachedJob.Result.CacheHit || client.callCount() != 1 {
		t.Fatalf("unexpected cached job: %+v calls=%d err=%v", cachedJob, client.callCount(), err)
	}

	changed := request
	changed.MaxTokens++
	if _, err := manager.Submit(changed); !errors.Is(err, rinruntime.ErrConflict) {
		t.Fatalf("expected request conflict, got %v", err)
	}
}

func TestGenerationJobCancellation(t *testing.T) {
	client := newBlockingClient()
	manager := newManager(t, client, generation.Config{Workers: 1, QueueSize: 2, MaxJobs: 4})
	defer closeManager(t, manager)
	submission, err := manager.Submit(generationRequest("request.cancel"))
	if err != nil {
		t.Fatal(err)
	}
	client.waitStarted(t)
	job, err := manager.Cancel(submission.JobID)
	if err != nil || job.Status != "canceled" {
		t.Fatalf("cancel: %+v err=%v", job, err)
	}
	job = waitJob(t, manager, submission.JobID)
	if job.Error == nil || job.Error.Code != "job_canceled" {
		t.Fatalf("unexpected canceled job: %+v", job)
	}
}

func TestGenerationQueueIsBounded(t *testing.T) {
	client := newBlockingClient()
	manager := newManager(t, client, generation.Config{Workers: 1, QueueSize: 1, MaxJobs: 3})
	defer closeManager(t, manager)

	first, err := manager.Submit(generationRequest("request.queue.running"))
	if err != nil {
		t.Fatal(err)
	}
	client.waitStarted(t)
	if _, err := manager.Submit(generationRequest("request.queue.waiting")); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Submit(generationRequest("request.queue.full")); !errors.Is(err, generation.ErrQueueFull) {
		t.Fatalf("expected bounded queue error, got %v", err)
	}
	if _, err := manager.Cancel(first.JobID); err != nil {
		t.Fatal(err)
	}
}

func TestGenerationRejectsInvalidProviderOutput(t *testing.T) {
	for _, test := range []struct {
		name    string
		content string
		limit   int
		code    string
	}{
		{name: "not object", content: `[]`, limit: 1024, code: "invalid_generation_json"},
		{name: "too large", content: `{"value":"` + strings.Repeat("x", 1100) + `"}`, limit: 1024, code: "generation_too_large"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &fixtureClient{response: provider.CompletionResponse{Content: test.content}}
			manager := newManager(t, client, generation.Config{Workers: 1, QueueSize: 2, MaxJobs: 4, MaxOutputBytes: test.limit})
			defer closeManager(t, manager)
			submission, err := manager.Submit(generationRequest("request.invalid"))
			if err != nil {
				t.Fatal(err)
			}
			job := waitJob(t, manager, submission.JobID)
			if job.Status != "failed" || job.Error == nil || job.Error.Code != test.code {
				t.Fatalf("unexpected failed job: %+v", job)
			}
		})
	}
}

type fixtureClient struct {
	mu       sync.Mutex
	response provider.CompletionResponse
	err      error
	calls    int
}

func (c *fixtureClient) Complete(_ context.Context, _ provider.CompletionRequest) (provider.CompletionResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	return c.response, c.err
}

func (c *fixtureClient) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

type blockingClient struct {
	started chan struct{}
	once    sync.Once
}

func newBlockingClient() *blockingClient { return &blockingClient{started: make(chan struct{})} }

func (c *blockingClient) Complete(ctx context.Context, _ provider.CompletionRequest) (provider.CompletionResponse, error) {
	c.once.Do(func() { close(c.started) })
	<-ctx.Done()
	return provider.CompletionResponse{}, ctx.Err()
}

func (c *blockingClient) waitStarted(t *testing.T) {
	t.Helper()
	select {
	case <-c.started:
	case <-time.After(time.Second):
		t.Fatal("generation client did not start")
	}
}

func newManager(t *testing.T, client provider.Client, config generation.Config) *generation.Manager {
	t.Helper()
	manager, err := generation.New(client, config)
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func closeManager(t *testing.T, manager *generation.Manager) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func waitJob(t *testing.T, manager *generation.Manager, id string) protocol.GenerationJob {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		job, err := manager.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status == "succeeded" || job.Status == "failed" || job.Status == "canceled" {
			return job
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("generation job did not finish")
	return protocol.GenerationJob{}
}

func generationRequest(requestID string) protocol.GenerationRequest {
	return protocol.GenerationRequest{
		ProtocolVersion: protocol.Version,
		RequestID:       requestID,
		Kind:            "scene",
		ContextHash:     strings.Repeat("a", 64),
		Messages: []protocol.GenerationMessage{
			{Role: "system", Content: "Return one JSON object."},
			{Role: "user", Content: `{"scene":"rain"}`},
		},
		Temperature:    0.6,
		MaxTokens:      512,
		ResponseFormat: "json_object",
	}
}
