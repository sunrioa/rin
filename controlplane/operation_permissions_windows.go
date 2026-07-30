//go:build windows

package controlplane

import "os"

func validateOperationStatePermissions(_ os.FileInfo) error {
	return nil
}
