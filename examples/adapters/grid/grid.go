// Package grid implements a small engine-neutral world used to prove Rin's
// Adapter contract without relying on Minecraft or any other game engine.
package grid

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"

	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/sdk/hostkit"
)

const (
	HostID  = "grid.host"
	WorldID = "grid.world"
	ActorID = "grid.actor"

	CapabilityMove    = "grid.actor.move"
	CapabilityCollect = "grid.resource.collect"
	CapabilityPut     = "grid.container.put"
	CapabilityTake    = "grid.container.take"
	CapabilityWait    = "grid.actor.wait"
	CapabilityVersion = "2.0.0"
)

// Point is one stable coordinate in the demonstration world.
type Point struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// Snapshot is a defensive state view used by tests and examples.
type Snapshot struct {
	Position  Point          `json:"position"`
	Inventory map[string]int `json:"inventory"`
	Container map[string]int `json:"container"`
	Resources map[string]int `json:"resources"`
	Epoch     host.Epoch     `json:"epoch"`
	Sequence  uint64         `json:"sequence"`
}

type cellResource struct {
	Key       string
	Kind      string
	Position  Point
	Quantity  int
	Protected bool
}

// Adapter is an in-memory authoritative grid world.
type Adapter struct {
	mu sync.Mutex

	manifest          host.HostManifest
	observationSchema host.Schema
	specs             []host.CapabilitySpec
	epoch             host.Epoch
	now               host.Timepoint
	sequence          uint64
	position          Point
	resources         map[string]*cellResource
	inventory         map[string]int
	container         map[string]int
	operations        map[string]hostkit.AdapterResult
}

// New creates the deterministic reference world.
func New() (*Adapter, error) {
	observationSchema, err := schema(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"position":{"$ref":"#/$defs/point"},
			"inventory":{"type":"object","additionalProperties":{"type":"integer","minimum":0}},
			"container":{"type":"object","additionalProperties":{"type":"integer","minimum":0}},
			"resources":{"type":"array","items":{"type":"object"}}
		},
		"required":["position","inventory","container","resources"],
		"additionalProperties":false,
		"$defs":{"point":{"type":"object","properties":{"x":{"type":"integer"},"y":{"type":"integer"}},"required":["x","y"],"additionalProperties":false}}
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
			AdapterID:           "rin.reference.grid",
			AdapterVersion:      "2.0.0",
			EngineID:            "rin.grid",
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
			SessionID: "session.grid.one",
			WorldID:   WorldID,
			Host:      1,
			World:     1,
			Timeline:  1,
		},
		now:      host.Timepoint{Clock: host.ClockStep, Value: 1},
		sequence: 1,
		position: Point{},
		resources: map[string]*cellResource{
			"wood": {
				Key: "wood", Kind: "resource.wood", Position: Point{X: 1}, Quantity: 3,
			},
			"crystal": {
				Key: "crystal", Kind: "resource.crystal", Position: Point{Y: 1},
				Quantity: 1, Protected: true,
			},
		},
		inventory:  make(map[string]int),
		container:  make(map[string]int),
		operations: make(map[string]hostkit.AdapterResult),
	}, nil
}

// Target returns the sole Actor's engine-neutral identity.
func Target() hostkit.AdapterTarget {
	return hostkit.AdapterTarget{HostID: HostID, WorldID: WorldID, ActorID: ActorID}
}

// Manifest implements hostkit.Adapter.
func (adapter *Adapter) Manifest() host.HostManifest {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	value := adapter.manifest
	value.ClockModes = append([]host.ClockMode(nil), value.ClockModes...)
	value.DecisionModes = append([]host.DecisionMode(nil), value.DecisionModes...)
	return value
}

// Snapshot implements hostkit.Adapter.
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

