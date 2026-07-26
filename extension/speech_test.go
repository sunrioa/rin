package extension

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/sunrioa/rin/protocol"
)

type speechProviderFixture struct {
	mu       sync.Mutex
	calls    int
	requests []SpeechRequest
	artifact AudioArtifactRef
	err      error
	wait     bool
}

func (provider *speechProviderFixture) Synthesize(
	ctx context.Context,
	request SpeechRequest,
) (AudioArtifactRef, error) {
	provider.mu.Lock()
	provider.calls++
	provider.requests = append(provider.requests, request)
	wait := provider.wait
	artifact, err := provider.artifact, provider.err
	provider.mu.Unlock()
	if wait {
		<-ctx.Done()
		return AudioArtifactRef{}, ctx.Err()
	}
	return artifact, err
}

type telemetryFixture struct {
	events []TelemetryEvent
	err    error
}

func (sink *telemetryFixture) Record(
	_ context.Context,
	event TelemetryEvent,
) error {
	sink.events = append(sink.events, event)
	return sink.err
}

func speechRequestFixture() SpeechRequest {
	return SpeechRequest{
		RequestID: "speech.request.1", SessionID: "session.speech",
		ActorID: "actor.speech", OperationID: "operation.speech",
		Text: "The northern gate is open.", Language: "en-US",
		VoiceID: "voice.guide", MediaType: "audio/ogg",
	}
}

func audioArtifactFixture(request SpeechRequest) AudioArtifactRef {
	return AudioArtifactRef{
		Ref: protocol.ArtifactRef{
			ID: "audio.speech.1", MediaType: request.MediaType,
			URI:    "https://artifacts.example/audio/speech.ogg",
			SHA256: textHash("fixture audio bytes"), SizeBytes: 128,
		},
		TextSHA256: textHash(request.Text), DurationMillis: 900,
	}
}

