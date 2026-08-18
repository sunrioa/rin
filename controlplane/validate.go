package controlplane

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/internal/jsonwire"
)

const (
	minLeaseTTLMillis    = 5_000
	maxLeaseTTLMillis    = 300_000
	maxWorldsPerHost     = 64
	maxActorsPerWorld    = 4_096
	maxVisiblePrincipals = 4_096
	maxCapabilitySpecs   = 512
	maxActorStateBytes   = 64 << 10
	maxPublicationBytes  = 8 << 20
	maxOperationOutput   = 64 << 10
	maxJSONSafeInteger   = 9_007_199_254_740_991
)

func validateRegistration(value HostRegistration) error {
	if value.ContractVersion != ContractVersion {
		return invalid("contract_version", "must equal "+ContractVersion)
	}
	if err := validateID("host_id", value.HostID); err != nil {
		return err
	}
	if err := validateID("instance_id", value.InstanceID); err != nil {
		return err
	}
	if err := host.ValidateHostManifest(value.Manifest); err != nil {
		return invalid("manifest", err.Error())
	}
	if value.LeaseTTLMillis < minLeaseTTLMillis ||
		value.LeaseTTLMillis > maxLeaseTTLMillis {
		return invalid("lease_ttl_millis", "must be between 5000 and 300000")
	}
	return nil
}

func validatePublication(
	value WorldPublication,
	manifest host.HostManifest,
	hostID string,
) error {
	if err := validateID("world_id", value.WorldID); err != nil {
		return err
	}
	if err := validateText("display_name", value.DisplayName, 128, true); err != nil {
		return err
	}
	if value.Sequence == 0 || value.Sequence > maxJSONSafeInteger {
		return invalid("sequence", "must be a positive JSON-safe integer")
	}
	if len(value.VisiblePrincipalIDs) > maxVisiblePrincipals {
		return invalid("visible_principal_ids", "must contain at most 4096 values")
	}
	visiblePrincipals := make(map[string]struct{}, len(value.VisiblePrincipalIDs))
	for index, principalID := range value.VisiblePrincipalIDs {
		field := fmt.Sprintf("visible_principal_ids[%d]", index)
		if err := validateID(field, principalID); err != nil {
			return err
		}
		if _, duplicate := visiblePrincipals[principalID]; duplicate {
			return invalid("visible_principal_ids", "must not contain duplicate values")
		}
		visiblePrincipals[principalID] = struct{}{}
	}
	maximumActors := uint32(maxActorsPerWorld)
	if manifest.MaxConcurrentActors < maximumActors {
		maximumActors = manifest.MaxConcurrentActors
	}
	if len(value.Actors) > int(maximumActors) {
		return invalid("actors", fmt.Sprintf("must contain at most %d values", maximumActors))
	}
	actors := make(map[string]struct{}, len(value.Actors))
	for index, actor := range value.Actors {
		if _, duplicate := actors[actor.ActorID]; duplicate {
			return invalid("actors", "must not contain duplicate actor_id values")
		}
		actors[actor.ActorID] = struct{}{}
		if err := validateActor(actor, hostID, value.WorldID, index); err != nil {
			return err
		}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return invalid("publication", "cannot encode: "+err.Error())
	}
	if len(encoded) > maxPublicationBytes {
		return invalid("publication", "must contain at most 8388608 bytes")
	}
	return nil
}

