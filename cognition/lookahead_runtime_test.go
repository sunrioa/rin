package cognition_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
)

// The original scripted fixtures are single-threaded. This wrapper makes
// publications and Host reads atomic and returns independent observation data.
type lookaheadTestWorld struct {
	cognition.AgentControlPlane
	mu             sync.Mutex
	fixture        *agentRuntimeFixture
	multipleActors bool
}

func (world *lookaheadTestWorld) GetActor(p host.Principal, h, w, a string) (controlplane.ActorView, error) {
	world.mu.Lock()
	defer world.mu.Unlock()
	view, err := world.fixture.control.GetActor(p, h, w, a)
	if world.multipleActors {
		view.ActorID = a
	}
	return view, err
}
func (world *lookaheadTestWorld) AcquireController(p host.Principal, input controlplane.AcquireControllerInput) (controlplane.ControllerLease, error) {
	world.mu.Lock()
	defer world.mu.Unlock()
	lease, err := world.fixture.control.AcquireController(p, input)
	if world.multipleActors {
		lease.ActorID, lease.LeaseID = input.ActorID, "lease."+input.ActorID
	}
	return lease, err
}
func (world *lookaheadTestWorld) RenewController(p host.Principal, target controlplane.ActorControlTarget, id string, ttl uint32) (controlplane.ControllerLease, error) {
	world.mu.Lock()
	defer world.mu.Unlock()
	lease, err := world.fixture.control.RenewController(p, target, id, ttl)
	if world.multipleActors {
		lease.ActorID, lease.LeaseID = target.ActorID, id
	}
	return lease, err
}
func (world *lookaheadTestWorld) GetOperation(p host.Principal, id string) (controlplane.OperationView, error) {
	world.mu.Lock()
	defer world.mu.Unlock()
	return world.fixture.control.GetOperation(p, id)
}
func (world *lookaheadTestWorld) SubmitAction(ctx context.Context, p host.Principal, input controlplane.SubmitActionInput) (controlplane.OperationView, error) {
	world.mu.Lock()
	defer world.mu.Unlock()
	return world.fixture.control.SubmitAction(ctx, p, input)
}
func (world *lookaheadTestWorld) CancelOperation(p host.Principal, id string) (controlplane.OperationView, error) {
	world.mu.Lock()
	defer world.mu.Unlock()
	return world.fixture.control.CancelOperation(p, id)
}
func (world *lookaheadTestWorld) Observe(ctx context.Context, query host.ObservationQuery) (host.ObservationEnvelope, error) {
	world.mu.Lock()
	defer world.mu.Unlock()
	observation, err := world.fixture.environment.Observe(ctx, query)
	if err != nil {
		return observation, err
	}
	payload, _ := json.Marshal(observation)
	var copy host.ObservationEnvelope
	err = json.Unmarshal(payload, &copy)
	if world.multipleActors {
		copy.ActorID = query.ActorID
	}
	return copy, err
}
func (world *lookaheadTestWorld) Capabilities(ctx context.Context, target controlplane.ActorControlTarget) (host.CapabilitySnapshot, error) {
	world.mu.Lock()
	defer world.mu.Unlock()
	catalog, err := world.fixture.environment.Capabilities(ctx, target)
	if err != nil {
		return catalog, err
	}
	payload, _ := json.Marshal(catalog)
	var copy host.CapabilitySnapshot
	err = json.Unmarshal(payload, &copy)
	return copy, err
}

type lookaheadTestModel struct {
	mu           sync.Mutex
	normal       *scriptedModelProvider
	started      chan cognition.LookaheadInput
	release      chan struct{}
	cancelled    chan struct{}
	delayedExit  chan struct{}
	cancelOnce   sync.Once
	previewCalls atomic.Int32
	reserve      uint64
	draft        cognition.NextStepDraft
	err          error
}

