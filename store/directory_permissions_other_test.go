//go:build !windows

package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileStoresRejectPermissiveRootAndStructuralDirectories(t *testing.T) {
	for _, relative := range []string{".", "sessions", "tombstones"} {
		t.Run(relative, func(t *testing.T) {
			root := t.TempDir()
			file, err := OpenFile(root)
			if err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, relative)
			if err := os.Chmod(path, 0o755); err != nil {
				t.Fatal(err)
			}
			if opened, err := OpenFile(root); err == nil {
				_ = opened.Close()
				t.Fatalf("writer accepted permissive %s", relative)
			}
			if opened, err := OpenFileReadOnly(root); err == nil {
				_ = opened.Close()
				t.Fatalf("read-only Store accepted permissive %s", relative)
			}
		})
	}
}

func TestFileStoresRejectPermissiveSessionDirectoryBeforeUse(t *testing.T) {
	root := t.TempDir()
	writer, err := OpenFile(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	sessionDirectory := filepath.Join(root, "sessions", "session.permissive")
	if err := os.Mkdir(sessionDirectory, 0o755); err != nil {
		t.Fatal(err)
	}

	writer, err = OpenFile(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Load("session.permissive"); err == nil {
		_ = writer.Close()
		t.Fatal("writer read a permissive Session directory")
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	readOnly, err := OpenFileReadOnly(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readOnly.Load("session.permissive"); err == nil {
		_ = readOnly.Close()
		t.Fatal("read-only Store read a permissive Session directory")
	}
	if err := readOnly.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestFileStoresAcceptPrivateDirectories(t *testing.T) {
	root := t.TempDir()
	writer, err := OpenFile(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	readOnly, err := OpenFileReadOnly(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := readOnly.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestFileStoreSecuresPermissiveEmptyRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	writer, err := OpenFile(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("empty data directory mode = %o, want 700", info.Mode().Perm())
	}
}
