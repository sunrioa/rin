package cognition

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/sunrioa/rin/host"
)

type MemoryDomain string

const (
	// MemoryCommonSemantic contains user-maintained context that is intentionally
	// shared by every actor and adapter connected to one Rin instance.
	MemoryCommonSemantic    MemoryDomain = "common-semantic"
	MemoryActorEpisodic     MemoryDomain = "actor-episodic"
	MemoryActorSemantic     MemoryDomain = "actor-semantic"
	MemoryControllerWorking MemoryDomain = "controller-working"
	MemoryControllerPrivate MemoryDomain = "controller-private"
	MemoryControllerBelief  MemoryDomain = "controller-belief"
)

const (
	commonMemorySessionID = "session.rin-common"
	commonMemoryActorID   = "actor.rin-common"
)

// CommonMemoryNamespace is the only namespace accepted for common semantic
// memories. Keeping the reserved identity behind this helper avoids callers
// accidentally turning a world or actor record into cross-adapter context.
func CommonMemoryNamespace() MemoryNamespace {
	return MemoryNamespace{
		SessionID: commonMemorySessionID,
		ActorID:   commonMemoryActorID,
		Domain:    MemoryCommonSemantic,
	}
}

type MemorySource string

const (
	MemorySourceHostOutcome MemorySource = "host-outcome"
	MemorySourcePlayer      MemorySource = "player"
	MemorySourceModel       MemorySource = "model"
	MemorySourceSystem      MemorySource = "system"
)

// MemoryNamespace makes visibility structural. Actor domains are shared by
// every controller of an actor; controller domains are visible only to the
// controller named here.
type MemoryNamespace struct {
	SessionID    string       `json:"session_id"`
	ActorID      string       `json:"actor_id"`
	ControllerID string       `json:"controller_id,omitempty"`
	Domain       MemoryDomain `json:"domain"`
}

type MemoryProvenance struct {
	Source        MemorySource `json:"source"`
	SourceID      string       `json:"source_id"`
	Authoritative bool         `json:"authoritative"`
}

type MemoryCanonStatus string

const (
	MemoryCanonCurrent    MemoryCanonStatus = "current"
	MemoryCanonConflicted MemoryCanonStatus = "conflicted"
)

// MemoryCanonRef points back to Host-owned truth without copying Canon into
// Rin. The digest identifies the projected fact; it does not authorize writes.
type MemoryCanonRef struct {
	HostID   string            `json:"host_id"`
	WorldID  string            `json:"world_id"`
	Epoch    host.Epoch        `json:"epoch"`
	Sequence uint64            `json:"sequence"`
	Digest   string            `json:"digest"`
	Status   MemoryCanonStatus `json:"status"`
}

type MemoryRecord struct {
	MemoryID       string           `json:"memory_id"`
	Namespace      MemoryNamespace  `json:"namespace"`
	Content        string           `json:"content"`
	SubjectRefs    []string         `json:"subject_refs,omitempty"`
	Tags           []string         `json:"tags,omitempty"`
	SourceEventIDs []string         `json:"source_event_ids,omitempty"`
	Provenance     MemoryProvenance `json:"provenance"`
	CanonRef       *MemoryCanonRef  `json:"canon_ref,omitempty"`
	Confidence     float64          `json:"confidence"`
	Importance     float64          `json:"importance"`
	CreatedAt      host.Timepoint   `json:"created_at"`
	LastRecalledAt *host.Timepoint  `json:"last_recalled_at,omitempty"`
	ExpiresAt      *host.Timepoint  `json:"expires_at,omitempty"`
	Supersedes     []string         `json:"supersedes,omitempty"`
	RecallCount    uint64           `json:"recall_count"`
}

type MemoryBudget struct {
	MaxRecords    uint32 `json:"max_records"`
	MaxCharacters uint32 `json:"max_characters"`
}

type MemoryQuery struct {
	SessionID    string         `json:"session_id"`
	ActorID      string         `json:"actor_id"`
	ControllerID string         `json:"controller_id,omitempty"`
	Terms        []string       `json:"terms,omitempty"`
	Tags         []string       `json:"tags,omitempty"`
	SubjectRefs  []string       `json:"subject_refs,omitempty"`
	Domains      []MemoryDomain `json:"domains,omitempty"`
	Now          host.Timepoint `json:"now"`
	Budget       MemoryBudget   `json:"budget"`
	Semantic     bool           `json:"semantic,omitempty"`
	SemanticText string         `json:"semantic_text,omitempty"`
}

type MemoryMatch struct {
	Record  MemoryRecord `json:"record"`
	Score   int          `json:"score"`
	Reasons []string     `json:"reasons"`
}