func (model *lookaheadTestModel) Decide(ctx context.Context, input cognition.ModelInput) (cognition.ModelDecision, error) {
	model.mu.Lock()
	defer model.mu.Unlock()
	return model.normal.Decide(ctx, input)
}
func (model *lookaheadTestModel) Health(ctx context.Context) cognition.ProviderHealth {
	return cognition.ProviderHealth{Available: ctx.Err() == nil}
}
func (model *lookaheadTestModel) LookaheadTokenReservation(cognition.LookaheadInput) (uint64, error) {
	return model.reserve, nil
}
func (model *lookaheadTestModel) Lookahead(ctx context.Context, input cognition.LookaheadInput) (cognition.NextStepDraft, error) {
	model.previewCalls.Add(1)
	model.started <- input
	select {
	case <-ctx.Done():
		model.cancelOnce.Do(func() { close(model.cancelled) })
		if model.delayedExit != nil {
			<-model.delayedExit
		}
		return cognition.NextStepDraft{}, ctx.Err()
	case <-model.release:
		return model.draft, model.err
	}
}
func (model *lookaheadTestModel) normalCalls() int {
	model.mu.Lock()
	defer model.mu.Unlock()
	return len(model.normal.inputs)
}

type lookaheadRig struct {
	fixture *agentRuntimeFixture
	world   *lookaheadTestWorld
	model   *lookaheadTestModel
	runtime *cognition.AgentRuntime
	task    cognition.TaskSession
	now     atomic.Int64
}

func newLookaheadRig(t *testing.T, options *cognition.LookaheadOptions, budget cognition.TaskBudget) *lookaheadRig {
	t.Helper()
	fixture := newAgentRuntimeFixture(t)
	fixture.environment.observation.Facts = []host.ObservationFact{{FactID: "next.allowed", Kind: "world.condition", Value: json.RawMessage("false")}}
	first := queuedAgentOperation()
	first.Status, first.Cursor = controlplane.OperationRunning, "cursor.running"
	fixture.control.submissionResults = []controlplane.OperationView{{OperationID: "operation.agent.1"}, {OperationID: "operation.agent.2"}}
	fixture.control.operationSequences = map[string][]controlplane.OperationView{"operation.agent.1": {first}, "operation.agent.2": {queuedAgentOperation()}}
	fixture.model.decisions = []cognition.ModelDecision{agentActionDecision(), {Kind: cognition.ModelDecisionWait, Summary: "Fallback observes changed conditions."}}
	action := agentActionDecision()
	model := &lookaheadTestModel{normal: fixture.model, started: make(chan cognition.LookaheadInput, 4), release: make(chan struct{}), cancelled: make(chan struct{}), reserve: 100,
		draft: cognition.NextStepDraft{Kind: "action", Capability: action.Capability, Arguments: json.RawMessage(`{"distance":3}`), TargetHandles: action.TargetHandles,
			Preconditions: []cognition.LookaheadCondition{{FactID: "next.allowed", FactValueJSON: "true"}}, Summary: "Continue toward the player when allowed.", UsageKnown: true},
	}
	model.draft.Usage.TotalTokens = 7
	world := &lookaheadTestWorld{AgentControlPlane: fixture.control, fixture: fixture}
	rig := &lookaheadRig{fixture: fixture, world: world, model: model}
	rig.now.Store(2_000)
	fixture.now = func() time.Time { return time.UnixMilli(rig.now.Load()) }
	runtime, err := cognition.NewAgentRuntime(cognition.AgentRuntimeOptions{Principal: fixture.principal, Control: world, Environment: world,
		Persona: fixture.persona, Memory: fixture.memory, Skills: fixture.skills, Model: model, Tasks: fixture.tasks, Now: fixture.now, Lookahead: options, MaxAdvancesPerRun: 16})
	if err != nil {
		t.Fatal(err)
	}
	rig.runtime = runtime
	t.Cleanup(runtime.Close)
	rig.task, err = runtime.StartTask(context.Background(), cognition.StartTaskInput{TaskID: "task.lookahead", HostID: "host.test", WorldID: "world.test", ActorID: "actor.mira", ControllerID: "controller.internal",
		Goal: "Follow the nearby player.", Budget: budget, Completion: cognition.TaskCompletionPolicy{Mode: cognition.CompletionModel}})
	if err != nil {
		t.Fatal(err)
	}
	return rig
}

