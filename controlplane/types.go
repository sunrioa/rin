// Package controlplane coordinates bounded external control requests with
// authoritative game hosts. It owns connectivity and read models, never game
// world mutation.
package controlplane

import (
	"encoding/json"

	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/policy"
)

const (
	ContractVersion = "rin.control/v2"

	ScopeActorRead       = "actor.read"
	ScopeActorConverse   = "actor.converse"
	ScopeActorDirect     = "actor.direct"
	ScopeActorSpeak      = "actor.speak"
	ScopeActorExecute    = "actor.execute"
	ScopeActorControl    = "actor.control"
	ScopeOperationCancel = "operation.cancel"
	ScopeHostAdmin       = "host.admin"
)

// ControlKind identifies the sole mutation request sent to a game Host.
type ControlKind string

const (
	ControlAction ControlKind = "action"
)

// DecisionSource identifies the one deliberative controller currently allowed
// to choose autonomous Actor actions. Real-time safety reflexes remain owned by
// the game Host regardless of this value.
type DecisionSource string

const (
	DecisionInternal DecisionSource = "internal"
	DecisionExternal DecisionSource = "external"
)

// PersonaMode controls how an external controller presents through an Actor.
// Character-bound controllers preserve the Host-authored role; agent-avatar
// controllers intentionally present the external Agent's own personality.
type PersonaMode string

const (
	PersonaCharacterBound PersonaMode = "character-bound"
	PersonaAgentAvatar    PersonaMode = "agent-avatar"
)

// DecisionAuthority is a Host-authored projection, not a second source of
// truth. The Host persists it with the game save and increments Revision on
// every controller transition.
type DecisionAuthority struct {
	Source                DecisionSource `json:"source"`
	ControllerPrincipalID string         `json:"controller_principal_id,omitempty"`
	Revision              uint64         `json:"revision"`
	PersonaMode           PersonaMode    `json:"persona_mode"`
}

// ActorControlTarget identifies one Actor without exposing an engine object.
type ActorControlTarget struct {
	HostID  string `json:"host_id"`
	WorldID string `json:"world_id"`
	ActorID string `json:"actor_id"`
}

// AcquireControllerInput requests exclusive deliberative control of one Actor.
// The Host-authored DecisionAuthority still decides whether an internal or
// external controller is eligible to acquire the lease.
type AcquireControllerInput struct {
	ActorControlTarget
	ControllerID   string `json:"controller_id"`
	LeaseTTLMillis uint32 `json:"lease_ttl_millis"`
}

// ControllerLease is the exclusive, epoch-bound right to author ActionRequest
// values for an Actor. It never grants gameplay effects by itself.
type ControllerLease struct {
	LeaseID              string         `json:"lease_id"`
	ControllerID         string         `json:"controller_id"`
	PrincipalID          string         `json:"principal_id"`
	HostID               string         `json:"host_id"`
	WorldID              string         `json:"world_id"`
	ActorID              string         `json:"actor_id"`
	Source               DecisionSource `json:"source"`
	PersonaMode          PersonaMode    `json:"persona_mode"`
	AuthorityRevision    uint64         `json:"authority_revision"`
	Epoch                host.Epoch     `json:"epoch"`
	AcquiredAtUnixMillis int64          `json:"acquired_at_unix_millis"`
	ExpiresAtUnixMillis  int64          `json:"expires_at_unix_millis"`
}

// ActorEmergencyStop is an owner-controlled safety latch. It survives
// controller changes and blocks every deliberative source equally.
type ActorEmergencyStop struct {
	ActorControlTarget
	Active               bool   `json:"active"`
	Revision             uint64 `json:"revision"`
	UpdatedByPrincipalID string `json:"updated_by_principal_id"`
	UpdatedAtUnixMillis  int64  `json:"updated_at_unix_millis"`
}

// OperationStatus describes delivery and authoritative Host execution state.
type OperationStatus string

