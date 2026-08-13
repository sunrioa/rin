package cognition

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sunrioa/rin/provider"
)

const recallSourceSemantic = "semantic"

type SemanticMemoryConfig struct {
	Model              string
	AllowedDomains     []MemoryDomain
	MaxInputCharacters uint32
	MinLocalMatches    uint32
	MaxSemanticResults uint32
	MaxCandidates      uint32
	QueueCapacity      uint32
	TimeoutMillis      uint32
	MinimumSimilarity  float64
}

type SemanticMemoryProvider struct {
	local    *SQLiteMemoryProvider
	embedder provider.EmbeddingProvider
	config   SemanticMemoryConfig

	ctx    context.Context
	cancel context.CancelFunc
	queue  chan MemoryRecord
	wg     sync.WaitGroup
	close  sync.Once

	remoteMu           sync.Mutex
	remoteSlots        chan struct{}
	dimensions         int
	consecutiveFailure int
	openUntil          time.Time
	cacheSequence      uint64
	queryCache         map[string]semanticCacheEntry
}

type semanticCacheEntry struct {
	vector   []float32
	sequence uint64
}

type semanticCandidate struct {
	memoryKey     string
	contentDigest string
	similarity    float64
}

func NewSemanticMemoryProvider(
	local *SQLiteMemoryProvider,
	embedder provider.EmbeddingProvider,
	config SemanticMemoryConfig,
) (*SemanticMemoryProvider, error) {
	if local == nil || embedder == nil {
		return nil, errors.New("semantic memory requires SQLite memory and an embedding provider")
	}
	sealed, err := normalizeSemanticMemoryConfig(config)
	if err != nil {
		return nil, err
	}
	if err := initializeSemanticProjection(local); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := &SemanticMemoryProvider{
		local: local, embedder: embedder, config: sealed,
		ctx: ctx, cancel: cancel, queue: make(chan MemoryRecord, sealed.QueueCapacity),
		remoteSlots: make(chan struct{}, 2), queryCache: make(map[string]semanticCacheEntry),
	}
	result.wg.Add(1)
	go result.worker()
	snapshot, err := local.Snapshot(context.Background())
	if err != nil {
		cancel()
		result.wg.Wait()
		return nil, err
	}
	result.wg.Add(1)
	go result.backfill(snapshot.Records)
	return result, nil
}

func normalizeSemanticMemoryConfig(config SemanticMemoryConfig) (SemanticMemoryConfig, error) {
	config.Model = strings.TrimSpace(config.Model)
	if config.Model == "" || len(config.Model) > 200 {
		return SemanticMemoryConfig{}, errors.New("semantic memory model is required")
	}
	if len(config.AllowedDomains) == 0 || len(config.AllowedDomains) > 5 {
		return SemanticMemoryConfig{}, errors.New("semantic memory requires explicit allowed domains")
	}
	config.AllowedDomains = append([]MemoryDomain(nil), config.AllowedDomains...)
	slices.Sort(config.AllowedDomains)
	for index, domain := range config.AllowedDomains {
		if !validMemoryDomain(domain) || privateMemoryDomain(domain) {
			return SemanticMemoryConfig{}, errors.New("semantic memory cannot export private or invalid domains")
		}
		if index > 0 && config.AllowedDomains[index-1] == domain {
			return SemanticMemoryConfig{}, errors.New("semantic memory domains contain duplicates")
		}
	}
	if config.MaxInputCharacters == 0 {
		config.MaxInputCharacters = 2_000
	}
	if config.MinLocalMatches == 0 {
		config.MinLocalMatches = 4
	}
	if config.MaxSemanticResults == 0 {
		config.MaxSemanticResults = 4
	}
	if config.MaxCandidates == 0 {
		config.MaxCandidates = 2_048
	}
	if config.QueueCapacity == 0 {
		config.QueueCapacity = 256
	}
	if config.TimeoutMillis == 0 {
		config.TimeoutMillis = 3_000
	}
	if config.MinimumSimilarity == 0 {
		// Keep candidate recall model-neutral until a concrete deployment has
		// calibrated a threshold for its embedding model and language mix.
		config.MinimumSimilarity = -1
	}
	if config.MaxInputCharacters > 8_000 || config.MinLocalMatches > 32 ||
		config.MaxSemanticResults > 16 || config.MaxCandidates > 10_000 ||
		config.QueueCapacity > 4_096 || config.TimeoutMillis > 15_000 ||
		config.MinimumSimilarity < -1 || config.MinimumSimilarity > 1 {
		return SemanticMemoryConfig{}, errors.New("semantic memory configuration exceeds bounds")
	}
	return config, nil
}

