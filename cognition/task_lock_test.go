package cognition

import (
	"fmt"
	"sync"
	"testing"
)

func TestTaskLocksSerializeWaitersAndReleaseHistoricalIDs(t *testing.T) {
	runtime := &AgentRuntime{taskLocks: make(map[string]*taskLockEntry)}
	lock := runtime.taskLock("task.shared")
	lock.Lock()
	if runtime.taskLock("task.shared").TryLock() {
		t.Fatal("TryLock bypassed holder")
	}
	var waiters sync.WaitGroup
	count := 0
	for i := 0; i < 32; i++ {
		waiters.Add(1)
		go func() {
			defer waiters.Done()
			for j := 0; j < 20; j++ {
				lock := runtime.taskLock("task.shared")
				lock.Lock()
				count++
				lock.Unlock()
			}
		}()
	}
	lock.Unlock()
	waiters.Wait()
	if count != 640 {
		t.Fatalf("task serialization failed: %d", count)
	}
	for i := 0; i < 2000; i++ {
		lock := runtime.taskLock(fmt.Sprintf("task.history.%d", i))
		lock.Lock()
		lock.Unlock()
	}
	if len(runtime.taskLocks) != 0 {
		t.Fatalf("historical mutexes retained: %d", len(runtime.taskLocks))
	}
}
