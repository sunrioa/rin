package cognition_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/host"
)

func TestMemoryProviderSeparatesSharedAndControllerPrivateMemory(t *testing.T) {
	provider := newMemoryProvider(t)
	shared := actorMemoryNamespace()
	internal := controllerMemoryNamespace("controller.internal")
	external := controllerMemoryNamespace("controller.external")
	appendMemory(t, provider, memoryRecord(
		"memory.shared", shared, "The player returned home.", cognition.MemorySourceHostOutcome, true, 1,
	))
	appendMemory(t, provider, memoryRecord(
		"memory.internal", internal, "The player may be tired.", cognition.MemorySourceModel, false, 2,
	))
	appendMemory(t, provider, memoryRecord(
		"memory.external", external, "The player may want to explore.", cognition.MemorySourceModel, false, 3,
	))

	internalMatches := retrieveMemory(t, provider, cognition.MemoryQuery{
		SessionID: "session.test", ActorID: "actor.mira", ControllerID: "controller.internal",
		Now: eventTime(5), Budget: cognition.MemoryBudget{MaxRecords: 10, MaxCharacters: 1_000},
	})
	if ids := memoryMatchIDs(internalMatches); !reflect.DeepEqual(ids, []string{"memory.internal", "memory.shared"}) {
		t.Fatalf("unexpected internal visibility: %v", ids)
	}

	externalMatches := retrieveMemory(t, provider, cognition.MemoryQuery{
		SessionID: "session.test", ActorID: "actor.mira", ControllerID: "controller.external",
		Now: eventTime(5), Budget: cognition.MemoryBudget{MaxRecords: 10, MaxCharacters: 1_000},
	})
	if ids := memoryMatchIDs(externalMatches); !reflect.DeepEqual(ids, []string{"memory.external", "memory.shared"}) {
		t.Fatalf("unexpected external visibility: %v", ids)
	}

	sharedOnly := retrieveMemory(t, provider, cognition.MemoryQuery{
		SessionID: "session.test", ActorID: "actor.mira", Now: eventTime(5),
		Budget: cognition.MemoryBudget{MaxRecords: 10, MaxCharacters: 1_000},
	})
	if ids := memoryMatchIDs(sharedOnly); !reflect.DeepEqual(ids, []string{"memory.shared"}) {
		t.Fatalf("private memory escaped without a controller: %v", ids)
	}
}

func TestMemoryProviderDoesNotPromoteModelInferenceToSharedFact(t *testing.T) {
	provider := newMemoryProvider(t)
	modelFact := memoryRecord(
		"memory.inference", actorMemoryNamespace(), "The player is angry.",
		cognition.MemorySourceModel, false, 1,
	)
	if _, err := provider.Append(context.Background(), modelFact); err == nil {
		t.Fatal("model-generated memory entered the actor-shared namespace")
	}

	playerFact := memoryRecord(
		"memory.player", actorMemoryNamespace(), "The player said hello.",
		cognition.MemorySourcePlayer, true, 1,
	)
	if _, err := provider.Append(context.Background(), playerFact); err == nil {
		t.Fatal("non-Host provenance was accepted as authoritative")
	}

	unmarkedOutcome := memoryRecord(
		"memory.outcome", actorMemoryNamespace(), "The Host moved the actor.",
		cognition.MemorySourceHostOutcome, false, 1,
	)
	if _, err := provider.Append(context.Background(), unmarkedOutcome); err == nil {
		t.Fatal("Host Outcome memory lost its authoritative marker")
	}
}

func TestMemoryProviderOwnsRecallMetadata(t *testing.T) {
	provider := newMemoryProvider(t)
	record := memoryRecord(
		"memory.forged-recall", actorMemoryNamespace(), "A memory.",
		cognition.MemorySourceHostOutcome, true, 1,
	)
	record.RecallCount = 99
	if _, err := provider.Append(context.Background(), record); err == nil {
		t.Fatal("caller forged provider-owned recall metadata")
	}
}

func TestMemoryProviderRejectsLossyJSONIntegers(t *testing.T) {
	record := memoryRecord(
		"memory.lossy", actorMemoryNamespace(), "A memory.",
		cognition.MemorySourceHostOutcome, true, 1,
	)
	record.CreatedAt.Value = 9_007_199_254_740_992
	if _, err := cognition.RestoreLocalMemoryProvider(
		cognition.LocalMemoryConfig{},
		cognition.MemorySnapshot{Revision: 1, Records: []cognition.MemoryRecord{record}},
	); err == nil {
		t.Fatal("lossy memory timepoint was accepted")
	}
}