func ValidateSemanticMemoryConfig(config SemanticMemoryConfig) error {
	_, err := normalizeSemanticMemoryConfig(config)
	return err
}

func (memory *SemanticMemoryProvider) Append(ctx context.Context, record MemoryRecord) (MemoryRecord, error) {
	stored, err := memory.local.Append(ctx, record)
	if err == nil {
		memory.enqueue(stored)
	}
	return stored, err
}

func (memory *SemanticMemoryProvider) Retrieve(ctx context.Context, query MemoryQuery) ([]MemoryMatch, error) {
	matches, _, err := memory.RetrieveWithTrace(ctx, query)
	return matches, err
}

func (memory *SemanticMemoryProvider) RetrieveWithTrace(
	ctx context.Context,
	query MemoryQuery,
) ([]MemoryMatch, MemoryRetrievalTrace, error) {
	trace := MemoryRetrievalTrace{}
	sealed, err := sealMemoryQuery(query)
	if err != nil {
		return nil, trace, err
	}
	localMatches, err := memory.local.Retrieve(ctx, sealed)
	if err != nil || !sealed.Semantic || sealed.SemanticText == "" ||
		!memory.queryAllowsSemantic(sealed) || !memory.needsSemantic(sealed, localMatches) ||
		sensitiveEmbeddingText(sealed.SemanticText) {
		return localMatches, trace, err
	}
	trace.SemanticUsed = true
	started := time.Now()
	vector, cacheHit, err := memory.embed(ctx, sealed.SemanticText, true)
	trace.RemoteLatencyMillis = uint64(time.Since(started).Milliseconds())
	trace.QueryCacheHit = cacheHit
	trace.RemoteRequested = !cacheHit
	if err != nil {
		trace.DegradedCode = semanticDegradationCode(err)
		return localMatches, trace, nil
	}
	candidates, err := searchSemanticProjection(memory.local, memory.config, sealed, vector)
	if err != nil {
		trace.DegradedCode = "memory.semantic-index"
		return localMatches, trace, nil
	}
	snapshot, err := memory.local.Snapshot(ctx)
	if err != nil {
		trace.DegradedCode = "memory.semantic-index"
		return localMatches, trace, nil
	}
	semantic := resolveSemanticMatches(snapshot.Records, sealed, candidates, memory.config)
	return mergeMemoryChannels(localMatches, semantic, sealed.Budget), trace, nil
}

func (memory *SemanticMemoryProvider) queryAllowsSemantic(query MemoryQuery) bool {
	if len(query.Domains) == 0 {
		return true
	}
	for _, domain := range query.Domains {
		if slices.Contains(memory.config.AllowedDomains, domain) {
			return true
		}
	}
	return false
}

func (memory *SemanticMemoryProvider) Consolidate(ctx context.Context, input MemoryConsolidation) (MemoryRecord, error) {
	record, err := memory.local.Consolidate(ctx, input)
	if err == nil {
		deleteSemanticProjections(memory.local, input.Namespace, input.SourceMemoryIDs)
		memory.enqueue(record)
	}
	return record, err
}

func (memory *SemanticMemoryProvider) Forget(ctx context.Context, input MemoryForgetRequest) error {
	err := memory.local.Forget(ctx, input)
	if err == nil {
		deleteSemanticProjections(memory.local, input.Namespace, input.MemoryIDs)
	}
	return err
}

