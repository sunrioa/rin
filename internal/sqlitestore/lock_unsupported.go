//go:build (!darwin && !linux && !windows) || ios || android

package sqlitestore

import (
	"errors"
	"os"
)

func lockWriter(string) (*os.File, error) {
	return nil, errors.New("exclusive SQLite locking is unsupported on this platform")
}
