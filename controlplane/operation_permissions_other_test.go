//go:build !windows

package controlplane

import (
	"os"
	"testing"
)

func TestOperationFileRejectsPermissiveExistingDirectory(t *testing.T) {
	for _, mode := range []os.FileMode{0o755, 0o770} {
		t.Run(mode.String(), func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, mode); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(root+"/existing-state", nil, 0o600); err != nil {
				t.Fatal(err)
			}
			file, _, err := openOperationFile(root)
			if err == nil {
				_ = file.close()
				t.Fatalf("accepted data directory mode %o", mode)
			}
		})
	}
}

func TestOperationFileSecuresPermissiveEmptyDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	file, _, err := openOperationFile(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.close(); err != nil {
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

func TestOperationFileAcceptsPrivateExistingDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	file, _, err := openOperationFile(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.close(); err != nil {
		t.Fatal(err)
	}
}
