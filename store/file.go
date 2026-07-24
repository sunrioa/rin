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

var (
	// ErrDataDirectoryLocked means another File store owns the data directory.
	ErrDataDirectoryLocked = errors.New("rin data directory is already locked")
	// ErrDataDirectoryLockUnsupported prevents silently running without the
	// single-writer guarantee on an unsupported operating system.
	ErrDataDirectoryLockUnsupported = errors.New("rin data directory locking is unsupported on this platform")
	// ErrDurabilityUncertain means an append and its rollback could not be
	// durably distinguished. Only an exact retry of that append may proceed.
	ErrDurabilityUncertain = errors.New("rin file store durability outcome is uncertain")
	ErrFileClosed          = errors.New("rin file store is closed")
)

type uncertainFileAppend struct {
	event       protocol.EventRecord
	priorOffset int64
}

type File struct {
	root     string
	lockFile *os.File

	lifecycle sync.RWMutex
	closed    bool
	closeErr  error

	locksMu      sync.Mutex
	sessionLocks map[string]*sync.Mutex
	indexesMu    sync.Mutex
	indexes      map[string]*eventIndex

	artifactsMu   sync.Mutex
	artifactLocks map[string]*sync.Mutex

	uncertainMu      sync.Mutex
	uncertainAppends map[string]uncertainFileAppend

	durabilityMu        sync.Mutex
	durabilityConfirmed map[string]struct{}

	syncEventFile func(string) error
	syncDir       func(string) error
}

func OpenFile(root string) (*File, error) {
	return openFileWithPreflight(root, checkDataDirectoryLockSupport)
}

func openFileWithPreflight(
	root string,
	preflight func() error,
) (*File, error) {
	if root == "" {
		return nil, errors.New("data directory is required")
	}
	if err := preflight(); err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := makeDirectoryTreeSynced(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	lockFile, err := acquireDataDirectoryLock(filepath.Join(absolute, ".rin.lock"))
	if err != nil {
		return nil, err
	}
	store := &File{
		root: absolute, lockFile: lockFile,
		sessionLocks:        make(map[string]*sync.Mutex),
		indexes:             make(map[string]*eventIndex),
		artifactLocks:       make(map[string]*sync.Mutex),
		uncertainAppends:    make(map[string]uncertainFileAppend),
		durabilityConfirmed: make(map[string]struct{}),
		syncEventFile:       syncExistingFile,
		syncDir:             syncDirectory,
	}
	sessions := filepath.Join(absolute, "sessions")
	if err := os.Mkdir(sessions, 0o700); err != nil {
		if !errors.Is(err, os.ErrExist) {
			_ = store.Close()
			return nil, fmt.Errorf("create sessions directory: %w", err)
		}
		info, statErr := os.Stat(sessions)
		if statErr != nil {
			_ = store.Close()
			return nil, fmt.Errorf("sessions path is not a directory: %w", statErr)
		}
		if !info.IsDir() {
			_ = store.Close()
			return nil, errors.New("sessions path is not a directory")
		}
	}
	if err := store.cleanupTemporaryFiles(); err != nil {
		_ = store.Close()
		return nil, err
	}
	if err := store.syncDir(absolute); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("sync data directory: %w", err)
	}
	if err := store.syncDir(sessions); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("sync sessions directory: %w", err)
	}
	return store, nil
}

// Close releases the process-wide data-directory lease. It is idempotent and
// waits for in-flight Store operations to finish.
func (s *File) Close() error {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	if s.closed {
		return s.closeErr
	}
	s.closed = true
	s.closeErr = releaseDataDirectoryLock(s.lockFile)
	s.lockFile = nil
	return s.closeErr
}

func (s *File) uncertainAppend(sessionID string) (uncertainFileAppend, bool) {
	s.uncertainMu.Lock()
	defer s.uncertainMu.Unlock()
	uncertain, exists := s.uncertainAppends[sessionID]
	return uncertain, exists
}

func (s *File) markAppendUncertain(
	sessionID string,
	event protocol.EventRecord,
	priorOffset int64,
) {
	event.Data = append([]byte(nil), event.Data...)
	s.uncertainMu.Lock()
	defer s.uncertainMu.Unlock()
	if s.uncertainAppends == nil {
		s.uncertainAppends = make(map[string]uncertainFileAppend)
	}
	s.uncertainAppends[sessionID] = uncertainFileAppend{
		event: event, priorOffset: priorOffset,
	}
}

func (s *File) clearAppendUncertain(sessionID string) {
	s.uncertainMu.Lock()
	defer s.uncertainMu.Unlock()
	delete(s.uncertainAppends, sessionID)
}