const (
	OperationQueued               OperationStatus = "queued"
	OperationAwaitingConfirmation OperationStatus = "awaiting-confirmation"
	OperationDelivered            OperationStatus = "delivered"
	OperationAccepted             OperationStatus = "accepted"
	OperationRunning              OperationStatus = "running"
	OperationSucceeded            OperationStatus = "succeeded"
	OperationFailed               OperationStatus = "failed"
	OperationCancelled            OperationStatus = "cancelled"
	OperationInterrupted          OperationStatus = "interrupted"
	OperationStale                OperationStatus = "stale"
	OperationOutcomeUnknown       OperationStatus = "outcome-unknown"
	OperationRejected             OperationStatus = "rejected"
)

// HostRegistration starts or resumes one authoritative host connection.
type HostRegistration struct {
	ContractVersion string            `json:"contract_version"`
	HostID          string            `json:"host_id"`
	InstanceID      string            `json:"instance_id"`
	Manifest        host.HostManifest `json:"manifest"`
	LeaseTTLMillis  uint32            `json:"lease_ttl_millis"`
}

// HostLease proves that one host instance currently owns its publication slot.
type HostLease struct {
	HostID              string `json:"host_id"`
	InstanceID          string `json:"instance_id"`
	LeaseID             string `json:"lease_id"`
	ExpiresAtUnixMillis int64  `json:"expires_at_unix_millis"`
}

// WorldPublication atomically replaces one host's read model for a world.
type WorldPublication struct {
	WorldID     string             `json:"world_id"`
	DisplayName string             `json:"display_name"`
	Sequence    uint64             `json:"sequence"`
	Actors      []ActorPublication `json:"actors"`
}

// ActorPublication contains only host-approved, externally visible state.
type ActorPublication struct {
	ActorID          string                    `json:"actor_id"`
	OwnerPrincipalID string                    `json:"owner_principal_id"`
	DisplayName      string                    `json:"display_name"`
	ObservationSeq   uint64                    `json:"observation_seq"`
	Epoch            host.Epoch                `json:"epoch"`
	Authority        *DecisionAuthority        `json:"decision_authority"`
	State            json.RawMessage           `json:"state"`
	Observation      *host.ObservationEnvelope `json:"observation,omitempty"`
	Capabilities     *host.CapabilitySnapshot  `json:"capabilities,omitempty"`
}

// WorldView is a principal-filtered world read model.
type WorldView struct {
	HostID               string `json:"host_id"`
	WorldID              string `json:"world_id"`
	DisplayName          string `json:"display_name"`
	Sequence             uint64 `json:"sequence"`
	Online               bool   `json:"online"`
	LeaseExpiresAtMillis int64  `json:"lease_expires_at_unix_millis"`
}

// ActorView is a defensive copy of one principal-visible actor snapshot.
type ActorView struct {
	HostID                string            `json:"host_id"`
	WorldID               string            `json:"world_id"`
	ActorID               string            `json:"actor_id"`
	OwnerPrincipalID      string            `json:"owner_principal_id"`
	DisplayName           string            `json:"display_name"`
	ObservationSeq        uint64            `json:"observation_seq"`
	Epoch                 host.Epoch        `json:"epoch"`
	Authority             DecisionAuthority `json:"decision_authority"`
	Controller            *ControllerLease  `json:"controller_lease,omitempty"`
	EmergencyStopped      bool              `json:"emergency_stopped"`
	EmergencyStopRevision uint64            `json:"emergency_stop_revision,omitempty"`
	State                 json.RawMessage   `json:"state"`
	Online                bool              `json:"online"`
	LeaseExpiresAtMillis  int64             `json:"lease_expires_at_unix_millis"`
}

