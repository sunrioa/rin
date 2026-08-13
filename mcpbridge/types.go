package mcpbridge

import (
	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/policy"
	"github.com/sunrioa/rin/skillapi"
	"github.com/sunrioa/rin/timeline"
)

type ListSkillsInput = skillapi.ListInput
type ListSkillsOutput = skillapi.ListOutput
type GetSkillInput = skillapi.GetInput
type GetSkillOutput = skillapi.GetOutput
type SaveExperienceAsSkillInput = skillapi.SaveInput
type ReloadSkillsInput struct{}
type ReloadSkillsOutput = skillapi.ReloadOutput

type ListWorldsInput struct{}

type ListWorldsOutput struct {
	Worlds []World `json:"worlds"`
}

type World struct {
	HostID                   string `json:"host_id"`
	WorldID                  string `json:"world_id"`
	DisplayName              string `json:"display_name"`
	Sequence                 uint64 `json:"sequence"`
	Online                   bool   `json:"online"`
	LeaseExpiresAtUnixMillis int64  `json:"lease_expires_at_unix_millis"`
}

type ListActorsInput struct {
	HostID  string `json:"host_id" jsonschema:"host identifier returned by list_worlds"`
	WorldID string `json:"world_id" jsonschema:"world identifier returned by list_worlds"`
}

type ListActorsOutput struct {
	Actors []Actor `json:"actors"`
}

type ActorTargetInput struct {
	HostID  string `json:"host_id" jsonschema:"host identifier returned by list_worlds"`
	WorldID string `json:"world_id" jsonschema:"world identifier returned by list_worlds"`
	ActorID string `json:"actor_id" jsonschema:"actor identifier returned by list_actors"`
}

func (input ActorTargetInput) target() controlplane.ActorControlTarget {
	return controlplane.ActorControlTarget{
		HostID: input.HostID, WorldID: input.WorldID, ActorID: input.ActorID,
	}
}

type GetActorStateInput = ActorTargetInput

type GetActorStateOutput struct {
	Actor Actor `json:"actor"`
}

type WaitActorUpdateInput struct {
	HostID                     string `json:"host_id" jsonschema:"host identifier returned by list_worlds"`
	WorldID                    string `json:"world_id" jsonschema:"world identifier returned by list_worlds"`
	ActorID                    string `json:"actor_id" jsonschema:"actor identifier returned by list_actors"`
	AfterObservationSeq        uint64 `json:"after_observation_seq" jsonschema:"last observation_seq returned for this actor"`
	AfterAuthorityRevision     uint64 `json:"after_authority_revision" jsonschema:"last decision_authority revision returned for this actor"`
	AfterControllerLeaseID     string `json:"after_controller_lease_id,omitempty" jsonschema:"last controller lease identifier or empty"`
	AfterEmergencyStopRevision uint64 `json:"after_emergency_stop_revision,omitempty" jsonschema:"last emergency stop revision"`
	WaitMillis                 uint32 `json:"wait_millis" jsonschema:"bounded wait from 0 through 25000 milliseconds"`
}

type WaitActorUpdateOutput struct {
	Actor   Actor `json:"actor"`
	Changed bool  `json:"changed"`
}

type Actor struct {
	HostID                   string                         `json:"host_id"`
	WorldID                  string                         `json:"world_id"`
	ActorID                  string                         `json:"actor_id"`
	OwnerPrincipalID         string                         `json:"owner_principal_id"`
	DisplayName              string                         `json:"display_name"`
	ObservationSeq           uint64                         `json:"observation_seq"`
	Epoch                    host.Epoch                     `json:"epoch"`
	DecisionAuthority        controlplane.DecisionAuthority `json:"decision_authority"`
	Controller               *controlplane.ControllerLease  `json:"controller_lease,omitempty"`
	EmergencyStopped         bool                           `json:"emergency_stopped"`
	EmergencyStopRevision    uint64                         `json:"emergency_stop_revision,omitempty"`
	State                    map[string]any                 `json:"state"`
	Online                   bool                           `json:"online"`
	LeaseExpiresAtUnixMillis int64                          `json:"lease_expires_at_unix_millis"`
}

type ObserveActorOutput struct {
	Observation Observation `json:"observation"`
}

