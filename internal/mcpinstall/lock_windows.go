//go:build windows

package mcpinstall

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

const (
	windowsSharingViolation = syscall.Errno(32)
	windowsLockViolation    = syscall.Errno(33)
)

func acquireInstallLock(root string) (*os.File, error) {
	path, err := prepareInstallLockPath(root)
	if err != nil {
		return nil, err
	}
	pointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("encode MCP installer lock path: %w", err)
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
		if errors.Is(err, windowsSharingViolation) ||
			errors.Is(err, windowsLockViolation) {
			return nil, fmt.Errorf("%w: %s", ErrInstallerLocked, path)
		}
		return nil, fmt.Errorf("open MCP installer lock: %w", err)
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = syscall.CloseHandle(handle)
		return nil, errors.New("open MCP installer lock: invalid Windows handle")
	}
	if err := file.Chmod(0o600); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}

func releaseInstallLock(file *os.File) error {
	if file == nil {
		return nil
	}
	return file.Close()
}
