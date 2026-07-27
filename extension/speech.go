package extension

import (
	"container/list"
	"context"
	"errors"
	"sync"
	"time"

	"github.com/sunrioa/rin/protocol"
)

type SpeechStatus string
type SpeechReasonCode string
type PlaybackStatus string

const (
	SpeechReady    SpeechStatus = "ready"
	SpeechTextOnly SpeechStatus = "text-only"

	SpeechProviderFailed  SpeechReasonCode = "speech_provider_failed"
	SpeechArtifactInvalid SpeechReasonCode = "speech_artifact_invalid"
	SpeechCancelled       SpeechReasonCode = "speech_cancelled"

	PlaybackCompleted PlaybackStatus = "completed"
	PlaybackCancelled PlaybackStatus = "cancelled"
	PlaybackFailed    PlaybackStatus = "failed"
)

type SpeechResult struct {
	Status     SpeechStatus
	Artifact   *AudioArtifactRef
	CacheHit   bool
	ReasonCode SpeechReasonCode
}

type PlaybackReport struct {
	SessionID      string
	RequestID      string
	ActorID        string
	OperationID    string
	ArtifactID     string
	Status         PlaybackStatus
	DurationMillis uint64
	OccurredAt     time.Time
}

type SpeechManagerConfig struct {
	MaxEntries int
	TTL        time.Duration
	Now        func() time.Time
}

type speechCacheEntry struct {
	key       string
	artifact  AudioArtifactRef
	expiresAt time.Time
}

type SpeechManager struct {
	provider   SpeechProvider
	telemetry  TelemetrySink
	maxEntries int
	ttl        time.Duration
	now        func() time.Time

	mu      sync.Mutex
	entries map[string]*list.Element
	order   *list.List
}

func NewSpeechManager(
	provider SpeechProvider,
	telemetry TelemetrySink,
	config SpeechManagerConfig,
) (*SpeechManager, error) {
	if provider == nil {
		return nil, errors.New("speech provider is required")
	}
	if config.MaxEntries == 0 {
		config.MaxEntries = 128
	}
	if config.MaxEntries < 1 || config.MaxEntries > 4096 {
		return nil, errors.New("speech cache entries must be between 1 and 4096")
	}
	if config.TTL == 0 {
		config.TTL = 30 * time.Minute
	}
	if config.TTL < 0 || config.TTL > 24*time.Hour {
		return nil, errors.New("speech cache TTL must be positive and at most 24 hours")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &SpeechManager{
		provider: provider, telemetry: telemetry,
		maxEntries: config.MaxEntries, ttl: config.TTL, now: config.Now,
		entries: make(map[string]*list.Element), order: list.New(),
	}, nil
}

func (manager *SpeechManager) Prepare(
	ctx context.Context,
	request SpeechRequest,
) (SpeechResult, error) {
	if err := requireContext(ctx); err != nil {
		return SpeechResult{
			Status: SpeechTextOnly, ReasonCode: SpeechCancelled,
		}, err
	}
	if err := ctx.Err(); err != nil {
		return SpeechResult{
			Status: SpeechTextOnly, ReasonCode: SpeechCancelled,
		}, err
	}
	request.TextSHA256 = textHash(request.Text)
	if err := validateSpeechRequest(request); err != nil {
		return SpeechResult{}, err
	}
	key := speechCacheKey(request)
	if artifact, ok := manager.cached(key); ok {
		manager.record(ctx, request, artifact.Ref.ID, TelemetryCacheHit, artifact.DurationMillis)
		return SpeechResult{
			Status: SpeechReady, Artifact: &artifact, CacheHit: true,
		}, nil
	}
	artifact, err := manager.provider.Synthesize(ctx, request)
	if err != nil {
		if ctx.Err() != nil {
			manager.record(ctx, request, "", TelemetryCancelled, 0)
			return SpeechResult{
				Status: SpeechTextOnly, ReasonCode: SpeechCancelled,
			}, ctx.Err()
		}
		manager.record(ctx, request, "", TelemetryTextOnly, 0)
		return SpeechResult{
			Status: SpeechTextOnly, ReasonCode: SpeechProviderFailed,
		}, nil
	}
	if err := validateAudioArtifact(request, artifact); err != nil {
		manager.record(ctx, request, "", TelemetryTextOnly, 0)
		return SpeechResult{
			Status: SpeechTextOnly, ReasonCode: SpeechArtifactInvalid,
		}, nil
	}
	manager.store(key, artifact)
	manager.record(ctx, request, artifact.Ref.ID, TelemetryReady, artifact.DurationMillis)
	copied := artifact
	return SpeechResult{Status: SpeechReady, Artifact: &copied}, nil
}

func (manager *SpeechManager) ReportPlayback(
	ctx context.Context,
	report PlaybackReport,
) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if err := validateIDFields(
		idField{"session_id", report.SessionID},
		idField{"request_id", report.RequestID},
		idField{"actor_id", report.ActorID},
		idField{"operation_id", report.OperationID},
		idField{"artifact_id", report.ArtifactID},
	); err != nil {
		return err
	}
	if report.Status != PlaybackCompleted && report.Status != PlaybackCancelled &&
		report.Status != PlaybackFailed {
		return errors.New("playback status is invalid")
	}
	if report.DurationMillis > uint64(protocol.MaxJSONSafeInteger) {
		return errors.New("playback duration is not JSON-safe")
	}
	if report.OccurredAt.IsZero() {
		return errors.New("playback time is required")
	}
	if manager.telemetry == nil {
		return nil
	}
	telemetryStatus := TelemetryCompleted
	if report.Status == PlaybackCancelled {
		telemetryStatus = TelemetryCancelled
	} else if report.Status == PlaybackFailed {
		telemetryStatus = TelemetryFailed
	}
	return manager.telemetry.Record(ctx, TelemetryEvent{
		Name:      TelemetrySpeechPlayback,
		SessionID: report.SessionID, RequestID: report.RequestID,
		ActorID: report.ActorID, OperationID: report.OperationID,
		ArtifactID: report.ArtifactID, Status: telemetryStatus,
		DurationMillis: report.DurationMillis, OccurredAt: report.OccurredAt,
	})
}

