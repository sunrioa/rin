package sqlitestore

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

var ErrLocked = errors.New("SQLite store already has a writer")

// OpenExclusive holds a process writer lock for stores with cached projections.
func OpenExclusive(path string) (*sql.DB, func() error, error) {
	if path == "" {
		return nil, nil, errors.New("SQLite path is required")
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, nil, err
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, nil, errors.New("SQLite directory must be a real directory")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return nil, nil, errors.New("SQLite directory must be private")
	}
	lockPath := path + ".lock"
	if info, err := os.Lstat(lockPath); err == nil && !info.Mode().IsRegular() {
		return nil, nil, errors.New("SQLite lock must be a regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	file, err := lockWriter(lockPath)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %s", err, lockPath)
	}
	db, err := Open(path)
	if err != nil {
		return nil, nil, errors.Join(err, file.Close())
	}
	var once sync.Once
	var closeErr error
	closeStore := func() error { once.Do(func() { closeErr = errors.Join(db.Close(), file.Close()) }); return closeErr }
	return db, closeStore, nil
}
