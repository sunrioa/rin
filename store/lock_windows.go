//go:build windows

package store

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

const (
	windowsErrorSharingViolation syscall.Errno = 32
	windowsErrorLockViolation    syscall.Errno = 33
)

func checkDataDirectoryLockSupport() error {
	return nil
}

func acquireDataDirectoryLock(path string) (*os.File, error) {
	pathPointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("encode data-directory lock path: %w", err)
	}
	handle, err := syscall.CreateFile(
		pathPointer,
		syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		0,
		nil,
		syscall.OPEN_ALWAYS,
		syscall.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		if errors.Is(err, windowsErrorSharingViolation) ||
			errors.Is(err, windowsErrorLockViolation) {
			return nil, fmt.Errorf("%w: %s", ErrDataDirectoryLocked, path)
		}
		return nil, fmt.Errorf("open data-directory lock: %w", err)
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = syscall.CloseHandle(handle)
		return nil, errors.New("open data-directory lock: invalid Windows handle")
	}
	return file, nil
}

func releaseDataDirectoryLock(file *os.File) error {
	if file == nil {
		return nil
	}
	return file.Close()
}
