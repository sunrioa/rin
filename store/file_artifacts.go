package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"

	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
)

var (
	snapshotFilePattern   = regexp.MustCompile(`^snapshot-([0-9]{20})-([0-9a-f]{64})\.json$`)
	checkpointFilePattern = regexp.MustCompile(`^checkpoint-([0-9]{20})-([0-9a-f]{64})\.json$`)
)

const (
	snapshotRetentionCount   = 2
	checkpointRetentionCount = 2
)

type retainedArtifact struct {
	name     string
	revision uint64
}

func (s *File) writeJSONAtomically(
	directory string,
	temporaryPattern string,
	destination string,
	value any,
	indent bool,
) error {
	temporary, err := os.CreateTemp(directory, temporaryPattern)
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
	if indent {
		encoder.SetIndent("", "  ")
	}
	if err := encoder.Encode(value); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, destination); err != nil {
		return err
	}
	return s.syncDir(directory)
}

func decodeJSONFile(path string, target any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return ensureEOF(decoder)
}

func validateStoredSnapshot(
	path string,
	sessionID string,
	revision uint64,
	stateHash string,
	identifierHistoryHash *string,
) error {
	var snapshot protocol.Snapshot
	if err := decodeJSONFile(path, &snapshot); err != nil {
		return fmt.Errorf("read published snapshot: %w", err)
	}
	if snapshot.State.SessionID != sessionID ||
		snapshot.State.Revision != revision ||
		snapshot.StateHash != stateHash ||
		(identifierHistoryHash != nil &&
			snapshot.IdentifierHistoryHash != *identifierHistoryHash) {
		return errors.New("published snapshot does not match its storage identity")
	}
	return rinruntime.ValidateSnapshot(snapshot)
}

func (s *File) retainSnapshotFiles(directory string, sessionID string, keep int) error {
	return s.retainArtifacts(
		directory,
		snapshotFilePattern,
		keep,
		func(path string, revision uint64, identity string) bool {
			return validateStoredSnapshot(
				path,
				sessionID,
				revision,
				identity,
				nil,
			) == nil
		},
	)
}

func (s *File) retainCheckpointFiles(directory string, sessionID string, keep int) error {
	return s.retainArtifacts(
		directory,
		checkpointFilePattern,
		keep,
		func(path string, revision uint64, identity string) bool {
			return validateStoredCheckpoint(
				path,
				sessionID,
				revision,
				identity,
			) == nil
		},
	)
}

func validateStoredCheckpoint(
	path string,
	sessionID string,
	revision uint64,
	checksum string,
) error {
	var checkpoint rinruntime.Checkpoint
	if err := decodeJSONFile(path, &checkpoint); err != nil {
		return fmt.Errorf("read published checkpoint: %w", err)
	}
	if checkpoint.SessionID != sessionID ||
		checkpoint.Revision != revision ||
		checkpoint.Checksum != checksum {
		return errors.New("published checkpoint does not match its storage identity")
	}
	return rinruntime.ValidateCheckpoint(checkpoint)
}

func (s *File) removeInvalidArtifact(directory string, path string) error {
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	// The invalid file is only a derived cache. Persist its removal before
	// publishing the replacement so a crash can expose either no cache or a
	// fully synced cache, never the rejected bytes again.
	return s.syncDir(directory)
}

func (s *File) retainArtifacts(
	directory string,
	pattern *regexp.Regexp,
	keep int,
	valid func(path string, revision uint64, identity string) bool,
) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	artifacts := make([]retainedArtifact, 0)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		matches := pattern.FindStringSubmatch(entry.Name())
		if matches == nil {
			continue
		}
		revision, err := strconv.ParseUint(matches[1], 10, 64)
		if err != nil || !valid(filepath.Join(directory, entry.Name()), revision, matches[2]) {
			continue
		}
		artifacts = append(artifacts, retainedArtifact{name: entry.Name(), revision: revision})
	}
	sort.Slice(artifacts, func(left, right int) bool {
		if artifacts[left].revision == artifacts[right].revision {
			return artifacts[left].name < artifacts[right].name
		}
		return artifacts[left].revision < artifacts[right].revision
	})
	if len(artifacts) <= keep {
		return nil
	}
	removed := false
	var removeErr error
	for _, artifact := range artifacts[:len(artifacts)-keep] {
		if err := os.Remove(filepath.Join(directory, artifact.name)); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				removed = true
				continue
			}
			removeErr = errors.Join(removeErr, err)
			continue
		}
		removed = true
	}
	var syncErr error
	if removed {
		syncErr = s.syncDir(directory)
	}
	return errors.Join(removeErr, syncErr)
}