func TestSpeechUsesApprovedTextBoundedCacheAndTypedTelemetry(t *testing.T) {
	now := time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC)
	request := speechRequestFixture()
	provider := &speechProviderFixture{artifact: audioArtifactFixture(request)}
	telemetry := &telemetryFixture{}
	manager, err := NewSpeechManager(provider, telemetry, SpeechManagerConfig{
		MaxEntries: 2, TTL: time.Hour, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := manager.Prepare(context.Background(), request)
	if err != nil || first.Status != SpeechReady || first.Artifact == nil ||
		first.CacheHit {
		t.Fatalf("unexpected first speech result: %+v, %v", first, err)
	}
	if len(provider.requests) != 1 ||
		provider.requests[0].TextSHA256 != textHash(request.Text) {
		t.Fatal("provider did not receive approved-text binding")
	}

	retry := request
	retry.RequestID = "speech.request.2"
	second, err := manager.Prepare(context.Background(), retry)
	if err != nil || second.Status != SpeechReady || !second.CacheHit ||
		provider.calls != 1 {
		t.Fatalf("speech cache did not reuse immutable artifact: %+v, %v", second, err)
	}
	otherSession := retry
	otherSession.RequestID = "speech.request.3"
	otherSession.SessionID = "session.other"
	if _, err := manager.Prepare(context.Background(), otherSession); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 2 {
		t.Fatal("speech artifact cache crossed Session boundary")
	}
	otherActor := retry
	otherActor.RequestID = "speech.request.4"
	otherActor.ActorID = "actor.other"
	if _, err := manager.Prepare(context.Background(), otherActor); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 3 {
		t.Fatal("speech artifact cache crossed Actor boundary")
	}
	if len(telemetry.events) != 4 ||
		telemetry.events[0].Status != TelemetryReady ||
		telemetry.events[1].Status != TelemetryCacheHit {
		t.Fatalf("unexpected speech telemetry: %+v", telemetry.events)
	}

	eventType := reflect.TypeOf(TelemetryEvent{})
	for _, forbidden := range []string{"Text", "Prompt", "Audio", "Credential", "Payload"} {
		if _, exists := eventType.FieldByName(forbidden); exists {
			t.Fatalf("TelemetryEvent exposes sensitive field %s", forbidden)
		}
	}
}

func TestSpeechDegradesToTextAndHonorsCancellation(t *testing.T) {
	request := speechRequestFixture()
	failed := &speechProviderFixture{err: errors.New("provider unavailable")}
	manager, err := NewSpeechManager(failed, nil, SpeechManagerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.Prepare(context.Background(), request)
	if err != nil || result.Status != SpeechTextOnly ||
		result.ReasonCode != SpeechProviderFailed || result.Artifact != nil {
		t.Fatalf("speech did not degrade to text: %+v, %v", result, err)
	}

	malformed := &speechProviderFixture{artifact: audioArtifactFixture(request)}
	malformed.artifact.TextSHA256 = textHash("different text")
	manager, err = NewSpeechManager(malformed, nil, SpeechManagerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	result, err = manager.Prepare(context.Background(), request)
	if err != nil || result.Status != SpeechTextOnly ||
		result.ReasonCode != SpeechArtifactInvalid {
		t.Fatalf("malformed artifact did not fail closed: %+v, %v", result, err)
	}

	blocking := &speechProviderFixture{wait: true}
	manager, err = NewSpeechManager(blocking, nil, SpeechManagerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err = manager.Prepare(ctx, request)
	if !errors.Is(err, context.Canceled) ||
		result.Status != SpeechTextOnly ||
		result.ReasonCode != SpeechCancelled {
		t.Fatalf("speech cancellation was lost: %+v, %v", result, err)
	}
}

func TestSpeechSynthesisIgnoresTelemetryFailure(t *testing.T) {
	request := speechRequestFixture()
	provider := &speechProviderFixture{artifact: audioArtifactFixture(request)}
	manager, err := NewSpeechManager(
		provider,
		&telemetryFixture{err: errors.New("telemetry unavailable")},
		SpeechManagerConfig{},
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.Prepare(context.Background(), request)
	if err != nil || result.Status != SpeechReady || result.Artifact == nil {
		t.Fatalf("telemetry changed speech availability: %+v, %v", result, err)
	}
}

func TestSpeechManagerCacheIsConcurrent(t *testing.T) {
	request := speechRequestFixture()
	provider := &speechProviderFixture{artifact: audioArtifactFixture(request)}
	manager, err := NewSpeechManager(provider, nil, SpeechManagerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	const workers = 32
	var wait sync.WaitGroup
	errorsByWorker := make(chan error, workers)
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, prepareErr := manager.Prepare(context.Background(), request)
			if prepareErr != nil {
				errorsByWorker <- prepareErr
				return
			}
			if result.Status != SpeechReady || result.Artifact == nil {
				errorsByWorker <- errors.New("speech was not ready")
			}
		}()
	}
	wait.Wait()
	close(errorsByWorker)
	for workerErr := range errorsByWorker {
		t.Error(workerErr)
	}
}

func TestPlaybackReportContainsNoDialogueAndPropagatesTelemetryFailure(t *testing.T) {
	sinkError := errors.New("telemetry unavailable")
	sink := &telemetryFixture{err: sinkError}
	manager, err := NewSpeechManager(
		&speechProviderFixture{},
		sink,
		SpeechManagerConfig{},
	)
	if err != nil {
		t.Fatal(err)
	}
	report := PlaybackReport{
		SessionID: "session.speech", RequestID: "speech.request.1",
		ActorID: "actor.speech", OperationID: "operation.speech",
		ArtifactID: "audio.speech.1", Status: PlaybackCompleted,
		DurationMillis: 850, OccurredAt: time.Now().UTC(),
	}
	if err := manager.ReportPlayback(context.Background(), report); !errors.Is(err, sinkError) {
		t.Fatalf("telemetry failure was hidden: %v", err)
	}
	if len(sink.events) != 1 ||
		sink.events[0].Name != TelemetrySpeechPlayback ||
		sink.events[0].ArtifactID != report.ArtifactID {
		t.Fatalf("invalid playback telemetry: %+v", sink.events)
	}
	report.Status = PlaybackStatus("private dialogue")
	if err := manager.ReportPlayback(context.Background(), report); err == nil {
		t.Fatal("arbitrary playback status was accepted")
	}
}
