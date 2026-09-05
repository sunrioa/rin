package cognition

import (
	"context"
	"slices"
)

func taskActorKey(hostID, worldID, actorID string) string {
	return hostID + "\x00" + worldID + "\x00" + actorID
}

func (store *LocalTaskStore) indexTaskLocked(task TaskSession) {
	store.addSchedulingIndexLocked(task)
	if taskOccupiesActor(task) {
		store.active[task.TaskID] = true
	} else {
		delete(store.active, task.TaskID)
	}
	key := taskActorKey(task.HostID, task.WorldID, task.ActorID)
	if store.actors[key] == nil {
		store.actors[key] = make(map[string]bool)
	}
	store.actors[key][task.TaskID] = true
}

// Only settled history leaves the working set. The SQLite facade archives its
// durable row in the same transaction that admits the replacement task.
func (store *LocalTaskStore) retireTaskLocked(exclude string) bool {
	var oldest *TaskSession
	for id, task := range store.tasks {
		if id == exclude || taskOccupiesActor(task) || task.PendingAction != nil || task.MacroOperationID != "" {
			continue
		}
		if task.SkillLearning != nil && task.SkillLearning.Status == SkillLearningPending {
			continue
		}
		if oldest == nil || task.UpdatedAtUnixMillis < oldest.UpdatedAtUnixMillis ||
			(task.UpdatedAtUnixMillis == oldest.UpdatedAtUnixMillis && task.TaskID < oldest.TaskID) {
			value := task
			oldest = &value
		}
	}
	if oldest == nil {
		return false
	}
	store.removeSchedulingIndexLocked(*oldest)
	delete(store.tasks, oldest.TaskID)
	store.retired = oldest.TaskID
	delete(store.active, oldest.TaskID)
	key := taskActorKey(oldest.HostID, oldest.WorldID, oldest.ActorID)
	delete(store.actors[key], oldest.TaskID)
	if len(store.actors[key]) == 0 {
		delete(store.actors, key)
	}
	return true
}

func (store *LocalTaskStore) SchedulingSnapshot(ctx context.Context) (TaskSnapshot, error) {
	if err := requireMemoryContext(ctx); err != nil {
		return TaskSnapshot{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	result := TaskSnapshot{Version: TaskSnapshotVersion, Revision: store.revision, Tasks: make([]TaskSession, 0, len(store.active))}
	for id := range store.active {
		value := store.tasks[id]
		result.Tasks = append(result.Tasks, schedulingTask(value))
	}
	slices.SortFunc(result.Tasks, func(a, b TaskSession) int { return compareString(a.TaskID, b.TaskID) })
	return result, nil
}

func (store *LocalTaskStore) ActorTasks(ctx context.Context, hostID, worldID, actorID string) (TaskSnapshot, error) {
	if err := requireMemoryContext(ctx); err != nil {
		return TaskSnapshot{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	result := TaskSnapshot{Version: TaskSnapshotVersion, Revision: store.revision, Tasks: []TaskSession{}}
	for id := range store.actors[taskActorKey(hostID, worldID, actorID)] {
		result.Tasks = append(result.Tasks, cloneTaskSession(store.tasks[id]))
	}
	slices.SortFunc(result.Tasks, func(a, b TaskSession) int { return compareString(a.TaskID, b.TaskID) })
	return result, nil
}

func (runtime *AgentRuntime) SchedulingSnapshot(ctx context.Context) (TaskSnapshot, error) {
	if indexed, ok := runtime.tasks.(interface {
		SchedulingSnapshot(context.Context) (TaskSnapshot, error)
	}); ok {
		return indexed.SchedulingSnapshot(ctx)
	}
	return runtime.tasks.Snapshot(ctx)
}

func (runtime *AgentRuntime) actorTasks(ctx context.Context, input StartTaskInput) (TaskSnapshot, error) {
	if indexed, ok := runtime.tasks.(interface {
		ActorTasks(context.Context, string, string, string) (TaskSnapshot, error)
	}); ok {
		return indexed.ActorTasks(ctx, input.HostID, input.WorldID, input.ActorID)
	}
	return runtime.tasks.Snapshot(ctx)
}