func TestMemoryProviderRanksMatchesAndHonorsExpiryAndBudget(t *testing.T) {
	provider := newMemoryProvider(t)
	namespace := actorMemoryNamespace()
	relevant := memoryRecord(
		"memory.relevant", namespace, "The player asked to collect oak logs.",
		cognition.MemorySourceHostOutcome, true, 7,
	)
	relevant.Tags = []string{"task.collect"}
	relevant.SubjectRefs = []string{"target.player"}
	relevant.Importance = 0.8
	appendMemory(t, provider, relevant)

	ordinary := memoryRecord(
		"memory.ordinary", namespace, "It started raining.",
		cognition.MemorySourceHostOutcome, true, 8,
	)
	ordinary.Importance = 0.2
	appendMemory(t, provider, ordinary)

	expired := memoryRecord(
		"memory.expired", namespace, "Old oak log request.",
		cognition.MemorySourceHostOutcome, true, 1,
	)
	expires := eventTime(5)
	expired.ExpiresAt = &expires
	expired.Tags = []string{"task.collect"}
	appendMemory(t, provider, expired)

	matches := retrieveMemory(t, provider, cognition.MemoryQuery{
		SessionID: "session.test", ActorID: "actor.mira", Terms: []string{"oak"},
		Tags: []string{"task.collect"}, SubjectRefs: []string{"target.player"}, Now: eventTime(10),
		Budget: cognition.MemoryBudget{MaxRecords: 1, MaxCharacters: 1_000},
	})
	if len(matches) != 1 || matches[0].Record.MemoryID != "memory.relevant" {
		t.Fatalf("unexpected bounded retrieval: %+v", matches)
	}
	if matches[0].Record.RecallCount != 1 || matches[0].Record.LastRecalledAt == nil {
		t.Fatalf("recall metadata was not updated: %+v", matches[0].Record)
	}
	if !reflect.DeepEqual(matches[0].Reasons, []string{"importance", "confidence", "tag", "subject", "term", "recency"}) {
		t.Fatalf("unexpected retrieval explanation: %v", matches[0].Reasons)
	}

	none := retrieveMemory(t, provider, cognition.MemoryQuery{
		SessionID: "session.test", ActorID: "actor.mira", Now: eventTime(10),
		Budget: cognition.MemoryBudget{MaxRecords: 10, MaxCharacters: 5},
	})
	if len(none) != 0 {
		t.Fatalf("character budget was exceeded: %+v", none)
	}
}