func (rig *lookaheadRig) start(t *testing.T) {
	t.Helper()
	current, err := rig.runtime.RunTask(context.Background(), rig.task.TaskID)
	if err != nil || current.PendingOperationID != "operation.agent.1" || current.Schedule.Kind != cognition.ScheduleOperation {
		t.Fatalf("initial operation did not yield: %#v %v", current, err)
	}
}
func (rig *lookaheadRig) waitStarted(t *testing.T) cognition.LookaheadInput {
	t.Helper()
	select {
	case input := <-rig.model.started:
		return input
	case <-time.After(3 * time.Second):
		current, _ := rig.runtime.GetTask(context.Background(), rig.task.TaskID)
		t.Fatalf("preview did not start: %#v", current.Lookahead)
		return cognition.LookaheadInput{}
	}
}
func waitLookaheadTask(t *testing.T, runtime *cognition.AgentRuntime, id string, predicate func(cognition.TaskSession) bool) cognition.TaskSession {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		changes, _ := runtime.SchedulingEvents()
		task, err := runtime.GetTask(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if predicate(task) {
			return task
		}
		select {
		case <-changes:
		case <-deadline.C:
			t.Fatalf("lookahead did not settle: %#v", task.Lookahead)
		}
	}
}
func (rig *lookaheadRig) ready(t *testing.T) cognition.TaskSession {
	t.Helper()
	rig.start(t)
	rig.waitStarted(t)
	close(rig.model.release)
	return waitLookaheadTask(t, rig.runtime, rig.task.TaskID, func(task cognition.TaskSession) bool {
		return task.Lookahead != nil && task.Lookahead.Status == "ready"
	})
}
func (rig *lookaheadRig) completeFirst(t *testing.T, mutate func(*agentRuntimeFixture)) {
	t.Helper()
	rig.world.mu.Lock()
	defer rig.world.mu.Unlock()
	advanceAgentObservation(rig.fixture)
	rig.fixture.environment.observation.Facts[0].Value = json.RawMessage("true")
	view := succeededAgentOperation(rig.fixture.environment.observation)
	view.Outcome.WorldSeq = rig.fixture.environment.observation.Sequence
	rig.fixture.control.operationSequences["operation.agent.1"] = []controlplane.OperationView{view}
	if mutate != nil {
		mutate(rig.fixture)
	}
}

func TestLookaheadOverlapsExecutionAndAdoptsWithFreshTargetHandles(t *testing.T) {
	rig := newLookaheadRig(t, nil, cognition.TaskBudget{})
	rig.start(t)
	input := rig.waitStarted(t)
	current, err := rig.runtime.GetTask(context.Background(), rig.task.TaskID)
	if err != nil || current.ModelCalls != 2 || current.Lookahead.ReservedTokens != 100 || current.Step != 0 {
		t.Fatalf("preview attempt was not reserved before invocation: %#v %v", current, err)
	}
	if input.OperationID != current.PendingOperationID || input.Context.LastOperationResult != nil {
		t.Fatal("running action was presented as an already completed result")
	}
	if _, err := rig.runtime.RunTask(context.Background(), rig.task.TaskID); err != nil {
		t.Fatal(err)
	}
	if rig.model.normalCalls() != 1 || rig.model.previewCalls.Load() != 1 {
		t.Fatal("operation progress started duplicate inference")
	}
	close(rig.model.release)
	ready := waitLookaheadTask(t, rig.runtime, rig.task.TaskID, func(task cognition.TaskSession) bool { return task.Lookahead.Status == "ready" })
	if ready.ModelTokens != 17 || ready.Lookahead.ReservedTokens != 0 || ready.ActionCount != 1 {
		t.Fatalf("ready candidate charged or executed incorrectly: %#v", ready)
	}
	expectedTargets, err := cognition.ResolveModelTargetHandles(input.Context.Observation, rig.model.draft.TargetHandles)
	if err != nil {
		t.Fatal(err)
	}
	rig.completeFirst(t, func(fixture *agentRuntimeFixture) {
		resource := fixture.environment.observation.Resources[0]
		resource.Ref.Namespace = "aaa.world"
		fixture.environment.observation.Resources = append(fixture.environment.observation.Resources, resource)
	})
	advanced, err := rig.runtime.RunTask(context.Background(), rig.task.TaskID)
	if err != nil || advanced.PendingOperationID != "operation.agent.2" || advanced.Lookahead.Adopted != 1 || rig.model.normalCalls() != 1 {
		t.Fatalf("prepared successor was not used: %#v calls=%d %v", advanced, rig.model.normalCalls(), err)
	}
	rig.world.mu.Lock()
	defer rig.world.mu.Unlock()
	if len(rig.fixture.control.submissions) != 2 {
		t.Fatal("successor bypassed or duplicated the action gateway")
	}
	request := rig.fixture.control.submissions[1].Request
	if request.ObservationSeq != rig.fixture.environment.observation.Sequence || len(request.Targets) != len(expectedTargets) || request.Targets[0] != expectedTargets[0] || string(request.Arguments) != `{"distance":3}` {
		t.Fatalf("candidate reused stale binding data: %#v", request)
	}
}

