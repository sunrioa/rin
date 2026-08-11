//go:build windows

package cognition

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

const (
	taskLockSharingViolation = syscall.Errno(32)
	taskLockViolation        = syscall.Errno(33)
)

func acquireProviderStoreLock(
	path string,
	lockedErr error,
	persistenceErr error,
	label string,
) (*os.File, error) {
	pointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("%w: encode %s lock path: %v", persistenceErr, label, err)
	}
	handle, err := syscall.CreateFile(
		pointer,
		syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		0,
		nil,
		syscall.OPEN_ALWAYS,
		syscall.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		if errors.Is(err, taskLockSharingViolation) || errors.Is(err, taskLockViolation) {
			return nil, fmt.Errorf("%w: %s", lockedErr, path)
		}
		return nil, fmt.Errorf("%w: open %s lock: %v", persistenceErr, label, err)
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = syscall.CloseHandle(handle)
		return nil, fmt.Errorf("%w: invalid %s lock handle", persistenceErr, label)
	}
	if err := file.Chmod(0o600); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}

func releaseProviderStoreLock(file *os.File) error {
	if file == nil {
		return nil
	}
	return file.Close()
}