func (memory *SemanticMemoryProvider) Snapshot(ctx context.Context) (MemorySnapshot, error) {
	return memory.local.Snapshot(ctx)
}

func (memory *SemanticMemoryProvider) Health(ctx context.Context) ProviderHealth {
	return memory.local.Health(ctx)
}

func (memory *SemanticMemoryProvider) Close() error {
	memory.close.Do(func() {
		memory.cancel()
		memory.wg.Wait()
	})
	return nil
}

func (memory *SemanticMemoryProvider) needsSemantic(query MemoryQuery, matches []MemoryMatch) bool {
	if len(matches) < int(memory.config.MinLocalMatches) {
		return true
	}
	if len(query.Terms) == 0 {
		return false
	}
	for _, match := range matches {
		if slices.Contains(match.Reasons, recallSourceFTS) {
			return false
		}
	}
	return true
}

func (memory *SemanticMemoryProvider) enqueue(record MemoryRecord) {
	if !slices.Contains(memory.config.AllowedDomains, record.Namespace.Domain) ||
		sensitiveEmbeddingText(record.Content) {
		return
	}
	select {
	case memory.queue <- cloneMemoryRecord(record):
	default:
	}
}

func (memory *SemanticMemoryProvider) backfill(records []MemoryRecord) {
	defer memory.wg.Done()
	for _, record := range records {
		if !slices.Contains(memory.config.AllowedDomains, record.Namespace.Domain) ||
			sensitiveEmbeddingText(record.Content) {
			continue
		}
		select {
		case <-memory.ctx.Done():
			return
		case memory.queue <- cloneMemoryRecord(record):
		}
	}
}

func (memory *SemanticMemoryProvider) worker() {
	defer memory.wg.Done()
	for {
		select {
		case <-memory.ctx.Done():
			return
		case record := <-memory.queue:
			digest := semanticContentDigest(record.Content)
			if currentSemanticProjection(memory.local, memory.config.Model, sqliteMemoryKey(record), digest) {
				continue
			}
			vector, _, err := memory.embed(memory.ctx, record.Content, false)
			if err == nil {
				_ = storeSemanticProjection(memory.local, memory.config.Model, record, digest, vector)
			}
		}
	}
}

func (memory *SemanticMemoryProvider) embed(
	ctx context.Context,
	text string,
	cache bool,
) ([]float32, bool, error) {
	text = cropRunes(strings.TrimSpace(text), int(memory.config.MaxInputCharacters))
	key := semanticContentDigest(memory.config.Model + "\x00" + text)
	if cache {
		memory.remoteMu.Lock()
		if entry, ok := memory.queryCache[key]; ok {
			memory.cacheSequence++
			entry.sequence = memory.cacheSequence
			memory.queryCache[key] = entry
			vector := append([]float32(nil), entry.vector...)
			memory.remoteMu.Unlock()
			return vector, true, nil
		}
		memory.remoteMu.Unlock()
	}
	if err := memory.acquireRemote(ctx); err != nil {
		return nil, false, err
	}
	defer func() { <-memory.remoteSlots }()
	requestCtx, cancel := context.WithTimeout(ctx, time.Duration(memory.config.TimeoutMillis)*time.Millisecond)
	defer cancel()
	response, err := memory.embedder.Embed(requestCtx, provider.EmbeddingRequest{Inputs: []string{text}})
	if err != nil || len(response.Embeddings) != 1 ||
		(response.Model != "" && response.Model != memory.config.Model) {
		memory.recordRemoteFailure()
		if err == nil {
			err = errors.New("embedding provider returned an invalid response")
		}
		return nil, false, err
	}
	vector, err := normalizeSemanticVector(response.Embeddings[0])
	if err != nil || !memory.acceptDimensions(len(vector)) {
		memory.recordRemoteFailure()
		if err == nil {
			err = errors.New("embedding dimensions changed")
		}
		return nil, false, err
	}
	memory.recordRemoteSuccess()
	if cache {
		memory.rememberQuery(key, vector)
	}
	return vector, false, nil
}

