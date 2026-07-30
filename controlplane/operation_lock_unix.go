//go:build (darwin && !ios) || (linux && !android)

package controlplane

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

const operationLockFileName = ".rin-control.lock"

func acquireOperationDirectoryLock(path string) (*os.File, error) {
	file, err := openOperationLockFile(path)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		closeErr := file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, errors.Join(
				fmt.Errorf("%w: %s", ErrDataLocked, path),
				closeErr,
			)
		}
		return nil, errors.Join(
			fmt.Errorf("%w: lock data directory: %v", ErrPersistence, err),
			closeErr,
		)
	}
	return file, nil
}

func releaseOperationDirectoryLock(file *os.File) error {
	if file == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	return errors.Join(unlockErr, file.Close())
}
