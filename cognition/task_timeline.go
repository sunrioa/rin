package cognition

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/sunrioa/rin/timeline"
)

// GetTaskTimeline projects the durable TaskSession history into the shared,
// read-only timeline contract. It does not mutate or advance the task.
func (runtime *AgentRuntime) GetTaskTimeline(
	ctx context.Context,
	query timeline.Query,
) (timeline.Page, error) {
	task, err := runtime.tasks.Load(ctx, query.TaskID)
	if err != nil {
		return timeline.Page{}, err
	}
	return projectTaskTimeline(task, query)
}

// WaitTaskTimeline waits for a newer durable task event. A timeout with no new
// event returns Changed=false and is not evidence of execution.
func (runtime *AgentRuntime) WaitTaskTimeline(
	ctx context.Context,
	input timeline.WaitInput,
) (timeline.Update, error) {
	input, after, err := timeline.NormalizeWait(input)
	if err != nil {
		return timeline.Update{}, err
	}
	timer := time.NewTimer(time.Duration(input.WaitMillis) * time.Millisecond)
	defer timer.Stop()
	for {
		changed := runtime.taskChangedChannel()
		page, err := runtime.GetTaskTimeline(ctx, input.Query())
		if err != nil {
			return timeline.Update{}, err
		}
		latest, err := timeline.ParseCursor(page.NextCursor)
		if err != nil {
			return timeline.Update{}, err
		}
		if latest > after || input.WaitMillis == 0 {
			return timeline.Update{Timeline: page, Changed: latest > after}, nil
		}
		select {
		case <-ctx.Done():
			return timeline.Update{}, ctx.Err()
		case <-timer.C:
			page, err = runtime.GetTaskTimeline(ctx, input.Query())
			if err != nil {
				return timeline.Update{}, err
			}
			latest, err = timeline.ParseCursor(page.NextCursor)
			if err != nil {
				return timeline.Update{}, err
			}
			return timeline.Update{Timeline: page, Changed: latest > after}, nil
		case <-changed:
		}
	}
}

func projectTaskTimeline(task TaskSession, query timeline.Query) (timeline.Page, error) {
	records := make([]timeline.Record, len(task.History))
	for index, event := range task.History {
		records[index] = timeline.Record{
			Sequence: event.Sequence,
			Event: timeline.Event{
				EventID:              task.TaskID + ".event." + eventSequenceID(event.Sequence),
				OccurredAtUnixMillis: event.AtUnixMillis,
				TaskID:               task.TaskID, SessionID: task.SessionID,
				HostID: task.HostID, WorldID: task.WorldID, ActorID: task.ActorID,
				ControllerID: task.ControllerID, Step: event.Step,
				PlanID: event.PlanID, PlanRevision: event.PlanRevision, PlanStepID: event.PlanStepID,
				EventKind: event.Kind, PublicSummary: event.Summary, ReasonCode: event.Code,
				ObservationID:       event.ObservationID,
				ObservationSequence: event.ObservationSequence,
				Epoch:               event.Epoch, Capability: event.Capability,
				SkillRefs: event.SkillRefs, MemoryContextRefs: event.MemoryContextRefs,
				Model: event.Model, Policy: event.Policy, Operation: event.Operation,
			},
		}
	}
	truncatedBefore := uint64(0)
	if len(task.History) != 0 && task.History[0].Sequence > 1 {
		truncatedBefore = task.History[0].Sequence - 1
	}
	return timeline.BuildPage(timeline.Snapshot{
		TaskID: task.TaskID, Goal: task.Goal, GoalDigest: taskGoalDigest(task.Goal),
		Status: string(task.Status), LatestSequence: task.EventSequence,
		TruncatedBefore: truncatedBefore, Records: records,
	}, query)
}

func taskGoalDigest(goal string) string {
	digest := sha256.Sum256([]byte(goal))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func eventSequenceID(value uint64) string {
	// Event IDs are stable local references; timeline cursors remain opaque.
	return timeline.FormatCursor(value)[4:]
}

func validateTaskTimelineHistory(task TaskSession) error {
	query := timeline.Query{TaskID: task.TaskID, Limit: timeline.MaximumLimit}
	for {
		page, err := projectTaskTimeline(task, query)
		if err != nil {
			return err
		}
		if !page.More {
			return nil
		}
		if page.NextCursor == query.AfterCursor {
			return errors.New("task timeline cursor did not advance")
		}
		query.AfterCursor = page.NextCursor
	}
}
