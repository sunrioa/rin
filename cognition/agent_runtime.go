package cognition

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
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
}

type AgentRuntimeOptions struct {
	Principal   host.Principal
	Control     AgentControlPlane
	Environment AgentEnvironment
	Persona     PersonaProvider
	Memory      MemoryProvider
	Skills      SkillProvider
	Model       ModelProvider
	Tasks       TaskStore

	Now                   func() time.Time
	ControllerLeaseMillis uint32
	RenewBeforeMillis     uint32
	OperationWaitMillis   uint32
	MaxAdvancesPerRun     uint32
	MemoryBudget          MemoryBudget
}

type StartTaskInput struct {
	TaskID       string     `json:"task_id"`
	HostID       string     `json:"host_id"`
	WorldID      string     `json:"world_id"`
	ActorID      string     `json:"actor_id"`
	ControllerID string     `json:"controller_id"`
	Goal         string     `json:"goal"`
	Tags         []string   `json:"tags,omitempty"`
	Budget       TaskBudget `json:"budget"`
}

type AgentRuntime struct {
	principal   host.Principal
	control     AgentControlPlane
	environment AgentEnvironment
	persona     PersonaProvider
	memory      MemoryProvider
	skills      SkillProvider
	model       ModelProvider
	tasks       TaskStore

	now                   func() time.Time
	controllerLeaseMillis uint32
	renewBeforeMillis     uint32
	operationWaitMillis   uint32
	maxAdvancesPerRun     uint32
	memoryBudget          MemoryBudget

	taskLocksMu sync.Mutex
	taskLocks   map[string]*sync.Mutex
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
	return &AgentRuntime{
		principal: options.Principal, control: options.Control, environment: options.Environment,
		persona: options.Persona, memory: options.Memory, skills: options.Skills,
		model: options.Model, tasks: options.Tasks, now: options.Now,
		controllerLeaseMillis: options.ControllerLeaseMillis,
		renewBeforeMillis:     options.RenewBeforeMillis,
		operationWaitMillis:   options.OperationWaitMillis,
		maxAdvancesPerRun:     options.MaxAdvancesPerRun,
		memoryBudget:          options.MemoryBudget,
		taskLocks:             make(map[string]*sync.Mutex),
	}, nil
}

func (runtime *AgentRuntime) StartTask(
	ctx context.Context,
	input StartTaskInput,
) (TaskSession, error) {
	if err := requireMemoryContext(ctx); err != nil {
		return TaskSession{}, err
	}
	sealed, err := sealStartTaskInput(input)
	if err != nil {
		return TaskSession{}, err
	}
	actor, err := runtime.control.GetActor(
		runtime.principal, sealed.HostID, sealed.WorldID, sealed.ActorID,
	)
	if err != nil {
		return TaskSession{}, err
	}
	if !actor.Online {
		return TaskSession{}, controlplane.ErrUnavailable
	}
	target := controlplane.ActorControlTarget{
		HostID: sealed.HostID, WorldID: sealed.WorldID, ActorID: sealed.ActorID,
	}
	lease, err := runtime.control.AcquireController(runtime.principal, controlplane.AcquireControllerInput{
		ActorControlTarget: target, ControllerID: sealed.ControllerID,
		LeaseTTLMillis: runtime.controllerLeaseMillis,
	})
	if err != nil {
		return TaskSession{}, err
	}
	if lease.Source != controlplane.DecisionInternal {
		_ = runtime.control.ReleaseController(runtime.principal, target, lease.LeaseID)
		return TaskSession{}, errors.New("internal Agent Runtime acquired a non-internal controller lease")
	}
	now := runtime.now().UnixMilli()
	task := TaskSession{
		TaskID: sealed.TaskID, SessionID: actor.Epoch.SessionID,
		HostID: sealed.HostID, WorldID: sealed.WorldID, ActorID: sealed.ActorID,
		ControllerID: sealed.ControllerID, Goal: sealed.Goal, Tags: sealed.Tags,
		Status: TaskActive, Budget: sealed.Budget, ControllerLease: lease,
		CreatedAtUnixMillis: now, UpdatedAtUnixMillis: now,
	}
	appendTaskEvent(&task, TaskEvent{
		Kind: "task.created", Step: 0, Summary: "Task accepted by the internal Agent Runtime.",
		AtUnixMillis: now,
	})
	created, err := runtime.tasks.Create(ctx, task)
	if err != nil {
		_ = runtime.control.ReleaseController(runtime.principal, target, lease.LeaseID)
		return TaskSession{}, err
	}
	return created, nil
}

