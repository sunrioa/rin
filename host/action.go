package host

import (
	"context"
	"encoding/json"
)

// SchemaRef identifies one immutable, host-published schema.
type SchemaRef struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

// ObservationFact is one bounded, host-authored scalar fact.
type ObservationFact struct {
	FactID  string          `json:"fact_id"`
	Kind    string          `json:"kind"`
	Subject *HostRef        `json:"subject,omitempty"`
	Tags    []string        `json:"tags,omitempty"`
	Value   json.RawMessage `json:"value"`
}

// OwnershipClass is a host-authored ownership classification. Unknown is a
// real value so policy can fail closed instead of guessing.
type OwnershipClass string

const (
	OwnershipUnknown    OwnershipClass = "unknown"
	OwnershipSystem     OwnershipClass = "system"
	OwnershipActor      OwnershipClass = "actor"
	OwnershipController OwnershipClass = "controller"
	OwnershipPlayer     OwnershipClass = "player"
	OwnershipShared     OwnershipClass = "shared"
	OwnershipUnowned    OwnershipClass = "unowned"
)

// ObservationResource describes a currently observed host resource without
// exposing an engine object or mutable handle.
type ObservationResource struct {
	Ref        HostRef         `json:"ref"`
	Kind       string          `json:"kind"`
	Tags       []string        `json:"tags,omitempty"`
	Ownership  OwnershipClass  `json:"ownership"`
	Scope      string          `json:"scope"`
	Quantity   uint64          `json:"quantity,omitempty"`
	Unit       string          `json:"unit,omitempty"`
	Attributes json.RawMessage `json:"attributes"`
}

// ObservationArtifact references host-controlled binary evidence. The
// contract intentionally carries no filesystem path or fetch URL.
type ObservationArtifact struct {
	ArtifactID string `json:"artifact_id"`
	Kind       string `json:"kind"`
	MediaType  string `json:"media_type"`
	SizeBytes  uint64 `json:"size_bytes"`
	SHA256     string `json:"sha256"`
}

// ObservationEnvelope is a bounded, epoch-bound snapshot produced by an
// authoritative adapter. Payload is validated against SchemaRef separately.
type ObservationEnvelope struct {
	ObservationID     string                `json:"observation_id"`
	HostID            string                `json:"host_id"`
	WorldID           string                `json:"world_id"`
	ActorID           string                `json:"actor_id"`
	Epoch             Epoch                 `json:"epoch"`
	Sequence          uint64                `json:"sequence"`
	ObservedAt        Timepoint             `json:"observed_at"`
	Schema            SchemaRef             `json:"schema_ref"`
	Payload           json.RawMessage       `json:"payload"`
	Facts             []ObservationFact     `json:"facts,omitempty"`
	Resources         []ObservationResource `json:"resources,omitempty"`
	Artifacts         []ObservationArtifact `json:"artifacts,omitempty"`
	ContinuationToken string                `json:"continuation_token,omitempty"`
}

// ObservationQuery asks an adapter for one bounded page of current facts.
type ObservationQuery struct {
	QueryID           string   `json:"query_id"`
	HostID            string   `json:"host_id"`
	WorldID           string   `json:"world_id"`
	ActorID           string   `json:"actor_id"`
	ExpectedEpoch     Epoch    `json:"expected_epoch"`
	AfterSequence     uint64   `json:"after_sequence,omitempty"`
	Kinds             []string `json:"kinds,omitempty"`
	Limit             uint32   `json:"limit"`
	ContinuationToken string   `json:"continuation_token,omitempty"`
}

// ObservationSource is the engine-neutral observation boundary implemented by
// a game adapter.
type ObservationSource interface {
	Observe(context.Context, ObservationQuery) (ObservationEnvelope, error)
}

// CapabilityKind distinguishes one host operation from a macro that may
// create auditable child operations.
type CapabilityKind string

const (
	CapabilityAtomic CapabilityKind = "atomic"
	CapabilityMacro  CapabilityKind = "macro"
)

// CapabilitySpec describes one V2 action type. Discovery is not
// authorization; effects are bound by the Host for each ActionRequest.
type CapabilitySpec struct {
	Capability  CapabilityRef `json:"capability"`
	Description string        `json:"description"`
	Input       Schema        `json:"input"`
	Output      Schema        `json:"output"`
	// EffectSchema validates the adapter-specific Attributes object on each
	// Effect. Core fields such as risk and ownership are validated by Rin.
	EffectSchema            Schema            `json:"effect_schema"`
	Kind                    CapabilityKind    `json:"kind"`
	Execution               ExecutionMode     `json:"execution"`
	Cancellation            CancellationMode  `json:"cancellation"`
	RiskFloor               RiskLevel         `json:"risk_floor"`
	RequiredDurability      DurabilityProfile `json:"required_durability"`
	RequiredScopes          []string          `json:"required_scopes,omitempty"`
	ExecutionBudget         Duration          `json:"execution_budget"`
	MaxInputBytes           uint32            `json:"max_input_bytes"`
	MaxOutputBytes          uint32            `json:"max_output_bytes"`
	MaxEffects              uint32            `json:"max_effects"`
	ProducesChildOperations bool              `json:"produces_child_operations"`
	Digest                  string            `json:"digest"`
}

