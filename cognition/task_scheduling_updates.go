package cognition

import (
	"context"
	"slices"

	"github.com/sunrioa/rin/controlplane"
)

type SchedulingCursor struct{ Tasks, Control uint64 }
type TaskSchedulingFilter struct {
	All     bool
	TaskIDs []string
	Changes []controlplane.SchedulingChange
}

func taskSchedulingKeys(task TaskSession) []string {
	if !taskOccupiesActor(task) {
		return nil
	}
	keys := []string{"h\x00" + task.HostID, "w\x00" + task.HostID + "\x00" + task.WorldID, "a\x00" + taskActorKey(task.HostID, task.WorldID, task.ActorID)}
	for _, id := range []string{task.Schedule.OperationID, task.PendingOperationID, task.MacroOperationID} {
		if id != "" {
			keys = append(keys, "o\x00"+id)
		}
	}
	return keys
}
func (store *LocalTaskStore) removeSchedulingIndexLocked(task TaskSession) {
	for _, key := range taskSchedulingKeys(task) {
		delete(store.scheduleIndex[key], task.TaskID)
		if len(store.scheduleIndex[key]) == 0 {
			delete(store.scheduleIndex, key)
		}
	}
}
func (store *LocalTaskStore) addSchedulingIndexLocked(task TaskSession) {
	for _, key := range taskSchedulingKeys(task) {
		if store.scheduleIndex[key] == nil {
			store.scheduleIndex[key] = make(map[string]bool)
		}
		store.scheduleIndex[key][task.TaskID] = true
	}
}
func schedulingTask(value TaskSession) TaskSession {
	return cloneTaskSession(TaskSession{TaskID: value.TaskID, HostID: value.HostID, WorldID: value.WorldID, ActorID: value.ActorID,
		Status: value.Status, Schedule: value.Schedule, PlanID: value.PlanID, PendingAction: value.PendingAction,
		PendingOperationID: value.PendingOperationID, MacroOperationID: value.MacroOperationID})
}

func (store *LocalTaskStore) SchedulingSelection(ctx context.Context, filter TaskSchedulingFilter) (TaskSnapshot, error) {
	if filter.All {
		return store.SchedulingSnapshot(ctx)
	}
	if err := requireMemoryContext(ctx); err != nil {
		return TaskSnapshot{}, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	ids := make(map[string]bool)
	for _, id := range filter.TaskIDs {
		if store.active[id] {
			ids[id] = true
		}
	}
	add := func(key string) {
		for id := range store.scheduleIndex[key] {
			ids[id] = true
		}
	}
	for _, change := range filter.Changes {
		if change.OperationID != "" {
			add("o\x00" + change.OperationID)
		}
		target := change.Target
		if target.HostID == "" {
			continue
		}
		switch {
		case target.WorldID == "":
			add("h\x00" + target.HostID)
		case target.ActorID == "":
			add("w\x00" + target.HostID + "\x00" + target.WorldID)
		default:
			add("a\x00" + taskActorKey(target.HostID, target.WorldID, target.ActorID))
		}
	}
	result := TaskSnapshot{Version: TaskSnapshotVersion, Revision: store.revision, Tasks: make([]TaskSession, 0, len(ids))}
	for id := range ids {
		result.Tasks = append(result.Tasks, schedulingTask(store.tasks[id]))
	}
	slices.SortFunc(result.Tasks, func(a, b TaskSession) int { return compareString(a.TaskID, b.TaskID) })
	return result, nil
}
func (store *SQLiteTaskStore) SchedulingSelection(ctx context.Context, filter TaskSchedulingFilter) (TaskSnapshot, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.ready(); err != nil {
		return TaskSnapshot{}, err
	}
	return store.local.SchedulingSelection(ctx, filter)
}

// Capture invalidation cursors before reading state. A concurrent commit is
// either reflected in this selection or remains available in the next page.
func (runtime *AgentRuntime) SchedulingUpdates(ctx context.Context, cursor SchedulingCursor, all bool) (TaskSnapshot, SchedulingCursor, error) {
	runtime.timelineMu.Lock()
	ids, revision, overflow := runtime.taskChanges.Since(cursor.Tasks)
	runtime.timelineMu.Unlock()
	next := SchedulingCursor{Tasks: revision, Control: cursor.Control}
	filter := TaskSchedulingFilter{All: all || overflow, TaskIDs: ids}
	if source, ok := runtime.control.(interface {
		SchedulingChanges(uint64) controlplane.SchedulingChangePage
	}); ok {
		page := source.SchedulingChanges(cursor.Control)
		next.Control = page.Revision
		filter.All = filter.All || page.All
		filter.Changes = page.Changes
	} else {
		filter.All = true
	}
	var snapshot TaskSnapshot
	var err error
	if indexed, ok := runtime.tasks.(interface {
		SchedulingSelection(context.Context, TaskSchedulingFilter) (TaskSnapshot, error)
	}); ok {
		snapshot, err = indexed.SchedulingSelection(ctx, filter)
	} else {
		snapshot, err = runtime.SchedulingSnapshot(ctx)
	}
	if err != nil {
		return snapshot, cursor, err
	}
	return snapshot, next, nil
}
