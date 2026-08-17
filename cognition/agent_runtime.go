package cognition

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/taskstate"
)

var ErrTaskBudgetExceeded = errors.New("cognition task budget exceeded")

// AgentEnvironment exposes only authoritative observation and capability
// discovery. It cannot authorize effects or report outcomes.
type AgentEnvironment interface {
	Observe(context.Context, host.ObservationQuery) (host.ObservationEnvelope, error)
	Capabilities(context.Context, controlplane.ActorControlTarget) (host.CapabilitySnapshot, error)
}

// AgentControlPlane is the exact subset of the shared control plane used by
// the internal Agent Runtime. *controlplane.Service implements this interface.
type AgentControlPlane interface {
	GetActor(host.Principal, string, string, string) (controlplane.ActorView, error)
	AcquireController(host.Principal, controlplane.AcquireControllerInput) (controlplane.ControllerLease, error)
	RenewController(host.Principal, controlplane.ActorControlTarget, string, uint32) (controlplane.ControllerLease, error)
	ReleaseController(host.Principal, controlplane.ActorControlTarget, string) error
	SubmitAction(context.Context, host.Principal, controlplane.SubmitActionInput) (controlplane.OperationView, error)
	GetOperation(host.Principal, string) (controlplane.OperationView, error)
	WaitOperation(context.Context, host.Principal, controlplane.WaitOperationInput) (controlplane.OperationUpdate, error)
	CancelOperation(host.Principal, string) (controlplane.OperationView, error)
}

type AgentRuntimeOptions struct {
	Principal                 host.Principal
	Control                   AgentControlPlane
	Environment               AgentEnvironment
	Persona                   PersonaProvider
	Memory                    MemoryProvider
	Skills                    SkillProvider
	Model                     ModelProvider
	Tasks                     TaskStore
	Decisions                 DecisionRecorder
	Learning                  *SkillLearningOptions
	OutcomesRecordedByControl bool
	Plans                     taskstate.PlanClient

	Now                   func() time.Time
	ControllerLeaseMillis uint32
	RenewBeforeMillis     uint32
	OperationWaitMillis   uint32
	MaxAdvancesPerRun     uint32
	MemoryBudget          MemoryBudget
}

type StartTaskInput struct {
	TaskID              string                 `json:"task_id"`
	HostID              string                 `json:"host_id"`
	WorldID             string                 `json:"world_id"`
	ActorID             string                 `json:"actor_id"`
	ControllerID        string                 `json:"controller_id"`
	Goal                string                 `json:"goal"`
	Tags                []string               `json:"tags,omitempty"`
	AllowedCapabilities []string               `json:"allowed_capabilities,omitempty"`
	PlanningMode        taskstate.PlanningMode `json:"planning_mode,omitempty"`
	Budget              TaskBudget             `json:"budget"`
}

type AgentRuntime struct {
	principal                 host.Principal
	control                   AgentControlPlane
	environment               AgentEnvironment
	persona                   PersonaProvider
	memory                    MemoryProvider
	skills                    SkillProvider
	model                     ModelProvider
	tasks                     TaskStore
	decisions                 DecisionRecorder
	learning                  *skillLearningRuntime
	plans                     taskstate.PlanClient
	outcomesRecordedByControl bool

	now                   func() time.Time
	controllerLeaseMillis uint32
	renewBeforeMillis     uint32
	operationWaitMillis   uint32
	maxAdvancesPerRun     uint32
	memoryBudget          MemoryBudget

	taskLocksMu sync.Mutex
	taskLocks   map[string]*sync.Mutex
	timelineMu  sync.Mutex
	taskChanged chan struct{}
}

