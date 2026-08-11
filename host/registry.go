package host

import "sync"

// Registry owns the host-local V2 capability catalog and active bindings. It
// is safe for concurrent discovery and validation; game object access remains
// the adapter's responsibility on its authority thread.
type Registry struct {
	mu       sync.RWMutex
	manifest HostManifest
	revision uint64
	specs    map[CapabilityRef]registeredCapabilitySpec
	bindings map[string]registeredBoundAction
}

// NewRegistry validates a manifest and creates an empty V2 registry.
func NewRegistry(manifest HostManifest) (*Registry, error) {
	if err := ValidateHostManifest(manifest); err != nil {
		return nil, err
	}
	return &Registry{
		manifest: manifest,
		specs:    make(map[CapabilityRef]registeredCapabilitySpec),
		bindings: make(map[string]registeredBoundAction),
	}, nil
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
