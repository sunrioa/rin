//go:build windows

package store

import "os"

func validatePrivateDirectoryPermissions(_ os.FileInfo, _ string) error {
	return nil
}

func preparePrivateDataDirectory(_ string) error {
	return nil
}
