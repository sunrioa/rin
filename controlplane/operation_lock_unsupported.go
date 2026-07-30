//go:build (!darwin && !linux && !windows) || ios || android

package controlplane

import (
	"fmt"
	"os"
	"runtime"
)

const operationLockFileName = ".rin-control.lock"

func acquireOperationDirectoryLock(string) (*os.File, error) {
	return nil, fmt.Errorf(
		"%w: data-directory locking is unsupported on %s",
		ErrPersistence,
		runtime.GOOS,
	)
}

func releaseOperationDirectoryLock(file *os.File) error {
	if file == nil {
		return nil
	}
	return file.Close()
}
