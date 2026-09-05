//go:build windows

package sqlitestore

import (
	"errors"
	"os"
	"syscall"
)

func lockWriter(path string) (*os.File, error) {
	pointer, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := syscall.CreateFile(pointer, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil, syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		if errors.Is(err, syscall.Errno(32)) || errors.Is(err, syscall.Errno(33)) {
			return nil, ErrLocked
		}
		return nil, err
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = syscall.CloseHandle(handle)
		return nil, errors.New("invalid SQLite lock handle")
	}
	return file, nil
}
