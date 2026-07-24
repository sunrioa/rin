//go:build (darwin && !ios) || (linux && !android)

package store

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func checkDataDirectoryLockSupport() error {
	return nil
}

func acquireDataDirectoryLock(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open data-directory lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		closeErr := file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, errors.Join(
				fmt.Errorf("%w: %s", ErrDataDirectoryLocked, path),
				closeErr,
			)
		}
		return nil, errors.Join(fmt.Errorf("lock data directory: %w", err), closeErr)
	}
	return file, nil
}

func releaseDataDirectoryLock(file *os.File) error {
	if file == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	closeErr := file.Close()
	return errors.Join(unlockErr, closeErr)
}
