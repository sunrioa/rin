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

	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
)

const eventIndexVersion = "rin.events-index/v1"

type eventIndex struct {
	entries []eventIndexEntry
}

type eventIndexHeader struct {
	Version string `json:"version"`
}

type eventIndexEntry struct {
	Revision  uint64 `json:"revision"`
	Offset    int64  `json:"offset"`
	EndOffset int64  `json:"end_offset"`
	Hash      string `json:"hash"`
}

func (s *File) Head(sessionID string) (rinruntime.EventAnchor, error) {
	directory, done, err := s.beginSession(sessionID)
	if err != nil {
		return rinruntime.EventAnchor{}, err
	}
	defer done()
	if err := s.ensureSessionDurability(sessionID, directory); err != nil {
		return rinruntime.EventAnchor{}, err
	}
	if err := s.rejectDurabilityUncertainty(sessionID); err != nil {
		return rinruntime.EventAnchor{}, err
	}
	file, err := openEventFile(directory, os.O_RDONLY)
	if err != nil {
		return rinruntime.EventAnchor{}, err
	}
	defer file.Close()
	index, err := s.ensureEventIndex(sessionID, directory, file)
	if err != nil {
		return rinruntime.EventAnchor{}, err
	}
	last, err := readIndexedEvent(file, index.entries[len(index.entries)-1])
	if err == nil {
		err = verifyIndexedEvent(index.entries, len(index.entries)-1, last)
	}
	if err != nil {
		index, err = s.rebuildEventIndex(sessionID, directory, file)
		if err != nil {
			return rinruntime.EventAnchor{}, err
		}
		last, err = readIndexedEvent(file, index.entries[len(index.entries)-1])
		if err != nil {
			return rinruntime.EventAnchor{}, err
		}
	}
	if err := verifyIndexedEvent(index.entries, len(index.entries)-1, last); err != nil {
		return rinruntime.EventAnchor{}, err
	}
	return rinruntime.EventAnchor{Revision: last.Sequence, HeadHash: last.Hash}, nil
}

func (s *File) LoadRange(
	sessionID string,
	afterRevision uint64,
	throughRevision uint64,
	limit int,
) (rinruntime.EventPage, error) {
	directory, done, err := s.beginSession(sessionID)
	if err != nil {
		return rinruntime.EventPage{}, err
	}
	defer done()
	if err := s.ensureSessionDurability(sessionID, directory); err != nil {
		return rinruntime.EventPage{}, err
	}
	if err := s.rejectDurabilityUncertainty(sessionID); err != nil {
		return rinruntime.EventPage{}, err
	}
	if limit <= 0 {
		return rinruntime.EventPage{}, errors.New("event range limit must be positive")
	}
	file, err := openEventFile(directory, os.O_RDONLY)
	if err != nil {
		return rinruntime.EventPage{}, err
	}
	defer file.Close()
	index, err := s.ensureEventIndex(sessionID, directory, file)
	if err != nil {
		return rinruntime.EventPage{}, err
	}
	page, err := readIndexedRange(file, index, afterRevision, throughRevision, limit)
	if err == nil {
		return page, nil
	}
	if errors.Is(err, rinruntime.ErrNotFound) {
		return rinruntime.EventPage{}, err
	}
	// The log is authoritative. Any malformed, stale, truncated, or otherwise
	// unusable offset table is rebuilt atomically and retried once.
	index, rebuildErr := s.rebuildEventIndex(sessionID, directory, file)
	if rebuildErr != nil {
		return rinruntime.EventPage{}, errors.Join(err, rebuildErr)
	}
	return readIndexedRange(file, index, afterRevision, throughRevision, limit)
}

func openEventFile(directory string, flags int) (*os.File, error) {
	file, err := os.OpenFile(filepath.Join(directory, "events.jsonl"), flags, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, rinruntime.ErrNotFound
		}
		return nil, err
	}
	return file, nil
}

func (s *File) ensureEventIndex(
	sessionID string,
	directory string,
	eventFile *os.File,
) (*eventIndex, error) {
	info, err := eventFile.Stat()
	if err != nil {
		return nil, err
	}
	if cached := s.cachedIndex(sessionID); indexMatchesLog(cached, info.Size()) {
		return cached, nil
	}
	index, err := readEventIndex(filepath.Join(directory, "events.idx"), info.Size())
	if err == nil {
		s.setCachedIndex(sessionID, index)
		return index, nil
	}
	return s.rebuildEventIndex(sessionID, directory, eventFile)
}

func (s *File) rebuildEventIndex(
	sessionID string,
	directory string,
	eventFile *os.File,
) (*eventIndex, error) {
	index, err := buildEventIndex(eventFile)
	if err != nil {
		s.deleteCachedIndex(sessionID)
		return nil, err
	}
	if err := writeEventIndex(directory, index); err != nil {
		s.deleteCachedIndex(sessionID)
		return nil, err
	}
	s.setCachedIndex(sessionID, index)
	return index, nil
}