func TestLookaheadDiscardsChangedConditionsAndReturnsToNormalDecision(t *testing.T) {
	for _, kind := range []string{"fact", "target", "capability", "failed", "epoch", "stale-observation", "expired", "signal"} {
		t.Run(kind, func(t *testing.T) {
			rig := newLookaheadRig(t, nil, cognition.TaskBudget{})
			rig.ready(t)
			if kind == "signal" {
				input := actorSignal(rig.fixture, "signal.changed", false)
				result, err := rig.runtime.HandleActorSignal(context.Background(), input)
				if err != nil || result.Status != "attached" {
					t.Fatalf("signal did not attach: %#v %v", result, err)
				}
			}
			rig.completeFirst(t, func(fixture *agentRuntimeFixture) {
				switch kind {
				case "fact":
					fixture.environment.observation.Facts[0].Value = json.RawMessage("false")
				case "target":
					fixture.environment.observation.Resources = nil
				case "capability":
					spec := fixture.environment.catalog.Specs[0]
					spec.Description += " Updated contract."
					spec.Digest = ""
					var err error
					spec, err = host.SealCapabilitySpec(spec)
					if err != nil {
						t.Fatal(err)
					}
					fixture.environment.catalog.Specs = []host.CapabilitySpec{spec}
				case "failed":
					view := fixture.control.operationSequences["operation.agent.1"][0]
					view.Status, view.Outcome.Status = controlplane.OperationFailed, host.ActionFailed
					view.ExecutionConfirmed = false
					fixture.control.operationSequences["operation.agent.1"][0] = view
				case "epoch":
					fixture.environment.observation.Epoch.Timeline++
					for index := range fixture.environment.observation.Resources {
						fixture.environment.observation.Resources[index].Ref.Epoch = fixture.environment.observation.Epoch
					}
					fixture.control.actor.Epoch, fixture.control.lease.Epoch = fixture.environment.observation.Epoch, fixture.environment.observation.Epoch
				case "stale-observation":
					fixture.control.operationSequences["operation.agent.1"][0].Outcome.WorldSeq++
				case "expired":
					rig.now.Store(63_000)
					fixture.control.lease.ExpiresAtUnixMillis = 123_000
				}
			})
			current, err := rig.runtime.RunTask(context.Background(), rig.task.TaskID)
			if err != nil || current.Schedule.Kind != cognition.ScheduleObservation || current.Lookahead.Adopted != 0 || current.Lookahead.Discarded != 1 || rig.model.normalCalls() != 2 {
				t.Fatalf("changed candidate was not discarded: %#v calls=%d %v", current, rig.model.normalCalls(), err)
			}
		})
	}
}

func TestLookaheadCancellationDoesNotWaitForProviderAndRetainsUnknownUsage(t *testing.T) {
	rig := newLookaheadRig(t, nil, cognition.TaskBudget{})
	rig.model.delayedExit = make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-rig.model.delayedExit:
		default:
			close(rig.model.delayedExit)
		}
	})
	rig.start(t)
	rig.waitStarted(t)
	rig.world.mu.Lock()
	rig.fixture.control.cancelResult = cancelledAgentOperation(rig.fixture.environment.observation)
	rig.world.mu.Unlock()
	returned := make(chan error, 1)
	go func() { _, err := rig.runtime.CancelTask(context.Background(), rig.task.TaskID); returned <- err }()
	select {
	case err := <-returned:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation waited for speculative inference")
	}
	select {
	case <-rig.model.cancelled:
	case <-time.After(time.Second):
		t.Fatal("preview context was not cancelled")
	}
	close(rig.model.delayedExit)
	settled := waitLookaheadTask(t, rig.runtime, rig.task.TaskID, func(task cognition.TaskSession) bool { return task.Lookahead.ReservedTokens == 0 })
	if settled.Status != cognition.TaskCancelled || settled.Lookahead.Discarded != 1 || settled.ModelTokens != 110 || settled.ModelCalls != 2 {
		t.Fatalf("cancelled preview lost accounting or revived the task: %#v", settled)
	}
}

