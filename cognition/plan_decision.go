package cognition

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/taskstate"
	"github.com/sunrioa/rin/timeline"
)

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
	reserved := uint64(0)
	if task.Lookahead != nil {
		reserved = task.Lookahead.ReservedTokens
	}
	if task.ModelTokens >= task.Budget.MaxModelTokens || reserved >= task.Budget.MaxModelTokens-task.ModelTokens {
		failed, err := runtime.failTask(ctx, task, "budget.model-tokens", ErrTaskBudgetExceeded)
		return ModelDecision{}, failed, err
	}
	beforeCall := task
	startedAt := runtime.now()
	decision, modelErr := runtime.model.Decide(ctx, input)
	finishedAt := runtime.now()
	latency := uint64(0)
	if !finishedAt.Before(startedAt) {
		latency = uint64(finishedAt.Sub(startedAt).Milliseconds())
	}
	if ctx.Err() != nil {
		return ModelDecision{}, beforeCall, ctx.Err()
	}
	task.ModelCalls++
	for _, warning := range warnings {
		appendTaskEvent(&task, warning)
	}
	if modelErr != nil {
		memories, skills := modelContextTimelineFields(input)
		appendTaskEvent(&task, TaskEvent{
			Kind: "model.failed", Step: task.Step, Code: "model.unavailable",
			AtUnixMillis:        finishedAt.UnixMilli(),
			ObservationID:       input.Observation.ObservationID,
			ObservationSequence: input.Observation.Sequence,
			Epoch:               &input.Observation.Epoch, MemoryContextRefs: memories, SkillRefs: skills,
			Model: &timeline.ModelUsage{LatencyMillis: uint64Pointer(latency)},
		})
		paused, pauseErr := runtime.pauseTask(ctx, task, "model.unavailable", modelErr)
		return ModelDecision{}, paused, pauseErr
	}
	usage, err := modelDecisionTokenUsage(decision)
	if err != nil || usage > maxProviderWireInteger-task.ModelTokens {
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
	memories, skills := modelContextTimelineFields(input)
	if runtime.decisions != nil {
		record, recordErr := newDecisionRecord(
			task, input, decision, latency, finishedAt.UnixMilli(),
		)
		if recordErr == nil {
			recordErr = runtime.decisions.Append(context.Background(), record)
		}
		if recordErr != nil {
			appendTaskEvent(&task, runtime.warningEvent(task, "decision-record.degraded"))
		}
	}
	appendTaskEvent(&task, TaskEvent{
		Kind: "model.decision", Step: task.Step, Code: string(decision.Kind),
		Summary: decision.Summary, AtUnixMillis: finishedAt.UnixMilli(),
		ObservationID:       input.Observation.ObservationID,
		ObservationSequence: input.Observation.Sequence,
		Epoch:               &input.Observation.Epoch, MemoryContextRefs: memories, SkillRefs: skills,
		Model: measuredModelUsage(decision, latency),
	})
	saved, err := runtime.saveTask(ctx, task)
	return decision, saved, err
}