type MemoryRetrievalTrace struct {
	SemanticUsed        bool
	RemoteRequested     bool
	QueryCacheHit       bool
	RemoteLatencyMillis uint64
	DegradedCode        string
}

type TracedMemoryProvider interface {
	RetrieveWithTrace(context.Context, MemoryQuery) ([]MemoryMatch, MemoryRetrievalTrace, error)
}

type MemoryConsolidation struct {
	Namespace       MemoryNamespace `json:"namespace"`
	SourceMemoryIDs []string        `json:"source_memory_ids"`
	Summary         MemoryRecord    `json:"summary"`
	Reason          string          `json:"reason"`
}

type MemoryForgetRequest struct {
	Namespace MemoryNamespace `json:"namespace"`
	MemoryIDs []string        `json:"memory_ids"`
	Reason    string          `json:"reason"`
	At        host.Timepoint  `json:"at"`
}

type MemoryTombstone struct {
	MemoryID  string          `json:"memory_id"`
	Namespace MemoryNamespace `json:"namespace"`
	Reason    string          `json:"reason"`
	At        host.Timepoint  `json:"at"`
}

type MemorySnapshot struct {
	Revision   uint64            `json:"revision"`
	Records    []MemoryRecord    `json:"records"`
	Tombstones []MemoryTombstone `json:"tombstones,omitempty"`
}

type MemoryProvider interface {
	Append(context.Context, MemoryRecord) (MemoryRecord, error)
	Retrieve(context.Context, MemoryQuery) ([]MemoryMatch, error)
	Consolidate(context.Context, MemoryConsolidation) (MemoryRecord, error)
	Forget(context.Context, MemoryForgetRequest) error
	Snapshot(context.Context) (MemorySnapshot, error)
	Health(context.Context) ProviderHealth
}

type LocalMemoryConfig struct {
	MaxActiveRecordsPerNamespace uint32
	MaxHistoryPerNamespace       uint32
}

type localMemoryNamespace struct {
	records    map[string]MemoryRecord
	tombstones map[string]MemoryTombstone
}

type LocalMemoryProvider struct {
	mu         sync.RWMutex
	revision   uint64
	config     LocalMemoryConfig
	namespaces map[string]*localMemoryNamespace
}

func NewLocalMemoryProvider(config LocalMemoryConfig) (*LocalMemoryProvider, error) {
	config, err := normalizeLocalMemoryConfig(config)
	if err != nil {
		return nil, err
	}
	return &LocalMemoryProvider{
		revision:   1,
		config:     config,
		namespaces: make(map[string]*localMemoryNamespace),
	}, nil
}

func RestoreLocalMemoryProvider(
	config LocalMemoryConfig,
	snapshot MemorySnapshot,
) (*LocalMemoryProvider, error) {
	provider, err := NewLocalMemoryProvider(config)
	if err != nil {
		return nil, err
	}
	if snapshot.Revision == 0 {
		return nil, errors.New("memory snapshot revision must be positive")
	}
	for index, record := range snapshot.Records {
		sealed, err := sealMemoryRecord(record)
		if err != nil {
			return nil, fmt.Errorf("records[%d]: %w", index, err)
		}
		state := provider.ensureNamespace(sealed.Namespace)
		if len(state.records) >= int(provider.config.MaxHistoryPerNamespace) {
			return nil, fmt.Errorf("records[%d]: %w", index, ErrProviderCapacity)
		}
		if _, exists := state.records[sealed.MemoryID]; exists {
			return nil, fmt.Errorf("records[%d]: %w", index, ErrProviderConflict)
		}
		state.records[sealed.MemoryID] = sealed
	}
	for index, tombstone := range snapshot.Tombstones {
		sealed, err := sealMemoryTombstone(tombstone)
		if err != nil {
			return nil, fmt.Errorf("tombstones[%d]: %w", index, err)
		}
		state := provider.namespaces[memoryNamespaceKey(sealed.Namespace)]
		if state == nil {
			return nil, fmt.Errorf("tombstones[%d]: %w", index, ErrProviderNotFound)
		}
		record, exists := state.records[sealed.MemoryID]
		if !exists || record.Namespace != sealed.Namespace {
			return nil, fmt.Errorf("tombstones[%d]: %w", index, ErrProviderNotFound)
		}
		if _, exists := state.tombstones[sealed.MemoryID]; exists {
			return nil, fmt.Errorf("tombstones[%d]: %w", index, ErrProviderConflict)
		}
		state.tombstones[sealed.MemoryID] = sealed
	}
	for _, state := range provider.namespaces {
		if activeMemoryCount(state) > int(provider.config.MaxActiveRecordsPerNamespace) {
			return nil, ErrProviderCapacity
		}
	}
	provider.revision = snapshot.Revision
	return provider, nil
}

