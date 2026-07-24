package runtime

import (
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/sunrioa/rin/protocol"
)

const (
	memoryCompactionBatch   = 16
	maxMemorySummaries      = 32
	summaryMergeBatch       = 4
	maxSummarySources       = 64
	maxMemorySummaryLevel   = 16
	maxMemorySummaryRunes   = 1000
	minSummaryFragmentRunes = 32
)

type summaryTextFragment struct {
	text       string
	importance int
}

type summaryTextSegment struct {
	summaryTextFragment
	originalIndex int
	fixed         bool
}

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
		// Keep the oldest-four lineage stable: Proposal events persist recalled
		// Summary IDs, so changing merge membership would make an older event
		// log fail replay after an upgrade. Fairness within that compatible
		// lineage is handled by bounded text and source sampling below.
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
	memories = append([]protocol.Memory(nil), memories...)
	sort.Slice(memories, func(i, j int) bool {
		if memories[i].Tick == memories[j].Tick {
			return memories[i].ID < memories[j].ID
		}
		return memories[i].Tick < memories[j].Tick
	})
	memoryIDs := make([]string, 0, len(memories))
	eventIDs := make([]string, 0, len(memories))
	tags := make([]string, 0)
	texts := make([]summaryTextFragment, 0, len(memories))
	importance := 1
	recallCount := 0
	lastRecalled := int64(0)
	for _, memory := range memories {
		memoryIDs = append(memoryIDs, memory.ID)
		eventIDs = append(eventIDs, memory.EventID)
		tags = append(tags, memory.Tags...)
		texts = append(texts, summaryTextFragment{
			text:       memory.Summary,
			importance: memory.Importance,
		})
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
		Tags:            boundedRepresentativeUnique(tags, 32),
		SourceMemoryIDs: boundedRepresentativeUnique(memoryIDs, maxSummarySources),
		SourceEventIDs:  boundedRepresentativeUnique(eventIDs, maxSummarySources), StartTick: memories[0].Tick,
		EndTick: memories[len(memories)-1].Tick, Importance: importance, Reason: "episodic_capacity",
		CreatedRevision: revision, RecallCount: recallCount, LastRecalledTick: lastRecalled,
	}, nil
}

