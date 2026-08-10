package cognition_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
)

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

func TestAgentRuntimeStopsOnOutcomeUnknownWithoutDuplicateSubmission(t *testing.T) {
	fixture := newAgentRuntimeFixture(t)
	fixture.model.decisions = []cognition.ModelDecision{agentActionDecision()}
	unknown := queuedAgentOperation()
	unknown.Status = controlplane.OperationOutcomeUnknown
	unknown.Terminal = true
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

type agentRuntimeFixture struct {
	principal   host.Principal
	control     *fakeAgentControlPlane
	environment *fakeAgentEnvironment
	persona     *cognition.LocalPersonaProvider
	memory      *cognition.LocalMemoryProvider
	skills      *cognition.LocalSkillProvider
	model       *scriptedModelProvider
	tasks       *cognition.LocalTaskStore
	now         func() time.Time
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
	nowValue := time.UnixMilli(2_000)
	return &agentRuntimeFixture{
		principal: host.Principal{ID: "principal.internal", GrantedScopes: []string{
			controlplane.ScopeActorRead, controlplane.ScopeActorControl, controlplane.ScopeActorExecute,
		}},
		control: control, environment: environment, persona: persona, memory: memory,
		skills: skills, model: &scriptedModelProvider{}, tasks: tasks,
		now: func() time.Time { return nowValue },
	}
}

func (fixture *agentRuntimeFixture) runtime(t *testing.T, advances uint32) *cognition.AgentRuntime {
	t.Helper()
	runtime, err := cognition.NewAgentRuntime(cognition.AgentRuntimeOptions{
		Principal: fixture.principal, Control: fixture.control, Environment: fixture.environment,
		Persona: fixture.persona, Memory: fixture.memory, Skills: fixture.skills,
		Model: fixture.model, Tasks: fixture.tasks, Now: fixture.now,
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
	actor                controlplane.ActorView
	lease                controlplane.ControllerLease
	operationAfterSubmit controlplane.OperationView
	submissions          []controlplane.SubmitActionInput
	releaseCalls         int
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
	control.submissions = append(control.submissions, input)
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
	return controlplane.OperationView{
		OperationID: "operation.agent.1", Status: controlplane.OperationSucceeded,
		Cursor: "cursor.2", Terminal: true, ExecutionConfirmed: true, DeliveryAttempts: 1,
		Outcome: &host.ActionOutcome{
			OperationID: "operation.agent.1", Status: host.ActionSucceeded,
			Summary:  "The Host moved the companion near the player.",
			Evidence: []host.HostRef{observation.Resources[0].Ref}, Epoch: observation.Epoch,
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
