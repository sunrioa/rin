package mcpinstall

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

const installLockFileName = ".rin-mcp-install.lock"

var ErrInstallerLocked = errors.New("Rin MCP installer is already running")

func prepareInstallLockPath(root string) (string, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("create MCP installer directory: %w", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(root, 0o700); err != nil {
			return "", fmt.Errorf("secure MCP installer directory: %w", err)
		}
	}
	path := filepath.Join(root, installLockFileName)
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() {
			return "", errors.New("MCP installer lock must be a real regular file")
		}
		return path, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect MCP installer lock: %w", err)
	}
	return path, nil
}

func openInstallLockFile(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open MCP installer lock: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("inspect MCP installer lock: %w", err)
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, errors.New("MCP installer lock must be a regular file")
	}
	if err := file.Chmod(0o600); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}
