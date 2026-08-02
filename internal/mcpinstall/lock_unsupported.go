//go:build (!darwin && !linux && !windows) || ios || android

package mcpinstall

import (
	"fmt"
	"os"
	"runtime"
)

func acquireInstallLock(string) (*os.File, error) {
	return nil, fmt.Errorf("MCP installer locking is unsupported on %s", runtime.GOOS)
}

func releaseInstallLock(file *os.File) error {
	if file == nil {
		return nil
	}
	return file.Close()
}
