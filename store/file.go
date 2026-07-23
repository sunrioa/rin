// Package store contains standard-library persistence implementations for Rin.
package store

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"

	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
)

var safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,95}$`)
var safeHash = regexp.MustCompile(`^[0-9a-f]{64}$`)

const maxEventRecordBytes = 64 * 1024 * 1024

type File struct {
	root string
	mu   sync.Mutex
}

func OpenFile(root string) (*File, error) {
	if root == "" {
		return nil, errors.New("data directory is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(absolute, "sessions"), 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	return &File{root: absolute}, nil
}

func (s *File) Create(sessionID string, event protocol.EventRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	directory, err := s.sessionDir(sessionID)
	if err != nil {
		return err
	}
	if err := os.Mkdir(directory, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			path := filepath.Join(directory, "events.jsonl")
			events, loadErr := readEventFile(path)
			if loadErr == nil && len(events) == 1 &&
				rinruntime.EventRecordsExactlyEqual(events[0], event) {
				return syncExistingFile(path)
			}
			return rinruntime.ErrConflict
		}
		return err
	}
	path := filepath.Join(directory, "events.jsonl")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		_ = os.Remove(directory)
		return err
	}
	err = writeEvent(file, event)
	closeErr := file.Close()
	if err != nil {
		_ = os.Remove(path)
		_ = os.Remove(directory)
		return err
	}
	return closeErr
}

func (s *File) Append(sessionID string, event protocol.EventRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	directory, err := s.sessionDir(sessionID)
	if err != nil {
		return err
	}
	path := filepath.Join(directory, "events.jsonl")
	last, err := readLastEvent(path)
	if err != nil {
		return err
	}
	if rinruntime.EventRecordsExactlyEqual(event, last) {
		return syncExistingFile(path)
	}
	if event.Sequence != last.Sequence+1 || event.PrevHash != last.Hash {
		return rinruntime.ErrConflict
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return rinruntime.ErrNotFound
		}
		return err
	}
	err = writeEvent(file, event)
	if err != nil {
		rollbackErr := file.Truncate(info.Size())
		if rollbackErr == nil {
			rollbackErr = file.Sync()
		}
		closeErr := file.Close()
		return errors.Join(err, rollbackErr, closeErr)
	}
	closeErr := file.Close()
	return closeErr
}

func writeEvent(file *os.File, event protocol.EventRecord) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	if _, err := file.Write(payload); err != nil {
		return err
	}
	return file.Sync()
}

func (s *File) Load(sessionID string) ([]protocol.EventRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	directory, err := s.sessionDir(sessionID)
	if err != nil {
		return nil, err
	}
	return readEventFile(filepath.Join(directory, "events.jsonl"))
}

func readEventFile(path string) ([]protocol.EventRecord, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, rinruntime.ErrNotFound
		}
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() == 0 {
		return nil, rinruntime.ErrCorruptLog
	}
	var final [1]byte
	if _, err := file.ReadAt(final[:], info.Size()-1); err != nil {
		return nil, err
	}
	if final[0] != '\n' {
		return nil, fmt.Errorf("%w: event log has an incomplete tail", rinruntime.ErrCorruptLog)
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), maxEventRecordBytes)
	events := make([]protocol.EventRecord, 0)
	for line := 1; scanner.Scan(); line++ {
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.DisallowUnknownFields()
		var event protocol.EventRecord
		if err := decoder.Decode(&event); err != nil {
			return nil, fmt.Errorf("decode event line %d: %w", line, err)
		}
		if err := ensureEOF(decoder); err != nil {
			return nil, fmt.Errorf("decode event line %d: %w", line, err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, rinruntime.ErrCorruptLog
	}
	return events, nil
}

func readLastEvent(path string) (protocol.EventRecord, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return protocol.EventRecord{}, rinruntime.ErrNotFound
		}
		return protocol.EventRecord{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return protocol.EventRecord{}, err
	}
	size := info.Size()
	if size == 0 {
		return protocol.EventRecord{}, rinruntime.ErrCorruptLog
	}
	var final [1]byte
	if _, err := file.ReadAt(final[:], size-1); err != nil {
		return protocol.EventRecord{}, err
	}
	if final[0] != '\n' {
		return protocol.EventRecord{}, fmt.Errorf("%w: event log has an incomplete tail", rinruntime.ErrCorruptLog)
	}

	lineEnd := size - 1
	lineStart := int64(0)
	searchEnd := lineEnd
	buffer := make([]byte, 64*1024)
	for searchEnd > 0 {
		chunkStart := searchEnd - int64(len(buffer))
		if chunkStart < 0 {
			chunkStart = 0
		}
		chunk := buffer[:searchEnd-chunkStart]
		if _, err := file.ReadAt(chunk, chunkStart); err != nil {
			return protocol.EventRecord{}, err
		}
		if index := bytes.LastIndexByte(chunk, '\n'); index >= 0 {
			lineStart = chunkStart + int64(index) + 1
			break
		}
		searchEnd = chunkStart
		if lineEnd-searchEnd > maxEventRecordBytes {
			return protocol.EventRecord{}, fmt.Errorf("%w: event tail exceeds %d bytes", rinruntime.ErrCorruptLog, maxEventRecordBytes)
		}
	}
	lineLength := lineEnd - lineStart
	if lineLength <= 0 || lineLength > maxEventRecordBytes {
		return protocol.EventRecord{}, fmt.Errorf("%w: invalid event tail length", rinruntime.ErrCorruptLog)
	}
	line := make([]byte, lineLength)
	if _, err := file.ReadAt(line, lineStart); err != nil {
		return protocol.EventRecord{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	var event protocol.EventRecord
	if err := decoder.Decode(&event); err != nil {
		return protocol.EventRecord{}, fmt.Errorf("decode event tail: %w", err)
	}
	if err := ensureEOF(decoder); err != nil {
		return protocol.EventRecord{}, fmt.Errorf("decode event tail: %w", err)
	}
	return event, nil
}

func syncExistingFile(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	return errors.Join(syncErr, closeErr)
}

func (s *File) ListSessions() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(filepath.Join(s.root, "sessions"))
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() && safeID.MatchString(entry.Name()) {
			if _, err := os.Stat(filepath.Join(s.root, "sessions", entry.Name(), "events.jsonl")); err == nil {
				result = append(result, entry.Name())
			}
		}
	}
	sort.Strings(result)
	return result, nil
}

func (s *File) SaveSnapshot(sessionID string, snapshot protocol.Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !safeHash.MatchString(snapshot.StateHash) {
		return errors.New("invalid snapshot hash")
	}
	directory, err := s.sessionDir(sessionID)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(directory, "events.jsonl")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return rinruntime.ErrNotFound
		}
		return err
	}
	payload, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".snapshot-*.tmp")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(payload, '\n')); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	destination := filepath.Join(directory, fmt.Sprintf("snapshot-%020d-%s.json", snapshot.State.Revision, snapshot.StateHash))
	if _, err := os.Stat(destination); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(temporaryName, destination)
}

func (s *File) sessionDir(sessionID string) (string, error) {
	if !safeID.MatchString(sessionID) {
		return "", errors.New("unsafe session id")
	}
	return filepath.Join(s.root, "sessions", sessionID), nil
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}
