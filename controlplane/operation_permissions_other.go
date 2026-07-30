//go:build !windows

package controlplane

import (
	"fmt"
	"os"
)

func validateOperationStatePermissions(info os.FileInfo) error {
	if info.Mode().Perm()&0o077 == 0 {
		return nil
	}
	return fmt.Errorf(
		"%w: operation state permissions must not grant group or world access",
		ErrPersistence,
	)
}