func (runtime *AgentRuntime) applyModelDecision(
	ctx context.Context,
	task TaskSession,
	observation host.ObservationEnvelope,
	specs []host.CapabilitySpec,
	capabilities []CapabilitySummary,
	decision ModelDecision,
) (TaskSession, bool, error) {
	validated, err := validateRuntimeFinalDecision(decision, observation, capabilities)
	if err != nil {
		paused, pauseErr := runtime.pauseTask(ctx, task, "model.invalid", err)
		return paused, false, pauseErr
	}
	decision = validated
	plan, task, err := runtime.applyDecisionPlan(ctx, task, observation, decision)
	if err != nil {
		paused, pauseErr := runtime.pauseTask(ctx, task, "plan.invalid", err)
		return paused, false, pauseErr
	}
	switch decision.Kind {
	case ModelDecisionWait:
		waitForObservation(&task, observation)
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
		if plan != nil && plan.Status != taskstate.PlanCompleted {
			err := errors.New("model cannot complete a task before its plan is complete")
			paused, pauseErr := runtime.pauseTask(ctx, task, "plan.incomplete", err)
			return paused, false, pauseErr
		}
		if task.MacroOperationID != "" {
			err := errors.New("model cannot complete a task while its macro is running")
			paused, pauseErr := runtime.pauseTask(ctx, task, "model.invalid", err)
			return paused, false, pauseErr
		}
		switch task.Completion.Mode {
		case CompletionHuman:
			task.CompletionRequested = true
			task.Status = TaskPaused
			task.PauseCode = "completion.confirmation-required"
			task.Schedule = TaskSchedule{Kind: ScheduleUser}
			appendTaskEvent(&task, TaskEvent{Kind: "task.completion-requested", Step: task.Step, Code: "human-confirmation", Summary: decision.Summary, AtUnixMillis: runtime.now().UnixMilli()})
			saved, err := runtime.saveTask(ctx, task)
			return saved, false, err
		case CompletionEvidence:
			refreshCompletionFacts(&task, observation)
			if !taskCompletionSatisfied(task, observation.Epoch) {
				task.CompletionRequested = true
				waitForObservation(&task, observation)
				task.Step++
				appendTaskEvent(&task, TaskEvent{Kind: "task.completion-unmet", Step: task.Step, Code: "host-evidence-required", Summary: "The completion request lacks the required Host evidence.", AtUnixMillis: runtime.now().UnixMilli()})
				saved, err := runtime.saveTask(ctx, task)
				return saved, false, err
			}
		}
		warning, err := runtime.appendModelDecisionMemories(ctx, task, observation, runtime.stepID(task, "decision"), decision.MemoryCandidates)
		if err != nil {
			return task, false, err
		}
		if warning {
			appendTaskEvent(&task, runtime.warningEvent(task, "memory.degraded"))
		}
		saved, err := runtime.finishCompletedTask(ctx, task, string(task.Completion.Mode), decision.Summary)
		return saved, false, err
	case ModelDecisionAction:
		if err := actionArgumentsSchemaError(decision, specs); err != nil {
			paused, pauseErr := runtime.pauseTask(ctx, task, "model.invalid", err)
			return paused, false, pauseErr
		}
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
		if plan != nil {
			if plan.Status != taskstate.PlanActive || plan.CurrentStepID == "" {
				err := errors.New("task plan has no active step")
				paused, pauseErr := runtime.pauseTask(ctx, task, "plan.blocked", err)
				return paused, false, pauseErr
			}
			request.PlanStep = &host.PlanStepRef{
				PlanID: plan.PlanID, PlanRevision: plan.Revision, StepID: plan.CurrentStepID,
			}
		}
		if err := host.ValidateActionRequest(request); err != nil {
			paused, pauseErr := runtime.pauseTask(ctx, task, "model.invalid", err)
			return paused, false, pauseErr
		}
		task.PendingAction = &request
		task.PendingActionMacro = summary.Kind == host.CapabilityMacro &&
			summary.ProducesChildOperations
		task.PendingMemories = buildPendingModelMemories(task, observation, request, decision.MemoryCandidates)
		task.ActionCount++
		appendTaskEvent(&task, TaskEvent{
			Kind: "action.selected", Step: task.Step, Code: decision.Capability.ID,
			Summary: decision.Summary, AtUnixMillis: runtime.now().UnixMilli(),
			ObservationID:       observation.ObservationID,
			ObservationSequence: observation.Sequence, Epoch: &observation.Epoch,
			Capability: actionCapabilityPointer(decision.Capability),
		})
		saved, err := runtime.saveTask(ctx, task)
		return saved, err == nil, err
	default:
		err := errors.New("model returned an unsupported final decision")
		paused, pauseErr := runtime.pauseTask(ctx, task, "model.invalid", err)
		return paused, false, pauseErr
	}
}

func actionArgumentsSchemaError(
	decision ModelDecision,
	specs []host.CapabilitySpec,
) error {
	if decision.Kind != ModelDecisionAction {
		return nil
	}
	for _, spec := range specs {
		if spec.Capability == decision.Capability {
			return spec.Input.ValidateInstance(decision.Arguments)
		}
	}
	return errors.New("model action capability is no longer available")
}

func actionArgumentsNeedSchemaRetry(
	decision ModelDecision,
	specs []host.CapabilitySpec,
) bool {
	if decision.Kind != ModelDecisionAction {
		return false
	}
	for _, spec := range specs {
		if spec.Capability == decision.Capability {
			return spec.Input.ValidateInstance(decision.Arguments) != nil
		}
	}
	return false
}

func (runtime *AgentRuntime) loadTaskPlan(
	ctx context.Context,
	task TaskSession,
) (*taskstate.PlanState, TaskSession, error) {
	if task.PlanID == "" && task.PlanningMode == taskstate.PlanningDisabled {
		return nil, task, nil
	}
	if runtime.plans == nil {
		return nil, task, errors.New("task plan coordinator is unavailable")
	}
	planID := task.PlanID
	if planID == "" {
		planID = "plan." + task.TaskID
	}
	plan, err := runtime.plans.GetPlan(ctx, planID)
	if errors.Is(err, taskstate.ErrNotFound) && task.PlanID == "" {
		return nil, task, nil
	}
	if err != nil {
		return nil, task, err
	}
	if plan.PlanID != planID || plan.TaskID != task.TaskID || plan.SessionID != task.SessionID ||
		plan.HostID != task.HostID || plan.WorldID != task.WorldID ||
		plan.ActorID != task.ActorID || plan.ControllerID != task.ControllerID ||
		plan.ControllerSource != taskstate.ControllerInternal || plan.Goal != task.Goal || plan.PlanningMode != task.PlanningMode {
		return nil, task, errors.New("task plan ownership does not match the internal task")
	}
	recovering := task.PlanID == ""
	task.PlanID = plan.PlanID
	task.PlanRevision = plan.Revision
	task.CurrentPlanStepID = plan.CurrentStepID
	if recovering {
		stepID := plan.CurrentStepID
		if stepID == "" {
			stepID = "plan.final"
		}
		appendTaskEvent(&task, TaskEvent{Kind: "plan.recovered", Step: task.Step, PlanID: plan.PlanID,
			PlanRevision: plan.Revision, PlanStepID: stepID, Summary: "Recovered the previously committed plan for this task.", AtUnixMillis: runtime.now().UnixMilli()})
		task, err = runtime.saveTask(ctx, task)
		if err != nil {
			return nil, task, err
		}
	}
	return &plan, task, nil
}

