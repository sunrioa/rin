//go:build (darwin && !ios) || (linux && !android)

package mcpinstall

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func acquireInstallLock(root string) (*os.File, error) {
	path, err := prepareInstallLockPath(root)
	if err != nil {
		return nil, err
	}
	file, err := openInstallLockFile(path)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		closeErr := file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, errors.Join(
				fmt.Errorf("%w: %s", ErrInstallerLocked, path),
				closeErr,
			)
		}
		return nil, errors.Join(
			fmt.Errorf("lock MCP installer: %w", err),
			closeErr,
		)
	}
	return file, nil
}

func releaseInstallLock(file *os.File) error {
	if file == nil {
		return nil
	}
	return errors.Join(
		syscall.Flock(int(file.Fd()), syscall.LOCK_UN),
		file.Close(),
	)
}
