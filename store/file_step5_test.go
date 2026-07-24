package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sunrioa/rin/policy"
	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
)

func TestFileStoreOwnsDataDirectoryUntilClose(t *testing.T) {
	root := t.TempDir()
	first, err := OpenFile(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFile(root); !errors.Is(err, ErrDataDirectoryLocked) {
		t.Fatalf("second OpenFile error = %v, want ErrDataDirectoryLocked", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("second Close should be idempotent: %v", err)
	}
	if _, err := first.ListSessions(); !errors.Is(err, ErrFileClosed) {
		t.Fatalf("operation after Close error = %v, want ErrFileClosed", err)
	}
	reopened, err := OpenFile(root)
	if err != nil {
		t.Fatalf("lock was not released by Close: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenFilePreflightRunsBeforeFilesystemMutation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "must-not-exist", "rin-data")
	sentinel := errors.New("injected unsupported lock platform")
	fileStore, err := openFileWithPreflight(root, func() error {
		return sentinel
	})
	if fileStore != nil || !errors.Is(err, sentinel) {
		t.Fatalf("preflight result store=%v err=%v, want nil/sentinel", fileStore, err)
	}
	if _, err := os.Stat(filepath.Dir(root)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("OpenFile mutated the filesystem before lock preflight: %v", err)
	}
}

func TestFileStoreLockBuildTagsMatchSupportedGOOS(t *testing.T) {
	targets := []struct {
		goos      string
		goarch    string
		supported bool
	}{
		{goos: "darwin", goarch: "amd64", supported: true},
		{goos: "linux", goarch: "amd64", supported: true},
		{goos: "ios", goarch: "arm64"},
		{goos: "android", goarch: "arm64"},
		{goos: "windows", goarch: "amd64"},
		{goos: "freebsd", goarch: "amd64"},
	}
	goTool := filepath.Join(runtime.GOROOT(), "bin", "go")
	for _, target := range targets {
		t.Run(target.goos, func(t *testing.T) {
			command := exec.Command(
				goTool,
				"list",
				"-f",
				"{{join .GoFiles \",\"}}",
				".",
			)
			command.Env = append(
				os.Environ(),
				"GOOS="+target.goos,
				"GOARCH="+target.goarch,
				"CGO_ENABLED=0",
			)
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("go list for %s/%s: %v\n%s", target.goos, target.goarch, err, output)
			}
			files := string(output)
			hasUnix := strings.Contains(files, "lock_unix.go")
			hasUnsupported := strings.Contains(files, "lock_unsupported.go")
			if hasUnix != target.supported || hasUnsupported == target.supported {
				t.Fatalf(
					"%s/%s files=%q, supported=%t",
					target.goos,
					target.goarch,
					strings.TrimSpace(files),
					target.supported,
				)
			}
		})
	}
}

func TestFileStoreLockIsReleasedAfterProcessExit(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("flock implementation is intentionally limited to darwin and linux")
	}
	if os.Getenv("RIN_STORE_LOCK_HELPER") == "1" {
		store, err := OpenFile(os.Getenv("RIN_STORE_LOCK_ROOT"))
		if err != nil {
			os.Exit(2)
		}
		if err := os.WriteFile(os.Getenv("RIN_STORE_LOCK_READY"), []byte("ready"), 0o600); err != nil {
			os.Exit(3)
		}
		for {
			runtime.KeepAlive(store)
			time.Sleep(time.Second)
		}
	}
	root := t.TempDir()
	ready := filepath.Join(t.TempDir(), "ready")
	command := exec.Command(os.Args[0], "-test.run=^TestFileStoreLockIsReleasedAfterProcessExit$")
	command.Env = append(
		os.Environ(),
		"RIN_STORE_LOCK_HELPER=1",
		"RIN_STORE_LOCK_ROOT="+root,
		"RIN_STORE_LOCK_READY="+ready,
	)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	exited := false
	defer func() {
		if !exited {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("helper did not acquire the data-directory lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := OpenFile(root); !errors.Is(err, ErrDataDirectoryLocked) {
		t.Fatalf("parent acquired helper-owned directory: %v", err)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("killed helper unexpectedly exited successfully")
	}
	exited = true
	reopened, err := OpenFile(root)
	if err != nil {
		t.Fatalf("process exit did not release the directory lock: %v", err)
	}
	defer reopened.Close()
}

func TestFileStoreRangeIndexRebuildsAndBoundsPages(t *testing.T) {
	root := t.TempDir()
	fileStore, engine := newStep5FileEngine(t, root, "session.range")
	for revision := 2; revision <= 8; revision++ {
		step5Observe(t, engine, "session.range", revision)
	}
	head, err := fileStore.Head("session.range")
	if err != nil {
		t.Fatal(err)
	}
	if head.Revision != 8 {
		t.Fatalf("head revision = %d, want 8", head.Revision)
	}
	page, err := fileStore.LoadRange("session.range", 2, 7, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !page.HasMore || len(page.Events) != 2 ||
		page.Events[0].Sequence != 3 || page.Events[1].Sequence != 4 {
		t.Fatalf("unexpected bounded page: %+v", page)
	}
	indexPath := filepath.Join(root, "sessions", "session.range", "events.idx")
	if err := fileStore.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(indexPath, []byte("{\"version\":\"broken\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fileStore, err = OpenFile(root)
	if err != nil {
		t.Fatal(err)
	}
	defer fileStore.Close()
	page, err = fileStore.LoadRange("session.range", 4, 8, 16)
	if err != nil {
		t.Fatalf("bad index was not rebuilt: %v", err)
	}
	if page.HasMore || len(page.Events) != 4 ||
		page.Events[0].Sequence != 5 || page.Events[3].Sequence != 8 {
		t.Fatalf("unexpected rebuilt-index page: %+v", page)
	}
	var header eventIndexHeader
	indexFile, err := os.Open(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewDecoder(indexFile).Decode(&header); err != nil {
		_ = indexFile.Close()
		t.Fatal(err)
	}
	_ = indexFile.Close()
	if header.Version != eventIndexVersion {
		t.Fatalf("rebuilt index version = %q", header.Version)
	}
}

func TestFileStoreRepairsMissingLaggingTruncatedAndBadIndexes(t *testing.T) {
	root := t.TempDir()
	fileStore, engine := newStep5FileEngine(t, root, "session.index-repair")
	for revision := 2; revision <= 6; revision++ {
		step5Observe(t, engine, "session.index-repair", revision)
	}
	if err := fileStore.Close(); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(root, "sessions", "session.index-repair", "events.idx")
	cases := []struct {
		name   string
		mutate func([]byte) error
	}{
		{
			name: "missing",
			mutate: func([]byte) error {
				return os.Remove(indexPath)
			},
		},
		{
			name: "lagging",
			mutate: func(payload []byte) error {
				trimmed := bytes.TrimSuffix(payload, []byte{'\n'})
				lastLine := bytes.LastIndexByte(trimmed, '\n')
				if lastLine < 0 {
					return errors.New("valid index has no entry lines")
				}
				return os.WriteFile(indexPath, trimmed[:lastLine+1], 0o600)
			},
		},
		{
			name: "truncated",
			mutate: func(payload []byte) error {
				return os.WriteFile(indexPath, payload[:len(payload)/2], 0o600)
			},
		},
		{
			name: "missing-final-newline",
			mutate: func(payload []byte) error {
				return os.WriteFile(indexPath, bytes.TrimSuffix(payload, []byte{'\n'}), 0o600)
			},
		},
		{
			name: "bad-offset",
			mutate: func(payload []byte) error {
				corrupted := bytes.Replace(payload, []byte(`"offset":0`), []byte(`"offset":1`), 1)
				if bytes.Equal(corrupted, payload) {
					return errors.New("valid index had no first offset")
				}
				return os.WriteFile(indexPath, corrupted, 0o600)
			},
		},
		{
			name: "bad-tail-hash",
			mutate: func(payload []byte) error {
				corrupted := append([]byte(nil), payload...)
				marker := []byte(`"hash":"`)
				position := bytes.LastIndex(corrupted, marker)
				if position < 0 {
					return errors.New("valid index had no hash")
				}
				position += len(marker)
				if corrupted[position] == '0' {
					corrupted[position] = '1'
				} else {
					corrupted[position] = '0'
				}
				return os.WriteFile(indexPath, corrupted, 0o600)
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			payload, err := os.ReadFile(indexPath)
			if err != nil && test.name != "missing" {
				t.Fatal(err)
			}
			if err := test.mutate(payload); err != nil {
				t.Fatal(err)
			}
			reopened, err := OpenFile(root)
			if err != nil {
				t.Fatal(err)
			}
			head, headErr := reopened.Head("session.index-repair")
			if headErr != nil || head.Revision != 6 {
				_ = reopened.Close()
				t.Fatalf("index Head repair failed: head=%+v err=%v", head, headErr)
			}
			page, rangeErr := reopened.LoadRange("session.index-repair", 1, 6, 16)
			closeErr := reopened.Close()
			if rangeErr != nil || closeErr != nil {
				t.Fatalf("index repair failed: range=%v close=%v", rangeErr, closeErr)
			}
			if page.HasMore || len(page.Events) != 5 ||
				page.Events[0].Sequence != 2 || page.Events[4].Sequence != 6 {
				t.Fatalf("unexpected repaired page: %+v", page)
			}
		})
	}
}

func TestFileStorePerSessionIndexAccessIsConcurrentSafe(t *testing.T) {
	root := t.TempDir()
	fileStore, engine := newStep5FileEngine(t, root, "session.concurrent-a")
	createStep5Session(t, engine, "session.concurrent-b")
	var wait sync.WaitGroup
	for _, sessionID := range []string{"session.concurrent-a", "session.concurrent-b"} {
		sessionID := sessionID
		wait.Add(1)
		go func() {
			defer wait.Done()
			for revision := 2; revision <= 16; revision++ {
				step5Observe(t, engine, sessionID, revision)
				head, err := fileStore.Head(sessionID)
				if err != nil {
					t.Errorf("Head(%s): %v", sessionID, err)
					return
				}
				if _, err := fileStore.LoadRange(sessionID, 0, head.Revision, 4); err != nil {
					t.Errorf("LoadRange(%s): %v", sessionID, err)
					return
				}
			}
		}()
	}
	wait.Wait()
	if err := fileStore.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestFileStoreRetainsTwoSnapshotsAndCheckpoints(t *testing.T) {
	root := t.TempDir()
	fileStore, engine := newStep5FileEngine(t, root, "session.retention")
	for revision := 1; revision <= 3; revision++ {
		if revision > 1 {
			step5Observe(t, engine, "session.retention", revision)
		}
		snapshot, err := engine.Snapshot(protocol.SessionRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       "session.retention",
		})
		if err != nil {
			t.Fatal(err)
		}
		checkpoint, err := rinruntime.BuildCheckpoint(
			snapshot.State,
			*snapshot.IdentifierHistory,
			0,
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := fileStore.SaveCheckpoint("session.retention", checkpoint); err != nil {
			t.Fatal(err)
		}
	}
	directory := filepath.Join(root, "sessions", "session.retention")
	snapshots, err := filepath.Glob(filepath.Join(directory, "snapshot-*.json"))
	if err != nil || len(snapshots) != 2 {
		t.Fatalf("retained snapshots = %v, err=%v", snapshots, err)
	}
	checkpoints, err := filepath.Glob(filepath.Join(directory, "checkpoint-*.json"))
	if err != nil || len(checkpoints) != 2 {
		t.Fatalf("retained checkpoints = %v, err=%v", checkpoints, err)
	}
	latest, err := fileStore.LoadCheckpoint("session.retention", 3)
	if err != nil || latest.Revision != 3 {
		t.Fatalf("latest checkpoint = %+v, err=%v", latest, err)
	}
	previous, err := fileStore.LoadCheckpoint("session.retention", 2)
	if err != nil || previous.Revision != 2 {
		t.Fatalf("previous checkpoint = %+v, err=%v", previous, err)
	}
	if _, err := fileStore.LoadCheckpoint("session.retention", 1); !errors.Is(err, rinruntime.ErrNotFound) {
		t.Fatalf("pruned checkpoint error = %v, want ErrNotFound", err)
	}
	sort.Strings(checkpoints)
	var corrupted rinruntime.Checkpoint
	payload, err := os.ReadFile(checkpoints[len(checkpoints)-1])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(payload, &corrupted); err != nil {
		t.Fatal(err)
	}
	corrupted.Snapshot.State.Tick++
	payload, err = json.Marshal(corrupted)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(checkpoints[len(checkpoints)-1], payload, 0o600); err != nil {
		t.Fatal(err)
	}
	fallback, err := fileStore.LoadCheckpoint("session.retention", 3)
	if err != nil || fallback.Revision != 2 {
		t.Fatalf("corrupt latest checkpoint did not fall back: %+v err=%v", fallback, err)
	}
	step5Observe(t, engine, "session.retention", 4)
	snapshot, err := engine.Snapshot(protocol.SessionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       "session.retention",
	})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := rinruntime.BuildCheckpoint(
		snapshot.State,
		*snapshot.IdentifierHistory,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := fileStore.SaveCheckpoint("session.retention", checkpoint); err != nil {
		t.Fatal(err)
	}
	fallback, err = fileStore.LoadCheckpoint("session.retention", 3)
	if err != nil || fallback.Revision != 2 {
		t.Fatalf("invalid checkpoint consumed a retention slot: %+v err=%v", fallback, err)
	}
	if err := fileStore.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestEventRecordReadableLimitIsExact(t *testing.T) {
	event := protocol.EventRecord{
		Sequence: 1, Type: rinruntime.EventSessionCreated, RequestID: "limit",
		Hash: strings.Repeat("a", 64), RecordedAt: "2026-01-01T00:00:00Z",
		Data: json.RawMessage(`""`),
	}
	baseline, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	event.Data = json.RawMessage(`"` + strings.Repeat("a", maxEventRecordBytes-len(baseline)) + `"`)
	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) != maxEventRecordBytes {
		t.Fatalf("fixture size = %d, want %d", len(payload), maxEventRecordBytes)
	}
	path := filepath.Join(t.TempDir(), "events.jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeEvent(file, event); err != nil {
		_ = file.Close()
		t.Fatalf("exact-limit write failed: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	events, err := readEventFile(path)
	if err != nil || len(events) != 1 {
		t.Fatalf("exact-limit read failed: count=%d err=%v", len(events), err)
	}
	event.Data = append(event.Data[:len(event.Data)-1], 'a', '"')
	file, err = os.OpenFile(filepath.Join(t.TempDir(), "too-large.jsonl"), os.O_CREATE|os.O_RDWR|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeEvent(file, event); !errors.Is(err, rinruntime.ErrCorruptLog) {
		_ = file.Close()
		t.Fatalf("oversized writer error = %v, want ErrCorruptLog", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	oversizedPayload, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	readerPath := filepath.Join(t.TempDir(), "too-large-reader.jsonl")
	oversizedPayload = append(oversizedPayload, '\n')
	if err := os.WriteFile(readerPath, oversizedPayload, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readEventFile(readerPath); !errors.Is(err, rinruntime.ErrCorruptLog) {
		t.Fatalf("oversized reader error = %v, want ErrCorruptLog", err)
	}
}

func TestFileStoreCleansOnlyRecognizedTemporaryArtifacts(t *testing.T) {
	root := t.TempDir()
	sessions := filepath.Join(root, "sessions")
	if err := os.MkdirAll(filepath.Join(sessions, "session.cleanup"), 0o700); err != nil {
		t.Fatal(err)
	}
	temporaryDirectory := filepath.Join(sessions, ".session-abandoned.tmp")
	if err := os.Mkdir(temporaryDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	temporarySnapshot := filepath.Join(sessions, "session.cleanup", ".snapshot-abandoned.tmp")
	if err := os.WriteFile(temporarySnapshot, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	unrecognized := filepath.Join(sessions, "session.cleanup", "keep.tmp")
	if err := os.WriteFile(unrecognized, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	fileStore, err := OpenFile(root)
	if err != nil {
		t.Fatal(err)
	}
	defer fileStore.Close()
	if _, err := os.Stat(temporaryDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("abandoned session temp still exists: %v", err)
	}
	if _, err := os.Stat(temporarySnapshot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("abandoned snapshot temp still exists: %v", err)
	}
	if _, err := os.Stat(unrecognized); err != nil {
		t.Fatalf("unrecognized file was removed: %v", err)
	}
}

func TestFileStoreCreateRecoversLegacyEmptySessionDirectory(t *testing.T) {
	root := t.TempDir()
	fileStore, err := OpenFile(root)
	if err != nil {
		t.Fatal(err)
	}
	defer fileStore.Close()
	sessionID := "session.empty-recovery"
	if err := os.Mkdir(filepath.Join(root, "sessions", sessionID), 0o700); err != nil {
		t.Fatal(err)
	}
	engine, err := rinruntime.Open(fileStore, policy.Deterministic{})
	if err != nil {
		t.Fatal(err)
	}
	createStep5Session(t, engine, sessionID)
	events, err := fileStore.Load(sessionID)
	if err != nil || len(events) != 1 {
		t.Fatalf("empty-directory recovery events=%d err=%v", len(events), err)
	}
}

func TestMemoryRangeStoreRejectsInvalidEventHashes(t *testing.T) {
	memory := NewMemory()
	created := protocol.EventRecord{
		Sequence: 1, Type: rinruntime.EventSessionCreated, RequestID: "create.memory",
		RecordedAt: "2026-01-01T00:00:00Z", Data: json.RawMessage(`{"value":"created"}`),
	}
	created.Hash = testEventHash(created)
	if err := memory.Create("session.memory", created); err != nil {
		t.Fatal(err)
	}
	invalidAppend := protocol.EventRecord{
		Sequence: 2, Type: rinruntime.EventObserved, RequestID: "observe.memory",
		PrevHash: created.Hash, Hash: strings.Repeat("f", 64),
		RecordedAt: "2026-01-01T00:00:01Z", Data: json.RawMessage(`{"value":"observed"}`),
	}
	if err := memory.Append("session.memory", invalidAppend); !errors.Is(err, rinruntime.ErrConflict) {
		t.Fatalf("invalid append error = %v, want ErrConflict", err)
	}
	invalidCreate := created
	invalidCreate.Hash = strings.Repeat("f", 64)
	if err := memory.Create("session.invalid", invalidCreate); !errors.Is(err, rinruntime.ErrCorruptLog) {
		t.Fatalf("invalid create error = %v, want ErrCorruptLog", err)
	}
}

func TestStoresRejectInvalidExactExistingCreate(t *testing.T) {
	invalid := protocol.EventRecord{
		Sequence: 1, Type: rinruntime.EventSessionCreated, RequestID: "create.invalid-existing",
		Hash:       strings.Repeat("f", 64),
		RecordedAt: "2026-01-01T00:00:00Z",
		Data:       json.RawMessage(`{"value":"created"}`),
	}
	memory := NewMemory()
	memory.events["session.invalid-existing"] = []protocol.EventRecord{invalid}
	if err := memory.Create("session.invalid-existing", invalid); !errors.Is(err, rinruntime.ErrCorruptLog) {
		t.Fatalf("Memory.Create invalid exact-existing error = %v, want ErrCorruptLog", err)
	}

	root := t.TempDir()
	fileStore, err := OpenFile(root)
	if err != nil {
		t.Fatal(err)
	}
	defer fileStore.Close()
	directory := filepath.Join(root, "sessions", "session.invalid-existing")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	payload, err := encodeEventRecord(invalid)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "events.jsonl"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := fileStore.Create("session.invalid-existing", invalid); !errors.Is(err, rinruntime.ErrCorruptLog) {
		t.Fatalf("File.Create invalid exact-existing error = %v, want ErrCorruptLog", err)
	}
}

func TestFileStoreCreatePropagatesCorruptExistingLog(t *testing.T) {
	root := t.TempDir()
	const sessionID = "session.create-corrupt"
	fileStore, _ := newStep5FileEngine(t, root, sessionID)
	defer fileStore.Close()
	events, err := fileStore.Load(sessionID)
	if err != nil || len(events) != 1 {
		t.Fatalf("fixture events=%d err=%v", len(events), err)
	}
	logPath := filepath.Join(root, "sessions", sessionID, "events.jsonl")
	if err := os.WriteFile(logPath, []byte("{\"incomplete\":"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := fileStore.Create(sessionID, events[0]); !errors.Is(err, rinruntime.ErrCorruptLog) {
		t.Fatalf("Create on corrupt existing log error = %v, want ErrCorruptLog", err)
	}
}

func TestMemoryArtifactRetriesDoNotConsumeRetentionSlots(t *testing.T) {
	memory := NewMemory()
	engine, err := rinruntime.Open(memory, policy.Deterministic{})
	if err != nil {
		t.Fatal(err)
	}
	createStep5Session(t, engine, "session.retention")
	snapshotOne, err := engine.Snapshot(protocol.SessionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       "session.retention",
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := rinruntime.BuildCheckpoint(
		snapshotOne.State,
		*snapshotOne.IdentifierHistory,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := memory.SaveCheckpoint("session.retention", first); err != nil {
		t.Fatal(err)
	}
	step5Observe(t, engine, "session.retention", 2)
	snapshotTwo, err := engine.Snapshot(protocol.SessionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       "session.retention",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := rinruntime.BuildCheckpoint(
		snapshotTwo.State,
		*snapshotTwo.IdentifierHistory,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := memory.SaveCheckpoint("session.retention", second); err != nil {
		t.Fatal(err)
	}
	if err := memory.SaveCheckpoint("session.retention", second); err != nil {
		t.Fatal(err)
	}
	if len(memory.checkpoints["session.retention"]) != checkpointRetentionCount {
		t.Fatalf("checkpoint retry consumed a retention slot: %+v", memory.checkpoints)
	}
	if loaded, err := memory.LoadCheckpoint("session.retention", 1); err != nil ||
		loaded.Checksum != first.Checksum {
		t.Fatalf("first checkpoint was displaced by retry: %+v err=%v", loaded, err)
	}
	if err := memory.SaveSnapshot("session.retention", snapshotOne); err != nil {
		t.Fatal(err)
	}
	if err := memory.SaveSnapshot("session.retention", snapshotTwo); err != nil {
		t.Fatal(err)
	}
	if err := memory.SaveSnapshot("session.retention", snapshotTwo); err != nil {
		t.Fatal(err)
	}
	if len(memory.snapshots["session.retention"]) != snapshotRetentionCount {
		t.Fatalf("snapshot retry consumed a retention slot: %+v", memory.snapshots)
	}
	actor := snapshotTwo.State.Actors["npc.one"]
	actor.DisplayName = "caller mutation"
	snapshotTwo.State.Actors["npc.one"] = actor
	for _, retained := range memory.snapshots["session.retention"] {
		if err := rinruntime.ValidateSnapshot(retained); err != nil {
			t.Fatalf("caller mutation changed retained snapshot: %v", err)
		}
	}
}

func TestFileStoreDurabilityUncertaintyBlocksUntilExactRetry(t *testing.T) {
	root := t.TempDir()
	fileStore, engine := newStep5FileEngine(t, root, "session.uncertain")
	defer fileStore.Close()
	createStep5Session(t, engine, "session.unaffected")
	step5Observe(t, engine, "session.uncertain", 2)
	snapshot, err := engine.Snapshot(protocol.SessionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       "session.uncertain",
	})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := rinruntime.BuildCheckpoint(
		snapshot.State,
		*snapshot.IdentifierHistory,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := fileStore.SaveCheckpoint("session.uncertain", checkpoint); err != nil {
		t.Fatal(err)
	}
	events, err := fileStore.Load("session.uncertain")
	if err != nil || len(events) != 2 {
		t.Fatalf("fixture events=%d err=%v", len(events), err)
	}
	firstLine, err := encodeEventRecord(events[0])
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "sessions", "session.uncertain", "events.jsonl")
	closed, err := os.OpenFile(logPath, os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	rollbackErr := fileStore.rollbackFailedAppend(
		"session.uncertain",
		events[1],
		closed,
		int64(len(firstLine)),
	)
	if !errors.Is(rollbackErr, ErrDurabilityUncertain) {
		t.Fatalf("failed rollback error = %v, want ErrDurabilityUncertain", rollbackErr)
	}

	assertUncertain := func(name string, err error) {
		t.Helper()
		if !errors.Is(err, ErrDurabilityUncertain) {
			t.Fatalf("%s error = %v, want ErrDurabilityUncertain", name, err)
		}
	}
	_, err = fileStore.Load("session.uncertain")
	assertUncertain("Load", err)
	_, err = fileStore.Head("session.uncertain")
	assertUncertain("Head", err)
	_, err = fileStore.LoadRange("session.uncertain", 0, 2, 2)
	assertUncertain("LoadRange", err)
	_, err = fileStore.LoadCheckpoint("session.uncertain", 2)
	assertUncertain("LoadCheckpoint", err)
	assertUncertain(
		"SaveSnapshot",
		fileStore.SaveSnapshot("session.uncertain", snapshot),
	)
	assertUncertain(
		"SaveCheckpoint",
		fileStore.SaveCheckpoint("session.uncertain", checkpoint),
	)
	assertUncertain(
		"Create",
		fileStore.Create("session.uncertain", events[0]),
	)
	different := events[1]
	different.RequestID += ".different"
	assertUncertain(
		"different Append",
		fileStore.Append("session.uncertain", different),
	)
	if unaffected, err := fileStore.Load("session.unaffected"); err != nil ||
		len(unaffected) != 1 {
		t.Fatalf("uncertainty leaked across sessions: events=%d err=%v", len(unaffected), err)
	}

	if err := fileStore.Append("session.uncertain", events[1]); err != nil {
		t.Fatalf("exact uncertain append retry failed: %v", err)
	}
	recovered, err := fileStore.Load("session.uncertain")
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != len(events) ||
		!rinruntime.EventRecordsExactlyEqual(recovered[1], events[1]) {
		t.Fatalf("exact retry changed the log: %+v", recovered)
	}
	if _, exists := fileStore.uncertainAppend("session.uncertain"); exists {
		t.Fatal("successful exact retry did not clear uncertainty")
	}
}

func TestFileStoreReopenRequiresDurabilityFenceAfterUncertainAppend(t *testing.T) {
	root := t.TempDir()
	const sessionID = "session.reopen-uncertain"
	fileStore, engine := newStep5FileEngine(t, root, sessionID)
	step5Observe(t, engine, sessionID, 2)
	events, err := fileStore.Load(sessionID)
	if err != nil || len(events) != 2 {
		t.Fatalf("fixture events=%d err=%v", len(events), err)
	}
	firstLine, err := encodeEventRecord(events[0])
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "sessions", sessionID, "events.jsonl")
	priorOffset := int64(len(firstLine))
	if err := os.Truncate(logPath, priorOffset); err != nil {
		t.Fatal(err)
	}
	fileStore.markAppendUncertain(sessionID, events[1], priorOffset)
	if err := fileStore.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenFile(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	sentinel := errors.New("injected post-reopen event sync failure")
	syncAttempts := 0
	realSyncFile := reopened.syncEventFile
	reopened.syncEventFile = func(path string) error {
		if path == logPath {
			syncAttempts++
			if syncAttempts == 1 {
				return sentinel
			}
		}
		return realSyncFile(path)
	}
	if _, err := reopened.Load(sessionID); !errors.Is(err, sentinel) {
		t.Fatalf("first post-reopen Load error = %v, want injected sync failure", err)
	}
	if reopened.sessionDurabilityIsConfirmed(sessionID) {
		t.Fatal("failed post-reopen fence marked the session durable")
	}
	recovered, err := reopened.Load(sessionID)
	if err != nil {
		t.Fatalf("second post-reopen Load did not retry the fence: %v", err)
	}
	if len(recovered) != 1 ||
		!rinruntime.EventRecordsExactlyEqual(recovered[0], events[0]) {
		t.Fatalf("post-reopen fence changed the event log: %+v", recovered)
	}
	if syncAttempts != 2 || !reopened.sessionDurabilityIsConfirmed(sessionID) {
		t.Fatalf(
			"post-reopen fence attempts=%d confirmed=%t, want 2/true",
			syncAttempts,
			reopened.sessionDurabilityIsConfirmed(sessionID),
		)
	}
}

func TestFileStoreCreateParentFenceFailureIsRetriedByLoad(t *testing.T) {
	root := t.TempDir()
	fileStore, err := OpenFile(root)
	if err != nil {
		t.Fatal(err)
	}
	defer fileStore.Close()
	engine, err := rinruntime.Open(fileStore, policy.Deterministic{})
	if err != nil {
		t.Fatal(err)
	}
	const sessionID = "session.create-fence"
	sessionsDirectory := filepath.Join(root, "sessions")
	sentinel := errors.New("injected create parent sync failure")
	syncAttempts := 0
	realSyncDir := fileStore.syncDir
	fileStore.syncDir = func(path string) error {
		if path == sessionsDirectory {
			syncAttempts++
			if syncAttempts <= 2 {
				return sentinel
			}
		}
		return realSyncDir(path)
	}
	request := protocol.CreateSessionRequest{
		ProtocolVersion: protocol.Version,
		RequestID:       "create." + sessionID,
		SessionID:       sessionID,
		Binding: protocol.Binding{
			GameID: "game.test", ContentID: "base", ContentVersion: "1", ContentHash: "hash",
		},
		Actors: []protocol.ActorSeed{{
			ID: "npc.one", Kind: "npc", DisplayName: "One",
			ThinkEveryTicks: 1, Enabled: true,
		}},
	}
	_, err = engine.CreateSession(request)
	if !errors.Is(err, sentinel) {
		t.Fatalf("CreateSession error = %v, want injected parent sync failure", err)
	}
	if fileStore.sessionDurabilityIsConfirmed(sessionID) {
		t.Fatal("failed create parent fence marked the session durable")
	}
	events, err := fileStore.Load(sessionID)
	if err != nil {
		t.Fatalf("Load did not retry the create parent fence: %v", err)
	}
	if len(events) != 1 || events[0].Sequence != 1 {
		t.Fatalf("unexpected events after create fence recovery: %+v", events)
	}
	if _, err := engine.CreateSession(request); err != nil {
		t.Fatalf("exact CreateSession retry did not reconcile the durable event: %v", err)
	}
	if syncAttempts != 3 || !fileStore.sessionDurabilityIsConfirmed(sessionID) {
		t.Fatalf(
			"create parent fence attempts=%d confirmed=%t, want 3/true",
			syncAttempts,
			fileStore.sessionDurabilityIsConfirmed(sessionID),
		)
	}
}

func TestFileStoreReopenNonExactCreateFencesBeforeConflict(t *testing.T) {
	root := t.TempDir()
	const sessionID = "session.create-conflict-fence"
	fileStore, _ := newStep5FileEngine(t, root, sessionID)
	events, err := fileStore.Load(sessionID)
	if err != nil || len(events) != 1 {
		t.Fatalf("fixture events=%d err=%v", len(events), err)
	}
	if err := fileStore.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenFile(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	different := events[0]
	different.RequestID += ".different"
	different.Hash = testEventHash(different)
	logPath := filepath.Join(root, "sessions", sessionID, "events.jsonl")
	sentinel := errors.New("injected create comparison fence failure")
	syncAttempts := 0
	realSyncFile := reopened.syncEventFile
	reopened.syncEventFile = func(path string) error {
		if path == logPath {
			syncAttempts++
			if syncAttempts == 1 {
				return sentinel
			}
		}
		return realSyncFile(path)
	}
	if err := reopened.Create(sessionID, different); !errors.Is(err, sentinel) {
		t.Fatalf("first non-exact Create error = %v, want fence failure", err)
	}
	if reopened.sessionDurabilityIsConfirmed(sessionID) {
		t.Fatal("failed comparison fence marked the session durable")
	}
	if err := reopened.Create(sessionID, different); !errors.Is(err, rinruntime.ErrConflict) {
		t.Fatalf("second non-exact Create error = %v, want ErrConflict", err)
	}
	if syncAttempts != 2 || !reopened.sessionDurabilityIsConfirmed(sessionID) {
		t.Fatalf(
			"comparison fence attempts=%d confirmed=%t, want 2/true",
			syncAttempts,
			reopened.sessionDurabilityIsConfirmed(sessionID),
		)
	}
}

func TestFileStoreAppendRejectsCorruptActualTail(t *testing.T) {
	root := t.TempDir()
	fileStore, engine := newStep5FileEngine(t, root, "session.tail-bitrot")
	defer fileStore.Close()
	step5Observe(t, engine, "session.tail-bitrot", 2)
	logPath := filepath.Join(root, "sessions", "session.tail-bitrot", "events.jsonl")
	payload, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	before := []byte("Step 5 range event.")
	after := []byte("Step 5 range evenx.")
	if len(before) != len(after) {
		t.Fatal("tail corruption fixture must preserve file size")
	}
	position := bytes.LastIndex(payload, before)
	if position < 0 {
		t.Fatal("tail corruption fixture text was not found")
	}
	copy(payload[position:position+len(before)], after)
	if err := os.WriteFile(logPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = engine.Observe(protocol.ObserveRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       "session.tail-bitrot",
		RequestID:       "observe.session.tail-bitrot.3",
		EventID:         "event.session.tail-bitrot.3",
		Tick:            3,
		ObserverIDs:     []string{"npc.one"},
		Source:          "game",
		Kind:            "world",
		Summary:         "A new event must not extend a corrupt tail.",
		Importance:      2,
	})
	if !errors.Is(err, rinruntime.ErrCorruptLog) {
		t.Fatalf("append on corrupt tail error = %v, want ErrCorruptLog", err)
	}
	events, loadErr := fileStore.Load("session.tail-bitrot")
	if loadErr != nil || len(events) != 2 {
		t.Fatalf("corrupt-tail append changed log: events=%d err=%v", len(events), loadErr)
	}
	if _, err := fileStore.Head("session.tail-bitrot"); !errors.Is(err, rinruntime.ErrCorruptLog) {
		t.Fatalf("Head accepted corrupt actual tail: %v", err)
	}
}

func TestMemorySnapshotRetentionUsesRevisionNotArrivalOrder(t *testing.T) {
	source := NewMemory()
	engine, err := rinruntime.Open(source, policy.Deterministic{})
	if err != nil {
		t.Fatal(err)
	}
	createStep5Session(t, engine, "session.snapshot-order")
	snapshots := make([]protocol.Snapshot, 0, 3)
	for revision := 1; revision <= 3; revision++ {
		if revision > 1 {
			step5Observe(t, engine, "session.snapshot-order", revision)
		}
		snapshot, err := engine.Snapshot(protocol.SessionRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       "session.snapshot-order",
		})
		if err != nil {
			t.Fatal(err)
		}
		snapshots = append(snapshots, snapshot)
	}
	events, err := source.Load("session.snapshot-order")
	if err != nil {
		t.Fatal(err)
	}
	destination := NewMemory()
	if err := destination.Create("session.snapshot-order", events[0]); err != nil {
		t.Fatal(err)
	}
	for _, position := range []int{1, 2, 0} {
		if err := destination.SaveSnapshot(
			"session.snapshot-order",
			snapshots[position],
		); err != nil {
			t.Fatal(err)
		}
	}
	retained := destination.snapshots["session.snapshot-order"]
	if len(retained) != 2 ||
		retained[0].State.Revision != 2 ||
		retained[1].State.Revision != 3 {
		t.Fatalf("out-of-order retention kept %+v", retained)
	}
}

func TestArtifactStoresRejectInvalidChecksums(t *testing.T) {
	root := t.TempDir()
	fileStore, engine := newStep5FileEngine(t, root, "session.invalid-artifact")
	defer fileStore.Close()
	snapshot, err := engine.Snapshot(protocol.SessionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       "session.invalid-artifact",
	})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := rinruntime.BuildCheckpoint(
		snapshot.State,
		*snapshot.IdentifierHistory,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := fileStore.SaveSnapshot("session.invalid-artifact", snapshot); err != nil {
		t.Fatalf("exact snapshot retry failed: %v", err)
	}
	if err := fileStore.SaveCheckpoint("session.invalid-artifact", checkpoint); err != nil {
		t.Fatalf("first checkpoint save failed: %v", err)
	}
	if err := fileStore.SaveCheckpoint("session.invalid-artifact", checkpoint); err != nil {
		t.Fatalf("exact checkpoint retry failed: %v", err)
	}
	invalidSnapshot := snapshot
	invalidSnapshot.State.Tick++
	if err := fileStore.SaveSnapshot(
		"session.invalid-artifact",
		invalidSnapshot,
	); err == nil {
		t.Fatal("File accepted an invalid Snapshot checksum")
	}
	invalidCheckpoint := checkpoint
	invalidCheckpoint.Snapshot.State.Tick++
	if err := fileStore.SaveCheckpoint(
		"session.invalid-artifact",
		invalidCheckpoint,
	); err == nil {
		t.Fatal("File accepted an invalid checkpoint checksum")
	}

	memory := NewMemory()
	events, err := fileStore.Load("session.invalid-artifact")
	if err != nil {
		t.Fatal(err)
	}
	if err := memory.Create("session.invalid-artifact", events[0]); err != nil {
		t.Fatal(err)
	}
	if err := memory.SaveSnapshot(
		"session.invalid-artifact",
		invalidSnapshot,
	); err == nil {
		t.Fatal("Memory accepted an invalid Snapshot checksum")
	}
	if err := memory.SaveCheckpoint(
		"session.invalid-artifact",
		invalidCheckpoint,
	); err == nil {
		t.Fatal("Memory accepted an invalid checkpoint checksum")
	}
}

func TestFileStoreRepairsSameNameDerivedArtifacts(t *testing.T) {
	root := t.TempDir()
	const sessionID = "session.artifact-repair"
	fileStore, engine := newStep5FileEngine(t, root, sessionID)
	step5Observe(t, engine, sessionID, 2)
	snapshot, err := engine.Snapshot(protocol.SessionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := rinruntime.BuildCheckpoint(
		snapshot.State,
		*snapshot.IdentifierHistory,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := fileStore.SaveCheckpoint(sessionID, checkpoint); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "sessions", sessionID)
	snapshotPath := filepath.Join(
		directory,
		fmt.Sprintf(
			"snapshot-%020d-%s.json",
			snapshot.State.Revision,
			snapshot.StateHash,
		),
	)
	checkpointPath := filepath.Join(
		directory,
		fmt.Sprintf(
			"checkpoint-%020d-%s.json",
			checkpoint.Revision,
			checkpoint.Checksum,
		),
	)
	if err := fileStore.Close(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{snapshotPath, checkpointPath} {
		if err := os.WriteFile(path, []byte("{\"corrupt\":true}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	reopened, err := OpenFile(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopenedEngine, err := rinruntime.Open(reopened, policy.Deterministic{})
	if err != nil {
		t.Fatal(err)
	}
	state, err := reopenedEngine.State(protocol.SessionRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
	})
	if err != nil || state.Revision != snapshot.State.Revision {
		t.Fatalf("lazy recovery state=%+v err=%v", state, err)
	}
	var repairedCheckpoint rinruntime.Checkpoint
	deadline := time.Now().Add(5 * time.Second)
	var repairErr error
	for {
		repairedCheckpoint = rinruntime.Checkpoint{}
		repairErr = decodeJSONFile(checkpointPath, &repairedCheckpoint)
		if repairErr == nil {
			repairErr = rinruntime.ValidateCheckpoint(repairedCheckpoint)
		}
		if repairErr == nil &&
			repairedCheckpoint.Revision == checkpoint.Revision &&
			repairedCheckpoint.Checksum == checkpoint.Checksum {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf(
				"lazy recovery did not replace corrupt checkpoint: checkpoint=%+v err=%v",
				repairedCheckpoint,
				repairErr,
			)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := rinruntime.ValidateCheckpoint(repairedCheckpoint); err != nil {
		t.Fatalf("lazy recovery checkpoint is invalid: %v", err)
	}
	if err := reopened.SaveSnapshot(sessionID, snapshot); err != nil {
		t.Fatalf("corrupt same-name snapshot was not replaced: %v", err)
	}

	replacement, err := cloneSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	replacement.IdentifierHistory.Requests["request.future-tombstone"] =
		protocol.RequestIdentity{Ambiguous: true}
	replacement.IdentifierHistoryHash = testJSONHash(
		t,
		*replacement.IdentifierHistory,
	)
	if err := rinruntime.ValidateSnapshot(replacement); err != nil {
		t.Fatalf("replacement snapshot fixture is invalid: %v", err)
	}
	if replacement.StateHash != snapshot.StateHash ||
		replacement.IdentifierHistoryHash == snapshot.IdentifierHistoryHash {
		t.Fatal("replacement must share state identity but carry newer identifier history")
	}
	if err := reopened.SaveSnapshot(sessionID, replacement); err != nil {
		t.Fatalf("valid newer identifier history was not persisted: %v", err)
	}
	var persisted protocol.Snapshot
	if err := decodeJSONFile(snapshotPath, &persisted); err != nil {
		t.Fatal(err)
	}
	if err := rinruntime.ValidateSnapshot(persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.IdentifierHistoryHash != replacement.IdentifierHistoryHash {
		t.Fatalf(
			"persisted identifier history hash = %s, want %s",
			persisted.IdentifierHistoryHash,
			replacement.IdentifierHistoryHash,
		)
	}
	if _, exists := persisted.IdentifierHistory.Requests["request.future-tombstone"]; !exists {
		t.Fatal("same-state snapshot replacement lost the newer identifier history")
	}
	if err := reopened.SaveSnapshot(sessionID, replacement); err != nil {
		t.Fatalf("exact replacement retry failed: %v", err)
	}
}

func TestOpenFileDurablyCreatesNestedDirectoryTree(t *testing.T) {
	root := filepath.Join(t.TempDir(), "one", "two", "rin-data")
	fileStore, err := OpenFile(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := fileStore.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(root, "sessions"))
	if err != nil || !info.IsDir() {
		t.Fatalf("nested data directory was not created: info=%v err=%v", info, err)
	}
}

func TestMakeDirectoryTreeSyncedRetriesExistingRootParentFence(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "rin-data")
	sentinel := errors.New("injected root parent sync failure")
	parentSyncAttempts := 0
	syncDir := func(path string) error {
		if path == parent {
			parentSyncAttempts++
			if parentSyncAttempts == 1 {
				return sentinel
			}
		}
		return syncDirectory(path)
	}
	if err := makeDirectoryTreeSyncedWith(root, 0o700, syncDir); !errors.Is(err, sentinel) {
		t.Fatalf("first directory creation error = %v, want parent sync failure", err)
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		t.Fatalf("failed fence should leave the created root available for retry: info=%v err=%v", info, err)
	}
	if err := makeDirectoryTreeSyncedWith(root, 0o700, syncDir); err != nil {
		t.Fatalf("existing root did not retry its parent fence: %v", err)
	}
	if parentSyncAttempts != 2 {
		t.Fatalf("root parent sync attempts = %d, want 2", parentSyncAttempts)
	}
}

func TestMakeDirectoryTreeSyncedRetriesIntermediateAncestorFence(t *testing.T) {
	parent := t.TempDir()
	intermediate := filepath.Join(parent, "one")
	root := filepath.Join(intermediate, "two", "rin-data")
	sentinel := errors.New("injected intermediate parent sync failure")
	parentSyncAttempts := 0
	syncDir := func(path string) error {
		if path == parent {
			parentSyncAttempts++
			if parentSyncAttempts == 1 {
				return sentinel
			}
		}
		return syncDirectory(path)
	}
	if err := makeDirectoryTreeSyncedWith(root, 0o700, syncDir); !errors.Is(err, sentinel) {
		t.Fatalf("first nested creation error = %v, want intermediate sync failure", err)
	}
	if info, err := os.Stat(intermediate); err != nil || !info.IsDir() {
		t.Fatalf("failed fence should leave the intermediate directory: info=%v err=%v", info, err)
	}
	if _, err := os.Stat(filepath.Join(intermediate, "two")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("creation proceeded past the failed intermediate fence: %v", err)
	}
	if err := makeDirectoryTreeSyncedWith(root, 0o700, syncDir); err != nil {
		t.Fatalf("retry did not re-fence the existing intermediate directory: %v", err)
	}
	if parentSyncAttempts != 2 {
		t.Fatalf("intermediate parent sync attempts = %d, want 2", parentSyncAttempts)
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		t.Fatalf("retry did not complete the directory tree: info=%v err=%v", info, err)
	}
}

func TestMakeDirectoryTreeSyncedFencesFilesystemRoot(t *testing.T) {
	root := string(os.PathSeparator)
	var synced []string
	if err := makeDirectoryTreeSyncedWith(
		root,
		0o700,
		func(path string) error {
			synced = append(synced, path)
			return nil
		},
	); err != nil {
		t.Fatal(err)
	}
	if len(synced) != 1 || synced[0] != filepath.Clean(root) {
		t.Fatalf("filesystem root syncs = %v, want [%s]", synced, filepath.Clean(root))
	}
}

func TestFileStoreArtifactIOAllowsAppendAndCloseWaits(t *testing.T) {
	root := t.TempDir()
	const sessionID = "session.artifact-lock"
	fileStore, _, checkpoint, created := newStep5FileArtifactFixture(
		t,
		root,
		sessionID,
	)
	releaseFirstSync := make(chan struct{})
	releaseSecondSync := make(chan struct{})
	var releaseFirstOnce sync.Once
	var releaseSecondOnce sync.Once
	releaseFirst := func() {
		releaseFirstOnce.Do(func() {
			close(releaseFirstSync)
		})
	}
	releaseSecond := func() {
		releaseSecondOnce.Do(func() {
			close(releaseSecondSync)
		})
	}
	artifactMapLocked := false
	unlockArtifactMap := func() {
		if artifactMapLocked {
			artifactMapLocked = false
			fileStore.artifactsMu.Unlock()
		}
	}
	defer func() {
		releaseFirst()
		releaseSecond()
		unlockArtifactMap()
		_ = fileStore.Close()
	}()

	if err := fileStore.SaveCheckpoint(sessionID, checkpoint); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(
		root,
		"sessions",
		sessionID,
		fmt.Sprintf(
			"checkpoint-%020d-%s.json",
			checkpoint.Revision,
			checkpoint.Checksum,
		),
	)
	firstSyncStarted := make(chan struct{})
	secondSyncStarted := make(chan struct{})
	syncCall := 0
	var hookMu sync.Mutex
	realSyncFile := fileStore.syncEventFile
	fileStore.syncEventFile = func(path string) error {
		if path == destination {
			hookMu.Lock()
			syncCall++
			call := syncCall
			hookMu.Unlock()
			switch call {
			case 1:
				close(firstSyncStarted)
				<-releaseFirstSync
			case 2:
				close(secondSyncStarted)
				<-releaseSecondSync
			}
		}
		return realSyncFile(path)
	}

	firstSaveDone := make(chan error, 1)
	go func() {
		firstSaveDone <- fileStore.SaveCheckpoint(sessionID, checkpoint)
	}()
	select {
	case <-firstSyncStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("exact checkpoint save did not reach the injected file sync")
	}

	appendDone := make(chan error, 1)
	go func() {
		event := protocol.EventRecord{
			Sequence:   2,
			Type:       rinruntime.EventObserved,
			RequestID:  "observe." + sessionID + ".2",
			PrevHash:   created.Hash,
			RecordedAt: "2026-01-01T00:00:01Z",
			Data:       json.RawMessage(`{"value":"append-during-checkpoint-sync"}`),
		}
		event.Hash = testEventHash(event)
		appendDone <- fileStore.Append(sessionID, event)
	}()
	select {
	case err := <-appendDone:
		if err != nil {
			t.Fatalf("Append failed while checkpoint sync was blocked: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("same-session Append waited for checkpoint artifact I/O")
	}

	// Hold the lock-map mutex so the second call can deterministically acquire
	// lifecycle.RLock and then queue inside beginArtifact. Once the first save
	// finishes, lifecycle.TryLock distinguishes that queued reader from the
	// first operation without relying on a scheduling sleep.
	fileStore.artifactsMu.Lock()
	artifactMapLocked = true
	secondSaveDone := make(chan error, 1)
	go func() {
		secondSaveDone <- fileStore.SaveCheckpoint(sessionID, checkpoint)
	}()
	releaseFirst()
	select {
	case err := <-firstSaveDone:
		if err != nil {
			t.Fatalf("first checkpoint save failed after releasing sync: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first checkpoint save did not finish after releasing sync")
	}
	readerDeadline := time.Now().Add(5 * time.Second)
	for fileStore.lifecycle.TryLock() {
		fileStore.lifecycle.Unlock()
		if time.Now().After(readerDeadline) {
			t.Fatal("second checkpoint save did not enter the lifecycle")
		}
		runtime.Gosched()
	}

	closeDone := make(chan error, 1)
	go func() {
		closeDone <- fileStore.Close()
	}()
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned before in-flight checkpoint completed: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	unlockArtifactMap()
	select {
	case <-secondSyncStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("queued checkpoint save did not acquire the artifact lock")
	}
	select {
	case err := <-closeDone:
		t.Fatalf("Close overtook the queued checkpoint save: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	releaseSecond()
	select {
	case err := <-secondSaveDone:
		if err != nil {
			t.Fatalf("second checkpoint save failed after releasing sync: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second checkpoint save did not finish after releasing sync")
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close failed after checkpoint completed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not finish after checkpoint completed")
	}
}

func TestFileStoreArtifactRetryFencesExistingRename(t *testing.T) {
	root := t.TempDir()
	const sessionID = "session.artifact-fence"
	fileStore, snapshot, checkpoint, _ := newStep5FileArtifactFixture(
		t,
		root,
		sessionID,
	)
	defer fileStore.Close()
	directory := filepath.Join(root, "sessions", sessionID)
	cases := []struct {
		name        string
		destination string
		save        func() error
	}{
		{
			name: "snapshot",
			destination: filepath.Join(
				directory,
				fmt.Sprintf(
					"snapshot-%020d-%s.json",
					snapshot.State.Revision,
					snapshot.StateHash,
				),
			),
			save: func() error {
				return fileStore.SaveSnapshot(sessionID, snapshot)
			},
		},
		{
			name: "checkpoint",
			destination: filepath.Join(
				directory,
				fmt.Sprintf(
					"checkpoint-%020d-%s.json",
					checkpoint.Revision,
					checkpoint.Checksum,
				),
			),
			save: func() error {
				return fileStore.SaveCheckpoint(sessionID, checkpoint)
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if err := os.Remove(testCase.destination); err != nil &&
				!errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			if err := syncDirectory(directory); err != nil {
				t.Fatal(err)
			}
			sentinel := errors.New("injected artifact directory sync failure")
			directorySyncAttempts := 0
			destinationSyncAttempts := 0
			realSyncDir := syncDirectory
			realSyncFile := syncExistingFile
			fileStore.syncDir = func(path string) error {
				if path == directory {
					directorySyncAttempts++
					if directorySyncAttempts == 1 {
						return sentinel
					}
				}
				return realSyncDir(path)
			}
			fileStore.syncEventFile = func(path string) error {
				if path == testCase.destination {
					destinationSyncAttempts++
				}
				return realSyncFile(path)
			}
			t.Cleanup(func() {
				fileStore.syncDir = syncDirectory
				fileStore.syncEventFile = syncExistingFile
			})

			if err := testCase.save(); !errors.Is(err, sentinel) {
				t.Fatalf(
					"first save error = %v, want post-rename sync failure (directory sync attempts=%d, directory=%q, store root=%q)",
					err,
					directorySyncAttempts,
					directory,
					fileStore.root,
				)
			}
			if _, err := os.Stat(testCase.destination); err != nil {
				t.Fatalf("rename did not publish the artifact before sync failure: %v", err)
			}
			if err := testCase.save(); err != nil {
				t.Fatalf("exact retry did not fence the existing artifact: %v", err)
			}
			if destinationSyncAttempts != 1 || directorySyncAttempts != 2 {
				t.Fatalf(
					"retry sync attempts destination=%d directory=%d, want 1/2",
					destinationSyncAttempts,
					directorySyncAttempts,
				)
			}
		})
	}
}

func newStep5FileEngine(
	t *testing.T,
	root string,
	sessionID string,
) (*File, *rinruntime.Engine) {
	t.Helper()
	fileStore, err := OpenFile(root)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := rinruntime.Open(fileStore, policy.Deterministic{})
	if err != nil {
		_ = fileStore.Close()
		t.Fatal(err)
	}
	createStep5Session(t, engine, sessionID)
	return fileStore, engine
}

func newStep5FileArtifactFixture(
	t *testing.T,
	root string,
	sessionID string,
) (*File, protocol.Snapshot, rinruntime.Checkpoint, protocol.EventRecord) {
	t.Helper()
	memory := NewMemory()
	engine, err := rinruntime.Open(memory, policy.Deterministic{})
	if err != nil {
		t.Fatal(err)
	}
	createStep5Session(t, engine, sessionID)
	events, err := memory.Load(sessionID)
	if err != nil || len(events) != 1 {
		t.Fatalf("artifact fixture events=%d err=%v", len(events), err)
	}
	snapshot, err := engine.Replay(protocol.ReplayRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
		Revision:        1,
	})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := rinruntime.BuildCheckpoint(
		snapshot.State,
		*snapshot.IdentifierHistory,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	fileStore, err := OpenFile(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := fileStore.Create(sessionID, events[0]); err != nil {
		_ = fileStore.Close()
		t.Fatal(err)
	}
	return fileStore, snapshot, checkpoint, events[0]
}

func createStep5Session(t *testing.T, engine *rinruntime.Engine, sessionID string) {
	t.Helper()
	_, err := engine.CreateSession(protocol.CreateSessionRequest{
		ProtocolVersion: protocol.Version,
		RequestID:       "create." + sessionID,
		SessionID:       sessionID,
		Binding: protocol.Binding{
			GameID: "game.test", ContentID: "base", ContentVersion: "1", ContentHash: "hash",
		},
		Actors: []protocol.ActorSeed{{
			ID: "npc.one", Kind: "npc", DisplayName: "One",
			ThinkEveryTicks: 1, Enabled: true,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func step5Observe(
	t *testing.T,
	engine *rinruntime.Engine,
	sessionID string,
	revision int,
) {
	t.Helper()
	_, err := engine.Observe(protocol.ObserveRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
		RequestID:       "observe." + sessionID + "." + strconv.Itoa(revision),
		EventID:         "event." + sessionID + "." + strconv.Itoa(revision),
		Tick:            int64(revision),
		ObserverIDs:     []string{"npc.one"},
		Source:          "game",
		Kind:            "world",
		Summary:         "Step 5 range event.",
		Importance:      2,
	})
	if err != nil {
		t.Errorf("Observe(%s,%d): %v", sessionID, revision, err)
	}
}

func testEventHash(event protocol.EventRecord) string {
	payload, _ := json.Marshal(struct {
		Sequence   uint64          `json:"sequence"`
		Type       string          `json:"type"`
		RequestID  string          `json:"request_id"`
		PrevHash   string          `json:"prev_hash"`
		RecordedAt string          `json:"recorded_at"`
		Data       json.RawMessage `json:"data"`
	}{
		Sequence: event.Sequence, Type: event.Type, RequestID: event.RequestID,
		PrevHash: event.PrevHash, RecordedAt: event.RecordedAt, Data: event.Data,
	})
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func testJSONHash(t *testing.T, value any) string {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func BenchmarkMemoryLoadRangeLatePage(b *testing.B) {
	const eventCount = 10_000
	memory := NewMemory()
	events := make([]protocol.EventRecord, 0, eventCount)
	previousHash := ""
	for revision := 1; revision <= eventCount; revision++ {
		event := protocol.EventRecord{
			Sequence:   uint64(revision),
			Type:       rinruntime.EventObserved,
			RequestID:  "benchmark",
			PrevHash:   previousHash,
			RecordedAt: "2026-01-01T00:00:00Z",
			Data:       json.RawMessage(`{"value":"benchmark"}`),
		}
		event.Hash = testEventHash(event)
		events = append(events, event)
		previousHash = event.Hash
	}
	memory.events["benchmark"] = events
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		page, err := memory.LoadRange("benchmark", eventCount-2, eventCount, 1)
		if err != nil || len(page.Events) != 1 || !page.HasMore {
			b.Fatalf("late page = %+v, err=%v", page, err)
		}
	}
}
