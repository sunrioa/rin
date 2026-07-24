//go:build windows

package store

import (
	"errors"
	"fmt"
	"syscall"
)

func syncDirectory(path string) error {
	pathPointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return fmt.Errorf("encode directory sync path: %w", err)
	}
	handle, err := syscall.CreateFile(
		pathPointer,
		syscall.GENERIC_WRITE,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil,
		syscall.OPEN_EXISTING,
		syscall.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return fmt.Errorf("open directory for sync: %w", err)
	}
	syncErr := syscall.FlushFileBuffers(handle)
	closeErr := syscall.CloseHandle(handle)
	return errors.Join(syncErr, closeErr)
}
