package runtime

import (
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/sunrioa/rin/protocol"
)

const (
	memoryCompactionBatch = 16
	maxMemorySummaries    = 32
	summaryMergeBatch     = 4
	maxSummarySources     = 64
	maxMemorySummaryLevel = 16
)

func compactActorMemories(
	sessionID string,
	actor *protocol.ActorState,
	revision uint64,
	states ...*protocol.SessionState,
) error {
	var state *protocol.SessionState
	if len(states) > 0 {
		state = states[0]
	}
	for len(actor.Memories) > maxMemories {
		window := len(actor.Memories) / 2
		if window < memoryCompactionBatch {
			window = memoryCompactionBatch
		}
		indexes := make([]int, window)
		for index := range indexes {
			indexes[index] = index
		}
		sort.Slice(indexes, func(i, j int) bool {
			left := actor.Memories[indexes[i]]
			right := actor.Memories[indexes[j]]
			if left.Importance != right.Importance {
				return left.Importance < right.Importance
			}
			if left.RecallCount != right.RecallCount {
				return left.RecallCount < right.RecallCount
			}
			if left.LastRecalledTick != right.LastRecalledTick {
				return left.LastRecalledTick < right.LastRecalledTick
			}
			if left.Tick != right.Tick {
				return left.Tick < right.Tick
			}
			return left.ID < right.ID
		})
		selectedIndexes := indexes[:memoryCompactionBatch]
		selected := make([]protocol.Memory, 0, len(selectedIndexes))
		selectedSet := make(map[int]struct{}, len(selectedIndexes))
		for _, index := range selectedIndexes {
			selected = append(selected, actor.Memories[index])
			selectedSet[index] = struct{}{}
		}
		sort.Slice(selected, func(i, j int) bool {
			if selected[i].Tick == selected[j].Tick {
				return selected[i].ID < selected[j].ID
			}
			return selected[i].Tick < selected[j].Tick
		})
		summary, err := summarizeMemories(sessionID, actor.ID, selected, revision)
		if err != nil {
			return err
		}
		retained := make([]protocol.Memory, 0, len(actor.Memories)-len(selected))
		for index, memory := range actor.Memories {
			if _, compacted := selectedSet[index]; !compacted {
				retained = append(retained, memory)
			}
		}
		actor.Memories = retained
		actor.MemorySummaries = append(actor.MemorySummaries, summary)
		replacements := make(map[string]string, len(selected))
		for _, memory := range selected {
			replacements[memory.ID] = summary.ID
		}
		rewriteRecalledMemoryReferences(state, actor, replacements)
	}
	for len(actor.MemorySummaries) > maxMemorySummaries {
		sortMemorySummaries(actor.MemorySummaries)
		selected := actor.MemorySummaries[:summaryMergeBatch]
		merged, err := mergeMemorySummaries(sessionID, actor.ID, selected, revision)
		if err != nil {
			return err
		}
		actor.MemorySummaries = append([]protocol.MemorySummary{merged}, actor.MemorySummaries[summaryMergeBatch:]...)
		replacements := make(map[string]string, len(selected))
		for _, summary := range selected {
			replacements[summary.ID] = merged.ID
		}
		rewriteRecalledMemoryReferences(state, actor, replacements)
	}
	sortMemorySummaries(actor.MemorySummaries)
	return nil
}

func summarizeMemories(sessionID, actorID string, memories []protocol.Memory, revision uint64) (protocol.MemorySummary, error) {
	memoryIDs := make([]string, 0, len(memories))
	eventIDs := make([]string, 0, len(memories))
	tags := make([]string, 0)
	texts := make([]string, 0, len(memories))
	importance := 1
	recallCount := 0
	lastRecalled := int64(0)
	for _, memory := range memories {
		memoryIDs = append(memoryIDs, memory.ID)
		eventIDs = append(eventIDs, memory.EventID)
		tags = append(tags, memory.Tags...)
		texts = append(texts, memory.Summary)
		if memory.Importance > importance {
			importance = memory.Importance
		}
		recallCount = saturatingRecallAdd(recallCount, memory.RecallCount)
		if memory.LastRecalledTick > lastRecalled {
			lastRecalled = memory.LastRecalledTick
		}
	}
	id, err := memorySummaryID(sessionID, actorID, 1, memoryIDs)
	if err != nil {
		return protocol.MemorySummary{}, err
	}
	return protocol.MemorySummary{
		ID: "summary." + id[:24], Level: 1, Summary: joinSummaryText(texts),
		Tags: boundedUnique(tags, 32), SourceMemoryIDs: boundedUnique(memoryIDs, maxSummarySources),
		SourceEventIDs: boundedUnique(eventIDs, maxSummarySources), StartTick: memories[0].Tick,
		EndTick: memories[len(memories)-1].Tick, Importance: importance, Reason: "episodic_capacity",
		CreatedRevision: revision, RecallCount: recallCount, LastRecalledTick: lastRecalled,
	}, nil
}

