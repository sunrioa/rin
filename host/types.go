// Package host defines the engine-neutral contract between Rin and an
// authoritative game host. It intentionally contains no engine, transport, or
// model-provider types.
package host

import "encoding/json"

const (
	// ContractVersion identifies this local Host Contract shape.
	ContractVersion = "rin.host/v1"
	// SchemaDialect is the only JSON Schema dialect accepted by this contract.
	SchemaDialect = "https://json-schema.org/draft/2020-12/schema"
)

// ClockMode describes how a host advances authoritative game time.
type ClockMode string

const (
	ClockEvent    ClockMode = "event"
	ClockStep     ClockMode = "step"
	ClockRealtime ClockMode = "realtime"
)

// DecisionMode describes how actors receive decision opportunities.
type DecisionMode string

const (
	DecisionSequential   DecisionMode = "sequential"
	DecisionSimultaneous DecisionMode = "simultaneous"
	DecisionAsynchronous DecisionMode = "asynchronous"
)

// AuthorityMode describes where authoritative world mutation occurs.
type AuthorityMode string

const (
	AuthorityStandalone     AuthorityMode = "standalone"
	AuthorityServer         AuthorityMode = "server"
	AuthorityClientAdvisory AuthorityMode = "client-advisory"
)

// DeploymentMode describes how a host reaches Rin.
type DeploymentMode string

const (
	DeploymentLoopbackSidecar DeploymentMode = "loopback-sidecar"
	DeploymentDedicatedServer DeploymentMode = "dedicated-server"
	DeploymentRemoteHTTPS     DeploymentMode = "remote-https"
	DeploymentEmbeddedOffline DeploymentMode = "embedded-offline"
	DeploymentComputerControl DeploymentMode = "computer-control"
)

// ControlMode describes the lowest-level interface exposed by a host.
type ControlMode string

const (
	ControlSemantic        ControlMode = "semantic"
	ControlAccessibility   ControlMode = "accessibility"
	ControlComputerControl ControlMode = "computer-control"
)

// DurabilityProfile summarizes crash and retry guarantees for host effects.
type DurabilityProfile string

const (
	DurabilityAdvisory      DurabilityProfile = "advisory"
	DurabilityIdempotent    DurabilityProfile = "idempotent-action"
	DurabilityTransactional DurabilityProfile = "transactional-action"
)

// Durability records the concrete guarantees behind a durability profile.
type Durability struct {
	Profile              DurabilityProfile `json:"profile"`
	StableIdentity       bool              `json:"stable_identity"`
	DurableBeforeNetwork bool              `json:"durable_before_network"`
	DurableOutbox        bool              `json:"durable_outbox"`
	IdempotentApply      bool              `json:"idempotent_apply"`
	AtomicApplyAndOutbox bool              `json:"atomic_apply_and_outbox"`
}

// HostManifest declares engine-neutral facts about one authoritative adapter.
type HostManifest struct {
	ContractVersion     string         `json:"contract_version"`
	AdapterID           string         `json:"adapter_id"`
	AdapterVersion      string         `json:"adapter_version"`
	EngineID            string         `json:"engine_id"`
	EngineVersion       string         `json:"engine_version"`
	Runtime             string         `json:"runtime"`
	Platform            string         `json:"platform"`
	Headless            bool           `json:"headless"`
	Authority           AuthorityMode  `json:"authority"`
	Deployment          DeploymentMode `json:"deployment"`
	Control             ControlMode    `json:"control"`
	ClockModes          []ClockMode    `json:"clock_modes"`
	DecisionModes       []DecisionMode `json:"decision_modes"`
	MaxConcurrentActors uint32         `json:"max_concurrent_actors"`
	Durability          Durability     `json:"durability"`
}

// Epoch identifies the authoritative host generation in which an observation
// or action is valid. Scene loads increment World; rollback or save-line forks
// increment Timeline. The values never stand for render or physics frames.
type Epoch struct {
	SessionID string `json:"session_id"`
	WorldID   string `json:"world_id"`
	Host      uint64 `json:"host"`
	World     uint64 `json:"world"`
	Timeline  uint64 `json:"timeline"`
}

// HostRef is an opaque game-object reference. Only the owning adapter may
// resolve Key on its authority thread. Ephemeral references must not be saved.
type HostRef struct {
	Namespace string `json:"namespace"`
	Type      string `json:"type"`
	Key       string `json:"key"`
	Ephemeral bool   `json:"ephemeral"`
	Epoch     Epoch  `json:"epoch"`
}

// Schema is a bounded, self-contained JSON Schema 2020-12 document. SHA256 is
// calculated over CanonicalizeSchema output, not caller formatting.
type Schema struct {
	Dialect  string          `json:"dialect"`
	Document json.RawMessage `json:"document"`
	SHA256   string          `json:"sha256"`
}

// EffectClass describes whether a capability reads, advises, or mutates.
type EffectClass string

const (
	EffectRead          EffectClass = "read"
	EffectAdvisory      EffectClass = "advisory"
	EffectWorldMutation EffectClass = "world-mutation"
)