// Observation mirrors the Host contract while exposing decoded JSON values.
// The MCP SDK otherwise describes json.RawMessage as a byte array and rejects
// the object values actually sent over JSON-RPC.
type Observation struct {
	ObservationID     string                     `json:"observation_id"`
	HostID            string                     `json:"host_id"`
	WorldID           string                     `json:"world_id"`
	ActorID           string                     `json:"actor_id"`
	Epoch             host.Epoch                 `json:"epoch"`
	Sequence          uint64                     `json:"sequence"`
	ObservedAt        host.Timepoint             `json:"observed_at"`
	Schema            host.SchemaRef             `json:"schema_ref"`
	Payload           map[string]any             `json:"payload"`
	Facts             []ObservationFact          `json:"facts,omitempty"`
	Resources         []ObservationResource      `json:"resources,omitempty"`
	Artifacts         []host.ObservationArtifact `json:"artifacts,omitempty"`
	ContinuationToken string                     `json:"continuation_token,omitempty"`
}

type ObservationFact struct {
	FactID  string        `json:"fact_id"`
	Kind    string        `json:"kind"`
	Subject *host.HostRef `json:"subject,omitempty"`
	Tags    []string      `json:"tags,omitempty"`
	Value   any           `json:"value"`
}

type ObservationResource struct {
	Ref        host.HostRef        `json:"ref"`
	Kind       string              `json:"kind"`
	Tags       []string            `json:"tags,omitempty"`
	Ownership  host.OwnershipClass `json:"ownership"`
	Scope      string              `json:"scope"`
	Quantity   uint64              `json:"quantity,omitempty"`
	Unit       string              `json:"unit,omitempty"`
	Attributes map[string]any      `json:"attributes"`
}

type CapabilitySummary struct {
	Capability              host.CapabilityRef    `json:"capability"`
	Description             string                `json:"description"`
	Kind                    host.CapabilityKind   `json:"kind"`
	Execution               host.ExecutionMode    `json:"execution"`
	Cancellation            host.CancellationMode `json:"cancellation"`
	RiskFloor               host.RiskLevel        `json:"risk_floor"`
	RequiredScopes          []string              `json:"required_scopes,omitempty"`
	ExecutionBudget         host.Duration         `json:"execution_budget"`
	MaxInputBytes           uint32                `json:"max_input_bytes"`
	MaxOutputBytes          uint32                `json:"max_output_bytes"`
	MaxEffects              uint32                `json:"max_effects"`
	ProducesChildOperations bool                  `json:"produces_child_operations"`
	Digest                  string                `json:"digest"`
}

type ListActorCapabilitiesOutput struct {
	Revision     uint64              `json:"revision"`
	Capabilities []CapabilitySummary `json:"capabilities"`
}

type DescribeActorCapabilityInput struct {
	ActorTargetInput
	CapabilityID      string `json:"capability_id" jsonschema:"exact capability ID returned by list_actor_capabilities"`
	CapabilityVersion string `json:"capability_version" jsonschema:"exact capability version returned by list_actor_capabilities"`
}

type DescribeActorCapabilityOutput struct {
	Capability CapabilitySpec `json:"capability"`
}

type Schema struct {
	Dialect  string         `json:"dialect"`
	Document map[string]any `json:"document"`
	SHA256   string         `json:"sha256"`
}

type CapabilitySpec struct {
	Capability              host.CapabilityRef     `json:"capability"`
	Description             string                 `json:"description"`
	Input                   Schema                 `json:"input"`
	Output                  Schema                 `json:"output"`
	EffectSchema            Schema                 `json:"effect_schema"`
	Kind                    host.CapabilityKind    `json:"kind"`
	Execution               host.ExecutionMode     `json:"execution"`
	Cancellation            host.CancellationMode  `json:"cancellation"`
	RiskFloor               host.RiskLevel         `json:"risk_floor"`
	RequiredDurability      host.DurabilityProfile `json:"required_durability"`
	RequiredScopes          []string               `json:"required_scopes,omitempty"`
	ExecutionBudget         host.Duration          `json:"execution_budget"`
	MaxInputBytes           uint32                 `json:"max_input_bytes"`
	MaxOutputBytes          uint32                 `json:"max_output_bytes"`
	MaxEffects              uint32                 `json:"max_effects"`
	ProducesChildOperations bool                   `json:"produces_child_operations"`
	Digest                  string                 `json:"digest"`
}

type AcquireActorControlInput struct {
	ActorTargetInput
	ControllerID   string `json:"controller_id" jsonschema:"stable controller identifier chosen by the caller"`
	LeaseTTLMillis uint32 `json:"lease_ttl_millis" jsonschema:"lease duration from 5000 through 300000 milliseconds"`
}

