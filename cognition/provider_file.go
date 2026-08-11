package cognition

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

func prepareProviderStorePath(path string, persistenceErr error, label string) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("%w: create %s directory: %v", persistenceErr, label, err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(directory, 0o700); err != nil {
			return fmt.Errorf("%w: protect %s directory: %v", persistenceErr, label, err)
		}
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s parent must be a real directory", label)
	}
	info, err = os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: inspect %s snapshot: %v", persistenceErr, label, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s snapshot must be a real regular file", label)
	}
	return nil
}

func openProviderStoreLockFile(
	path string,
	persistenceErr error,
	label string,
) (*os.File, error) {
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("%w: %s lock path is not a real file", persistenceErr, label)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: inspect %s lock: %v", persistenceErr, label, err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("%w: open %s lock: %v", persistenceErr, label, err)
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("%w: %s lock path is not a regular file", persistenceErr, label)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("%w: protect %s lock: %v", persistenceErr, label, err)
	}
	return file, nil
}
