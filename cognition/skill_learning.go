package cognition

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/sunrioa/rin/experience"
	"github.com/sunrioa/rin/timeline"
)

type SkillPublishMode string

const (
	SkillPublishDraft   SkillPublishMode = "draft"
	SkillPublishLearned SkillPublishMode = "learned"
)

type SkillLearningOptions struct {
	Generator  experience.DraftGenerator
	Drafts     SkillWriter
	Learned    SkillWriter
	Mode       SkillPublishMode
	MinActions uint32
	Adapter    string
}

type skillLearningRuntime struct {
	generator  experience.DraftGenerator
	writer     SkillWriter
	mode       SkillPublishMode
	minActions uint32
	adapter    string
}

func normalizeSkillLearningOptions(
	options *SkillLearningOptions,
) (*skillLearningRuntime, error) {
	if options == nil {
		return nil, nil
	}
	if options.Generator == nil {
		return nil, errors.New("skill learning requires a draft generator")
	}
	mode := options.Mode
	if mode == "" {
		mode = SkillPublishDraft
	}
	var writer SkillWriter
	switch mode {
	case SkillPublishDraft:
		writer = options.Drafts
	case SkillPublishLearned:
		writer = options.Learned
	default:
		return nil, errors.New("skill learning publish mode is invalid")
	}
	if writer == nil {
		return nil, errors.New("skill learning target writer is required")
	}
	minimum := options.MinActions
	if minimum == 0 {
		minimum = 3
	}
	if minimum > 100 {
		return nil, errors.New("skill learning minimum actions is too large")
	}
	if options.Adapter != "" {
		if err := validateProviderID("skill learning adapter", options.Adapter); err != nil {
			return nil, err
		}
	}
	return &skillLearningRuntime{
		generator: options.Generator, writer: writer, mode: mode,
		minActions: minimum, adapter: options.Adapter,
	}, nil
}

func (runtime *AgentRuntime) maybeLearnSkill(
	ctx context.Context,
	task TaskSession,
) (TaskSession, error) {
	if runtime.learning == nil || task.Status != TaskCompleted {
		return task, nil
	}
	if task.SkillLearning != nil && task.SkillLearning.Status != SkillLearningPending {
		return task, nil
	}
	if task.ActionCount < runtime.learning.minActions {
		return runtime.finishSkillLearning(ctx, task, SkillLearningState{
			Status: SkillLearningSkipped, Attempts: 1, Code: "below-threshold",
		})
	}
	attempts := uint32(1)
	if task.SkillLearning != nil {
		attempts = task.SkillLearning.Attempts + 1
		if attempts > 3 {
			return runtime.finishSkillLearning(ctx, task, SkillLearningState{
				Status: SkillLearningFailed, Attempts: 3, Code: "retry-exhausted",
			})
		}
	}
	task.SkillLearning = &SkillLearningState{
		Status: SkillLearningPending, Attempts: attempts,
	}
	var err error
	task, err = runtime.saveTask(ctx, task)
	if err != nil {
		return task, err
	}
	page, err := runtime.completeTaskTimeline(ctx, task.TaskID)
	if err != nil {
		return runtime.failSkillLearning(ctx, task, attempts, "timeline-unavailable")
	}
	episode, err := experience.Project(experience.ProjectionInput{
		ControllerKind: experience.ControllerInternal, Timeline: page, Tags: task.Tags,
	})
	if err != nil || episode.VerifiedResult == nil || !episode.VerifiedResult.Success {
		return runtime.finishSkillLearning(ctx, task, SkillLearningState{
			Status: SkillLearningSkipped, Attempts: attempts, Code: "unverified-result",
		})
	}
	draft, err := runtime.learning.generator.Generate(ctx, experience.DraftRequest{
		Episode: episode, SkillID: learnedSkillID(task.TaskID), Adapter: runtime.learning.adapter,
	})
	if err != nil {
		if ctx.Err() != nil {
			task.SkillLearning = nil
			return runtime.saveTask(context.Background(), task)
		}
		return runtime.failSkillLearning(ctx, task, attempts, "generation-failed")
	}
	skill, err := SealSkill(Skill{SkillSummary: SkillSummary{
		SkillID: draft.SkillID, Version: draft.Version, Summary: draft.Description,
		Triggers: draft.Triggers, Adapters: draft.Adapters,
		Capabilities: draft.Capabilities, Source: string(runtime.learning.mode),
	}, Instructions: draft.Instructions})
	if err != nil {
		return runtime.failSkillLearning(ctx, task, attempts, "draft-invalid")
	}
	if err := runtime.learning.writer.Save(ctx, skill); err != nil {
		return runtime.failSkillLearning(ctx, task, attempts, "write-failed")
	}
	status := SkillLearningDrafted
	if runtime.learning.mode == SkillPublishLearned {
		status = SkillLearningEnabled
	}
	return runtime.finishSkillLearning(ctx, task, SkillLearningState{
		Status: status, Attempts: attempts, SkillID: skill.SkillID, Digest: skill.Digest,
	})
}

func (runtime *AgentRuntime) completeTaskTimeline(
	ctx context.Context,
	taskID string,
) (timeline.Page, error) {
	query := timeline.Query{TaskID: taskID, Limit: timeline.MaximumLimit}
	var result timeline.Page
	for {
		page, err := runtime.GetTaskTimeline(ctx, query)
		if err != nil {
			return timeline.Page{}, err
		}
		if result.ContractVersion == "" {
			result = page
		} else {
			result.Events = append(result.Events, page.Events...)
			result.NextCursor = page.NextCursor
			result.More = page.More
		}
		if !page.More {
			return result, nil
		}
		query.AfterCursor = page.NextCursor
	}
}

func (runtime *AgentRuntime) failSkillLearning(
	ctx context.Context,
	task TaskSession,
	attempts uint32,
	code string,
) (TaskSession, error) {
	return runtime.finishSkillLearning(ctx, task, SkillLearningState{
		Status: SkillLearningFailed, Attempts: attempts, Code: code,
	})
}

func (runtime *AgentRuntime) finishSkillLearning(
	ctx context.Context,
	task TaskSession,
	state SkillLearningState,
) (TaskSession, error) {
	task.SkillLearning = &state
	appendTaskEvent(&task, TaskEvent{
		Kind: "skill.learning", Step: task.Step, Code: string(state.Status),
		Summary: state.SkillID, AtUnixMillis: runtime.now().UnixMilli(),
	})
	return runtime.saveTask(ctx, task)
}

func learnedSkillID(taskID string) string {
	digest := sha256.Sum256([]byte(taskID))
	return "learned." + hex.EncodeToString(digest[:8])
}
