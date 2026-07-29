//go:build !windows

package controlplane

import (
	"errors"
	"os"
)

func replaceOperationFile(oldPath, newPath string) error {
	return os.Rename(oldPath, newPath)
}

func syncOperationDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
