//go:build !windows

package store

import (
	"fmt"
	"os"
)

func validatePrivateDirectoryPermissions(info os.FileInfo, label string) error {
	if info.Mode().Perm()&0o077 == 0 {
		return nil
	}
	return fmt.Errorf(
		"%s permissions must not grant group or world access",
		label,
	)
}

func preparePrivateDataDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect data directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	if err := validatePrivateDirectoryPermissions(info, "data directory"); err == nil {
		return nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return fmt.Errorf("inspect data directory contents: %w", err)
	}
	if len(entries) != 0 {
		return validatePrivateDirectoryPermissions(info, "data directory")
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("secure empty data directory: %w", err)
	}
	return nil
}
