//go:build (!darwin && !linux) || ios || android

package store

import (
	"fmt"
	"os"
	"runtime"
)

func checkDataDirectoryLockSupport() error {
	return fmt.Errorf("%w: %s", ErrDataDirectoryLockUnsupported, runtime.GOOS)
}

func acquireDataDirectoryLock(string) (*os.File, error) {
	return nil, checkDataDirectoryLockSupport()
}

func releaseDataDirectoryLock(file *os.File) error {
	if file == nil {
		return nil
	}
	return file.Close()
}
