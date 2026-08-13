//go:build (darwin && !ios) || (linux && !android)

package taskstate

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func acquireStoreLock(path string) (*os.File, error) {
	file, err := openStoreLock(path)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		closeErr := file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, errors.Join(fmt.Errorf("%w: %s", ErrLocked, path), closeErr)
		}
		return nil, errors.Join(fmt.Errorf("%w: lock: %v", ErrPersist, err), closeErr)
	}
	return file, nil
}

func releaseStoreLock(file *os.File) error {
	if file == nil {
		return nil
	}
	return errors.Join(syscall.Flock(int(file.Fd()), syscall.LOCK_UN), file.Close())
}
