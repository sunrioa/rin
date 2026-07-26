package host

import (
	"fmt"
	"slices"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

type registeredCapability struct {
	descriptor CapabilityDescriptor
	input      *jsonschema.Schema
	output     *jsonschema.Schema
}

// RegistrySnapshot is an immutable, deterministically ordered registry view.
type RegistrySnapshot struct {
	Revision    uint64                 `json:"revision"`
	Descriptors []CapabilityDescriptor `json:"descriptors"`
}

// Registry owns the host-local, versioned capability catalog. It is safe for
// concurrent discovery and validation; game object access remains the adapter's
// responsibility and must occur on its authority thread.
type Registry struct {
	mu       sync.RWMutex
	manifest HostManifest
	revision uint64
	entries  map[CapabilityRef]registeredCapability
}

// NewRegistry validates a manifest and creates an empty capability registry.
func NewRegistry(manifest HostManifest) (*Registry, error) {
	if err := ValidateHostManifest(manifest); err != nil {
		return nil, err
	}
	return &Registry{
		manifest: manifest,
		entries:  make(map[CapabilityRef]registeredCapability),
	}, nil
}

// Register validates, seals, and registers one exact capability version.
func (registry *Registry) Register(descriptor CapabilityDescriptor) (CapabilityDescriptor, error) {
	sealed, input, output, err := prepareDescriptor(descriptor)
	if err != nil {
		return CapabilityDescriptor{}, err
	}
	if durabilityRank(sealed.RequiredDurability) >
		durabilityRank(registry.manifest.Durability.Profile) {
		return CapabilityDescriptor{}, invalid(
			"required_durability",
			"exceeds host durability "+string(registry.manifest.Durability.Profile),
		)
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if current, exists := registry.entries[sealed.Capability]; exists {
		if current.descriptor.Digest == sealed.Digest {
			return cloneDescriptor(current.descriptor), nil
		}
		return CapabilityDescriptor{}, invalid(
			"capability",
			"exact version is already registered with a different descriptor",
		)
	}
	registry.entries[sealed.Capability] = registeredCapability{
		descriptor: cloneDescriptor(sealed),
		input:      input,
		output:     output,
	}
	registry.revision++
	return cloneDescriptor(sealed), nil
}

// Unregister revokes one exact capability version.
func (registry *Registry) Unregister(ref CapabilityRef) bool {
	if err := ref.Validate("capability"); err != nil {
		return false
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if _, exists := registry.entries[ref]; !exists {
		return false
	}
	delete(registry.entries, ref)
	registry.revision++
	return true
}

// Resolve returns a defensive copy of one active descriptor.
func (registry *Registry) Resolve(ref CapabilityRef) (CapabilityDescriptor, bool) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	registered, exists := registry.entries[ref]
	if !exists {
		return CapabilityDescriptor{}, false
	}
	return cloneDescriptor(registered.descriptor), true
}

// Snapshot returns all active descriptors and the current registry revision.
func (registry *Registry) Snapshot() RegistrySnapshot {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	descriptors := make([]CapabilityDescriptor, 0, len(registry.entries))
	for _, registered := range registry.entries {
		descriptors = append(descriptors, cloneDescriptor(registered.descriptor))
	}
	slices.SortFunc(descriptors, func(left, right CapabilityDescriptor) int {
		if left.Capability.ID != right.Capability.ID {
			if left.Capability.ID < right.Capability.ID {
				return -1
			}
			return 1
		}
		if left.Capability.Version < right.Capability.Version {
			return -1
		}
		if left.Capability.Version > right.Capability.Version {
			return 1
		}
		return 0
	})
	return RegistrySnapshot{Revision: registry.revision, Descriptors: descriptors}
}

// ValidateOffer checks a game-authored offer against current host state.
func (registry *Registry) ValidateOffer(
	offer ActionOffer,
	now Timepoint,
	currentEpoch Epoch,
) error {
	_, err := registry.validateOffer(offer, now, currentEpoch)
	return err
}

func (registry *Registry) validateOffer(
	offer ActionOffer,
	now Timepoint,
	currentEpoch Epoch,
) (registeredCapability, error) {
	if err := ValidateActionOffer(offer); err != nil {
		return registeredCapability{}, err
	}
	if err := now.Validate("now"); err != nil {
		return registeredCapability{}, err
	}
	if now.Clock != offer.Deadline.Clock {
		return registeredCapability{}, invalid("deadline.clock", "must match the current host clock")
	}
	if now.Value >= offer.Deadline.Value {
		return registeredCapability{}, invalid("deadline", "offer has expired")
	}
	if offer.ExpectedEpoch != currentEpoch {
		return registeredCapability{}, invalid("expected_epoch", "offer belongs to a stale host epoch")
	}
	registered, err := registry.lookupForExecution(
		offer.Capability,
		offer.DescriptorDigest,
	)
	if err != nil {
		return registeredCapability{}, err
	}
	if len(offer.Arguments) > int(registered.descriptor.MaxInputBytes) {
		return registeredCapability{}, invalid("arguments", "exceeds capability input limit")
	}
	instance, err := decodeJSON(offer.Arguments)
	if err != nil {
		return registeredCapability{}, invalid("arguments", err.Error())
	}
	if err := registered.input.Validate(instance); err != nil {
		return registeredCapability{}, invalid("arguments", err.Error())
	}
	return registered, nil
}

// NewInvocation validates an offer and binds it to a stable operation ID.
func (registry *Registry) NewInvocation(
	offer ActionOffer,
	operationID string,
	now Timepoint,
	deadline Timepoint,
	currentEpoch Epoch,
) (ActionInvocation, error) {
	registered, err := registry.validateOffer(offer, now, currentEpoch)
	if err != nil {
		return ActionInvocation{}, err
	}
	if err := validateHostID("operation_id", operationID, false); err != nil {
		return ActionInvocation{}, err
	}
	if err := deadline.Validate("deadline"); err != nil {
		return ActionInvocation{}, err
	}
	if deadline.Clock != now.Clock ||
		registered.descriptor.ExecutionBudget.Clock != now.Clock {
		return ActionInvocation{}, invalid(
			"deadline.clock",
			"must match the current host clock and capability execution budget",
		)
	}
	budget := int64(registered.descriptor.ExecutionBudget.Value)
	if now.Value > maxInteroperableInteger-budget {
		return ActionInvocation{}, invalid("now.value", "cannot add execution budget safely")
	}
	latestDeadline := now.Value + budget
	if deadline.Value <= now.Value || deadline.Value > latestDeadline ||
		deadline.Value > offer.Deadline.Value {
		return ActionInvocation{}, invalid(
			"deadline",
			"must be after now and within the capability budget and offer deadline",
		)
	}
	invocation := ActionInvocation{
		OperationID:      operationID,
		OfferID:          offer.OfferID,
		DecisionWindowID: offer.DecisionWindowID,
		ActorID:          offer.ActorID,
		Capability:       offer.Capability,
		DescriptorDigest: offer.DescriptorDigest,
		Arguments:        append([]byte(nil), offer.Arguments...),
		Targets:          cloneRefs(offer.Targets),
		ExpectedEpoch:    offer.ExpectedEpoch,
		ObservationSeq:   offer.ObservationSeq,
		Deadline:         deadline,
	}
	return invocation, nil
}

// AuthorizeInvocation performs the final local TOCTOU check immediately before
// an adapter dispatches onto the authority thread.
func (registry *Registry) AuthorizeInvocation(
	invocation ActionInvocation,
	now Timepoint,
	currentEpoch Epoch,
) error {
	if err := ValidateActionInvocation(invocation); err != nil {
		return err
	}
	if err := now.Validate("now"); err != nil {
		return err
	}
	if now.Clock != invocation.Deadline.Clock {
		return invalid("deadline.clock", "must match the current host clock")
	}
	if now.Value >= invocation.Deadline.Value {
		return invalid("deadline", "invocation has expired")
	}
	if invocation.ExpectedEpoch != currentEpoch {
		return invalid("expected_epoch", "invocation belongs to a stale host epoch")
	}
	registered, err := registry.lookupForExecution(
		invocation.Capability,
		invocation.DescriptorDigest,
	)
	if err != nil {
		return err
	}
	if uint32(len(invocation.Arguments)) > registered.descriptor.MaxInputBytes {
		return invalid("arguments", "exceeds capability input limit")
	}
	instance, err := decodeJSON(invocation.Arguments)
	if err != nil {
		return invalid("arguments", err.Error())
	}
	if err := registered.input.Validate(instance); err != nil {
		return invalid("arguments", err.Error())
	}
	return nil
}

// ValidateOutput checks a capability result against its active descriptor.
func (registry *Registry) ValidateOutput(ref CapabilityRef, digest string, document []byte) error {
	registered, err := registry.lookupForExecution(ref, digest)
	if err != nil {
		return err
	}
	if len(document) > int(registered.descriptor.MaxOutputBytes) {
		return invalid("output", "exceeds capability output limit")
	}
	instance, err := decodeJSON(document)
	if err != nil {
		return invalid("output", err.Error())
	}
	if err := registered.output.Validate(instance); err != nil {
		return invalid("output", err.Error())
	}
	return nil
}

func (registry *Registry) lookupForExecution(
	ref CapabilityRef,
	digest string,
) (registeredCapability, error) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	registered, exists := registry.entries[ref]
	if !exists {
		return registeredCapability{}, invalid(
			"capability",
			fmt.Sprintf("%s@%s is not registered", ref.ID, ref.Version),
		)
	}
	if registered.descriptor.Digest != digest {
		return registeredCapability{}, invalid(
			"descriptor_digest",
			"does not match the active capability descriptor",
		)
	}
	return registered, nil
}

func durabilityRank(profile DurabilityProfile) int {
	switch profile {
	case DurabilityAdvisory:
		return 0
	case DurabilityIdempotent:
		return 1
	case DurabilityTransactional:
		return 2
	default:
		return -1
	}
}

func cloneRefs(refs []HostRef) []HostRef {
	return append([]HostRef(nil), refs...)
}
