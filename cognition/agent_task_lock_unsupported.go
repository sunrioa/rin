//go:build (!darwin && !linux && !windows) || ios || android

package cognition

import (
	"fmt"
	"os"
	"runtime"
)

func acquireProviderStoreLock(
	string,
	error,
	persistenceErr error,
	label string,
) (*os.File, error) {
	return nil, fmt.Errorf(
		"%w: %s locking is unsupported on %s", persistenceErr, label, runtime.GOOS,
	)
}

func releaseProviderStoreLock(file *os.File) error {
	if file == nil {
		return nil
	}
	return file.Close()
}