// Observe implements hostkit.Adapter.
func (adapter *Adapter) Observe(
	_ context.Context,
	query host.ObservationQuery,
) (host.ObservationEnvelope, error) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if query.HostID != HostID || query.WorldID != WorldID || query.ActorID != ActorID {
		return host.ObservationEnvelope{}, errors.New("grid observation target is unknown")
	}
	if query.ExpectedEpoch != adapter.epoch {
		return host.ObservationEnvelope{}, errors.New("grid observation belongs to a stale epoch")
	}
	if query.AfterSequence > adapter.sequence {
		return host.ObservationEnvelope{}, errors.New("grid observation cursor is ahead of the world")
	}
	resourceKeys := make([]string, 0, len(adapter.resources))
	for key := range adapter.resources {
		resourceKeys = append(resourceKeys, key)
	}
	sort.Strings(resourceKeys)
	type resourcePayload struct {
		Key       string `json:"key"`
		Kind      string `json:"kind"`
		Position  Point  `json:"position"`
		Quantity  int    `json:"quantity"`
		Protected bool   `json:"protected"`
	}
	payloadResources := make([]resourcePayload, 0, len(resourceKeys))
	resources := make([]host.ObservationResource, 0, len(resourceKeys)+1)
	for _, key := range resourceKeys {
		resource := adapter.resources[key]
		if resource.Quantity == 0 {
			continue
		}
		payloadResources = append(payloadResources, resourcePayload{
			Key: resource.Key, Kind: resource.Kind, Position: resource.Position,
			Quantity: resource.Quantity, Protected: resource.Protected,
		})
		ownership := host.OwnershipUnowned
		scope := "world.public"
		if resource.Protected {
			ownership = host.OwnershipPlayer
			scope = "world.protected"
		}
		attributes, _ := json.Marshal(map[string]any{
			"x": resource.Position.X, "y": resource.Position.Y,
		})
		resources = append(resources, host.ObservationResource{
			Ref:        adapter.resourceRef(resource),
			Kind:       resource.Kind,
			Tags:       []string{"grid.resource", resource.Kind},
			Ownership:  ownership,
			Scope:      scope,
			Quantity:   uint64(resource.Quantity),
			Unit:       "item",
			Attributes: attributes,
		})
	}
	containerAttributes, _ := json.Marshal(map[string]any{"x": 0, "y": 0})
	resources = append(resources, host.ObservationResource{
		Ref:        adapter.containerRef(),
		Kind:       "container.storage",
		Tags:       []string{"grid.container"},
		Ownership:  host.OwnershipActor,
		Scope:      "world.public",
		Unit:       "container",
		Attributes: containerAttributes,
	})
	payload, err := json.Marshal(map[string]any{
		"position":  adapter.position,
		"inventory": copyCounts(adapter.inventory),
		"container": copyCounts(adapter.container),
		"resources": payloadResources,
	})
	if err != nil {
		return host.ObservationEnvelope{}, err
	}
	actor := adapter.actorRef()
	positionValue, _ := json.Marshal(fmt.Sprintf("%d,%d", adapter.position.X, adapter.position.Y))
	observation := host.ObservationEnvelope{
		ObservationID: fmt.Sprintf("observation.grid.%d", adapter.sequence),
		HostID:        HostID,
		WorldID:       WorldID,
		ActorID:       ActorID,
		Epoch:         adapter.epoch,
		Sequence:      adapter.sequence,
		ObservedAt:    adapter.now,
		Schema: host.SchemaRef{
			ID:      "rin.grid.observation",
			Version: "1.0.0",
			SHA256:  adapter.observationSchema.SHA256,
		},
		Payload: payload,
		Facts: []host.ObservationFact{{
			FactID:  "fact.grid.actor.position",
			Kind:    "actor.position",
			Subject: &actor,
			Tags:    []string{"grid.position"},
			Value:   positionValue,
		}},
		Resources: resources,
	}
	if err := host.ValidateObservationPayload(observation, adapter.observationSchema); err != nil {
		return host.ObservationEnvelope{}, err
	}
	return observation, nil
}