func semanticDegradationCode(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "memory.semantic-timeout"
	}
	var providerError *provider.Error
	if errors.As(err, &providerError) {
		if providerError.StatusCode == http.StatusTooManyRequests {
			return "memory.semantic-rate-limited"
		}
		if providerError.StatusCode >= 500 || providerError.Kind == "transport" {
			return "memory.semantic-unavailable"
		}
		return "memory.semantic-invalid"
	}
	if strings.Contains(err.Error(), "invalid") || strings.Contains(err.Error(), "dimensions") {
		return "memory.semantic-invalid"
	}
	return "memory.semantic-unavailable"
}

func (memory *SemanticMemoryProvider) acquireRemote(ctx context.Context) error {
	memory.remoteMu.Lock()
	open := time.Now().Before(memory.openUntil)
	memory.remoteMu.Unlock()
	if open {
		return errors.New("embedding circuit is open")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case memory.remoteSlots <- struct{}{}:
		return nil
	}
}

func (memory *SemanticMemoryProvider) acceptDimensions(value int) bool {
	memory.remoteMu.Lock()
	defer memory.remoteMu.Unlock()
	if memory.dimensions == 0 {
		memory.dimensions = value
	}
	return value != 0 && value == memory.dimensions
}

func (memory *SemanticMemoryProvider) recordRemoteFailure() {
	memory.remoteMu.Lock()
	defer memory.remoteMu.Unlock()
	memory.consecutiveFailure++
	if memory.consecutiveFailure >= 3 {
		memory.openUntil = time.Now().Add(30 * time.Second)
	}
}

func (memory *SemanticMemoryProvider) recordRemoteSuccess() {
	memory.remoteMu.Lock()
	defer memory.remoteMu.Unlock()
	memory.consecutiveFailure = 0
	memory.openUntil = time.Time{}
}

func (memory *SemanticMemoryProvider) rememberQuery(key string, vector []float32) {
	memory.remoteMu.Lock()
	defer memory.remoteMu.Unlock()
	memory.cacheSequence++
	memory.queryCache[key] = semanticCacheEntry{vector: append([]float32(nil), vector...), sequence: memory.cacheSequence}
	if len(memory.queryCache) <= 128 {
		return
	}
	oldestKey := ""
	oldestSequence := uint64(math.MaxUint64)
	for candidate, entry := range memory.queryCache {
		if entry.sequence < oldestSequence {
			oldestKey, oldestSequence = candidate, entry.sequence
		}
	}
	delete(memory.queryCache, oldestKey)
}

func initializeSemanticProjection(local *SQLiteMemoryProvider) error {
	local.mu.Lock()
	defer local.mu.Unlock()
	if err := local.ready(); err != nil {
		return err
	}
	_, err := local.db.Exec(`CREATE TABLE IF NOT EXISTS memory_embeddings (
        memory_key TEXT NOT NULL,
        model TEXT NOT NULL,
        session_id TEXT NOT NULL,
        actor_id TEXT NOT NULL,
        controller_id TEXT NOT NULL,
        domain TEXT NOT NULL,
        content_digest TEXT NOT NULL,
        dimensions INTEGER NOT NULL,
        vector BLOB NOT NULL,
        PRIMARY KEY(memory_key, model)
    );
    CREATE INDEX IF NOT EXISTS memory_embeddings_lookup
        ON memory_embeddings(model, session_id, actor_id);`)
	return err
}

func currentSemanticProjection(local *SQLiteMemoryProvider, model, key, digest string) bool {
	local.mu.Lock()
	defer local.mu.Unlock()
	if local.ready() != nil {
		return false
	}
	var current string
	err := local.db.QueryRow(`SELECT content_digest FROM memory_embeddings
        WHERE memory_key = ? AND model = ?`, key, model).Scan(&current)
	return err == nil && current == digest
}