func (s *File) rejectDurabilityUncertainty(sessionID string) error {
	if _, exists := s.uncertainAppend(sessionID); !exists {
		return nil
	}
	return fmt.Errorf("%w: session %s requires an exact append retry", ErrDurabilityUncertain, sessionID)
}

func (s *File) Create(sessionID string, event protocol.EventRecord) error {
	directory, done, err := s.beginSession(sessionID)
	if err != nil {
		return err
	}
	defer done()
	if err := s.rejectDurabilityUncertainty(sessionID); err != nil {
		return err
	}
	sessions := filepath.Dir(directory)
	if _, err := os.Stat(directory); err == nil {
		confirmErr := s.confirmExistingCreate(
			sessionID,
			directory,
			event,
		)
		if !errors.Is(confirmErr, rinruntime.ErrNotFound) {
			return confirmErr
		}
		removed, cleanupErr := removeEmptySessionDirectory(directory, sessions)
		if cleanupErr != nil {
			return cleanupErr
		}
		if !removed {
			return fmt.Errorf(
				"%w: session %s directory exists without an event log",
				rinruntime.ErrCorruptLog,
				sessionID,
			)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := rinruntime.VerifyEventRecord(0, "", event); err != nil {
		return err
	}
	temporary, err := os.MkdirTemp(sessions, ".session-"+sessionID+"-*.tmp")
	if err != nil {
		return err
	}
	temporaryExists := true
	defer func() {
		if temporaryExists {
			_ = os.RemoveAll(temporary)
		}
	}()
	path := filepath.Join(temporary, "events.jsonl")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	writeErr := writeEvent(file, event)
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	index := &eventIndex{entries: []eventIndexEntry{{
		Revision: event.Sequence, Offset: 0, EndOffset: info.Size(), Hash: event.Hash,
	}}}
	if err := writeEventIndex(temporary, index); err != nil {
		return err
	}
	if err := syncDirectory(temporary); err != nil {
		return err
	}
	if err := os.Rename(temporary, directory); err != nil {
		if errors.Is(err, os.ErrExist) {
			return s.confirmExistingCreate(
				sessionID,
				directory,
				event,
			)
		}
		return err
	}
	temporaryExists = false
	if err := s.syncDir(sessions); err != nil {
		return err
	}
	s.markSessionDurabilityConfirmed(sessionID)
	s.setCachedIndex(sessionID, index)
	return nil
}

func (s *File) Append(sessionID string, event protocol.EventRecord) error {
	directory, done, err := s.beginSession(sessionID)
	if err != nil {
		return err
	}
	defer done()
	if err := s.ensureSessionDurability(sessionID, directory); err != nil {
		return err
	}
	if uncertain, exists := s.uncertainAppend(sessionID); exists {
		if !rinruntime.EventRecordsExactlyEqual(uncertain.event, event) {
			return fmt.Errorf(
				"%w: session %s requires an exact retry of revision %d",
				ErrDurabilityUncertain,
				sessionID,
				uncertain.event.Sequence,
			)
		}
		payload, err := encodeEventRecord(event)
		if err != nil {
			return durabilityUncertainError(sessionID, err)
		}
		return s.retryUncertainAppend(
			sessionID,
			directory,
			event,
			payload,
			uncertain.priorOffset,
		)
	}

	path := filepath.Join(directory, "events.jsonl")
	file, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return rinruntime.ErrNotFound
		}
		return err
	}
	last, info, err := readLastEventFromFile(file)
	if err != nil {
		return errors.Join(err, file.Close())
	}
	index, err := s.ensureEventIndex(sessionID, directory, file)
	if err != nil {
		return errors.Join(err, file.Close())
	}
	index, last, err = s.verifyTailAgainstIndex(
		sessionID,
		directory,
		file,
		index,
		last,
	)
	if err != nil {
		return errors.Join(err, file.Close())
	}
	if rinruntime.EventRecordsExactlyEqual(event, last) {
		return errors.Join(file.Sync(), file.Close())
	}
	if err := rinruntime.VerifyEventRecord(last.Sequence, last.Hash, event); err != nil {
		return errors.Join(rinruntime.ErrConflict, file.Close())
	}
	payload, err := encodeEventRecord(event)
	if err != nil {
		return errors.Join(err, file.Close())
	}
	err = writeEventPayload(file, payload)
	if err != nil {
		rollbackErr := s.rollbackFailedAppend(
			sessionID,
			event,
			file,
			info.Size(),
		)
		closeErr := file.Close()
		return errors.Join(err, rollbackErr, closeErr)
	}
	return s.finishAppend(sessionID, directory, file, index, event, info.Size())
}