func NewAgentRuntime(options AgentRuntimeOptions) (*AgentRuntime, error) {
	if err := host.ValidatePrincipal(options.Principal); err != nil {
		return nil, fmt.Errorf("principal: %w", err)
	}
	if options.Control == nil || options.Environment == nil || options.Persona == nil ||
		options.Model == nil || options.Tasks == nil {
		return nil, errors.New("control, environment, persona, model, and task store are required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.ControllerLeaseMillis == 0 {
		options.ControllerLeaseMillis = 60_000
	}
	if options.ControllerLeaseMillis < 5_000 || options.ControllerLeaseMillis > 300_000 {
		return nil, errors.New("controller lease duration must be between 5000 and 300000 milliseconds")
	}
	if options.RenewBeforeMillis == 0 {
		options.RenewBeforeMillis = min(10_000, options.ControllerLeaseMillis/2)
	}
	if options.RenewBeforeMillis >= options.ControllerLeaseMillis {
		return nil, errors.New("controller renewal window must be shorter than the lease")
	}
	if options.OperationWaitMillis == 0 {
		options.OperationWaitMillis = 1_000
	}
	if options.OperationWaitMillis > 25_000 {
		return nil, errors.New("operation wait must not exceed 25000 milliseconds")
	}
	if options.MaxAdvancesPerRun == 0 {
		options.MaxAdvancesPerRun = 16
	}
	if options.MaxAdvancesPerRun > 1_024 {
		return nil, errors.New("max advances per run is too large")
	}
	if options.MemoryBudget.MaxRecords == 0 {
		options.MemoryBudget.MaxRecords = 16
	}
	if options.MemoryBudget.MaxCharacters == 0 {
		options.MemoryBudget.MaxCharacters = 6_000
	}
	learning, err := normalizeSkillLearningOptions(options.Learning)
	if err != nil {
		return nil, err
	}
	return &AgentRuntime{
		principal: options.Principal, control: options.Control, environment: options.Environment,
		persona: options.Persona, memory: options.Memory, skills: options.Skills,
		model: options.Model, tasks: options.Tasks, decisions: options.Decisions,
		learning: learning, plans: options.Plans,
		outcomesRecordedByControl: options.OutcomesRecordedByControl,
		now:                       options.Now,
		controllerLeaseMillis:     options.ControllerLeaseMillis,
		renewBeforeMillis:         options.RenewBeforeMillis,
		operationWaitMillis:       options.OperationWaitMillis,
		maxAdvancesPerRun:         options.MaxAdvancesPerRun,
		memoryBudget:              options.MemoryBudget,
		taskLocks:                 make(map[string]*sync.Mutex),
		taskChanged:               make(chan struct{}),
	}, nil
}

// RunTask advances a bounded number of semantic decisions. It stops on wait,
// confirmation, provider pause, non-terminal operation wait, or terminal task.
func (runtime *AgentRuntime) RunTask(
	ctx context.Context,
	taskID string,
) (TaskSession, error) {
	if err := validateTaskID(taskID); err != nil {
		return TaskSession{}, err
	}
	lock := runtime.taskLock(taskID)
	lock.Lock()
	defer lock.Unlock()
	var task TaskSession
	var err error
	for advance := uint32(0); advance < runtime.maxAdvancesPerRun; advance++ {
		task, err = runtime.tasks.Load(ctx, taskID)
		if err != nil {
			return TaskSession{}, err
		}
		if terminalTaskStatus(task.Status) || task.Status == TaskPaused {
			if task.Status == TaskCompleted {
				return runtime.maybeLearnSkill(ctx, task)
			}
			return task, nil
		}
		var keepRunning bool
		task, keepRunning, err = runtime.advanceTask(ctx, task)
		if err != nil || !keepRunning {
			if err == nil && task.Status == TaskCompleted {
				return runtime.maybeLearnSkill(ctx, task)
			}
			return task, err
		}
	}
	return runtime.tasks.Load(ctx, taskID)
}

func (runtime *AgentRuntime) advanceTask(
	ctx context.Context,
	task TaskSession,
) (TaskSession, bool, error) {
	if task.PendingAction != nil {
		return runtime.advancePendingAction(ctx, task)
	}
	if task.MacroOperationID != "" {
		advanced, ready, err := runtime.advanceMacroOperation(ctx, task)
		if err != nil || !ready {
			return advanced, false, err
		}
		task = advanced
	}
	if task.Status == TaskCancelling {
		cancelled, err := runtime.finishCancelledTask(ctx, task, "no-pending-operation")
		return cancelled, false, err
	}
	if task.Status != TaskActive {
		return task, false, nil
	}
	if task.Step >= task.Budget.MaxSteps {
		failed, err := runtime.failTask(ctx, task, "budget.steps", ErrTaskBudgetExceeded)
		return failed, false, err
	}
	var err error
	task, err = runtime.ensureController(ctx, task)
	if err != nil {
		return task, false, err
	}
	actor, err := runtime.control.GetActor(
		runtime.principal, task.HostID, task.WorldID, task.ActorID,
	)
	if err != nil || !actor.Online {
		if err == nil {
			err = controlplane.ErrUnavailable
		}
		paused, pauseErr := runtime.pauseTask(ctx, task, "host.unavailable", err)
		return paused, false, pauseErr
	}
	query := host.ObservationQuery{
		QueryID: runtime.stepID(task, "observe"), HostID: task.HostID,
		WorldID: task.WorldID, ActorID: task.ActorID, ExpectedEpoch: actor.Epoch, Limit: 256,
	}
	observation, err := runtime.environment.Observe(ctx, query)
	if err != nil {
		paused, pauseErr := runtime.pauseTask(ctx, task, "observation.unavailable", err)
		return paused, false, pauseErr
	}
	if err := validateTaskObservation(task, actor, observation); err != nil {
		paused, pauseErr := runtime.pauseTask(ctx, task, "observation.invalid", err)
		return paused, false, pauseErr
	}
	// A macro changes the Host-owned plan before its children can be selected.
	// Wait for that newer publication instead of binding a child to the snapshot
	// that originally started the macro.
	if task.MacroOperationID != "" && observation.Sequence <= task.LastObservationSeq {
		return task, false, nil
	}
	target := controlplane.ActorControlTarget{
		HostID: task.HostID, WorldID: task.WorldID, ActorID: task.ActorID,
	}
	catalog, err := runtime.environment.Capabilities(ctx, target)
	if err != nil {
		paused, pauseErr := runtime.pauseTask(ctx, task, "capabilities.unavailable", err)
		return paused, false, pauseErr
	}
	specs, summaries, err := prepareAgentCapabilities(catalog)
	if err != nil {
		paused, pauseErr := runtime.pauseTask(ctx, task, "capabilities.invalid", err)
		return paused, false, pauseErr
	}
	if task.MacroOperationID != "" {
		specs = slices.DeleteFunc(specs, func(spec host.CapabilitySpec) bool {
			return spec.Kind == host.CapabilityMacro
		})
		summaries = slices.DeleteFunc(summaries, func(summary CapabilitySummary) bool {
			return summary.Kind == host.CapabilityMacro
		})
	}
	if len(task.AllowedCapabilities) != 0 {
		specs = slices.DeleteFunc(specs, func(spec host.CapabilitySpec) bool {
			return !taskAllowsCapability(task, spec.Capability.ID)
		})
		summaries = slices.DeleteFunc(summaries, func(summary CapabilitySummary) bool {
			return !taskAllowsCapability(task, summary.Capability.ID)
		})
		if len(summaries) == 0 {
			paused, pauseErr := runtime.pauseTask(
				ctx,
				task,
				"capabilities.scope-empty",
				errors.New("task capability scope has no published capability in this phase"),
			)
			return paused, false, pauseErr
		}
	}
	persona, err := runtime.persona.Load(ctx, PersonaRequest{
		ActorID: task.ActorID, ControllerID: task.ControllerID,
	})
	if err != nil {
		paused, pauseErr := runtime.pauseTask(ctx, task, "persona.unavailable", err)
		return paused, false, pauseErr
	}
	assembled, err := runtime.assembleOptionalModelContext(
		ctx, task, observation, summaries,
	)
	if err != nil {
		return task, false, err
	}
	warnings := assembled.warnings
	memories := assembled.memories
	skills := assembled.skills
	task.LastObservationID = observation.ObservationID
	task.LastObservationSeq = observation.Sequence
	plan, task, err := runtime.loadTaskPlan(ctx, task)
	if err != nil {
		paused, pauseErr := runtime.pauseTask(ctx, task, "plan.unavailable", err)
		return paused, false, pauseErr
	}
	input := ModelInput{
		Task: ModelTaskContext{
			TaskID: task.TaskID, SessionID: task.SessionID, ActorID: task.ActorID,
			ControllerID: task.ControllerID, ParentOperationID: task.MacroOperationID,
			Goal: task.Goal, Tags: task.Tags, PlanningMode: task.PlanningMode,
		},
		Persona: persona, Observation: observation, Memories: memories,
		Capabilities: summaries, Skills: skills, Plan: plan,
	}
	decision, task, err := runtime.callModel(ctx, task, input, warnings)
	if err != nil {
		return task, false, err
	}
	if decision.Kind == ModelDecisionInspect {
		if err := validateRuntimeInspection(decision, summaries, skills); err != nil {
			paused, pauseErr := runtime.pauseTask(ctx, task, "model.invalid", err)
			return paused, false, pauseErr
		}
		inspectedCapabilities, inspectErr := selectInspectedCapabilities(
			specs, decision.InspectCapabilities,
		)
		if inspectErr != nil {
			paused, pauseErr := runtime.pauseTask(ctx, task, "capabilities.stale", inspectErr)
			return paused, false, pauseErr
		}
		inspectedSkills := make([]Skill, 0, len(decision.InspectSkills))
		for _, ref := range decision.InspectSkills {
			if runtime.skills == nil {
				appendTaskEvent(&task, runtime.warningEvent(task, "skills.degraded"))
				break
			}
			skill, describeErr := runtime.skills.DescribeSkill(ctx, ref.SkillID, ref.Version)
			if describeErr != nil {
				appendTaskEvent(&task, runtime.warningEvent(task, "skills.degraded"))
				continue
			}
			inspectedSkills = append(inspectedSkills, skill)
		}
		input.InspectionRound = 1
		input.InspectedCapabilities = inspectedCapabilities
		input.InspectedSkills = inspectedSkills
		decision, task, err = runtime.callModel(ctx, task, input, nil)
		if err != nil {
			return task, false, err
		}
		if decision.Kind == ModelDecisionInspect {
			err := errors.New("model requested more than one inspection round")
			paused, pauseErr := runtime.pauseTask(ctx, task, "model.invalid", err)
			return paused, false, pauseErr
		}
	}
	return runtime.applyModelDecision(ctx, task, observation, summaries, decision)
}

func (runtime *AgentRuntime) advancePendingAction(
	ctx context.Context,
	task TaskSession,
) (TaskSession, bool, error) {
	if len(task.PendingMemories) != 0 {
		var warning bool
		if runtime.memory == nil {
			warning = true
		} else {
			for _, record := range task.PendingMemories {
				if _, err := runtime.memory.Append(ctx, record); err != nil {
					warning = true
				}
			}
		}
		task.PendingMemories = nil
		if warning {
			appendTaskEvent(&task, runtime.warningEvent(task, "memory.degraded"))
		}
		var err error
		task, err = runtime.saveTask(ctx, task)
		if err != nil {
			return task, false, err
		}
	}
	if task.PendingOperationID == "" {
		action := controlplane.SubmitActionInput{
			HostID: task.HostID, WorldID: task.WorldID, Request: *task.PendingAction,
			ParentOperationID: task.MacroOperationID,
		}
		var view controlplane.OperationView
		var err error
		if task.PendingAction.PlanStep == nil {
			view, err = runtime.control.SubmitAction(ctx, runtime.principal, action)
		} else if runtime.plans == nil {
			err = errors.New("task plan coordinator is unavailable")
		} else {
			plan, planErr := runtime.plans.GetPlan(ctx, task.PendingAction.PlanStep.PlanID)
			if planErr != nil {
				err = planErr
			} else {
				view, err = runtime.plans.SubmitStepAction(ctx, taskstate.SubmitStepActionInput{
					Action: action, ConditionIDs: operationOutcomeConditionIDs(plan),
				})
			}
		}
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
				errors.Is(err, controlplane.ErrUnavailable) || errors.Is(err, controlplane.ErrPersistence) {
				paused, pauseErr := runtime.pauseTask(ctx, task, "action.submit-unavailable", err)
				return paused, false, pauseErr
			}
			if errors.Is(err, controlplane.ErrStale) || errors.Is(err, controlplane.ErrLeaseExpired) ||
				errors.Is(err, controlplane.ErrForbidden) || errors.Is(err, controlplane.ErrInvalid) {
				clearPendingTaskAction(&task)
				task.Step++
				appendTaskEvent(&task, TaskEvent{
					Kind: "action.rejected", Step: task.Step,
					Code:         actionSubmissionRejectionCode(err),
					AtUnixMillis: runtime.now().UnixMilli(),
				})
				saved, saveErr := runtime.saveTask(ctx, task)
				return saved, saveErr == nil, errors.Join(err, saveErr)
			}
			failed, failErr := runtime.failTask(ctx, task, "action.submit-conflict", err)
			return failed, false, failErr
		}
		task.PendingOperationID = view.OperationID
		if view.Status == controlplane.OperationAwaitingConfirmation {
			task.Status = TaskWaitingConfirmation
		}
		appendTaskEvent(&task, operationTimelineEvent(
			task, "operation.submitted", view, runtime.now().UnixMilli(),
		))
		saved, saveErr := runtime.saveTask(ctx, task)
		return saved, saveErr == nil, saveErr
	}
	cancelling := task.Status == TaskCancelling
	var view controlplane.OperationView
	var err error
	if cancelling {
		view, err = runtime.control.CancelOperation(runtime.principal, task.PendingOperationID)
	} else {
		view, err = runtime.control.GetOperation(runtime.principal, task.PendingOperationID)
	}
	if err != nil {
		if cancelling {
			return task, false, err
		}
		paused, pauseErr := runtime.pauseTask(ctx, task, "operation.unavailable", err)
		return paused, false, pauseErr
	}
	if operationRequiresReconciliation(view) {
		return runtime.recordUnknownOperation(ctx, task, "operation.unknown", view)
	}
	if task.Status == TaskWaitingConfirmation &&
		view.Status != controlplane.OperationAwaitingConfirmation {
		task.Status = TaskActive
		task.PauseCode = ""
		var saveErr error
		task, saveErr = runtime.saveTask(ctx, task)
		if saveErr != nil {
			return task, false, saveErr
		}
	}
	if task.PendingActionMacro && macroOperationStarted(view) {
		return runtime.activatePendingMacro(ctx, task, view)
	}
	if !view.Terminal && view.Status != controlplane.OperationAwaitingConfirmation {
		update, waitErr := runtime.control.WaitOperation(ctx, runtime.principal, controlplane.WaitOperationInput{
			OperationID: view.OperationID, AfterCursor: view.Cursor,
			WaitMillis: runtime.operationWaitMillis,
		})
		if waitErr != nil {
			return task, false, waitErr
		}
		view = update.Operation
		if !update.Changed && !view.Terminal {
			return task, false, nil
		}
	}
	if operationRequiresReconciliation(view) {
		return runtime.recordUnknownOperation(ctx, task, "operation.unknown", view)
	}
	if view.Status == controlplane.OperationAwaitingConfirmation {
		if task.Status != TaskWaitingConfirmation {
			task.Status = TaskWaitingConfirmation
			saved, saveErr := runtime.saveTask(ctx, task)
			return saved, false, saveErr
		}
		return task, false, nil
	}
	if task.PendingActionMacro && macroOperationStarted(view) {
		return runtime.activatePendingMacro(ctx, task, view)
	}
	if !view.Terminal {
		if task.Status == TaskWaitingConfirmation {
			task.Status = TaskActive
			saved, saveErr := runtime.saveTask(ctx, task)
			return saved, saveErr == nil, saveErr
		}
		return task, false, nil
	}
	if operationOutcomeIsUnknown(view) {
		return runtime.recordUnknownOperation(ctx, task, "operation.unknown", view)
	}
	warning := false
	if view.Outcome != nil && !runtime.outcomesRecordedByControl {
		warning = runtime.appendOutcomeMemory(ctx, task, view, "outcome")
	}
	if cancelling && task.MacroOperationID == "" {
		task.Status = TaskCancelled
	} else if cancelling {
		task.Status = TaskCancelling
	} else {
		task.Status = TaskActive
	}
	task.PauseCode = ""
	clearPendingTaskAction(&task)
	task.Step++
	if task.PlanID != "" {
		_, task, err = runtime.loadTaskPlan(ctx, task)
		if err != nil {
			paused, pauseErr := runtime.pauseTask(ctx, task, "plan.unavailable", err)
			return paused, false, pauseErr
		}
	}
	if task.Status == TaskCancelled {
		task, err = runtime.cancelOwnedPlan(ctx, task, "The owning task was cancelled.")
		if err != nil {
			return task, false, err
		}
	}
	code := string(view.Status)
	if view.Outcome != nil {
		code = string(view.Outcome.Status)
	}
	appendTaskEvent(&task, operationTimelineEvent(
		task, "operation.terminal", view, runtime.now().UnixMilli(),
	))
	if cancelling && task.Status == TaskCancelled {
		appendTaskEvent(&task, TaskEvent{
			Kind: "task.cancelled", Step: task.Step, Code: code,
			OperationID: view.OperationID, AtUnixMillis: runtime.now().UnixMilli(),
		})
	}
	if warning {
		appendTaskEvent(&task, runtime.warningEvent(task, "memory.degraded"))
	}
	saved, saveErr := runtime.saveTask(ctx, task)
	if saveErr == nil && task.Status == TaskCancelled {
		runtime.releaseController(saved)
	}
	return saved, saveErr == nil, saveErr
}

