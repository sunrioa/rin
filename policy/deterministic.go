// Package policy provides lightweight policies for Rin runtimes.
package policy

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"

	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
)

// Deterministic is an offline policy suitable for tests, local play, and
// games that want agent-like selection without a language model.
type Deterministic struct {
	MemoryLimit int
}

func (p Deterministic) Propose(ctx context.Context, input rinruntime.DecisionContext) (rinruntime.DecisionDraft, error) {
	if err := ctx.Err(); err != nil {
		return rinruntime.DecisionDraft{}, err
	}
	memoryLimit := p.MemoryLimit
	if memoryLimit <= 0 || memoryLimit > 8 {
		memoryLimit = 3
	}
	memories := retrieveMemories(input.Actor, input.Request.Tags, input.Request.Tick, memoryLimit)
	goals := append([]protocol.Goal(nil), input.Actor.Goals...)
	goals = append(goals, input.Request.CandidateGoals...)
	goal := selectGoal(goals)
	boundary, triggered := triggeredBoundary(input.Actor.Boundaries, input.Request.Tags)

	var selected protocol.ActionOffer
	if triggered {
		goal = nil
		var found bool
		for _, action := range input.Request.Offers {
			if action.OfferID == boundary.Response {
				selected = action
				found = true
				break
			}
		}
		if !found {
			return rinruntime.DecisionDraft{}, rinruntime.ErrNoSafeAction
		}
	} else {
		selected = selectAction(input, goal, memories)
	}

	stance := selected.OfferID
	if triggered {
		// Boundary.Response is the authoritative refusal/redirection/wait
		// stance even when the matching host offer has a domain-specific
		// capability.
		stance = boundary.Response
	} else if stance != "refuse" && stance != "redirect" && stance != "wait" && stance != "partial" {
		stance = "engage"
	}
	memoryIDs := make([]string, 0, len(memories))
	for _, memory := range memories {
		memoryIDs = append(memoryIDs, memory.ID)
	}
	draft := rinruntime.DecisionDraft{
		OfferID:           selected.OfferID,
		Stance:            stance,
		PolicySource:      "deterministic",
		RecalledMemoryIDs: memoryIDs,
	}
	if goal != nil {
		draft.GoalID = goal.ID
	}
	if triggered {
		draft.BoundaryID = boundary.ID
	}
	return draft, nil
}

func triggeredBoundary(boundaries []protocol.Boundary, tags []string) (protocol.Boundary, bool) {
	set := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		set[tag] = struct{}{}
	}
	for _, boundary := range boundaries {
		for _, trigger := range boundary.TriggerTags {
			if _, exists := set[trigger]; exists {
				return boundary, true
			}
		}
	}
	return protocol.Boundary{}, false
}

func selectGoal(goals []protocol.Goal) *protocol.Goal {
	active := make([]protocol.Goal, 0, len(goals))
	for _, goal := range goals {
		if goal.Status == "active" {
			active = append(active, goal)
		}
	}
	if len(active) == 0 {
		return nil
	}
	sort.Slice(active, func(i, j int) bool {
		if active[i].Priority == active[j].Priority {
			if active[i].Progress == active[j].Progress {
				return active[i].ID < active[j].ID
			}
			return active[i].Progress < active[j].Progress
		}
		return active[i].Priority > active[j].Priority
	})
	return &active[0]
}