func (manager *SpeechManager) record(
	ctx context.Context,
	request SpeechRequest,
	artifactID string,
	status TelemetryStatus,
	duration uint64,
) {
	if manager.telemetry == nil {
		return
	}
	_ = manager.telemetry.Record(ctx, TelemetryEvent{
		Name:      TelemetrySpeechSynthesis,
		SessionID: request.SessionID, RequestID: request.RequestID,
		ActorID: request.ActorID, OperationID: request.OperationID,
		ArtifactID: artifactID, Status: status,
		DurationMillis: duration, OccurredAt: manager.now().UTC(),
	})
}

func (manager *SpeechManager) cached(key string) (AudioArtifactRef, bool) {
	now := manager.now()
	manager.mu.Lock()
	defer manager.mu.Unlock()
	element := manager.entries[key]
	if element == nil {
		return AudioArtifactRef{}, false
	}
	entry := element.Value.(speechCacheEntry)
	if !now.Before(entry.expiresAt) {
		delete(manager.entries, key)
		manager.order.Remove(element)
		return AudioArtifactRef{}, false
	}
	manager.order.MoveToFront(element)
	return entry.artifact, true
}

func (manager *SpeechManager) store(key string, artifact AudioArtifactRef) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if existing := manager.entries[key]; existing != nil {
		existing.Value = speechCacheEntry{
			key: key, artifact: artifact, expiresAt: manager.now().Add(manager.ttl),
		}
		manager.order.MoveToFront(existing)
		return
	}
	element := manager.order.PushFront(speechCacheEntry{
		key: key, artifact: artifact, expiresAt: manager.now().Add(manager.ttl),
	})
	manager.entries[key] = element
	for manager.order.Len() > manager.maxEntries {
		oldest := manager.order.Back()
		delete(manager.entries, oldest.Value.(speechCacheEntry).key)
		manager.order.Remove(oldest)
	}
}

func speechCacheKey(request SpeechRequest) string {
	return request.SessionID + "\x00" + request.ActorID + "\x00" +
		request.TextSHA256 + "\x00" +
		request.Language + "\x00" + request.VoiceID + "\x00" +
		request.MediaType
}
