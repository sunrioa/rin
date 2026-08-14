package cognition_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/provider"
)

func TestSemanticMemorySupplementsLocalRecallWithoutBecomingRequired(t *testing.T) {
	local, err := cognition.OpenSQLiteMemoryProvider(
		t.TempDir()+"/memory.db", cognition.LocalMemoryConfig{},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	embedder := &semanticTestEmbedder{}
	memory, err := cognition.NewSemanticMemoryProvider(local, embedder, cognition.SemanticMemoryConfig{
		Model: "embed-test", AllowedDomains: []cognition.MemoryDomain{cognition.MemoryActorSemantic},
		MinLocalMatches: 3, MinimumSimilarity: 0.8, TimeoutMillis: 500,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	for index, content := range []string{
		"The player treasures the orchard fruit festival.",
		"The player repaired the eastern bridge.",
		"The player returned from the mountain trail.",
	} {
		if _, err := memory.Append(context.Background(), semanticRecord(index+1, content)); err != nil {
			t.Fatal(err)
		}
	}
	waitForEmbeddingCalls(t, embedder, 3)
	query := cognition.MemoryQuery{
		SessionID: "session.one", ActorID: "actor.one",
		Now:      host.Timepoint{Clock: host.ClockStep, Value: 20},
		Budget:   cognition.MemoryBudget{MaxRecords: 2, MaxCharacters: 2_000},
		Semantic: true, SemanticText: "What did the player value at the fruit celebration?",
	}
	matches, trace, err := memory.RetrieveWithTrace(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if !trace.SemanticUsed || !trace.RemoteRequested || trace.QueryCacheHit || trace.DegradedCode != "" {
		t.Fatalf("semantic trace = %#v", trace)
	}
	if len(matches) != 2 || matches[1].Record.MemoryID != "memory.1" ||
		!slicesContains(matches[1].Reasons, "semantic") {
		t.Fatalf("hybrid matches = %#v", matches)
	}
	before := embedder.callCount()
	if _, trace, err := memory.RetrieveWithTrace(context.Background(), query); err != nil {
		t.Fatal(err)
	} else if !trace.QueryCacheHit || trace.RemoteRequested {
		t.Fatalf("cached semantic trace = %#v", trace)
	}
	if embedder.callCount() != before {
		t.Fatal("query embedding cache missed an identical query")
	}
}

func TestSemanticMemoryFallsBackToLocalAndRejectsPrivateExport(t *testing.T) {
	local, err := cognition.OpenSQLiteMemoryProvider(
		t.TempDir()+"/memory.db", cognition.LocalMemoryConfig{},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	if _, err := cognition.NewSemanticMemoryProvider(local, failingEmbedder{}, cognition.SemanticMemoryConfig{
		Model: "embed-test", AllowedDomains: []cognition.MemoryDomain{cognition.MemoryControllerPrivate},
	}); err == nil {
		t.Fatal("private memory export was accepted")
	}
	// The rejected wrapper does not own or close the local provider.
	memory, err := cognition.NewSemanticMemoryProvider(local, failingEmbedder{}, cognition.SemanticMemoryConfig{
		Model: "embed-test", AllowedDomains: []cognition.MemoryDomain{cognition.MemoryActorSemantic},
		MinLocalMatches: 2, TimeoutMillis: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	if _, err := memory.Append(context.Background(), semanticRecord(1, "A local memory remains available.")); err != nil {
		t.Fatal(err)
	}
	matches, trace, err := memory.RetrieveWithTrace(context.Background(), cognition.MemoryQuery{
		SessionID: "session.one", ActorID: "actor.one",
		Now:      host.Timepoint{Clock: host.ClockStep, Value: 20},
		Budget:   cognition.MemoryBudget{MaxRecords: 4, MaxCharacters: 2_000},
		Semantic: true, SemanticText: "unavailable remote meaning",
	})
	if err != nil || len(matches) != 1 || matches[0].Record.MemoryID != "memory.1" {
		t.Fatalf("local fallback = %#v, %v", matches, err)
	}
	if !trace.SemanticUsed || trace.DegradedCode != "memory.semantic-unavailable" {
		t.Fatalf("fallback trace = %#v", trace)
	}
}

func TestSemanticMemoryDoesNotExportSensitiveText(t *testing.T) {
	local, err := cognition.OpenSQLiteMemoryProvider(
		t.TempDir()+"/memory.db", cognition.LocalMemoryConfig{},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	embedder := &semanticTestEmbedder{}
	memory, err := cognition.NewSemanticMemoryProvider(local, embedder, cognition.SemanticMemoryConfig{
		Model: "embed-test", AllowedDomains: []cognition.MemoryDomain{cognition.MemoryActorSemantic},
		TimeoutMillis: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	query := cognition.MemoryQuery{
		SessionID: "session.one", ActorID: "actor.one",
		Now:      host.Timepoint{Clock: host.ClockStep, Value: 20},
		Budget:   cognition.MemoryBudget{MaxRecords: 4, MaxCharacters: 2_000},
		Semantic: true, SemanticText: "Authorization: Bearer sk-secret-value-1234567890",
	}
	if matches, err := memory.Retrieve(context.Background(), query); err != nil || len(matches) != 0 {
		t.Fatalf("sensitive fallback = %#v, %v", matches, err)
	}
	if embedder.callCount() != 0 {
		t.Fatal("sensitive query text was sent to the embedding provider")
	}
	query.SemanticText = "a harmless private working-memory query"
	query.ControllerID = "controller.one"
	query.Domains = []cognition.MemoryDomain{cognition.MemoryControllerPrivate}
	if _, trace, err := memory.RetrieveWithTrace(context.Background(), query); err != nil {
		t.Fatal(err)
	} else if trace.SemanticUsed {
		t.Fatalf("private-only query used semantic egress: %#v", trace)
	}
	if embedder.callCount() != 0 {
		t.Fatal("private-only query was sent to the embedding provider")
	}
}

func TestSemanticMemoryFallsBackOnTimeoutAndDimensionChange(t *testing.T) {
	for _, test := range []struct {
		name     string
		embedder provider.EmbeddingProvider
		queries  []string
	}{
		{name: "timeout", embedder: waitingEmbedder{}, queries: []string{"find a distant memory"}},
		{name: "dimension-change", embedder: &changingDimensionEmbedder{}, queries: []string{"first meaning", "second meaning"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			local, err := cognition.OpenSQLiteMemoryProvider(
				t.TempDir()+"/memory.db", cognition.LocalMemoryConfig{},
			)
			if err != nil {
				t.Fatal(err)
			}
			defer local.Close()
			memory, err := cognition.NewSemanticMemoryProvider(local, test.embedder, cognition.SemanticMemoryConfig{
				Model: "embed-test", AllowedDomains: []cognition.MemoryDomain{cognition.MemoryActorSemantic},
				TimeoutMillis: 10,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer memory.Close()
			for index, text := range test.queries {
				matches, trace, retrieveErr := memory.RetrieveWithTrace(context.Background(), cognition.MemoryQuery{
					SessionID: "session.one", ActorID: "actor.one",
					Now:      host.Timepoint{Clock: host.ClockStep, Value: 20},
					Budget:   cognition.MemoryBudget{MaxRecords: 4, MaxCharacters: 2_000},
					Semantic: true, SemanticText: text,
				})
				if retrieveErr != nil || len(matches) != 0 {
					t.Fatalf("remote degradation leaked to retrieval: %#v, %v", matches, retrieveErr)
				}
				if trace.DegradedCode == "" && (test.name != "dimension-change" || index != 0) {
					t.Fatalf("remote degradation was not observable: %#v", trace)
				}
			}
		})
	}
}

func semanticRecord(index int, content string) cognition.MemoryRecord {
	return cognition.MemoryRecord{
		MemoryID: "memory." + string(rune('0'+index)),
		Namespace: cognition.MemoryNamespace{
			SessionID: "session.one", ActorID: "actor.one", Domain: cognition.MemoryActorSemantic,
		},
		Content: content,
		Provenance: cognition.MemoryProvenance{
			Source: cognition.MemorySourcePlayer, SourceID: "event." + string(rune('0'+index)),
		},
		Confidence: 1, Importance: 0.8,
		CreatedAt: host.Timepoint{Clock: host.ClockStep, Value: int64(index)},
	}
}

type semanticTestEmbedder struct {
	mu    sync.Mutex
	calls int
}

func (embedder *semanticTestEmbedder) Embed(
	_ context.Context,
	request provider.EmbeddingRequest,
) (provider.EmbeddingResponse, error) {
	embedder.mu.Lock()
	embedder.calls++
	embedder.mu.Unlock()
	vectors := make([][]float32, len(request.Inputs))
	for index, input := range request.Inputs {
		if strings.Contains(strings.ToLower(input), "fruit") || strings.Contains(strings.ToLower(input), "orchard") {
			vectors[index] = []float32{1, 0}
		} else {
			vectors[index] = []float32{0, 1}
		}
	}
	return provider.EmbeddingResponse{Model: "embed-test", Embeddings: vectors}, nil
}

func (embedder *semanticTestEmbedder) callCount() int {
	embedder.mu.Lock()
	defer embedder.mu.Unlock()
	return embedder.calls
}

func waitForEmbeddingCalls(t *testing.T, embedder *semanticTestEmbedder, minimum int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for embedder.callCount() < minimum && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if embedder.callCount() < minimum {
		t.Fatalf("embedding calls = %d, want at least %d", embedder.callCount(), minimum)
	}
}

type failingEmbedder struct{}

func (failingEmbedder) Embed(context.Context, provider.EmbeddingRequest) (provider.EmbeddingResponse, error) {
	return provider.EmbeddingResponse{}, errors.New("offline")
}

type waitingEmbedder struct{}

func (waitingEmbedder) Embed(ctx context.Context, _ provider.EmbeddingRequest) (provider.EmbeddingResponse, error) {
	<-ctx.Done()
	return provider.EmbeddingResponse{}, ctx.Err()
}

type changingDimensionEmbedder struct {
	mu    sync.Mutex
	calls int
}

func (embedder *changingDimensionEmbedder) Embed(
	_ context.Context,
	_ provider.EmbeddingRequest,
) (provider.EmbeddingResponse, error) {
	embedder.mu.Lock()
	defer embedder.mu.Unlock()
	embedder.calls++
	if embedder.calls == 1 {
		return provider.EmbeddingResponse{Embeddings: [][]float32{{1, 0}}}, nil
	}
	return provider.EmbeddingResponse{Embeddings: [][]float32{{1, 0, 0}}}, nil
}
