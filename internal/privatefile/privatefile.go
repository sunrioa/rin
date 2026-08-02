package privatefile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

// ReadJSON decodes one bounded, private, regular JSON file.
func ReadJSON(path string, maxBytes int64, target any) error {
	if path == "" || maxBytes < 1 || target == nil {
		return errors.New("invalid private JSON read")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > maxBytes {
		return errors.New("private JSON path must be a bounded regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return errors.New("private JSON file permissions must not allow group or other access")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode private JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("private JSON contains multiple values")
		}
		return fmt.Errorf("decode private JSON trailer: %w", err)
	}
	return nil
}

// WriteJSON atomically writes one private JSON file with mode 0600.
func WriteJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode private JSON: %w", err)
	}
	data = append(data, '\n')
	return Write(path, data, 0o600)
}

// Write atomically replaces one file in a private parent directory.
func Write(path string, data []byte, mode os.FileMode) error {
	if path == "" || mode.Perm() == 0 {
		return errors.New("invalid private file write")
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create private directory: %w", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(directory, 0o700); err != nil {
			return fmt.Errorf("secure private directory: %w", err)
		}
	}
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return fmt.Errorf("create temporary private file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(mode.Perm()); err != nil {
		return fmt.Errorf("set private file permissions: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		return fmt.Errorf("write private file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync private file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close private file: %w", err)
	}
	if err := replaceFile(temporaryPath, path); err != nil {
		return fmt.Errorf("replace private file: %w", err)
	}
	if err := syncDirectory(directory); err != nil {
		return fmt.Errorf("sync private directory: %w", err)
	}
	return nil
}
