package cognition_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
)

func TestLocalTaskStoreUsesRevisionCASAndDefensiveCopies(t *testing.T) {
	store, err := cognition.NewLocalTaskStore(10)
	if err != nil {
		t.Fatal(err)
	}
	input := validTaskSession("task.one")
	created, err := store.Create(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if created.Revision != 1 {
		t.Fatalf("unexpected created revision: %d", created.Revision)
	}
	input.Tags[0] = "mutated-input"
	input.AllowedCapabilities[0] = "mutated-capability"
	if created.Tags[0] != "task.follow" ||
		created.AllowedCapabilities[0] != "rin.navigation.move-to" {
		t.Fatalf("create result shared the caller's input slice: %v", created.Tags)
	}
	created.Goal = "Updated goal."
	updated, err := store.CompareAndSwap(context.Background(), 1, created)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 2 || updated.Goal != "Updated goal." {
		t.Fatalf("unexpected task update: %+v", updated)
	}
	updated.Tags[0] = "mutated-output"
	updated.AllowedCapabilities[0] = "mutated-output-capability"
	if _, err := store.CompareAndSwap(context.Background(), 1, created); !errors.Is(err, cognition.ErrTaskRevisionConflict) {
		t.Fatalf("expected stale CAS rejection, got %v", err)
	}
	loaded, err := store.Load(context.Background(), "task.one")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Tags[0] != "task.follow" ||
		loaded.AllowedCapabilities[0] != "rin.navigation.move-to" {
		t.Fatalf("task store was mutated through caller slices: %+v", loaded)
	}
}

func TestTaskCapabilityScopeRejectsInvalidInputAndPendingAction(t *testing.T) {
	base := cognition.StartTaskInput{
		TaskID: "task.scope", HostID: "host.test", WorldID: "world.test",
		ActorID: "actor.mira", ControllerID: "controller.internal",
		Goal: "Use only the scoped capability.",
	}
	duplicate := base
	duplicate.AllowedCapabilities = []string{"dialogue.speak", "dialogue.speak"}
	if err := cognition.ValidateStartTaskInput(duplicate); err == nil {
		t.Fatal("duplicate task capability was accepted")
	}
	invalid := base
	invalid.AllowedCapabilities = []string{"Bad Capability"}
	if err := cognition.ValidateStartTaskInput(invalid); err == nil {
		t.Fatal("invalid task capability was accepted")
	}
	tooMany := base
	for index := 0; index < 129; index++ {
		tooMany.AllowedCapabilities = append(
			tooMany.AllowedCapabilities,
			fmt.Sprintf("capability.%03d", index),
		)
	}
	if err := cognition.ValidateStartTaskInput(tooMany); err == nil {
		t.Fatal("oversized task capability scope was accepted")
	}

	task := validTaskSession("task.pending-outside-scope")
	request := host.ActionRequest{
		RequestID:      "task.pending-outside-scope.action.1",
		ControllerID:   task.ControllerID,
		ActorID:        task.ActorID,
		Capability:     host.CapabilityRef{ID: "dialogue.speak", Version: "2.0.0"},
		SpecDigest:     strings.Repeat("a", 64),
		Arguments:      []byte(`{"text":"hello"}`),
		ExpectedEpoch:  task.ControllerLease.Epoch,
		ObservationSeq: 7,
		TaskID:         task.TaskID,
		IdempotencyKey: "task.pending-outside-scope.action.1",
	}
	task.PendingAction = &request
	store, err := cognition.NewLocalTaskStore(10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(context.Background(), task); err == nil ||
		!strings.Contains(err.Error(), "capability scope") {
		t.Fatalf("pending action outside task scope was accepted: %v", err)
	}
}

func TestLocalTaskStoreRestoresExactPendingAction(t *testing.T) {
	store, err := cognition.NewLocalTaskStore(10)
	if err != nil {
		t.Fatal(err)
	}
	task := validTaskSession("task.pending")
	request := host.ActionRequest{
		RequestID: "task.pending.action.1", ControllerID: task.ControllerID, ActorID: task.ActorID,
		Capability: host.CapabilityRef{ID: "rin.navigation.move-to", Version: "2.0.0"},
		SpecDigest: strings.Repeat("a", 64), Arguments: []byte(`{"distance":2}`),
		Targets: []host.HostRef{{
			Namespace: "test.world", Type: "player", Key: "player-one", Epoch: task.ControllerLease.Epoch,
		}},
		ExpectedEpoch: task.ControllerLease.Epoch, ObservationSeq: 7, TaskID: task.TaskID,
		IdempotencyKey: "task.pending.action.1",
	}
	task.PendingAction = &request
	task.PendingOperationID = "operation.pending.1"
	task.PendingMemories = []cognition.MemoryRecord{{
		MemoryID: "task.pending.memory.1",
		Namespace: cognition.MemoryNamespace{
			SessionID: task.SessionID, ActorID: task.ActorID, ControllerID: task.ControllerID,
			Domain: cognition.MemoryControllerBelief,
		},
		Content: "The player may want company.",
		Provenance: cognition.MemoryProvenance{
			Source: cognition.MemorySourceModel, SourceID: request.RequestID,
		},
		Confidence: 0.5, Importance: 0.4,
		CreatedAt: host.Timepoint{Clock: host.ClockStep, Value: 7},
	}}
	if _, err := store.Create(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	restored, err := cognition.RestoreLocalTaskStore(10, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := restored.Load(context.Background(), task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PendingAction == nil ||
		string(loaded.PendingAction.Arguments) != `{"distance":2}` ||
		loaded.PendingAction.IdempotencyKey != request.IdempotencyKey ||
		!slices.Equal(loaded.AllowedCapabilities, task.AllowedCapabilities) ||
		loaded.PendingOperationID != task.PendingOperationID || len(loaded.PendingMemories) != 1 {
		t.Fatalf("pending action was not restored exactly: %+v", loaded)
	}
}

func TestRestoreLocalTaskStoreRejectsV1Snapshot(t *testing.T) {
	snapshot := cognition.TaskSnapshot{
		Version:  "rin.cognition.tasks/v1",
		Revision: 1,
		Tasks:    []cognition.TaskSession{validTaskSession("task.old-snapshot")},
	}
	if _, err := cognition.RestoreLocalTaskStore(10, snapshot); err == nil {
		t.Fatal("v1 task snapshot was accepted")
	}
}

func TestLocalTaskStoreCapacityAndCancellation(t *testing.T) {
	store, err := cognition.NewLocalTaskStore(1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(context.Background(), validTaskSession("task.one")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(context.Background(), validTaskSession("task.two")); !errors.Is(err, cognition.ErrProviderCapacity) {
		t.Fatalf("expected task capacity error, got %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Load(ctx, "task.one"); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled task load, got %v", err)
	}
}

func TestLocalTaskStoreRejectsLossyJSONIntegers(t *testing.T) {
	store, err := cognition.NewLocalTaskStore(10)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*cognition.TaskSession){
		"timestamp": func(task *cognition.TaskSession) {
			task.UpdatedAtUnixMillis = 9_007_199_254_740_992
		},
		"lease revision": func(task *cognition.TaskSession) {
			task.ControllerLease.AuthorityRevision = 9_007_199_254_740_992
		},
	} {
		t.Run(name, func(t *testing.T) {
			task := validTaskSession("task.lossy")
			mutate(&task)
			if _, err := store.Create(context.Background(), task); err == nil {
				t.Fatal("lossy JSON integer was accepted")
			}
		})
	}
}

func validTaskSession(taskID string) cognition.TaskSession {
	epoch := host.Epoch{
		SessionID: "session.test", WorldID: "world.test", Host: 1, World: 1, Timeline: 1,
	}
	return cognition.TaskSession{
		TaskID: taskID, SessionID: epoch.SessionID, HostID: "host.test", WorldID: epoch.WorldID,
		ActorID: "actor.mira", ControllerID: "controller.internal", Goal: "Follow the player.",
		Tags:                []string{"task.follow"},
		AllowedCapabilities: []string{"rin.navigation.move-to"},
		Status:              cognition.TaskActive,
		ControllerLease: controlplane.ControllerLease{
			LeaseID: "lease.test", ControllerID: "controller.internal", PrincipalID: "principal.internal",
			HostID: "host.test", WorldID: epoch.WorldID, ActorID: "actor.mira",
			Source: controlplane.DecisionInternal, PersonaMode: controlplane.PersonaCharacterBound,
			AuthorityRevision: 1, Epoch: epoch, AcquiredAtUnixMillis: 1, ExpiresAtUnixMillis: 60_001,
		},
		CreatedAtUnixMillis: 10, UpdatedAtUnixMillis: 10,
	}
}
