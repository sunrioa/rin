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