func (runtime *AgentRuntime) GetTask(
	ctx context.Context,
	taskID string,
) (TaskSession, error) {
	return runtime.tasks.Load(ctx, taskID)
}

func (runtime *AgentRuntime) ResumeTask(
	ctx context.Context,
	taskID string,
) (TaskSession, error) {
	lock := runtime.taskLock(taskID)
	lock.Lock()
	defer lock.Unlock()
	task, err := runtime.tasks.Load(ctx, taskID)
	if err != nil {
		return TaskSession{}, err
	}
	if task.Status != TaskPaused {
		return task, nil
	}
	task.Status = TaskActive
	task.PauseCode = ""
	task.UpdatedAtUnixMillis = runtime.now().UnixMilli()
	appendTaskEvent(&task, TaskEvent{
		Kind: "task.resumed", Step: task.Step, AtUnixMillis: task.UpdatedAtUnixMillis,
	})
	return runtime.saveTask(ctx, task)
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
			return task, nil
		}
		var keepRunning bool
		task, keepRunning, err = runtime.advanceTask(ctx, task)
		if err != nil || !keepRunning {
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
	persona, err := runtime.persona.Load(ctx, PersonaRequest{
		ActorID: task.ActorID, ControllerID: task.ControllerID,
	})
	if err != nil {
		paused, pauseErr := runtime.pauseTask(ctx, task, "persona.unavailable", err)
		return paused, false, pauseErr
	}
	warnings := make([]TaskEvent, 0, 2)
	memories := []MemoryMatch(nil)
	if runtime.memory != nil {
		memories, err = runtime.memory.Retrieve(ctx, MemoryQuery{
			SessionID: task.SessionID, ActorID: task.ActorID, ControllerID: task.ControllerID,
			Tags: task.Tags, Now: observation.ObservedAt, Budget: runtime.memoryBudget,
		})
		if err != nil {
			if ctx.Err() != nil {
				return task, false, ctx.Err()
			}
			memories = nil
			warnings = append(warnings, runtime.warningEvent(task, "memory.degraded"))
		}
	}
	skills := []SkillSummary(nil)
	if runtime.skills != nil {
		skills, err = runtime.skills.ListSkills(ctx, SkillQuery{Tags: task.Tags, Limit: 64})
		if err != nil {
			if ctx.Err() != nil {
				return task, false, ctx.Err()
			}
			skills = nil
			warnings = append(warnings, runtime.warningEvent(task, "skills.degraded"))
		}
	}
	task.LastObservationID = observation.ObservationID
	task.LastObservationSeq = observation.Sequence
	input := ModelInput{
		Task: ModelTaskContext{
			TaskID: task.TaskID, SessionID: task.SessionID, ActorID: task.ActorID,
			ControllerID: task.ControllerID, Goal: task.Goal, Tags: task.Tags,
		},
		Persona: persona, Observation: observation, Memories: memories,
		Capabilities: summaries, Skills: skills,
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

func (runtime *AgentRuntime) callModel(
	ctx context.Context,
	task TaskSession,
	input ModelInput,
	warnings []TaskEvent,
) (ModelDecision, TaskSession, error) {
	if task.ModelCalls >= task.Budget.MaxModelCalls {
		failed, err := runtime.failTask(ctx, task, "budget.model-calls", ErrTaskBudgetExceeded)
		return ModelDecision{}, failed, err
	}
	beforeCall := task
	decision, modelErr := runtime.model.Decide(ctx, input)
	if modelErr != nil && ctx.Err() != nil {
		return ModelDecision{}, beforeCall, ctx.Err()
	}
	task.ModelCalls++
	for _, warning := range warnings {
		appendTaskEvent(&task, warning)
	}
	if modelErr != nil {
		paused, pauseErr := runtime.pauseTask(ctx, task, "model.unavailable", modelErr)
		return ModelDecision{}, paused, pauseErr
	}
	usage, err := modelDecisionTokenUsage(decision)
	if err != nil || usage > math.MaxUint64-task.ModelTokens {
		if err == nil {
			err = errors.New("model token usage overflow")
		}
		paused, pauseErr := runtime.pauseTask(ctx, task, "model.invalid", err)
		return ModelDecision{}, paused, pauseErr
	}
	task.ModelTokens += usage
	if task.ModelTokens > task.Budget.MaxModelTokens {
		failed, failErr := runtime.failTask(ctx, task, "budget.model-tokens", ErrTaskBudgetExceeded)
		return ModelDecision{}, failed, failErr
	}
	appendTaskEvent(&task, TaskEvent{
		Kind: "model.decision", Step: task.Step, Code: string(decision.Kind),
		Summary: decision.Summary, AtUnixMillis: runtime.now().UnixMilli(),
	})
	saved, err := runtime.saveTask(ctx, task)
	return decision, saved, err
}

func (runtime *AgentRuntime) applyModelDecision(
	ctx context.Context,
	task TaskSession,
	observation host.ObservationEnvelope,
	capabilities []CapabilitySummary,
	decision ModelDecision,
) (TaskSession, bool, error) {
	validated, err := validateRuntimeFinalDecision(decision, observation, capabilities)
	if err != nil {
		paused, pauseErr := runtime.pauseTask(ctx, task, "model.invalid", err)
		return paused, false, pauseErr
	}
	decision = validated
	switch decision.Kind {
	case ModelDecisionWait:
		warning, err := runtime.appendModelDecisionMemories(
			ctx, task, observation, runtime.stepID(task, "decision"), decision.MemoryCandidates,
		)
		if err != nil {
			return task, false, err
		}
		task.Step++
		appendTaskEvent(&task, TaskEvent{
			Kind: "task.wait", Step: task.Step, Summary: decision.Summary,
			AtUnixMillis: runtime.now().UnixMilli(),
		})
		if warning {
			appendTaskEvent(&task, runtime.warningEvent(task, "memory.degraded"))
		}
		saved, err := runtime.saveTask(ctx, task)
		return saved, false, err
	case ModelDecisionComplete:
		warning, err := runtime.appendModelDecisionMemories(
			ctx, task, observation, runtime.stepID(task, "decision"), decision.MemoryCandidates,
		)
		if err != nil {
			return task, false, err
		}
		task.Status = TaskCompleted
		appendTaskEvent(&task, TaskEvent{
			Kind: "task.completed", Step: task.Step, Code: "model-declared",
			Summary: decision.Summary, AtUnixMillis: runtime.now().UnixMilli(),
		})
		if warning {
			appendTaskEvent(&task, runtime.warningEvent(task, "memory.degraded"))
		}
		saved, err := runtime.saveTask(ctx, task)
		if err == nil {
			runtime.releaseController(saved)
		}
		return saved, false, err
	case ModelDecisionAction:
		if task.ActionCount >= task.Budget.MaxActions {
			failed, err := runtime.failTask(ctx, task, "budget.actions", ErrTaskBudgetExceeded)
			return failed, false, err
		}
		summary, exists := findCapabilitySummary(capabilities, decision.Capability)
		if !exists {
			err := errors.New("model action capability is no longer available")
			paused, pauseErr := runtime.pauseTask(ctx, task, "model.invalid", err)
			return paused, false, pauseErr
		}
		targets, err := ResolveModelTargetHandles(observation, decision.TargetHandles)
		if err != nil {
			paused, pauseErr := runtime.pauseTask(ctx, task, "model.invalid", err)
			return paused, false, pauseErr
		}
		requestID := runtime.stepID(task, "action")
		request := host.ActionRequest{
			RequestID: requestID, ControllerID: task.ControllerID, ActorID: task.ActorID,
			Capability: decision.Capability, SpecDigest: summary.SpecDigest,
			Arguments: append(json.RawMessage(nil), decision.Arguments...), Targets: targets,
			ExpectedEpoch: observation.Epoch, ObservationSeq: observation.Sequence,
			TaskID: task.TaskID, IdempotencyKey: requestID,
		}
		if err := host.ValidateActionRequest(request); err != nil {
			paused, pauseErr := runtime.pauseTask(ctx, task, "model.invalid", err)
			return paused, false, pauseErr
		}
		task.PendingAction = &request
		task.PendingMemories = buildPendingModelMemories(task, observation, request, decision.MemoryCandidates)
		task.ActionCount++
		appendTaskEvent(&task, TaskEvent{
			Kind: "action.selected", Step: task.Step, Code: decision.Capability.ID,
			Summary: decision.Summary, AtUnixMillis: runtime.now().UnixMilli(),
		})
		saved, err := runtime.saveTask(ctx, task)
		return saved, err == nil, err
	default:
		err := errors.New("model returned an unsupported final decision")
		paused, pauseErr := runtime.pauseTask(ctx, task, "model.invalid", err)
		return paused, false, pauseErr
	}
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
		view, err := runtime.control.SubmitAction(ctx, runtime.principal, controlplane.SubmitActionInput{
			HostID: task.HostID, WorldID: task.WorldID, Request: *task.PendingAction,
		})
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
				errors.Is(err, controlplane.ErrUnavailable) || errors.Is(err, controlplane.ErrPersistence) {
				paused, pauseErr := runtime.pauseTask(ctx, task, "action.submit-unavailable", err)
				return paused, false, pauseErr
			}
			if errors.Is(err, controlplane.ErrStale) || errors.Is(err, controlplane.ErrLeaseExpired) ||
				errors.Is(err, controlplane.ErrForbidden) || errors.Is(err, controlplane.ErrInvalid) {
				task.PendingAction = nil
				task.Step++
				appendTaskEvent(&task, TaskEvent{
					Kind: "action.rejected", Step: task.Step, Code: "gateway-rejected",
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
		appendTaskEvent(&task, TaskEvent{
			Kind: "operation.submitted", Step: task.Step, Code: string(view.Status),
			OperationID: view.OperationID, AtUnixMillis: runtime.now().UnixMilli(),
		})
		saved, saveErr := runtime.saveTask(ctx, task)
		return saved, saveErr == nil, saveErr
	}
	view, err := runtime.control.GetOperation(runtime.principal, task.PendingOperationID)
	if err != nil {
		paused, pauseErr := runtime.pauseTask(ctx, task, "operation.unavailable", err)
		return paused, false, pauseErr
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
	if view.Status == controlplane.OperationAwaitingConfirmation {
		if task.Status != TaskWaitingConfirmation {
			task.Status = TaskWaitingConfirmation
			saved, saveErr := runtime.saveTask(ctx, task)
			return saved, false, saveErr
		}
		return task, false, nil
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
		task.Status = TaskOutcomeUnknown
		task.PauseCode = "operation.outcome-unknown"
		appendTaskEvent(&task, TaskEvent{
			Kind: "operation.unknown", Step: task.Step, Code: string(view.Status),
			OperationID: view.OperationID, AtUnixMillis: runtime.now().UnixMilli(),
		})
		saved, saveErr := runtime.saveTask(ctx, task)
		if saveErr == nil {
			runtime.releaseController(saved)
		}
		return saved, false, saveErr
	}
	warning := false
	if view.Outcome != nil {
		warning = runtime.appendOutcomeMemory(ctx, task, view)
	}
	task.Status = TaskActive
	task.PauseCode = ""
	task.PendingAction = nil
	task.PendingOperationID = ""
	task.PendingMemories = nil
	task.Step++
	code := string(view.Status)
	summary := view.RejectionMessage
	if view.Outcome != nil {
		code = string(view.Outcome.Status)
		summary = view.Outcome.Summary
	}
	appendTaskEvent(&task, TaskEvent{
		Kind: "operation.terminal", Step: task.Step, Code: code, Summary: summary,
		OperationID: view.OperationID, AtUnixMillis: runtime.now().UnixMilli(),
	})
	if warning {
		appendTaskEvent(&task, runtime.warningEvent(task, "memory.degraded"))
	}
	saved, saveErr := runtime.saveTask(ctx, task)
	return saved, saveErr == nil, saveErr
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
	task.Status = TaskFailed
	task.PauseCode = code
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

func (runtime *AgentRuntime) saveTask(
	ctx context.Context,
	task TaskSession,
) (TaskSession, error) {
	task.UpdatedAtUnixMillis = runtime.now().UnixMilli()
	saved, err := runtime.tasks.CompareAndSwap(ctx, task.Revision, task)
	if err != nil {
		return task, err
	}
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

func sealStartTaskInput(input StartTaskInput) (StartTaskInput, error) {
	if err := validateTaskID(input.TaskID); err != nil {
		return StartTaskInput{}, err
	}
	for field, value := range map[string]string{
		"host_id": input.HostID, "world_id": input.WorldID,
		"actor_id": input.ActorID, "controller_id": input.ControllerID,
	} {
		if err := validateProviderID(field, value); err != nil {
			return StartTaskInput{}, err
		}
	}
	if err := validateProviderText("goal", input.Goal, 2_000, true); err != nil {
		return StartTaskInput{}, err
	}
	var err error
	if input.Tags, err = normalizeProviderIDs("tags", input.Tags, 32); err != nil {
		return StartTaskInput{}, err
	}
	if input.Budget, err = normalizeTaskBudget(input.Budget); err != nil {
		return StartTaskInput{}, err
	}
	return input, nil
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

func selectInspectedCapabilities(
	specs []host.CapabilitySpec,
	refs []host.CapabilityRef,
) ([]host.CapabilitySpec, error) {
	result := make([]host.CapabilitySpec, 0, len(refs))
	for _, ref := range refs {
		found := false
		for _, spec := range specs {
			if spec.Capability == ref {
				result = append(result, cloneCapabilitySpecForModel(spec))
				found = true
				break
			}
		}
		if !found {
			return nil, ErrProviderNotFound
		}
	}
	return result, nil
}

func validateRuntimeInspection(
	decision ModelDecision,
	capabilities []CapabilitySummary,
	skills []SkillSummary,
) error {
	if len(decision.InspectCapabilities)+len(decision.InspectSkills) == 0 ||
		len(decision.InspectCapabilities) > 4 || len(decision.InspectSkills) > 1 {
		return errors.New("model inspection selection exceeds its bounds")
	}
	seenCapabilities := make(map[host.CapabilityRef]struct{}, len(decision.InspectCapabilities))
	for _, ref := range decision.InspectCapabilities {
		if _, duplicate := seenCapabilities[ref]; duplicate ||
			!containsCapabilitySummary(capabilities, ref, "") {
			return errors.New("model inspected a capability outside the advertised catalog")
		}
		seenCapabilities[ref] = struct{}{}
	}
	seenSkills := make(map[string]struct{}, len(decision.InspectSkills))
	for _, ref := range decision.InspectSkills {
		key := providerKey(ref.SkillID, ref.Version)
		if _, duplicate := seenSkills[key]; duplicate || !containsSkillSummary(skills, ref) {
			return errors.New("model inspected a skill outside the advertised catalog")
		}
		seenSkills[key] = struct{}{}
	}
	return nil
}

func validateRuntimeFinalDecision(
	decision ModelDecision,
	observation host.ObservationEnvelope,
	capabilities []CapabilitySummary,
) (ModelDecision, error) {
	if err := validateProviderText("model.summary", decision.Summary, 500, true); err != nil {
		return ModelDecision{}, err
	}
	view, _, err := BuildModelObservation(observation)
	if err != nil {
		return ModelDecision{}, err
	}
	allowedTargets := make([]string, 0, len(view.Targets))
	for _, target := range view.Targets {
		allowedTargets = append(allowedTargets, target.HandleID)
	}
	decision.MemoryCandidates, err = validateModelMemoryCandidates(
		decision.MemoryCandidates, allowedTargets,
	)
	if err != nil {
		return ModelDecision{}, err
	}
	switch decision.Kind {
	case ModelDecisionAction:
		if !containsCapabilitySummary(capabilities, decision.Capability, "") ||
			len(decision.InspectCapabilities) != 0 || len(decision.InspectSkills) != 0 {
			return ModelDecision{}, errors.New("model action is outside the advertised contract")
		}
		if _, err := ResolveModelTargetHandles(observation, decision.TargetHandles); err != nil {
			return ModelDecision{}, err
		}
		if len(decision.Arguments) == 0 {
			return ModelDecision{}, errors.New("model action arguments are missing")
		}
	case ModelDecisionWait, ModelDecisionComplete:
		if decision.Capability != (host.CapabilityRef{}) || len(decision.Arguments) != 0 ||
			len(decision.TargetHandles) != 0 || len(decision.InspectCapabilities) != 0 ||
			len(decision.InspectSkills) != 0 {
			return ModelDecision{}, errors.New("non-action model decision contains action selections")
		}
	default:
		return ModelDecision{}, errors.New("model returned an unsupported final decision")
	}
	return decision, nil
}

func modelDecisionTokenUsage(decision ModelDecision) (uint64, error) {
	if decision.Usage.PromptTokens < 0 || decision.Usage.CompletionTokens < 0 ||
		decision.Usage.TotalTokens < 0 {
		return 0, errors.New("model returned negative token usage")
	}
	total := decision.Usage.TotalTokens
	if total == 0 {
		if decision.Usage.PromptTokens > math.MaxInt-decision.Usage.CompletionTokens {
			return 0, errors.New("model token usage overflow")
		}
		total = decision.Usage.PromptTokens + decision.Usage.CompletionTokens
	}
	return uint64(total), nil
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
		MemoryID: task.TaskID + ".outcome." + strconv.FormatUint(uint64(task.Step+1), 10),
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

func stableMemorySubject(ref host.HostRef) string {
	return ref.Namespace + ":" + ref.Type + ":" + ref.Key
}
