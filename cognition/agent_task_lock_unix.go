//go:build (darwin && !ios) || (linux && !android)

package cognition

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func acquireTaskStoreLock(path string) (*os.File, error) {
	file, err := openTaskStoreLockFile(path)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		closeErr := file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, errors.Join(fmt.Errorf("%w: %s", ErrTaskStoreLocked, path), closeErr)
		}
		return nil, errors.Join(
			fmt.Errorf("%w: lock task store: %v", ErrTaskStorePersistence, err), closeErr,
		)
	}
	return file, nil
}

func releaseTaskStoreLock(file *os.File) error {
	if file == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	return errors.Join(unlockErr, file.Close())
}