func TestLookaheadNotReadyFallsBackWithoutWaitingAndLateResultCannotReplaceIt(t *testing.T) {
	rig := newLookaheadRig(t, nil, cognition.TaskBudget{})
	rig.start(t)
	rig.waitStarted(t)
	rig.completeFirst(t, nil)
	current, err := rig.runtime.RunTask(context.Background(), rig.task.TaskID)
	if err != nil || current.Schedule.Kind != cognition.ScheduleObservation || current.PendingAction != nil || rig.model.normalCalls() != 2 {
		t.Fatalf("fallback was blocked: %#v %v", current, err)
	}
	settled := waitLookaheadTask(t, rig.runtime, rig.task.TaskID, func(task cognition.TaskSession) bool { return task.Lookahead.ReservedTokens == 0 })
	if settled.Lookahead.Adopted != 0 || settled.Lookahead.Discarded != 1 || settled.ModelCalls != 3 || settled.ModelTokens != 120 {
		t.Fatalf("late completion replaced fallback or lost cost: %#v", settled)
	}
}

func TestLookaheadTimeoutDoesNotPauseAnExecutingTask(t *testing.T) {
	rig := newLookaheadRig(t, &cognition.LookaheadOptions{TimeoutMillis: 100, DraftTTLMillis: 1000}, cognition.TaskBudget{})
	rig.start(t)
	rig.waitStarted(t)
	current := waitLookaheadTask(t, rig.runtime, rig.task.TaskID, func(task cognition.TaskSession) bool {
		return task.Lookahead.Status == "discarded" && task.Lookahead.ReservedTokens == 0
	})
	if current.Status != cognition.TaskActive || current.PendingOperationID == "" || current.Schedule.Kind != cognition.ScheduleOperation {
		t.Fatalf("optional timeout interrupted execution: %#v", current)
	}
	rig.completeFirst(t, nil)
	if _, err := rig.runtime.RunTask(context.Background(), rig.task.TaskID); err != nil {
		t.Fatal(err)
	}
}

func TestLookaheadLeavesBudgetForNormalFallback(t *testing.T) {
	for _, budget := range []cognition.TaskBudget{{MaxModelCalls: 2}, {MaxModelTokens: 150}} {
		t.Run(fmt.Sprintf("%d-%d", budget.MaxModelCalls, budget.MaxModelTokens), func(t *testing.T) {
			rig := newLookaheadRig(t, nil, budget)
			rig.start(t)
			current := waitLookaheadTask(t, rig.runtime, rig.task.TaskID, func(task cognition.TaskSession) bool {
				return task.Lookahead != nil && task.Lookahead.Status == "discarded"
			})
			if current.ModelCalls != 1 || current.ModelTokens != 10 || rig.model.previewCalls.Load() != 0 {
				t.Fatalf("insufficient budget still invoked preview: %#v", current)
			}
			rig.completeFirst(t, nil)
			current, err := rig.runtime.RunTask(context.Background(), rig.task.TaskID)
			if err != nil || current.Schedule.Kind != cognition.ScheduleObservation || current.ModelCalls != 2 {
				t.Fatalf("fallback budget was consumed: %#v %v", current, err)
			}
		})
	}
}

