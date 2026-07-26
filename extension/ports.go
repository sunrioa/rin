// Package extension defines optional, vendor-neutral ports around Rin's
// authoritative decision and Host contracts.
package extension

import (
	"context"
	"time"

	"github.com/sunrioa/rin/protocol"
)

// MemoryDocument is a rebuildable projection of authoritative events. Text is
// sensitive derived data; SourceEventIDs preserve its provenance.
type MemoryDocument struct {
	ID             string
	SessionID      string
	ActorID        string
	Text           string
	TextSHA256     string
	SourceEventIDs []string
	StartTick      int64
	EndTick        int64
	Tags           []string
}

type MemoryQuery struct {
	SessionID string
	ActorID   string
	Text      string
	Limit     int
}

type MemoryMatch struct {
	DocumentID string
	Score      float64
}

// MemoryIndex stores derived search data only. Implementations must be safe for
// concurrent use and return promptly when ctx is cancelled. ReplaceSession
// must atomically replace the complete Session projection. Deleting or
// rebuilding the index must never mutate Rin's authoritative event history.
type MemoryIndex interface {
	ReplaceSession(context.Context, string, []MemoryDocument) error
	Search(context.Context, MemoryQuery) ([]MemoryMatch, error)
	DeleteSession(context.Context, string) error
}

type SpeechRequest struct {
	RequestID   string
	SessionID   string
	ActorID     string
	OperationID string
	Text        string
	TextSHA256  string
	Language    string
	VoiceID     string
	MediaType   string
}

// AudioArtifactRef contains metadata only. Raw audio remains in the provider's
// bounded artifact store and is referenced by an immutable ArtifactRef.
type AudioArtifactRef struct {
	Ref            protocol.ArtifactRef
	TextSHA256     string
	DurationMillis uint64
}

// SpeechProvider synthesizes already-approved display text. It has no access
// to Action Offers and cannot grant or execute game authority. Implementations
// must be safe for concurrent use, return promptly when ctx is cancelled, and
// keep returned immutable artifacts available for the caller's cache lifetime.
type SpeechProvider interface {
	Synthesize(context.Context, SpeechRequest) (AudioArtifactRef, error)
}

type TelemetryName string
type TelemetryStatus string

const (
	TelemetrySpeechSynthesis TelemetryName = "speech.synthesis"
	TelemetrySpeechPlayback  TelemetryName = "speech.playback"

	TelemetryReady     TelemetryStatus = "ready"
	TelemetryCacheHit  TelemetryStatus = "cache-hit"
	TelemetryTextOnly  TelemetryStatus = "text-only"
	TelemetryCancelled TelemetryStatus = "cancelled"
	TelemetryCompleted TelemetryStatus = "completed"
	TelemetryFailed    TelemetryStatus = "failed"
)

// TelemetryEvent deliberately has no arbitrary attributes, prompt, dialogue,
// audio, credential, or save-payload field.
type TelemetryEvent struct {
	Name           TelemetryName
	SessionID      string
	RequestID      string
	ActorID        string
	OperationID    string
	ArtifactID     string
	Status         TelemetryStatus
	DurationMillis uint64
	OccurredAt     time.Time
}

type TelemetrySink interface {
	// Record must be safe for concurrent use and return promptly when ctx is
	// cancelled. Event IDs are opaque correlation identifiers, not content.
	Record(context.Context, TelemetryEvent) error
}
