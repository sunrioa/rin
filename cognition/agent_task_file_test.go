package cognition_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sunrioa/rin/cognition"
)

func TestFileTaskStorePersistsAndLocksSnapshot(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		t.Skip("task store locking is not supported on this platform")
	}
	path := filepath.Join(t.TempDir(), "private", "tasks.json")
	store, err := cognition.OpenFileTaskStore(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cognition.OpenFileTaskStore(path, 10); !errors.Is(err, cognition.ErrTaskStoreLocked) {
		_ = store.Close()
		t.Fatalf("expected a second writer to be rejected, got %v", err)
	}
	created, err := store.Create(context.Background(), validTaskSession("task.durable"))
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	created.Goal = "Persist the updated goal."
	if _, err := store.CompareAndSwap(context.Background(), created.Revision, created); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := cognition.OpenFileTaskStore(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	loaded, err := reopened.Load(context.Background(), "task.durable")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != 2 || loaded.Goal != "Persist the updated goal." {
		t.Fatalf("durable task did not survive reopen: %+v", loaded)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("task snapshot mode = %o, want 600", info.Mode().Perm())
		}
	}
}

func TestFileTaskStoreRejectsSymlinkSnapshot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not reliably available on Windows CI")
	}
	root := t.TempDir()
	target := filepath.Join(root, "target.json")
	if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "tasks.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := cognition.OpenFileTaskStore(link, 10); err == nil {
		t.Fatal("symlink task snapshot was accepted")
	}
}