// ListCapabilities implements hostkit.Adapter.
func (adapter *Adapter) ListCapabilities(
	context.Context,
) ([]host.CapabilitySpec, error) {
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

// Bind resolves controller arguments to current grid references.
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
	binding := hostkit.AdapterBinding{
		BindingID: "binding.grid." + request.RequestID,
		ValidUntil: host.Timepoint{
			Clock: adapter.now.Clock,
			Value: adapter.now.Value + 10,
		},
	}
	switch request.Capability.ID {
	case CapabilityMove, CapabilityWait:
		if len(request.Targets) != 0 {
			return hostkit.AdapterBinding{}, errors.New("grid actor action does not accept target refs")
		}
		binding.ResolvedTargets = []host.HostRef{adapter.actorRef()}
	case CapabilityCollect:
		arguments, err := decodeResourceArguments(request.Arguments)
		if err != nil {
			return hostkit.AdapterBinding{}, err
		}
		resource, exists := adapter.resources[arguments.Resource]
		if !exists || resource.Quantity < arguments.Quantity {
			return hostkit.AdapterBinding{}, errors.New("requested grid resource is unavailable")
		}
		binding.ResolvedTargets = []host.HostRef{adapter.resourceRef(resource)}
	case CapabilityPut, CapabilityTake:
		if _, err := decodeResourceArguments(request.Arguments); err != nil {
			return hostkit.AdapterBinding{}, err
		}
		binding.ResolvedTargets = []host.HostRef{adapter.containerRef()}
	default:
		return hostkit.AdapterBinding{}, errors.New("grid capability is not implemented")
	}
	if len(request.Targets) > 0 && !slices.Equal(request.Targets, binding.ResolvedTargets) {
		return hostkit.AdapterBinding{}, errors.New("grid target refs do not match current binding")
	}
	return binding, nil
}

// Preview returns authoritative standardized effects without mutating state.
func (adapter *Adapter) Preview(
	_ context.Context,
	_ hostkit.AdapterTarget,
	request host.ActionRequest,
	binding hostkit.AdapterBinding,
) ([]host.Effect, error) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	effect := host.Effect{
		EffectID:   "effect.grid." + request.RequestID,
		Ownership:  host.OwnershipActor,
		Scope:      "world.public",
		Quantity:   1,
		Unit:       "action",
		Reversible: true,
		Risk:       host.RiskLow,
	}
	switch request.Capability.ID {
	case CapabilityMove:
		arguments, err := decodeMoveArguments(request.Arguments)
		if err != nil {
			return nil, err
		}
		attributes, _ := json.Marshal(map[string]any{
			"from_x": adapter.position.X, "from_y": adapter.position.Y,
			"to_x": arguments.X, "to_y": arguments.Y,
		})
		effect.Kind = "world.position"
		effect.Operation = host.EffectOperationUpdate
		effect.Tags = []string{"actor.movement"}
		effect.Subject = refPointer(adapter.actorRef())
		effect.Target = refPointer(adapter.actorRef())
		effect.Attributes = attributes
	case CapabilityCollect:
		arguments, err := decodeResourceArguments(request.Arguments)
		if err != nil {
			return nil, err
		}
		resource := adapter.resources[arguments.Resource]
		if resource == nil || len(binding.ResolvedTargets) != 1 {
			return nil, errors.New("grid resource binding is invalid")
		}
		attributes, _ := json.Marshal(map[string]any{
			"resource": arguments.Resource,
			"x":        resource.Position.X,
			"y":        resource.Position.Y,
		})
		effect.Kind = "world.resource"
		effect.Operation = host.EffectOperationTransfer
		effect.Tags = []string{"resource.collect"}
		effect.Target = refPointer(binding.ResolvedTargets[0])
		effect.Quantity = uint64(arguments.Quantity)
		effect.Unit = "item"
		effect.Reversible = false
		effect.Attributes = attributes
		if resource.Protected {
			effect.Ownership = host.OwnershipPlayer
			effect.Scope = "world.protected"
			effect.Risk = host.RiskModerate
		}
	case CapabilityPut, CapabilityTake:
		arguments, err := decodeResourceArguments(request.Arguments)
		if err != nil {
			return nil, err
		}
		attributes, _ := json.Marshal(map[string]any{"resource": arguments.Resource})
		effect.Kind = "world.container"
		effect.Operation = host.EffectOperationTransfer
		effect.Tags = []string{"container.transfer"}
		effect.Target = refPointer(adapter.containerRef())
		effect.Quantity = uint64(arguments.Quantity)
		effect.Unit = "item"
		effect.Attributes = attributes
	case CapabilityWait:
		attributes, _ := json.Marshal(map[string]any{"duration_steps": 5})
		effect.Kind = "world.time"
		effect.Operation = host.EffectOperationUpdate
		effect.Tags = []string{"actor.wait"}
		effect.Subject = refPointer(adapter.actorRef())
		effect.Reversible = false
		effect.Attributes = attributes
	default:
		return nil, errors.New("grid capability is not implemented")
	}
	return []host.Effect{effect}, nil
}

