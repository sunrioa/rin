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
	minLeaseTTLMillis  = 5_000
	maxLeaseTTLMillis  = 300_000
	maxWorldsPerHost   = 64
	maxActorsPerWorld  = 4_096
	maxOffersPerActor  = 64
	maxActorStateBytes = 64 << 10
	maxOperationOutput = 64 << 10
	maxJSONSafeInteger = 9_007_199_254_740_991
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

func validatePublication(value WorldPublication, manifest host.HostManifest) error {
	if err := validateID("world_id", value.WorldID); err != nil {
		return err
	}
	if err := validateText("display_name", value.DisplayName, 128, true); err != nil {
		return err
	}
	if value.Sequence == 0 || value.Sequence > maxJSONSafeInteger {
		return invalid("sequence", "must be a positive JSON-safe integer")
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
		if err := validateActor(actor, value.WorldID, index); err != nil {
			return err
		}
	}
	return nil
}

func validateActor(value ActorPublication, worldID string, index int) error {
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
	if err := validateJSONObject(prefix+".state", value.State, maxActorStateBytes); err != nil {
		return err
	}
	if len(value.Offers) > maxOffersPerActor {
		return invalid(prefix+".offers", "must contain at most 64 values")
	}
	offers := make(map[string]struct{}, len(value.Offers))
	for offerIndex, offer := range value.Offers {
		field := fmt.Sprintf("%s.offers[%d]", prefix, offerIndex)
		if err := host.ValidateActionOffer(offer); err != nil {
			return invalid(field, err.Error())
		}
		if offer.ActorID != value.ActorID {
			return invalid(field+".actor_id", "must equal actor_id")
		}
		if offer.ExpectedEpoch != value.Epoch {
			return invalid(field+".expected_epoch", "must equal actor epoch")
		}
		if offer.ObservationSeq != value.ObservationSeq {
			return invalid(field+".observation_seq", "must equal actor observation_seq")
		}
		if _, duplicate := offers[offer.OfferID]; duplicate {
			return invalid(prefix+".offers", "must not contain duplicate offer_id values")
		}
		offers[offer.OfferID] = struct{}{}
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

func canRead(principal host.Principal, ownerPrincipalID string) bool {
	return hasScope(principal, ScopeHostAdmin) ||
		(principal.ID == ownerPrincipalID && hasScope(principal, ScopeActorRead))
}