// EffectOperation is the generic mutation verb used by gameplay policy.
type EffectOperation string

const (
	EffectOperationRead        EffectOperation = "read"
	EffectOperationCreate      EffectOperation = "create"
	EffectOperationUpdate      EffectOperation = "update"
	EffectOperationDelete      EffectOperation = "delete"
	EffectOperationTransfer    EffectOperation = "transfer"
	EffectOperationConsume     EffectOperation = "consume"
	EffectOperationExecute     EffectOperation = "execute"
	EffectOperationCommunicate EffectOperation = "communicate"
)

// Effect is a Host-authored preview of the consequences of a bound action.
// Controllers cannot declare ownership, reversibility, or risk.
type Effect struct {
	EffectID   string          `json:"effect_id"`
	Kind       string          `json:"kind"`
	Operation  EffectOperation `json:"operation"`
	Subject    *HostRef        `json:"subject_ref,omitempty"`
	Target     *HostRef        `json:"target_ref,omitempty"`
	Tags       []string        `json:"tags,omitempty"`
	Ownership  OwnershipClass  `json:"ownership"`
	Scope      string          `json:"scope"`
	Quantity   uint64          `json:"quantity,omitempty"`
	Unit       string          `json:"unit,omitempty"`
	Reversible bool            `json:"reversible"`
	Risk       RiskLevel       `json:"risk"`
	Attributes json.RawMessage `json:"attributes"`
}

// ActionRequest is the only action intent a controller may author.
type PlanStepRef struct {
	PlanID       string `json:"plan_id"`
	PlanRevision uint64 `json:"plan_revision"`
	StepID       string `json:"step_id"`
}

type ActionRequest struct {
	RequestID      string          `json:"request_id"`
	ControllerID   string          `json:"controller_id"`
	ActorID        string          `json:"actor_id"`
	Capability     CapabilityRef   `json:"capability"`
	SpecDigest     string          `json:"spec_digest"`
	Arguments      json.RawMessage `json:"arguments"`
	Targets        []HostRef       `json:"target_refs,omitempty"`
	ExpectedEpoch  Epoch           `json:"expected_epoch"`
	ObservationSeq uint64          `json:"observation_sequence"`
	TaskID         string          `json:"task_id,omitempty"`
	PlanStep       *PlanStepRef    `json:"plan_step_ref,omitempty"`
	IdempotencyKey string          `json:"idempotency_key"`
}

// BindingDraft is the authoritative adapter's resolution and effect preview.
// The Registry seals it into a BoundAction after schema and epoch checks.
type BindingDraft struct {
	BindingID       string    `json:"binding_id"`
	ResolvedTargets []HostRef `json:"resolved_targets,omitempty"`
	Effects         []Effect  `json:"effect_preview"`
	ValidUntil      Timepoint `json:"valid_until"`
}

// ActionBinder resolves HostRefs and previews authoritative effects. It must
// run inside the trusted game adapter, never inside a model provider.
type ActionBinder interface {
	Bind(context.Context, ActionRequest) (BindingDraft, error)
}

// BoundAction is an immutable Host binding of one controller request. Only an
// authoritative adapter may produce its effect preview.
type BoundAction struct {
	BindingID           string          `json:"binding_id"`
	RequestID           string          `json:"request_id"`
	RequestDigest       string          `json:"request_digest"`
	ControllerID        string          `json:"controller_id"`
	ActorID             string          `json:"actor_id"`
	Capability          CapabilityRef   `json:"capability"`
	SpecDigest          string          `json:"spec_digest"`
	NormalizedArguments json.RawMessage `json:"normalized_arguments"`
	RequestedTargets    []HostRef       `json:"requested_targets,omitempty"`
	ResolvedTargets     []HostRef       `json:"resolved_targets,omitempty"`
	ExpectedEpoch       Epoch           `json:"expected_epoch"`
	ObservationSeq      uint64          `json:"observation_sequence"`
	TaskID              string          `json:"task_id,omitempty"`
	PlanStep            *PlanStepRef    `json:"plan_step_ref,omitempty"`
	IdempotencyKey      string          `json:"idempotency_key"`
	Effects             []Effect        `json:"effect_preview"`
	EffectDigest        string          `json:"effect_digest"`
	BoundAt             Timepoint       `json:"bound_at"`
	ValidUntil          Timepoint       `json:"valid_until"`
}
