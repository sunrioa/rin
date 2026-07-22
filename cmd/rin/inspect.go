package main

import (
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

func runInspect(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("rin inspect", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dataDirectory := flags.String("data", envOr("RIN_DATA_DIR", "./rin-data"), "event and snapshot directory")
	sessionID := flags.String("session", "", "session identifier")
	revision := flags.Uint64("revision", 0, "event-log revision; zero selects current")
	timelineLimit := flags.Int("timeline-limit", 50, "number of redacted timeline entries (0-256)")
	if err := flags.Parse(arguments); err != nil {
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
	fileStore, err := store.OpenFile(*dataDirectory)
	if err != nil {
		return err
	}
	engine, err := rintime.Open(fileStore, policy.Deterministic{})
	if err != nil {
		return err
	}
	var snapshot protocol.Snapshot
	if *revision == 0 {
		state, stateErr := engine.State(protocol.SessionRequest{ProtocolVersion: protocol.Version, SessionID: *sessionID})
		if stateErr != nil {
			return stateErr
		}
		snapshot, err = rintime.SnapshotOf(state)
	} else {
		snapshot, err = engine.Replay(protocol.ReplayRequest{
			ProtocolVersion: protocol.Version, SessionID: *sessionID, Revision: *revision,
		})
	}
	if err != nil {
		return err
	}
	timeline, err := inspectTimeline(engine, *sessionID, snapshot.State.Revision, *timelineLimit)
	if err != nil {
		return err
	}
	pending := 0
	for _, proposal := range snapshot.State.Proposals {
		if proposal.Status == "pending" {
			pending++
		}
	}
	result := inspectOutput{
		ProtocolVersion: protocol.Version, SessionID: snapshot.State.SessionID,
		Binding: snapshot.State.Binding, Revision: snapshot.State.Revision,
		WorldRevision: snapshot.State.WorldRevision, Tick: snapshot.State.Tick,
		Features:   append([]string(nil), snapshot.State.Features...),
		ActorCount: len(snapshot.State.Actors), PendingProposals: pending,
		ArbitrationCount: len(snapshot.State.Arbitrations), StateHash: snapshot.StateHash,
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
	for {
		pageStart := after
		page, err := engine.Timeline(protocol.TimelineRequest{
			ProtocolVersion: protocol.Version, SessionID: sessionID,
			AfterRevision: after, Limit: 256,
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
			after = entry.Sequence
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