func validateActor(
	value ActorPublication,
	hostID, worldID string,
	index int,
) error {
	prefix := fmt.Sprintf("actors[%d]", index)
	if err := validateID(prefix+".actor_id", value.ActorID); err != nil {
		return err
	}
	if err := validateID(prefix+".owner_principal_id", value.OwnerPrincipalID); err != nil {
		return err
	}
	if err := validateText(prefix+".display_name", value.DisplayName, 128, true); err != nil {
		return err
	}
	if value.ObservationSeq == 0 || value.ObservationSeq > maxJSONSafeInteger {
		return invalid(prefix+".observation_seq", "must be a positive JSON-safe integer")
	}
	if err := value.Epoch.Validate(prefix + ".epoch"); err != nil {
		return invalid(prefix+".epoch", err.Error())
	}
	if value.Epoch.WorldID != worldID {
		return invalid(prefix+".epoch.world_id", "must equal publication world_id")
	}
	if value.Authority == nil {
		return invalid(prefix+".decision_authority", "is required")
	}
	if err := validateDecisionAuthority(
		prefix+".decision_authority",
		*value.Authority,
	); err != nil {
		return err
	}
	if err := validateJSONObject(prefix+".state", value.State, maxActorStateBytes); err != nil {
		return err
	}
	if value.Observation != nil {
		if err := host.ValidateObservationEnvelope(*value.Observation); err != nil {
			return invalid(prefix+".observation", err.Error())
		}
		if value.Observation.HostID != hostID ||
			value.Observation.WorldID != worldID ||
			value.Observation.ActorID != value.ActorID ||
			value.Observation.Epoch != value.Epoch ||
			value.Observation.Sequence != value.ObservationSeq {
			return invalid(
				prefix+".observation",
				"must match the enclosing Host, world, Actor, Epoch, and sequence",
			)
		}
	}
	if value.Capabilities != nil {
		if value.Capabilities.Revision > maxJSONSafeInteger {
			return invalid(
				prefix+".capabilities.revision",
				"must be a JSON-safe integer",
			)
		}
		if len(value.Capabilities.Specs) > maxCapabilitySpecs {
			return invalid(
				prefix+".capabilities.specs",
				"must contain at most 512 values",
			)
		}
		var previous host.CapabilityRef
		for specIndex, spec := range value.Capabilities.Specs {
			field := fmt.Sprintf("%s.capabilities.specs[%d]", prefix, specIndex)
			if err := spec.Validate(); err != nil {
				return invalid(field, err.Error())
			}
			if specIndex > 0 && !capabilityRefLess(previous, spec.Capability) {
				return invalid(
					prefix+".capabilities.specs",
					"must be sorted by capability ID and version without duplicates",
				)
			}
			previous = spec.Capability
		}
	}
	return nil
}

func capabilityRefLess(left, right host.CapabilityRef) bool {
	return left.ID < right.ID || left.ID == right.ID && left.Version < right.Version
}

func validateDecisionAuthority(
	field string,
	value DecisionAuthority,
) error {
	if value.Revision == 0 || value.Revision > maxJSONSafeInteger {
		return invalid(field+".revision", "must be a positive JSON-safe integer")
	}
	switch value.Source {
	case DecisionInternal:
		if value.ControllerPrincipalID != "" {
			return invalid(
				field+".controller_principal_id",
				"must be empty for internal authority",
			)
		}
		if value.PersonaMode != PersonaCharacterBound {
			return invalid(
				field+".persona_mode",
				"must be character-bound for internal authority",
			)
		}
	case DecisionExternal:
		if err := validateID(
			field+".controller_principal_id",
			value.ControllerPrincipalID,
		); err != nil {
			return err
		}
		if value.PersonaMode != PersonaCharacterBound &&
			value.PersonaMode != PersonaAgentAvatar {
			return invalid(
				field+".persona_mode",
				"must be character-bound or agent-avatar",
			)
		}
	default:
		return invalid(field+".source", "must be internal or external")
	}
	return nil
}

func validateID(field, value string) error {
	if err := host.ValidatePrincipal(host.Principal{ID: value}); err != nil {
		return invalid(field, strings.TrimPrefix(err.Error(), "principal.id: "))
	}
	return nil
}

func validateText(field, value string, maximum int, required bool) error {
	if !utf8.ValidString(value) || len(value) > maximum ||
		(required && strings.TrimSpace(value) == "") {
		return invalid(field, fmt.Sprintf("must be valid UTF-8 text of at most %d bytes", maximum))
	}
	return nil
}

func validateJSONObject(field string, raw json.RawMessage, maximum int) error {
	if len(raw) == 0 || len(raw) > maximum {
		return invalid(field, "must be a bounded JSON object")
	}
	if err := jsonwire.Validate(raw); err != nil {
		return invalid(field, err.Error())
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return invalid(field, "must be valid JSON")
	}
	if _, ok := value.(map[string]any); !ok {
		return invalid(field, "must be a JSON object")
	}
	return nil
}

func validateOperationOutput(output json.RawMessage) error {
	if len(output) == 0 {
		return nil
	}
	return validateJSONObject("output", output, maxOperationOutput)
}

func invalid(field, message string) error {
	return fmt.Errorf("%w: %s: %s", ErrInvalid, field, message)
}

func hasScope(principal host.Principal, scope string) bool {
	for _, granted := range principal.GrantedScopes {
		if granted == scope {
			return true
		}
	}
	return false
}

func canAccessActor(
	principal host.Principal,
	actor ActorPublication,
	requiredScope string,
) bool {
	if hasScope(principal, ScopeHostAdmin) {
		return true
	}
	if !hasScope(principal, requiredScope) {
		return false
	}
	if principal.ID == actor.OwnerPrincipalID {
		return true
	}
	return authorityAllowsExternal(actor, principal.ID)
}
