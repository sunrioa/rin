// Package sqlitestore opens private SQLite files for single-writer stores.
// Callers own their directory and application-level writer lock.
package sqlitestore

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"runtime"

	"github.com/sunrioa/rin/internal/sqlitedsn"
	_ "modernc.org/sqlite"
)

func Open(path string) (*sql.DB, error) {
	for _, candidate := range []string{path, path + "-wal", path + "-shm", path + "-journal"} {
		info, err := os.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0) {
			return nil, fmt.Errorf("SQLite path %s must be a private regular file", candidate)
		}
	}
	// Set permissions before SQLite can create a WAL or publish user content.
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", sqlitedsn.File(path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	for _, statement := range []string{`PRAGMA journal_mode=WAL`, `PRAGMA synchronous=FULL`, `PRAGMA busy_timeout=5000`, `PRAGMA foreign_keys=ON`} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return db, nil
}