type RenewActorControlInput struct {
	ActorTargetInput
	LeaseID        string `json:"lease_id" jsonschema:"exact lease identifier returned by acquire_actor_control"`
	LeaseTTLMillis uint32 `json:"lease_ttl_millis" jsonschema:"lease duration from 5000 through 300000 milliseconds"`
}

type ReleaseActorControlInput struct {
	ActorTargetInput
	LeaseID string `json:"lease_id" jsonschema:"exact lease identifier returned by acquire_actor_control"`
}

type ControllerOutput struct {
	Controller controlplane.ControllerLease `json:"controller"`
}

type ReleaseActorControlOutput struct {
	Released bool `json:"released"`
}

type SubmitActorActionInput struct {
	ActorTargetInput
	RequestID         string         `json:"request_id" jsonschema:"stable request identifier chosen by the caller"`
	ControllerID      string         `json:"controller_id" jsonschema:"controller identifier bound to the current lease"`
	CapabilityID      string         `json:"capability_id" jsonschema:"exact capability ID returned by list_actor_capabilities"`
	CapabilityVersion string         `json:"capability_version" jsonschema:"exact capability version returned by list_actor_capabilities"`
	SpecDigest        string         `json:"spec_digest" jsonschema:"exact digest returned by list_actor_capabilities"`
	Arguments         map[string]any `json:"arguments" jsonschema:"arguments matching the described capability input schema"`
	Targets           []host.HostRef `json:"target_refs,omitempty" jsonschema:"opaque Host references copied from the current observation"`
	ExpectedEpoch     host.Epoch     `json:"expected_epoch" jsonschema:"exact epoch copied from the current actor observation"`
	ObservationSeq    uint64         `json:"observation_sequence" jsonschema:"exact sequence copied from the current actor observation"`
	TaskID            string         `json:"task_id,omitempty" jsonschema:"optional stable parent task identifier"`
	IdempotencyKey    string         `json:"idempotency_key" jsonschema:"stable retry key; reuse only for identical input"`
	ParentOperationID string         `json:"parent_operation_id,omitempty" jsonschema:"optional accepted or running child-producing macro operation with the same non-empty task_id"`
}

type ConfirmActionInput struct {
	OperationID string `json:"operation_id" jsonschema:"awaiting-confirmation operation identifier"`
}

type SetEmergencyStopInput struct {
	ActorTargetInput
	Active bool `json:"active" jsonschema:"true latches emergency stop; false clears it when the principal is allowed"`
}

type EmergencyStopOutput struct {
	EmergencyStop controlplane.ActorEmergencyStop `json:"emergency_stop"`
}

type GetOperationInput struct {
	OperationID string `json:"operation_id" jsonschema:"operation identifier returned by a write tool"`
}

type WaitOperationInput struct {
	OperationID string `json:"operation_id" jsonschema:"operation identifier returned by a write tool"`
	AfterCursor string `json:"after_cursor,omitempty" jsonschema:"opaque cursor copied unchanged from the last operation response"`
	WaitMillis  uint32 `json:"wait_millis" jsonschema:"bounded wait in milliseconds from 0 through 25000"`
}

type CancelOperationInput struct {
	OperationID string `json:"operation_id" jsonschema:"operation identifier returned by a write tool"`
}

type OperationOutput struct {
	Operation Operation `json:"operation"`
}

type OperationUpdateOutput struct {
	Operation Operation `json:"operation"`
	Changed   bool      `json:"changed"`
}

type GetTaskTimelineInput struct {
	TaskID      string `json:"task_id" jsonschema:"stable task identifier copied from submitted actions"`
	AfterCursor string `json:"after_cursor,omitempty" jsonschema:"opaque cursor copied unchanged from the last timeline response"`
	Limit       uint32 `json:"limit,omitempty" jsonschema:"maximum events from 1 through 256; zero uses the default"`
}

type WaitTaskTimelineInput struct {
	TaskID      string `json:"task_id" jsonschema:"stable task identifier copied from submitted actions"`
	AfterCursor string `json:"after_cursor,omitempty" jsonschema:"opaque cursor copied unchanged from the last timeline response"`
	Limit       uint32 `json:"limit,omitempty" jsonschema:"maximum events from 1 through 256; zero uses the default"`
	WaitMillis  uint32 `json:"wait_millis" jsonschema:"bounded wait in milliseconds from 0 through 25000"`
}

type TaskTimelineOutput struct {
	Timeline timeline.Page `json:"timeline"`
}