func actionSubmissionRejectionCode(err error) string {
	switch {
	case errors.Is(err, controlplane.ErrStale):
		return "gateway.stale"
	case errors.Is(err, controlplane.ErrLeaseExpired):
		return "gateway.lease-expired"
	case errors.Is(err, controlplane.ErrForbidden):
		return "gateway.forbidden"
	case errors.Is(err, controlplane.ErrInvalid):
		return "gateway.invalid"
	default:
		return "gateway.rejected"
	}
}

func (runtime *AgentRuntime) activatePendingMacro(
	ctx context.Context,
	task TaskSession,
	view controlplane.OperationView,
) (TaskSession, bool, error) {
	task.MacroOperationID = view.OperationID
	clearPendingTaskAction(&task)
	if task.Status != TaskCancelling {
		task.Status = TaskActive
	}
	task.PauseCode = ""
	task.Step++
	appendTaskEvent(&task, operationTimelineEvent(
		task, "macro.started", view, runtime.now().UnixMilli(),
	))
	saved, err := runtime.saveTask(ctx, task)
	return saved, err == nil, err
}

func macroOperationStarted(view controlplane.OperationView) bool {
	return !view.Terminal &&
		(view.Status == controlplane.OperationAccepted ||
			view.Status == controlplane.OperationRunning)
}