func mergeMemorySummaries(sessionID, actorID string, summaries []protocol.MemorySummary, revision uint64) (protocol.MemorySummary, error) {
	summaries = append([]protocol.MemorySummary(nil), summaries...)
	sortMemorySummaries(summaries)
	tags := make([]string, 0)
	texts := make([]summaryTextFragment, 0, len(summaries))
	identityIDs := make([]string, 0, len(summaries))
	maxLevel := 1
	importance := 1
	recallCount := 0
	lastRecalled := int64(0)
	endTick := int64(0)
	for _, summary := range summaries {
		identityIDs = append(identityIDs, summary.ID)
		tags = append(tags, summary.Tags...)
		texts = append(texts, summaryTextFragment{
			text:       summary.Summary,
			importance: summary.Importance,
		})
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
		Tags: boundedRepresentativeUnique(tags, 32),
		SourceMemoryIDs: boundedSummarySources(
			summaries,
			func(summary protocol.MemorySummary) []string { return summary.SourceMemoryIDs },
			maxSummarySources,
		),
		SourceEventIDs: boundedSummarySources(
			summaries,
			func(summary protocol.MemorySummary) []string { return summary.SourceEventIDs },
			maxSummarySources,
		), StartTick: summaries[0].StartTick,
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

func joinSummaryText(values []summaryTextFragment) string {
	fragments := make([]summaryTextFragment, 0, len(values))
	for _, value := range values {
		value.text = strings.TrimSpace(value.text)
		if value.text == "" {
			continue
		}
		if value.importance < 1 {
			value.importance = 1
		} else if value.importance > 5 {
			value.importance = 5
		}
		fragments = append(fragments, value)
	}
	if len(fragments) == 0 {
		return ""
	}
	if len(fragments) == 1 {
		return boundedTextExcerpt(fragments[0].text, maxMemorySummaryRunes)
	}
	segments := make([]summaryTextSegment, 0, len(fragments)+1)
	head, remainder := splitSummaryHead(
		fragments[0].text,
		minSummaryFragmentRunes,
	)
	segments = append(segments, summaryTextSegment{
		summaryTextFragment: summaryTextFragment{
			text:       head,
			importance: fragments[0].importance,
		},
		originalIndex: 0,
		fixed:         true,
	})
	dynamic := make([]summaryTextSegment, 0, len(fragments)-1)
	if remainder != "" {
		dynamic = append(dynamic, summaryTextSegment{
			summaryTextFragment: summaryTextFragment{
				text:       remainder,
				importance: fragments[0].importance,
			},
			originalIndex: 0,
		})
	}
	for index := 1; index < len(fragments)-1; index++ {
		dynamic = append(dynamic, summaryTextSegment{
			summaryTextFragment: fragments[index],
			originalIndex:       index,
		})
	}
	sort.SliceStable(dynamic, func(i, j int) bool {
		if dynamic[i].importance != dynamic[j].importance {
			return dynamic[i].importance > dynamic[j].importance
		}
		return dynamic[i].originalIndex > dynamic[j].originalIndex
	})
	segments = append(segments, dynamic...)
	last := len(fragments) - 1
	segments = append(segments, summaryTextSegment{
		summaryTextFragment: fragments[last],
		originalIndex:       last,
	})
	const separator = " | "
	contentBudget := maxMemorySummaryRunes -
		utf8.RuneCountInString(separator)*(len(segments)-1)
	budgets := summaryTextBudgets(segments, contentBudget, len(fragments))
	parts := make([]string, 0, len(segments))
	for index, segment := range segments {
		parts = append(parts, boundedTextExcerpt(segment.text, budgets[index]))
	}
	return strings.Join(parts, separator)
}

func splitSummaryHead(value string, limit int) (string, string) {
	runes := []rune(value)
	if limit <= 0 {
		return "", value
	}
	if len(runes) <= limit {
		return value, ""
	}
	return string(runes[:limit]), strings.TrimSpace(string(runes[limit:]))
}

func summaryTextBudgets(values []summaryTextSegment, total, fragmentCount int) []int {
	budgets := make([]int, len(values))
	if len(values) == 0 || total <= 0 {
		return budgets
	}
	lengths := make([]int, len(values))
	remaining := total
	dynamic := 0
	for index, value := range values {
		lengths[index] = utf8.RuneCountInString(value.text)
		if value.fixed {
			budgets[index] = min(minSummaryFragmentRunes, lengths[index])
		} else {
			dynamic++
		}
		remaining -= budgets[index]
	}
	base := 0
	if dynamic > 0 {
		base = min(minSummaryFragmentRunes, remaining/dynamic)
	}
	for index, value := range values {
		if value.fixed {
			continue
		}
		budgets[index] = min(base, lengths[index])
		remaining -= budgets[index]
	}
	for remaining > 0 {
		totalWeight := 0
		for index, value := range values {
			if !value.fixed && budgets[index] < lengths[index] {
				totalWeight += summaryTextWeight(
					value.summaryTextFragment,
					value.originalIndex,
					fragmentCount,
				)
			}
		}
		if totalWeight == 0 {
			// Dynamic fragments are fully represented. Use any spare capacity
			// for the fixed oldest anchor instead of discarding most of the
			// bounded summary budget when newer fragments are short.
			for index, value := range values {
				if !value.fixed || budgets[index] >= lengths[index] {
					continue
				}
				share := min(remaining, lengths[index]-budgets[index])
				budgets[index] += share
				remaining -= share
				if remaining == 0 {
					break
				}
			}
			break
		}
		startingRemaining := remaining
		granted := 0
		for index, value := range values {
			if value.fixed || budgets[index] >= lengths[index] {
				continue
			}
			share := startingRemaining *
				summaryTextWeight(
					value.summaryTextFragment,
					value.originalIndex,
					fragmentCount,
				) / totalWeight
			if share == 0 {
				continue
			}
			share = min(share, lengths[index]-budgets[index])
			budgets[index] += share
			remaining -= share
			granted += share
		}
		if granted > 0 {
			continue
		}
		best := -1
		for index, value := range values {
			if value.fixed || budgets[index] >= lengths[index] {
				continue
			}
			if best == -1 ||
				summaryTextWeight(
					value.summaryTextFragment,
					value.originalIndex,
					fragmentCount,
				) >
					summaryTextWeight(
						values[best].summaryTextFragment,
						values[best].originalIndex,
						fragmentCount,
					) ||
				(summaryTextWeight(
					value.summaryTextFragment,
					value.originalIndex,
					fragmentCount,
				) ==
					summaryTextWeight(
						values[best].summaryTextFragment,
						values[best].originalIndex,
						fragmentCount,
					) &&
					value.originalIndex > values[best].originalIndex) {
				best = index
			}
		}
		if best == -1 {
			break
		}
		budgets[best]++
		remaining--
	}
	return budgets
}

func summaryTextWeight(value summaryTextFragment, index, count int) int {
	recency := 0
	if count > 1 {
		recency = index * 3 / (count - 1)
	}
	weight := (value.importance-1)*4 + recency
	if weight == 0 {
		return 1
	}
	return weight
}

func boundedTextExcerpt(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	if limit <= 0 {
		return ""
	}
	if limit == 1 {
		return "…"
	}
	content := limit - 1
	head := (content + 1) / 2
	tail := content - head
	return string(runes[:head]) + "…" + string(runes[len(runes)-tail:])
}

func boundedRepresentativeUnique(values []string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	if len(unique) <= limit {
		return unique
	}
	if limit == 1 {
		return []string{unique[len(unique)-1]}
	}
	result := make([]string, limit)
	for index := range result {
		sourceIndex := index * (len(unique) - 1) / (limit - 1)
		result[index] = unique[sourceIndex]
	}
	return result
}

type summarySourceCandidate struct {
	value       string
	tick        int64
	summary     int
	sourceIndex int
}

func boundedSummarySources(
	summaries []protocol.MemorySummary,
	sources func(protocol.MemorySummary) []string,
	limit int,
) []string {
	if limit <= 0 {
		return nil
	}
	candidates := make([]summarySourceCandidate, 0, len(summaries)*maxSummarySources)
	for summaryIndex, summary := range summaries {
		values := sources(summary)
		for sourceIndex, value := range values {
			if value == "" {
				continue
			}
			candidates = append(candidates, summarySourceCandidate{
				value:       value,
				tick:        representativeTick(summary.StartTick, summary.EndTick, sourceIndex, len(values)),
				summary:     summaryIndex,
				sourceIndex: sourceIndex,
			})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		return lessSummarySourceCandidate(candidates[i], candidates[j])
	})
	seen := make(map[string]struct{}, len(candidates))
	unique := candidates[:0]
	for _, candidate := range candidates {
		if _, exists := seen[candidate.value]; exists {
			continue
		}
		seen[candidate.value] = struct{}{}
		unique = append(unique, candidate)
	}
	candidates = unique
	if len(candidates) <= limit {
		result := make([]string, len(candidates))
		for index, candidate := range candidates {
			result[index] = candidate.value
		}
		return result
	}
	if limit == 1 {
		return []string{candidates[len(candidates)-1].value}
	}

	selected := make([]bool, len(candidates))
	selected[0] = true
	selected[len(candidates)-1] = true
	selectedIndexes := []int{0, len(candidates) - 1}
	for sample := 1; sample < limit-1; sample++ {
		target := representativeTick(
			candidates[0].tick,
			candidates[len(candidates)-1].tick,
			sample,
			limit,
		)
		best := -1
		for index, candidate := range candidates {
			if selected[index] {
				continue
			}
			if best == -1 ||
				tickDistance(candidate.tick, target) <
					tickDistance(candidates[best].tick, target) ||
				(tickDistance(candidate.tick, target) ==
					tickDistance(candidates[best].tick, target) &&
					lessSummarySourceCandidate(candidate, candidates[best])) {
				best = index
			}
		}
		if best == -1 {
			break
		}
		selected[best] = true
		selectedIndexes = append(selectedIndexes, best)
	}
	sort.Ints(selectedIndexes)
	result := make([]string, 0, len(selectedIndexes))
	for _, index := range selectedIndexes {
		result = append(result, candidates[index].value)
	}
	return result
}

func representativeTick(start, end int64, index, count int) int64 {
	if count <= 1 || end <= start {
		return start
	}
	divisor := int64(count - 1)
	position := int64(index)
	span := end - start
	return start + (span/divisor)*position + (span%divisor)*position/divisor
}

func tickDistance(left, right int64) int64 {
	if left >= right {
		return left - right
	}
	return right - left
}

func lessSummarySourceCandidate(left, right summarySourceCandidate) bool {
	if left.tick != right.tick {
		return left.tick < right.tick
	}
	if left.summary != right.summary {
		return left.summary < right.summary
	}
	if left.sourceIndex != right.sourceIndex {
		return left.sourceIndex < right.sourceIndex
	}
	return left.value < right.value
}

func sortMemorySummaries(values []protocol.MemorySummary) {
	sort.Slice(values, func(i, j int) bool {
		if values[i].StartTick == values[j].StartTick {
			return values[i].ID < values[j].ID
		}
		return values[i].StartTick < values[j].StartTick
	})
}