func storeSemanticProjection(
	local *SQLiteMemoryProvider,
	model string,
	record MemoryRecord,
	digest string,
	vector []float32,
) error {
	payload := encodeSemanticVector(vector)
	local.mu.Lock()
	defer local.mu.Unlock()
	if err := local.ready(); err != nil {
		return err
	}
	var currentContent string
	var forgotten int
	err := local.db.QueryRow(`SELECT content, forgotten FROM memory_records
        WHERE session_id = ? AND actor_id = ? AND controller_id = ?
        AND domain = ? AND memory_id = ?`, record.Namespace.SessionID,
		record.Namespace.ActorID, record.Namespace.ControllerID,
		string(record.Namespace.Domain), record.MemoryID,
	).Scan(&currentContent, &forgotten)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if forgotten != 0 || semanticContentDigest(currentContent) != digest {
		return nil
	}
	_, err = local.db.Exec(`INSERT INTO memory_embeddings(
        memory_key, model, session_id, actor_id, controller_id, domain,
        content_digest, dimensions, vector
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(memory_key, model) DO UPDATE SET
        session_id=excluded.session_id, actor_id=excluded.actor_id,
        controller_id=excluded.controller_id, domain=excluded.domain,
        content_digest=excluded.content_digest, dimensions=excluded.dimensions,
        vector=excluded.vector`,
		sqliteMemoryKey(record), model, record.Namespace.SessionID, record.Namespace.ActorID,
		record.Namespace.ControllerID, string(record.Namespace.Domain), digest, len(vector), payload,
	)
	return err
}

func deleteSemanticProjections(
	local *SQLiteMemoryProvider,
	namespace MemoryNamespace,
	memoryIDs []string,
) {
	local.mu.Lock()
	defer local.mu.Unlock()
	if local.ready() != nil {
		return
	}
	for _, memoryID := range memoryIDs {
		key := sqliteMemoryIdentityKey(namespace, memoryID)
		_, _ = local.db.Exec(`DELETE FROM memory_embeddings WHERE memory_key = ?`, key)
	}
}

