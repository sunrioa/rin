package cognition_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/experience"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/taskstate"
	"github.com/sunrioa/rin/timeline"
)

func TestAgentRuntimeSharesOnePlanAcrossDecisionAndOutcome(t *testing.T) {
	fixture := newAgentRuntimeFixture(t)
	plans := &runtimePlanStub{control: fixture.control, principal: fixture.principal}
	action := agentActionDecision()
	action.PlanDraft = &taskstate.Draft{
		Phase: "Approach", MaxReplans: 2,
		Steps: []taskstate.StepDraft{{
			StepID: "step.approach", Title: "Approach", Objective: "Reach the player.",
			CapabilityHints: []host.CapabilityRef{action.Capability}, MaxAttempts: 3,
			SuccessConditions: []taskstate.PlanCondition{{
				ConditionID: "condition.arrived", Kind: taskstate.EvidenceOperationOutcome,
				Summary: "The Host confirms arrival.",
			}},
		}},
	}
	fixture.model.decisions = []cognition.ModelDecision{
		action,
		{Kind: cognition.ModelDecisionComplete, Summary: "The plan is complete."},
	}
	fixture.control.operationAfterSubmit = succeededAgentOperation(fixture.environment.observation)
	runtime, err := cognition.NewAgentRuntime(cognition.AgentRuntimeOptions{
		Principal: fixture.principal, Control: fixture.control, Environment: fixture.environment,
		Persona: fixture.persona, Memory: fixture.memory, Skills: fixture.skills,
		Model: fixture.model, Tasks: fixture.tasks, Decisions: fixture.decisions,
		Plans: plans, Now: fixture.now, MaxAdvancesPerRun: 16,
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := runtime.StartTask(context.Background(), cognition.StartTaskInput{
		TaskID: "task.planned", HostID: "host.test", WorldID: "world.test",
		ActorID: "actor.mira", ControllerID: "controller.internal",
		Goal: "Reach the nearby player.", PlanningMode: taskstate.PlanningRequired,
	})
	if err != nil {
		t.Fatal(err)
	}
	completed, err := runtime.RunTask(context.Background(), started.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != cognition.TaskCompleted || completed.PlanID == "" ||
		completed.CurrentPlanStepID != "" || plans.plan.Status != taskstate.PlanCompleted {
		t.Fatalf("completed task = %#v, plan = %#v", completed, plans.plan)
	}
	if len(plans.submissions) != 1 || plans.submissions[0].Action.Request.PlanStep == nil ||
		plans.submissions[0].Action.Request.PlanStep.PlanID != completed.PlanID {
		t.Fatalf("planned submissions = %#v", plans.submissions)
	}
	if len(fixture.model.inputs) != 2 || fixture.model.inputs[0].Plan != nil ||
		fixture.model.inputs[1].Plan == nil ||
		fixture.model.inputs[1].Plan.Status != taskstate.PlanCompleted {
		t.Fatalf("model plan contexts = %#v", fixture.model.inputs)
	}
}

func TestAgentRuntimeCompletesMultiStepTaskThroughControlPlane(t *testing.T) {
	fixture := newAgentRuntimeFixture(t)
	fixture.model.decisions = []cognition.ModelDecision{
		agentActionDecision(),
		{Kind: cognition.ModelDecisionComplete, Summary: "The player has been reached."},
	}
	fixture.control.operationAfterSubmit = succeededAgentOperation(fixture.environment.observation)
	runtime := fixture.runtime(t, 16)
	task := fixture.start(t, runtime, "task.follow")

	completed, err := runtime.RunTask(context.Background(), task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != cognition.TaskCompleted || completed.Step != 1 ||
		completed.ActionCount != 1 || completed.ModelCalls != 2 {
		t.Fatalf("unexpected completed task: %+v", completed)
	}
	if len(fixture.control.submissions) != 1 {
		t.Fatalf("action submission count = %d, want 1", len(fixture.control.submissions))
	}
	request := fixture.control.submissions[0].Request
	if request.Capability.ID != "rin.navigation.move-to" || request.IdempotencyKey != "task.follow.action.1" ||
		len(request.Targets) != 1 || request.Targets[0] != fixture.environment.observation.Resources[0].Ref {
		t.Fatalf("runtime submitted an ungrounded action: %+v", request)
	}
	if fixture.control.releaseCalls != 1 {
		t.Fatalf("completed task did not release its controller: %d", fixture.control.releaseCalls)
	}

	matches, err := fixture.memory.Retrieve(context.Background(), cognition.MemoryQuery{
		SessionID: "session.test", ActorID: "actor.mira", ControllerID: "controller.internal",
		Now:    host.Timepoint{Clock: host.ClockStep, Value: 20},
		Budget: cognition.MemoryBudget{MaxRecords: 10, MaxCharacters: 2_000},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ids := memoryMatchIDs(matches); !containsAllStrings(ids, "task.follow.belief.1.1", "task.follow.outcome.1") {
		t.Fatalf("subjective and Host outcome memories were not both retained: %v", ids)
	}
	for _, match := range matches {
		if match.Record.MemoryID == "task.follow.belief.1.1" && match.Record.Provenance.Authoritative {
			t.Fatal("model belief became authoritative")
		}
		if match.Record.MemoryID == "task.follow.outcome.1" && !match.Record.Provenance.Authoritative {
			t.Fatal("Host outcome lost its authoritative provenance")
		}
	}
}

func TestAgentRuntimeCreatesDraftOnlyAfterVerifiedComplexTask(t *testing.T) {
	fixture := newAgentRuntimeFixture(t)
	fixture.model.decisions = []cognition.ModelDecision{
		agentActionDecision(),
		{Kind: cognition.ModelDecisionComplete, Summary: "The player has been reached."},
	}
	fixture.control.operationAfterSubmit = succeededAgentOperation(fixture.environment.observation)
	drafts := &recordingSkillWriter{}
	generator := &recordingDraftGenerator{}
	runtime, err := cognition.NewAgentRuntime(cognition.AgentRuntimeOptions{
		Principal: fixture.principal, Control: fixture.control, Environment: fixture.environment,
		Persona: fixture.persona, Memory: fixture.memory, Skills: fixture.skills,
		Model: fixture.model, Tasks: fixture.tasks, Decisions: fixture.decisions, Now: fixture.now,
		MaxAdvancesPerRun: 16,
		Learning: &cognition.SkillLearningOptions{
			Generator: generator, Drafts: drafts, Mode: cognition.SkillPublishDraft,
			MinActions: 1, Adapter: "test.adapter",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	task := fixture.start(t, runtime, "task.learn-draft")
	completed, err := runtime.RunTask(context.Background(), task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.SkillLearning == nil ||
		completed.SkillLearning.Status != cognition.SkillLearningDrafted ||
		len(drafts.skills) != 1 || generator.calls != 1 {
		t.Fatalf("learning = %#v, drafts = %#v, calls = %d", completed.SkillLearning, drafts.skills, generator.calls)
	}
	if drafts.skills[0].Adapters[0] != "test.adapter" ||
		!slices.Contains(drafts.skills[0].Capabilities, "rin.navigation.move-to") {
		t.Fatalf("draft scope = %#v", drafts.skills[0])
	}
	if _, err := runtime.RunTask(context.Background(), task.TaskID); err != nil {
		t.Fatal(err)
	}
	if generator.calls != 1 {
		t.Fatalf("completed task regenerated its draft %d times", generator.calls)
	}
}

func TestAgentRuntimeTimelineUsesReferencesAndMeasuredEvidence(t *testing.T) {
	fixture := newAgentRuntimeFixture(t)
	secretMemory := "private memory text must not enter the timeline"
	if _, err := fixture.memory.Append(context.Background(), cognition.MemoryRecord{
		MemoryID: "memory.timeline.private",
		Namespace: cognition.MemoryNamespace{
			SessionID: "session.test", ActorID: "actor.mira",
			Domain: cognition.MemoryActorSemantic,
		},
		Content: secretMemory, Tags: []string{"task.follow"},
		Provenance: cognition.MemoryProvenance{
			Source: cognition.MemorySourcePlayer, SourceID: "source.timeline.private",
		},
		Confidence: 0.9, Importance: 0.8,
		CreatedAt: host.Timepoint{Clock: host.ClockStep, Value: 1},
	}); err != nil {
		t.Fatal(err)
	}
	decision := agentActionDecision()
	decision.ProviderModel = "model.timeline"
	decision.Usage.PromptTokens = 7
	decision.Usage.CompletionTokens = 3
	cacheHit := 5
	cacheMiss := 2
	cacheWrite := 1
	decision.Usage.PromptCacheHitTokens = &cacheHit
	decision.Usage.PromptCacheMissTokens = &cacheMiss
	decision.Usage.CacheWriteTokens = &cacheWrite
	fixture.model.decisions = []cognition.ModelDecision{
		decision,
		{Kind: cognition.ModelDecisionComplete, Summary: "The task is complete.", ProviderModel: "model.timeline"},
	}
	fixture.control.operationAfterSubmit = succeededAgentOperation(fixture.environment.observation)
	runtime := fixture.runtime(t, 16)
	task := fixture.start(t, runtime, "task.timeline.internal")
	before, err := runtime.GetTaskTimeline(context.Background(), timeline.Query{TaskID: task.TaskID})
	if err != nil || len(before.Events) == 0 {
		t.Fatalf("initial timeline = %#v, %v", before, err)
	}

	waited := make(chan timeline.Update, 1)
	failure := make(chan error, 1)
	go func() {
		update, waitErr := runtime.WaitTaskTimeline(context.Background(), timeline.WaitInput{
			TaskID: task.TaskID, AfterCursor: before.NextCursor,
			Limit: 64, WaitMillis: 1_000,
		})
		if waitErr != nil {
			failure <- waitErr
			return
		}
		waited <- update
	}()
	time.Sleep(10 * time.Millisecond)
	if _, err := runtime.RunTask(context.Background(), task.TaskID); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-failure:
		t.Fatalf("WaitTaskTimeline: %v", err)
	case update := <-waited:
		if !update.Changed || len(update.Timeline.Events) == 0 {
			t.Fatalf("timeline update = %#v", update)
		}
	case <-time.After(time.Second):
		t.Fatal("WaitTaskTimeline did not wake")
	}

	page, err := runtime.GetTaskTimeline(context.Background(), timeline.Query{
		TaskID: task.TaskID, Limit: timeline.MaximumLimit,
	})
	if err != nil {
		t.Fatal(err)
	}
	kinds := make([]string, len(page.Events))
	for index, event := range page.Events {
		kinds[index] = event.EventKind
	}
	wantKinds := []string{
		"task.created", "model.decision", "action.selected", "operation.submitted",
		"operation.terminal", "model.decision", "task.completed",
	}
	if !slices.Equal(kinds, wantKinds) {
		t.Fatalf("internal timeline kinds = %v, want %v", kinds, wantKinds)
	}
	var modelEvent *timeline.Event
	for index := range page.Events {
		if page.Events[index].EventKind == "model.decision" &&
			len(page.Events[index].MemoryContextRefs) != 0 {
			modelEvent = &page.Events[index]
			break
		}
	}
	if modelEvent == nil || len(modelEvent.SkillRefs) != 1 || modelEvent.Model == nil ||
		modelEvent.Model.TotalTokens == nil || *modelEvent.Model.TotalTokens != 10 ||
		modelEvent.Model.PromptTokens == nil || *modelEvent.Model.PromptTokens != 7 ||
		modelEvent.Model.CompletionTokens == nil || *modelEvent.Model.CompletionTokens != 3 ||
		modelEvent.Model.CacheHitTokens == nil || *modelEvent.Model.CacheHitTokens != 5 ||
		modelEvent.Model.CacheMissTokens == nil || *modelEvent.Model.CacheMissTokens != 2 ||
		modelEvent.Model.CacheWriteTokens == nil || *modelEvent.Model.CacheWriteTokens != 1 {
		t.Fatalf("model evidence = %#v", modelEvent)
	}
	if modelEvent.MemoryContextRefs[0].MemoryID != "memory.timeline.private" ||
		modelEvent.MemoryContextRefs[0].Digest == "" {
		t.Fatalf("memory reference = %#v", modelEvent.MemoryContextRefs)
	}
	directDigest := sha256.Sum256([]byte(secretMemory))
	if modelEvent.MemoryContextRefs[0].Digest == fmt.Sprintf("sha256:%x", directDigest) {
		t.Fatal("memory reference exposed a dictionary-testable content-only digest")
	}
	payload, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), secretMemory) || strings.Contains(string(payload), "Follow safely.") ||
		strings.Contains(string(payload), "Inspect the observed target") {
		t.Fatalf("timeline leaked memory or skill text: %s", payload)
	}
	decisionSnapshot, err := fixture.decisions.Snapshot(context.Background())
	if err != nil || len(decisionSnapshot.Records) != 2 {
		t.Fatalf("decision records = %#v, %v", decisionSnapshot, err)
	}
	record := decisionSnapshot.Records[0]
	if record.ContextDigest == "" || record.PersonaDigest == "" ||
		len(record.MemoryRefs) != 1 || record.MemoryRefs[0].MemoryID != "memory.timeline.private" ||
		record.Usage.PromptCacheHitTokens == nil || *record.Usage.PromptCacheHitTokens != 5 {
		t.Fatalf("decision record = %#v", record)
	}
	privatePayload, err := json.Marshal(decisionSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		secretMemory, "Follow safely.", "Inspect the observed target", "private memory text",
	} {
		if strings.Contains(string(privatePayload), forbidden) {
			t.Fatalf("decision record leaked %q: %s", forbidden, privatePayload)
		}
	}
}

func TestAgentRuntimeEnforcesTaskCapabilityScopeBeforeModelExecution(t *testing.T) {
	fixture := newAgentRuntimeFixture(t)
	fixture.environment.catalog.Specs = append(
		fixture.environment.catalog.Specs,
		agentMacroCapabilitySpec(t),
	)
	fixture.model.decisions = []cognition.ModelDecision{agentMacroDecision()}
	runtime := fixture.runtime(t, 8)
	started, err := runtime.StartTask(context.Background(), cognition.StartTaskInput{
		TaskID: "task.scoped", HostID: "host.test", WorldID: "world.test",
		ActorID: "actor.mira", ControllerID: "controller.internal",
		Goal: "Use only movement.", Tags: []string{"task.follow"},
		AllowedCapabilities: []string{"rin.navigation.move-to"},
	})
	if err != nil {
		t.Fatal(err)
	}
	paused, err := runtime.RunTask(context.Background(), started.TaskID)
	if err == nil || paused.Status != cognition.TaskPaused ||
		paused.PauseCode != "model.invalid" || len(fixture.control.submissions) != 0 ||
		len(fixture.model.inputs) != 1 || len(fixture.model.inputs[0].Capabilities) != 1 ||
		fixture.model.inputs[0].Capabilities[0].Capability.ID != "rin.navigation.move-to" {
		t.Fatalf("task capability scope was not enforced: task=%+v err=%v inputs=%+v",
			paused, err, fixture.model.inputs)
	}

	emptyFixture := newAgentRuntimeFixture(t)
	emptyRuntime := emptyFixture.runtime(t, 8)
	emptyTask, err := emptyRuntime.StartTask(context.Background(), cognition.StartTaskInput{
		TaskID: "task.scope-empty", HostID: "host.test", WorldID: "world.test",
		ActorID: "actor.mira", ControllerID: "controller.internal",
		Goal:                "Use a capability that is not currently published.",
		AllowedCapabilities: []string{"dialogue.speak"},
	})
	if err != nil {
		t.Fatal(err)
	}
	emptyPaused, err := emptyRuntime.RunTask(context.Background(), emptyTask.TaskID)
	if err == nil || emptyPaused.Status != cognition.TaskPaused ||
		emptyPaused.PauseCode != "capabilities.scope-empty" ||
		len(emptyFixture.model.inputs) != 0 {
		t.Fatalf("empty capability scope reached the model: task=%+v err=%v",
			emptyPaused, err)
	}
}

func TestAgentRuntimeDrivesMacroThroughAuditedChildOperation(t *testing.T) {
	fixture := newAgentRuntimeFixture(t)
	macro := agentMacroCapabilitySpec(t)
	fixture.environment.catalog.Specs = append(
		fixture.environment.catalog.Specs,
		macro,
	)
	fixture.model.decisions = []cognition.ModelDecision{
		agentMacroDecision(),
		agentActionDecision(),
		{Kind: cognition.ModelDecisionComplete, Summary: "The collection macro finished."},
	}
	macroAccepted := queuedAgentOperation()
	macroAccepted.OperationID = "operation.agent.macro"
	macroAccepted.Status = controlplane.OperationAccepted
	macroAccepted.Cursor = "cursor.macro.accepted"
	macroAccepted.DeliveryAttempts = 1
	macroSucceeded := succeededAgentOperationWithID(
		fixture.environment.observation,
		"operation.agent.macro",
		"The Host completed the bounded macro.",
	)
	childSucceeded := succeededAgentOperationWithID(
		fixture.environment.observation,
		"operation.agent.child",
		"The Host completed the authorized child action.",
	)
	fixture.control.submissionResults = []controlplane.OperationView{
		{OperationID: "operation.agent.macro"},
		{OperationID: "operation.agent.child"},
	}
	fixture.control.operationSequences = map[string][]controlplane.OperationView{
		"operation.agent.macro": {macroAccepted, macroAccepted, macroSucceeded},
		"operation.agent.child": {childSucceeded},
	}
	runtime := fixture.runtime(t, 24)
	started := fixture.start(t, runtime, "task.macro")

	completed, err := runtime.RunTask(context.Background(), started.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != cognition.TaskCompleted || completed.MacroOperationID != "" ||
		completed.PendingAction != nil || completed.Step != 2 ||
		completed.ActionCount != 2 || completed.ModelCalls != 3 {
		t.Fatalf("macro task did not complete through its child: %+v", completed)
	}
	if len(fixture.control.submissions) != 2 ||
		fixture.control.submissions[0].ParentOperationID != "" ||
		fixture.control.submissions[1].ParentOperationID != "operation.agent.macro" ||
		fixture.control.submissions[0].Request.TaskID != started.TaskID ||
		fixture.control.submissions[1].Request.TaskID != started.TaskID {
		t.Fatalf("macro parent/child submissions are not linked: %+v", fixture.control.submissions)
	}
	if len(fixture.model.inputs) != 3 ||
		fixture.model.inputs[0].Task.ParentOperationID != "" ||
		fixture.model.inputs[1].Task.ParentOperationID != "operation.agent.macro" ||
		len(fixture.model.inputs[1].Capabilities) != 1 ||
		fixture.model.inputs[1].Capabilities[0].Kind != host.CapabilityAtomic ||
		fixture.model.inputs[2].Task.ParentOperationID != "" {
		t.Fatalf("model did not receive the bounded macro contract: %+v", fixture.model.inputs)
	}
	if !historyHasKind(completed.History, "macro.started") ||
		!historyHasKind(completed.History, "macro.terminal") {
		t.Fatalf("macro lifecycle is missing audit events: %+v", completed.History)
	}
}

func TestAgentRuntimeRestoresActiveMacroBeforeSelectingChild(t *testing.T) {
	fixture := newAgentRuntimeFixture(t)
	fixture.environment.catalog.Specs = append(
		fixture.environment.catalog.Specs,
		agentMacroCapabilitySpec(t),
	)
	fixture.model.decisions = []cognition.ModelDecision{agentMacroDecision()}
	macroAccepted := queuedAgentOperation()
	macroAccepted.OperationID = "operation.agent.restore-macro"
	macroAccepted.Status = controlplane.OperationRunning
	macroAccepted.Cursor = "cursor.restore.running"
	macroAccepted.DeliveryAttempts = 1
	fixture.control.submissionResults = []controlplane.OperationView{{
		OperationID: "operation.agent.restore-macro",
	}}
	fixture.control.operationSequences = map[string][]controlplane.OperationView{
		"operation.agent.restore-macro": {macroAccepted},
	}
	firstRuntime := fixture.runtime(t, 3)
	started := fixture.start(t, firstRuntime, "task.restore-macro")
	active, err := firstRuntime.RunTask(context.Background(), started.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if active.MacroOperationID != "operation.agent.restore-macro" ||
		active.PendingAction != nil || active.Step != 1 {
		t.Fatalf("macro did not reach a restorable boundary: %+v", active)
	}
	snapshot, err := fixture.tasks.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	restored, err := cognition.RestoreLocalTaskStore(10, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	fixture.tasks = restored
	fixture.model.decisions = []cognition.ModelDecision{agentActionDecision()}
	restarted := fixture.runtime(t, 1)
	selected, err := restarted.RunTask(context.Background(), started.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if selected.MacroOperationID != "operation.agent.restore-macro" ||
		selected.PendingAction == nil || selected.PendingActionMacro ||
		len(fixture.model.inputs) != 2 ||
		fixture.model.inputs[1].Task.ParentOperationID != selected.MacroOperationID {
		t.Fatalf("restored macro lost its child boundary: %+v", selected)
	}
}

func TestAgentRuntimeCancelsMacroChildBeforeParent(t *testing.T) {
	fixture := newAgentRuntimeFixture(t)
	fixture.environment.catalog.Specs = append(
		fixture.environment.catalog.Specs,
		agentMacroCapabilitySpec(t),
	)
	fixture.model.decisions = []cognition.ModelDecision{
		agentMacroDecision(), agentActionDecision(),
	}
	macroRunning := queuedAgentOperation()
	macroRunning.OperationID = "operation.agent.cancel-macro"
	macroRunning.Status = controlplane.OperationRunning
	macroRunning.Cursor = "cursor.cancel-macro.running"
	macroRunning.DeliveryAttempts = 1
	childRunning := queuedAgentOperation()
	childRunning.OperationID = "operation.agent.cancel-child"
	childRunning.Status = controlplane.OperationRunning
	childRunning.Cursor = "cursor.cancel-child.running"
	childRunning.DeliveryAttempts = 1
	fixture.control.submissionResults = []controlplane.OperationView{
		{OperationID: macroRunning.OperationID},
		{OperationID: childRunning.OperationID},
	}
	fixture.control.operationSequences = map[string][]controlplane.OperationView{
		macroRunning.OperationID: {macroRunning},
		childRunning.OperationID: {childRunning},
	}
	runtime := fixture.runtime(t, 5)
	started := fixture.start(t, runtime, "task.cancel-macro")
	pending, err := runtime.RunTask(context.Background(), started.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if pending.MacroOperationID != macroRunning.OperationID ||
		pending.PendingOperationID != childRunning.OperationID {
		t.Fatalf("macro child did not reach running state: %+v", pending)
	}
	fixture.control.cancelSequences = map[string][]controlplane.OperationView{
		childRunning.OperationID: {cancelledAgentOperationWithID(
			fixture.environment.observation, childRunning.OperationID,
		)},
		macroRunning.OperationID: {cancelledAgentOperationWithID(
			fixture.environment.observation, macroRunning.OperationID,
		)},
	}
	cancelled, err := runtime.CancelTask(context.Background(), started.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != cognition.TaskCancelled || cancelled.MacroOperationID != "" ||
		cancelled.PendingOperationID != "" || fixture.control.releaseCalls != 1 ||
		!reflect.DeepEqual(fixture.control.cancelledOperationIDs, []string{
			childRunning.OperationID, macroRunning.OperationID,
		}) {
		t.Fatalf("macro cancellation was not child-before-parent: task=%+v calls=%v",
			cancelled, fixture.control.cancelledOperationIDs)
	}
}

func TestAgentRuntimeKeepsCancellingWhenPendingMacroStarts(t *testing.T) {
	fixture := newAgentRuntimeFixture(t)
	fixture.environment.catalog.Specs = append(
		fixture.environment.catalog.Specs,
		agentMacroCapabilitySpec(t),
	)
	fixture.model.decisions = []cognition.ModelDecision{agentMacroDecision()}
	fixture.control.submissionResults = []controlplane.OperationView{{
		OperationID: "operation.agent.starting-macro",
	}}
	runtime := fixture.runtime(t, 2)
	started := fixture.start(t, runtime, "task.cancel-starting-macro")
	pending, err := runtime.RunTask(context.Background(), started.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if pending.PendingOperationID != "operation.agent.starting-macro" ||
		!pending.PendingActionMacro || pending.MacroOperationID != "" {
		t.Fatalf("macro did not remain pending before cancellation: %+v", pending)
	}

	running := queuedAgentOperation()
	running.OperationID = pending.PendingOperationID
	running.Status = controlplane.OperationRunning
	running.Cursor = "cursor.starting-macro.running"
	running.DeliveryAttempts = 1
	fixture.control.cancelSequences = map[string][]controlplane.OperationView{
		running.OperationID: {
			running,
			cancelledAgentOperationWithID(fixture.environment.observation, running.OperationID),
		},
	}
	cancelled, err := runtime.CancelTask(context.Background(), started.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != cognition.TaskCancelled ||
		cancelled.MacroOperationID != "" || cancelled.PendingAction != nil ||
		!reflect.DeepEqual(fixture.control.cancelledOperationIDs, []string{
			running.OperationID, running.OperationID,
		}) {
		t.Fatalf("starting macro escaped cancellation: task=%+v calls=%v",
			cancelled, fixture.control.cancelledOperationIDs)
	}
}

func TestAgentRuntimeKeepsUnknownMacroForReconciliation(t *testing.T) {
	fixture := newAgentRuntimeFixture(t)
	fixture.environment.catalog.Specs = append(
		fixture.environment.catalog.Specs,
		agentMacroCapabilitySpec(t),
	)
	fixture.model.decisions = []cognition.ModelDecision{agentMacroDecision()}
	macroAccepted := queuedAgentOperation()
	macroAccepted.OperationID = "operation.agent.unknown-macro"
	macroAccepted.Status = controlplane.OperationAccepted
	macroAccepted.Cursor = "cursor.unknown-macro.accepted"
	macroAccepted.DeliveryAttempts = 1
	macroUnknown := macroAccepted
	macroUnknown.Status = controlplane.OperationOutcomeUnknown
	macroUnknown.Cursor = "cursor.unknown-macro.unknown"
	macroUnknown.Terminal = false
	macroUnknown.ReconciliationPending = true
	fixture.control.submissionResults = []controlplane.OperationView{{
		OperationID: macroAccepted.OperationID,
	}}
	fixture.control.operationSequences = map[string][]controlplane.OperationView{
		macroAccepted.OperationID: {macroAccepted, macroUnknown},
	}
	runtime := fixture.runtime(t, 4)
	started := fixture.start(t, runtime, "task.unknown-macro")
	unknown, err := runtime.RunTask(context.Background(), started.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if unknown.Status != cognition.TaskOutcomeUnknown ||
		unknown.MacroOperationID != macroAccepted.OperationID ||
		fixture.control.releaseCalls != 1 || len(fixture.control.submissions) != 1 {
		t.Fatalf("unknown macro lost reconciliation state: %+v", unknown)
	}
}

func TestAgentRuntimeActivatesMacroOnlyAfterConfirmationApproval(t *testing.T) {
	fixture := newAgentRuntimeFixture(t)
	fixture.environment.catalog.Specs = append(
		fixture.environment.catalog.Specs,
		agentMacroCapabilitySpec(t),
	)
	fixture.model.decisions = []cognition.ModelDecision{agentMacroDecision()}
	pending := queuedAgentOperation()
	pending.Status = controlplane.OperationAwaitingConfirmation
	fixture.control.operationAfterSubmit = pending
	runtime := fixture.runtime(t, 8)
	started := fixture.start(t, runtime, "task.confirm-macro")
	waiting, err := runtime.RunTask(context.Background(), started.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Status != cognition.TaskWaitingConfirmation ||
		waiting.MacroOperationID != "" || waiting.PendingAction == nil ||
		!waiting.PendingActionMacro {
		t.Fatalf("unapproved macro became active: %+v", waiting)
	}

	approved := queuedAgentOperation()
	approved.Status = controlplane.OperationAccepted
	approved.Cursor = "cursor.confirm-macro.accepted"
	approved.DeliveryAttempts = 1
	fixture.control.operationAfterSubmit = approved
	afterApproval := fixture.runtime(t, 1)
	active, err := afterApproval.RunTask(context.Background(), started.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if active.Status != cognition.TaskActive ||
		active.MacroOperationID != approved.OperationID ||
		active.PendingAction != nil || active.PendingActionMacro {
		t.Fatalf("approved macro did not enter the durable parent state: %+v", active)
	}
}

func TestAgentRuntimePausesInsteadOfOrphaningMacroAtBudgetLimit(t *testing.T) {
	fixture := newAgentRuntimeFixture(t)
	fixture.environment.catalog.Specs = append(
		fixture.environment.catalog.Specs,
		agentMacroCapabilitySpec(t),
	)
	fixture.model.decisions = []cognition.ModelDecision{
		agentMacroDecision(), agentActionDecision(),
	}
	macroRunning := queuedAgentOperation()
	macroRunning.OperationID = "operation.agent.budget-macro"
	macroRunning.Status = controlplane.OperationRunning
	macroRunning.Cursor = "cursor.budget-macro.running"
	macroRunning.DeliveryAttempts = 1
	fixture.control.submissionResults = []controlplane.OperationView{{
		OperationID: macroRunning.OperationID,
	}}
	fixture.control.operationSequences = map[string][]controlplane.OperationView{
		macroRunning.OperationID: {macroRunning},
	}
	runtime := fixture.runtime(t, 8)
	started, err := runtime.StartTask(context.Background(), cognition.StartTaskInput{
		TaskID: "task.budget-macro", HostID: "host.test", WorldID: "world.test",
		ActorID: "actor.mira", ControllerID: "controller.internal",
		Goal: "Run one bounded macro.", Tags: []string{"task.follow"},
		Budget: cognition.TaskBudget{MaxActions: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	paused, err := runtime.RunTask(context.Background(), started.TaskID)
	if !errors.Is(err, cognition.ErrTaskBudgetExceeded) ||
		paused.Status != cognition.TaskPaused || paused.PauseCode != "budget.actions" ||
		paused.MacroOperationID != macroRunning.OperationID ||
		len(fixture.control.submissions) != 1 || fixture.control.releaseCalls != 0 {
		t.Fatalf("budget limit orphaned macro: task=%+v err=%v", paused, err)
	}
	fixture.control.cancelSequences = map[string][]controlplane.OperationView{
		macroRunning.OperationID: {cancelledAgentOperationWithID(
			fixture.environment.observation, macroRunning.OperationID,
		)},
	}
	cancelled, err := runtime.CancelTask(context.Background(), started.TaskID)
	if err != nil || cancelled.Status != cognition.TaskCancelled ||
		cancelled.MacroOperationID != "" || fixture.control.releaseCalls != 1 {
		t.Fatalf("paused budget macro could not be cancelled: task=%+v err=%v", cancelled, err)
	}
}

func TestAgentRuntimeRejectsUnboundActorBeforeControllerSideEffects(t *testing.T) {
	fixture := newAgentRuntimeFixture(t)
	runtime := fixture.runtime(t, 1)
	_, err := runtime.StartTask(context.Background(), cognition.StartTaskInput{
		TaskID: "task.unbound", HostID: "host.test", WorldID: "world.test",
		ActorID: "actor.unbound", ControllerID: "controller.internal",
		Goal: "Attempt an unconfigured role.",
	})
	if !errors.Is(err, cognition.ErrProviderNotFound) {
		t.Fatalf("unbound task error = %v", err)
	}
	if fixture.control.acquireCalls != 0 {
		t.Fatalf("unbound task acquired %d controller leases", fixture.control.acquireCalls)
	}
}

func TestAgentRuntimeReplaysExactPendingActionAfterRestore(t *testing.T) {
	fixture := newAgentRuntimeFixture(t)
	fixture.model.decisions = []cognition.ModelDecision{agentActionDecision()}
	firstRuntime := fixture.runtime(t, 1)
	started := fixture.start(t, firstRuntime, "task.replay")
	pending, err := firstRuntime.RunTask(context.Background(), started.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if pending.PendingAction == nil || pending.PendingOperationID != "" || len(fixture.control.submissions) != 0 {
		t.Fatalf("first bounded run did not stop before submission: %+v", pending)
	}
	exact := *pending.PendingAction
	snapshot, err := fixture.tasks.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	restored, err := cognition.RestoreLocalTaskStore(10, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	fixture.tasks = restored
	fixture.control.operationAfterSubmit = queuedAgentOperation()
	resumedRuntime := fixture.runtime(t, 1)
	resumed, err := resumedRuntime.RunTask(context.Background(), started.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(fixture.control.submissions) != 1 || resumed.PendingOperationID == "" {
		t.Fatalf("restored task did not submit its pending action: %+v", resumed)
	}
	if !reflect.DeepEqual(fixture.control.submissions[0].Request, exact) {
		t.Fatalf("restored request changed:\n got: %+v\nwant: %+v", fixture.control.submissions[0].Request, exact)
	}
}

func TestAgentRuntimeDoesNotTreatQueuedOperationAsSuccess(t *testing.T) {
	fixture := newAgentRuntimeFixture(t)
	fixture.model.decisions = []cognition.ModelDecision{agentActionDecision()}
	fixture.control.operationAfterSubmit = queuedAgentOperation()
	runtime := fixture.runtime(t, 8)
	started := fixture.start(t, runtime, "task.queued")
	current, err := runtime.RunTask(context.Background(), started.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != cognition.TaskActive || current.PendingOperationID == "" || current.Step != 0 {
		t.Fatalf("queued operation was treated as terminal: %+v", current)
	}
	if historyHasKind(current.History, "operation.terminal") {
		t.Fatal("queued operation produced terminal audit evidence")
	}
	matches, err := fixture.memory.Retrieve(context.Background(), cognition.MemoryQuery{
		SessionID: "session.test", ActorID: "actor.mira", Now: host.Timepoint{Clock: host.ClockStep, Value: 20},
		Budget: cognition.MemoryBudget{MaxRecords: 10, MaxCharacters: 2_000},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("queued operation created shared outcome memory: %+v", matches)
	}
}

func TestAgentRuntimeRecordsStableActionGatewayRejectionCodes(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		code string
	}{
		{name: "stale", err: controlplane.ErrStale, code: "gateway.stale"},
		{name: "lease expired", err: controlplane.ErrLeaseExpired, code: "gateway.lease-expired"},
		{name: "forbidden", err: controlplane.ErrForbidden, code: "gateway.forbidden"},
		{name: "invalid", err: controlplane.ErrInvalid, code: "gateway.invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAgentRuntimeFixture(t)
			fixture.model.decisions = []cognition.ModelDecision{agentActionDecision()}
			fixture.control.submitError = test.err
			runtime := fixture.runtime(t, 4)
			started := fixture.start(
				t, runtime, "task.rejected."+strings.ReplaceAll(test.name, " ", "-"),
			)
			current, err := runtime.RunTask(context.Background(), started.TaskID)
			if !errors.Is(err, test.err) || !historyHasCode(current.History, test.code) ||
				current.PendingAction != nil || current.PendingOperationID != "" {
				t.Fatalf("gateway rejection was not classified: task=%+v err=%v", current, err)
			}
		})
	}
}

func TestAgentRuntimeStopsOnOutcomeUnknownWithoutDuplicateSubmission(t *testing.T) {
	fixture := newAgentRuntimeFixture(t)
	fixture.model.decisions = []cognition.ModelDecision{agentActionDecision()}
	unknown := queuedAgentOperation()
	unknown.Status = controlplane.OperationOutcomeUnknown
	unknown.Terminal = false
	unknown.ReconciliationPending = true
	unknown.DeliveryAttempts = 1
	fixture.control.operationAfterSubmit = unknown
	runtime := fixture.runtime(t, 8)
	started := fixture.start(t, runtime, "task.unknown")
	current, err := runtime.RunTask(context.Background(), started.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != cognition.TaskOutcomeUnknown || current.PendingAction == nil ||
		current.PendingOperationID == "" {
		t.Fatalf("outcome-unknown task lost reconciliation state: %+v", current)
	}
	if _, err := runtime.RunTask(context.Background(), started.TaskID); err != nil {
		t.Fatal(err)
	}
	if len(fixture.control.submissions) != 1 {
		t.Fatalf("outcome-unknown action was duplicated: %d submissions", len(fixture.control.submissions))
	}
}

func TestAgentRuntimePausesBeforeActionWhenModelFails(t *testing.T) {
	fixture := newAgentRuntimeFixture(t)
	fixture.model.err = errors.New("provider offline")
	runtime := fixture.runtime(t, 8)
	started := fixture.start(t, runtime, "task.model-failure")
	paused, err := runtime.RunTask(context.Background(), started.TaskID)
	if err == nil {
		t.Fatal("model failure was hidden")
	}
	if paused.Status != cognition.TaskPaused || paused.PauseCode != "model.unavailable" ||
		len(fixture.control.submissions) != 0 || paused.PendingAction != nil {
		t.Fatalf("model failure reached action execution: %+v", paused)
	}
}

func TestAgentRuntimeDegradesMemoryAndSkillsWithoutBlockingDecision(t *testing.T) {
	fixture := newAgentRuntimeFixture(t)
	fixture.model.decisions = []cognition.ModelDecision{{
		Kind: cognition.ModelDecisionWait, Summary: "Wait for a clearer signal.",
	}}
	runtime, err := cognition.NewAgentRuntime(cognition.AgentRuntimeOptions{
		Principal: fixture.principal, Control: fixture.control, Environment: fixture.environment,
		Persona: fixture.persona, Memory: failingMemoryProvider{}, Skills: failingSkillProvider{},
		Model: fixture.model, Tasks: fixture.tasks, Now: fixture.now,
		MaxAdvancesPerRun: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	started := fixture.start(t, runtime, "task.degraded")
	current, err := runtime.RunTask(context.Background(), started.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != cognition.TaskActive || current.Step != 1 ||
		!historyHasCode(current.History, "memory.degraded") ||
		!historyHasCode(current.History, "skills.degraded") {
		t.Fatalf("optional provider failures did not degrade cleanly: %+v", current)
	}
	if len(fixture.model.inputs) != 1 || len(fixture.model.inputs[0].Memories) != 0 ||
		len(fixture.model.inputs[0].Skills) != 0 {
		t.Fatalf("failed optional provider data reached the model: %+v", fixture.model.inputs)
	}
}

func TestAgentRuntimeStoresWaitDecisionMemoryAsPrivateBelief(t *testing.T) {
	fixture := newAgentRuntimeFixture(t)
	fixture.model.decisions = []cognition.ModelDecision{{
		Kind: cognition.ModelDecisionWait, Summary: "Give the player some space.",
		MemoryCandidates: []cognition.ModelMemoryCandidate{{
			Content: "The player may prefer quiet company.", Tags: []string{"player.nearby"},
			SubjectHandles: []string{"target.0"}, Confidence: 0.5, Importance: 0.4,
		}},
	}}
	runtime := fixture.runtime(t, 8)
	started := fixture.start(t, runtime, "task.wait-memory")
	if _, err := runtime.RunTask(context.Background(), started.TaskID); err != nil {
		t.Fatal(err)
	}
	matches, err := fixture.memory.Retrieve(context.Background(), cognition.MemoryQuery{
		SessionID: "session.test", ActorID: "actor.mira", ControllerID: "controller.internal",
		Now:    host.Timepoint{Clock: host.ClockStep, Value: 20},
		Budget: cognition.MemoryBudget{MaxRecords: 10, MaxCharacters: 1_000},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Record.Namespace.Domain != cognition.MemoryControllerBelief ||
		matches[0].Record.Provenance.Authoritative {
		t.Fatalf("wait hypothesis was not stored as a private belief: %+v", matches)
	}
}

func TestAgentRuntimeExpandsOneInspectionRound(t *testing.T) {
	fixture := newAgentRuntimeFixture(t)
	fixture.model.decisions = []cognition.ModelDecision{
		{
			Kind: cognition.ModelDecisionInspect, Summary: "Inspect movement details.",
			InspectCapabilities: []host.CapabilityRef{{ID: "rin.navigation.move-to", Version: "2.0.0"}},
			InspectSkills:       []cognition.SkillRef{{SkillID: "skill.follow", Version: "v1"}},
		},
		{Kind: cognition.ModelDecisionWait, Summary: "Wait after inspection."},
	}
	runtime := fixture.runtime(t, 8)
	started := fixture.start(t, runtime, "task.inspect")
	current, err := runtime.RunTask(context.Background(), started.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Step != 1 || len(fixture.model.inputs) != 2 ||
		fixture.model.inputs[1].InspectionRound != 1 ||
		len(fixture.model.inputs[1].InspectedCapabilities) != 1 ||
		len(fixture.model.inputs[1].InspectedSkills) != 1 {
		t.Fatalf("progressive inspection was not applied: task=%+v inputs=%+v", current, fixture.model.inputs)
	}
}

func TestAgentRuntimeLeavesConfirmationStateAfterExternalApproval(t *testing.T) {
	fixture := newAgentRuntimeFixture(t)
	fixture.model.decisions = []cognition.ModelDecision{agentActionDecision()}
	pending := queuedAgentOperation()
	pending.Status = controlplane.OperationAwaitingConfirmation
	fixture.control.operationAfterSubmit = pending
	runtime := fixture.runtime(t, 8)
	started := fixture.start(t, runtime, "task.confirmation")
	waiting, err := runtime.RunTask(context.Background(), started.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.Status != cognition.TaskWaitingConfirmation {
		t.Fatalf("task did not enter confirmation state: %+v", waiting)
	}

	fixture.control.operationAfterSubmit = queuedAgentOperation()
	current, err := runtime.RunTask(context.Background(), started.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != cognition.TaskActive || current.PendingOperationID == "" {
		t.Fatalf("approved operation left task stuck in confirmation: %+v", current)
	}
}

func TestAgentRuntimeRejectsUnadvertisedSkillInspection(t *testing.T) {
	fixture := newAgentRuntimeFixture(t)
	fixture.model.decisions = []cognition.ModelDecision{{
		Kind: cognition.ModelDecisionInspect, Summary: "Inspect a hidden skill.",
		InspectSkills: []cognition.SkillRef{{SkillID: "skill.hidden", Version: "v1"}},
	}}
	runtime := fixture.runtime(t, 8)
	started := fixture.start(t, runtime, "task.hidden-skill")
	paused, err := runtime.RunTask(context.Background(), started.TaskID)
	if err == nil || paused.Status != cognition.TaskPaused || paused.PauseCode != "model.invalid" {
		t.Fatalf("unadvertised skill inspection was accepted: task=%+v err=%v", paused, err)
	}
}

func TestAgentRuntimeDoesNotPersistCallerCancellationAsProviderFailure(t *testing.T) {
	fixture := newAgentRuntimeFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	fixture.model = &scriptedModelProvider{cancel: cancel}
	runtime := fixture.runtime(t, 8)
	started := fixture.start(t, runtime, "task.cancelled-call")
	returned, err := runtime.RunTask(ctx, started.TaskID)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected caller cancellation, got %v", err)
	}
	stored, loadErr := runtime.GetTask(context.Background(), started.TaskID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if returned.Status != cognition.TaskActive || stored.Status != cognition.TaskActive ||
		stored.ModelCalls != 0 || stored.PauseCode != "" {
		t.Fatalf("caller cancellation was persisted as a provider failure: returned=%+v stored=%+v", returned, stored)
	}
}

func TestAgentRuntimeCancelsPendingActionBeforeSubmission(t *testing.T) {
	fixture := newAgentRuntimeFixture(t)
	fixture.model.decisions = []cognition.ModelDecision{agentActionDecision()}
	runtime := fixture.runtime(t, 1)
	started := fixture.start(t, runtime, "task.cancel-before-submit")
	pending, err := runtime.RunTask(context.Background(), started.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if pending.PendingAction == nil || pending.PendingOperationID != "" {
		t.Fatalf("task did not stop before submission: %+v", pending)
	}
	cancelled, err := runtime.CancelTask(context.Background(), started.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != cognition.TaskCancelled || cancelled.PendingAction != nil ||
		cancelled.PendingOperationID != "" || fixture.control.cancelCalls != 0 ||
		fixture.control.releaseCalls != 1 {
		t.Fatalf("local cancellation did not settle safely: %+v", cancelled)
	}
	again, err := runtime.CancelTask(context.Background(), started.TaskID)
	if err != nil || again.Revision != cancelled.Revision || fixture.control.releaseCalls != 1 {
		t.Fatalf("repeated cancellation was not idempotent: task=%+v err=%v", again, err)
	}
}

func TestAgentRuntimeCancelsOwnedPlanWithPendingAction(t *testing.T) {
	fixture := newAgentRuntimeFixture(t)
	plans := &runtimePlanStub{control: fixture.control, principal: fixture.principal}
	action := agentActionDecision()
	action.PlanDraft = &taskstate.Draft{
		Phase: "Approach",
		Steps: []taskstate.StepDraft{{
			StepID: "step.approach", Title: "Approach", Objective: "Reach the player.",
			CapabilityHints: []host.CapabilityRef{action.Capability}, MaxAttempts: 3,
			SuccessConditions: []taskstate.PlanCondition{{
				ConditionID: "condition.arrived", Kind: taskstate.EvidenceOperationOutcome,
				Summary: "The Host confirms arrival.",
			}},
		}},
	}
	fixture.model.decisions = []cognition.ModelDecision{action}
	fixture.plans = plans
	runtime := fixture.runtime(t, 1)
	started, err := runtime.StartTask(context.Background(), cognition.StartTaskInput{
		TaskID: "task.cancel-plan", HostID: "host.test", WorldID: "world.test",
		ActorID: "actor.mira", ControllerID: "controller.internal",
		Goal: "Reach the nearby player.", PlanningMode: taskstate.PlanningRequired,
	})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := runtime.RunTask(context.Background(), started.TaskID)
	if err != nil || pending.PlanID == "" || pending.PendingAction == nil {
		t.Fatalf("planned task did not stop before submission: task=%+v err=%v", pending, err)
	}
	cancelled, err := runtime.CancelTask(context.Background(), started.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != cognition.TaskCancelled || plans.plan.Status != taskstate.PlanCancelled ||
		cancelled.PlanRevision != plans.plan.Revision || plans.statusUpdates != 1 {
		t.Fatalf("task and plan cancellation diverged: task=%+v plan=%+v", cancelled, plans.plan)
	}
}

func TestAgentRuntimePersistsCancellationUntilHostOutcome(t *testing.T) {
	fixture := newAgentRuntimeFixture(t)
	fixture.model.decisions = []cognition.ModelDecision{agentActionDecision()}
	running := queuedAgentOperation()
	running.Status = controlplane.OperationRunning
	running.Cursor = "cursor.running"
	running.DeliveryAttempts = 1
	fixture.control.operationAfterSubmit = running
	fixture.control.cancelResult = running
	runtime := fixture.runtime(t, 2)
	started := fixture.start(t, runtime, "task.cancel-running")
	pending, err := runtime.RunTask(context.Background(), started.TaskID)
	if err != nil || pending.PendingOperationID == "" {
		t.Fatalf("submit running action: task=%+v err=%v", pending, err)
	}
	cancelling, err := runtime.CancelTask(context.Background(), started.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelling.Status != cognition.TaskCancelling || cancelling.PendingOperationID == "" ||
		fixture.control.cancelCalls != 1 || fixture.control.releaseCalls != 0 {
		t.Fatalf("delivered action cancellation was reported as complete: %+v", cancelling)
	}

	snapshot, err := fixture.tasks.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	restored, err := cognition.RestoreLocalTaskStore(10, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	fixture.tasks = restored
	settled := cancelledAgentOperation(fixture.environment.observation)
	fixture.control.cancelResult = settled
	fixture.control.operationAfterSubmit = settled
	restarted := fixture.runtime(t, 4)
	cancelled, err := restarted.RunTask(context.Background(), started.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != cognition.TaskCancelled || cancelled.Step != 1 ||
		cancelled.PendingOperationID != "" || fixture.control.cancelCalls != 2 ||
		fixture.control.releaseCalls != 1 ||
		!historyHasKind(cancelled.History, "operation.terminal") ||
		!historyHasKind(cancelled.History, "task.cancelled") {
		t.Fatalf("restarted cancellation did not settle from Host evidence: %+v", cancelled)
	}
}

func TestAgentRuntimeCancelsUndeliveredQueuedOperation(t *testing.T) {
	fixture := newAgentRuntimeFixture(t)
	fixture.model.decisions = []cognition.ModelDecision{agentActionDecision()}
	fixture.control.operationAfterSubmit = queuedAgentOperation()
	runtime := fixture.runtime(t, 2)
	started := fixture.start(t, runtime, "task.cancel-queued")
	pending, err := runtime.RunTask(context.Background(), started.TaskID)
	if err != nil || pending.PendingOperationID == "" {
		t.Fatalf("submit queued action: task=%+v err=%v", pending, err)
	}
	queuedCancellation := queuedAgentOperation()
	queuedCancellation.Status = controlplane.OperationCancelled
	queuedCancellation.Terminal = true
	queuedCancellation.Cursor = "cursor.cancelled"
	fixture.control.cancelResult = queuedCancellation
	cancelled, err := runtime.CancelTask(context.Background(), started.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != cognition.TaskCancelled || cancelled.Step != 1 ||
		cancelled.PendingOperationID != "" || fixture.control.releaseCalls != 1 {
		t.Fatalf("undelivered operation did not cancel locally: %+v", cancelled)
	}
}

func TestAgentRuntimeDoesNotClaimDeliveredCancellationWithoutOutcome(t *testing.T) {
	fixture := newAgentRuntimeFixture(t)
	fixture.model.decisions = []cognition.ModelDecision{agentActionDecision()}
	fixture.control.operationAfterSubmit = queuedAgentOperation()
	runtime := fixture.runtime(t, 2)
	started := fixture.start(t, runtime, "task.cancel-unknown")
	pending, err := runtime.RunTask(context.Background(), started.TaskID)
	if err != nil || pending.PendingOperationID == "" {
		t.Fatalf("submit action: task=%+v err=%v", pending, err)
	}
	unknown := queuedAgentOperation()
	unknown.Status = controlplane.OperationCancelled
	unknown.Terminal = true
	unknown.DeliveryAttempts = 1
	unknown.Cursor = "cursor.cancelled"
	fixture.control.cancelResult = unknown
	current, err := runtime.CancelTask(context.Background(), started.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != cognition.TaskOutcomeUnknown || current.PendingOperationID == "" ||
		fixture.control.releaseCalls != 1 {
		t.Fatalf("unproven delivered cancellation was treated as settled: %+v", current)
	}
}

type agentRuntimeFixture struct {
	principal   host.Principal
	control     *fakeAgentControlPlane
	environment *fakeAgentEnvironment
	persona     *cognition.LocalPersonaProvider
	memory      *cognition.LocalMemoryProvider
	skills      *cognition.LocalSkillProvider
	model       *scriptedModelProvider
	tasks       *cognition.LocalTaskStore
	decisions   *cognition.LocalDecisionRecorder
	plans       taskstate.PlanClient
	now         func() time.Time
}

type runtimePlanStub struct {
	control       *fakeAgentControlPlane
	principal     host.Principal
	plan          taskstate.PlanState
	submissions   []taskstate.SubmitStepActionInput
	advanced      bool
	statusUpdates int
}

func (client *runtimePlanStub) CreatePlan(_ context.Context, input taskstate.Draft) (taskstate.PlanState, error) {
	plan, err := taskstate.NewPlan(input, 2_000)
	client.plan = plan
	return plan, err
}

func (client *runtimePlanStub) GetPlan(context.Context, string) (taskstate.PlanState, error) {
	if len(client.submissions) != 0 && !client.advanced {
		evidence := taskstate.PlanEvidence{
			EvidenceID: "evidence.arrived", ConditionID: "condition.arrived",
			Kind: taskstate.EvidenceOperationOutcome, OperationID: "operation.agent.1",
			Epoch: client.plan.BasedOnEpoch, ObservationSequence: 2,
			Digest: strings.Repeat("a", 64), RecordedAtUnixMillis: 2_001,
		}
		plan, _, err := taskstate.ApplyEvidence(client.plan, evidence, 2_001)
		if err != nil {
			return taskstate.PlanState{}, err
		}
		client.plan = plan
		client.advanced = true
	}
	return client.plan, nil
}

func (client *runtimePlanStub) WaitPlan(context.Context, taskstate.WaitInput) (taskstate.PlanUpdate, error) {
	return taskstate.PlanUpdate{Plan: client.plan}, nil
}

func (client *runtimePlanStub) RevisePlan(context.Context, taskstate.ReviseInput) (taskstate.PlanState, error) {
	return taskstate.PlanState{}, taskstate.ErrConflict
}

func (client *runtimePlanStub) SetPlanStatus(_ context.Context, input taskstate.StatusInput) (taskstate.PlanState, error) {
	if input.PlanID != client.plan.PlanID || input.ExpectedRevision != client.plan.Revision ||
		input.Status != taskstate.PlanCancelled {
		return taskstate.PlanState{}, taskstate.ErrConflict
	}
	client.plan.Status = taskstate.PlanCancelled
	client.plan.CurrentStepID = ""
	client.plan.Revision++
	for index := range client.plan.Steps {
		if client.plan.Steps[index].Status != taskstate.StepCompleted {
			client.plan.Steps[index].Status = taskstate.StepSkipped
		}
	}
	client.statusUpdates++
	return client.plan, nil
}

func (client *runtimePlanStub) RequestTransition(context.Context, taskstate.TransitionInput) (taskstate.PlanState, error) {
	return taskstate.PlanState{}, taskstate.ErrForbidden
}

func (client *runtimePlanStub) SubmitStepAction(
	ctx context.Context,
	input taskstate.SubmitStepActionInput,
) (controlplane.OperationView, error) {
	client.submissions = append(client.submissions, input)
	return client.control.SubmitAction(ctx, client.principal, input.Action)
}

func newAgentRuntimeFixture(t *testing.T) *agentRuntimeFixture {
	t.Helper()
	input := modelV2Input(t)
	spec := agentCapabilitySpec(t)
	environment := &fakeAgentEnvironment{
		observation: input.Observation,
		catalog:     host.CapabilitySnapshot{Revision: 1, Specs: []host.CapabilitySpec{spec}},
	}
	lease := controlplane.ControllerLease{
		LeaseID: "lease.agent", ControllerID: "controller.internal", PrincipalID: "principal.internal",
		HostID: "host.test", WorldID: "world.test", ActorID: "actor.mira",
		Source: controlplane.DecisionInternal, PersonaMode: controlplane.PersonaCharacterBound,
		AuthorityRevision: 1, Epoch: input.Observation.Epoch,
		AcquiredAtUnixMillis: 1_000, ExpiresAtUnixMillis: 61_000,
	}
	control := &fakeAgentControlPlane{
		actor: controlplane.ActorView{
			HostID: "host.test", WorldID: "world.test", ActorID: "actor.mira",
			OwnerPrincipalID: "principal.internal", DisplayName: "Mira",
			ObservationSeq: input.Observation.Sequence, Epoch: input.Observation.Epoch,
			Authority: controlplane.DecisionAuthority{
				Source: controlplane.DecisionInternal, Revision: 1,
				PersonaMode: controlplane.PersonaCharacterBound,
			},
			Online: true, LeaseExpiresAtMillis: 61_000,
		},
		lease: lease,
	}
	persona, err := cognition.NewLocalPersonaProvider(
		[]cognition.PersonaProfile{{
			PersonaID: "persona.mira", Version: "v1", Identity: "Mira is a careful companion.",
		}},
		[]cognition.PersonaBinding{{
			ActorID: "actor.mira", PersonaID: "persona.mira", Version: "v1",
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	memory, err := cognition.NewLocalMemoryProvider(cognition.LocalMemoryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	skill, err := cognition.SealSkill(cognition.Skill{
		SkillSummary: cognition.SkillSummary{
			SkillID: "skill.follow", Version: "v1", Summary: "Follow safely.",
			Triggers: []string{"task.follow"}, Source: "builtin",
		},
		Instructions: "Inspect the observed target before moving.",
	})
	if err != nil {
		t.Fatal(err)
	}
	skills, err := cognition.NewLocalSkillProvider([]cognition.Skill{skill})
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := cognition.NewLocalTaskStore(10)
	if err != nil {
		t.Fatal(err)
	}
	decisions, err := cognition.NewLocalDecisionRecorder(32)
	if err != nil {
		t.Fatal(err)
	}
	nowValue := time.UnixMilli(2_000)
	return &agentRuntimeFixture{
		principal: host.Principal{ID: "principal.internal", GrantedScopes: []string{
			controlplane.ScopeActorRead, controlplane.ScopeActorControl, controlplane.ScopeActorExecute,
		}},
		control: control, environment: environment, persona: persona, memory: memory,
		skills: skills, model: &scriptedModelProvider{}, tasks: tasks,
		decisions: decisions,
		now:       func() time.Time { return nowValue },
	}
}

func (fixture *agentRuntimeFixture) runtime(t *testing.T, advances uint32) *cognition.AgentRuntime {
	t.Helper()
	runtime, err := cognition.NewAgentRuntime(cognition.AgentRuntimeOptions{
		Principal: fixture.principal, Control: fixture.control, Environment: fixture.environment,
		Persona: fixture.persona, Memory: fixture.memory, Skills: fixture.skills,
		Model: fixture.model, Tasks: fixture.tasks, Decisions: fixture.decisions, Now: fixture.now,
		Plans:             fixture.plans,
		MaxAdvancesPerRun: advances,
	})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func (fixture *agentRuntimeFixture) start(
	t *testing.T,
	runtime *cognition.AgentRuntime,
	taskID string,
) cognition.TaskSession {
	t.Helper()
	task, err := runtime.StartTask(context.Background(), cognition.StartTaskInput{
		TaskID: taskID, HostID: "host.test", WorldID: "world.test", ActorID: "actor.mira",
		ControllerID: "controller.internal", Goal: "Follow the nearby player.",
		Tags: []string{"task.follow"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return task
}

type scriptedModelProvider struct {
	decisions []cognition.ModelDecision
	err       error
	inputs    []cognition.ModelInput
	cancel    context.CancelFunc
}

type recordingDraftGenerator struct {
	calls int
}

func (generator *recordingDraftGenerator) Generate(
	_ context.Context,
	request experience.DraftRequest,
) (experience.SkillDraft, error) {
	generator.calls++
	if request.Episode.VerifiedResult == nil || !request.Episode.VerifiedResult.Success {
		return experience.SkillDraft{}, errors.New("episode is not verified")
	}
	capabilities := []string(nil)
	for _, event := range request.Episode.Events {
		if event.Evidence.Capability != nil {
			capabilities = append(capabilities, event.Evidence.Capability.ID)
		}
	}
	capabilities = slices.Compact(capabilities)
	return experience.SkillDraft{
		SkillID: request.SkillID, Version: "v1", Description: "Follow a nearby player",
		Instructions: "Observe the player, move using current capabilities, and verify the outcome.",
		Triggers:     request.Episode.Tags, Adapters: []string{request.Adapter},
		Capabilities: capabilities,
	}, nil
}

type recordingSkillWriter struct {
	skills []cognition.Skill
}

func (writer *recordingSkillWriter) Save(_ context.Context, skill cognition.Skill) error {
	writer.skills = append(writer.skills, skill)
	return nil
}

func (model *scriptedModelProvider) Decide(
	ctx context.Context,
	input cognition.ModelInput,
) (cognition.ModelDecision, error) {
	if err := ctx.Err(); err != nil {
		return cognition.ModelDecision{}, err
	}
	model.inputs = append(model.inputs, input)
	if model.cancel != nil {
		model.cancel()
		return cognition.ModelDecision{}, ctx.Err()
	}
	if model.err != nil {
		return cognition.ModelDecision{}, model.err
	}
	if len(model.decisions) == 0 {
		return cognition.ModelDecision{}, errors.New("no scripted model decision")
	}
	decision := model.decisions[0]
	model.decisions = model.decisions[1:]
	decision.Usage.TotalTokens = 10
	return decision, nil
}

func (model *scriptedModelProvider) Health(ctx context.Context) cognition.ProviderHealth {
	return cognition.ProviderHealth{Available: ctx != nil && ctx.Err() == nil}
}

type fakeAgentEnvironment struct {
	observation host.ObservationEnvelope
	catalog     host.CapabilitySnapshot
}

func (environment *fakeAgentEnvironment) Observe(
	ctx context.Context,
	query host.ObservationQuery,
) (host.ObservationEnvelope, error) {
	if err := ctx.Err(); err != nil {
		return host.ObservationEnvelope{}, err
	}
	if query.ExpectedEpoch != environment.observation.Epoch {
		return host.ObservationEnvelope{}, errors.New("stale observation query")
	}
	return environment.observation, nil
}

func (environment *fakeAgentEnvironment) Capabilities(
	ctx context.Context,
	target controlplane.ActorControlTarget,
) (host.CapabilitySnapshot, error) {
	if err := ctx.Err(); err != nil {
		return host.CapabilitySnapshot{}, err
	}
	return environment.catalog, nil
}

type fakeAgentControlPlane struct {
	actor                 controlplane.ActorView
	lease                 controlplane.ControllerLease
	operationAfterSubmit  controlplane.OperationView
	submissionResults     []controlplane.OperationView
	operationSequences    map[string][]controlplane.OperationView
	submissions           []controlplane.SubmitActionInput
	cancelResult          controlplane.OperationView
	cancelSequences       map[string][]controlplane.OperationView
	cancelledOperationIDs []string
	cancelCalls           int
	releaseCalls          int
	acquireCalls          int
	submitError           error
}

func (control *fakeAgentControlPlane) GetActor(
	principal host.Principal,
	hostID, worldID, actorID string,
) (controlplane.ActorView, error) {
	return control.actor, nil
}

func (control *fakeAgentControlPlane) AcquireController(
	principal host.Principal,
	input controlplane.AcquireControllerInput,
) (controlplane.ControllerLease, error) {
	control.acquireCalls++
	return control.lease, nil
}

func (control *fakeAgentControlPlane) RenewController(
	principal host.Principal,
	target controlplane.ActorControlTarget,
	leaseID string,
	ttl uint32,
) (controlplane.ControllerLease, error) {
	return control.lease, nil
}

func (control *fakeAgentControlPlane) ReleaseController(
	principal host.Principal,
	target controlplane.ActorControlTarget,
	leaseID string,
) error {
	control.releaseCalls++
	return nil
}

func (control *fakeAgentControlPlane) SubmitAction(
	ctx context.Context,
	principal host.Principal,
	input controlplane.SubmitActionInput,
) (controlplane.OperationView, error) {
	if err := ctx.Err(); err != nil {
		return controlplane.OperationView{}, err
	}
	index := len(control.submissions)
	control.submissions = append(control.submissions, input)
	if control.submitError != nil {
		return controlplane.OperationView{}, control.submitError
	}
	if index < len(control.submissionResults) {
		view := control.submissionResults[index]
		queued := queuedAgentOperation()
		queued.OperationID = view.OperationID
		return queued, nil
	}
	view := control.operationAfterSubmit
	if view.OperationID == "" {
		view = queuedAgentOperation()
	}
	queued := queuedAgentOperation()
	queued.OperationID = view.OperationID
	return queued, nil
}

func (control *fakeAgentControlPlane) GetOperation(
	principal host.Principal,
	operationID string,
) (controlplane.OperationView, error) {
	if sequence := control.operationSequences[operationID]; len(sequence) != 0 {
		view := sequence[0]
		if len(sequence) > 1 {
			control.operationSequences[operationID] = sequence[1:]
		}
		view.OperationID = operationID
		return view, nil
	}
	view := control.operationAfterSubmit
	if view.OperationID == "" {
		view = queuedAgentOperation()
	}
	view.OperationID = operationID
	return view, nil
}

func (control *fakeAgentControlPlane) WaitOperation(
	ctx context.Context,
	principal host.Principal,
	input controlplane.WaitOperationInput,
) (controlplane.OperationUpdate, error) {
	view, err := control.GetOperation(principal, input.OperationID)
	if err != nil {
		return controlplane.OperationUpdate{}, err
	}
	return controlplane.OperationUpdate{Operation: view, Changed: view.Cursor != input.AfterCursor}, nil
}

func (control *fakeAgentControlPlane) CancelOperation(
	principal host.Principal,
	operationID string,
) (controlplane.OperationView, error) {
	control.cancelCalls++
	control.cancelledOperationIDs = append(control.cancelledOperationIDs, operationID)
	if sequence := control.cancelSequences[operationID]; len(sequence) != 0 {
		view := sequence[0]
		if len(sequence) > 1 {
			control.cancelSequences[operationID] = sequence[1:]
		}
		view.OperationID = operationID
		return view, nil
	}
	view := control.cancelResult
	if view.OperationID == "" {
		view = control.operationAfterSubmit
	}
	if view.OperationID == "" {
		view = queuedAgentOperation()
	}
	view.OperationID = operationID
	return view, nil
}

type failingMemoryProvider struct{}

func (failingMemoryProvider) Append(context.Context, cognition.MemoryRecord) (cognition.MemoryRecord, error) {
	return cognition.MemoryRecord{}, errors.New("memory unavailable")
}
func (failingMemoryProvider) Retrieve(context.Context, cognition.MemoryQuery) ([]cognition.MemoryMatch, error) {
	return nil, errors.New("memory unavailable")
}
func (failingMemoryProvider) Consolidate(context.Context, cognition.MemoryConsolidation) (cognition.MemoryRecord, error) {
	return cognition.MemoryRecord{}, errors.New("memory unavailable")
}
func (failingMemoryProvider) Forget(context.Context, cognition.MemoryForgetRequest) error {
	return errors.New("memory unavailable")
}
func (failingMemoryProvider) Snapshot(context.Context) (cognition.MemorySnapshot, error) {
	return cognition.MemorySnapshot{}, errors.New("memory unavailable")
}
func (failingMemoryProvider) Health(context.Context) cognition.ProviderHealth {
	return cognition.ProviderHealth{Degraded: true, Code: "memory.unavailable"}
}

type failingSkillProvider struct{}

func (failingSkillProvider) ListSkills(context.Context, cognition.SkillQuery) ([]cognition.SkillSummary, error) {
	return nil, errors.New("skills unavailable")
}
func (failingSkillProvider) DescribeSkill(context.Context, string, string) (cognition.Skill, error) {
	return cognition.Skill{}, errors.New("skills unavailable")
}
func (failingSkillProvider) Health(context.Context) cognition.ProviderHealth {
	return cognition.ProviderHealth{Degraded: true, Code: "skills.unavailable"}
}

func agentActionDecision() cognition.ModelDecision {
	return cognition.ModelDecision{
		Kind:       cognition.ModelDecisionAction,
		Capability: host.CapabilityRef{ID: "rin.navigation.move-to", Version: "2.0.0"},
		Arguments:  json.RawMessage(`{"distance":2}`), TargetHandles: []string{"target.0"},
		Summary: "Move toward the nearby player.",
		MemoryCandidates: []cognition.ModelMemoryCandidate{{
			Content: "The player may want company.", Tags: []string{"player.nearby"},
			SubjectHandles: []string{"target.0"}, Confidence: 0.6, Importance: 0.4, TTLSteps: 20,
		}},
	}
}

func queuedAgentOperation() controlplane.OperationView {
	return controlplane.OperationView{
		OperationID: "operation.agent.1", Status: controlplane.OperationQueued,
		Cursor: "cursor.1", Terminal: false,
	}
}

func succeededAgentOperation(observation host.ObservationEnvelope) controlplane.OperationView {
	return succeededAgentOperationWithID(
		observation,
		"operation.agent.1",
		"The Host moved the companion near the player.",
	)
}

func succeededAgentOperationWithID(
	observation host.ObservationEnvelope,
	operationID string,
	summary string,
) controlplane.OperationView {
	return controlplane.OperationView{
		OperationID: operationID, Status: controlplane.OperationSucceeded,
		Cursor: "cursor.2", Terminal: true, ExecutionConfirmed: true, DeliveryAttempts: 1,
		Outcome: &host.ActionOutcome{
			OperationID: operationID, Status: host.ActionSucceeded,
			Summary:  summary,
			Evidence: []host.HostRef{observation.Resources[0].Ref}, Epoch: observation.Epoch,
			WorldSeq: 2, OccurredAt: host.Timepoint{Clock: host.ClockStep, Value: 12},
		},
	}
}

func cancelledAgentOperation(observation host.ObservationEnvelope) controlplane.OperationView {
	return cancelledAgentOperationWithID(observation, "operation.agent.1")
}

func cancelledAgentOperationWithID(
	observation host.ObservationEnvelope,
	operationID string,
) controlplane.OperationView {
	return controlplane.OperationView{
		OperationID: operationID, Status: controlplane.OperationCancelled,
		Cursor: "cursor.cancelled", Terminal: true, DeliveryAttempts: 1,
		Outcome: &host.ActionOutcome{
			OperationID: operationID, Status: host.ActionCancelled,
			Summary: "The Host stopped the companion action.", Epoch: observation.Epoch,
			WorldSeq: 2, OccurredAt: host.Timepoint{Clock: host.ClockStep, Value: 12},
		},
	}
}

func agentCapabilitySpec(t *testing.T) host.CapabilitySpec {
	t.Helper()
	input, err := host.NewSchema([]byte(`{
      "$schema":"https://json-schema.org/draft/2020-12/schema",
      "type":"object",
      "properties":{"distance":{"type":"integer","minimum":1,"maximum":16}},
      "required":["distance"],
      "additionalProperties":false
    }`))
	if err != nil {
		t.Fatal(err)
	}
	output, err := host.NewSchema([]byte(`{
      "$schema":"https://json-schema.org/draft/2020-12/schema",
      "type":"object",
      "properties":{"state":{"type":"string"}},
      "required":["state"],
      "additionalProperties":false
    }`))
	if err != nil {
		t.Fatal(err)
	}
	effects, err := host.NewSchema([]byte(`{
      "$schema":"https://json-schema.org/draft/2020-12/schema",
      "type":"object",
      "properties":{"distance":{"type":"integer","minimum":0}},
      "required":["distance"],
      "additionalProperties":false
    }`))
	if err != nil {
		t.Fatal(err)
	}
	spec, err := host.SealCapabilitySpec(host.CapabilitySpec{
		Capability:  host.CapabilityRef{ID: "rin.navigation.move-to", Version: "2.0.0"},
		Description: "Move toward one observed target.", Input: input, Output: output,
		EffectSchema: effects, Kind: host.CapabilityAtomic, Execution: host.ExecutionLongRunning,
		Cancellation: host.CancellationCooperative, RiskFloor: host.RiskLow,
		RequiredDurability: host.DurabilityAdvisory,
		ExecutionBudget:    host.Duration{Clock: host.ClockStep, Value: 20},
		MaxInputBytes:      1_024, MaxOutputBytes: 1_024, MaxEffects: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func agentMacroCapabilitySpec(t *testing.T) host.CapabilitySpec {
	t.Helper()
	spec := agentCapabilitySpec(t)
	spec.Capability = host.CapabilityRef{
		ID: "rin.task.collect-resource", Version: "2.0.0",
	}
	spec.Description = "Collect a bounded resource goal through child actions."
	spec.Kind = host.CapabilityMacro
	spec.ProducesChildOperations = true
	spec.Digest = ""
	sealed, err := host.SealCapabilitySpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	return sealed
}

func agentMacroDecision() cognition.ModelDecision {
	return cognition.ModelDecision{
		Kind: cognition.ModelDecisionAction,
		Capability: host.CapabilityRef{
			ID: "rin.task.collect-resource", Version: "2.0.0",
		},
		Arguments:     json.RawMessage(`{"distance":2}`),
		TargetHandles: []string{"target.0"},
		Summary:       "Start a bounded collection macro.",
	}
}

func historyHasKind(history []cognition.TaskEvent, kind string) bool {
	for _, event := range history {
		if event.Kind == kind {
			return true
		}
	}
	return false
}

func historyHasCode(history []cognition.TaskEvent, code string) bool {
	for _, event := range history {
		if event.Code == code {
			return true
		}
	}
	return false
}

func containsAllStrings(values []string, wanted ...string) bool {
	for _, value := range wanted {
		if !slicesContains(values, value) {
			return false
		}
	}
	return true
}

func slicesContains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

var _ cognition.AgentControlPlane = (*fakeAgentControlPlane)(nil)
var _ cognition.AgentEnvironment = (*fakeAgentEnvironment)(nil)
var _ cognition.ModelProvider = (*scriptedModelProvider)(nil)
var _ cognition.MemoryProvider = failingMemoryProvider{}
var _ cognition.SkillProvider = failingSkillProvider{}