// WaitActorInput identifies the last actor cursor observed by a client.
// Waiting is bounded and returns the same principal-filtered ActorView used by
// ordinary reads.
type WaitActorInput struct {
	HostID                     string `json:"host_id"`
	WorldID                    string `json:"world_id"`
	ActorID                    string `json:"actor_id"`
	AfterObservationSeq        uint64 `json:"after_observation_seq"`
	AfterAuthorityRevision     uint64 `json:"after_authority_revision"`
	AfterControllerLeaseID     string `json:"after_controller_lease_id,omitempty"`
	AfterEmergencyStopRevision uint64 `json:"after_emergency_stop_revision,omitempty"`
	WaitMillis                 uint32 `json:"wait_millis"`
}

// ActorUpdate reports whether the actor cursor changed before the bounded wait
// elapsed. Actor always contains the latest visible snapshot.
type ActorUpdate struct {
	Actor   ActorView `json:"actor"`
	Changed bool      `json:"changed"`
}

// ControlBinding records the exact Host timeline and observation that were
// visible when an external request was accepted by the Control Plane.
type ControlBinding struct {
	Epoch             host.Epoch `json:"epoch"`
	ObservationSeq    uint64     `json:"observation_seq"`
	AuthorityRevision uint64     `json:"authority_revision"`
	ControllerLeaseID string     `json:"controller_lease_id,omitempty"`
}

// HostControlRequest is trusted queue data delivered to an authoritative Host.
type HostControlRequest struct {
	OperationID       string              `json:"operation_id"`
	RequestID         string              `json:"request_id"`
	Principal         host.Principal      `json:"principal"`
	HostID            string              `json:"host_id"`
	WorldID           string              `json:"world_id"`
	ActorID           string              `json:"actor_id"`
	Kind              ControlKind         `json:"kind"`
	Binding           *ControlBinding     `json:"binding"`
	ActionRequest     *host.ActionRequest `json:"action_request"`
	BoundAction       *host.BoundAction   `json:"bound_action"`
	PolicyDecision    *policy.Decision    `json:"policy_decision"`
	ParentOperationID string              `json:"parent_operation_id,omitempty"`
	SubmittedAt       int64               `json:"submitted_at_unix_millis"`
}

// HostControlDelivery includes a stable request and its redelivery attempt.
type HostControlDelivery struct {
	Request         HostControlRequest `json:"request"`
	DeliveryAttempt uint32             `json:"delivery_attempt"`
}

// HostControlBatch is returned by one bounded Host poll.
type HostControlBatch struct {
	GatewayRequests []HostGatewayDelivery `json:"gateway_requests"`
	Requests        []HostControlDelivery `json:"requests"`
	Cancellations   []string              `json:"cancellations"`
}

// HostGatewayKind identifies one read-only, pre-execution request that must be
// answered by the authoritative game adapter. These requests never mutate the
// game world and never become Operations by themselves.
type HostGatewayKind string

const (
	HostGatewayBind     HostGatewayKind = "bind"
	HostGatewaySnapshot HostGatewayKind = "snapshot"
)

// HostGatewayRequest asks the connected Host to bind one controller request
// or return a fresh authority-thread snapshot. GatewayRequestID is an opaque
// delivery identifier and must be used for Host-side deduplication.
type HostGatewayRequest struct {
	GatewayRequestID string              `json:"gateway_request_id"`
	Kind             HostGatewayKind     `json:"kind"`
	Target           ActorControlTarget  `json:"target"`
	ActionRequest    *host.ActionRequest `json:"action_request,omitempty"`
	SubmittedAt      int64               `json:"submitted_at_unix_millis"`
}

// HostGatewayDelivery carries a stable request and its redelivery attempt.
// A Host may receive the same GatewayRequestID more than once until it reports
// a result and therefore must replay its cached result instead of rebinding.
type HostGatewayDelivery struct {
	Request         HostGatewayRequest `json:"request"`
	DeliveryAttempt uint32             `json:"delivery_attempt"`
}

