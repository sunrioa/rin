// Package hostkit defines the engine-neutral ports and restartable workflow
// coordinator used by game Host adapters.
package hostkit

import (
	"context"

	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/protocol"
)

// RinTransport is the complete network boundary needed by Coordinator.
// Implementations may use HTTP, an embedded runtime, or a test double.
type RinTransport interface {
	SubmitProposal(context.Context, protocol.ProposeRequest) (protocol.ProposalJobSubmission, error)
	PollProposal(context.Context, string) (protocol.ProposalJob, error)
	ReportAction(context.Context, protocol.ReportActionRequest) (protocol.MutationResult, error)
}

// AuthorityDispatcher marshals work onto the game-owned authority thread.
type AuthorityDispatcher interface {
	Dispatch(context.Context, func(context.Context) error) error
}

// HostStateStore persists restartable workflow state. CompareAndSwap must write
// next only when expectedRevision matches. CommitEffect must invoke effect at
// most once and publish its returned state with the same revision check.
//
// A transactional-action Host implements CommitEffect in the same transaction
// as its game effect. An idempotent-action Host may apply by OperationID before
// persisting. Advisory Hosts may provide best-effort persistence but must not
// claim stronger durability.
type HostStateStore interface {
	Load(context.Context) (WorkflowState, error)
	CompareAndSwap(context.Context, uint64, WorkflowState) error
	CommitEffect(
		context.Context,
		uint64,
		func(context.Context) (WorkflowState, error),
	) error
}

// HostIdentity is an immutable snapshot of game-owned identity and time.
type HostIdentity struct {
	SessionID      string
	Epoch          host.Epoch
	Now            host.Timepoint
	Tick           int64
	ObservationSeq uint64
}

// IdentityProvider reads game-owned identity and creates stable identifiers.
// NewID must never derive IDs from display names, paths, or model output.
type IdentityProvider interface {
	Current(context.Context) (HostIdentity, error)
	NewID(context.Context, IDKind) (string, error)
}

// IDKind identifies the namespace in which a stable ID is requested.
type IDKind string

const (
	IDOperation IDKind = "operation"
	IDRequest   IDKind = "request"
	IDEvent     IDKind = "event"
	IDOutbox    IDKind = "outbox"
)

// ObservationMapper converts a bounded engine event into an immutable Rin
// observation DTO. Engine objects must not escape through this port.
type ObservationMapper interface {
	MapObservation(context.Context, any, HostIdentity) (protocol.ObserveRequest, error)
}

// CapabilityRegistry is the TOCTOU-safe subset of host.Registry required by
// the coordinator. *host.Registry implements this interface directly.
type CapabilityRegistry interface {
	Resolve(host.CapabilityRef) (host.CapabilityDescriptor, bool)
	NewInvocation(
		host.ActionOffer,
		string,
		host.Timepoint,
		host.Timepoint,
		host.Epoch,
	) (host.ActionInvocation, error)
	AuthorizeInvocation(host.ActionInvocation, host.Timepoint, host.Epoch) error
}

// ActionExecutor owns game-specific action execution. Execute and Cancel are
// always called through AuthorityDispatcher.
type ActionExecutor interface {
	Execute(context.Context, host.ActionInvocation) (host.ActionRun, *host.ActionOutcome, error)
	Cancel(context.Context, host.ActionInvocation, HostIdentity) (host.ActionRun, host.ActionOutcome, error)
}

// ArtifactPresenter resolves an immutable artifact into a game-owned
// presentation operation. It must not grant the artifact world authority.
type ArtifactPresenter interface {
	Present(context.Context, protocol.ArtifactRef) error
}