func selectAction(input rinruntime.DecisionContext, goal *protocol.Goal, memories []protocol.Memory) protocol.ActionOffer {
	type scoredAction struct {
		action protocol.ActionOffer
		score  int64
		tie    uint64
	}
	preferred := make(map[string]struct{})
	if goal != nil {
		for _, value := range goal.PreferredActions {
			preferred[value] = struct{}{}
		}
	}
	tags := make(map[string]struct{}, len(input.Request.Tags))
	for _, tag := range input.Request.Tags {
		tags[tag] = struct{}{}
	}
	memoryTags := make(map[string]struct{})
	for _, memory := range memories {
		for _, tag := range memory.Tags {
			memoryTags[tag] = struct{}{}
		}
	}
	scored := make([]scoredAction, 0, len(input.Request.Offers))
	for _, action := range input.Request.Offers {
		var score int64
		if _, exists := preferred[action.OfferID]; exists {
			score += 100
		}
		if _, exists := preferred[action.Capability.ID]; exists {
			score += 80
		}
		if _, exists := tags[action.Capability.ID]; exists {
			score += 20
		}
		// Game-authored tags are the safe bridge between recalled private
		// context and an allowlisted action. A match is deliberately weaker
		// than an active Goal preference and is counted once, so duplicate
		// memories cannot amplify an action without bound.
		if _, exists := memoryTags[action.OfferID]; exists {
			score += 30
		} else if _, exists := memoryTags[action.Capability.ID]; exists {
			score += 30
		}
		for index := len(input.Actor.RecentActions) - 1; index >= 0 && index >= len(input.Actor.RecentActions)-4; index-- {
			if input.Actor.RecentActions[index].Action.OfferID == action.OfferID {
				score -= 15
			}
		}
		scored = append(scored, scoredAction{action: action, score: score, tie: tieBreak(input, action.OfferID)})
	}
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].score == scored[j].score {
			if scored[i].tie == scored[j].tie {
				return scored[i].action.OfferID < scored[j].action.OfferID
			}
			return scored[i].tie < scored[j].tie
		}
		return scored[i].score > scored[j].score
	})
	return scored[0].action
}

func tieBreak(input rinruntime.DecisionContext, actionID string) uint64 {
	payload := fmt.Sprintf("%d\x00%s\x00%s\x00%s\x00%s", input.State.Seed, input.State.SessionID, input.Request.RequestID, input.Actor.ID, actionID)
	digest := sha256.Sum256([]byte(payload))
	return binary.BigEndian.Uint64(digest[:8])
}

func retrieveMemories(actor protocol.ActorState, tags []string, tick int64, limit int) []protocol.Memory {
	query := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		query[tag] = struct{}{}
	}
	type scoredMemory struct {
		memory protocol.Memory
		score  int64
	}
	values := make([]scoredMemory, 0, len(actor.Memories)+len(actor.MemorySummaries))
	for _, memory := range actor.Memories {
		score := int64(memory.Importance * 10)
		age := tick - memory.Tick
		if age < 0 {
			age = 0
		}
		if age < 10 {
			score += 10 - age
		}
		if memory.Quote != "" {
			score += 4
		}
		if memory.RecallCount == 0 {
			score += 5
		}
		for _, tag := range memory.Tags {
			if _, exists := query[tag]; exists {
				score += 8
			}
		}
		values = append(values, scoredMemory{memory: memory, score: score})
	}
	for _, summary := range actor.MemorySummaries {
		memory := protocol.Memory{
			ID: summary.ID, EventID: summary.ID, Tick: summary.EndTick,
			Summary: summary.Summary, Tags: append([]string(nil), summary.Tags...),
			Importance: summary.Importance, CreatedRevision: summary.CreatedRevision,
			RecallCount: summary.RecallCount, LastRecalledTick: summary.LastRecalledTick,
		}
		score := int64(memory.Importance * 10)
		age := tick - memory.Tick
		if age < 0 {
			age = 0
		}
		if age < 10 {
			score += 10 - age
		}
		if memory.RecallCount == 0 {
			score += 5
		}
		for _, tag := range memory.Tags {
			if _, exists := query[tag]; exists {
				score += 8
			}
		}
		values = append(values, scoredMemory{memory: memory, score: score})
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].score == values[j].score {
			if values[i].memory.Tick == values[j].memory.Tick {
				return values[i].memory.ID < values[j].memory.ID
			}
			return values[i].memory.Tick > values[j].memory.Tick
		}
		return values[i].score > values[j].score
	})
	if len(values) > limit {
		values = values[:limit]
	}
	result := make([]protocol.Memory, 0, len(values))
	for _, value := range values {
		result = append(result, value.memory)
	}
	return result
}
