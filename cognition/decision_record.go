package cognition

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"

	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/provider"
	"github.com/sunrioa/rin/timeline"
)

const (
	DecisionRecordSnapshotVersion = "rin.cognition.decision-records/v1"
	DefaultDecisionRecordLimit    = uint32(4_096)
)

var ErrDecisionRecordConflict = errors.New("cognition decision record conflict")

// DecisionRecord is private diagnostic evidence for one completed model call.
// It stores digests and bounded references, never prompt or response bodies.
type DecisionRecord struct {
	RecordID            string                      `json:"record_id"`
	TaskID              string                      `json:"task_id"`
	SessionID           string                      `json:"session_id"`
	ActorID             string                      `json:"actor_id"`
	ControllerID        string                      `json:"controller_id"`
	Step                uint32                      `json:"step"`
	InspectionRound     uint32                      `json:"inspection_round"`
	OccurredAtUnixMilli int64                       `json:"occurred_at_unix_millis"`
	ObservationID       string                      `json:"observation_id"`
	ObservationSequence uint64                      `json:"observation_sequence"`
	Epoch               host.Epoch                  `json:"epoch"`
	GoalDigest          string                      `json:"goal_digest"`
	PersonaID           string                      `json:"persona_id"`
	PersonaVersion      string                      `json:"persona_version"`
	PersonaDigest       string                      `json:"persona_digest"`
	CapabilityDigest    string                      `json:"capability_digest"`
	ContextDigest       string                      `json:"context_digest"`
	ProviderRequestHash string                      `json:"provider_request_digest,omitempty"`
	SkillRefs           []timeline.SkillContextRef  `json:"skill_refs,omitempty"`
	MemoryRefs          []timeline.MemoryContextRef `json:"memory_refs,omitempty"`
	DecisionKind        ModelDecisionKind           `json:"decision_kind"`
	DecisionSummary     string                      `json:"decision_summary"`
	SelectedCapability  *host.CapabilityRef         `json:"selected_capability,omitempty"`
	ProviderModel       string                      `json:"provider_model,omitempty"`
	LatencyMillis       uint64                      `json:"latency_ms"`
	Usage               provider.Usage              `json:"usage"`
}

type DecisionRecordSnapshot struct {
	Version  string           `json:"version"`
	Revision uint64           `json:"revision"`
	Records  []DecisionRecord `json:"records"`
}

type DecisionRecorder interface {
	Append(context.Context, DecisionRecord) error
	Snapshot(context.Context) (DecisionRecordSnapshot, error)
}

type LocalDecisionRecorder struct {
	mu       sync.RWMutex
	revision uint64
	limit    uint32
	records  []DecisionRecord
}

func NewLocalDecisionRecorder(limit uint32) (*LocalDecisionRecorder, error) {
	if limit == 0 {
		limit = DefaultDecisionRecordLimit
	}
	if limit > 100_000 {
		return nil, errors.New("decision record limit is too large")
	}
	return &LocalDecisionRecorder{revision: 1, limit: limit}, nil
}

func RestoreLocalDecisionRecorder(
	limit uint32,
	snapshot DecisionRecordSnapshot,
) (*LocalDecisionRecorder, error) {
	recorder, err := NewLocalDecisionRecorder(limit)
	if err != nil {
		return nil, err
	}
	if snapshot.Version != DecisionRecordSnapshotVersion || snapshot.Revision == 0 ||
		len(snapshot.Records) > int(recorder.limit) {
		return nil, errors.New("decision record snapshot is invalid")
	}
	seen := make(map[string]struct{}, len(snapshot.Records))
	for index, record := range snapshot.Records {
		sealed, err := sealDecisionRecord(record)
		if err != nil {
			return nil, fmt.Errorf("records[%d]: %w", index, err)
		}
		if _, duplicate := seen[sealed.RecordID]; duplicate {
			return nil, errors.New("decision record snapshot contains duplicate ids")
		}
		seen[sealed.RecordID] = struct{}{}
		recorder.records = append(recorder.records, sealed)
	}
	recorder.revision = snapshot.Revision
	return recorder, nil
}

