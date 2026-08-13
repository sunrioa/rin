package signalbox

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/sunrioa/rin/host"
)

const SchemaVersion = "rin.signal/v1"

var (
	ErrInvalid   = errors.New("invalid signal")
	ErrNotFound  = errors.New("signal inbox not found")
	ErrForbidden = errors.New("signal access forbidden")
	ErrClosed    = errors.New("signal inbox is closed")
	identifier   = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`)
)

type Signal struct {
	SchemaVersion        string     `json:"schema_version"`
	SignalID             string     `json:"signal_id"`
	HostID               string     `json:"host_id"`
	WorldID              string     `json:"world_id"`
	ActorID              string     `json:"actor_id"`
	Kind                 string     `json:"kind"`
	Summary              string     `json:"summary"`
	Epoch                host.Epoch `json:"epoch"`
	ObservationSequence  uint64     `json:"observation_sequence"`
	ExpiresAtUnixMillis  int64      `json:"expires_at_unix_millis"`
	ReceivedAtUnixMillis int64      `json:"received_at_unix_millis"`
	Cursor               uint64     `json:"cursor"`
	globalSequence       uint64
}

type Settings struct {
	Enabled        bool   `json:"enabled"`
	CooldownMillis uint32 `json:"cooldown_millis"`
	MaxPending     uint32 `json:"max_pending"`
}

type Target struct {
	HostID  string `json:"host_id"`
	WorldID string `json:"world_id"`
	ActorID string `json:"actor_id"`
}

type HostPublishInput struct {
	HostID  string `json:"host_id"`
	LeaseID string `json:"lease_id"`
	Signal  Signal `json:"signal"`
}

type HostSettingsInput struct {
	HostID   string   `json:"host_id"`
	LeaseID  string   `json:"lease_id"`
	WorldID  string   `json:"world_id"`
	ActorID  string   `json:"actor_id"`
	Settings Settings `json:"settings"`
}

type PublishResult struct {
	Accepted bool   `json:"accepted"`
	Reason   string `json:"reason,omitempty"`
	Cursor   uint64 `json:"cursor,omitempty"`
}

type ListInput struct {
	Target
	AfterCursor uint64 `json:"after_cursor,omitempty"`
	Limit       uint32 `json:"limit,omitempty"`
}

type Page struct {
	Signals    []Signal `json:"signals"`
	NextCursor uint64   `json:"next_cursor"`
	More       bool     `json:"more"`
}

type WaitInput struct {
	ListInput
	WaitMillis uint32 `json:"wait_millis,omitempty"`
}

type Update struct {
	Changed bool `json:"changed"`
	Page    Page `json:"page"`
}

func DefaultSettings() Settings {
	return Settings{Enabled: false, CooldownMillis: 30_000, MaxPending: 32}
}

func ValidateSettings(value Settings) error {
	if value.CooldownMillis > 3_600_000 {
		return invalid("cooldown_millis", "must not exceed 3600000")
	}
	if value.MaxPending == 0 || value.MaxPending > 256 {
		return invalid("max_pending", "must be between 1 and 256")
	}
	return nil
}

func ValidateTarget(value Target) error {
	for field, item := range map[string]string{
		"host_id": value.HostID, "world_id": value.WorldID, "actor_id": value.ActorID,
	} {
		if err := validateIdentifier(field, item, 128); err != nil {
			return err
		}
	}
	return nil
}

func validateSignal(value Signal, now int64) error {
	if value.SchemaVersion != "" && value.SchemaVersion != SchemaVersion {
		return invalid("schema_version", "is unsupported")
	}
	if err := ValidateTarget(Target{value.HostID, value.WorldID, value.ActorID}); err != nil {
		return err
	}
	if err := validateIdentifier("signal_id", value.SignalID, 128); err != nil {
		return err
	}
	if err := validateIdentifier("kind", value.Kind, 128); err != nil {
		return err
	}
	if !strings.Contains(value.Kind, ".") {
		return invalid("kind", "must use an adapter namespace")
	}
	if err := validateText("summary", value.Summary, 1_000, true); err != nil {
		return err
	}
	if err := value.Epoch.Validate("epoch"); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if value.Epoch.WorldID != value.WorldID || value.ObservationSequence == 0 {
		return invalid("observation_sequence", "must identify the current world observation")
	}
	if value.ExpiresAtUnixMillis <= now || value.ExpiresAtUnixMillis > now+86_400_000 {
		return invalid("expires_at_unix_millis", "must be within the next 24 hours")
	}
	if value.Cursor != 0 || value.ReceivedAtUnixMillis != 0 || value.globalSequence != 0 {
		return invalid("runtime fields", "must be assigned by Rin")
	}
	return nil
}

func validateList(input ListInput) (ListInput, error) {
	if err := ValidateTarget(input.Target); err != nil {
		return ListInput{}, err
	}
	if input.Limit == 0 {
		input.Limit = 32
	}
	if input.Limit > 100 {
		return ListInput{}, invalid("limit", "must not exceed 100")
	}
	return input, nil
}

func validateIdentifier(field, value string, maximum int) error {
	value = strings.TrimSpace(value)
	if len(value) == 0 || len(value) > maximum || !identifier.MatchString(value) {
		return invalid(field, "has invalid syntax")
	}
	return nil
}

func validateText(field, value string, maximum int, required bool) error {
	value = strings.TrimSpace(value)
	if (required && value == "") || len(value) > maximum || strings.ContainsRune(value, 0) {
		return invalid(field, "has invalid length or content")
	}
	return nil
}

func invalid(field, reason string) error {
	return fmt.Errorf("%w: %s %s", ErrInvalid, field, reason)
}
