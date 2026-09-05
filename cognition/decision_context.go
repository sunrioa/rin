package cognition

import (
	"context"
	"errors"
	"slices"

	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/taskstate"
)

type taskDecisionContext struct {
	observation host.ObservationEnvelope
	specs       []host.CapabilitySpec
	summaries   []CapabilitySummary
	input       ModelInput
	warnings    []TaskEvent
}

// collectDecisionContext gathers current Host evidence and bounded optional
// context. A nil result means the task paused or registered an observation wait.
func (runtime *AgentRuntime) collectDecisionContext(ctx context.Context, task TaskSession) (TaskSession, *taskDecisionContext, error) {
	var err error
	task, err = runtime.ensureController(ctx, task)
	if err != nil {
		return task, nil, err
	}
	actor, err := runtime.control.GetActor(
		runtime.principal, task.HostID, task.WorldID, task.ActorID,
	)
	if err != nil || !actor.Online {
		if err == nil {
			err = controlplane.ErrUnavailable
		}
		paused, pauseErr := runtime.pauseTask(ctx, task, "host.unavailable", err)
		return paused, nil, pauseErr
	}
	query := host.ObservationQuery{
		QueryID: runtime.stepID(task, "observe"), HostID: task.HostID,
		WorldID: task.WorldID, ActorID: task.ActorID, ExpectedEpoch: actor.Epoch, Limit: 256,
	}
	observation, err := runtime.environment.Observe(ctx, query)
	if err != nil {
		paused, pauseErr := runtime.pauseTask(ctx, task, "observation.unavailable", err)
		return paused, nil, pauseErr
	}
	if err := validateTaskObservation(task, actor, observation); err != nil {
		paused, pauseErr := runtime.pauseTask(ctx, task, "observation.invalid", err)
		return paused, nil, pauseErr
	}
	// A macro changes the Host-owned plan before its children can be selected.
	// Wait for that newer publication instead of binding a child to the snapshot
	// that originally started the macro.
	if task.MacroOperationID != "" && observation.Sequence <= task.LastObservationSeq {
		waitForObservation(&task, observation)
		saved, err := runtime.saveTask(ctx, task)
		return saved, nil, err
	}
	target := controlplane.ActorControlTarget{
		HostID: task.HostID, WorldID: task.WorldID, ActorID: task.ActorID,
	}
	catalog, err := runtime.environment.Capabilities(ctx, target)
	if err != nil {
		paused, pauseErr := runtime.pauseTask(ctx, task, "capabilities.unavailable", err)
		return paused, nil, pauseErr
	}
	specs, summaries, err := prepareAgentCapabilities(catalog)
	if err != nil {
		paused, pauseErr := runtime.pauseTask(ctx, task, "capabilities.invalid", err)
		return paused, nil, pauseErr
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
			return paused, nil, pauseErr
		}
	}
	task.LastObservationID = observation.ObservationID
	task.LastObservationSeq = observation.Sequence
	plan, task, err := runtime.loadTaskPlan(ctx, task)
	if err != nil {
		paused, pauseErr := runtime.pauseTask(ctx, task, "plan.unavailable", err)
		return paused, nil, pauseErr
	}
	plan, task, err = runtime.applyObservedPlanFacts(ctx, task, plan, observation)
	if err != nil {
		paused, pauseErr := runtime.pauseTask(ctx, task, "plan.evidence-unavailable", err)
		return paused, nil, pauseErr
	}
	refreshCompletionFacts(&task, observation)
	if task.CompletionRequested && task.Completion.Mode == CompletionEvidence {
		if taskCompletionSatisfied(task, observation.Epoch) && (plan == nil || plan.Status == taskstate.PlanCompleted) {
			completed, err := runtime.finishCompletedTask(ctx, task, "host-evidence", "The Host supplied the required completion evidence.")
			return completed, nil, err
		}
		task.CompletionRequested = false
	}
	persona, err := runtime.persona.Load(ctx, PersonaRequest{
		ActorID: task.ActorID, ControllerID: task.ControllerID,
	})
	if err != nil {
		paused, pauseErr := runtime.pauseTask(ctx, task, "persona.unavailable", err)
		return paused, nil, pauseErr
	}
	assembled, err := runtime.assembleOptionalModelContext(
		ctx, task, observation, summaries,
	)
	if err != nil {
		return task, nil, err
	}
	memories := assembled.memories
	skills := assembled.skills
	input := ModelInput{
		Task: ModelTaskContext{
			TaskID: task.TaskID, SessionID: task.SessionID, ActorID: task.ActorID,
			ControllerID: task.ControllerID, ParentOperationID: task.MacroOperationID,
			Goal: task.Goal, Tags: task.Tags, PlanningMode: task.PlanningMode, Completion: cloneTaskCompletion(task.Completion),
		},
		Persona: persona, Observation: observation, Memories: memories,
		Capabilities: summaries, Skills: skills, Plan: plan,
		AllowedReplanReason: allowedPlanRevisionReason(plan, observation.Epoch, summaries),
		LastOperationResult: task.LastOperationResult,
	}
	for _, signal := range task.PendingSignals {
		if signal.Epoch == observation.Epoch && signal.ExpiresAtUnixMillis > runtime.now().UnixMilli() {
			input.Task.Signals = append(input.Task.Signals, signal)
		}
	}
	return task, &taskDecisionContext{observation: observation, specs: specs, summaries: summaries, input: input, warnings: assembled.warnings}, nil
}
