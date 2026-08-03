//go:build !windows

package controlplane

import (
	"fmt"
	"os"
)

func validateOperationStatePermissions(info os.FileInfo) error {
	if info.Mode().Perm()&0o077 == 0 {
		return nil
	}
	return fmt.Errorf(
		"%w: operation state permissions must not grant group or world access",
		ErrPersistence,
	)
}

func validateOperationDirectoryPermissions(info os.FileInfo) error {
	if info.Mode().Perm()&0o077 == 0 {
		return nil
	}
	return fmt.Errorf(
		"%w: data directory permissions must not grant group or world access",
		ErrPersistence,
	)
}

func prepareOperationDirectoryPermissions(path string, info os.FileInfo) error {
	if err := validateOperationDirectoryPermissions(info); err == nil {
		return nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return fmt.Errorf("%w: inspect data directory contents: %v", ErrPersistence, err)
	}
	if len(entries) != 0 {
		return validateOperationDirectoryPermissions(info)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("%w: secure empty data directory: %v", ErrPersistence, err)
	}
	return nil
}