func (runtime *AgentRuntime) applyDecisionPlan(
	ctx context.Context,
	task TaskSession,
	observation host.ObservationEnvelope,
	decision ModelDecision,
) (*taskstate.PlanState, TaskSession, error) {
	if decision.PlanDraft == nil {
		if task.PlanID == "" && task.PlanningMode == taskstate.PlanningRequired {
			return nil, task, errors.New("required planning mode needs a plan draft")
		}
		return runtime.loadTaskPlan(ctx, task)
	}
	if task.PlanningMode == taskstate.PlanningDisabled || runtime.plans == nil {
		return nil, task, errors.New("model returned a plan when planning is unavailable")
	}
	draft := *decision.PlanDraft
	draft.TaskID = task.TaskID
	draft.SessionID = task.SessionID
	draft.HostID = task.HostID
	draft.WorldID = task.WorldID
	draft.ActorID = task.ActorID
	draft.ControllerID = task.ControllerID
	draft.ControllerSource = taskstate.ControllerInternal
	draft.Goal = task.Goal
	draft.PlanningMode = task.PlanningMode
	draft.BasedOnEpoch = observation.Epoch
	draft.BasedOnObservationSequence = observation.Sequence
	var plan taskstate.PlanState
	var err error
	if task.PlanID == "" {
		draft.PlanID = "plan." + task.TaskID
		plan, err = runtime.plans.CreatePlan(ctx, draft)
		if errors.Is(err, taskstate.ErrConflict) {
			return runtime.loadTaskPlan(ctx, task)
		}
	} else {
		draft.PlanID = task.PlanID
		plan, err = runtime.plans.RevisePlan(ctx, taskstate.ReviseInput{
			PlanID: task.PlanID, ExpectedRevision: task.PlanRevision,
			Reason: decision.ReplanReason, Summary: decision.Summary, Draft: draft,
		})
	}
	if err != nil {
		return nil, task, err
	}
	task.PlanID = plan.PlanID
	task.PlanRevision = plan.Revision
	task.CurrentPlanStepID = plan.CurrentStepID
	kind := "plan.created"
	if plan.ReplanCount != 0 {
		kind = "plan.revised"
	}
	appendTaskEvent(&task, TaskEvent{
		Kind: kind, Step: task.Step, Code: string(decision.ReplanReason),
		Summary: decision.Summary, AtUnixMillis: runtime.now().UnixMilli(),
		PlanID: plan.PlanID, PlanRevision: plan.Revision, PlanStepID: plan.CurrentStepID,
	})
	saved, saveErr := runtime.saveTask(ctx, task)
	if saveErr != nil {
		// The deterministic identity lets restart (or cancellation) recover this
		// committed plan. Cancelling it here would make a transient Task CAS
		// failure permanently destroy otherwise recoverable work.
		return nil, task, saveErr
	}
	return &plan, saved, nil
}

func allowedPlanRevisionReason(
	plan *taskstate.PlanState,
	epoch host.Epoch,
	capabilities []CapabilitySummary,
) taskstate.ReplanReason {
	if plan == nil {
		return ""
	}
	if plan.BasedOnEpoch != epoch {
		return taskstate.ReplanEpochInvalidated
	}
	available := make(map[host.CapabilityRef]struct{}, len(capabilities))
	for _, capability := range capabilities {
		available[capability.Capability] = struct{}{}
	}
	for _, step := range plan.Steps {
		if step.StepID != plan.CurrentStepID {
			continue
		}
		for _, capability := range step.CapabilityHints {
			if _, exists := available[capability]; !exists {
				return taskstate.ReplanRequiredCapabilityMissing
			}
		}
		break
	}
	if taskstate.ShouldReplan(
		taskstate.ReplanPolicy{FailureThreshold: 3, MaxReplans: plan.MaxReplans},
		taskstate.ReplanInput{
			Reason:              taskstate.ReplanFailureThresholdReached,
			ConsecutiveFailures: plan.ConsecutiveFailures,
			ReplanCount:         plan.ReplanCount, HasAuthoritativeProof: true,
		},
	) {
		return taskstate.ReplanFailureThresholdReached
	}
	return ""
}

