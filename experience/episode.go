// Package experience projects authoritative task evidence into portable
// learning inputs. It never authorizes actions or stores hidden reasoning.
package experience

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/sunrioa/rin/host"
	"github.com/sunrioa/rin/timeline"
)

const ContractVersion = "rin.experience.episode/v1"

type ControllerKind string

const (
	ControllerInternal    ControllerKind = "internal"
	ControllerExternalMCP ControllerKind = "external-mcp"
)

type ProjectionInput struct {
	EpisodeID      string
	ControllerKind ControllerKind
	Tags           []string
	Timeline       timeline.Page
	Corrections    []Correction
}

type Correction struct {
	CorrectionID         string `json:"correction_id"`
	OccurredAtUnixMillis int64  `json:"occurred_at_unix_millis"`
	Summary              string `json:"summary"`
	RelatedEventID       string `json:"related_event_id,omitempty"`
}

type EvidenceRef struct {
	EventID             string              `json:"event_id"`
	ObservationID       string              `json:"observation_id,omitempty"`
	ObservationSequence uint64              `json:"observation_sequence,omitempty"`
	Capability          *host.CapabilityRef `json:"capability,omitempty"`
	OperationID         string              `json:"operation_id,omitempty"`
	OperationStatus     string              `json:"operation_status,omitempty"`
	ExecutionConfirmed  bool                `json:"execution_confirmed,omitempty"`
	OutcomeCode         string              `json:"outcome_code,omitempty"`
}

type Event struct {
	EventID              string      `json:"event_id"`
	Kind                 string      `json:"kind"`
	OccurredAtUnixMillis int64       `json:"occurred_at_unix_millis"`
	Step                 uint32      `json:"step,omitempty"`
	Summary              string      `json:"summary,omitempty"`
	ReasonCode           string      `json:"reason_code,omitempty"`
	Evidence             EvidenceRef `json:"evidence"`
}

type VerifiedResult struct {
	Success         bool   `json:"success"`
	OutcomeCode     string `json:"outcome_code"`
	OperationID     string `json:"operation_id"`
	EvidenceEventID string `json:"evidence_event_id"`
	Summary         string `json:"summary,omitempty"`
}

// Episode is a derived evidence set. The timeline remains the source of
// execution truth, so projecting an episode never creates a second outcome.
type Episode struct {
	ContractVersion string          `json:"contract_version"`
	EpisodeID       string          `json:"episode_id"`
	TaskID          string          `json:"task_id"`
	ControllerKind  ControllerKind  `json:"controller_kind"`
	Goal            string          `json:"goal,omitempty"`
	GoalDigest      string          `json:"goal_digest,omitempty"`
	Tags            []string        `json:"tags,omitempty"`
	Status          string          `json:"status,omitempty"`
	Events          []Event         `json:"events"`
	Corrections     []Correction    `json:"corrections,omitempty"`
	VerifiedResult  *VerifiedResult `json:"verified_result,omitempty"`
}

func Project(input ProjectionInput) (Episode, error) {
	input.EpisodeID = strings.TrimSpace(input.EpisodeID)
	if input.EpisodeID == "" {
		input.EpisodeID = input.Timeline.TaskID
	}
	if err := validateText("episode_id", input.EpisodeID, 192, true); err != nil {
		return Episode{}, err
	}
	if input.ControllerKind != ControllerInternal &&
		input.ControllerKind != ControllerExternalMCP {
		return Episode{}, errors.New("controller kind is invalid")
	}
	if input.Timeline.ContractVersion != timeline.ContractVersion ||
		strings.TrimSpace(input.Timeline.TaskID) == "" {
		return Episode{}, errors.New("task timeline is invalid")
	}
	tags, err := normalizeTags(input.Tags)
	if err != nil {
		return Episode{}, err
	}
	episode := Episode{
		ContractVersion: ContractVersion, EpisodeID: input.EpisodeID,
		TaskID: input.Timeline.TaskID, ControllerKind: input.ControllerKind,
		Goal: input.Timeline.Goal, GoalDigest: input.Timeline.GoalDigest,
		Tags: tags, Status: input.Timeline.Status,
		Events: make([]Event, 0, len(input.Timeline.Events)),
	}
	knownEvents := make(map[string]struct{}, len(input.Timeline.Events))
	for _, source := range input.Timeline.Events {
		event, err := projectEvent(source)
		if err != nil {
			return Episode{}, err
		}
		if _, duplicate := knownEvents[event.EventID]; duplicate {
			return Episode{}, errors.New("timeline contains duplicate event ids")
		}
		knownEvents[event.EventID] = struct{}{}
		episode.Events = append(episode.Events, event)
		if event.Evidence.OperationStatus != "" && source.Operation != nil && source.Operation.Terminal {
			result, err := verifiedResult(source)
			if err != nil {
				return Episode{}, err
			}
			episode.VerifiedResult = result
		}
	}
	corrections := append([]Correction(nil), input.Corrections...)
	slices.SortFunc(corrections, func(left, right Correction) int {
		if left.OccurredAtUnixMillis < right.OccurredAtUnixMillis {
			return -1
		}
		if left.OccurredAtUnixMillis > right.OccurredAtUnixMillis {
			return 1
		}
		return strings.Compare(left.CorrectionID, right.CorrectionID)
	})
	seenCorrections := make(map[string]struct{}, len(corrections))
	for index, correction := range corrections {
		sealed, err := sealCorrection(correction, knownEvents)
		if err != nil {
			return Episode{}, fmt.Errorf("corrections[%d]: %w", index, err)
		}
		if _, duplicate := seenCorrections[sealed.CorrectionID]; duplicate {
			return Episode{}, errors.New("corrections contain duplicate ids")
		}
		seenCorrections[sealed.CorrectionID] = struct{}{}
		episode.Corrections = append(episode.Corrections, sealed)
	}
	return episode, nil
}