func (runtime *AgentRuntime) advanceMacroOperation(
	ctx context.Context,
	task TaskSession,
) (TaskSession, bool, error) {
	cancelling := task.Status == TaskCancelling
	var view controlplane.OperationView
	var err error
	if cancelling {
		view, err = runtime.control.CancelOperation(runtime.principal, task.MacroOperationID)
	} else {
		view, err = runtime.control.GetOperation(runtime.principal, task.MacroOperationID)
	}
	if err != nil {
		if cancelling {
			return task, false, err
		}
		paused, pauseErr := runtime.pauseTask(ctx, task, "operation.unavailable", err)
		return paused, false, pauseErr
	}
	if operationRequiresReconciliation(view) {
		return runtime.recordUnknownOperation(ctx, task, "macro.unknown", view)
	}
	if !view.Terminal && !cancelling &&
		view.Status != controlplane.OperationAwaitingConfirmation &&
		view.Status != controlplane.OperationAccepted &&
		view.Status != controlplane.OperationRunning {
		update, waitErr := runtime.control.WaitOperation(
			ctx,
			runtime.principal,
			controlplane.WaitOperationInput{
				OperationID: view.OperationID,
				AfterCursor: view.Cursor,
				WaitMillis:  runtime.operationWaitMillis,
			},
		)
		if waitErr != nil {
			return task, false, waitErr
		}
		view = update.Operation
		if !update.Changed && !view.Terminal {
			return task, false, nil
		}
	}
	if operationRequiresReconciliation(view) {
		return runtime.recordUnknownOperation(ctx, task, "macro.unknown", view)
	}
	if view.Status == controlplane.OperationAwaitingConfirmation {
		if task.Status != TaskWaitingConfirmation {
			task.Status = TaskWaitingConfirmation
			task.PauseCode = ""
			saved, saveErr := runtime.saveTask(ctx, task)
			return saved, false, saveErr
		}
		return task, false, nil
	}
	if !view.Terminal {
		if cancelling {
			return task, false, nil
		}
		if view.Status != controlplane.OperationAccepted &&
			view.Status != controlplane.OperationRunning {
			return task, false, nil
		}
		if task.Status == TaskWaitingConfirmation {
			task.Status = TaskActive
			task.PauseCode = ""
			var saveErr error
			task, saveErr = runtime.saveTask(ctx, task)
			if saveErr != nil {
				return task, false, saveErr
			}
		}
		return task, true, nil
	}
	if operationOutcomeIsUnknown(view) {
		return runtime.recordUnknownOperation(ctx, task, "macro.unknown", view)
	}
	warning := false
	if view.Outcome != nil && !runtime.outcomesRecordedByControl {
		warning = runtime.appendOutcomeMemory(ctx, task, view, "macro-outcome")
	}
	operationID := task.MacroOperationID
	task.MacroOperationID = ""
	if cancelling {
		task.Status = TaskCancelled
	} else {
		task.Status = TaskActive
	}
	if task.PlanID != "" {
		_, task, err = runtime.loadTaskPlan(ctx, task)
		if err != nil {
			return task, false, err
		}
	}
	if cancelling {
		task, err = runtime.cancelOwnedPlan(ctx, task, "The owning macro task was cancelled.")
		if err != nil {
			return task, false, err
		}
	}
	task.PauseCode = ""
	appendTaskEvent(&task, operationTimelineEvent(
		task, "macro.terminal", view, runtime.now().UnixMilli(),
	))
	if view.Outcome != nil {
		task.History[len(task.History)-1].Code = string(view.Outcome.Status)
		task.History[len(task.History)-1].Summary = view.Outcome.Summary
	}
	if cancelling {
		appendTaskEvent(&task, TaskEvent{
			Kind: "task.cancelled", Step: task.Step, Code: string(view.Status),
			OperationID: operationID, AtUnixMillis: runtime.now().UnixMilli(),
		})
	}
	if warning {
		appendTaskEvent(&task, runtime.warningEvent(task, "memory.degraded"))
	}
	saved, saveErr := runtime.saveTask(ctx, task)
	if saveErr == nil && cancelling {
		runtime.releaseController(saved)
	}
	return saved, saveErr == nil && !cancelling, saveErr
}

