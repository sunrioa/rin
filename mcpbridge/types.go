package mcpbridge

import (
	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
)

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

type GetActorStateInput struct {
	HostID  string `json:"host_id" jsonschema:"host identifier returned by list_worlds"`
	WorldID string `json:"world_id" jsonschema:"world identifier returned by list_worlds"`
	ActorID string `json:"actor_id" jsonschema:"actor identifier returned by list_actors"`
}

type GetActorStateOutput struct {
	Actor Actor `json:"actor"`
}

type Actor struct {
	HostID                   string                         `json:"host_id"`
	WorldID                  string                         `json:"world_id"`
	ActorID                  string                         `json:"actor_id"`
	DisplayName              string                         `json:"display_name"`
	ObservationSeq           uint64                         `json:"observation_seq"`
	Epoch                    host.Epoch                     `json:"epoch"`
	DecisionAuthority        controlplane.DecisionAuthority `json:"decision_authority"`
	State                    map[string]any                 `json:"state"`
	Online                   bool                           `json:"online"`
	LeaseExpiresAtUnixMillis int64                          `json:"lease_expires_at_unix_millis"`
}

type ListActorOffersInput struct {
	HostID  string `json:"host_id" jsonschema:"host identifier returned by list_worlds"`
	WorldID string `json:"world_id" jsonschema:"world identifier returned by list_worlds"`
	ActorID string `json:"actor_id" jsonschema:"actor identifier returned by list_actors"`
}

type ListActorOffersOutput struct {
	Offers []Offer `json:"offers"`
}

type Offer struct {
	OfferID          string             `json:"offer_id"`
	DecisionWindowID string             `json:"decision_window_id"`
	ActorID          string             `json:"actor_id"`
	Capability       host.CapabilityRef `json:"capability"`
	DescriptorDigest string             `json:"descriptor_digest"`
	Description      string             `json:"description"`
	Arguments        map[string]any     `json:"arguments"`
	Targets          []host.HostRef     `json:"targets,omitempty"`
	ExpectedEpoch    host.Epoch         `json:"expected_epoch"`
	ObservationSeq   uint64             `json:"observation_seq"`
	Deadline         host.Timepoint     `json:"deadline"`
}

type SendActorMessageInput struct {
	RequestID string `json:"request_id" jsonschema:"stable idempotency identifier chosen by the caller"`
	HostID    string `json:"host_id" jsonschema:"host identifier returned by list_worlds"`
	WorldID   string `json:"world_id" jsonschema:"world identifier returned by list_worlds"`
	ActorID   string `json:"actor_id" jsonschema:"actor identifier returned by list_actors"`
	Text      string `json:"text" jsonschema:"plain message for the actor"`
}

type SendActorDirectiveInput struct {
	RequestID string `json:"request_id" jsonschema:"stable idempotency identifier chosen by the caller"`
	HostID    string `json:"host_id" jsonschema:"host identifier returned by list_worlds"`
	WorldID   string `json:"world_id" jsonschema:"world identifier returned by list_worlds"`
	ActorID   string `json:"actor_id" jsonschema:"actor identifier returned by list_actors"`
	Text      string `json:"text" jsonschema:"negotiable goal that the actor may refuse"`
}

type ExecuteActorOfferInput struct {
	RequestID string `json:"request_id" jsonschema:"stable idempotency identifier chosen by the caller"`
	HostID    string `json:"host_id" jsonschema:"host identifier returned by list_worlds"`
	WorldID   string `json:"world_id" jsonschema:"world identifier returned by list_worlds"`
	ActorID   string `json:"actor_id" jsonschema:"actor identifier returned by list_actors"`
	OfferID   string `json:"offer_id" jsonschema:"exact identifier returned by list_actor_offers"`
}

type GetOperationInput struct {
	OperationID string `json:"operation_id" jsonschema:"operation identifier returned by a write tool"`
}

type CancelOperationInput struct {
	OperationID string `json:"operation_id" jsonschema:"operation identifier returned by a write tool"`
}

type OperationOutput struct {
	Operation controlplane.OperationView `json:"operation"`
}
