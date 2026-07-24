//go:build !windows

package store

import "os"

func renameDurably(oldPath, newPath string) error {
	return os.Rename(oldPath, newPath)
}