func TestLookaheadRestartRetainsReservationAndOriginalOperation(t *testing.T) {
	rig := newLookaheadRig(t, nil, cognition.TaskBudget{})
	rig.start(t)
	rig.waitStarted(t)
	crashImage, err := rig.runtime.GetTask(context.Background(), rig.task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	// Restore the durable crash image into a separate database. The old runtime
	// remains isolated so its eventual cancellation cannot settle this image.
	path := filepath.Join(t.TempDir(), "tasks.db")
	store, err := cognition.OpenSQLiteTaskStore(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	crashImage.Revision = 0
	if _, err := store.Create(context.Background(), crashImage); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = cognition.OpenSQLiteTaskStore(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	restarted, err := cognition.NewAgentRuntime(cognition.AgentRuntimeOptions{Principal: rig.fixture.principal, Control: rig.world, Environment: rig.world,
		Persona: rig.fixture.persona, Memory: rig.fixture.memory, Model: rig.model, Tasks: store, Now: rig.fixture.now, Lookahead: &cognition.LookaheadOptions{Disabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restarted.Close)
	current, err := restarted.RunTask(context.Background(), rig.task.TaskID)
	if err != nil || current.PendingOperationID != crashImage.PendingOperationID || current.Lookahead.ReservedTokens != 0 || current.ModelCalls != 2 || current.ModelTokens != 110 || current.Lookahead.Discarded != 1 {
		t.Fatalf("restart lost reservation or operation identity: %#v %v", current, err)
	}
	rig.world.mu.Lock()
	submissions := len(rig.fixture.control.submissions)
	rig.world.mu.Unlock()
	if submissions != 1 || rig.model.previewCalls.Load() != 1 {
		t.Fatal("restart resubmitted execution or speculation")
	}
}

func TestLookaheadReportedBudgetOverrunIsRecordedAndStopsFurtherDecision(t *testing.T) {
	rig := newLookaheadRig(t, nil, cognition.TaskBudget{MaxModelTokens: 250})
	rig.model.draft.Usage.TotalTokens = 300
	rig.start(t)
	rig.waitStarted(t)
	close(rig.model.release)
	current := waitLookaheadTask(t, rig.runtime, rig.task.TaskID, func(task cognition.TaskSession) bool {
		return task.Lookahead.Status == "discarded" && task.Lookahead.ReservedTokens == 0
	})
	if current.ModelTokens != 310 || current.Status != cognition.TaskActive {
		t.Fatalf("provider overrun was hidden or lost the running action: %#v", current)
	}
	rig.completeFirst(t, nil)
	current, err := rig.runtime.RunTask(context.Background(), rig.task.TaskID)
	if !errors.Is(err, cognition.ErrTaskBudgetExceeded) || current.Status != cognition.TaskFailed || rig.model.normalCalls() != 1 {
		t.Fatalf("budget overrun allowed more decisions: %#v %v", current, err)
	}
}

func TestLookaheadUsageSettlementSurvivesInvalidMetadata(t *testing.T) {
	for _, kind := range []string{"model-label", "unicode-model-label", "negative-cache", "oversized-cache", "underreported-total"} {
		t.Run(kind, func(t *testing.T) {
			rig := newLookaheadRig(t, nil, cognition.TaskBudget{})
			expectedTokens, expectedStatus := uint64(17), "ready"
			switch kind {
			case "model-label":
				rig.model.draft.ProviderModel = strings.Repeat("x", 201)
			case "unicode-model-label":
				rig.model.draft.ProviderModel = strings.Repeat("中", 67)
			case "negative-cache":
				count := -1
				rig.model.draft.Usage.PromptCacheHitTokens = &count
				expectedTokens, expectedStatus = 110, "discarded"
			case "oversized-cache":
				count := int(uint64(1) << 53)
				rig.model.draft.Usage.PromptCacheMissTokens = &count
				expectedTokens, expectedStatus = 110, "discarded"
			case "underreported-total":
				rig.model.draft.Usage.PromptTokens, rig.model.draft.Usage.CompletionTokens = 8, 5
				expectedTokens = 23
			}
			rig.start(t)
			rig.waitStarted(t)
			close(rig.model.release)
			settled := waitLookaheadTask(t, rig.runtime, rig.task.TaskID, func(task cognition.TaskSession) bool {
				return task.Lookahead.ReservedTokens == 0
			})
			if settled.ModelTokens != expectedTokens || settled.Lookahead.Status != expectedStatus || settled.PendingOperationID != "operation.agent.1" {
				t.Fatalf("usage metadata blocked accounting or execution: %#v", settled)
			}
			if !historyHasKind(settled.History, "lookahead.usage") {
				t.Fatal("usage settlement lost its timeline receipt")
			}
		})
	}
}

func TestLookaheadUpgradePreservesOlderTaskAcceptanceAndSchedule(t *testing.T) {
	store, err := cognition.NewLocalTaskStore(10)
	if err != nil {
		t.Fatal(err)
	}
	task := validTaskSession("task.before-lookahead")
	task.Completion = cognition.TaskCompletionPolicy{Mode: cognition.CompletionHuman}
	created, err := store.Create(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"rin.cognition.tasks/v4", "rin.cognition.tasks/v5", cognition.TaskSnapshotVersion} {
		t.Run(version, func(t *testing.T) {
			snapshot.Version = version
			restored, err := cognition.RestoreLocalTaskStore(10, snapshot)
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := restored.Load(context.Background(), task.TaskID)
			if err != nil || loaded.Lookahead != nil || loaded.Completion.Mode != cognition.CompletionHuman || loaded.Schedule.Kind != created.Schedule.Kind {
				t.Fatalf("upgrade changed existing task policy or schedule: %#v %v", loaded, err)
			}
		})
	}
}

func TestLookaheadConcurrencyLimitLeavesOtherActorsExecuting(t *testing.T) {
	rig := newLookaheadRig(t, &cognition.LookaheadOptions{MaxConcurrent: 1}, cognition.TaskBudget{})
	rig.world.multipleActors = true
	personas, err := rig.fixture.persona.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	personas.Bindings = append(personas.Bindings, cognition.PersonaBinding{ActorID: "actor.two", PersonaID: "persona.mira", Version: "v1"})
	if _, err := rig.fixture.persona.CompareAndSwap(context.Background(), personas); err != nil {
		t.Fatal(err)
	}
	rig.start(t)
	rig.waitStarted(t)
	rig.model.mu.Lock()
	rig.model.normal.decisions = append([]cognition.ModelDecision{agentActionDecision()}, rig.model.normal.decisions...)
	rig.model.mu.Unlock()
	rig.world.mu.Lock()
	running := queuedAgentOperation()
	running.Status, running.Cursor = controlplane.OperationRunning, "cursor.second-running"
	rig.fixture.control.operationSequences["operation.agent.2"] = []controlplane.OperationView{running}
	rig.world.mu.Unlock()
	second, err := rig.runtime.StartTask(context.Background(), cognition.StartTaskInput{TaskID: "task.other-actor", HostID: "host.test", WorldID: "world.test", ActorID: "actor.two", ControllerID: "controller.internal", Goal: "Follow the player as another actor."})
	if err != nil {
		t.Fatal(err)
	}
	second, err = rig.runtime.RunTask(context.Background(), second.TaskID)
	if err != nil || second.PendingOperationID != "operation.agent.2" || second.Lookahead != nil || rig.model.previewCalls.Load() != 1 {
		t.Fatalf("preview pool blocked or exceeded actor execution: %#v %v", second, err)
	}
	close(rig.model.release)
	waitLookaheadTask(t, rig.runtime, rig.task.TaskID, func(task cognition.TaskSession) bool { return task.Lookahead.Status == "ready" })
	// Join the first worker's slot release before the next explicit progress
	// check; ready state itself is committed slightly before releasing the slot.
	deadline := time.Now().Add(time.Second)
	for rig.model.previewCalls.Load() == 1 {
		if _, err := rig.runtime.RunTask(context.Background(), second.TaskID); err != nil {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("released preview capacity was not reusable")
		}
		select {
		case <-rig.model.started:
		case <-time.After(time.Millisecond):
		}
	}
	if rig.model.previewCalls.Load() != 2 {
		t.Fatal("unexpected duplicate preview")
	}
}

func TestLookaheadCanBeDisabledAndCloseJoinsProviderBeforeStoreClosure(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		rig := newLookaheadRig(t, &cognition.LookaheadOptions{Disabled: true}, cognition.TaskBudget{})
		rig.start(t)
		current, _ := rig.runtime.GetTask(context.Background(), rig.task.TaskID)
		if current.Lookahead != nil || rig.model.previewCalls.Load() != 0 {
			t.Fatal("disabled lookahead still started")
		}
	})
	t.Run("close", func(t *testing.T) {
		rig := newLookaheadRig(t, nil, cognition.TaskBudget{})
		rig.start(t)
		rig.waitStarted(t)
		done := make(chan struct{})
		go func() { rig.runtime.Close(); close(done) }()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("runtime close did not join the provider")
		}
		current, err := rig.runtime.GetTask(context.Background(), rig.task.TaskID)
		if err != nil || current.Lookahead.ReservedTokens != 0 || current.ModelTokens != 110 {
			t.Fatalf("runtime closed before durable accounting: %#v %v", current.Lookahead, err)
		}
	})
}