func (recorder *LocalDecisionRecorder) Append(ctx context.Context, record DecisionRecord) error {
	if err := requireMemoryContext(ctx); err != nil {
		return err
	}
	sealed, err := sealDecisionRecord(record)
	if err != nil {
		return err
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	for _, current := range recorder.records {
		if current.RecordID != sealed.RecordID {
			continue
		}
		if reflect.DeepEqual(current, sealed) {
			return nil
		}
		return ErrDecisionRecordConflict
	}
	if len(recorder.records) == int(recorder.limit) {
		copy(recorder.records, recorder.records[1:])
		recorder.records[len(recorder.records)-1] = sealed
	} else {
		recorder.records = append(recorder.records, sealed)
	}
	recorder.revision++
	return nil
}

func (recorder *LocalDecisionRecorder) Snapshot(
	ctx context.Context,
) (DecisionRecordSnapshot, error) {
	if err := requireMemoryContext(ctx); err != nil {
		return DecisionRecordSnapshot{}, err
	}
	recorder.mu.RLock()
	defer recorder.mu.RUnlock()
	snapshot := DecisionRecordSnapshot{
		Version:  DecisionRecordSnapshotVersion,
		Revision: recorder.revision,
		Records:  make([]DecisionRecord, len(recorder.records)),
	}
	for index, record := range recorder.records {
		snapshot.Records[index] = cloneDecisionRecord(record)
	}
	return snapshot, nil
}

func newDecisionRecord(
	task TaskSession,
	input ModelInput,
	decision ModelDecision,
	latency uint64,
	occurredAt int64,
) (DecisionRecord, error) {
	sealed, observation, err := sealModelInput(input)
	if err != nil {
		return DecisionRecord{}, err
	}
	packet := buildModelV2Packet(sealed, observation)
	memories, skills := modelContextTimelineFields(sealed)
	record := DecisionRecord{
		RecordID: fmt.Sprintf("%s.decision.%d", task.TaskID, task.ModelCalls),
		TaskID:   task.TaskID, SessionID: task.SessionID, ActorID: task.ActorID,
		ControllerID: task.ControllerID, Step: task.Step,
		InspectionRound: sealed.InspectionRound, OccurredAtUnixMilli: occurredAt,
		ObservationID:       sealed.Observation.ObservationID,
		ObservationSequence: sealed.Observation.Sequence, Epoch: sealed.Observation.Epoch,
		GoalDigest: digestJSON(sealed.Task.Goal), PersonaID: sealed.Persona.PersonaID,
		PersonaVersion: sealed.Persona.Version, PersonaDigest: digestJSON(sealed.Persona),
		CapabilityDigest: digestJSON(sealed.Capabilities), ContextDigest: digestJSON(packet),
		ProviderRequestHash: decision.ProviderRequestDigest,
		SkillRefs:           skills, MemoryRefs: memories, DecisionKind: decision.Kind,
		DecisionSummary: decision.Summary, ProviderModel: decision.ProviderModel,
		LatencyMillis: latency, Usage: cloneProviderUsage(decision.Usage),
	}
	if decision.Kind == ModelDecisionAction {
		capability := decision.Capability
		record.SelectedCapability = &capability
	}
	return sealDecisionRecord(record)
}

func sealDecisionRecord(record DecisionRecord) (DecisionRecord, error) {
	for field, value := range map[string]string{
		"record_id": record.RecordID, "task_id": record.TaskID,
		"session_id": record.SessionID, "actor_id": record.ActorID,
		"controller_id": record.ControllerID, "observation_id": record.ObservationID,
		"persona_id": record.PersonaID, "persona_version": record.PersonaVersion,
	} {
		if err := validateProviderText(field, value, 192, true); err != nil {
			return DecisionRecord{}, err
		}
	}
	if record.InspectionRound > 1 || record.OccurredAtUnixMilli < 0 ||
		record.ObservationSequence == 0 {
		return DecisionRecord{}, errors.New("decision record sequence or time is invalid")
	}
	if err := record.Epoch.Validate("epoch"); err != nil {
		return DecisionRecord{}, err
	}
	for field, value := range map[string]string{
		"goal_digest": record.GoalDigest, "persona_digest": record.PersonaDigest,
		"capability_digest": record.CapabilityDigest, "context_digest": record.ContextDigest,
	} {
		if !validDecisionDigest(value) {
			return DecisionRecord{}, fmt.Errorf("%s is invalid", field)
		}
	}
	if record.ProviderRequestHash != "" && !validDecisionDigest(record.ProviderRequestHash) {
		return DecisionRecord{}, errors.New("provider_request_digest is invalid")
	}
	if err := validateProviderText("decision_summary", record.DecisionSummary, 500, true); err != nil {
		return DecisionRecord{}, err
	}
	if record.DecisionKind != ModelDecisionAction && record.DecisionKind != ModelDecisionWait &&
		record.DecisionKind != ModelDecisionComplete && record.DecisionKind != ModelDecisionInspect {
		return DecisionRecord{}, errors.New("decision kind is invalid")
	}
	if record.SelectedCapability != nil {
		if record.DecisionKind != ModelDecisionAction {
			return DecisionRecord{}, errors.New("only action decisions may select a capability")
		}
		if err := record.SelectedCapability.Validate("selected_capability"); err != nil {
			return DecisionRecord{}, err
		}
	}
	if record.Usage.PromptTokens < 0 || record.Usage.CompletionTokens < 0 ||
		record.Usage.TotalTokens < 0 || negativeOptionalInt(record.Usage.PromptCacheHitTokens) ||
		negativeOptionalInt(record.Usage.PromptCacheMissTokens) ||
		negativeOptionalInt(record.Usage.CacheWriteTokens) {
		return DecisionRecord{}, errors.New("decision record token usage is invalid")
	}
	if len(record.SkillRefs) > 64 || len(record.MemoryRefs) > 64 {
		return DecisionRecord{}, errors.New("decision record context reference limit exceeded")
	}
	record.SkillRefs = append([]timeline.SkillContextRef(nil), record.SkillRefs...)
	record.MemoryRefs = append([]timeline.MemoryContextRef(nil), record.MemoryRefs...)
	slices.SortFunc(record.SkillRefs, func(left, right timeline.SkillContextRef) int {
		if left.SkillID != right.SkillID {
			return strings.Compare(left.SkillID, right.SkillID)
		}
		return strings.Compare(left.Version, right.Version)
	})
	record.Usage = cloneProviderUsage(record.Usage)
	if record.SelectedCapability != nil {
		capability := *record.SelectedCapability
		record.SelectedCapability = &capability
	}
	return record, nil
}

func cloneDecisionRecord(record DecisionRecord) DecisionRecord {
	record.SkillRefs = append([]timeline.SkillContextRef(nil), record.SkillRefs...)
	record.MemoryRefs = append([]timeline.MemoryContextRef(nil), record.MemoryRefs...)
	record.Usage = cloneProviderUsage(record.Usage)
	if record.SelectedCapability != nil {
		capability := *record.SelectedCapability
		record.SelectedCapability = &capability
	}
	return record
}

func cloneProviderUsage(usage provider.Usage) provider.Usage {
	usage.PromptCacheHitTokens = cloneIntPointer(usage.PromptCacheHitTokens)
	usage.PromptCacheMissTokens = cloneIntPointer(usage.PromptCacheMissTokens)
	usage.CacheWriteTokens = cloneIntPointer(usage.CacheWriteTokens)
	return usage
}

func cloneIntPointer(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func digestJSON(value any) string {
	payload, _ := json.Marshal(value)
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func validDecisionDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != 71 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}
