//go:build windows

package controlplane

import "os"

func validateOperationStatePermissions(_ os.FileInfo) error {
	return nil
}

func validateOperationDirectoryPermissions(_ os.FileInfo) error {
	return nil
}

func prepareOperationDirectoryPermissions(_ string, _ os.FileInfo) error {
	return nil
}
