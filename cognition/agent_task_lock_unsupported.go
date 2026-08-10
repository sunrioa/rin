//go:build (!darwin && !linux && !windows) || ios || android

package cognition

import (
	"fmt"
	"os"
	"runtime"
)

func acquireTaskStoreLock(string) (*os.File, error) {
	return nil, fmt.Errorf(
		"%w: task-store locking is unsupported on %s", ErrTaskStorePersistence, runtime.GOOS,
	)
}

func releaseTaskStoreLock(file *os.File) error {
	if file == nil {
		return nil
	}
	return file.Close()
}