func (runtime *AgentRuntime) finishCancelledTask(
	ctx context.Context,
	task TaskSession,
	code string,
) (TaskSession, error) {
	var err error
	task, err = runtime.cancelOwnedPlan(ctx, task, "The owning task was cancelled before execution.")
	if err != nil {
		return task, err
	}
	task.Status = TaskCancelled
	task.PauseCode = ""
	clearPendingTaskAction(&task)
	task.MacroOperationID = ""
	appendTaskEvent(&task, TaskEvent{
		Kind: "task.cancelled", Step: task.Step, Code: code,
		AtUnixMillis: runtime.now().UnixMilli(),
	})
	saved, err := runtime.saveTask(ctx, task)
	if err == nil {
		runtime.releaseController(saved)
	}
	return saved, err
}

func (runtime *AgentRuntime) ensureController(
	ctx context.Context,
	task TaskSession,
) (TaskSession, error) {
	if err := ctx.Err(); err != nil {
		return task, err
	}
	actor, err := runtime.control.GetActor(
		runtime.principal, task.HostID, task.WorldID, task.ActorID,
	)
	if err != nil || !actor.Online {
		if err == nil {
			err = controlplane.ErrUnavailable
		}
		return runtime.pauseTask(ctx, task, "controller.unavailable", err)
	}
	target := controlplane.ActorControlTarget{
		HostID: task.HostID, WorldID: task.WorldID, ActorID: task.ActorID,
	}
	now := runtime.now().UnixMilli()
	lease := task.ControllerLease
	needsRenewal := lease.Epoch != actor.Epoch ||
		lease.ExpiresAtUnixMillis-now <= int64(runtime.renewBeforeMillis)
	if !needsRenewal {
		return task, nil
	}
	lease, err = runtime.control.RenewController(
		runtime.principal, target, lease.LeaseID, runtime.controllerLeaseMillis,
	)
	if errors.Is(err, controlplane.ErrLeaseExpired) || errors.Is(err, controlplane.ErrNotFound) {
		if task.MacroOperationID != "" {
			return runtime.pauseTask(ctx, task, "controller.parent-lease-lost", err)
		}
		lease, err = runtime.control.AcquireController(runtime.principal, controlplane.AcquireControllerInput{
			ActorControlTarget: target, ControllerID: task.ControllerID,
			LeaseTTLMillis: runtime.controllerLeaseMillis,
		})
	}
	if err != nil {
		return runtime.pauseTask(ctx, task, "controller.unavailable", err)
	}
	if lease.Source != controlplane.DecisionInternal {
		return runtime.pauseTask(
			ctx, task, "controller.invalid", errors.New("controller source changed"),
		)
	}
	task.ControllerLease = lease
	return runtime.saveTask(ctx, task)
}

