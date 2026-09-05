package cognition

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestSQLiteDecisionRecorderMigratesBoundsAndRejectsOldWriter(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "decision-records.json")
	old, err := OpenFileDecisionRecorder(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Append(ctx, validDecisionRecord("record.one", 1)); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(path)
	dbpath := filepath.Join(filepath.Dir(path), "decision-records.db")
	recorder, err := OpenSQLiteDecisionRecorder(dbpath, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenSQLiteDecisionRecorder(dbpath, 2); err == nil {
		t.Fatal("second writer opened")
	}
	if err := recorder.Append(ctx, validDecisionRecord("record.one", 1)); err != nil {
		t.Fatal(err)
	}
	conflict := validDecisionRecord("record.one", 1)
	conflict.DecisionSummary = "Changed."
	if err := recorder.Append(ctx, conflict); !errors.Is(err, ErrDecisionRecordConflict) {
		t.Fatal(err)
	}
	for i := 2; i <= 4; i++ {
		if err := recorder.Append(ctx, validDecisionRecord(fmt.Sprintf("record.%d", i), uint64(i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFileDecisionRecorder(path, 2); err == nil {
		t.Fatal("old JSON writer opened after migration")
	}
	recorder, err = OpenSQLiteDecisionRecorder(dbpath, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer recorder.Close()
	snapshot, err := recorder.Snapshot(ctx)
	if err != nil || len(snapshot.Records) != 2 || snapshot.Records[0].RecordID != "record.3" || snapshot.Revision != 5 {
		t.Fatalf("snapshot: %#v %v", snapshot, err)
	}
	after, _ := os.ReadFile(path)
	if string(original) != string(after) {
		t.Fatal("migration altered legacy backup")
	}
}

func TestSQLiteDecisionRecorderRefusesCorruptIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "records.db")
	recorder, err := OpenSQLiteDecisionRecorder(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := recorder.Append(context.Background(), validDecisionRecord("record.one", 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := recorder.db.Exec(`UPDATE decision_records SET record_id='record.wrong'`); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	if recorder, err = OpenSQLiteDecisionRecorder(path, 2); err == nil {
		recorder.Close()
		t.Fatal("corrupt identity restored")
	}
}

func BenchmarkDecisionPersistence(b *testing.B) {
	for _, backend := range []string{"JSON", "SQLite"} {
		b.Run(backend, func(b *testing.B) {
			path := filepath.Join(b.TempDir(), "records")
			var recorder interface {
				Append(context.Context, DecisionRecord) error
				Close() error
			}
			var err error
			if backend == "JSON" {
				recorder, err = OpenFileDecisionRecorder(path+".json", 4096)
			} else {
				recorder, err = OpenSQLiteDecisionRecorder(path+".db", 4096)
			}
			if err != nil {
				b.Fatal(err)
			}
			defer recorder.Close()
			for i := 1; i <= 1000; i++ {
				if err := recorder.Append(context.Background(), validDecisionRecord(fmt.Sprintf("record.%d", i), uint64(i))); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := recorder.Append(context.Background(), validDecisionRecord(fmt.Sprintf("next.%d", i), uint64(i+1))); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
