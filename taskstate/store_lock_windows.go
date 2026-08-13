//go:build windows

package taskstate

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32Plan     = syscall.NewLazyDLL("kernel32.dll")
	lockFileExPlan   = kernel32Plan.NewProc("LockFileEx")
	unlockFileExPlan = kernel32Plan.NewProc("UnlockFileEx")
)

func acquireStoreLock(path string) (*os.File, error) {
	file, err := openStoreLock(path)
	if err != nil {
		return nil, err
	}
	overlapped := new(syscall.Overlapped)
	result, _, callErr := lockFileExPlan.Call(file.Fd(), 0x2|0x1, 0, 1, 0, uintptr(unsafe.Pointer(overlapped)))
	if result == 0 {
		return nil, errors.Join(fmt.Errorf("%w: %v", ErrLocked, callErr), file.Close())
	}
	return file, nil
}

func releaseStoreLock(file *os.File) error {
	if file == nil {
		return nil
	}
	overlapped := new(syscall.Overlapped)
	_, _, unlockErr := unlockFileExPlan.Call(file.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(overlapped)))
	return errors.Join(unlockErr, file.Close())
}