// Execute applies one policy-approved BoundAction exactly once.
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
			OperationID: operation.OperationID,
			Status:      host.ActionRunning,
			ProgressSeq: 1,
			Progress:    0,
			UpdatedAt:   adapter.now,
			Message:     "The grid actor is waiting.",
		}}
		adapter.operations[operation.OperationID] = cloneResult(result)
		return result, nil
	}

	var output any
	switch operation.Action.Capability.ID {
	case CapabilityMove:
		arguments, err := decodeMoveArguments(operation.Action.NormalizedArguments)
		if err != nil {
			return hostkit.AdapterResult{}, err
		}
		adapter.position = Point{X: arguments.X, Y: arguments.Y}
		output = map[string]any{"x": arguments.X, "y": arguments.Y}
	case CapabilityCollect:
		arguments, err := decodeResourceArguments(operation.Action.NormalizedArguments)
		if err != nil {
			return hostkit.AdapterResult{}, err
		}
		resource := adapter.resources[arguments.Resource]
		if resource == nil || resource.Quantity < arguments.Quantity {
			return hostkit.AdapterResult{}, errors.New("grid resource changed before execution")
		}
		resource.Quantity -= arguments.Quantity
		adapter.inventory[arguments.Resource] += arguments.Quantity
		output = map[string]any{
			"resource":           arguments.Resource,
			"quantity":           arguments.Quantity,
			"inventory_quantity": adapter.inventory[arguments.Resource],
		}
	case CapabilityPut:
		arguments, err := decodeResourceArguments(operation.Action.NormalizedArguments)
		if err != nil {
			return hostkit.AdapterResult{}, err
		}
		if adapter.inventory[arguments.Resource] < arguments.Quantity {
			return hostkit.AdapterResult{}, errors.New("grid inventory has insufficient resources")
		}
		adapter.inventory[arguments.Resource] -= arguments.Quantity
		adapter.container[arguments.Resource] += arguments.Quantity
		output = transferOutput(arguments, adapter)
	case CapabilityTake:
		arguments, err := decodeResourceArguments(operation.Action.NormalizedArguments)
		if err != nil {
			return hostkit.AdapterResult{}, err
		}
		if adapter.container[arguments.Resource] < arguments.Quantity {
			return hostkit.AdapterResult{}, errors.New("grid container has insufficient resources")
		}
		adapter.container[arguments.Resource] -= arguments.Quantity
		adapter.inventory[arguments.Resource] += arguments.Quantity
		output = transferOutput(arguments, adapter)
	default:
		return hostkit.AdapterResult{}, errors.New("grid capability is not executable")
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
		"The grid action changed the authoritative world.",
		encoded,
	)
	adapter.operations[operation.OperationID] = cloneResult(result)
	return result, nil
}

// Cancel cooperatively terminates the long-running wait capability.
func (adapter *Adapter) Cancel(
	_ context.Context,
	operation hostkit.AdapterOperation,
) (hostkit.AdapterResult, error) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	current, exists := adapter.operations[operation.OperationID]
	if !exists {
		return hostkit.AdapterResult{}, errors.New("grid operation is unknown")
	}
	if current.Outcome != nil {
		return cloneResult(current), nil
	}
	if operation.Action.Capability.ID != CapabilityWait {
		return hostkit.AdapterResult{}, errors.New("grid operation is not cancellable")
	}
	adapter.advance()
	result := adapter.terminalResult(
		operation.OperationID,
		host.ActionCancelled,
		"grid.cancelled",
		"The grid wait was cancelled.",
		nil,
	)
	result.Run.ProgressSeq = current.Run.ProgressSeq + 1
	result.Run.Progress = current.Run.Progress
	adapter.operations[operation.OperationID] = cloneResult(result)
	return result, nil
}

// Verify returns the latest authoritative operation state without executing.
func (adapter *Adapter) Verify(
	_ context.Context,
	operation hostkit.AdapterOperation,
) (hostkit.AdapterResult, error) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	result, exists := adapter.operations[operation.OperationID]
	if !exists {
		return hostkit.AdapterResult{}, errors.New("grid operation is unknown")
	}
	return cloneResult(result), nil
}

