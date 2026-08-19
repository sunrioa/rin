package cognition

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
)

type PersonaBoundary struct {
	BoundaryID string `json:"boundary_id"`
	Rule       string `json:"rule"`
	Response   string `json:"response"`
}

type RelationshipStance struct {
	Role   string `json:"role"`
	Stance string `json:"stance"`
}

type InitiativePolicy struct {
	Enabled               bool     `json:"enabled"`
	CooldownMillis        uint32   `json:"cooldown_millis"`
	MaxConsecutiveActions uint32   `json:"max_consecutive_actions"`
	Triggers              []string `json:"triggers,omitempty"`
}

// PersonaProfile is character identity and presentation configuration. It has
// no authority, scopes, policy rules, or executable hooks.
type PersonaProfile struct {
	PersonaID         string               `json:"persona_id"`
	Version           string               `json:"version"`
	Identity          string               `json:"identity"`
	Traits            []string             `json:"traits,omitempty"`
	Values            []string             `json:"values,omitempty"`
	Voice             string               `json:"voice,omitempty"`
	Boundaries        []PersonaBoundary    `json:"boundaries,omitempty"`
	Relationships     []RelationshipStance `json:"relationship_stances,omitempty"`
	Initiative        InitiativePolicy     `json:"initiative_policy"`
	PresentationRules []string             `json:"presentation_rules,omitempty"`
}

type PersonaBinding struct {
	ActorID      string `json:"actor_id"`
	ControllerID string `json:"controller_id,omitempty"`
	PersonaID    string `json:"persona_id"`
	Version      string `json:"version"`
}

type PersonaRequest struct {
	ActorID      string `json:"actor_id"`
	ControllerID string `json:"controller_id"`
}

type PersonaSnapshot struct {
	Revision uint64           `json:"revision"`
	Profiles []PersonaProfile `json:"profiles"`
	Bindings []PersonaBinding `json:"bindings"`
}

type PersonaProvider interface {
	Load(context.Context, PersonaRequest) (PersonaProfile, error)
	Snapshot(context.Context) (PersonaSnapshot, error)
	Health(context.Context) ProviderHealth
}

var ErrPersonaConflict = errors.New("persona snapshot revision conflict")

// PersonaStore adds one revision-checked mutation to PersonaProvider. Callers
// edit a snapshot as a whole so profile and binding changes become visible
// atomically and can never leave a binding pointing at a missing profile.
type PersonaStore interface {
	PersonaProvider
	CompareAndSwap(context.Context, PersonaSnapshot) (PersonaSnapshot, error)
}

type LocalPersonaProvider struct {
	mu       sync.RWMutex
	revision uint64
	profiles map[string]PersonaProfile
	bindings map[string]PersonaBinding
}

func NewLocalPersonaProvider(
	profiles []PersonaProfile,
	bindings []PersonaBinding,
) (*LocalPersonaProvider, error) {
	provider := &LocalPersonaProvider{
		revision: 1,
		profiles: make(map[string]PersonaProfile, len(profiles)),
		bindings: make(map[string]PersonaBinding, len(bindings)),
	}
	for index, profile := range profiles {
		sealed, err := SealPersonaProfile(profile)
		if err != nil {
			return nil, fmt.Errorf("profiles[%d]: %w", index, err)
		}
		key := providerKey(sealed.PersonaID, sealed.Version)
		if _, exists := provider.profiles[key]; exists {
			return nil, errors.New("persona profiles contain a duplicate id and version")
		}
		provider.profiles[key] = sealed
	}
	for index, binding := range bindings {
		if err := validatePersonaBinding(binding); err != nil {
			return nil, fmt.Errorf("bindings[%d]: %w", index, err)
		}
		if _, exists := provider.profiles[providerKey(binding.PersonaID, binding.Version)]; !exists {
			return nil, fmt.Errorf("bindings[%d]: %w", index, ErrProviderNotFound)
		}
		key := personaBindingKey(binding.ActorID, binding.ControllerID)
		if _, exists := provider.bindings[key]; exists {
			return nil, errors.New("persona bindings contain a duplicate selector")
		}
		provider.bindings[key] = binding
	}
	return provider, nil
}

