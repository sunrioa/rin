// Package story implements an engine-neutral interactive-story adapter. It
// demonstrates that Rin can govern dialogue and narrative state without
// teaching the core about chapters, affection, or visual-novel concepts.
package story

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/sdk/hostkit"
)

const (
	HostID  = "story.host"
	WorldID = "story.world"
	ActorID = "story.mira"

	CapabilitySpeak       = "story.character.speak"
	CapabilityWait        = "story.scene.wait"
	CapabilityChangeTopic = "story.topic.change"
	CapabilityAcceptTask  = "story.task.accept"
	CapabilityVersion     = "2.0.0"

	TopicPhotographs  = "photographs"
	TopicFestival     = "festival"
	TopicSealedLetter = "sealed-letter"
)

const (
	scopePublic            = "story.public"
	scopeCharacterBoundary = "story.character-boundary"
)

// Line is one public transcript entry.
type Line struct {
	Speaker string `json:"speaker"`
	Text    string `json:"text"`
}

// State is a defensive view of the authoritative story state.
type State struct {
	Scene        string     `json:"scene"`
	Topic        string     `json:"topic"`
	Relation     int        `json:"relation"`
	AcceptedTask string     `json:"accepted_task,omitempty"`
	Transcript   []Line     `json:"transcript"`
	Epoch        host.Epoch `json:"epoch"`
	Sequence     uint64     `json:"sequence"`
}

// Adapter owns one deterministic story scene and its public transcript.
type Adapter struct {
	mu sync.Mutex

	manifest          host.HostManifest
	observationSchema host.Schema
	specs             []host.CapabilitySpec
	epoch             host.Epoch
	now               host.Timepoint
	sequence          uint64
	scene             string
	topic             string
	relation          int
	acceptedTask      string
	transcript        []Line
	operations        map[string]hostkit.AdapterResult
}