func (runtime *AgentRuntime) applyObservedPlanFacts(
	ctx context.Context,
	task TaskSession,
	plan *taskstate.PlanState,
	observation host.ObservationEnvelope,
) (*taskstate.PlanState, TaskSession, error) {
	if plan == nil || runtime.plans == nil {
		return plan, task, nil
	}
	for applied := 0; applied < 32; applied++ {
		condition, fact, found := nextObservedPlanFact(*plan, observation.Facts)
		if !found {
			return plan, task, nil
		}
		evidenceStepID := plan.CurrentStepID
		if evidenceStepID == "" {
			evidenceStepID = "plan.final"
		}
		updated, err := runtime.plans.RequestTransition(ctx, taskstate.TransitionInput{
			PlanID: plan.PlanID, ExpectedRevision: plan.Revision,
			ConditionID: condition.ConditionID, Kind: taskstate.EvidenceObservationFact,
			EvidenceID: fact.FactID,
		})
		if err != nil {
			return plan, task, err
		}
		plan = &updated
		task.PlanRevision = updated.Revision
		task.CurrentPlanStepID = updated.CurrentStepID
		appendTaskEvent(&task, TaskEvent{
			Kind: "plan.evidence", Step: task.Step, Code: fact.FactID,
			Summary: condition.Summary, AtUnixMillis: runtime.now().UnixMilli(),
			ObservationID:       observation.ObservationID,
			ObservationSequence: observation.Sequence, Epoch: &observation.Epoch,
			PlanID: updated.PlanID, PlanRevision: updated.Revision,
			PlanStepID: evidenceStepID,
		})
		var saveErr error
		task, saveErr = runtime.saveTask(ctx, task)
		if saveErr != nil {
			return plan, task, saveErr
		}
	}
	return plan, task, errors.New("observation satisfied too many plan conditions in one advance")
}

func nextObservedPlanFact(
	plan taskstate.PlanState,
	facts []host.ObservationFact,
) (taskstate.PlanCondition, host.ObservationFact, bool) {
	conditions := append([]taskstate.PlanCondition(nil), plan.SuccessConditions...)
	for _, step := range plan.Steps {
		if step.StepID == plan.CurrentStepID {
			current := append([]taskstate.PlanCondition(nil), step.SuccessConditions...)
			conditions = append(current, conditions...)
			break
		}
	}
	for _, condition := range conditions {
		if planConditionHasEvidence(plan, condition.ConditionID) {
			continue
		}
		for _, fact := range facts {
			if taskstate.ObservationConditionMatches(condition, fact) {
				return condition, fact, true
			}
		}
	}
	return taskstate.PlanCondition{}, host.ObservationFact{}, false
}

func planConditionHasEvidence(plan taskstate.PlanState, conditionID string) bool {
	for _, evidence := range plan.Evidence {
		if evidence.ConditionID == conditionID {
			return true
		}
	}
	for _, step := range plan.Steps {
		for _, evidence := range step.Evidence {
			if evidence.ConditionID == conditionID {
				return true
			}
		}
	}
	return false
}

func validateRequiredPlanRevision(input ModelInput, decision ModelDecision) error {
	if decision.Kind == ModelDecisionInspect || input.AllowedReplanReason == "" {
		return nil
	}
	if decision.PlanDraft == nil || decision.ReplanReason != input.AllowedReplanReason {
		return errors.New("the current plan must be revised with the allowed replan reason")
	}
	return nil
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
		decision.Usage.TotalTokens < 0 || negativeOptionalInt(decision.Usage.PromptCacheHitTokens) ||
		negativeOptionalInt(decision.Usage.PromptCacheMissTokens) || negativeOptionalInt(decision.Usage.CacheWriteTokens) {
		return 0, errors.New("model returned negative token usage")
	}
	for _, count := range []*int{&decision.Usage.PromptTokens, &decision.Usage.CompletionTokens, &decision.Usage.TotalTokens,
		decision.Usage.PromptCacheHitTokens, decision.Usage.PromptCacheMissTokens, decision.Usage.CacheWriteTokens} {
		if count != nil && uint64(*count) > maxProviderWireInteger {
			return 0, errors.New("model token usage is not JSON-safe")
		}
	}
	prompt, completion := uint64(decision.Usage.PromptTokens), uint64(decision.Usage.CompletionTokens)
	if prompt > maxProviderWireInteger-completion {
		return 0, errors.New("model token usage overflow")
	}
	return max(uint64(decision.Usage.TotalTokens), prompt+completion), nil
}