func TestMemoryProviderConsolidatesForgetsAndRestoresTombstones(t *testing.T) {
	provider := newMemoryProvider(t)
	namespace := actorMemoryNamespace()
	appendMemory(t, provider, memoryRecord(
		"memory.one", namespace, "The player chose tea.", cognition.MemorySourceHostOutcome, true, 1,
	))
	appendMemory(t, provider, memoryRecord(
		"memory.two", namespace, "The player chose tea again.", cognition.MemorySourceHostOutcome, true, 2,
	))

	summary := memoryRecord(
		"memory.summary", namespace, "The player consistently prefers tea.",
		cognition.MemorySourceSystem, false, 3,
	)
	consolidated, err := provider.Consolidate(context.Background(), cognition.MemoryConsolidation{
		Namespace: namespace, SourceMemoryIDs: []string{"memory.two", "memory.one"},
		Summary: summary, Reason: "periodic-consolidation",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(consolidated.Supersedes, []string{"memory.one", "memory.two"}) {
		t.Fatalf("unexpected consolidation lineage: %v", consolidated.Supersedes)
	}

	matches := retrieveMemory(t, provider, cognition.MemoryQuery{
		SessionID: "session.test", ActorID: "actor.mira", Now: eventTime(4),
		Budget: cognition.MemoryBudget{MaxRecords: 10, MaxCharacters: 1_000},
	})
	if ids := memoryMatchIDs(matches); !reflect.DeepEqual(ids, []string{"memory.summary"}) {
		t.Fatalf("superseded memories remained retrievable: %v", ids)
	}
	if err := provider.Forget(context.Background(), cognition.MemoryForgetRequest{
		Namespace: namespace, MemoryIDs: []string{"memory.summary"},
		Reason: "player-request", At: eventTime(5),
	}); err != nil {
		t.Fatal(err)
	}

	snapshot, err := provider.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Records) != 3 || len(snapshot.Tombstones) != 3 {
		t.Fatalf("audit lineage was not retained: %+v", snapshot)
	}
	restored, err := cognition.RestoreLocalMemoryProvider(cognition.LocalMemoryConfig{}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	restoredMatches := retrieveMemory(t, restored, cognition.MemoryQuery{
		SessionID: "session.test", ActorID: "actor.mira", Now: eventTime(6),
		Budget: cognition.MemoryBudget{MaxRecords: 10, MaxCharacters: 1_000},
	})
	if len(restoredMatches) != 0 {
		t.Fatalf("forgotten memories reappeared after restore: %+v", restoredMatches)
	}
}

func TestMemoryProviderCapacityAndCancellation(t *testing.T) {
	provider, err := cognition.NewLocalMemoryProvider(cognition.LocalMemoryConfig{
		MaxActiveRecordsPerNamespace: 1, MaxHistoryPerNamespace: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	namespace := actorMemoryNamespace()
	appendMemory(t, provider, memoryRecord(
		"memory.one", namespace, "One.", cognition.MemorySourceHostOutcome, true, 1,
	))
	_, err = provider.Append(context.Background(), memoryRecord(
		"memory.two", namespace, "Two.", cognition.MemorySourceHostOutcome, true, 2,
	))
	if !errors.Is(err, cognition.ErrProviderCapacity) {
		t.Fatalf("expected active capacity error, got %v", err)
	}
	if err := provider.Forget(context.Background(), cognition.MemoryForgetRequest{
		Namespace: namespace, MemoryIDs: []string{"memory.one"}, Reason: "replace", At: eventTime(2),
	}); err != nil {
		t.Fatal(err)
	}
	appendMemory(t, provider, memoryRecord(
		"memory.two", namespace, "Two.", cognition.MemorySourceHostOutcome, true, 2,
	))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = provider.Retrieve(ctx, cognition.MemoryQuery{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled retrieval, got %v", err)
	}
}

func newMemoryProvider(t *testing.T) *cognition.LocalMemoryProvider {
	t.Helper()
	provider, err := cognition.NewLocalMemoryProvider(cognition.LocalMemoryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func actorMemoryNamespace() cognition.MemoryNamespace {
	return cognition.MemoryNamespace{
		SessionID: "session.test", ActorID: "actor.mira", Domain: cognition.MemoryActorSemantic,
	}
}

func controllerMemoryNamespace(controllerID string) cognition.MemoryNamespace {
	return cognition.MemoryNamespace{
		SessionID: "session.test", ActorID: "actor.mira", ControllerID: controllerID,
		Domain: cognition.MemoryControllerBelief,
	}
}

func memoryRecord(
	id string,
	namespace cognition.MemoryNamespace,
	content string,
	source cognition.MemorySource,
	authoritative bool,
	tick int64,
) cognition.MemoryRecord {
	return cognition.MemoryRecord{
		MemoryID: id, Namespace: namespace, Content: content,
		Provenance: cognition.MemoryProvenance{
			Source: source, SourceID: "source." + id, Authoritative: authoritative,
		},
		Confidence: 0.9, Importance: 0.5, CreatedAt: eventTime(tick),
	}
}

func appendMemory(
	t *testing.T,
	provider *cognition.LocalMemoryProvider,
	record cognition.MemoryRecord,
) {
	t.Helper()
	if _, err := provider.Append(context.Background(), record); err != nil {
		t.Fatal(err)
	}
}

func retrieveMemory(
	t *testing.T,
	provider *cognition.LocalMemoryProvider,
	query cognition.MemoryQuery,
) []cognition.MemoryMatch {
	t.Helper()
	matches, err := provider.Retrieve(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

func memoryMatchIDs(matches []cognition.MemoryMatch) []string {
	ids := make([]string, 0, len(matches))
	for _, match := range matches {
		ids = append(ids, match.Record.MemoryID)
	}
	return ids
}

func eventTime(value int64) host.Timepoint {
	return host.Timepoint{Clock: host.ClockEvent, Value: value}
}
