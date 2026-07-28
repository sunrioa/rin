package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/sunrioa/rin/policy"
	"github.com/sunrioa/rin/protocol"
	rintime "github.com/sunrioa/rin/runtime"
	"github.com/sunrioa/rin/store"
)

type inspectOutput struct {
	ProtocolVersion  string                   `json:"protocol_version"`
	Mode             string                   `json:"mode"`
	SessionID        string                   `json:"session_id"`
	Binding          protocol.Binding         `json:"binding"`
	Revision         uint64                   `json:"revision"`
	WorldRevision    uint64                   `json:"world_revision,omitempty"`
	Tick             int64                    `json:"tick"`
	Features         []string                 `json:"features,omitempty"`
	ActorCount       int                      `json:"actor_count"`
	PendingProposals int                      `json:"pending_proposals"`
	ArbitrationCount int                      `json:"arbitration_count"`
	StateHash        string                   `json:"state_hash"`
	Timeline         []protocol.TimelineEntry `json:"timeline,omitempty"`
}

func runInspect(arguments []string, output io.Writer) (resultErr error) {
	flags := flag.NewFlagSet("rin inspect", flag.ContinueOnError)
	flags.SetOutput(output)
	dataDirectory := flags.String("data", envOr("RIN_DATA_DIR", "./rin-data"), "event and snapshot directory")
	sessionID := flags.String("session", "", "session identifier")
	revision := flags.Uint64("revision", 0, "event-log revision; zero selects current")
	timelineLimit := flags.Int("timeline-limit", 50, "number of redacted timeline entries (0-256)")
	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	if *sessionID == "" {
		return errors.New("-session is required")
	}
	if *timelineLimit < 0 || *timelineLimit > 256 {
		return errors.New("-timeline-limit must be between 0 and 256")
	}
	fileStore, err := store.OpenFileReadOnly(*dataDirectory)
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, fileStore.Close())
	}()
	engine, err := rintime.OpenWithOptions(
		fileStore,
		policy.Deterministic{},
		rintime.EngineOptions{
			MaxSessionStateBytes: rintime.MaxConfigurableSessionStateBytes,
		},
	)
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(
			resultErr,
			engine.Close(context.Background()),
		)
	}()
	var state protocol.SessionState
	if *revision == 0 {
		state, err = engine.State(protocol.SessionRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       *sessionID,
		})
	} else {
		state, err = engine.ReplayState(protocol.ReplayRequest{
			ProtocolVersion: protocol.Version,
			SessionID:       *sessionID,
			Revision:        *revision,
		})
	}
	if err != nil {
		return err
	}
	stateHash, err := rintime.SessionStateHash(state)
	if err != nil {
		return err
	}
	timeline, err := inspectTimeline(
		engine,
		*sessionID,
		state.Revision,
		*timelineLimit,
	)
	if err != nil {
		return err
	}
	pending := 0
	for _, proposal := range state.Proposals {
		if proposal.Status == "pending" {
			pending++
		}
	}
	result := inspectOutput{
		ProtocolVersion: protocol.Version, Mode: "read-only",
		SessionID: state.SessionID,
		Binding:   state.Binding, Revision: state.Revision,
		WorldRevision: state.WorldRevision, Tick: state.Tick,
		Features:   append([]string(nil), state.Features...),
		ActorCount: len(state.Actors), PendingProposals: pending,
		ArbitrationCount: len(state.Arbitrations), StateHash: stateHash,
		Timeline: timeline,
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}

func inspectTimeline(engine *rintime.Engine, sessionID string, revision uint64, limit int) ([]protocol.TimelineEntry, error) {
	if limit == 0 {
		return nil, nil
	}
	entries := make([]protocol.TimelineEntry, 0, limit)
	after := uint64(0)
	if revision > uint64(limit) {
		after = revision - uint64(limit)
	}
	for {
		pageStart := after
		page, err := engine.Timeline(protocol.TimelineRequest{
			ProtocolVersion: protocol.Version, SessionID: sessionID,
			AfterRevision: after, Limit: limit,
		})
		if err != nil {
			return nil, err
		}
		reachedTarget := false
		for _, entry := range page.Entries {
			if entry.Sequence > revision {
				reachedTarget = true
				break
			}
			entries = append(entries, entry)
			if len(entries) > limit {
				entries = append([]protocol.TimelineEntry(nil), entries[len(entries)-limit:]...)
			}
			if entry.Sequence == revision {
				reachedTarget = true
				break
			}
		}
		if reachedTarget || !page.HasMore {
			break
		}
		if page.NextAfterRevision <= pageStart {
			return nil, errors.New("timeline pagination did not advance")
		}
		after = page.NextAfterRevision
	}
	return entries, nil
}