// PolicyFacts implements hostkit.Adapter.
func (adapter *Adapter) PolicyFacts(
	_ context.Context,
	target hostkit.AdapterTarget,
) (hostkit.AdapterPolicyFacts, error) {
	if err := validateTarget(target); err != nil {
		return hostkit.AdapterPolicyFacts{}, err
	}
	return hostkit.AdapterPolicyFacts{
		KnownEffectKinds: []string{
			"world.container", "world.position", "world.resource", "world.time",
		},
		KnownScopes: []string{"world.protected", "world.public"},
	}, nil
}

// AdvanceObservation simulates an unrelated authoritative world event.
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

// State returns a defensive state snapshot.
func (adapter *Adapter) State() Snapshot {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	resources := make(map[string]int, len(adapter.resources))
	for key, resource := range adapter.resources {
		resources[key] = resource.Quantity
	}
	return Snapshot{
		Position: adapter.position, Inventory: copyCounts(adapter.inventory),
		Container: copyCounts(adapter.container), Resources: resources,
		Epoch: adapter.epoch, Sequence: adapter.sequence,
	}
}

// StateDigest returns deterministic JSON suitable for conformance checks.
func (adapter *Adapter) StateDigest() string {
	value := adapter.State()
	payload, _ := json.Marshal(value)
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
		return errors.New("grid operation belongs to stale world state")
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
	return hostkit.AdapterResult{
		Run: host.ActionRun{
			OperationID: operationID,
			Status:      status,
			ProgressSeq: 1,
			Progress:    terminalProgress(status),
			UpdatedAt:   adapter.now,
		},
		Outcome: &host.ActionOutcome{
			OperationID: operationID,
			Status:      status,
			Code:        code,
			Summary:     summary,
			Epoch:       adapter.epoch,
			WorldSeq:    adapter.sequence,
			OccurredAt:  adapter.now,
		},
		Output: append(json.RawMessage(nil), output...),
	}
}

func (adapter *Adapter) actorRef() host.HostRef {
	return host.HostRef{
		Namespace: "rin.grid", Type: "actor", Key: ActorID, Epoch: adapter.epoch,
	}
}

func (adapter *Adapter) resourceRef(resource *cellResource) host.HostRef {
	return host.HostRef{
		Namespace: "rin.grid", Type: "resource",
		Key:   fmt.Sprintf("%s@%d,%d", resource.Key, resource.Position.X, resource.Position.Y),
		Epoch: adapter.epoch,
	}
}

func (adapter *Adapter) containerRef() host.HostRef {
	return host.HostRef{
		Namespace: "rin.grid", Type: "container", Key: "storage.one", Epoch: adapter.epoch,
	}
}

type moveArguments struct {
	X int `json:"x"`
	Y int `json:"y"`
}

type resourceArguments struct {
	Resource string `json:"resource"`
	Quantity int    `json:"quantity"`
}

func decodeMoveArguments(raw json.RawMessage) (moveArguments, error) {
	var value moveArguments
	if err := json.Unmarshal(raw, &value); err != nil {
		return moveArguments{}, err
	}
	return value, nil
}

func decodeResourceArguments(raw json.RawMessage) (resourceArguments, error) {
	var value resourceArguments
	if err := json.Unmarshal(raw, &value); err != nil {
		return resourceArguments{}, err
	}
	if value.Resource == "" || value.Quantity <= 0 {
		return resourceArguments{}, errors.New("grid resource and positive quantity are required")
	}
	return value, nil
}

func transferOutput(arguments resourceArguments, adapter *Adapter) map[string]any {
	return map[string]any{
		"resource":           arguments.Resource,
		"quantity":           arguments.Quantity,
		"inventory_quantity": adapter.inventory[arguments.Resource],
		"container_quantity": adapter.container[arguments.Resource],
	}
}

