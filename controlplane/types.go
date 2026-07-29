// Package controlplane coordinates bounded external control requests with
// authoritative game hosts. It owns connectivity and read models, never game
// world mutation.
package controlplane

import (
	"encoding/json"

	"github.com/sunrioa/rin/host"
)

const (
	ContractVersion = "rin.control/v1"

	ScopeActorRead       = "actor.read"
	ScopeActorConverse   = "actor.converse"
	ScopeActorDirect     = "actor.direct"
	ScopeActorExecute    = "actor.execute"
	ScopeOperationCancel = "operation.cancel"
	ScopeHostAdmin       = "host.admin"
)

// ControlKind identifies one bounded semantic request sent to a game Host.
type ControlKind string

const (
	ControlMessage   ControlKind = "message"
	ControlDirective ControlKind = "directive"
	ControlOffer     ControlKind = "offer"
)

// OperationStatus describes delivery and authoritative Host execution state.
type OperationStatus string

const (
	OperationQueued         OperationStatus = "queued"
	OperationDelivered      OperationStatus = "delivered"
	OperationAccepted       OperationStatus = "accepted"
	OperationRunning        OperationStatus = "running"
	OperationSucceeded      OperationStatus = "succeeded"
	OperationFailed         OperationStatus = "failed"
	OperationCancelled      OperationStatus = "cancelled"
	OperationInterrupted    OperationStatus = "interrupted"
	OperationStale          OperationStatus = "stale"
	OperationOutcomeUnknown OperationStatus = "outcome-unknown"
	OperationRejected       OperationStatus = "rejected"
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
	ActorID          string             `json:"actor_id"`
	OwnerPrincipalID string             `json:"owner_principal_id"`
	DisplayName      string             `json:"display_name"`
	ObservationSeq   uint64             `json:"observation_seq"`
	Epoch            host.Epoch         `json:"epoch"`
	State            json.RawMessage    `json:"state"`
	Offers           []host.ActionOffer `json:"offers,omitempty"`
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
	HostID               string          `json:"host_id"`
	WorldID              string          `json:"world_id"`
	ActorID              string          `json:"actor_id"`
	OwnerPrincipalID     string          `json:"owner_principal_id"`
	DisplayName          string          `json:"display_name"`
	ObservationSeq       uint64          `json:"observation_seq"`
	Epoch                host.Epoch      `json:"epoch"`
	State                json.RawMessage `json:"state"`
	Online               bool            `json:"online"`
	LeaseExpiresAtMillis int64           `json:"lease_expires_at_unix_millis"`
}

// ActorTextInput submits one message or negotiable directive to an Actor.
type ActorTextInput struct {
	RequestID string `json:"request_id"`
	HostID    string `json:"host_id"`
	WorldID   string `json:"world_id"`
	ActorID   string `json:"actor_id"`
	Text      string `json:"text"`
}

// ExecuteOfferInput selects an exact Host-published Offer without adding
// model-authored arguments.
type ExecuteOfferInput struct {
	RequestID string `json:"request_id"`
	HostID    string `json:"host_id"`
	WorldID   string `json:"world_id"`
	ActorID   string `json:"actor_id"`
	OfferID   string `json:"offer_id"`
}

// HostControlRequest is trusted queue data delivered to an authoritative Host.
type HostControlRequest struct {
	OperationID string                 `json:"operation_id"`
	RequestID   string                 `json:"request_id"`
	Principal   host.Principal         `json:"principal"`
	HostID      string                 `json:"host_id"`
	WorldID     string                 `json:"world_id"`
	ActorID     string                 `json:"actor_id"`
	Kind        ControlKind            `json:"kind"`
	Text        string                 `json:"text,omitempty"`
	Invocation  *host.ActionInvocation `json:"invocation,omitempty"`
	SubmittedAt int64                  `json:"submitted_at_unix_millis"`
}

// HostControlDelivery includes a stable request and its redelivery attempt.
type HostControlDelivery struct {
	Request         HostControlRequest `json:"request"`
	DeliveryAttempt uint32             `json:"delivery_attempt"`
}

// HostControlBatch is returned by one bounded Host poll.
type HostControlBatch struct {
	Requests      []HostControlDelivery `json:"requests"`
	Cancellations []string              `json:"cancellations"`
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
	OperationID      string              `json:"operation_id"`
	RequestID        string              `json:"request_id"`
	HostID           string              `json:"host_id"`
	WorldID          string              `json:"world_id"`
	ActorID          string              `json:"actor_id"`
	Kind             ControlKind         `json:"kind"`
	Status           OperationStatus     `json:"status"`
	CancelRequested  bool                `json:"cancel_requested"`
	DeliveryAttempts uint32              `json:"delivery_attempts"`
	Run              *host.ActionRun     `json:"run,omitempty"`
	Outcome          *host.ActionOutcome `json:"outcome,omitempty"`
	RejectionCode    string              `json:"rejection_code,omitempty"`
	RejectionMessage string              `json:"rejection_message,omitempty"`
	CreatedAt        int64               `json:"created_at_unix_millis"`
	UpdatedAt        int64               `json:"updated_at_unix_millis"`
}