func searchSemanticProjection(
	local *SQLiteMemoryProvider,
	config SemanticMemoryConfig,
	query MemoryQuery,
	vector []float32,
) ([]semanticCandidate, error) {
	local.mu.Lock()
	defer local.mu.Unlock()
	if err := local.ready(); err != nil {
		return nil, err
	}
	rows, err := local.db.Query(`SELECT memory_key, content_digest, dimensions, vector
        FROM memory_embeddings WHERE model = ? AND session_id = ? AND actor_id = ?
        ORDER BY memory_key LIMIT ?`, config.Model, query.SessionID, query.ActorID, config.MaxCandidates)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]semanticCandidate, 0)
	for rows.Next() {
		var key, digest string
		var dimensions int
		var payload []byte
		if err := rows.Scan(&key, &digest, &dimensions, &payload); err != nil {
			return nil, err
		}
		if dimensions != len(vector) {
			continue
		}
		candidate, err := decodeSemanticVector(payload, dimensions)
		if err != nil {
			continue
		}
		similarity := semanticDot(vector, candidate)
		if similarity >= config.MinimumSimilarity {
			result = append(result, semanticCandidate{memoryKey: key, contentDigest: digest, similarity: similarity})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.SortFunc(result, func(left, right semanticCandidate) int {
		if left.similarity > right.similarity {
			return -1
		}
		if left.similarity < right.similarity {
			return 1
		}
		return strings.Compare(left.memoryKey, right.memoryKey)
	})
	return result, nil
}

func resolveSemanticMatches(
	records []MemoryRecord,
	query MemoryQuery,
	candidates []semanticCandidate,
	config SemanticMemoryConfig,
) []MemoryMatch {
	byKey := make(map[string]MemoryRecord, len(records))
	for _, record := range records {
		byKey[sqliteMemoryKey(record)] = record
	}
	result := make([]MemoryMatch, 0, config.MaxSemanticResults)
	for _, candidate := range candidates {
		record, ok := byKey[candidate.memoryKey]
		if !ok || semanticContentDigest(record.Content) != candidate.contentDigest ||
			!slices.Contains(config.AllowedDomains, record.Namespace.Domain) ||
			!memoryVisibleToQuery(record, query) || memoryExpired(record, query.Now) ||
			!memoryMatchesFilters(record, query) {
			continue
		}
		score, reasons := scoreMemory(record, query)
		result = append(result, MemoryMatch{
			Record: record, Score: score, Reasons: appendUniqueString(reasons, recallSourceSemantic),
		})
		if len(result) == int(config.MaxSemanticResults) {
			break
		}
	}
	return result
}

func mergeMemoryChannels(local, semantic []MemoryMatch, budget MemoryBudget) []MemoryMatch {
	type ranked struct {
		match MemoryMatch
		rank  int
	}
	values := make([]ranked, 0, len(local)+len(semantic))
	seen := make(map[string]struct{}, len(local)+len(semantic))
	for index, match := range local {
		key := sqliteMemoryKey(match.Record)
		seen[key] = struct{}{}
		values = append(values, ranked{match: match, rank: index * 2})
	}
	for index, match := range semantic {
		key := sqliteMemoryKey(match.Record)
		if _, exists := seen[key]; exists {
			continue
		}
		values = append(values, ranked{match: match, rank: index*2 + 1})
	}
	slices.SortFunc(values, func(left, right ranked) int {
		if left.rank != right.rank {
			return left.rank - right.rank
		}
		return compareMemoryMatches(left.match, right.match)
	})
	result := make([]MemoryMatch, 0, min(len(values), int(budget.MaxRecords)))
	characters := 0
	for _, value := range values {
		count := utf8.RuneCountInString(value.match.Record.Content)
		if characters+count > int(budget.MaxCharacters) {
			continue
		}
		result = append(result, value.match)
		characters += count
		if len(result) == int(budget.MaxRecords) {
			break
		}
	}
	return result
}

func normalizeSemanticVector(input []float32) ([]float32, error) {
	if len(input) == 0 || len(input) > 8_192 {
		return nil, errors.New("embedding dimensions are invalid")
	}
	result := append([]float32(nil), input...)
	norm := float64(0)
	for _, value := range result {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return nil, errors.New("embedding contains a non-finite value")
		}
		norm += float64(value) * float64(value)
	}
	if norm == 0 {
		return nil, errors.New("embedding has zero magnitude")
	}
	norm = math.Sqrt(norm)
	for index := range result {
		result[index] = float32(float64(result[index]) / norm)
	}
	return result, nil
}

func encodeSemanticVector(vector []float32) []byte {
	result := make([]byte, len(vector)*4)
	for index, value := range vector {
		binary.LittleEndian.PutUint32(result[index*4:], math.Float32bits(value))
	}
	return result
}

func decodeSemanticVector(payload []byte, dimensions int) ([]float32, error) {
	if dimensions <= 0 || dimensions > 8_192 || len(payload) != dimensions*4 {
		return nil, sql.ErrNoRows
	}
	result := make([]float32, dimensions)
	for index := range result {
		result[index] = math.Float32frombits(binary.LittleEndian.Uint32(payload[index*4:]))
	}
	return result, nil
}

func semanticDot(left, right []float32) float64 {
	result := float64(0)
	for index := range left {
		result += float64(left[index]) * float64(right[index])
	}
	return result
}

func semanticContentDigest(content string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
}

func cropRunes(value string, maximum int) string {
	runes := []rune(value)
	if len(runes) > maximum {
		runes = runes[:maximum]
	}
	return string(runes)
}

func sensitiveEmbeddingText(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range []string{"api_key", "authorization:", "bearer ", "private key"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	for _, field := range strings.Fields(value) {
		if strings.HasPrefix(field, "sk-") && len(field) >= 20 {
			return true
		}
	}
	return false
}

var _ MemoryProvider = (*SemanticMemoryProvider)(nil)
var _ TracedMemoryProvider = (*SemanticMemoryProvider)(nil)