func (s *File) retryUncertainAppend(
	sessionID string,
	directory string,
	event protocol.EventRecord,
	payload []byte,
	priorOffset int64,
) error {
	if priorOffset <= 0 {
		return durabilityUncertainError(
			sessionID,
			fmt.Errorf("%w: uncertain append has an invalid prior offset", rinruntime.ErrCorruptLog),
		)
	}
	file, err := os.OpenFile(
		filepath.Join(directory, "events.jsonl"),
		os.O_RDWR|os.O_APPEND,
		0o600,
	)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			err = rinruntime.ErrNotFound
		}
		return durabilityUncertainError(sessionID, err)
	}
	info, err := file.Stat()
	if err != nil {
		return errors.Join(durabilityUncertainError(sessionID, err), file.Close())
	}
	if info.Size() < priorOffset {
		return errors.Join(
			durabilityUncertainError(
				sessionID,
				fmt.Errorf(
					"%w: event log ends before the uncertain append offset",
					rinruntime.ErrCorruptLog,
				),
			),
			file.Close(),
		)
	}
	if err := file.Truncate(priorOffset); err != nil {
		return errors.Join(durabilityUncertainError(sessionID, err), file.Close())
	}
	if err := file.Sync(); err != nil {
		return errors.Join(durabilityUncertainError(sessionID, err), file.Close())
	}

	// The old prefix is now durably restored. Rebuild the derived index from
	// that exact prefix before appending the only candidate allowed through the
	// uncertainty barrier.
	s.deleteCachedIndex(sessionID)
	last, prefixInfo, err := readLastEventFromFile(file)
	if err != nil {
		return errors.Join(durabilityUncertainError(sessionID, err), file.Close())
	}
	if prefixInfo.Size() != priorOffset {
		return errors.Join(
			durabilityUncertainError(
				sessionID,
				fmt.Errorf("%w: uncertain append offset changed during retry", rinruntime.ErrCorruptLog),
			),
			file.Close(),
		)
	}
	index, err := s.ensureEventIndex(sessionID, directory, file)
	if err != nil {
		return errors.Join(durabilityUncertainError(sessionID, err), file.Close())
	}
	index, last, err = s.verifyTailAgainstIndex(
		sessionID,
		directory,
		file,
		index,
		last,
	)
	if err != nil {
		return errors.Join(durabilityUncertainError(sessionID, err), file.Close())
	}
	if err := rinruntime.VerifyEventRecord(last.Sequence, last.Hash, event); err != nil {
		return errors.Join(durabilityUncertainError(sessionID, err), file.Close())
	}
	if err := writeEventPayload(file, payload); err != nil {
		rollbackErr := s.rollbackFailedAppend(
			sessionID,
			event,
			file,
			priorOffset,
		)
		return errors.Join(
			durabilityUncertainError(sessionID, errors.Join(err, rollbackErr)),
			file.Close(),
		)
	}

	// The exact candidate is now durably present. Index maintenance is derived
	// and may be reconciled independently, so reads can safely resume.
	s.clearAppendUncertain(sessionID)
	return s.finishAppend(sessionID, directory, file, index, event, priorOffset)
}

func (s *File) verifyTailAgainstIndex(
	sessionID string,
	directory string,
	file *os.File,
	index *eventIndex,
	last protocol.EventRecord,
) (*eventIndex, protocol.EventRecord, error) {
	position := len(index.entries) - 1
	if position >= 0 && verifyIndexedEvent(index.entries, position, last) == nil {
		return index, last, nil
	}
	rebuilt, err := s.rebuildEventIndex(sessionID, directory, file)
	if err != nil {
		return nil, protocol.EventRecord{}, err
	}
	position = len(rebuilt.entries) - 1
	last, err = readIndexedEvent(file, rebuilt.entries[position])
	if err != nil {
		return nil, protocol.EventRecord{}, err
	}
	if err := verifyIndexedEvent(rebuilt.entries, position, last); err != nil {
		return nil, protocol.EventRecord{}, err
	}
	return rebuilt, last, nil
}

func (s *File) rollbackFailedAppend(
	sessionID string,
	event protocol.EventRecord,
	file *os.File,
	priorOffset int64,
) error {
	truncateErr := file.Truncate(priorOffset)
	var syncErr error
	if truncateErr == nil {
		syncErr = file.Sync()
	}
	rollbackErr := errors.Join(truncateErr, syncErr)
	if rollbackErr == nil {
		return nil
	}
	s.markAppendUncertain(sessionID, event, priorOffset)
	return errors.Join(
		fmt.Errorf(
			"%w: session %s append rollback was not durable",
			ErrDurabilityUncertain,
			sessionID,
		),
		rollbackErr,
	)
}