func RestoreLocalPersonaProvider(snapshot PersonaSnapshot) (*LocalPersonaProvider, error) {
	if snapshot.Revision == 0 {
		return nil, errors.New("persona snapshot revision must be positive")
	}
	provider, err := NewLocalPersonaProvider(snapshot.Profiles, snapshot.Bindings)
	if err != nil {
		return nil, err
	}
	provider.revision = snapshot.Revision
	return provider, nil
}

func (provider *LocalPersonaProvider) Load(
	ctx context.Context,
	request PersonaRequest,
) (PersonaProfile, error) {
	if err := requireContext(ctx); err != nil {
		return PersonaProfile{}, err
	}
	if err := ctx.Err(); err != nil {
		return PersonaProfile{}, err
	}
	if err := validateProviderID("actor_id", request.ActorID); err != nil {
		return PersonaProfile{}, err
	}
	if err := validateProviderID("controller_id", request.ControllerID); err != nil {
		return PersonaProfile{}, err
	}
	provider.mu.RLock()
	defer provider.mu.RUnlock()
	binding, exists := provider.bindings[personaBindingKey(request.ActorID, request.ControllerID)]
	if !exists {
		binding, exists = provider.bindings[personaBindingKey(request.ActorID, "")]
	}
	if !exists {
		binding, exists = provider.bindings[personaBindingKey("", "")]
	}
	if !exists {
		return PersonaProfile{}, ErrProviderNotFound
	}
	return clonePersonaProfile(provider.profiles[providerKey(binding.PersonaID, binding.Version)]), nil
}

func (provider *LocalPersonaProvider) Snapshot(
	ctx context.Context,
) (PersonaSnapshot, error) {
	if err := requireContext(ctx); err != nil {
		return PersonaSnapshot{}, err
	}
	if err := ctx.Err(); err != nil {
		return PersonaSnapshot{}, err
	}
	provider.mu.RLock()
	defer provider.mu.RUnlock()
	return provider.snapshotLocked(), nil
}