func (runtime *AgentRuntime) pauseTask(
	ctx context.Context,
	task TaskSession,
	code string,
	cause error,
) (TaskSession, error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return task, errors.Join(cause, ctxErr)
	}
	task.Status = TaskPaused
	task.PauseCode = code
	appendTaskEvent(&task, TaskEvent{
		Kind: "task.paused", Step: task.Step, Code: code,
		AtUnixMillis: runtime.now().UnixMilli(),
	})
	saved, err := runtime.saveTask(ctx, task)
	return saved, errors.Join(cause, err)
}

func (runtime *AgentRuntime) failTask(
	ctx context.Context,
	task TaskSession,
	code string,
	cause error,
) (TaskSession, error) {
	if task.MacroOperationID != "" {
		return runtime.pauseTask(ctx, task, code, cause)
	}
	var planErr error
	task, planErr = runtime.cancelOwnedPlan(ctx, task, "The owning task failed.")
	if planErr != nil {
		return task, errors.Join(cause, planErr)
	}
	task.Status = TaskFailed
	task.PauseCode = code
	clearPendingTaskAction(&task)
	appendTaskEvent(&task, TaskEvent{
		Kind: "task.failed", Step: task.Step, Code: code,
		AtUnixMillis: runtime.now().UnixMilli(),
	})
	saved, err := runtime.saveTask(ctx, task)
	if err == nil {
		runtime.releaseController(saved)
	}
	return saved, errors.Join(cause, err)
}

func (runtime *AgentRuntime) cancelOwnedPlan(
	ctx context.Context,
	task TaskSession,
	summary string,
) (TaskSession, error) {
	if task.PlanID == "" {
		return task, nil
	}
	if runtime.plans == nil {
		return task, errors.New("task plan coordinator is unavailable")
	}
	plan, err := runtime.plans.GetPlan(ctx, task.PlanID)
	if err != nil {
		return task, err
	}
	if plan.Status != taskstate.PlanCompleted && plan.Status != taskstate.PlanCancelled &&
		plan.Status != taskstate.PlanFailed {
		plan, err = runtime.plans.SetPlanStatus(ctx, taskstate.StatusInput{
			PlanID: plan.PlanID, ExpectedRevision: plan.Revision,
			Status: taskstate.PlanCancelled, Summary: summary,
		})
		if err != nil {
			return task, err
		}
	}
	task.PlanRevision = plan.Revision
	task.CurrentPlanStepID = plan.CurrentStepID
	return task, nil
}