// HostGatewayResult completes one pre-execution request. Exactly one of
// Binding, Snapshot, or ErrorCode is present according to the request kind.
type HostGatewayResult struct {
	GatewayRequestID string               `json:"gateway_request_id"`
	Binding          *ActionBindingResult `json:"binding,omitempty"`
	Snapshot         *ActionHostSnapshot  `json:"snapshot,omitempty"`
	ErrorCode        string               `json:"error_code,omitempty"`
	ErrorMessage     string               `json:"error_message,omitempty"`
}

// HostAcknowledgement records whether a delivered request was accepted.
type HostAcknowledgement struct {
	OperationID string `json:"operation_id"`
	Accepted    bool   `json:"accepted"`
	Code        string `json:"code,omitempty"`
	Message     string `json:"message,omitempty"`
}

// OperationView is the principal-safe state of one Control Operation.
type OperationView struct {
	OperationID           string              `json:"operation_id"`
	RequestID             string              `json:"request_id"`
	HostID                string              `json:"host_id"`
	WorldID               string              `json:"world_id"`
	ActorID               string              `json:"actor_id"`
	Kind                  ControlKind         `json:"kind"`
	ControllerLeaseID     string              `json:"controller_lease_id,omitempty"`
	ParentOperationID     string              `json:"parent_operation_id,omitempty"`
	ChildOperationIDs     []string            `json:"child_operation_ids,omitempty"`
	ActionRequest         *host.ActionRequest `json:"action_request,omitempty"`
	BoundAction           *host.BoundAction   `json:"bound_action,omitempty"`
	PolicyDecision        *policy.Decision    `json:"policy_decision,omitempty"`
	Status                OperationStatus     `json:"status"`
	Cursor                string              `json:"cursor"`
	Terminal              bool                `json:"terminal"`
	ReconciliationPending bool                `json:"reconciliation_pending"`
	ExecutionConfirmed    bool                `json:"execution_confirmed"`
	CancelRequested       bool                `json:"cancel_requested"`
	DeliveryAttempts      uint32              `json:"delivery_attempts"`
	Run                   *host.ActionRun     `json:"run,omitempty"`
	Outcome               *host.ActionOutcome `json:"outcome,omitempty"`
	Output                map[string]any      `json:"output,omitempty"`
	RejectionCode         string              `json:"rejection_code,omitempty"`
	RejectionMessage      string              `json:"rejection_message,omitempty"`
	CreatedAt             int64               `json:"created_at_unix_millis"`
	UpdatedAt             int64               `json:"updated_at_unix_millis"`
}

// WaitOperationInput identifies the last operation revision observed by a
// client. Cursor is opaque and must be copied from OperationView unchanged.
type WaitOperationInput struct {
	OperationID string `json:"operation_id"`
	AfterCursor string `json:"after_cursor,omitempty"`
	WaitMillis  uint32 `json:"wait_millis"`
}

// DescribeCapabilityInput identifies one exact capability published for an
// Actor. Discovery never grants permission to execute that capability.
type DescribeCapabilityInput struct {
	ActorControlTarget
	Capability host.CapabilityRef `json:"capability"`
}

// RenewControllerInput renews one exact controller lease.
type RenewControllerInput struct {
	ActorControlTarget
	LeaseID        string `json:"lease_id"`
	LeaseTTLMillis uint32 `json:"lease_ttl_millis"`
}

// ReleaseControllerInput releases one exact controller lease.
type ReleaseControllerInput struct {
	ActorControlTarget
	LeaseID string `json:"lease_id"`
}

// SetEmergencyStopInput changes the owner-controlled safety latch.
type SetEmergencyStopInput struct {
	ActorControlTarget
	Active bool `json:"active"`
}

// OperationUpdate reports whether an operation changed during a bounded wait.
// Operation always contains the latest principal-safe view.
type OperationUpdate struct {
	Operation OperationView `json:"operation"`
	Changed   bool          `json:"changed"`
}
