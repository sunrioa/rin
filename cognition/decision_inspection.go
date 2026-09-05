package cognition

import (
	"context"
	"errors"

	"github.com/sunrioa/rin/host"
)

// decideWithInspection owns the single extra model round shared by explicit
// inspection and argument-schema repair. Both paths retain their diagnostics.
func (runtime *AgentRuntime) decideWithInspection(ctx context.Context, task TaskSession, decisionContext *taskDecisionContext) (ModelDecision, TaskSession, error) {
	input := decisionContext.input
	decision, task, err := runtime.callModel(ctx, task, input, decisionContext.warnings)
	if err != nil {
		return decision, task, err
	}
	var capabilities []host.CapabilityRef
	var skills []SkillRef
	switch {
	case decision.Kind == ModelDecisionInspect:
		if err := validateRuntimeInspection(decision, decisionContext.summaries, input.Skills); err != nil {
			task, err = runtime.pauseTask(ctx, task, "model.invalid", err)
			return ModelDecision{}, task, err
		}
		capabilities, skills = decision.InspectCapabilities, decision.InspectSkills
	case actionArgumentsNeedSchemaRetry(decision, decisionContext.specs):
		capabilities = []host.CapabilityRef{decision.Capability}
		appendTaskEvent(&task, TaskEvent{
			Kind: "model.retry", Step: task.Step, Code: "arguments.schema",
			Summary:      "The selected capability arguments did not match its schema; retrying with the full capability contract.",
			AtUnixMillis: runtime.now().UnixMilli(),
		})
		task, err = runtime.saveTask(ctx, task)
		if err != nil {
			return ModelDecision{}, task, err
		}
	}
	if decision.Kind == ModelDecisionInspect || len(capabilities) != 0 {
		inspected, inspectErr := selectInspectedCapabilities(decisionContext.specs, capabilities)
		if inspectErr != nil {
			task, err = runtime.pauseTask(ctx, task, "capabilities.stale", inspectErr)
			return ModelDecision{}, task, err
		}
		input.InspectedCapabilities = inspected
		input.InspectedSkills = make([]Skill, 0, len(skills))
		for _, ref := range skills {
			if runtime.skills == nil {
				appendTaskEvent(&task, runtime.warningEvent(task, "skills.degraded"))
				break
			}
			skill, err := runtime.skills.DescribeSkill(ctx, ref.SkillID, ref.Version)
			if err != nil {
				appendTaskEvent(&task, runtime.warningEvent(task, "skills.degraded"))
				continue
			}
			input.InspectedSkills = append(input.InspectedSkills, skill)
		}
		input.InspectionRound = 1
		decision, task, err = runtime.callModel(ctx, task, input, nil)
		if err != nil {
			return ModelDecision{}, task, err
		}
		if decision.Kind == ModelDecisionInspect {
			task, err = runtime.pauseTask(ctx, task, "model.invalid", errors.New("model requested more than one inspection round"))
			return ModelDecision{}, task, err
		}
	}
	if err := validateRequiredPlanRevision(input, decision); err != nil {
		task, err = runtime.pauseTask(ctx, task, "plan.revision-required", err)
		return ModelDecision{}, task, err
	}
	return decision, task, nil
}