func (runtime *AgentRuntime) saveTask(
	ctx context.Context,
	task TaskSession,
) (TaskSession, error) {
	task.UpdatedAtUnixMillis = runtime.now().UnixMilli()
	saved, err := runtime.tasks.CompareAndSwap(ctx, task.Revision, task)
	if err != nil {
		return task, err
	}
	runtime.notifyTaskChanged()
	return saved, nil
}

func (runtime *AgentRuntime) releaseController(task TaskSession) {
	target := controlplane.ActorControlTarget{
		HostID: task.HostID, WorldID: task.WorldID, ActorID: task.ActorID,
	}
	_ = runtime.control.ReleaseController(runtime.principal, target, task.ControllerLease.LeaseID)
}

func (runtime *AgentRuntime) warningEvent(task TaskSession, code string) TaskEvent {
	return TaskEvent{
		Kind: "provider.warning", Step: task.Step, Code: code,
		AtUnixMillis: runtime.now().UnixMilli(),
	}
}

func (runtime *AgentRuntime) stepID(task TaskSession, kind string) string {
	return task.TaskID + "." + kind + "." + strconv.FormatUint(uint64(task.Step+1), 10)
}

func (runtime *AgentRuntime) taskLock(taskID string) *sync.Mutex {
	runtime.taskLocksMu.Lock()
	defer runtime.taskLocksMu.Unlock()
	lock := runtime.taskLocks[taskID]
	if lock == nil {
		lock = &sync.Mutex{}
		runtime.taskLocks[taskID] = lock
	}
	return lock
}

func (runtime *AgentRuntime) taskChangedChannel() <-chan struct{} {
	runtime.timelineMu.Lock()
	defer runtime.timelineMu.Unlock()
	return runtime.taskChanged
}

func (runtime *AgentRuntime) notifyTaskChanged() {
	runtime.timelineMu.Lock()
	close(runtime.taskChanged)
	runtime.taskChanged = make(chan struct{})
	runtime.timelineMu.Unlock()
}

// ValidateStartTaskInput validates one public task request without starting it.
func ValidateStartTaskInput(input StartTaskInput) error {
	_, err := sealStartTaskInput(input)
	return err
}

// ValidateTaskID validates one task identifier used by application transports.
func ValidateTaskID(taskID string) error {
	return validateTaskID(taskID)
}

func validateTaskObservation(
	task TaskSession,
	actor controlplane.ActorView,
	observation host.ObservationEnvelope,
) error {
	if err := host.ValidateObservationEnvelope(observation); err != nil {
		return err
	}
	if observation.HostID != task.HostID || observation.WorldID != task.WorldID ||
		observation.ActorID != task.ActorID || observation.Epoch != actor.Epoch ||
		observation.Sequence != actor.ObservationSeq {
		return errors.New("observation does not match the current actor publication")
	}
	return nil
}

func prepareAgentCapabilities(
	catalog host.CapabilitySnapshot,
) ([]host.CapabilitySpec, []CapabilitySummary, error) {
	if len(catalog.Specs) > 128 {
		return nil, nil, ErrProviderCapacity
	}
	specs := make([]host.CapabilitySpec, len(catalog.Specs))
	summaries := make([]CapabilitySummary, len(catalog.Specs))
	for index, spec := range catalog.Specs {
		sealed, err := host.SealCapabilitySpec(spec)
		if err != nil {
			return nil, nil, fmt.Errorf("capability %d: %w", index, err)
		}
		if spec.Digest != "" && spec.Digest != sealed.Digest {
			return nil, nil, errors.New("capability digest changed during validation")
		}
		specs[index] = sealed
		summaries[index] = CapabilitySummaryFromSpec(sealed)
	}
	slices.SortFunc(specs, func(left, right host.CapabilitySpec) int {
		if left.Capability.ID != right.Capability.ID {
			return compareString(left.Capability.ID, right.Capability.ID)
		}
		return compareString(left.Capability.Version, right.Capability.Version)
	})
	slices.SortFunc(summaries, func(left, right CapabilitySummary) int {
		if left.Capability.ID != right.Capability.ID {
			return compareString(left.Capability.ID, right.Capability.ID)
		}
		return compareString(left.Capability.Version, right.Capability.Version)
	})
	if err := validateCapabilitySummaries(summaries); err != nil {
		return nil, nil, err
	}
	return specs, summaries, nil
}

func buildPendingModelMemories(
	task TaskSession,
	observation host.ObservationEnvelope,
	request host.ActionRequest,
	candidates []ModelMemoryCandidate,
) []MemoryRecord {
	return buildModelMemories(task, observation, request.RequestID, candidates)
}

