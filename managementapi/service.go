// Package managementapi exposes the small, local management surface used by
// Rin Console. It composes existing providers and does not own another store.
package managementapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/host"
)

const MemoryScopeCommon = "common"

type Service struct {
	personas cognition.PersonaStore
	memory   cognition.MemoryProvider
	tasks    TaskManager
	now      func() time.Time
	newID    func() (string, error)
}

type MemoryListRequest struct {
	Scope  string   `json:"scope,omitempty"`
	Search string   `json:"search,omitempty"`
	Tags   []string `json:"tags,omitempty"`
	Limit  uint32   `json:"limit,omitempty"`
}

// MemoryCard is the Console-safe projection of a common memory. Storage
// namespace, Canon, controller and recall metadata never cross this API.
type MemoryCard struct {
	MemoryID   string         `json:"memory_id"`
	Content    string         `json:"content"`
	Tags       []string       `json:"tags,omitempty"`
	Pinned     bool           `json:"pinned,omitempty"`
	Importance float64        `json:"importance"`
	CreatedAt  host.Timepoint `json:"created_at"`
	Source     string         `json:"source"`
}

type MemoryListResponse struct {
	Revision uint64       `json:"revision"`
	Records  []MemoryCard `json:"records"`
}

type MemoryCardInput struct {
	MemoryID   string   `json:"memory_id,omitempty"`
	Content    string   `json:"content"`
	Tags       []string `json:"tags,omitempty"`
	Pinned     bool     `json:"pinned,omitempty"`
	Importance float64  `json:"importance,omitempty"`
}

type MemoryForgetInput struct {
	MemoryID string `json:"memory_id"`
	Reason   string `json:"reason,omitempty"`
}

func New(
	personas cognition.PersonaStore,
	memory cognition.MemoryProvider,
	tasks ...TaskManager,
) (*Service, error) {
	if personas == nil || memory == nil {
		return nil, errors.New("persona and memory providers are required")
	}
	service := &Service{personas: personas, memory: memory, now: time.Now, newID: newMemoryID}
	if len(tasks) > 1 {
		return nil, errors.New("at most one task manager may be configured")
	}
	if len(tasks) == 1 {
		service.tasks = tasks[0]
	}
	return service, nil
}

func (service *Service) PersonaSnapshot(ctx context.Context) (cognition.PersonaSnapshot, error) {
	return service.personas.Snapshot(ctx)
}

func (service *Service) ReplacePersonas(
	ctx context.Context,
	snapshot cognition.PersonaSnapshot,
) (cognition.PersonaSnapshot, error) {
	return service.personas.CompareAndSwap(ctx, snapshot)
}

func (service *Service) ListMemories(
	ctx context.Context,
	request MemoryListRequest,
) (MemoryListResponse, error) {
	request.Scope = strings.TrimSpace(request.Scope)
	if request.Scope != "" && request.Scope != MemoryScopeCommon {
		return MemoryListResponse{}, errors.New("Console can manage only common memory cards")
	}
	request.Search = strings.ToLower(strings.TrimSpace(request.Search))
	if len(request.Search) > 500 || len(request.Tags) > 32 {
		return MemoryListResponse{}, errors.New("memory list filter exceeds its bounds")
	}
	if request.Limit == 0 {
		request.Limit = 100
	}
	if request.Limit > 500 {
		return MemoryListResponse{}, errors.New("memory list limit exceeds 500")
	}
	snapshot, err := service.memory.Snapshot(ctx)
	if err != nil {
		return MemoryListResponse{}, err
	}
	forgotten := make(map[string]struct{}, len(snapshot.Tombstones))
	for _, tombstone := range snapshot.Tombstones {
		if tombstone.Namespace == cognition.CommonMemoryNamespace() {
			forgotten[tombstone.MemoryID] = struct{}{}
		}
	}
	active := make([]cognition.MemoryRecord, 0)
	for _, record := range snapshot.Records {
		if record.Namespace == cognition.CommonMemoryNamespace() {
			if _, removed := forgotten[record.MemoryID]; !removed {
				active = append(active, record)
			}
		}
	}
	superseded := make(map[string]struct{})
	for _, record := range active {
		for _, memoryID := range record.Supersedes {
			superseded[memoryID] = struct{}{}
		}
	}
	records := make([]MemoryCard, 0, min(len(active), int(request.Limit)))
	for _, record := range active {
		if _, hidden := superseded[record.MemoryID]; hidden ||
			(request.Search != "" && !strings.Contains(strings.ToLower(record.Content), request.Search)) ||
			!containsAll(record.Tags, request.Tags) {
			continue
		}
		records = append(records, memoryCard(record))
	}
	slices.SortFunc(records, func(left, right MemoryCard) int {
		if left.CreatedAt.Value != right.CreatedAt.Value {
			if left.CreatedAt.Value > right.CreatedAt.Value {
				return -1
			}
			return 1
		}
		return strings.Compare(left.MemoryID, right.MemoryID)
	})
	if len(records) > int(request.Limit) {
		records = records[:request.Limit]
	}
	return MemoryListResponse{Revision: snapshot.Revision, Records: records}, nil
}