// ExecutionMode describes how a capability reports completion.
type ExecutionMode string

const (
	ExecutionImmediate   ExecutionMode = "immediate"
	ExecutionQueued      ExecutionMode = "queued"
	ExecutionLongRunning ExecutionMode = "long-running"
)

// RiskLevel classifies the consequence of invoking a capability.
type RiskLevel string

const (
	RiskLow      RiskLevel = "low"
	RiskModerate RiskLevel = "moderate"
	RiskHigh     RiskLevel = "high"
	RiskCritical RiskLevel = "critical"
)

// CancellationMode describes how an in-flight capability can be stopped.
type CancellationMode string

const (
	CancellationUnsupported CancellationMode = "unsupported"
	CancellationCooperative CancellationMode = "cooperative"
	CancellationPreemptive  CancellationMode = "preemptive"
)

// CapabilityRef identifies one exact, namespaced capability version.
type CapabilityRef struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

// CapabilityDescriptor describes a host-local implementation. Discovery never
// authorizes use: each turn still needs an ActionOffer created by the game.
type CapabilityDescriptor struct {
	Capability         CapabilityRef     `json:"capability"`
	Description        string            `json:"description"`
	Input              Schema            `json:"input"`
	Output             Schema            `json:"output"`
	Effect             EffectClass       `json:"effect"`
	Execution          ExecutionMode     `json:"execution"`
	Risk               RiskLevel         `json:"risk"`
	RequiredDurability DurabilityProfile `json:"required_durability"`
	RequiredScopes     []string          `json:"required_scopes,omitempty"`
	TimeoutMS          uint32            `json:"timeout_ms"`
	MaxInputBytes      uint32            `json:"max_input_bytes"`
	MaxOutputBytes     uint32            `json:"max_output_bytes"`
	Cancellation       CancellationMode  `json:"cancellation"`
	Reversible         bool              `json:"reversible"`
	Digest             string            `json:"digest"`
}

// ActionOffer is a fully bound, game-authored candidate. Arguments are already
// selected and constrained by the host; a model chooses OfferID, not a method
// name or arbitrary arguments.
type ActionOffer struct {
	OfferID          string          `json:"offer_id"`
	ActorID          string          `json:"actor_id"`
	Capability       CapabilityRef   `json:"capability"`
	DescriptorDigest string          `json:"descriptor_digest"`
	Description      string          `json:"description"`
	Arguments        json.RawMessage `json:"arguments"`
	Targets          []HostRef       `json:"targets,omitempty"`
	Epoch            Epoch           `json:"epoch"`
	ObservationSeq   uint64          `json:"observation_seq"`
	ExpiresAtUnixMS  int64           `json:"expires_at_unix_ms"`
}

// ActionInvocation is a validated offer bound to a stable operation ID.
type ActionInvocation struct {
	OperationID      string          `json:"operation_id"`
	OfferID          string          `json:"offer_id"`
	ActorID          string          `json:"actor_id"`
	Capability       CapabilityRef   `json:"capability"`
	DescriptorDigest string          `json:"descriptor_digest"`
	Arguments        json.RawMessage `json:"arguments"`
	Targets          []HostRef       `json:"targets,omitempty"`
	ExpectedEpoch    Epoch           `json:"expected_epoch"`
	ObservationSeq   uint64          `json:"observation_seq"`
	DeadlineUnixMS   int64           `json:"deadline_unix_ms"`
}

// ActionRunStatus is the lifecycle state of a host-owned operation.
type ActionRunStatus string

const (
	ActionQueued         ActionRunStatus = "queued"
	ActionRunning        ActionRunStatus = "running"
	ActionSucceeded      ActionRunStatus = "succeeded"
	ActionFailed         ActionRunStatus = "failed"
	ActionCancelled      ActionRunStatus = "cancelled"
	ActionInterrupted    ActionRunStatus = "interrupted"
	ActionStale          ActionRunStatus = "stale"
	ActionOutcomeUnknown ActionRunStatus = "outcome-unknown"
)

// ActionRun reports monotonic progress for an accepted operation.
type ActionRun struct {
	OperationID   string          `json:"operation_id"`
	Status        ActionRunStatus `json:"status"`
	ProgressSeq   uint64          `json:"progress_seq"`
	Progress      uint32          `json:"progress"`
	UpdatedUnixMS int64           `json:"updated_unix_ms"`
	Message       string          `json:"message,omitempty"`
}

// ActionOutcome records the terminal effect observed by the host.
type ActionOutcome struct {
	OperationID    string          `json:"operation_id"`
	Status         ActionRunStatus `json:"status"`
	Code           string          `json:"code,omitempty"`
	Summary        string          `json:"summary"`
	Evidence       []HostRef       `json:"evidence,omitempty"`
	Epoch          Epoch           `json:"epoch"`
	WorldSeq       uint64          `json:"world_seq"`
	OccurredUnixMS int64           `json:"occurred_unix_ms"`
}