func (provider *LocalMemoryProvider) Append(
	ctx context.Context,
	record MemoryRecord,
) (MemoryRecord, error) {
	if err := requireMemoryContext(ctx); err != nil {
		return MemoryRecord{}, err
	}
	sealed, err := sealMemoryRecord(record)
	if err != nil {
		return MemoryRecord{}, err
	}
	if sealed.LastRecalledAt != nil || sealed.RecallCount != 0 {
		return MemoryRecord{}, errors.New("append cannot set provider-owned recall metadata")
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	state := provider.ensureNamespace(sealed.Namespace)
	if existing, exists := state.records[sealed.MemoryID]; exists {
		if memoryRecordsEqual(existing, sealed) {
			return cloneMemoryRecord(existing), nil
		}
		return MemoryRecord{}, ErrProviderConflict
	}
	if activeMemoryCount(state) >= int(provider.config.MaxActiveRecordsPerNamespace) ||
		len(state.records) >= int(provider.config.MaxHistoryPerNamespace) {
		return MemoryRecord{}, ErrProviderCapacity
	}
	state.records[sealed.MemoryID] = sealed
	provider.revision++
	return cloneMemoryRecord(sealed), nil
}

func (provider *LocalMemoryProvider) Retrieve(
	ctx context.Context,
	query MemoryQuery,
) ([]MemoryMatch, error) {
	if err := requireMemoryContext(ctx); err != nil {
		return nil, err
	}
	sealed, err := sealMemoryQuery(query)
	if err != nil {
		return nil, err
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	candidates := make([]MemoryMatch, 0)
	for _, namespace := range memoryQueryNamespaces(sealed) {
		state := provider.namespaces[memoryNamespaceKey(namespace)]
		if state == nil {
			continue
		}
		for memoryID, record := range state.records {
			if _, forgotten := state.tombstones[memoryID]; forgotten ||
				!memoryVisibleToQuery(record, sealed) || memoryExpired(record, sealed.Now) {
				continue
			}
			score, reasons := scoreMemory(record, sealed)
			candidates = append(candidates, MemoryMatch{
				Record: cloneMemoryRecord(record), Score: score, Reasons: reasons,
			})
		}
	}
	candidates = removeSupersededMemoryMatches(candidates)
	slices.SortFunc(candidates, compareMemoryMatches)
	selected := make([]MemoryMatch, 0, min(len(candidates), int(sealed.Budget.MaxRecords)))
	usedCharacters := 0
	for _, match := range candidates {
		characters := utf8.RuneCountInString(match.Record.Content)
		if usedCharacters+characters > int(sealed.Budget.MaxCharacters) {
			continue
		}
		state := provider.namespaces[memoryNamespaceKey(match.Record.Namespace)]
		record := state.records[match.Record.MemoryID]
		if record.RecallCount < maxProviderWireInteger {
			record.RecallCount++
		}
		now := sealed.Now
		record.LastRecalledAt = &now
		state.records[record.MemoryID] = record
		match.Record = cloneMemoryRecord(record)
		selected = append(selected, match)
		usedCharacters += characters
		if len(selected) == int(sealed.Budget.MaxRecords) {
			break
		}
	}
	if len(selected) != 0 {
		provider.revision++
	}
	return selected, nil
}

func removeSupersededMemoryMatches(matches []MemoryMatch) []MemoryMatch {
	superseded := make(map[string]struct{})
	for _, match := range matches {
		for _, memoryID := range match.Record.Supersedes {
			superseded[memoryNamespaceKey(match.Record.Namespace)+"\x00"+memoryID] = struct{}{}
		}
	}
	return slices.DeleteFunc(matches, func(match MemoryMatch) bool {
		_, hidden := superseded[memoryRecordKey(match.Record)]
		return hidden
	})
}

func (provider *LocalMemoryProvider) Consolidate(
	ctx context.Context,
	request MemoryConsolidation,
) (MemoryRecord, error) {
	if err := requireMemoryContext(ctx); err != nil {
		return MemoryRecord{}, err
	}
	if err := validateMemoryNamespace(request.Namespace); err != nil {
		return MemoryRecord{}, err
	}
	sourceIDs, err := normalizeMemoryIDs("source_memory_ids", request.SourceMemoryIDs, 64)
	if err != nil {
		return MemoryRecord{}, err
	}
	if len(sourceIDs) < 2 {
		return MemoryRecord{}, errors.New("memory consolidation requires at least two source records")
	}
	if err := validateProviderText("reason", request.Reason, 500, true); err != nil {
		return MemoryRecord{}, err
	}
	request.Summary.Namespace = request.Namespace
	request.Summary.Supersedes = append([]string(nil), sourceIDs...)
	summary, err := sealMemoryRecord(request.Summary)
	if err != nil {
		return MemoryRecord{}, err
	}
	if summary.LastRecalledAt != nil || summary.RecallCount != 0 {
		return MemoryRecord{}, errors.New("consolidation cannot set provider-owned recall metadata")
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	state := provider.namespaces[memoryNamespaceKey(request.Namespace)]
	if state == nil {
		return MemoryRecord{}, ErrProviderNotFound
	}
	if existing, exists := state.records[summary.MemoryID]; exists {
		if memoryRecordsEqual(existing, summary) {
			return cloneMemoryRecord(existing), nil
		}
		return MemoryRecord{}, ErrProviderConflict
	}
	for _, memoryID := range sourceIDs {
		if _, exists := state.records[memoryID]; !exists {
			return MemoryRecord{}, fmt.Errorf("source memory %q: %w", memoryID, ErrProviderNotFound)
		}
		if _, forgotten := state.tombstones[memoryID]; forgotten {
			return MemoryRecord{}, fmt.Errorf("source memory %q is forgotten", memoryID)
		}
	}
	if len(state.records) >= int(provider.config.MaxHistoryPerNamespace) {
		return MemoryRecord{}, ErrProviderCapacity
	}
	state.records[summary.MemoryID] = summary
	for _, memoryID := range sourceIDs {
		state.tombstones[memoryID] = MemoryTombstone{
			MemoryID: memoryID, Namespace: request.Namespace,
			Reason: request.Reason, At: summary.CreatedAt,
		}
	}
	provider.revision++
	return cloneMemoryRecord(summary), nil
}

func (provider *LocalMemoryProvider) Forget(
	ctx context.Context,
	request MemoryForgetRequest,
) error {
	if err := requireMemoryContext(ctx); err != nil {
		return err
	}
	if err := validateMemoryNamespace(request.Namespace); err != nil {
		return err
	}
	memoryIDs, err := normalizeMemoryIDs("memory_ids", request.MemoryIDs, 128)
	if err != nil {
		return err
	}
	if len(memoryIDs) == 0 {
		return errors.New("memory_ids is required")
	}
	if err := validateProviderText("reason", request.Reason, 500, true); err != nil {
		return err
	}
	if err := validateMemoryTimepoint("at", request.At); err != nil {
		return err
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	state := provider.namespaces[memoryNamespaceKey(request.Namespace)]
	if state == nil {
		return ErrProviderNotFound
	}
	for _, memoryID := range memoryIDs {
		if _, exists := state.records[memoryID]; !exists {
			return fmt.Errorf("memory %q: %w", memoryID, ErrProviderNotFound)
		}
	}
	changed := false
	for _, memoryID := range memoryIDs {
		if _, exists := state.tombstones[memoryID]; exists {
			continue
		}
		state.tombstones[memoryID] = MemoryTombstone{
			MemoryID: memoryID, Namespace: request.Namespace,
			Reason: request.Reason, At: request.At,
		}
		changed = true
	}
	if changed {
		provider.revision++
	}
	return nil
}

func (provider *LocalMemoryProvider) Snapshot(
	ctx context.Context,
) (MemorySnapshot, error) {
	if err := requireMemoryContext(ctx); err != nil {
		return MemorySnapshot{}, err
	}
	provider.mu.RLock()
	defer provider.mu.RUnlock()
	snapshot := MemorySnapshot{Revision: provider.revision}
	for _, state := range provider.namespaces {
		for _, record := range state.records {
			snapshot.Records = append(snapshot.Records, cloneMemoryRecord(record))
		}
		for _, tombstone := range state.tombstones {
			snapshot.Tombstones = append(snapshot.Tombstones, tombstone)
		}
	}
	slices.SortFunc(snapshot.Records, func(left, right MemoryRecord) int {
		return compareString(memoryRecordKey(left), memoryRecordKey(right))
	})
	slices.SortFunc(snapshot.Tombstones, func(left, right MemoryTombstone) int {
		return compareString(memoryTombstoneKey(left), memoryTombstoneKey(right))
	})
	return snapshot, nil
}

func (provider *LocalMemoryProvider) Health(ctx context.Context) ProviderHealth {
	if ctx == nil || ctx.Err() != nil {
		return ProviderHealth{Code: "context_unavailable"}
	}
	return ProviderHealth{Available: true}
}

func (provider *LocalMemoryProvider) revisionValue() uint64 {
	provider.mu.RLock()
	defer provider.mu.RUnlock()
	return provider.revision
}

func (provider *LocalMemoryProvider) ensureNamespace(namespace MemoryNamespace) *localMemoryNamespace {
	key := memoryNamespaceKey(namespace)
	state := provider.namespaces[key]
	if state == nil {
		state = &localMemoryNamespace{
			records: make(map[string]MemoryRecord), tombstones: make(map[string]MemoryTombstone),
		}
		provider.namespaces[key] = state
	}
	return state
}

func normalizeLocalMemoryConfig(config LocalMemoryConfig) (LocalMemoryConfig, error) {
	if config.MaxActiveRecordsPerNamespace == 0 {
		config.MaxActiveRecordsPerNamespace = 2_048
	}
	if config.MaxHistoryPerNamespace == 0 {
		config.MaxHistoryPerNamespace = 4_096
	}
	if config.MaxActiveRecordsPerNamespace > 100_000 || config.MaxHistoryPerNamespace > 200_000 {
		return LocalMemoryConfig{}, errors.New("memory provider limits are too large")
	}
	if config.MaxHistoryPerNamespace < config.MaxActiveRecordsPerNamespace {
		return LocalMemoryConfig{}, errors.New("memory history limit must cover active records")
	}
	return config, nil
}

func sealMemoryRecord(record MemoryRecord) (MemoryRecord, error) {
	if err := validateMemoryOpaqueID("memory_id", record.MemoryID); err != nil {
		return MemoryRecord{}, err
	}
	if err := validateMemoryNamespace(record.Namespace); err != nil {
		return MemoryRecord{}, err
	}
	if err := validateProviderText("content", record.Content, 8_000, true); err != nil {
		return MemoryRecord{}, err
	}
	var err error
	if record.SubjectRefs, err = normalizeMemoryTexts("subject_refs", record.SubjectRefs, 32, 256); err != nil {
		return MemoryRecord{}, err
	}
	if record.Tags, err = normalizeProviderIDs("tags", record.Tags, 64); err != nil {
		return MemoryRecord{}, err
	}
	if record.SourceEventIDs, err = normalizeMemoryIDs("source_event_ids", record.SourceEventIDs, 64); err != nil {
		return MemoryRecord{}, err
	}
	if record.Supersedes, err = normalizeMemoryIDs("supersedes", record.Supersedes, 64); err != nil {
		return MemoryRecord{}, err
	}
	if slices.Contains(record.Supersedes, record.MemoryID) {
		return MemoryRecord{}, errors.New("memory cannot supersede itself")
	}
	if err := validateMemoryProvenance(record.Provenance, record.Namespace); err != nil {
		return MemoryRecord{}, err
	}
	if record.CanonRef != nil {
		canon := *record.CanonRef
		if err := validateMemoryCanonRef(canon, record.Provenance); err != nil {
			return MemoryRecord{}, err
		}
		record.CanonRef = &canon
	}
	if record.Confidence < 0 || record.Confidence > 1 || record.Importance < 0 || record.Importance > 1 {
		return MemoryRecord{}, errors.New("memory confidence and importance must be between zero and one")
	}
	if record.RecallCount > maxProviderWireInteger {
		return MemoryRecord{}, errors.New("memory recall count exceeds the exact JSON integer range")
	}
	if err := validateMemoryTimepoint("created_at", record.CreatedAt); err != nil {
		return MemoryRecord{}, err
	}
	if record.LastRecalledAt != nil {
		if err := validateMemoryTimepoint("last_recalled_at", *record.LastRecalledAt); err != nil {
			return MemoryRecord{}, err
		}
		if record.LastRecalledAt.Clock != record.CreatedAt.Clock {
			return MemoryRecord{}, errors.New("last_recalled_at must use the creation clock")
		}
	}
	if record.ExpiresAt != nil {
		if err := validateMemoryTimepoint("expires_at", *record.ExpiresAt); err != nil {
			return MemoryRecord{}, err
		}
		if record.ExpiresAt.Clock != record.CreatedAt.Clock || record.ExpiresAt.Value <= record.CreatedAt.Value {
			return MemoryRecord{}, errors.New("expires_at must be later on the creation clock")
		}
	}
	return cloneMemoryRecord(record), nil
}

func sealMemoryQuery(query MemoryQuery) (MemoryQuery, error) {
	if err := validateMemoryOpaqueID("session_id", query.SessionID); err != nil {
		return MemoryQuery{}, err
	}
	if err := validateMemoryOpaqueID("actor_id", query.ActorID); err != nil {
		return MemoryQuery{}, err
	}
	if query.ControllerID != "" {
		if err := validateMemoryOpaqueID("controller_id", query.ControllerID); err != nil {
			return MemoryQuery{}, err
		}
	}
	var err error
	if query.Terms, err = normalizeMemoryTerms(query.Terms); err != nil {
		return MemoryQuery{}, err
	}
	if query.Tags, err = normalizeProviderIDs("tags", query.Tags, 32); err != nil {
		return MemoryQuery{}, err
	}
	if query.SubjectRefs, err = normalizeMemoryTexts("subject_refs", query.SubjectRefs, 32, 256); err != nil {
		return MemoryQuery{}, err
	}
	query.SemanticText = strings.TrimSpace(query.SemanticText)
	if utf8.RuneCountInString(query.SemanticText) > 2_000 || strings.ContainsRune(query.SemanticText, 0) {
		return MemoryQuery{}, errors.New("semantic_text exceeds its bounds")
	}
	if query.Semantic && query.SemanticText == "" {
		return MemoryQuery{}, errors.New("semantic retrieval requires semantic_text")
	}
	if len(query.Domains) > 8 {
		return MemoryQuery{}, errors.New("domains must contain at most 8 values")
	}
	query.Domains = append([]MemoryDomain(nil), query.Domains...)
	slices.Sort(query.Domains)
	for index, domain := range query.Domains {
		if !validMemoryDomain(domain) {
			return MemoryQuery{}, fmt.Errorf("domains[%d] is invalid", index)
		}
		if index > 0 && query.Domains[index-1] == domain {
			return MemoryQuery{}, errors.New("domains must not contain duplicates")
		}
		if privateMemoryDomain(domain) && query.ControllerID == "" {
			return MemoryQuery{}, errors.New("private memory domains require controller_id")
		}
	}
	if err := validateMemoryTimepoint("now", query.Now); err != nil {
		return MemoryQuery{}, err
	}
	if query.Budget.MaxRecords == 0 {
		query.Budget.MaxRecords = 16
	}
	if query.Budget.MaxCharacters == 0 {
		query.Budget.MaxCharacters = 6_000
	}
	if query.Budget.MaxRecords > 128 || query.Budget.MaxCharacters > 64_000 {
		return MemoryQuery{}, errors.New("memory query budget exceeds its bounds")
	}
	return query, nil
}

func validateMemoryNamespace(namespace MemoryNamespace) error {
	if namespace.Domain == MemoryCommonSemantic {
		if namespace != CommonMemoryNamespace() {
			return errors.New("common semantic memory must use the reserved common namespace")
		}
		return nil
	}
	if err := validateMemoryOpaqueID("session_id", namespace.SessionID); err != nil {
		return err
	}
	if err := validateMemoryOpaqueID("actor_id", namespace.ActorID); err != nil {
		return err
	}
	if !validMemoryDomain(namespace.Domain) {
		return errors.New("memory domain is invalid")
	}
	if privateMemoryDomain(namespace.Domain) {
		if err := validateMemoryOpaqueID("controller_id", namespace.ControllerID); err != nil {
			return err
		}
	} else if namespace.ControllerID != "" {
		return errors.New("actor-shared memory must not name a controller")
	}
	return nil
}

func validateMemoryProvenance(provenance MemoryProvenance, namespace MemoryNamespace) error {
	switch provenance.Source {
	case MemorySourceHostOutcome, MemorySourcePlayer, MemorySourceModel, MemorySourceSystem:
	default:
		return errors.New("memory provenance source is invalid")
	}
	if err := validateMemoryOpaqueID("provenance.source_id", provenance.SourceID); err != nil {
		return err
	}
	if provenance.Authoritative && provenance.Source != MemorySourceHostOutcome {
		return errors.New("only a Host Outcome may be authoritative")
	}
	if provenance.Source == MemorySourceHostOutcome && !provenance.Authoritative {
		return errors.New("Host Outcome memory must retain authoritative provenance")
	}
	if provenance.Source == MemorySourceModel && !privateMemoryDomain(namespace.Domain) {
		return errors.New("model-generated memory must remain controller-private")
	}
	if namespace.Domain == MemoryCommonSemantic {
		if provenance.Authoritative ||
			(provenance.Source != MemorySourcePlayer && provenance.Source != MemorySourceSystem) {
			return errors.New("common semantic memory must be non-authoritative player or system context")
		}
	}
	return nil
}

func validateMemoryCanonRef(ref MemoryCanonRef, provenance MemoryProvenance) error {
	if provenance.Source != MemorySourceHostOutcome || !provenance.Authoritative {
		return errors.New("canon_ref requires authoritative Host provenance")
	}
	if err := validateMemoryOpaqueID("canon_ref.host_id", ref.HostID); err != nil {
		return err
	}
	if err := validateMemoryOpaqueID("canon_ref.world_id", ref.WorldID); err != nil {
		return err
	}
	if err := ref.Epoch.Validate("canon_ref.epoch"); err != nil {
		return err
	}
	if ref.Sequence == 0 || ref.Sequence > maxProviderWireInteger {
		return errors.New("canon_ref sequence is invalid")
	}
	if !providerDigestPattern.MatchString(ref.Digest) {
		return errors.New("canon_ref digest is invalid")
	}
	if ref.Status != MemoryCanonCurrent && ref.Status != MemoryCanonConflicted {
		return errors.New("canon_ref status is invalid")
	}
	return nil
}

func validateMemoryTimepoint(field string, point host.Timepoint) error {
	return point.Validate(field)
}

func sealMemoryTombstone(tombstone MemoryTombstone) (MemoryTombstone, error) {
	if err := validateMemoryOpaqueID("memory_id", tombstone.MemoryID); err != nil {
		return MemoryTombstone{}, err
	}
	if err := validateMemoryNamespace(tombstone.Namespace); err != nil {
		return MemoryTombstone{}, err
	}
	if err := validateProviderText("reason", tombstone.Reason, 500, true); err != nil {
		return MemoryTombstone{}, err
	}
	if err := validateMemoryTimepoint("at", tombstone.At); err != nil {
		return MemoryTombstone{}, err
	}
	return tombstone, nil
}

func memoryVisibleToQuery(record MemoryRecord, query MemoryQuery) bool {
	if record.Namespace.Domain == MemoryCommonSemantic {
		return record.Namespace == CommonMemoryNamespace() &&
			(len(query.Domains) == 0 || slices.Contains(query.Domains, MemoryCommonSemantic))
	}
	if record.Namespace.SessionID != query.SessionID || record.Namespace.ActorID != query.ActorID {
		return false
	}
	if privateMemoryDomain(record.Namespace.Domain) && record.Namespace.ControllerID != query.ControllerID {
		return false
	}
	return len(query.Domains) == 0 || slices.Contains(query.Domains, record.Namespace.Domain)
}

func memoryQueryNamespaces(query MemoryQuery) []MemoryNamespace {
	domains := query.Domains
	if len(domains) == 0 {
		domains = []MemoryDomain{
			MemoryCommonSemantic, MemoryActorEpisodic, MemoryActorSemantic,
		}
		if query.ControllerID != "" {
			domains = append(domains,
				MemoryControllerWorking, MemoryControllerPrivate, MemoryControllerBelief,
			)
		}
	}
	result := make([]MemoryNamespace, 0, len(domains))
	for _, domain := range domains {
		if domain == MemoryCommonSemantic {
			result = append(result, CommonMemoryNamespace())
			continue
		}
		namespace := MemoryNamespace{
			SessionID: query.SessionID, ActorID: query.ActorID, Domain: domain,
		}
		if privateMemoryDomain(domain) {
			namespace.ControllerID = query.ControllerID
		}
		result = append(result, namespace)
	}
	return result
}

func memoryExpired(record MemoryRecord, now host.Timepoint) bool {
	return record.ExpiresAt != nil && record.ExpiresAt.Clock == now.Clock && record.ExpiresAt.Value <= now.Value
}

func scoreMemory(record MemoryRecord, query MemoryQuery) (int, []string) {
	score := int(record.Importance*100) + int(record.Confidence*40)
	reasons := []string{"importance", "confidence"}
	for _, tag := range query.Tags {
		if slices.Contains(record.Tags, tag) {
			score += 40
			reasons = appendUniqueString(reasons, "tag")
		}
	}
	for _, subject := range query.SubjectRefs {
		if slices.Contains(record.SubjectRefs, subject) {
			score += 50
			reasons = appendUniqueString(reasons, "subject")
		}
	}
	lowerContent := strings.ToLower(record.Content)
	for _, term := range query.Terms {
		if strings.Contains(lowerContent, term) {
			score += 30
			reasons = appendUniqueString(reasons, "term")
		}
	}
	if record.CreatedAt.Clock == query.Now.Clock && record.CreatedAt.Value <= query.Now.Value {
		delta := query.Now.Value - record.CreatedAt.Value
		score += max(0, 20-int(min(delta, 20)))
		reasons = appendUniqueString(reasons, "recency")
	}
	return score, reasons
}

func compareMemoryMatches(left, right MemoryMatch) int {
	if left.Score != right.Score {
		return right.Score - left.Score
	}
	if left.Record.Importance != right.Record.Importance {
		if left.Record.Importance > right.Record.Importance {
			return -1
		}
		return 1
	}
	if left.Record.CreatedAt.Clock == right.Record.CreatedAt.Clock &&
		left.Record.CreatedAt.Value != right.Record.CreatedAt.Value {
		if left.Record.CreatedAt.Value > right.Record.CreatedAt.Value {
			return -1
		}
		return 1
	}
	return compareString(memoryRecordKey(left.Record), memoryRecordKey(right.Record))
}

func normalizeMemoryTerms(values []string) ([]string, error) {
	if len(values) > 32 {
		return nil, errors.New("terms must contain at most 32 values")
	}
	result := make([]string, 0, len(values))
	for index, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if err := validateProviderText(fmt.Sprintf("terms[%d]", index), value, 100, true); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	slices.Sort(result)
	compacted := slices.Compact(result)
	if len(compacted) != len(values) {
		return nil, errors.New("terms must not contain duplicates")
	}
	return compacted, nil
}

func normalizeMemoryIDs(field string, values []string, maximum int) ([]string, error) {
	if len(values) > maximum {
		return nil, fmt.Errorf("%s must contain at most %d values", field, maximum)
	}
	result := append([]string(nil), values...)
	slices.Sort(result)
	for index, value := range result {
		if err := validateMemoryOpaqueID(fmt.Sprintf("%s[%d]", field, index), value); err != nil {
			return nil, err
		}
		if index > 0 && result[index-1] == value {
			return nil, fmt.Errorf("%s must not contain duplicates", field)
		}
	}
	return result, nil
}

func normalizeMemoryTexts(field string, values []string, maximum, maximumCharacters int) ([]string, error) {
	if len(values) > maximum {
		return nil, fmt.Errorf("%s must contain at most %d values", field, maximum)
	}
	result := append([]string(nil), values...)
	slices.Sort(result)
	for index, value := range result {
		if err := validateProviderText(
			fmt.Sprintf("%s[%d]", field, index), value, maximumCharacters, true,
		); err != nil {
			return nil, err
		}
		if index > 0 && result[index-1] == value {
			return nil, fmt.Errorf("%s must not contain duplicates", field)
		}
	}
	return result, nil
}

func validateMemoryOpaqueID(field, value string) error {
	if strings.TrimSpace(value) == "" || len(value) > 256 || !utf8.ValidString(value) ||
		strings.ContainsRune(value, 0) {
		return fmt.Errorf("%s must be a non-empty UTF-8 identifier of at most 256 bytes", field)
	}
	return nil
}

func validMemoryDomain(domain MemoryDomain) bool {
	switch domain {
	case MemoryCommonSemantic, MemoryActorEpisodic, MemoryActorSemantic, MemoryControllerWorking,
		MemoryControllerPrivate, MemoryControllerBelief:
		return true
	default:
		return false
	}
}

func privateMemoryDomain(domain MemoryDomain) bool {
	switch domain {
	case MemoryControllerWorking, MemoryControllerPrivate, MemoryControllerBelief:
		return true
	default:
		return false
	}
}

func activeMemoryCount(state *localMemoryNamespace) int {
	return len(state.records) - len(state.tombstones)
}

func memoryNamespaceKey(namespace MemoryNamespace) string {
	return namespace.SessionID + "\x00" + namespace.ActorID + "\x00" +
		namespace.ControllerID + "\x00" + string(namespace.Domain)
}

func memoryRecordKey(record MemoryRecord) string {
	return memoryNamespaceKey(record.Namespace) + "\x00" + record.MemoryID
}

func memoryTombstoneKey(tombstone MemoryTombstone) string {
	return memoryNamespaceKey(tombstone.Namespace) + "\x00" + tombstone.MemoryID
}

func cloneMemoryRecord(record MemoryRecord) MemoryRecord {
	if record.CanonRef != nil {
		canon := *record.CanonRef
		record.CanonRef = &canon
	}
	record.SubjectRefs = append([]string(nil), record.SubjectRefs...)
	record.Tags = append([]string(nil), record.Tags...)
	record.SourceEventIDs = append([]string(nil), record.SourceEventIDs...)
	record.Supersedes = append([]string(nil), record.Supersedes...)
	if record.LastRecalledAt != nil {
		value := *record.LastRecalledAt
		record.LastRecalledAt = &value
	}
	if record.ExpiresAt != nil {
		value := *record.ExpiresAt
		record.ExpiresAt = &value
	}
	return record
}

func memoryRecordsEqual(left, right MemoryRecord) bool {
	return memoryRecordKey(left) == memoryRecordKey(right) &&
		left.Content == right.Content &&
		left.Namespace == right.Namespace &&
		slices.Equal(left.SubjectRefs, right.SubjectRefs) &&
		slices.Equal(left.Tags, right.Tags) &&
		slices.Equal(left.SourceEventIDs, right.SourceEventIDs) &&
		left.Provenance == right.Provenance &&
		equalMemoryCanonRef(left.CanonRef, right.CanonRef) &&
		left.Confidence == right.Confidence && left.Importance == right.Importance &&
		left.CreatedAt == right.CreatedAt && equalMemoryTimepoint(left.ExpiresAt, right.ExpiresAt) &&
		slices.Equal(left.Supersedes, right.Supersedes)
}

func equalMemoryCanonRef(left, right *MemoryCanonRef) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func equalMemoryTimepoint(left, right *host.Timepoint) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func appendUniqueString(values []string, value string) []string {
	if slices.Contains(values, value) {
		return values
	}
	return append(values, value)
}

func requireMemoryContext(ctx context.Context) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	return ctx.Err()
}