func (service *Service) SaveMemoryCard(
	ctx context.Context,
	input MemoryCardInput,
) (MemoryCard, error) {
	input.Content = strings.TrimSpace(input.Content)
	if input.Content == "" {
		return MemoryCard{}, errors.New("memory card content is required")
	}
	if input.Importance == 0 {
		input.Importance = 0.8
	}
	tags := append([]string(nil), input.Tags...)
	tags = append(tags, "memory-card")
	if input.Pinned {
		tags = append(tags, "pinned")
	}
	slices.Sort(tags)
	tags = slices.Compact(tags)
	now := host.Timepoint{Clock: host.ClockRealtime, Value: service.now().UnixMilli()}
	memoryID, err := service.newID()
	if err != nil {
		return MemoryCard{}, err
	}
	record := cognition.MemoryRecord{
		MemoryID: memoryID, Namespace: cognition.CommonMemoryNamespace(),
		Content: input.Content, Tags: tags,
		Provenance: cognition.MemoryProvenance{
			Source: cognition.MemorySourcePlayer, SourceID: "rin.console",
		},
		Confidence: 1, Importance: input.Importance, CreatedAt: now,
	}
	if input.MemoryID != "" {
		existing, findErr := service.findActiveCommonMemory(ctx, input.MemoryID)
		if findErr != nil {
			return MemoryCard{}, findErr
		}
		if existing.Provenance.Source != cognition.MemorySourcePlayer &&
			existing.Provenance.Source != cognition.MemorySourceSystem {
			return MemoryCard{}, errors.New("only player or system memory cards can be edited")
		}
		record.Supersedes = []string{existing.MemoryID}
	}
	created, err := service.memory.Append(ctx, record)
	if err != nil {
		return MemoryCard{}, err
	}
	return memoryCard(created), nil
}

func (service *Service) ForgetMemory(
	ctx context.Context,
	input MemoryForgetInput,
) error {
	input.MemoryID = strings.TrimSpace(input.MemoryID)
	if input.MemoryID == "" {
		return errors.New("memory_id is required")
	}
	snapshot, err := service.memory.Snapshot(ctx)
	if err != nil {
		return err
	}
	byID := make(map[string]cognition.MemoryRecord)
	for _, record := range snapshot.Records {
		if record.Namespace == cognition.CommonMemoryNamespace() {
			byID[record.MemoryID] = record
		}
	}
	if _, exists := byID[input.MemoryID]; !exists {
		return cognition.ErrProviderNotFound
	}
	lineage := make([]string, 0, 4)
	seen := make(map[string]struct{})
	var visit func(string)
	visit = func(memoryID string) {
		if _, exists := seen[memoryID]; exists {
			return
		}
		record, exists := byID[memoryID]
		if !exists {
			return
		}
		seen[memoryID] = struct{}{}
		lineage = append(lineage, memoryID)
		for _, previous := range record.Supersedes {
			visit(previous)
		}
	}
	visit(input.MemoryID)
	reason := strings.TrimSpace(input.Reason)
	if reason == "" {
		reason = "forgotten from Rin Console"
	}
	return service.memory.Forget(ctx, cognition.MemoryForgetRequest{
		Namespace: cognition.CommonMemoryNamespace(), MemoryIDs: lineage, Reason: reason,
		At: host.Timepoint{Clock: host.ClockRealtime, Value: service.now().UnixMilli()},
	})
}

func (service *Service) findActiveCommonMemory(
	ctx context.Context,
	memoryID string,
) (cognition.MemoryRecord, error) {
	snapshot, err := service.memory.Snapshot(ctx)
	if err != nil {
		return cognition.MemoryRecord{}, err
	}
	for _, tombstone := range snapshot.Tombstones {
		if tombstone.Namespace == cognition.CommonMemoryNamespace() && tombstone.MemoryID == memoryID {
			return cognition.MemoryRecord{}, cognition.ErrProviderNotFound
		}
	}
	for _, record := range snapshot.Records {
		if record.Namespace == cognition.CommonMemoryNamespace() && record.MemoryID == memoryID {
			return record, nil
		}
	}
	return cognition.MemoryRecord{}, cognition.ErrProviderNotFound
}

func memoryCard(record cognition.MemoryRecord) MemoryCard {
	return MemoryCard{
		MemoryID: record.MemoryID, Content: record.Content, Tags: record.Tags,
		Pinned: slices.Contains(record.Tags, "pinned"), Importance: record.Importance,
		CreatedAt: record.CreatedAt, Source: string(record.Provenance.Source),
	}
}

func containsAll(values, required []string) bool {
	for _, value := range required {
		if !slices.Contains(values, value) {
			return false
		}
	}
	return true
}

func newMemoryID() (string, error) {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", err
	}
	return "memory.card." + hex.EncodeToString(suffix[:]), nil
}
