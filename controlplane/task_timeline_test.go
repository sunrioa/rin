package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/timeline"
)

func TestTaskTimelineTracksAuthoritativeOperationLifecycle(t *testing.T) {
	service, lease, _, principal, actionHost := actionOperationTestHarness(t, Options{})
	input := actionHost.input("request.timeline.lifecycle", "action.timeline.lifecycle")
	input.Request.TaskID = "task.timeline.lifecycle"
	operation, err := service.SubmitAction(context.Background(), principal, input)
	if err != nil {
		t.Fatalf("SubmitAction: %v", err)
	}
	initial, err := service.GetTaskTimeline(principal, timeline.Query{
		TaskID: input.Request.TaskID, Limit: 1,
	})
	if err != nil || len(initial.Events) != 1 || initial.More {
		t.Fatalf("initial timeline = %#v, %v", initial, err)
	}
	assertTimelineOperation(t, initial.Events[0], OperationQueued, false, false)

	waited := make(chan timeline.Update, 1)
	failure := make(chan error, 1)
	go func() {
		update, waitErr := service.WaitTaskTimeline(context.Background(), principal, timeline.WaitInput{
			TaskID: input.Request.TaskID, AfterCursor: initial.NextCursor,
			Limit: 64, WaitMillis: 1_000,
		})
		if waitErr != nil {
			failure <- waitErr
			return
		}
		waited <- update
	}()
	time.Sleep(10 * time.Millisecond)
	pollHost(t, service, lease, 1)
	select {
	case err := <-failure:
		t.Fatalf("WaitTaskTimeline: %v", err)
	case update := <-waited:
		if !update.Changed || len(update.Timeline.Events) != 1 {
			t.Fatalf("delivery update = %#v", update)
		}
		assertTimelineOperation(t, update.Timeline.Events[0], OperationDelivered, false, false)
	case <-time.After(time.Second):
		t.Fatal("WaitTaskTimeline did not wake for delivery")
	}

	if err := service.AcknowledgeHost(
		"test.host", lease.LeaseID,
		HostAcknowledgement{OperationID: operation.OperationID, Accepted: true},
	); err != nil {
		t.Fatal(err)
	}
	if err := service.ReportHostRun("test.host", lease.LeaseID, host.ActionRun{
		OperationID: operation.OperationID, Status: host.ActionRunning,
		ProgressSeq: 1, Progress: 40,
		UpdatedAt: host.Timepoint{Clock: host.ClockStep, Value: 11},
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.ReportHostResult(
		"test.host", lease.LeaseID,
		host.ActionOutcome{
			OperationID: operation.OperationID, Status: host.ActionSucceeded,
			Summary: "The Host completed the task.", Epoch: testEpoch(), WorldSeq: 2,
			OccurredAt: host.Timepoint{Clock: host.ClockStep, Value: 12},
		},
		json.RawMessage(`{"api_key":"must-not-enter-timeline","private":"payload"}`),
	); err != nil {
		t.Fatal(err)
	}
	page, err := service.GetTaskTimeline(principal, timeline.Query{
		TaskID: input.Request.TaskID, Limit: 2,
	})
	if err != nil || len(page.Events) != 2 || !page.More {
		t.Fatalf("first page = %#v, %v", page, err)
	}
	second, err := service.GetTaskTimeline(principal, timeline.Query{
		TaskID: input.Request.TaskID, AfterCursor: page.NextCursor, Limit: 64,
	})
	if err != nil || len(second.Events) != 3 || second.More {
		t.Fatalf("second page = %#v, %v", second, err)
	}
	assertTimelineOperation(
		t, second.Events[len(second.Events)-1], OperationSucceeded, true, true,
	)
	payload, err := json.Marshal(append(page.Events, second.Events...))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "must-not-enter-timeline") ||
		strings.Contains(string(payload), `"private":"payload"`) {
		t.Fatalf("timeline leaked operation output: %s", payload)
	}
}

func TestTaskTimelineEnforcesPrincipalAndTaskBoundaries(t *testing.T) {
	service, _, _, principal, actionHost := actionOperationTestHarness(t, Options{})
	input := actionHost.input("request.timeline.private", "action.timeline.private")
	input.Request.TaskID = "task.timeline.private"
	if _, err := service.SubmitAction(context.Background(), principal, input); err != nil {
		t.Fatal(err)
	}
	other := host.Principal{ID: "player.two", GrantedScopes: []string{ScopeActorRead}}
	if _, err := service.GetTaskTimeline(other, timeline.Query{
		TaskID: input.Request.TaskID,
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other principal error = %v", err)
	}
	admin := host.Principal{ID: "admin.one", GrantedScopes: []string{ScopeHostAdmin}}
	if _, err := service.GetTaskTimeline(admin, timeline.Query{
		TaskID: input.Request.TaskID,
	}); err != nil {
		t.Fatalf("admin timeline: %v", err)
	}
	if _, err := service.GetTaskTimeline(principal, timeline.Query{
		TaskID: "task.timeline.missing",
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing timeline error = %v", err)
	}
}

func TestTaskTimelineCursorSurvivesOperationFileRestart(t *testing.T) {
	root := t.TempDir()
	now := time.UnixMilli(1_000_000)
	service, lease, principal, actionHost := openActionFileHarness(
		t, root, &now, "instance.timeline.first",
	)
	input := actionHost.input("request.timeline.persist", "action.timeline.persist")
	input.Request.TaskID = "task.timeline.persist"
	operation, err := service.SubmitAction(context.Background(), principal, input)
	if err != nil {
		t.Fatal(err)
	}
	pollHost(t, service, lease, 1)
	if err := service.AcknowledgeHost(
		"test.host", lease.LeaseID,
		HostAcknowledgement{OperationID: operation.OperationID, Accepted: true},
	); err != nil {
		t.Fatal(err)
	}
	if err := service.ReportHostOutcome("test.host", lease.LeaseID, host.ActionOutcome{
		OperationID: operation.OperationID, Status: host.ActionSucceeded,
		Summary: "Persisted result.", Epoch: testEpoch(), WorldSeq: 2,
		OccurredAt: host.Timepoint{Clock: host.ClockStep, Value: 12},
	}); err != nil {
		t.Fatal(err)
	}
	before, err := service.GetTaskTimeline(principal, timeline.Query{TaskID: input.Request.TaskID})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	restored, _, restoredPrincipal, _ := openActionFileHarness(
		t, root, &now, "instance.timeline.restored",
	)
	defer restored.Close()
	after, err := restored.GetTaskTimeline(restoredPrincipal, timeline.Query{
		TaskID: input.Request.TaskID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if after.NextCursor != before.NextCursor || len(after.Events) != len(before.Events) {
		t.Fatalf("restored timeline changed: before=%#v after=%#v", before, after)
	}
	empty, err := restored.GetTaskTimeline(restoredPrincipal, timeline.Query{
		TaskID: input.Request.TaskID, AfterCursor: before.NextCursor,
	})
	if err != nil || len(empty.Events) != 0 || empty.NextCursor != before.NextCursor {
		t.Fatalf("incremental restored timeline = %#v, %v", empty, err)
	}
}

func TestTaskTimelineReportsRetentionTruncation(t *testing.T) {
	service, lease, _, principal, actionHost := actionOperationTestHarness(t, Options{})
	input := actionHost.input("request.timeline.retention", "action.timeline.retention")
	input.Request.TaskID = "task.timeline.retention"
	if _, err := service.SubmitAction(context.Background(), principal, input); err != nil {
		t.Fatal(err)
	}
	for range maxOperationTimelineEvents + 2 {
		pollHost(t, service, lease, 1)
	}
	page, err := service.GetTaskTimeline(principal, timeline.Query{
		TaskID: input.Request.TaskID, Limit: timeline.MaximumLimit,
	})
	if err != nil || !page.Truncated || len(page.Events) != maxOperationTimelineEvents {
		t.Fatalf("retained timeline = %#v, %v", page, err)
	}
}

func TestTaskTimelineRefreshesExpiredOperation(t *testing.T) {
	service, _, now, principal, actionHost := actionOperationTestHarness(t, Options{
		OperationTTL: time.Second,
	})
	input := actionHost.input("request.timeline.expired", "action.timeline.expired")
	input.Request.TaskID = "task.timeline.expired"
	if _, err := service.SubmitAction(context.Background(), principal, input); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(2 * time.Second)
	page, err := service.GetTaskTimeline(principal, timeline.Query{TaskID: input.Request.TaskID})
	if err != nil || len(page.Events) != 2 {
		t.Fatalf("expired timeline = %#v, %v", page, err)
	}
	last := page.Events[len(page.Events)-1]
	if last.Operation == nil || last.Operation.Status != string(OperationStale) ||
		!last.Operation.Terminal || last.Operation.ExecutionConfirmed {
		t.Fatalf("expired evidence = %#v", last)
	}
}

func TestOperationTimelinePersistenceRejectsContradictoryEvidence(t *testing.T) {
	valid := operationTimelineEvent{
		Sequence: 1, Kind: "operation.succeeded", Status: OperationSucceeded,
		AtUnixMillis: 1_000, Terminal: true, ExecutionConfirmed: true,
		OutcomeCode: "succeeded",
	}
	for name, mutate := range map[string]func(*operationTimelineEvent){
		"unconfirmed terminal": func(event *operationTimelineEvent) {
			event.Terminal = false
		},
		"mismatched outcome": func(event *operationTimelineEvent) {
			event.OutcomeCode = "failed"
		},
		"invalid reconciliation": func(event *operationTimelineEvent) {
			event.ReconciliationPending = true
		},
	} {
		t.Run(name, func(t *testing.T) {
			event := valid
			mutate(&event)
			if err := validateOperationTimeline([]operationTimelineEvent{event}, 0); err == nil {
				t.Fatalf("contradictory event was accepted: %#v", event)
			}
		})
	}
}

func TestOperationTimelinePersistenceRejectsDuplicateGlobalCursor(t *testing.T) {
	operations := map[string]*operationState{
		"operation.one": {timeline: []operationTimelineEvent{{Sequence: 1}}},
		"operation.two": {timeline: []operationTimelineEvent{{Sequence: 1}}},
	}
	if err := validateOperationTimelineSequences(operations, 1); err == nil {
		t.Fatal("duplicate global timeline sequence was accepted")
	}
}

func assertTimelineOperation(
	t *testing.T,
	event timeline.Event,
	status OperationStatus,
	terminal bool,
	executionConfirmed bool,
) {
	t.Helper()
	if event.Operation == nil || event.Operation.Status != string(status) ||
		event.Operation.Terminal != terminal ||
		event.Operation.ExecutionConfirmed != executionConfirmed {
		t.Fatalf("timeline event = %#v", event)
	}
}