type TaskTimelineUpdateOutput struct {
	Timeline timeline.Page `json:"timeline"`
	Changed  bool          `json:"changed"`
}

type ActionRequest struct {
	RequestID      string             `json:"request_id"`
	ControllerID   string             `json:"controller_id"`
	ActorID        string             `json:"actor_id"`
	Capability     host.CapabilityRef `json:"capability"`
	SpecDigest     string             `json:"spec_digest"`
	Arguments      map[string]any     `json:"arguments"`
	Targets        []host.HostRef     `json:"target_refs,omitempty"`
	ExpectedEpoch  host.Epoch         `json:"expected_epoch"`
	ObservationSeq uint64             `json:"observation_sequence"`
	TaskID         string             `json:"task_id,omitempty"`
	IdempotencyKey string             `json:"idempotency_key"`
}

type Effect struct {
	EffectID   string               `json:"effect_id"`
	Kind       string               `json:"kind"`
	Operation  host.EffectOperation `json:"operation"`
	Subject    *host.HostRef        `json:"subject_ref,omitempty"`
	Target     *host.HostRef        `json:"target_ref,omitempty"`
	Tags       []string             `json:"tags,omitempty"`
	Ownership  host.OwnershipClass  `json:"ownership"`
	Scope      string               `json:"scope"`
	Quantity   uint64               `json:"quantity,omitempty"`
	Unit       string               `json:"unit,omitempty"`
	Reversible bool                 `json:"reversible"`
	Risk       host.RiskLevel       `json:"risk"`
	Attributes map[string]any       `json:"attributes"`
}

type BoundAction struct {
	BindingID           string             `json:"binding_id"`
	RequestID           string             `json:"request_id"`
	RequestDigest       string             `json:"request_digest"`
	ControllerID        string             `json:"controller_id"`
	ActorID             string             `json:"actor_id"`
	Capability          host.CapabilityRef `json:"capability"`
	SpecDigest          string             `json:"spec_digest"`
	NormalizedArguments map[string]any     `json:"normalized_arguments"`
	RequestedTargets    []host.HostRef     `json:"requested_targets,omitempty"`
	ResolvedTargets     []host.HostRef     `json:"resolved_targets,omitempty"`
	ExpectedEpoch       host.Epoch         `json:"expected_epoch"`
	ObservationSeq      uint64             `json:"observation_sequence"`
	TaskID              string             `json:"task_id,omitempty"`
	IdempotencyKey      string             `json:"idempotency_key"`
	Effects             []Effect           `json:"effect_preview"`
	EffectDigest        string             `json:"effect_digest"`
	BoundAt             host.Timepoint     `json:"bound_at"`
	ValidUntil          host.Timepoint     `json:"valid_until"`
}

type Operation struct {
	OperationID           string                       `json:"operation_id"`
	RequestID             string                       `json:"request_id"`
	HostID                string                       `json:"host_id"`
	WorldID               string                       `json:"world_id"`
	ActorID               string                       `json:"actor_id"`
	Kind                  controlplane.ControlKind     `json:"kind"`
	ControllerLeaseID     string                       `json:"controller_lease_id,omitempty"`
	ParentOperationID     string                       `json:"parent_operation_id,omitempty"`
	ChildOperationIDs     []string                     `json:"child_operation_ids,omitempty"`
	ActionRequest         *ActionRequest               `json:"action_request,omitempty"`
	BoundAction           *BoundAction                 `json:"bound_action,omitempty"`
	PolicyDecision        *policy.Decision             `json:"policy_decision,omitempty"`
	Status                controlplane.OperationStatus `json:"status"`
	Cursor                string                       `json:"cursor"`
	Terminal              bool                         `json:"terminal"`
	ReconciliationPending bool                         `json:"reconciliation_pending"`
	ExecutionConfirmed    bool                         `json:"execution_confirmed"`
	CancelRequested       bool                         `json:"cancel_requested"`
	DeliveryAttempts      uint32                       `json:"delivery_attempts"`
	Run                   *host.ActionRun              `json:"run,omitempty"`
	Outcome               *host.ActionOutcome          `json:"outcome,omitempty"`
	Output                map[string]any               `json:"output,omitempty"`
	RejectionCode         string                       `json:"rejection_code,omitempty"`
	RejectionMessage      string                       `json:"rejection_message,omitempty"`
	CreatedAt             int64                        `json:"created_at_unix_millis"`
	UpdatedAt             int64                        `json:"updated_at_unix_millis"`
}