// New creates the reference interactive-story world.
func New() (*Adapter, error) {
	observationSchema, err := schema(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"scene":{"type":"string"},
			"topic":{"type":"string"},
			"relation":{"type":"integer","minimum":-10,"maximum":10},
			"accepted_task":{"type":"string"},
			"transcript":{"type":"array","maxItems":32,"items":{"type":"object","properties":{"speaker":{"type":"string"},"text":{"type":"string"}},"required":["speaker","text"],"additionalProperties":false}}
		},
		"required":["scene","topic","relation","accepted_task","transcript"],
		"additionalProperties":false
	}`)
	if err != nil {
		return nil, err
	}
	specs, err := capabilitySpecs()
	if err != nil {
		return nil, err
	}
	return &Adapter{
		manifest: host.HostManifest{
			ContractVersion:     host.ContractVersion,
			AdapterID:           "rin.reference.story",
			AdapterVersion:      "2.0.0",
			EngineID:            "rin.story",
			EngineVersion:       "1.0.0",
			Runtime:             "go",
			Platform:            "portable",
			Headless:            true,
			Authority:           host.AuthorityStandalone,
			Deployment:          host.DeploymentEmbeddedOffline,
			Control:             host.ControlSemantic,
			ClockModes:          []host.ClockMode{host.ClockStep},
			DecisionModes:       []host.DecisionMode{host.DecisionAsynchronous},
			MaxConcurrentActors: 1,
			Durability: host.Durability{
				Profile:        host.DurabilityAdvisory,
				StableIdentity: true,
			},
		},
		observationSchema: observationSchema,
		specs:             specs,
		epoch: host.Epoch{
			SessionID: "session.story.one",
			WorldID:   WorldID,
			Host:      1,
			World:     1,
			Timeline:  1,
		},
		now:        host.Timepoint{Clock: host.ClockStep, Value: 1},
		sequence:   1,
		scene:      "archive-room",
		topic:      TopicPhotographs,
		transcript: []Line{},
		operations: make(map[string]hostkit.AdapterResult),
	}, nil
}

// Target returns Mira's engine-neutral identity.
func Target() hostkit.AdapterTarget {
	return hostkit.AdapterTarget{HostID: HostID, WorldID: WorldID, ActorID: ActorID}
}

func (adapter *Adapter) Manifest() host.HostManifest {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	value := adapter.manifest
	value.ClockModes = append([]host.ClockMode(nil), value.ClockModes...)
	value.DecisionModes = append([]host.DecisionMode(nil), value.DecisionModes...)
	return value
}

func (adapter *Adapter) Snapshot(
	_ context.Context,
	target hostkit.AdapterTarget,
) (hostkit.AdapterSnapshot, error) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if err := validateTarget(target); err != nil {
		return hostkit.AdapterSnapshot{}, err
	}
	return adapter.adapterSnapshot(), nil
}

func (adapter *Adapter) Observe(
	_ context.Context,
	query host.ObservationQuery,
) (host.ObservationEnvelope, error) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if query.HostID != HostID || query.WorldID != WorldID || query.ActorID != ActorID {
		return host.ObservationEnvelope{}, errors.New("story observation target is unknown")
	}
	if query.ExpectedEpoch != adapter.epoch {
		return host.ObservationEnvelope{}, errors.New("story observation belongs to a stale epoch")
	}
	if query.AfterSequence > adapter.sequence {
		return host.ObservationEnvelope{}, errors.New("story observation cursor is ahead of the world")
	}
	payload, err := json.Marshal(map[string]any{
		"scene":         adapter.scene,
		"topic":         adapter.topic,
		"relation":      adapter.relation,
		"accepted_task": adapter.acceptedTask,
		"transcript":    cloneLines(adapter.transcript),
	})
	if err != nil {
		return host.ObservationEnvelope{}, err
	}
	actor := adapter.actorRef()
	sceneValue, _ := json.Marshal(adapter.scene)
	topicValue, _ := json.Marshal(adapter.topic)
	relationValue, _ := json.Marshal(adapter.relation)
	characterAttributes, _ := json.Marshal(map[string]any{
		"display_name": "Mira",
		"role":         "archive volunteer",
	})
	observation := host.ObservationEnvelope{
		ObservationID: fmt.Sprintf("observation.story.%d", adapter.sequence),
		HostID:        HostID,
		WorldID:       WorldID,
		ActorID:       ActorID,
		Epoch:         adapter.epoch,
		Sequence:      adapter.sequence,
		ObservedAt:    adapter.now,
		Schema: host.SchemaRef{
			ID: "rin.story.observation", Version: "1.0.0",
			SHA256: adapter.observationSchema.SHA256,
		},
		Payload: payload,
		Facts: []host.ObservationFact{
			{FactID: "fact.story.relation", Kind: "relation.trust", Subject: &actor, Tags: []string{"relation.public"}, Value: relationValue},
			{FactID: "fact.story.scene", Kind: "story.scene", Subject: &actor, Tags: []string{"story.public"}, Value: sceneValue},
			{FactID: "fact.story.topic", Kind: "story.topic", Subject: &actor, Tags: []string{"story.public"}, Value: topicValue},
		},
		Resources: []host.ObservationResource{{
			Ref: actor, Kind: "story.character", Tags: []string{"character.public"},
			Ownership: host.OwnershipActor, Scope: scopePublic, Quantity: 1,
			Unit: "character", Attributes: characterAttributes,
		}},
	}
	if err := host.ValidateObservationPayload(observation, adapter.observationSchema); err != nil {
		return host.ObservationEnvelope{}, err
	}
	return observation, nil
}

func (adapter *Adapter) ListCapabilities(context.Context) ([]host.CapabilitySpec, error) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	payload, err := json.Marshal(adapter.specs)
	if err != nil {
		return nil, err
	}
	var cloned []host.CapabilitySpec
	if err := json.Unmarshal(payload, &cloned); err != nil {
		return nil, err
	}
	return cloned, nil
}

func (adapter *Adapter) Bind(
	_ context.Context,
	target hostkit.AdapterTarget,
	request host.ActionRequest,
) (hostkit.AdapterBinding, error) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if err := validateTarget(target); err != nil {
		return hostkit.AdapterBinding{}, err
	}
	if len(request.Targets) != 0 {
		return hostkit.AdapterBinding{}, errors.New("story actions do not accept controller-authored target refs")
	}
	binding := hostkit.AdapterBinding{
		BindingID:  "binding.story." + request.RequestID,
		ValidUntil: host.Timepoint{Clock: adapter.now.Clock, Value: adapter.now.Value + 10},
	}
	switch request.Capability.ID {
	case CapabilitySpeak:
		if _, err := decodeSpeakArguments(request.Arguments); err != nil {
			return hostkit.AdapterBinding{}, err
		}
		binding.ResolvedTargets = []host.HostRef{adapter.actorRef()}
	case CapabilityChangeTopic:
		arguments, err := decodeTopicArguments(request.Arguments)
		if err != nil {
			return hostkit.AdapterBinding{}, err
		}
		binding.ResolvedTargets = []host.HostRef{adapter.topicRef(arguments.Topic)}
	case CapabilityAcceptTask:
		arguments, err := decodeTaskArguments(request.Arguments)
		if err != nil {
			return hostkit.AdapterBinding{}, err
		}
		binding.ResolvedTargets = []host.HostRef{adapter.taskRef(arguments.Task)}
	case CapabilityWait:
		binding.ResolvedTargets = []host.HostRef{adapter.actorRef()}
	default:
		return hostkit.AdapterBinding{}, errors.New("story capability is not implemented")
	}
	return binding, nil
}

func (adapter *Adapter) Preview(
	_ context.Context,
	_ hostkit.AdapterTarget,
	request host.ActionRequest,
	binding hostkit.AdapterBinding,
) ([]host.Effect, error) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if len(binding.ResolvedTargets) != 1 {
		return nil, errors.New("story binding must resolve exactly one target")
	}
	target := binding.ResolvedTargets[0]
	base := host.Effect{
		EffectID: "effect.story." + request.RequestID,
		Target:   &target, Ownership: host.OwnershipShared, Scope: scopePublic,
		Quantity: 1, Unit: "event", Reversible: false, Risk: host.RiskLow,
	}
	switch request.Capability.ID {
	case CapabilitySpeak:
		arguments, err := decodeSpeakArguments(request.Arguments)
		if err != nil {
			return nil, err
		}
		attributes, _ := json.Marshal(map[string]any{
			"delta": 1, "speaker": "Mira", "text": arguments.Text,
		})
		dialogue := base
		dialogue.EffectID += ".dialogue"
		dialogue.Kind = "social.dialogue"
		dialogue.Operation = host.EffectOperationCommunicate
		dialogue.Ownership = host.OwnershipActor
		dialogue.Tags = []string{"dialogue.public"}
		dialogue.Attributes = attributes
		relation := base
		relation.EffectID += ".relation"
		relation.Kind = "relation.update"
		relation.Operation = host.EffectOperationUpdate
		relation.Tags = []string{"relation.trust"}
		relation.Attributes = attributes
		return []host.Effect{relation, dialogue}, nil
	case CapabilityChangeTopic:
		arguments, err := decodeTopicArguments(request.Arguments)
		if err != nil {
			return nil, err
		}
		base.Kind = "story.progress"
		base.Operation = host.EffectOperationUpdate
		base.Tags = []string{"story.topic"}
		if arguments.Topic == TopicSealedLetter {
			base.Ownership = host.OwnershipActor
			base.Scope = scopeCharacterBoundary
			base.Risk = host.RiskModerate
		}
		base.Attributes, _ = json.Marshal(map[string]any{
			"from_topic": adapter.topic, "to_topic": arguments.Topic,
		})
		return []host.Effect{base}, nil
	case CapabilityAcceptTask:
		arguments, err := decodeTaskArguments(request.Arguments)
		if err != nil {
			return nil, err
		}
		base.Kind = "story.progress"
		base.Operation = host.EffectOperationUpdate
		base.Tags = []string{"story.task"}
		base.Attributes, _ = json.Marshal(map[string]any{"task": arguments.Task})
		return []host.Effect{base}, nil
	case CapabilityWait:
		base.Kind = "story.progress"
		base.Operation = host.EffectOperationUpdate
		base.Tags = []string{"story.wait"}
		base.Attributes, _ = json.Marshal(map[string]any{"duration_steps": 3})
		return []host.Effect{base}, nil
	default:
		return nil, errors.New("story capability is not implemented")
	}
}

func (adapter *Adapter) Execute(
	_ context.Context,
	operation hostkit.AdapterOperation,
) (hostkit.AdapterResult, error) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if current, exists := adapter.operations[operation.OperationID]; exists {
		return cloneResult(current), nil
	}
	if err := adapter.validateOperation(operation); err != nil {
		return hostkit.AdapterResult{}, err
	}
	if operation.Action.Capability.ID == CapabilityWait {
		result := hostkit.AdapterResult{Run: host.ActionRun{
			OperationID: operation.OperationID, Status: host.ActionRunning,
			ProgressSeq: 1, Progress: 0, UpdatedAt: adapter.now,
			Message: "Mira is taking a quiet moment.",
		}}
		adapter.operations[operation.OperationID] = cloneResult(result)
		return result, nil
	}

	var output any
	switch operation.Action.Capability.ID {
	case CapabilitySpeak:
		arguments, err := decodeSpeakArguments(operation.Action.NormalizedArguments)
		if err != nil {
			return hostkit.AdapterResult{}, err
		}
		adapter.transcript = append(adapter.transcript, Line{Speaker: "Mira", Text: arguments.Text})
		if len(adapter.transcript) > 32 {
			adapter.transcript = append([]Line(nil), adapter.transcript[len(adapter.transcript)-32:]...)
		}
		if adapter.relation < 10 {
			adapter.relation++
		}
		output = map[string]any{
			"line_id":  fmt.Sprintf("line.%d", adapter.sequence+1),
			"relation": adapter.relation,
			"text":     arguments.Text,
		}
	case CapabilityChangeTopic:
		arguments, err := decodeTopicArguments(operation.Action.NormalizedArguments)
		if err != nil {
			return hostkit.AdapterResult{}, err
		}
		adapter.topic = arguments.Topic
		output = map[string]any{"scene": adapter.scene, "topic": adapter.topic}
	case CapabilityAcceptTask:
		arguments, err := decodeTaskArguments(operation.Action.NormalizedArguments)
		if err != nil {
			return hostkit.AdapterResult{}, err
		}
		adapter.acceptedTask = arguments.Task
		output = map[string]any{"accepted": true, "task": adapter.acceptedTask}
	default:
		return hostkit.AdapterResult{}, errors.New("story capability is not executable")
	}
	adapter.advance()
	encoded, err := json.Marshal(output)
	if err != nil {
		return hostkit.AdapterResult{}, err
	}
	result := adapter.terminalResult(
		operation.OperationID,
		host.ActionSucceeded,
		"",
		"The story action changed the authoritative scene.",
		encoded,
	)
	adapter.operations[operation.OperationID] = cloneResult(result)
	return result, nil
}

func (adapter *Adapter) Cancel(
	_ context.Context,
	operation hostkit.AdapterOperation,
) (hostkit.AdapterResult, error) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	current, exists := adapter.operations[operation.OperationID]
	if !exists {
		return hostkit.AdapterResult{}, errors.New("story operation is unknown")
	}
	if current.Outcome != nil {
		return cloneResult(current), nil
	}
	if operation.Action.Capability.ID != CapabilityWait {
		return hostkit.AdapterResult{}, errors.New("story operation is not cancellable")
	}
	adapter.advance()
	result := adapter.terminalResult(
		operation.OperationID,
		host.ActionCancelled,
		"story.wait_cancelled",
		"The quiet moment was cancelled.",
		nil,
	)
	result.Run.ProgressSeq = current.Run.ProgressSeq + 1
	adapter.operations[operation.OperationID] = cloneResult(result)
	return result, nil
}

func (adapter *Adapter) Verify(
	_ context.Context,
	operation hostkit.AdapterOperation,
) (hostkit.AdapterResult, error) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	result, exists := adapter.operations[operation.OperationID]
	if !exists {
		return hostkit.AdapterResult{}, errors.New("story operation is unknown")
	}
	return cloneResult(result), nil
}

func (adapter *Adapter) PolicyFacts(
	_ context.Context,
	target hostkit.AdapterTarget,
) (hostkit.AdapterPolicyFacts, error) {
	if err := validateTarget(target); err != nil {
		return hostkit.AdapterPolicyFacts{}, err
	}
	return hostkit.AdapterPolicyFacts{
		KnownEffectKinds: []string{"relation.update", "social.dialogue", "story.progress"},
		KnownScopes:      []string{scopeCharacterBoundary, scopePublic},
	}, nil
}

// AdvanceObservation simulates an unrelated authoritative scene event.
func (adapter *Adapter) AdvanceObservation() {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	adapter.advance()
}

// RestartHost advances the Host generation and invalidates old bindings.
func (adapter *Adapter) RestartHost() {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	adapter.epoch.Host++
	adapter.sequence = 1
	adapter.now.Value++
	adapter.operations = make(map[string]hostkit.AdapterResult)
}

func (adapter *Adapter) State() State {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	return State{
		Scene: adapter.scene, Topic: adapter.topic, Relation: adapter.relation,
		AcceptedTask: adapter.acceptedTask,
		Transcript:   cloneLines(adapter.transcript),
		Epoch:        adapter.epoch, Sequence: adapter.sequence,
	}
}

func (adapter *Adapter) StateDigest() string {
	payload, _ := json.Marshal(adapter.State())
	return string(payload)
}

func (adapter *Adapter) adapterSnapshot() hostkit.AdapterSnapshot {
	return hostkit.AdapterSnapshot{
		Now: adapter.now, Epoch: adapter.epoch, ObservationSeq: adapter.sequence,
	}
}

func (adapter *Adapter) validateOperation(operation hostkit.AdapterOperation) error {
	if err := validateTarget(operation.Target); err != nil {
		return err
	}
	if operation.Action.ExpectedEpoch != adapter.epoch ||
		operation.Action.ObservationSeq != adapter.sequence {
		return errors.New("story operation belongs to stale scene state")
	}
	return nil
}

func (adapter *Adapter) advance() {
	adapter.sequence++
	adapter.now.Value++
}

func (adapter *Adapter) terminalResult(
	operationID string,
	status host.ActionRunStatus,
	code, summary string,
	output json.RawMessage,
) hostkit.AdapterResult {
	progress := uint32(0)
	if status == host.ActionSucceeded {
		progress = 100
	}
	return hostkit.AdapterResult{
		Run: host.ActionRun{
			OperationID: operationID, Status: status, ProgressSeq: 1,
			Progress: progress, UpdatedAt: adapter.now,
		},
		Outcome: &host.ActionOutcome{
			OperationID: operationID, Status: status, Code: code, Summary: summary,
			Epoch: adapter.epoch, WorldSeq: adapter.sequence, OccurredAt: adapter.now,
		},
		Output: append(json.RawMessage(nil), output...),
	}
}

func (adapter *Adapter) actorRef() host.HostRef {
	return host.HostRef{
		Namespace: "rin.story", Type: "character", Key: ActorID, Epoch: adapter.epoch,
	}
}

func (adapter *Adapter) topicRef(topic string) host.HostRef {
	return host.HostRef{
		Namespace: "rin.story", Type: "topic", Key: topic, Epoch: adapter.epoch,
	}
}

func (adapter *Adapter) taskRef(task string) host.HostRef {
	return host.HostRef{
		Namespace: "rin.story", Type: "task", Key: task, Epoch: adapter.epoch,
	}
}

type speakArguments struct {
	Text string `json:"text"`
}

type topicArguments struct {
	Topic string `json:"topic"`
}

type taskArguments struct {
	Task string `json:"task"`
}

func decodeSpeakArguments(raw json.RawMessage) (speakArguments, error) {
	var value speakArguments
	if err := json.Unmarshal(raw, &value); err != nil {
		return speakArguments{}, err
	}
	if value.Text == "" {
		return speakArguments{}, errors.New("story dialogue text is required")
	}
	return value, nil
}

func decodeTopicArguments(raw json.RawMessage) (topicArguments, error) {
	var value topicArguments
	if err := json.Unmarshal(raw, &value); err != nil {
		return topicArguments{}, err
	}
	if value.Topic != TopicPhotographs && value.Topic != TopicFestival &&
		value.Topic != TopicSealedLetter {
		return topicArguments{}, errors.New("story topic is not available")
	}
	return value, nil
}

func decodeTaskArguments(raw json.RawMessage) (taskArguments, error) {
	var value taskArguments
	if err := json.Unmarshal(raw, &value); err != nil {
		return taskArguments{}, err
	}
	if value.Task != "restore-photograph" && value.Task != "prepare-exhibit" {
		return taskArguments{}, errors.New("story task is not available")
	}
	return value, nil
}

func cloneResult(value hostkit.AdapterResult) hostkit.AdapterResult {
	value.Output = append(json.RawMessage(nil), value.Output...)
	if value.Outcome != nil {
		outcome := *value.Outcome
		outcome.Evidence = append([]host.HostRef(nil), value.Outcome.Evidence...)
		value.Outcome = &outcome
	}
	return value
}

func cloneLines(values []Line) []Line {
	cloned := make([]Line, len(values))
	copy(cloned, values)
	return cloned
}

func validateTarget(target hostkit.AdapterTarget) error {
	if target != Target() {
		return errors.New("story adapter target is unknown")
	}
	return nil
}