func durabilityUncertainError(sessionID string, cause error) error {
	return errors.Join(
		fmt.Errorf(
			"%w: session %s requires an exact append retry",
			ErrDurabilityUncertain,
			sessionID,
		),
		cause,
	)
}

func (s *File) finishAppend(
	sessionID string,
	directory string,
	file *os.File,
	index *eventIndex,
	event protocol.EventRecord,
	priorOffset int64,
) error {
	after, statErr := file.Stat()
	if statErr != nil {
		return errors.Join(statErr, file.Close())
	}
	entry := eventIndexEntry{
		Revision: event.Sequence, Offset: priorOffset, EndOffset: after.Size(), Hash: event.Hash,
	}
	index.entries = append(index.entries, entry)
	indexErr := appendEventIndex(directory, entry)
	if indexErr != nil {
		s.deleteCachedIndex(sessionID)
		if _, rebuildErr := s.rebuildEventIndex(sessionID, directory, file); rebuildErr != nil {
			indexErr = errors.Join(indexErr, rebuildErr)
		} else {
			indexErr = nil
		}
	} else {
		s.setCachedIndex(sessionID, index)
	}
	closeErr := file.Close()
	return errors.Join(indexErr, closeErr)
}

func writeEvent(file *os.File, event protocol.EventRecord) error {
	payload, err := encodeEventRecord(event)
	if err != nil {
		return err
	}
	return writeEventPayload(file, payload)
}

func encodeEventRecord(event protocol.EventRecord) ([]byte, error) {
	payload, err := json.Marshal(event)
	if err != nil {
		return nil, err
	}
	if len(payload) > maxEventRecordBytes {
		return nil, fmt.Errorf(
			"%w: event record exceeds the %d-byte readable limit",
			rinruntime.ErrCorruptLog,
			maxEventRecordBytes,
		)
	}
	return append(payload, '\n'), nil
}

func writeEventPayload(file *os.File, payload []byte) error {
	if _, err := file.Write(payload); err != nil {
		return err
	}
	return file.Sync()
}