func projectEvent(source timeline.Event) (Event, error) {
	if err := validateText("event_id", source.EventID, 192, true); err != nil {
		return Event{}, err
	}
	if err := validateText("event_kind", source.EventKind, 128, true); err != nil {
		return Event{}, err
	}
	if source.OccurredAtUnixMillis < 0 {
		return Event{}, errors.New("event time is invalid")
	}
	result := Event{
		EventID: source.EventID, Kind: source.EventKind,
		OccurredAtUnixMillis: source.OccurredAtUnixMillis, Step: source.Step,
		Summary: source.PublicSummary, ReasonCode: source.ReasonCode,
		Evidence: EvidenceRef{
			EventID: source.EventID, ObservationID: source.ObservationID,
			ObservationSequence: source.ObservationSequence,
		},
	}
	if source.Capability != nil {
		capability := *source.Capability
		result.Evidence.Capability = &capability
	}
	if source.Operation != nil {
		result.Evidence.OperationID = source.Operation.OperationID
		result.Evidence.OperationStatus = source.Operation.Status
		result.Evidence.ExecutionConfirmed = source.Operation.ExecutionConfirmed
		result.Evidence.OutcomeCode = source.Operation.OutcomeCode
	}
	return result, nil
}

func verifiedResult(source timeline.Event) (*VerifiedResult, error) {
	operation := source.Operation
	if operation == nil || !operation.Terminal {
		return nil, nil
	}
	if operation.Status == "succeeded" {
		if !operation.ExecutionConfirmed || operation.OutcomeCode != "succeeded" {
			return nil, errors.New("successful terminal operation lacks authoritative outcome")
		}
		return &VerifiedResult{
			Success: true, OutcomeCode: operation.OutcomeCode,
			OperationID: operation.OperationID, EvidenceEventID: source.EventID,
			Summary: source.PublicSummary,
		}, nil
	}
	if operation.ExecutionConfirmed {
		return nil, errors.New("only a succeeded operation may be execution-confirmed")
	}
	return &VerifiedResult{
		Success: false, OutcomeCode: operation.OutcomeCode,
		OperationID: operation.OperationID, EvidenceEventID: source.EventID,
		Summary: source.PublicSummary,
	}, nil
}

func sealCorrection(
	correction Correction,
	knownEvents map[string]struct{},
) (Correction, error) {
	correction.CorrectionID = strings.TrimSpace(correction.CorrectionID)
	correction.Summary = strings.TrimSpace(correction.Summary)
	correction.RelatedEventID = strings.TrimSpace(correction.RelatedEventID)
	if err := validateText("correction_id", correction.CorrectionID, 192, true); err != nil {
		return Correction{}, err
	}
	if correction.OccurredAtUnixMillis < 0 {
		return Correction{}, errors.New("correction time is invalid")
	}
	if err := validateText("summary", correction.Summary, 500, true); err != nil {
		return Correction{}, err
	}
	if correction.RelatedEventID != "" {
		if _, exists := knownEvents[correction.RelatedEventID]; !exists {
			return Correction{}, errors.New("related event is not present in the episode")
		}
	}
	return correction, nil
}

func normalizeTags(values []string) ([]string, error) {
	if len(values) > 32 {
		return nil, errors.New("tags must contain at most 32 values")
	}
	result := append([]string(nil), values...)
	for index := range result {
		result[index] = strings.TrimSpace(result[index])
		if err := validateText(fmt.Sprintf("tags[%d]", index), result[index], 96, true); err != nil {
			return nil, err
		}
	}
	slices.Sort(result)
	if len(slices.Compact(result)) != len(result) {
		return nil, errors.New("tags must not contain duplicates")
	}
	return result, nil
}

func validateText(field, value string, maximum int, required bool) error {
	if required && strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", field)
	}
	if !utf8.ValidString(value) || strings.ContainsRune(value, 0) ||
		utf8.RuneCountInString(value) > maximum {
		return fmt.Errorf("%s is invalid", field)
	}
	return nil
}