func mergeMemorySummaries(sessionID, actorID string, summaries []protocol.MemorySummary, revision uint64) (protocol.MemorySummary, error) {
	sortMemorySummaries(summaries)
	sourceMemoryIDs := make([]string, 0)
	sourceEventIDs := make([]string, 0)
	tags := make([]string, 0)
	texts := make([]string, 0, len(summaries))
	identityIDs := make([]string, 0, len(summaries))
	maxLevel := 1
	importance := 1
	recallCount := 0
	lastRecalled := int64(0)
	endTick := int64(0)
	for _, summary := range summaries {
		identityIDs = append(identityIDs, summary.ID)
		sourceMemoryIDs = append(sourceMemoryIDs, summary.SourceMemoryIDs...)
		sourceEventIDs = append(sourceEventIDs, summary.SourceEventIDs...)
		tags = append(tags, summary.Tags...)
		texts = append(texts, summary.Summary)
		if summary.Level > maxLevel {
			maxLevel = summary.Level
		}
		if summary.Importance > importance {
			importance = summary.Importance
		}
		recallCount = saturatingRecallAdd(recallCount, summary.RecallCount)
		if summary.LastRecalledTick > lastRecalled {
			lastRecalled = summary.LastRecalledTick
		}
		if summary.EndTick > endTick {
			endTick = summary.EndTick
		}
	}
	level := maxLevel + 1
	if maxLevel >= maxMemorySummaryLevel {
		level = maxMemorySummaryLevel
	}
	id, err := memorySummaryID(sessionID, actorID, level, identityIDs)
	if err != nil {
		return protocol.MemorySummary{}, err
	}
	return protocol.MemorySummary{
		ID: "summary." + id[:24], Level: level, Summary: joinSummaryText(texts),
		Tags: boundedUnique(tags, 32), SourceMemoryIDs: boundedUnique(sourceMemoryIDs, maxSummarySources),
		SourceEventIDs: boundedUnique(sourceEventIDs, maxSummarySources), StartTick: summaries[0].StartTick,
		EndTick: endTick, Importance: importance, Reason: "archive_capacity",
		CreatedRevision: revision, RecallCount: recallCount, LastRecalledTick: lastRecalled,
	}, nil
}

func saturatingRecallAdd(total, value int) int {
	if total >= maxRecallCount || value >= maxRecallCount-total {
		return maxRecallCount
	}
	return total + value
}

func memorySummaryID(sessionID, actorID string, level int, sourceIDs []string) (string, error) {
	return hashJSON(struct {
		SessionID string   `json:"session_id"`
		ActorID   string   `json:"actor_id"`
		Level     int      `json:"level"`
		SourceIDs []string `json:"source_ids"`
	}{SessionID: sessionID, ActorID: actorID, Level: level, SourceIDs: sourceIDs})
}

func joinSummaryText(values []string) string {
	var builder strings.Builder
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		separator := ""
		if builder.Len() > 0 {
			separator = " | "
		}
		candidate := builder.String() + separator + value
		if utf8.RuneCountInString(candidate) > 1000 {
			remaining := 1000 - utf8.RuneCountInString(builder.String()+separator)
			if remaining > 0 {
				builder.WriteString(separator)
				builder.WriteString(string([]rune(value)[:min(remaining, utf8.RuneCountInString(value))]))
			}
			break
		}
		builder.WriteString(separator)
		builder.WriteString(value)
	}
	return builder.String()
}

func boundedUnique(values []string, limit int) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, min(len(values), limit))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
		if len(result) == limit {
			break
		}
	}
	return result
}

func sortMemorySummaries(values []protocol.MemorySummary) {
	sort.Slice(values, func(i, j int) bool {
		if values[i].StartTick == values[j].StartTick {
			return values[i].ID < values[j].ID
		}
		return values[i].StartTick < values[j].StartTick
	})
}