func (provider *LocalPersonaProvider) CompareAndSwap(
	ctx context.Context,
	snapshot PersonaSnapshot,
) (PersonaSnapshot, error) {
	if err := requireContext(ctx); err != nil {
		return PersonaSnapshot{}, err
	}
	if err := ctx.Err(); err != nil {
		return PersonaSnapshot{}, err
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if snapshot.Revision != provider.revision {
		return PersonaSnapshot{}, ErrPersonaConflict
	}
	replacement, err := NewLocalPersonaProvider(snapshot.Profiles, snapshot.Bindings)
	if err != nil {
		return PersonaSnapshot{}, err
	}
	provider.profiles = replacement.profiles
	provider.bindings = replacement.bindings
	provider.revision++
	return provider.snapshotLocked(), nil
}

func (provider *LocalPersonaProvider) Health(ctx context.Context) ProviderHealth {
	if ctx == nil || ctx.Err() != nil {
		return ProviderHealth{Code: "context_unavailable"}
	}
	return ProviderHealth{Available: true}
}

func (provider *LocalPersonaProvider) snapshotLocked() PersonaSnapshot {
	snapshot := PersonaSnapshot{
		Revision: provider.revision,
		Profiles: make([]PersonaProfile, 0, len(provider.profiles)),
		Bindings: make([]PersonaBinding, 0, len(provider.bindings)),
	}
	for _, profile := range provider.profiles {
		snapshot.Profiles = append(snapshot.Profiles, clonePersonaProfile(profile))
	}
	for _, binding := range provider.bindings {
		snapshot.Bindings = append(snapshot.Bindings, binding)
	}
	slices.SortFunc(snapshot.Profiles, func(left, right PersonaProfile) int {
		if left.PersonaID != right.PersonaID {
			return compareString(left.PersonaID, right.PersonaID)
		}
		return compareString(left.Version, right.Version)
	})
	slices.SortFunc(snapshot.Bindings, func(left, right PersonaBinding) int {
		if left.ActorID != right.ActorID {
			return compareString(left.ActorID, right.ActorID)
		}
		return compareString(left.ControllerID, right.ControllerID)
	})
	return snapshot
}

func SealPersonaProfile(profile PersonaProfile) (PersonaProfile, error) {
	if err := validateProviderID("persona_id", profile.PersonaID); err != nil {
		return PersonaProfile{}, err
	}
	if err := validateProviderID("version", profile.Version); err != nil {
		return PersonaProfile{}, err
	}
	if err := validateProviderText("identity", profile.Identity, 2_000, true); err != nil {
		return PersonaProfile{}, err
	}
	if err := validateProviderText("voice", profile.Voice, 1_000, false); err != nil {
		return PersonaProfile{}, err
	}
	var err error
	if profile.Traits, err = normalizeProviderTexts("traits", profile.Traits, 32, 160); err != nil {
		return PersonaProfile{}, err
	}
	if profile.Values, err = normalizeProviderTexts("values", profile.Values, 32, 160); err != nil {
		return PersonaProfile{}, err
	}
	if profile.PresentationRules, err = normalizeProviderTexts(
		"presentation_rules", profile.PresentationRules, 32, 300,
	); err != nil {
		return PersonaProfile{}, err
	}
	if len(profile.Boundaries) > 32 || len(profile.Relationships) > 32 {
		return PersonaProfile{}, errors.New("persona contains too many boundaries or relationships")
	}
	profile.Boundaries = append([]PersonaBoundary(nil), profile.Boundaries...)
	seenBoundaries := make(map[string]struct{}, len(profile.Boundaries))
	for index, boundary := range profile.Boundaries {
		if err := validateProviderID(fmt.Sprintf("boundaries[%d].boundary_id", index), boundary.BoundaryID); err != nil {
			return PersonaProfile{}, err
		}
		if _, exists := seenBoundaries[boundary.BoundaryID]; exists {
			return PersonaProfile{}, errors.New("persona boundaries contain duplicate ids")
		}
		seenBoundaries[boundary.BoundaryID] = struct{}{}
		if err := validateProviderText(fmt.Sprintf("boundaries[%d].rule", index), boundary.Rule, 500, true); err != nil {
			return PersonaProfile{}, err
		}
		if err := validateProviderText(fmt.Sprintf("boundaries[%d].response", index), boundary.Response, 500, true); err != nil {
			return PersonaProfile{}, err
		}
	}
	profile.Relationships = append([]RelationshipStance(nil), profile.Relationships...)
	seenRoles := make(map[string]struct{}, len(profile.Relationships))
	for index, relationship := range profile.Relationships {
		if err := validateProviderID(fmt.Sprintf("relationship_stances[%d].role", index), relationship.Role); err != nil {
			return PersonaProfile{}, err
		}
		if _, exists := seenRoles[relationship.Role]; exists {
			return PersonaProfile{}, errors.New("persona relationship roles must be unique")
		}
		seenRoles[relationship.Role] = struct{}{}
		if err := validateProviderText(fmt.Sprintf("relationship_stances[%d].stance", index), relationship.Stance, 300, true); err != nil {
			return PersonaProfile{}, err
		}
	}
	if profile.Initiative.CooldownMillis > 86_400_000 ||
		profile.Initiative.MaxConsecutiveActions > 64 {
		return PersonaProfile{}, errors.New("initiative policy exceeds its bounds")
	}
	if profile.Initiative.Triggers, err = normalizeProviderIDs(
		"initiative_policy.triggers", profile.Initiative.Triggers, 32,
	); err != nil {
		return PersonaProfile{}, err
	}
	return profile, nil
}

func validatePersonaBinding(binding PersonaBinding) error {
	if binding.ActorID == "" {
		if binding.ControllerID != "" {
			return errors.New("default persona binding cannot select a controller")
		}
	} else if err := validateProviderID("actor_id", binding.ActorID); err != nil {
		return err
	}
	if binding.ControllerID != "" {
		if err := validateProviderID("controller_id", binding.ControllerID); err != nil {
			return err
		}
	}
	if err := validateProviderID("persona_id", binding.PersonaID); err != nil {
		return err
	}
	return validateProviderID("version", binding.Version)
}

func clonePersonaProfile(profile PersonaProfile) PersonaProfile {
	profile.Traits = append([]string(nil), profile.Traits...)
	profile.Values = append([]string(nil), profile.Values...)
	profile.Boundaries = append([]PersonaBoundary(nil), profile.Boundaries...)
	profile.Relationships = append([]RelationshipStance(nil), profile.Relationships...)
	profile.Initiative.Triggers = append([]string(nil), profile.Initiative.Triggers...)
	profile.PresentationRules = append([]string(nil), profile.PresentationRules...)
	return profile
}

func personaBindingKey(actorID, controllerID string) string {
	return actorID + "\x00" + controllerID
}

func compareString(left, right string) int {
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}