func (s *File) cachedIndex(sessionID string) *eventIndex {
	s.indexesMu.Lock()
	defer s.indexesMu.Unlock()
	return s.indexes[sessionID]
}

func (s *File) setCachedIndex(sessionID string, index *eventIndex) {
	s.indexesMu.Lock()
	defer s.indexesMu.Unlock()
	s.indexes[sessionID] = index
}

func (s *File) deleteCachedIndex(sessionID string) {
	s.indexesMu.Lock()
	defer s.indexesMu.Unlock()
	delete(s.indexes, sessionID)
}

func indexMatchesLog(index *eventIndex, logSize int64) bool {
	return index != nil && len(index.entries) > 0 &&
		index.entries[len(index.entries)-1].EndOffset == logSize
}

func readEventIndex(path string, logSize int64) (*eventIndex, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() == 0 {
		return nil, errors.New("event index is empty")
	}
	var final [1]byte
	if _, err := file.ReadAt(final[:], info.Size()-1); err != nil {
		return nil, err
	}
	if final[0] != '\n' {
		return nil, errors.New("event index has an incomplete final record")
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4*1024), 1024*1024)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return nil, err
		}
		return nil, errors.New("event index is empty")
	}
	var header eventIndexHeader
	if err := decodeStrictJSON(scanner.Bytes(), &header); err != nil {
		return nil, err
	}
	if header.Version != eventIndexVersion {
		return nil, fmt.Errorf("unsupported event index version %q", header.Version)
	}
	index := &eventIndex{}
	for scanner.Scan() {
		var entry eventIndexEntry
		if err := decodeStrictJSON(scanner.Bytes(), &entry); err != nil {
			return nil, err
		}
		index.entries = append(index.entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if err := validateEventIndex(index, logSize); err != nil {
		return nil, err
	}
	return index, nil
}

func validateEventIndex(index *eventIndex, logSize int64) error {
	if index == nil || len(index.entries) == 0 {
		return errors.New("event index has no entries")
	}
	expectedOffset := int64(0)
	for position, entry := range index.entries {
		expectedRevision := uint64(position + 1)
		if entry.Revision != expectedRevision {
			return fmt.Errorf("event index revision %d is not contiguous", entry.Revision)
		}
		if entry.Offset != expectedOffset || entry.EndOffset <= entry.Offset ||
			entry.EndOffset-entry.Offset > maxEventRecordBytes+1 {
			return fmt.Errorf("event index offset for revision %d is invalid", entry.Revision)
		}
		if !safeHash.MatchString(entry.Hash) {
			return fmt.Errorf("event index hash for revision %d is invalid", entry.Revision)
		}
		expectedOffset = entry.EndOffset
	}
	if expectedOffset != logSize {
		return fmt.Errorf("event index ends at %d, log ends at %d", expectedOffset, logSize)
	}
	return nil
}

func buildEventIndex(file *os.File) (*eventIndex, error) {
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
	scanner := bufio.NewScanner(io.NewSectionReader(file, 0, info.Size()))
	scanner.Buffer(make([]byte, 64*1024), maxEventRecordBytes+1)
	index := &eventIndex{}
	var (
		offset           int64
		previousRevision uint64
		previousHash     string
	)
	for scanner.Scan() {
		line := scanner.Bytes()
		endOffset := offset + int64(len(line)) + 1
		var delimiter [1]byte
		if _, err := file.ReadAt(delimiter[:], endOffset-1); err != nil || delimiter[0] != '\n' {
			return nil, fmt.Errorf("%w: non-canonical event delimiter at offset %d", rinruntime.ErrCorruptLog, endOffset-1)
		}
		event, err := decodeEvent(line)
		if err != nil {
			return nil, fmt.Errorf("%w: decode event at revision %d: %v", rinruntime.ErrCorruptLog, previousRevision+1, err)
		}
		if err := rinruntime.VerifyEventRecord(previousRevision, previousHash, event); err != nil {
			return nil, err
		}
		index.entries = append(index.entries, eventIndexEntry{
			Revision: event.Sequence, Offset: offset, EndOffset: endOffset, Hash: event.Hash,
		})
		offset = endOffset
		previousRevision = event.Sequence
		previousHash = event.Hash
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("%w: scan event log: %v", rinruntime.ErrCorruptLog, err)
	}
	if len(index.entries) == 0 || offset != info.Size() {
		return nil, rinruntime.ErrCorruptLog
	}
	return index, nil
}

func writeEventIndex(directory string, index *eventIndex) error {
	temporary, err := os.CreateTemp(directory, ".events.idx-*.tmp")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	encoder := json.NewEncoder(temporary)
	if err := encoder.Encode(eventIndexHeader{Version: eventIndexVersion}); err != nil {
		_ = temporary.Close()
		return err
	}
	for _, entry := range index.entries {
		if err := encoder.Encode(entry); err != nil {
			_ = temporary.Close()
			return err
		}
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, filepath.Join(directory, "events.idx")); err != nil {
		return err
	}
	return syncDirectory(directory)
}

func appendEventIndex(directory string, entry eventIndexEntry) error {
	file, err := os.OpenFile(
		filepath.Join(directory, "events.idx"),
		os.O_WRONLY|os.O_APPEND,
		0o600,
	)
	if err != nil {
		return err
	}
	encodeErr := json.NewEncoder(file).Encode(entry)
	syncErr := error(nil)
	if encodeErr == nil {
		syncErr = file.Sync()
	}
	closeErr := file.Close()
	return errors.Join(encodeErr, syncErr, closeErr)
}

func readIndexedEvent(file *os.File, entry eventIndexEntry) (protocol.EventRecord, error) {
	length := entry.EndOffset - entry.Offset
	if length <= 1 || length > maxEventRecordBytes+1 {
		return protocol.EventRecord{}, fmt.Errorf("%w: indexed event length is invalid", rinruntime.ErrCorruptLog)
	}
	line := make([]byte, length)
	if _, err := file.ReadAt(line, entry.Offset); err != nil {
		return protocol.EventRecord{}, err
	}
	if line[len(line)-1] != '\n' {
		return protocol.EventRecord{}, fmt.Errorf("%w: indexed event is missing its delimiter", rinruntime.ErrCorruptLog)
	}
	event, err := decodeEvent(line[:len(line)-1])
	if err != nil {
		return protocol.EventRecord{}, fmt.Errorf("%w: decode indexed event: %v", rinruntime.ErrCorruptLog, err)
	}
	return event, nil
}

func verifyIndexedEvent(entries []eventIndexEntry, position int, event protocol.EventRecord) error {
	if event.Sequence != entries[position].Revision || event.Hash != entries[position].Hash {
		return fmt.Errorf("%w: event index does not match revision %d", rinruntime.ErrCorruptLog, entries[position].Revision)
	}
	var previousRevision uint64
	var previousHash string
	if position > 0 {
		previousRevision = entries[position-1].Revision
		previousHash = entries[position-1].Hash
	}
	return rinruntime.VerifyEventRecord(previousRevision, previousHash, event)
}

func readIndexedRange(
	file *os.File,
	index *eventIndex,
	afterRevision uint64,
	throughRevision uint64,
	limit int,
) (rinruntime.EventPage, error) {
	head := uint64(len(index.entries))
	if throughRevision == 0 {
		throughRevision = head
	}
	if afterRevision > head || throughRevision > head {
		return rinruntime.EventPage{}, rinruntime.ErrNotFound
	}
	if throughRevision <= afterRevision {
		return rinruntime.EventPage{Events: []protocol.EventRecord{}}, nil
	}
	start := int(afterRevision)
	endRevision := throughRevision
	if maximum := uint64(limit); maximum < throughRevision-afterRevision {
		endRevision = afterRevision + maximum
	}
	page := rinruntime.EventPage{
		Events:  make([]protocol.EventRecord, 0, int(endRevision-afterRevision)),
		HasMore: endRevision < throughRevision,
	}
	var previousRevision uint64
	var previousHash string
	if start > 0 {
		anchor, err := readIndexedEvent(file, index.entries[start-1])
		if err != nil {
			return rinruntime.EventPage{}, err
		}
		if err := verifyIndexedEvent(index.entries, start-1, anchor); err != nil {
			return rinruntime.EventPage{}, err
		}
		previousRevision = anchor.Sequence
		previousHash = anchor.Hash
	}
	for position := start; position < int(endRevision); position++ {
		event, err := readIndexedEvent(file, index.entries[position])
		if err != nil {
			return rinruntime.EventPage{}, err
		}
		if event.Sequence != index.entries[position].Revision ||
			event.Hash != index.entries[position].Hash {
			return rinruntime.EventPage{}, fmt.Errorf(
				"%w: event index does not match revision %d",
				rinruntime.ErrCorruptLog,
				index.entries[position].Revision,
			)
		}
		if err := rinruntime.VerifyEventRecord(previousRevision, previousHash, event); err != nil {
			return rinruntime.EventPage{}, err
		}
		page.Events = append(page.Events, event)
		previousRevision = event.Sequence
		previousHash = event.Hash
	}
	return page, nil
}

func decodeEvent(line []byte) (protocol.EventRecord, error) {
	var event protocol.EventRecord
	if err := decodeStrictJSON(line, &event); err != nil {
		return protocol.EventRecord{}, err
	}
	return event, nil
}

func decodeStrictJSON(payload []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return ensureEOF(decoder)
}
