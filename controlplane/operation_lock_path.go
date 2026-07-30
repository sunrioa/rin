package controlplane

import (
	"fmt"
	"os"
)

func openOperationLockFile(path string) (*os.File, error) {
	if err := validateOperationLockPath(path); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("%w: open lock file: %v", ErrPersistence, err)
	}
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("%w: inspect lock file: %v", ErrPersistence, err)
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("%w: lock path must be a regular file", ErrPersistence)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("%w: protect lock file: %v", ErrPersistence, err)
	}
	return file, nil
}

func validateOperationLockPath(path string) error {
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf(
				"%w: lock path must be a real regular file",
				ErrPersistence,
			)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("%w: inspect lock path: %v", ErrPersistence, err)
	}
	return nil
}