func buildModelMemories(
	task TaskSession,
	observation host.ObservationEnvelope,
	sourceID string,
	candidates []ModelMemoryCandidate,
) []MemoryRecord {
	_, lookup, err := BuildModelObservation(observation)
	if err != nil {
		return nil
	}
	result := make([]MemoryRecord, 0, len(candidates))
	for index, candidate := range candidates {
		subjects := make([]string, 0, len(candidate.SubjectHandles))
		for _, handle := range candidate.SubjectHandles {
			ref, exists := lookup[handle]
			if !exists || ref.Ephemeral {
				continue
			}
			subjects = append(subjects, stableMemorySubject(ref))
		}
		slices.Sort(subjects)
		subjects = slices.Compact(subjects)
		var expiresAt *host.Timepoint
		if candidate.TTLSteps > 0 && observation.ObservedAt.Clock == host.ClockStep &&
			candidate.TTLSteps <= uint64(math.MaxInt64-observation.ObservedAt.Value) {
			value := host.Timepoint{
				Clock: host.ClockStep,
				Value: observation.ObservedAt.Value + int64(candidate.TTLSteps),
			}
			expiresAt = &value
		}
		result = append(result, MemoryRecord{
			MemoryID: task.TaskID + ".belief." + strconv.FormatUint(uint64(task.Step+1), 10) + "." + strconv.Itoa(index+1),
			Namespace: MemoryNamespace{
				SessionID: task.SessionID, ActorID: task.ActorID,
				ControllerID: task.ControllerID, Domain: MemoryControllerBelief,
			},
			Content: candidate.Content, SubjectRefs: subjects, Tags: candidate.Tags,
			Provenance: MemoryProvenance{
				Source: MemorySourceModel, SourceID: sourceID,
			},
			Confidence: candidate.Confidence, Importance: candidate.Importance,
			CreatedAt: observation.ObservedAt, ExpiresAt: expiresAt,
		})
	}
	return result
}

func (runtime *AgentRuntime) appendModelDecisionMemories(
	ctx context.Context,
	task TaskSession,
	observation host.ObservationEnvelope,
	sourceID string,
	candidates []ModelMemoryCandidate,
) (bool, error) {
	if len(candidates) == 0 {
		return false, nil
	}
	if runtime.memory == nil {
		return true, nil
	}
	warning := false
	for _, record := range buildModelMemories(task, observation, sourceID, candidates) {
		if _, err := runtime.memory.Append(ctx, record); err != nil {
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			warning = true
		}
	}
	return warning, nil
}

func (runtime *AgentRuntime) appendOutcomeMemory(
	ctx context.Context,
	task TaskSession,
	view controlplane.OperationView,
	kind string,
) bool {
	if runtime.memory == nil || view.Outcome == nil {
		return runtime.memory == nil
	}
	subjects := make([]string, 0, len(view.Outcome.Evidence))
	for _, ref := range view.Outcome.Evidence {
		if !ref.Ephemeral {
			subjects = append(subjects, stableMemorySubject(ref))
		}
	}
	slices.Sort(subjects)
	subjects = slices.Compact(subjects)
	importance := 0.5
	if view.Outcome.Status == host.ActionSucceeded {
		importance = 0.7
	}
	_, err := runtime.memory.Append(ctx, MemoryRecord{
		MemoryID: task.TaskID + "." + kind + "." +
			strconv.FormatUint(uint64(task.Step+1), 10),
		Namespace: MemoryNamespace{
			SessionID: task.SessionID, ActorID: task.ActorID, Domain: MemoryActorEpisodic,
		},
		Content: view.Outcome.Summary, SubjectRefs: subjects,
		SourceEventIDs: []string{view.OperationID},
		Provenance: MemoryProvenance{
			Source: MemorySourceHostOutcome, SourceID: view.OperationID, Authoritative: true,
		},
		Confidence: 1, Importance: importance, CreatedAt: view.Outcome.OccurredAt,
	})
	return err != nil
}

func clearPendingTaskAction(task *TaskSession) {
	task.PendingAction = nil
	task.PendingActionMacro = false
	task.PendingOperationID = ""
	task.PendingMemories = nil
}

func operationOutcomeIsUnknown(view controlplane.OperationView) bool {
	if view.ReconciliationPending || view.Status == controlplane.OperationOutcomeUnknown {
		return true
	}
	if view.Status == controlplane.OperationSucceeded {
		return !view.ExecutionConfirmed || view.Outcome == nil || view.Outcome.Status != host.ActionSucceeded
	}
	if view.Outcome != nil {
		return false
	}
	switch view.Status {
	case controlplane.OperationRejected:
		return false
	case controlplane.OperationStale, controlplane.OperationCancelled:
		return view.DeliveryAttempts != 0
	case controlplane.OperationFailed, controlplane.OperationInterrupted:
		return true
	default:
		return view.DeliveryAttempts != 0
	}
}

func operationRequiresReconciliation(view controlplane.OperationView) bool {
	return view.ReconciliationPending || view.Status == controlplane.OperationOutcomeUnknown
}

func (runtime *AgentRuntime) recordUnknownOperation(
	ctx context.Context,
	task TaskSession,
	eventKind string,
	view controlplane.OperationView,
) (TaskSession, bool, error) {
	task.Status = TaskOutcomeUnknown
	task.PauseCode = "operation.outcome-unknown"
	appendTaskEvent(&task, operationTimelineEvent(
		task, eventKind, view, runtime.now().UnixMilli(),
	))
	saved, err := runtime.saveTask(ctx, task)
	if err == nil {
		runtime.releaseController(saved)
	}
	return saved, false, err
}

func stableMemorySubject(ref host.HostRef) string {
	return ref.Namespace + ":" + ref.Type + ":" + ref.Key
}