func (s *File) SaveCheckpoint(sessionID string, checkpoint rinruntime.Checkpoint) error {
	directory, done, err := s.beginArtifact(sessionID)
	if err != nil {
		return err
	}
	defer done()
	if err := s.confirmArtifactSession(sessionID, directory); err != nil {
		return err
	}
	if err := rinruntime.ValidateCheckpoint(checkpoint); err != nil {
		return err
	}
	if checkpoint.SessionID != sessionID || checkpoint.Revision == 0 ||
		!safeHash.MatchString(checkpoint.HeadHash) ||
		!safeHash.MatchString(checkpoint.Checksum) {
		return errors.New("checkpoint storage identity is invalid")
	}
	destination := filepath.Join(
		directory,
		fmt.Sprintf("checkpoint-%020d-%s.json", checkpoint.Revision, checkpoint.Checksum),
	)
	if existingErr := validateStoredCheckpoint(
		destination,
		sessionID,
		checkpoint.Revision,
		checkpoint.Checksum,
	); existingErr == nil {
		if err := s.syncEventFile(destination); err != nil {
			return fmt.Errorf("sync existing checkpoint: %w", err)
		}
		if err := s.syncDir(directory); err != nil {
			return fmt.Errorf("sync checkpoint directory: %w", err)
		}
		return s.retainCheckpointFiles(directory, sessionID, checkpointRetentionCount)
	} else if !errors.Is(existingErr, os.ErrNotExist) {
		if err := s.removeInvalidArtifact(directory, destination); err != nil {
			return fmt.Errorf(
				"replace invalid checkpoint: %w",
				errors.Join(existingErr, err),
			)
		}
	}
	if err := s.writeJSONAtomically(directory, ".checkpoint-*.tmp", destination, checkpoint, false); err != nil {
		return err
	}
	if err := validateStoredCheckpoint(
		destination,
		sessionID,
		checkpoint.Revision,
		checkpoint.Checksum,
	); err != nil {
		return fmt.Errorf("published checkpoint is invalid: %w", err)
	}
	return s.retainCheckpointFiles(directory, sessionID, checkpointRetentionCount)
}

func (s *File) LoadCheckpoint(
	sessionID string,
	atOrBeforeRevision uint64,
) (rinruntime.Checkpoint, error) {
	directory, done, err := s.beginArtifact(sessionID)
	if err != nil {
		return rinruntime.Checkpoint{}, err
	}
	defer done()
	if err := s.confirmArtifactSession(sessionID, directory); err != nil {
		return rinruntime.Checkpoint{}, err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return rinruntime.Checkpoint{}, rinruntime.ErrNotFound
		}
		return rinruntime.Checkpoint{}, err
	}
	candidates := make([]retainedArtifact, 0)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		matches := checkpointFilePattern.FindStringSubmatch(entry.Name())
		if matches == nil {
			continue
		}
		revision, parseErr := strconv.ParseUint(matches[1], 10, 64)
		if parseErr != nil || revision > atOrBeforeRevision {
			continue
		}
		candidates = append(candidates, retainedArtifact{name: entry.Name(), revision: revision})
	}
	sort.Slice(candidates, func(left, right int) bool {
		if candidates[left].revision == candidates[right].revision {
			return candidates[left].name > candidates[right].name
		}
		return candidates[left].revision > candidates[right].revision
	})
	for _, candidate := range candidates {
		var checkpoint rinruntime.Checkpoint
		path := filepath.Join(directory, candidate.name)
		if err := decodeJSONFile(path, &checkpoint); err != nil {
			continue
		}
		matches := checkpointFilePattern.FindStringSubmatch(candidate.name)
		if checkpoint.SessionID != sessionID ||
			checkpoint.Revision != candidate.revision ||
			checkpoint.Checksum != matches[2] {
			continue
		}
		if err := rinruntime.ValidateCheckpoint(checkpoint); err != nil {
			continue
		}
		return checkpoint, nil
	}
	return rinruntime.Checkpoint{}, rinruntime.ErrNotFound
}
