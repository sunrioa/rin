package controlplane

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/internal/sqlitestore"
)

// These storage microbenchmarks hold one operation mutable among retained
// histories. They measure durable writes, not gateway or Host throughput.
func benchmarkOperationService(b *testing.B, count int) *Service {
	b.Helper()
	service := newService(Options{})
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("operation.bench.%d", i)
		operation := &operationState{request: HostControlRequest{OperationID: id, HostID: "host.bench", WorldID: "world.bench", ActorID: "actor.bench", Principal: host.Principal{ID: "principal.bench"}}, status: OperationQueued, createdAt: 1000, updatedAt: 1000}
		// Stable bounded output represents retained result/context bytes.
		operation.output = []byte(`{"detail":"` + strings.Repeat("context ", 1024) + `"}`)
		service.operations[id] = operation
	}
	return service
}

func BenchmarkOperationJSONCommit(b *testing.B) {
	for _, count := range []int{1, 100, 1000} {
		b.Run(fmt.Sprintf("operations=%d", count), func(b *testing.B) {
			service := benchmarkOperationService(b, count)
			file, _, err := openOperationFile(b.TempDir())
			if err != nil {
				b.Fatal(err)
			}
			defer file.close()
			service.operationFile = file
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				service.operations["operation.bench.0"].updatedAt++
				service.operations["operation.bench.0"].persistenceRevision++
				if err := service.writeOperationsLocked(maxOperationFileBytes); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkOperationSQLiteCommit(b *testing.B) {
	for _, count := range []int{1, 100, 1000} {
		b.Run(fmt.Sprintf("operations=%d", count), func(b *testing.B) {
			service := benchmarkOperationService(b, count)
			file, err := openOperationDirectory(b.TempDir())
			if err != nil {
				b.Fatal(err)
			}
			db, err := sqlitestore.Open(filepath.Join(file.root, operationSQLiteName))
			if err != nil {
				b.Fatal(err)
			}
			store := &operationSQLite{db: db, file: file, newDatabase: true, versions: make(map[string]uint64), rowBytes: make(map[string]int64)}
			defer store.close()
			service.operationSQLite = store
			if err := service.writeOperationsLocked(maxOperationFileBytes); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				service.operations["operation.bench.0"].updatedAt++
				service.operations["operation.bench.0"].persistenceRevision++
				if err := service.writeOperationsLocked(maxOperationFileBytes); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
