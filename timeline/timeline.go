// Package timeline defines Rin's engine-neutral, read-only task timeline.
// Timeline records explain authoritative state; they never authorize or drive
// execution.
package timeline

import (
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/sunrioa/rin/host"
)

const (
	ContractVersion = "rin.task-timeline/v1"
	DefaultLimit    = uint32(64)
	MaximumLimit    = uint32(256)
	MaximumWaitMS   = uint32(25_000)
	maxJSONSafeUint = uint64(9_007_199_254_740_991)
)

var ErrInvalid = errors.New("timeline invalid value")

type MemoryContextRef struct {
	MemoryID string `json:"memory_id"`
	Domain   string `json:"domain"`
	Source   string `json:"source"`
	Rank     uint32 `json:"rank"`
	Digest   string `json:"digest"`
}

type SkillContextRef struct {
	SkillID string `json:"skill_id"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
}

type SignalContextRef struct {
	SignalID string `json:"signal_id"`
	Kind     string `json:"kind"`
	Cursor   uint64 `json:"cursor"`
}

// ModelUsage contains measured provider metadata only. A nil pointer means the
// provider did not report that metric; callers must not interpret it as zero.
type ModelUsage struct {
	Model            string  `json:"model,omitempty"`
	LatencyMillis    *uint64 `json:"latency_ms,omitempty"`
	PromptTokens     *uint64 `json:"prompt_tokens,omitempty"`
	CompletionTokens *uint64 `json:"completion_tokens,omitempty"`
	TotalTokens      *uint64 `json:"total_tokens,omitempty"`
	CacheHitTokens   *uint64 `json:"cache_hit_tokens,omitempty"`
	CacheMissTokens  *uint64 `json:"cache_miss_tokens,omitempty"`
	CacheWriteTokens *uint64 `json:"cache_write_tokens,omitempty"`
}

type PolicySummary struct {
	Disposition         string   `json:"disposition"`
	ReasonCode          string   `json:"reason_code"`
	HumanSummary        string   `json:"human_summary"`
	MatchedRuleIDs      []string `json:"matched_rule_ids,omitempty"`
	ConfirmationPending bool     `json:"confirmation_pending"`
	EffectCount         uint32   `json:"effect_count"`
}

type OperationSummary struct {
	OperationID           string `json:"operation_id"`
	Status                string `json:"status"`
	Terminal              bool   `json:"terminal"`
	ExecutionConfirmed    bool   `json:"execution_confirmed"`
	ReconciliationPending bool   `json:"reconciliation_pending"`
	OutcomeCode           string `json:"outcome_code,omitempty"`
	DeliveryAttempts      uint32 `json:"delivery_attempts,omitempty"`
	ProgressSequence      uint64 `json:"progress_sequence,omitempty"`
	Progress              uint32 `json:"progress,omitempty"`
	CancelRequested       bool   `json:"cancel_requested,omitempty"`
}

// Event is safe, bounded explanatory metadata. PublicSummary is a short
// caller-visible reason, never hidden chain-of-thought.
type Event struct {
	EventID              string `json:"event_id"`
	Cursor               string `json:"cursor"`
	OccurredAtUnixMillis int64  `json:"occurred_at_unix_millis"`

	TaskID       string            `json:"task_id"`
	SessionID    string            `json:"session_id,omitempty"`
	HostID       string            `json:"host_id,omitempty"`
	WorldID      string            `json:"world_id,omitempty"`
	ActorID      string            `json:"actor_id,omitempty"`
	ControllerID string            `json:"controller_id,omitempty"`
	Step         uint32            `json:"step,omitempty"`
	PlanID       string            `json:"plan_id,omitempty"`
	PlanRevision uint64            `json:"plan_revision,omitempty"`
	PlanStepID   string            `json:"plan_step_id,omitempty"`
	Signal       *SignalContextRef `json:"signal,omitempty"`

	EventKind     string `json:"event_kind"`
	PublicSummary string `json:"public_summary,omitempty"`
	ReasonCode    string `json:"reason_code,omitempty"`
	GoalDigest    string `json:"goal_digest,omitempty"`

	ObservationID       string              `json:"observation_id,omitempty"`
	ObservationSequence uint64              `json:"observation_sequence,omitempty"`
	Epoch               *host.Epoch         `json:"epoch,omitempty"`
	Capability          *host.CapabilityRef `json:"capability,omitempty"`

	SkillRefs         []SkillContextRef  `json:"skill_refs,omitempty"`
	MemoryContextRefs []MemoryContextRef `json:"memory_context_refs,omitempty"`
	Model             *ModelUsage        `json:"model_usage,omitempty"`
	Policy            *PolicySummary     `json:"policy,omitempty"`
	Operation         *OperationSummary  `json:"operation,omitempty"`
}

// Record is an internal projection input. Sequence is monotonically
// increasing within one task timeline source.
type Record struct {
	Sequence uint64
	Event    Event
}

type Snapshot struct {
	TaskID          string
	Goal            string
	GoalDigest      string
	Status          string
	LatestSequence  uint64
	TruncatedBefore uint64
	Records         []Record
}

type Query struct {
	TaskID      string `json:"task_id"`
	AfterCursor string `json:"after_cursor,omitempty"`
	Limit       uint32 `json:"limit,omitempty"`
}

type WaitInput struct {
	TaskID      string `json:"task_id"`
	AfterCursor string `json:"after_cursor,omitempty"`
	Limit       uint32 `json:"limit,omitempty"`
	WaitMillis  uint32 `json:"wait_millis"`
}

func (input WaitInput) Query() Query {
	return Query{TaskID: input.TaskID, AfterCursor: input.AfterCursor, Limit: input.Limit}
}

type Page struct {
	ContractVersion string  `json:"contract_version"`
	TaskID          string  `json:"task_id"`
	Goal            string  `json:"goal,omitempty"`
	GoalDigest      string  `json:"goal_digest,omitempty"`
	Status          string  `json:"status,omitempty"`
	Events          []Event `json:"events"`
	NextCursor      string  `json:"next_cursor"`
	More            bool    `json:"more"`
	Truncated       bool    `json:"truncated"`
}

type Update struct {
	Timeline Page `json:"timeline"`
	Changed  bool `json:"changed"`
}

func NormalizeQuery(query Query) (Query, uint64, error) {
	query.TaskID = strings.TrimSpace(query.TaskID)
	if err := validateText("task_id", query.TaskID, 128, true); err != nil {
		return Query{}, 0, err
	}
	if query.Limit == 0 {
		query.Limit = DefaultLimit
	}
	if query.Limit > MaximumLimit {
		return Query{}, 0, fmt.Errorf("%w: limit must not exceed %d", ErrInvalid, MaximumLimit)
	}
	after, err := ParseCursor(query.AfterCursor)
	if err != nil {
		return Query{}, 0, err
	}
	return query, after, nil
}

func NormalizeWait(input WaitInput) (WaitInput, uint64, error) {
	query, after, err := NormalizeQuery(input.Query())
	if err != nil {
		return WaitInput{}, 0, err
	}
	if input.WaitMillis > MaximumWaitMS {
		return WaitInput{}, 0, fmt.Errorf("%w: wait_millis must not exceed %d", ErrInvalid, MaximumWaitMS)
	}
	input.TaskID = query.TaskID
	input.AfterCursor = query.AfterCursor
	input.Limit = query.Limit
	return input, after, nil
}

func BuildPage(snapshot Snapshot, query Query) (Page, error) {
	query, after, err := NormalizeQuery(query)
	if err != nil {
		return Page{}, err
	}
	if snapshot.TaskID != query.TaskID {
		return Page{}, fmt.Errorf("%w: snapshot task_id does not match query", ErrInvalid)
	}
	if err := validateText("goal", snapshot.Goal, 2_000, false); err != nil {
		return Page{}, err
	}
	if err := validateText("status", snapshot.Status, 64, false); err != nil {
		return Page{}, err
	}
	if snapshot.GoalDigest != "" && !validDigest(snapshot.GoalDigest) {
		return Page{}, fmt.Errorf("%w: snapshot goal_digest is invalid", ErrInvalid)
	}
	if snapshot.TruncatedBefore > snapshot.LatestSequence {
		return Page{}, fmt.Errorf("%w: truncation cursor exceeds snapshot", ErrInvalid)
	}
	if after > snapshot.LatestSequence {
		return Page{}, fmt.Errorf("%w: after_cursor exceeds task timeline", ErrInvalid)
	}
	records := append([]Record(nil), snapshot.Records...)
	slices.SortFunc(records, func(left, right Record) int {
		return compareSequence(left.Sequence, right.Sequence)
	})
	for index, record := range records {
		if record.Sequence == 0 ||
			(index != 0 && record.Sequence == records[index-1].Sequence) {
			return Page{}, fmt.Errorf("%w: record sequence is invalid", ErrInvalid)
		}
		if snapshot.LatestSequence != 0 && record.Sequence > snapshot.LatestSequence {
			return Page{}, fmt.Errorf("%w: record sequence exceeds snapshot", ErrInvalid)
		}
	}
	events := make([]Event, 0, min(len(records), int(query.Limit)))
	more := false
	last := after
	for _, record := range records {
		if record.Sequence == 0 || record.Sequence <= after {
			continue
		}
		if len(events) == int(query.Limit) {
			more = true
			break
		}
		event, err := sealEvent(snapshot, record)
		if err != nil {
			return Page{}, err
		}
		events = append(events, event)
		last = record.Sequence
	}
	return Page{
		ContractVersion: ContractVersion,
		TaskID:          query.TaskID, Goal: snapshot.Goal, GoalDigest: snapshot.GoalDigest,
		Status: snapshot.Status, Events: events, NextCursor: FormatCursor(last), More: more,
		Truncated: snapshot.TruncatedBefore != 0 && after < snapshot.TruncatedBefore,
	}, nil
}

func FormatCursor(sequence uint64) string {
	return "tl1:" + strconv.FormatUint(sequence, 36)
}

func ParseCursor(cursor string) (uint64, error) {
	if cursor == "" {
		return 0, nil
	}
	if len(cursor) > 64 || !strings.HasPrefix(cursor, "tl1:") {
		return 0, fmt.Errorf("%w: after_cursor is not a Rin timeline cursor", ErrInvalid)
	}
	sequence, err := strconv.ParseUint(strings.TrimPrefix(cursor, "tl1:"), 36, 64)
	if err != nil || cursor != FormatCursor(sequence) {
		return 0, fmt.Errorf("%w: after_cursor is invalid", ErrInvalid)
	}
	return sequence, nil
}

func sealEvent(snapshot Snapshot, record Record) (Event, error) {
	event := record.Event
	if event.TaskID == "" {
		event.TaskID = snapshot.TaskID
	}
	if event.TaskID != snapshot.TaskID {
		return Event{}, fmt.Errorf("%w: event task_id does not match snapshot", ErrInvalid)
	}
	if event.EventID == "" {
		event.EventID = snapshot.TaskID + ".event." + strconv.FormatUint(record.Sequence, 10)
	}
	event.Cursor = FormatCursor(record.Sequence)
	if event.GoalDigest == "" {
		event.GoalDigest = snapshot.GoalDigest
	}
	for field, value := range map[string]string{
		"event_id": event.EventID, "task_id": event.TaskID, "event_kind": event.EventKind,
	} {
		if err := validateText(field, value, 192, true); err != nil {
			return Event{}, err
		}
	}
	for field, value := range map[string]string{
		"session_id": event.SessionID, "host_id": event.HostID,
		"world_id": event.WorldID, "actor_id": event.ActorID,
		"controller_id": event.ControllerID, "observation_id": event.ObservationID,
	} {
		if err := validateText(field, value, 128, false); err != nil {
			return Event{}, err
		}
	}
	if event.OccurredAtUnixMillis < 0 || uint64(event.OccurredAtUnixMillis) > maxJSONSafeUint {
		return Event{}, fmt.Errorf("%w: event time is invalid", ErrInvalid)
	}
	if err := validateText("public_summary", event.PublicSummary, 500, false); err != nil {
		return Event{}, err
	}
	if err := validateText("reason_code", event.ReasonCode, 128, false); err != nil {
		return Event{}, err
	}
	if err := validateText("goal_digest", event.GoalDigest, 71, false); err != nil {
		return Event{}, err
	}
	if event.GoalDigest != "" && !validDigest(event.GoalDigest) {
		return Event{}, fmt.Errorf("%w: goal_digest is invalid", ErrInvalid)
	}
	if event.ObservationID != "" && event.ObservationSequence == 0 {
		return Event{}, fmt.Errorf("%w: observation_id requires a sequence", ErrInvalid)
	}
	if (event.ObservationSequence == 0) != (event.Epoch == nil) {
		return Event{}, fmt.Errorf("%w: observation sequence and epoch must appear together", ErrInvalid)
	}
	if event.ObservationSequence > maxJSONSafeUint {
		return Event{}, fmt.Errorf("%w: observation sequence is not JSON-safe", ErrInvalid)
	}
	if event.Epoch != nil {
		if err := event.Epoch.Validate("epoch"); err != nil {
			return Event{}, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
	}
	if event.Capability != nil {
		if err := event.Capability.Validate("capability"); err != nil {
			return Event{}, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
	}
	if event.PlanID == "" {
		if event.PlanRevision != 0 || event.PlanStepID != "" {
			return Event{}, fmt.Errorf("%w: incomplete plan reference", ErrInvalid)
		}
	} else if err := (host.PlanStepRef{
		PlanID: event.PlanID, PlanRevision: event.PlanRevision, StepID: event.PlanStepID,
	}).Validate("plan_step_ref"); err != nil {
		return Event{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if event.Signal != nil {
		if err := ValidateSignalContextRef(*event.Signal); err != nil {
			return Event{}, err
		}
		signal := *event.Signal
		event.Signal = &signal
	}
	if err := validateContextRefs(event); err != nil {
		return Event{}, err
	}
	if err := validateModelUsage(event.Model); err != nil {
		return Event{}, err
	}
	if err := validatePolicySummary(event.Policy); err != nil {
		return Event{}, err
	}
	if err := validateOperationSummary(event.Operation); err != nil {
		return Event{}, err
	}
	event.SkillRefs = append([]SkillContextRef(nil), event.SkillRefs...)
	event.MemoryContextRefs = append([]MemoryContextRef(nil), event.MemoryContextRefs...)
	if event.Policy != nil {
		policy := *event.Policy
		policy.MatchedRuleIDs = append([]string(nil), policy.MatchedRuleIDs...)
		event.Policy = &policy
	}
	if event.Epoch != nil {
		epoch := *event.Epoch
		event.Epoch = &epoch
	}
	if event.Capability != nil {
		capability := *event.Capability
		event.Capability = &capability
	}
	if event.Model != nil {
		model := *event.Model
		model.LatencyMillis = cloneUint64Pointer(model.LatencyMillis)
		model.PromptTokens = cloneUint64Pointer(model.PromptTokens)
		model.CompletionTokens = cloneUint64Pointer(model.CompletionTokens)
		model.TotalTokens = cloneUint64Pointer(model.TotalTokens)
		model.CacheHitTokens = cloneUint64Pointer(model.CacheHitTokens)
		model.CacheMissTokens = cloneUint64Pointer(model.CacheMissTokens)
		model.CacheWriteTokens = cloneUint64Pointer(model.CacheWriteTokens)
		event.Model = &model
	}
	if event.Operation != nil {
		operation := *event.Operation
		event.Operation = &operation
	}
	return event, nil
}

func ValidateSignalContextRef(value SignalContextRef) error {
	if err := validateText("signal_id", value.SignalID, 128, true); err != nil {
		return err
	}
	if err := validateText("signal.kind", value.Kind, 128, true); err != nil {
		return err
	}
	if value.Cursor == 0 || value.Cursor > maxJSONSafeUint {
		return fmt.Errorf("%w: signal cursor is invalid", ErrInvalid)
	}
	return nil
}

func validateContextRefs(event Event) error {
	if len(event.SkillRefs) > 64 || len(event.MemoryContextRefs) > 64 {
		return fmt.Errorf("%w: context reference limit exceeded", ErrInvalid)
	}
	seenSkills := make(map[string]struct{}, len(event.SkillRefs))
	for index, ref := range event.SkillRefs {
		for field, value := range map[string]string{
			"skill_id": ref.SkillID, "version": ref.Version,
		} {
			if err := validateText(
				fmt.Sprintf("skill_refs[%d].%s", index, field), value, 96, true,
			); err != nil {
				return err
			}
		}
		if !validDigest(ref.Digest) {
			return fmt.Errorf("%w: skill_refs[%d].digest is invalid", ErrInvalid, index)
		}
		key := ref.SkillID + "\x00" + ref.Version
		if _, duplicate := seenSkills[key]; duplicate {
			return fmt.Errorf("%w: skill_refs contain a duplicate", ErrInvalid)
		}
		seenSkills[key] = struct{}{}
	}
	seenMemories := make(map[string]struct{}, len(event.MemoryContextRefs))
	seenRanks := make(map[uint32]struct{}, len(event.MemoryContextRefs))
	for index, ref := range event.MemoryContextRefs {
		for field, value := range map[string]string{
			"memory_id": ref.MemoryID, "domain": ref.Domain, "source": ref.Source,
		} {
			if err := validateText(
				fmt.Sprintf("memory_context_refs[%d].%s", index, field), value, 128, true,
			); err != nil {
				return err
			}
		}
		if ref.Rank == 0 || ref.Rank > 64 || !validDigest(ref.Digest) {
			return fmt.Errorf("%w: memory_context_refs[%d] is invalid", ErrInvalid, index)
		}
		if _, duplicate := seenMemories[ref.MemoryID]; duplicate {
			return fmt.Errorf("%w: memory_context_refs contain a duplicate", ErrInvalid)
		}
		if _, duplicate := seenRanks[ref.Rank]; duplicate {
			return fmt.Errorf("%w: memory context ranks contain a duplicate", ErrInvalid)
		}
		seenMemories[ref.MemoryID] = struct{}{}
		seenRanks[ref.Rank] = struct{}{}
	}
	return nil
}

func validateModelUsage(usage *ModelUsage) error {
	if usage == nil {
		return nil
	}
	if err := validateText("model_usage.model", usage.Model, 200, false); err != nil {
		return err
	}
	for field, value := range map[string]*uint64{
		"latency_ms": usage.LatencyMillis, "prompt_tokens": usage.PromptTokens,
		"completion_tokens": usage.CompletionTokens, "total_tokens": usage.TotalTokens,
		"cache_hit_tokens": usage.CacheHitTokens, "cache_miss_tokens": usage.CacheMissTokens,
		"cache_write_tokens": usage.CacheWriteTokens,
	} {
		if value != nil && *value > maxJSONSafeUint {
			return fmt.Errorf("%w: model_usage.%s is not JSON-safe", ErrInvalid, field)
		}
	}
	return nil
}

func validatePolicySummary(summary *PolicySummary) error {
	if summary == nil {
		return nil
	}
	if summary.Disposition != "allow" && summary.Disposition != "deny" &&
		summary.Disposition != "require_confirmation" {
		return fmt.Errorf("%w: policy disposition is invalid", ErrInvalid)
	}
	if err := validateText("policy.reason_code", summary.ReasonCode, 128, false); err != nil {
		return err
	}
	if err := validateText("policy.human_summary", summary.HumanSummary, 500, false); err != nil {
		return err
	}
	if len(summary.MatchedRuleIDs) > 1_024 {
		return fmt.Errorf("%w: policy matched rule limit exceeded", ErrInvalid)
	}
	seen := make(map[string]struct{}, len(summary.MatchedRuleIDs))
	for index, ruleID := range summary.MatchedRuleIDs {
		if err := validateText(
			fmt.Sprintf("policy.matched_rule_ids[%d]", index), ruleID, 96, true,
		); err != nil {
			return err
		}
		if _, duplicate := seen[ruleID]; duplicate {
			return fmt.Errorf("%w: policy matched rules contain a duplicate", ErrInvalid)
		}
		seen[ruleID] = struct{}{}
	}
	return nil
}

func validateOperationSummary(summary *OperationSummary) error {
	if summary == nil {
		return nil
	}
	if err := validateText("operation.operation_id", summary.OperationID, 128, true); err != nil {
		return err
	}
	if err := validateText("operation.status", summary.Status, 64, true); err != nil {
		return err
	}
	if err := validateText("operation.outcome_code", summary.OutcomeCode, 128, false); err != nil {
		return err
	}
	if summary.Progress > 100 || summary.ProgressSequence > maxJSONSafeUint {
		return fmt.Errorf("%w: operation progress is invalid", ErrInvalid)
	}
	if summary.ExecutionConfirmed && (!summary.Terminal || summary.Status != "succeeded") {
		return fmt.Errorf("%w: confirmed execution must be terminal success", ErrInvalid)
	}
	if summary.ReconciliationPending && summary.Terminal {
		return fmt.Errorf("%w: terminal operation cannot await reconciliation", ErrInvalid)
	}
	return nil
}

func validDigest(value string) bool {
	value = strings.TrimPrefix(value, "sha256:")
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func cloneUint64Pointer(value *uint64) *uint64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func validateText(field, value string, maximum int, required bool) error {
	if required && value == "" {
		return fmt.Errorf("%w: %s is required", ErrInvalid, field)
	}
	if !utf8.ValidString(value) || len(value) > maximum {
		return fmt.Errorf("%w: %s must be valid UTF-8 of at most %d bytes", ErrInvalid, field, maximum)
	}
	return nil
}

func compareSequence(left, right uint64) int {
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}
