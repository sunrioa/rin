package cognition_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/internal/privatefile"
)

func benchmarkTaskSnapshot(b *testing.B, count int) string {
	b.Helper()
	local, err := cognition.NewLocalTaskStore(uint32(count))
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < count; i++ {
		task := validTaskSession(fmt.Sprintf("task.bench.%d", i))
		for step := 0; step < 64; step++ {
			task.History = append(task.History, cognition.TaskEvent{Kind: "task.created", Summary: strings.Repeat("context ", 16), AtUnixMillis: 10})
		}
		if _, err := local.Create(context.Background(), task); err != nil {
			b.Fatal(err)
		}
	}
	snapshot, err := local.Snapshot(context.Background())
	if err != nil {
		b.Fatal(err)
	}
	path := filepath.Join(b.TempDir(), "tasks.json")
	if err := privatefile.WriteJSON(path, snapshot); err != nil {
		b.Fatal(err)
	}
	return path
}

func BenchmarkTaskJSONCAS(b *testing.B) {
	for _, count := range []int{1, 100, 1000} {
		b.Run(fmt.Sprintf("tasks=%d", count), func(b *testing.B) {
			path := benchmarkTaskSnapshot(b, count)
			store, err := cognition.OpenFileTaskStore(path, uint32(count))
			if err != nil {
				b.Fatal(err)
			}
			defer store.Close()
			benchmarkTaskCAS(b, store)
		})
	}
}

func BenchmarkTaskSQLiteCAS(b *testing.B) {
	for _, count := range []int{1, 100, 1000} {
		b.Run(fmt.Sprintf("tasks=%d", count), func(b *testing.B) {
			legacy := benchmarkTaskSnapshot(b, count)
			store, err := cognition.OpenSQLiteTaskStore(filepath.Join(filepath.Dir(legacy), "tasks.db"), uint32(count))
			if err != nil {
				b.Fatal(err)
			}
			defer store.Close()
			benchmarkTaskCAS(b, store)
		})
	}
}

func benchmarkTaskCAS(b *testing.B, store cognition.TaskStore) {
	b.Helper()
	task, err := store.Load(context.Background(), "task.bench.0")
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		task, err = store.CompareAndSwap(context.Background(), task.Revision, task)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// Public Load latency under one continuous writer includes mutex contention
// and defensive-copy cost. It is a read-delay measurement, not pure lock time.
func BenchmarkTaskLoadUnderWrites(b *testing.B) {
	for _, backend := range []string{"JSON", "SQLite"} {
		for _, count := range []int{1, 100, 1000} {
			b.Run(fmt.Sprintf("%s/tasks=%d", backend, count), func(b *testing.B) {
				legacy := benchmarkTaskSnapshot(b, count)
				var store interface {
					cognition.TaskStore
					Close() error
				}
				var err error
				if backend == "JSON" {
					store, err = cognition.OpenFileTaskStore(legacy, uint32(count))
				} else {
					store, err = cognition.OpenSQLiteTaskStore(filepath.Join(filepath.Dir(legacy), "tasks.db"), uint32(count))
				}
				if err != nil {
					b.Fatal(err)
				}
				defer store.Close()
				task, err := store.Load(context.Background(), "task.bench.0")
				if err != nil {
					b.Fatal(err)
				}
				ready := make(chan struct{})
				stop := make(chan struct{})
				done := make(chan error, 1)
				go func() {
					first := true
					for {
						select {
						case <-stop:
							done <- nil
							return
						default:
						}
						updated, err := store.CompareAndSwap(context.Background(), task.Revision, task)
						if first {
							close(ready)
							first = false
						}
						if err != nil {
							done <- err
							return
						}
						task = updated
					}
				}()
				<-ready
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if _, err := store.Load(context.Background(), "task.bench.0"); err != nil {
						b.Fatal(err)
					}
				}
				b.StopTimer()
				close(stop)
				if err := <-done; err != nil {
					b.Fatal(err)
				}
			})
		}
	}
}