func copyCounts(source map[string]int) map[string]int {
	cloned := make(map[string]int, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
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

func refPointer(value host.HostRef) *host.HostRef {
	return &value
}

func terminalProgress(status host.ActionRunStatus) uint32 {
	if status == host.ActionSucceeded {
		return 100
	}
	return 0
}

func validateTarget(target hostkit.AdapterTarget) error {
	if target != Target() {
		return errors.New("grid adapter target is unknown")
	}
	return nil
}

func schema(document string) (host.Schema, error) {
	return host.NewSchema([]byte(document))
}

func capabilitySpecs() ([]host.CapabilitySpec, error) {
	moveInput, err := schema(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{"x":{"type":"integer","minimum":-8,"maximum":8},"y":{"type":"integer","minimum":-8,"maximum":8}},
		"required":["x","y"],"additionalProperties":false
	}`)
	if err != nil {
		return nil, err
	}
	moveOutput, err := schema(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{"x":{"type":"integer"},"y":{"type":"integer"}},
		"required":["x","y"],"additionalProperties":false
	}`)
	if err != nil {
		return nil, err
	}
	moveEffects, err := schema(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{"from_x":{"type":"integer"},"from_y":{"type":"integer"},"to_x":{"type":"integer"},"to_y":{"type":"integer"}},
		"required":["from_x","from_y","to_x","to_y"],"additionalProperties":false
	}`)
	if err != nil {
		return nil, err
	}
	resourceInput, err := schema(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{"resource":{"type":"string","minLength":1,"maxLength":64},"quantity":{"type":"integer","minimum":1,"maximum":8}},
		"required":["resource","quantity"],"additionalProperties":false
	}`)
	if err != nil {
		return nil, err
	}
	collectOutput, err := schema(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{"resource":{"type":"string"},"quantity":{"type":"integer"},"inventory_quantity":{"type":"integer"}},
		"required":["resource","quantity","inventory_quantity"],"additionalProperties":false
	}`)
	if err != nil {
		return nil, err
	}
	transferOutputSchema, err := schema(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{"resource":{"type":"string"},"quantity":{"type":"integer"},"inventory_quantity":{"type":"integer"},"container_quantity":{"type":"integer"}},
		"required":["resource","quantity","inventory_quantity","container_quantity"],"additionalProperties":false
	}`)
	if err != nil {
		return nil, err
	}
	resourceEffects, err := schema(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{"resource":{"type":"string"},"x":{"type":"integer"},"y":{"type":"integer"}},
		"required":["resource"],"additionalProperties":false
	}`)
	if err != nil {
		return nil, err
	}
	waitInput, err := schema(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object","additionalProperties":false
	}`)
	if err != nil {
		return nil, err
	}
	waitOutput, err := schema(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object","properties":{"completed":{"type":"boolean"}},
		"required":["completed"],"additionalProperties":false
	}`)
	if err != nil {
		return nil, err
	}
	waitEffects, err := schema(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object","properties":{"duration_steps":{"type":"integer"}},
		"required":["duration_steps"],"additionalProperties":false
	}`)
	if err != nil {
		return nil, err
	}
	base := func(
		id, description string,
		input, output, effects host.Schema,
	) host.CapabilitySpec {
		return host.CapabilitySpec{
			Capability:         host.CapabilityRef{ID: id, Version: CapabilityVersion},
			Description:        description,
			Input:              input,
			Output:             output,
			EffectSchema:       effects,
			Kind:               host.CapabilityAtomic,
			Execution:          host.ExecutionImmediate,
			Cancellation:       host.CancellationUnsupported,
			RiskFloor:          host.RiskLow,
			RequiredDurability: host.DurabilityAdvisory,
			RequiredScopes:     []string{controlplane.ScopeActorExecute},
			ExecutionBudget:    host.Duration{Clock: host.ClockStep, Value: 20},
			MaxInputBytes:      2_048,
			MaxOutputBytes:     2_048,
			MaxEffects:         4,
		}
	}
	specs := []host.CapabilitySpec{
		base(CapabilityMove, "Move the grid actor to a bounded coordinate.", moveInput, moveOutput, moveEffects),
		base(CapabilityCollect, "Collect an available grid resource.", resourceInput, collectOutput, resourceEffects),
		base(CapabilityPut, "Put an inventory resource into the grid container.", resourceInput, transferOutputSchema, resourceEffects),
		base(CapabilityTake, "Take a resource from the grid container.", resourceInput, transferOutputSchema, resourceEffects),
		base(CapabilityWait, "Start a cancellable wait operation.", waitInput, waitOutput, waitEffects),
	}
	specs[4].Execution = host.ExecutionLongRunning
	specs[4].Cancellation = host.CancellationCooperative
	return specs, nil
}
