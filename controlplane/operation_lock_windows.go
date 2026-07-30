//go:build windows

package controlplane

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

const (
	operationLockFileName = ".rin-control.lock"
	lockSharingViolation  = syscall.Errno(32)
	lockViolation         = syscall.Errno(33)
)

func acquireOperationDirectoryLock(path string) (*os.File, error) {
	if err := validateOperationLockPath(path); err != nil {
		return nil, err
	}
	pointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("%w: encode lock path: %v", ErrPersistence, err)
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
		if errors.Is(err, lockSharingViolation) || errors.Is(err, lockViolation) {
			return nil, fmt.Errorf("%w: %s", ErrDataLocked, path)
		}
		return nil, fmt.Errorf("%w: open lock file: %v", ErrPersistence, err)
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = syscall.CloseHandle(handle)
		return nil, fmt.Errorf("%w: invalid lock handle", ErrPersistence)
	}
	if err := file.Chmod(0o600); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}

func releaseOperationDirectoryLock(file *os.File) error {
	if file == nil {
		return nil
	}
	return file.Close()
}