func (s *File) Load(sessionID string) ([]protocol.EventRecord, error) {
	directory, done, err := s.beginSession(sessionID)
	if err != nil {
		return nil, err
	}
	defer done()
	if err := s.ensureSessionDurability(sessionID, directory); err != nil {
		return nil, err
	}
	if err := s.rejectDurabilityUncertainty(sessionID); err != nil {
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
	// Scanner's token buffer includes the newline delimiter while the shared
	// limit describes the JSON EventRecord bytes themselves.
	scanner.Buffer(make([]byte, 64*1024), maxEventRecordBytes+1)
	events := make([]protocol.EventRecord, 0)
	for line := 1; scanner.Scan(); line++ {
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.DisallowUnknownFields()
		var event protocol.EventRecord
		if err := decoder.Decode(&event); err != nil {
			return nil, fmt.Errorf("%w: decode event line %d: %v", rinruntime.ErrCorruptLog, line, err)
		}
		if err := ensureEOF(decoder); err != nil {
			return nil, fmt.Errorf("%w: decode event line %d: %v", rinruntime.ErrCorruptLog, line, err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("%w: scan event log: %v", rinruntime.ErrCorruptLog, err)
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
	event, _, err := readLastEventFromFile(file)
	return event, err
}

func readLastEventFromFile(file *os.File) (protocol.EventRecord, os.FileInfo, error) {
	info, err := file.Stat()
	if err != nil {
		return protocol.EventRecord{}, nil, err
	}
	size := info.Size()
	if size == 0 {
		return protocol.EventRecord{}, info, rinruntime.ErrCorruptLog
	}
	var final [1]byte
	if _, err := file.ReadAt(final[:], size-1); err != nil {
		return protocol.EventRecord{}, info, err
	}
	if final[0] != '\n' {
		return protocol.EventRecord{}, info, fmt.Errorf("%w: event log has an incomplete tail", rinruntime.ErrCorruptLog)
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
			return protocol.EventRecord{}, info, err
		}
		if index := bytes.LastIndexByte(chunk, '\n'); index >= 0 {
			lineStart = chunkStart + int64(index) + 1
			break
		}
		searchEnd = chunkStart
		if lineEnd-searchEnd > maxEventRecordBytes {
			return protocol.EventRecord{}, info, fmt.Errorf("%w: event tail exceeds %d bytes", rinruntime.ErrCorruptLog, maxEventRecordBytes)
		}
	}
	lineLength := lineEnd - lineStart
	if lineLength <= 0 || lineLength > maxEventRecordBytes {
		return protocol.EventRecord{}, info, fmt.Errorf("%w: invalid event tail length", rinruntime.ErrCorruptLog)
	}
	line := make([]byte, lineLength)
	if _, err := file.ReadAt(line, lineStart); err != nil {
		return protocol.EventRecord{}, info, err
	}
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	var event protocol.EventRecord
	if err := decoder.Decode(&event); err != nil {
		return protocol.EventRecord{}, info, fmt.Errorf("%w: decode event tail: %v", rinruntime.ErrCorruptLog, err)
	}
	if err := ensureEOF(decoder); err != nil {
		return protocol.EventRecord{}, info, fmt.Errorf("%w: decode event tail: %v", rinruntime.ErrCorruptLog, err)
	}
	return event, info, nil
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

func (s *File) confirmExistingCreate(
	sessionID string,
	directory string,
	event protocol.EventRecord,
) error {
	if err := s.ensureSessionDurability(sessionID, directory); err != nil {
		return err
	}
	path := filepath.Join(directory, "events.jsonl")
	events, loadErr := readEventFile(path)
	if loadErr != nil {
		return loadErr
	}
	if len(events) == 1 &&
		rinruntime.EventRecordsExactlyEqual(events[0], event) {
		return rinruntime.VerifyEventRecord(0, "", event)
	}
	return rinruntime.ErrConflict
}

func removeEmptySessionDirectory(directory string, sessionsDirectory string) (bool, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return false, err
	}
	if len(entries) != 0 {
		return false, nil
	}
	if err := os.Remove(directory); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return true, nil
		}
		return false, err
	}
	if err := syncDirectory(sessionsDirectory); err != nil {
		return false, err
	}
	return true, nil
}

func (s *File) ListSessions() ([]string, error) {
	done, err := s.beginRoot()
	if err != nil {
		return nil, err
	}
	defer done()
	entries, err := os.ReadDir(filepath.Join(s.root, "sessions"))
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() && safeID.MatchString(entry.Name()) {
			if _, err := os.Stat(filepath.Join(s.root, "sessions", entry.Name(), "events.jsonl")); err == nil {
				result = append(result, entry.Name())
			} else if !errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("stat session %s event log: %w", entry.Name(), err)
			}
		}
	}
	sort.Strings(result)
	return result, nil
}

func (s *File) SaveSnapshot(sessionID string, snapshot protocol.Snapshot) error {
	directory, done, err := s.beginArtifact(sessionID)
	if err != nil {
		return err
	}
	defer done()
	if err := s.confirmArtifactSession(sessionID, directory); err != nil {
		return err
	}
	if err := rinruntime.ValidateSnapshot(snapshot); err != nil {
		return err
	}
	if !safeHash.MatchString(snapshot.StateHash) {
		return errors.New("invalid snapshot hash")
	}
	if snapshot.State.SessionID != sessionID {
		return errors.New("snapshot session id does not match destination")
	}
	destination := filepath.Join(directory, fmt.Sprintf("snapshot-%020d-%s.json", snapshot.State.Revision, snapshot.StateHash))
	if existingErr := validateStoredSnapshot(
		destination,
		sessionID,
		snapshot.State.Revision,
		snapshot.StateHash,
		&snapshot.IdentifierHistoryHash,
	); existingErr == nil {
		if err := s.syncEventFile(destination); err != nil {
			return fmt.Errorf("sync existing snapshot: %w", err)
		}
		if err := s.syncDir(directory); err != nil {
			return fmt.Errorf("sync snapshot directory: %w", err)
		}
		return s.retainSnapshotFiles(directory, sessionID, snapshotRetentionCount)
	} else if !errors.Is(existingErr, os.ErrNotExist) {
		if err := s.removeInvalidArtifact(directory, destination); err != nil {
			return fmt.Errorf(
				"replace invalid snapshot: %w",
				errors.Join(existingErr, err),
			)
		}
	}
	if err := s.writeJSONAtomically(directory, ".snapshot-*.tmp", destination, snapshot, true); err != nil {
		return err
	}
	if err := validateStoredSnapshot(
		destination,
		sessionID,
		snapshot.State.Revision,
		snapshot.StateHash,
		&snapshot.IdentifierHistoryHash,
	); err != nil {
		return err
	}
	return s.retainSnapshotFiles(directory, sessionID, snapshotRetentionCount)
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
