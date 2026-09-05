package cognition

import "sync"

// References include holders and callers waiting to lock. Removing only the
// final reference prevents both unbounded historical locks and split mutexes.
type taskLockEntry struct {
	mu         sync.Mutex
	references int
}
type taskLockHandle struct {
	runtime *AgentRuntime
	key     string
	entry   *taskLockEntry
}

func (runtime *AgentRuntime) taskLock(key string) *taskLockHandle {
	runtime.taskLocksMu.Lock()
	defer runtime.taskLocksMu.Unlock()
	entry := runtime.taskLocks[key]
	if entry == nil {
		entry = &taskLockEntry{}
		runtime.taskLocks[key] = entry
	}
	entry.references++
	return &taskLockHandle{runtime: runtime, key: key, entry: entry}
}
func (handle *taskLockHandle) Lock() { handle.entry.mu.Lock() }
func (handle *taskLockHandle) TryLock() bool {
	if handle.entry.mu.TryLock() {
		return true
	}
	handle.release()
	return false
}
func (handle *taskLockHandle) Unlock() { handle.entry.mu.Unlock(); handle.release() }
func (handle *taskLockHandle) release() {
	handle.runtime.taskLocksMu.Lock()
	defer handle.runtime.taskLocksMu.Unlock()
	handle.entry.references--
	if handle.entry.references == 0 {
		delete(handle.runtime.taskLocks, handle.key)
	}
}
