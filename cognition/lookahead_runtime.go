package cognition

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/taskstate"
)

const maxLookaheadJobs = 256

type lookaheadRuntime struct {
	provider LookaheadProvider
	options  LookaheadOptions
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	closed   bool
	jobs     map[string]*lookaheadJob
	slots    chan struct{}
	wg       sync.WaitGroup
}

type lookaheadJob struct {
	task        TaskSession
	identity    string
	operationID string
	expires     time.Time
	ctx         context.Context
	cancel      context.CancelFunc
	// All fields below are protected by lookaheadRuntime.mu. The result is
	// published only after its usage has been committed under the task lock.
	invalid  string
	finished bool
	input    LookaheadInput
	draft    NextStepDraft
}

func newLookaheadRuntime(model ModelProvider, options LookaheadOptions) *lookaheadRuntime {
	provider, ok := model.(LookaheadProvider)
	if !ok || options.Disabled {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &lookaheadRuntime{provider: provider, options: options, ctx: ctx, cancel: cancel,
		jobs: make(map[string]*lookaheadJob), slots: make(chan struct{}, options.MaxConcurrent)}
}

// Close joins independent lookahead calls before the caller closes TaskStore.
// A ModelProvider must honor cancellation just like a normal decision call.
func (runtime *AgentRuntime) Close() {
	lookahead := runtime.lookahead
	if lookahead == nil {
		return
	}
	lookahead.mu.Lock()
	lookahead.closed = true
	lookahead.cancel()
	for _, job := range lookahead.jobs {
		job.invalid = "shutdown"
		job.cancel()
	}
	lookahead.mu.Unlock()
	lookahead.wg.Wait()
	lookahead.mu.Lock()
	clear(lookahead.jobs)
	lookahead.mu.Unlock()
}

// startLookahead runs under the ordinary task lock. Only the small durable
// attempt marker is written here; observation/context assembly and inference
// happen in the independent job after this function returns.
func (runtime *AgentRuntime) startLookahead(ctx context.Context, task TaskSession, view controlplane.OperationView) (TaskSession, error) {
	lookahead := runtime.lookahead
	if lookahead == nil || task.Status != TaskActive || task.CancelRequested || task.PendingAction == nil ||
		task.PendingActionMacro || view.Terminal || (view.Status != controlplane.OperationAccepted && view.Status != controlplane.OperationRunning) ||
		view.OperationID != task.PendingOperationID || len(task.PendingSignals) != 0 ||
		(task.Lookahead != nil && task.Lookahead.OperationID == view.OperationID) {
		return task, nil
	}
	lookahead.mu.Lock()
	for id, cached := range lookahead.jobs {
		if cached.finished && !runtime.now().Before(cached.expires) {
			cached.cancel()
			delete(lookahead.jobs, id)
		}
	}
	if lookahead.closed || lookahead.jobs[task.TaskID] != nil || len(lookahead.jobs) >= maxLookaheadJobs {
		lookahead.mu.Unlock()
		return task, nil
	}
	select {
	case lookahead.slots <- struct{}{}:
	default:
		lookahead.mu.Unlock()
		return task, nil
	}
	jobCtx, cancel := context.WithTimeout(lookahead.ctx, lookaheadDuration(lookahead.options.TimeoutMillis))
	proofTask := task
	proofTask.History = nil // The bounded prompt needs no retained diagnostic history.
	job := &lookaheadJob{task: cloneTaskSession(proofTask), identity: lookaheadTaskIdentity(task), operationID: view.OperationID,
		expires: runtime.now().Add(lookaheadDuration(lookahead.options.DraftTTLMillis)), ctx: jobCtx, cancel: cancel}
	lookahead.jobs[task.TaskID] = job
	lookahead.wg.Add(1)
	lookahead.mu.Unlock()
	state := TaskLookaheadState{}
	if task.Lookahead != nil {
		state = *task.Lookahead
	}
	state.OperationID, state.Status, state.Code, state.ReservedTokens = view.OperationID, "preparing", "", 0
	task.Lookahead = &state
	appendTaskEvent(&task, runtime.lookaheadEvent(task, "lookahead.preparing", "", view.OperationID))
	saved, err := runtime.saveTask(ctx, task)
	if err != nil {
		cancel()
		lookahead.mu.Lock()
		delete(lookahead.jobs, task.TaskID)
		lookahead.mu.Unlock()
		<-lookahead.slots
		lookahead.wg.Done()
		return task, err
	}
	go runtime.runLookahead(job)
	return saved, nil
}

func (runtime *AgentRuntime) runLookahead(job *lookaheadJob) {
	lookahead := runtime.lookahead
	defer lookahead.wg.Done()
	defer func() { <-lookahead.slots }()
	defer job.cancel()
	var input LookaheadInput
	var specs []host.CapabilitySpec
	var draft NextStepDraft
	var workErr error
	var startedAt time.Time
	// Keep a custom provider panic inside this optional job and settle any
	// reserved budget conservatively before exposing another decision boundary.
	defer func() {
		if recover() != nil {
			workErr = errors.New("lookahead provider panicked")
		}
		latency := uint64(0)
		if !startedAt.IsZero() {
			latency = uint64(time.Since(startedAt).Milliseconds())
		}
		runtime.finishLookahead(job, input, draft, workErr, latency)
	}()
	input, specs, workErr = runtime.buildLookaheadInput(job.ctx, job.task, job.operationID)
	if workErr != nil {
		return
	}
	var reserve uint64
	reserve, workErr = lookahead.provider.LookaheadTokenReservation(input)
	if workErr != nil {
		return
	}
	if reserve == 0 || reserve > maxProviderWireInteger/2 {
		workErr = errors.New("invalid lookahead token reservation")
		return
	}
	if workErr = runtime.reserveLookahead(job, reserve); workErr != nil {
		return
	}
	startedAt = time.Now()
	draft, workErr = lookahead.provider.Lookahead(job.ctx, input)
	// Optional diagnostic metadata must not prevent durable usage settlement.
	if len(draft.ProviderModel) > 200 || validateProviderText("lookahead.provider_model", draft.ProviderModel, 200, false) != nil {
		draft.ProviderModel = ""
	}
	if usage, usageErr := modelDecisionTokenUsage(ModelDecision{Usage: draft.Usage}); usageErr != nil || usage > maxProviderWireInteger {
		draft.UsageKnown = false
		if workErr == nil {
			workErr = errors.New("invalid lookahead usage")
		}
	}
	if workErr == nil {
		workErr = validateNextStepDraft(draft, input.Context)
	}
	if workErr == nil && draft.Kind == "action" {
		workErr = actionArgumentsSchemaError(ModelDecision{Kind: ModelDecisionAction, Capability: draft.Capability, Arguments: draft.Arguments}, specs)
	}
}

func (runtime *AgentRuntime) buildLookaheadInput(ctx context.Context, task TaskSession, operationID string) (LookaheadInput, []host.CapabilitySpec, error) {
	actor, err := runtime.control.GetActor(runtime.principal, task.HostID, task.WorldID, task.ActorID)
	if err != nil || !actor.Online || actor.Epoch != task.ControllerLease.Epoch {
		return LookaheadInput{}, nil, controlplane.ErrStale
	}
	observation, err := runtime.environment.Observe(ctx, host.ObservationQuery{
		QueryID: runtime.stepID(task, "lookahead.observe"), HostID: task.HostID, WorldID: task.WorldID,
		ActorID: task.ActorID, ExpectedEpoch: actor.Epoch, Limit: 256,
	})
	if err != nil {
		return LookaheadInput{}, nil, err
	}
	if err := validateTaskObservation(task, actor, observation); err != nil {
		return LookaheadInput{}, nil, err
	}
	catalog, err := runtime.environment.Capabilities(ctx, controlplane.ActorControlTarget{HostID: task.HostID, WorldID: task.WorldID, ActorID: task.ActorID})
	if err != nil {
		return LookaheadInput{}, nil, err
	}
	specs, summaries, err := prepareAgentCapabilities(catalog)
	if err != nil {
		return LookaheadInput{}, nil, err
	}
	specs = slices.DeleteFunc(specs, func(spec host.CapabilitySpec) bool {
		return spec.Kind == host.CapabilityMacro || !taskAllowsCapability(task, spec.Capability.ID)
	})
	summaries = slices.DeleteFunc(summaries, func(summary CapabilitySummary) bool {
		return summary.Kind == host.CapabilityMacro || !taskAllowsCapability(task, summary.Capability.ID)
	})
	persona, err := runtime.persona.Load(ctx, PersonaRequest{ActorID: task.ActorID, ControllerID: task.ControllerID})
	if err != nil {
		return LookaheadInput{}, nil, err
	}
	var plan *taskstate.PlanState
	if task.PlanID != "" {
		if runtime.plans == nil {
			return LookaheadInput{}, nil, errors.New("lookahead plan is unavailable")
		}
		value, err := runtime.plans.GetPlan(ctx, task.PlanID)
		if err != nil {
			return LookaheadInput{}, nil, err
		}
		if value.Status != taskstate.PlanActive || value.Goal != task.Goal || value.HostID != task.HostID || value.WorldID != task.WorldID {
			return LookaheadInput{}, nil, errors.New("lookahead plan has changed")
		}
		plan = &value
	}
	assembled, err := runtime.assembleOptionalModelContext(ctx, task, observation, summaries)
	if err != nil {
		return LookaheadInput{}, nil, err
	}
	input := LookaheadInput{OperationID: operationID, Action: cloneTaskActionRequest(*task.PendingAction), Context: ModelInput{
		Task: ModelTaskContext{TaskID: task.TaskID, SessionID: task.SessionID, ActorID: task.ActorID, ControllerID: task.ControllerID,
			ParentOperationID: task.MacroOperationID, Goal: task.Goal, Tags: task.Tags, PlanningMode: task.PlanningMode, Completion: cloneTaskCompletion(task.Completion)},
		Persona: persona, Observation: observation, Capabilities: summaries, Plan: plan,
		Memories: assembled.memories, Skills: assembled.skills, LastOperationResult: task.LastOperationResult,
	}}
	// At most four full schemas supplement the summaries in the single preview
	// call. Prefer capabilities hinted by the current/following plan steps.
	if plan != nil {
		for _, step := range plan.Steps {
			if step.Status != taskstate.StepActive && step.Status != taskstate.StepPending {
				continue
			}
			for _, hint := range step.CapabilityHints {
				for _, spec := range specs {
					if spec.Capability == hint && len(input.Context.InspectedCapabilities) < 4 && !containsInspectedCapability(input.Context.InspectedCapabilities, hint) {
						input.Context.InspectedCapabilities = append(input.Context.InspectedCapabilities, spec)
					}
				}
			}
		}
	}
	for _, spec := range specs {
		if len(input.Context.InspectedCapabilities) >= 4 {
			break
		}
		if !containsInspectedCapability(input.Context.InspectedCapabilities, spec.Capability) {
			input.Context.InspectedCapabilities = append(input.Context.InspectedCapabilities, spec)
		}
	}
	sealed, _, err := sealModelInput(input.Context)
	input.Context = sealed
	if err == nil {
		// Optional providers may return read-only shared projections. Keep an
		// independent proof snapshot for the entire lifetime of this draft.
		var payload []byte
		// ModelInput deliberately excludes the raw Host observation from JSON;
		// carry it separately instead of accidentally losing its authority data.
		payload, err = json.Marshal(struct {
			Input       LookaheadInput
			Observation host.ObservationEnvelope
		}{input, input.Context.Observation})
		if err == nil {
			var copy struct {
				Input       LookaheadInput
				Observation host.ObservationEnvelope
			}
			err = json.Unmarshal(payload, &copy)
			input = copy.Input
			input.Context.Observation = copy.Observation
		}
	}
	return input, specs, err
}

func (runtime *AgentRuntime) reserveLookahead(job *lookaheadJob, reserve uint64) error {
	lock := runtime.taskLock(job.task.TaskID)
	lock.Lock()
	defer lock.Unlock()
	if err := job.ctx.Err(); err != nil {
		return err
	}
	task, err := runtime.tasks.Load(job.ctx, job.task.TaskID)
	if err != nil {
		return err
	}
	if !lookaheadPendingTaskMatches(task, job) || runtime.lookaheadInvalid(job) != "" {
		return errors.New("lookahead task changed before the model call")
	}
	view, err := runtime.control.GetOperation(runtime.principal, job.operationID)
	if err != nil || view.Terminal || (view.Status != controlplane.OperationAccepted && view.Status != controlplane.OperationRunning) {
		return errors.New("lookahead operation already settled")
	}
	// Leave a normal decision's allowance available if this speculative call is
	// discarded or still cancelling at the execution boundary.
	if uint64(task.ModelCalls)+2 > uint64(task.Budget.MaxModelCalls) || task.ModelTokens >= task.Budget.MaxModelTokens ||
		reserve*2 > task.Budget.MaxModelTokens-task.ModelTokens {
		return ErrTaskBudgetExceeded
	}
	task.Lookahead.Status, task.Lookahead.ReservedTokens = "running", reserve
	task.Lookahead.Calls++
	task.ModelCalls++
	appendTaskEvent(&task, runtime.lookaheadEvent(task, "lookahead.started", "", job.operationID))
	_, err = runtime.saveTask(job.ctx, task)
	return err
}

func lookaheadPendingTaskMatches(task TaskSession, job *lookaheadJob) bool {
	return task.Status == TaskActive && !task.CancelRequested && task.Lookahead != nil &&
		task.Lookahead.OperationID == job.operationID && task.Lookahead.Status == "preparing" &&
		task.PendingOperationID == job.operationID && task.Step == job.task.Step && lookaheadTaskIdentity(task) == job.identity
}

func (runtime *AgentRuntime) lookaheadInvalid(job *lookaheadJob) string {
	runtime.lookahead.mu.Lock()
	defer runtime.lookahead.mu.Unlock()
	return job.invalid
}

func (runtime *AgentRuntime) finishLookahead(job *lookaheadJob, input LookaheadInput, draft NextStepDraft, workErr error, latency uint64) {
	lock := runtime.taskLock(job.task.TaskID)
	lock.Lock()
	defer lock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ready := false
	for {
		task, err := runtime.tasks.Load(ctx, job.task.TaskID)
		if err != nil || task.Lookahead == nil || task.Lookahead.OperationID != job.operationID {
			break
		}
		state := task.Lookahead
		called := state.ReservedTokens > 0
		if called {
			charge := state.ReservedTokens
			if usage, usageErr := modelDecisionTokenUsage(ModelDecision{Usage: draft.Usage}); draft.UsageKnown && usageErr == nil && usage <= maxProviderWireInteger-task.ModelTokens {
				charge = usage
			}
			task.ModelTokens += charge
			state.ReservedTokens = 0
		}
		code := runtime.lookaheadInvalid(job)
		if code == "" {
			switch {
			case job.ctx.Err() != nil:
				code = "timeout-or-cancelled"
			case errors.Is(workErr, ErrTaskBudgetExceeded):
				code = "budget-reserved-for-execution"
			case workErr != nil:
				code = "unavailable-or-invalid"
			case draft.Kind != "action":
				code = "no-successor"
			case task.Status != TaskActive || task.CancelRequested || lookaheadTaskIdentity(task) != job.identity:
				code = "task-changed"
			case state.Status == "discarded":
				code = state.Code
			case !runtime.now().Before(job.expires):
				code = "expired"
			case task.ModelTokens > task.Budget.MaxModelTokens:
				code = "budget-exceeded"
			}
		}
		ready = code == "" && called
		if ready {
			state.Status, state.Code = "ready", ""
			event := runtime.lookaheadEvent(task, "lookahead.ready", "", job.operationID)
			event.Summary = draft.Summary
			appendTaskEvent(&task, event)
		} else {
			if code == "" {
				code = "not-started"
			}
			lookaheadDiscardState(&task, code)
			appendTaskEvent(&task, runtime.lookaheadEvent(task, "lookahead.discarded", code, job.operationID))
		}
		if called {
			event := runtime.lookaheadEvent(task, "lookahead.usage", "reported", job.operationID)
			if !draft.UsageKnown {
				event.Code = "reserved-usage-unknown"
				event.Model = measuredModelUsage(ModelDecision{ProviderModel: draft.ProviderModel}, latency)
			} else {
				event.Model = measuredModelUsage(ModelDecision{Usage: draft.Usage, ProviderModel: draft.ProviderModel}, latency)
			}
			appendTaskEvent(&task, event)
		}
		_, err = runtime.saveTask(ctx, task)
		if errors.Is(err, ErrTaskRevisionConflict) {
			continue
		}
		if err != nil {
			ready = false
		}
		break
	}
	lookahead := runtime.lookahead
	lookahead.mu.Lock()
	job.finished = true
	job.input, job.draft = input, draft
	if !ready || job.invalid != "" {
		delete(lookahead.jobs, job.task.TaskID)
	}
	lookahead.mu.Unlock()
}

func (runtime *AgentRuntime) lookaheadEvent(task TaskSession, kind, code, operationID string) TaskEvent {
	return TaskEvent{Kind: kind, Step: task.Step, Code: code, OperationID: operationID, AtUnixMillis: runtime.now().UnixMilli()}
}

// guardLookaheadSave participates in the same CAS as a cancellation, attention
// update, pause, or authority change. It never waits for a provider to stop.
func (runtime *AgentRuntime) guardLookaheadSave(task *TaskSession) {
	if task.Lookahead == nil || runtime.lookahead == nil {
		return
	}
	lookahead := runtime.lookahead
	lookahead.mu.Lock()
	defer lookahead.mu.Unlock()
	job := lookahead.jobs[task.TaskID]
	if job == nil || job.operationID != task.Lookahead.OperationID {
		return
	}
	if task.Status != TaskActive || task.CancelRequested || lookaheadTaskIdentity(*task) != job.identity {
		if task.Lookahead.Status != "adopted" && task.Lookahead.Status != "discarded" {
			lookaheadDiscardState(task, "task-changed")
			appendTaskEvent(task, runtime.lookaheadEvent(*task, "lookahead.discarded", "task-changed", job.operationID))
		}
		job.invalid = "task-changed"
		job.cancel()
		if job.finished {
			delete(lookahead.jobs, task.TaskID)
		}
	}
}

// A process restart loses the optional draft, but never erases the durable
// charge for a provider call that may already have reached the remote service.
func (runtime *AgentRuntime) recoverLookahead(ctx context.Context, task TaskSession) (TaskSession, error) {
	state := task.Lookahead
	if state == nil || ((state.Status == "adopted" || state.Status == "discarded") && state.ReservedTokens == 0) {
		return task, nil
	}
	if runtime.lookahead != nil {
		runtime.lookahead.mu.Lock()
		job := runtime.lookahead.jobs[task.TaskID]
		runtime.lookahead.mu.Unlock()
		if job != nil {
			return task, nil
		}
	}
	if state.ReservedTokens > maxProviderWireInteger-task.ModelTokens {
		return task, errors.New("lookahead recovery usage overflow")
	}
	task.ModelTokens += state.ReservedTokens
	state.ReservedTokens = 0
	lookaheadDiscardState(&task, "restart-or-lost-draft")
	appendTaskEvent(&task, runtime.lookaheadEvent(task, "lookahead.discarded", "restart-or-lost-draft", state.OperationID))
	return runtime.saveTask(ctx, task)
}

func (runtime *AgentRuntime) tryLookahead(ctx context.Context, task TaskSession, current *taskDecisionContext) (ModelDecision, TaskSession, bool, error) {
	lookahead := runtime.lookahead
	if lookahead == nil {
		return ModelDecision{}, task, false, nil
	}
	lookahead.mu.Lock()
	job := lookahead.jobs[task.TaskID]
	finished := job != nil && job.finished
	lookahead.mu.Unlock()
	if job == nil {
		return ModelDecision{}, task, false, nil
	}
	code := ""
	var decision ModelDecision
	if !finished {
		code = "not-ready"
	} else {
		decision, code = runtime.validateLookaheadForAdoption(task, current, job)
	}
	lookahead.mu.Lock()
	if code != "" {
		job.invalid = code
	}
	job.cancel()
	if finished {
		delete(lookahead.jobs, task.TaskID)
	}
	lookahead.mu.Unlock()
	if task.Lookahead == nil || task.Lookahead.OperationID != job.operationID {
		return ModelDecision{}, task, false, nil
	}
	if code != "" {
		lookaheadDiscardState(&task, code)
		appendTaskEvent(&task, runtime.lookaheadEvent(task, "lookahead.discarded", code, job.operationID))
	} else {
		task.Lookahead.Status, task.Lookahead.Code = "adopted", ""
		task.Lookahead.Adopted++
		for _, warning := range current.warnings {
			appendTaskEvent(&task, warning)
		}
		event := runtime.lookaheadEvent(task, "lookahead.adopted", "verified", job.operationID)
		event.Summary = job.draft.Summary
		appendTaskEvent(&task, event)
	}
	saved, err := runtime.saveTask(ctx, task)
	return decision, saved, code == "" && err == nil, err
}

func (runtime *AgentRuntime) validateLookaheadForAdoption(task TaskSession, current *taskDecisionContext, job *lookaheadJob) (ModelDecision, string) {
	if runtime.lookaheadInvalid(job) != "" || task.Lookahead == nil || task.Lookahead.Status != "ready" ||
		task.Status != TaskActive || task.CancelRequested || lookaheadTaskIdentity(task) != job.identity ||
		task.Step != job.task.Step+1 || task.PendingAction != nil || task.LastOperationResult == nil ||
		task.LastOperationResult.OperationID != job.operationID || task.LastOperationResult.Status != string(controlplane.OperationSucceeded) {
		return ModelDecision{}, "task-or-result-changed"
	}
	if !runtime.now().Before(job.expires) || current.observation.Epoch != job.input.Context.Observation.Epoch {
		return ModelDecision{}, "expired-or-epoch-changed"
	}
	if task.ModelTokens > task.Budget.MaxModelTokens || current.input.AllowedReplanReason != "" ||
		lookaheadPlanIdentity(current.input.Plan) != lookaheadPlanIdentity(job.input.Context.Plan) {
		return ModelDecision{}, "budget-or-plan-changed"
	}
	if plan := current.input.Plan; plan != nil && (plan.Status != taskstate.PlanActive || plan.CurrentStepID != job.draft.PlanStepID) {
		return ModelDecision{}, "plan-step-changed"
	}
	view, err := runtime.control.GetOperation(runtime.principal, job.operationID)
	if err != nil || !view.Terminal || !view.ExecutionConfirmed || view.Outcome == nil ||
		view.Outcome.Status != host.ActionSucceeded || view.ReconciliationPending || current.observation.Sequence < view.Outcome.WorldSeq ||
		current.observation.Sequence < job.input.Context.Observation.Sequence {
		return ModelDecision{}, "fresh-result-required"
	}
	oldCapability, _ := findCapabilitySummary(job.input.Context.Capabilities, job.draft.Capability)
	capability, ok := findCapabilitySummary(current.summaries, job.draft.Capability)
	if !ok || capability.SpecDigest != oldCapability.SpecDigest || capability.Kind == host.CapabilityMacro {
		return ModelDecision{}, "capability-changed"
	}
	for _, condition := range job.draft.Preconditions {
		var original *host.HostRef
		for _, fact := range job.input.Context.Observation.Facts {
			if fact.FactID == condition.FactID {
				original = fact.Subject
			}
		}
		found := false
		for _, fact := range current.observation.Facts {
			if fact.FactID == condition.FactID && sameLookaheadSubject(original, fact.Subject) && lookaheadJSONEqual(fact.Value, []byte(condition.FactValueJSON)) {
				found = true
				break
			}
		}
		if !found {
			return ModelDecision{}, "precondition-changed"
		}
	}
	targets, err := ResolveModelTargetHandles(job.input.Context.Observation, job.draft.TargetHandles)
	if err != nil {
		return ModelDecision{}, "target-changed"
	}
	_, lookup, err := BuildModelObservation(current.observation)
	if err != nil {
		return ModelDecision{}, "observation-invalid"
	}
	handles := make([]string, 0, len(targets))
	for _, target := range targets {
		found := ""
		for handle, ref := range lookup {
			if ref == target {
				found = handle
				break
			}
		}
		if found == "" {
			return ModelDecision{}, "target-changed"
		}
		handles = append(handles, found)
	}
	decision := ModelDecision{Kind: ModelDecisionAction, Capability: job.draft.Capability,
		Arguments: append(json.RawMessage(nil), job.draft.Arguments...), TargetHandles: handles, Summary: job.draft.Summary}
	if err := actionArgumentsSchemaError(decision, current.specs); err != nil {
		return ModelDecision{}, "arguments-invalid"
	}
	return decision, ""
}

func sameLookaheadSubject(left, right *host.HostRef) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func lookaheadJSONEqual(left, right []byte) bool {
	var l, r bytes.Buffer
	return json.Compact(&l, left) == nil && json.Compact(&r, right) == nil && bytes.Equal(l.Bytes(), r.Bytes())
}
