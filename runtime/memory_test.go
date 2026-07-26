package runtime

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sunrioa/rin/protocol"
)

func TestMemoryArchiveMergesInsteadOfSilentlyDroppingSummaries(t *testing.T) {
	actor := protocol.ActorState{ActorSeed: protocol.ActorSeed{ID: "npc.archive"}}
	for index := 0; index < 700; index++ {
		actor.Memories = append(actor.Memories, protocol.Memory{
			ID: "memory." + fixedID(index), EventID: "event." + fixedID(index), Tick: int64(index),
			Summary: "A bounded event summary.", Tags: []string{"history"}, Importance: 2,
			CreatedRevision: uint64(index + 1),
		})
	}
	copyActor := actor
	copyActor.Memories = append([]protocol.Memory(nil), actor.Memories...)
	if err := compactActorMemories("session.archive", &actor, 701); err != nil {
		t.Fatal(err)
	}
	if err := compactActorMemories("session.archive", &copyActor, 701); err != nil {
		t.Fatal(err)
	}
	if len(actor.Memories) > maxMemories || len(actor.MemorySummaries) > maxMemorySummaries {
		t.Fatalf("archive exceeded bounds: memories=%d summaries=%d", len(actor.Memories), len(actor.MemorySummaries))
	}
	higherLevel := false
	for _, summary := range actor.MemorySummaries {
		higherLevel = higherLevel || summary.Level > 1
	}
	if !higherLevel {
		t.Fatalf("expected archive summaries to merge: %+v", actor.MemorySummaries)
	}
	if !reflect.DeepEqual(actor, copyActor) {
		t.Fatal("identical memory streams produced different archives")
	}
}

func TestHierarchicalSummaryKeepsTemporalSalienceAndSourceCoverage(t *testing.T) {
	build := func(t *testing.T) protocol.MemorySummary {
		t.Helper()
		current := protocol.MemorySummary{
			ID:               "summary.root",
			Level:            1,
			Summary:          "OLDEST-ANCHOR " + strings.Repeat("o", 1_100),
			SourceMemoryIDs:  summarySourceIDs("memory.root", 0),
			SourceEventIDs:   summarySourceIDs("event.root", 0),
			StartTick:        1,
			EndTick:          64,
			Importance:       1,
			Reason:           "episodic_capacity",
			CreatedRevision:  1,
			LastRecalledTick: 1,
		}
		for generation := 1; generation <= 48; generation++ {
			summaries := []protocol.MemorySummary{current}
			for child := 1; child <= 3; child++ {
				prefix := fmt.Sprintf("MIDDLE-%02d-%d ", generation, child)
				importance := 1
				if child == 2 {
					prefix = fmt.Sprintf("IMPORTANT-CANARY-%02d ", generation)
					importance = 5
				}
				suffix := ""
				if child == 3 {
					suffix = fmt.Sprintf(" LATEST-CANARY-%02d", generation)
				}
				base := generation*3 + child
				summaries = append(summaries, protocol.MemorySummary{
					ID:               fmt.Sprintf("summary.g%02d.%d", generation, child),
					Level:            min(generation+1, maxMemorySummaryLevel),
					Summary:          prefix + strings.Repeat("x", 1_100) + suffix,
					SourceMemoryIDs:  summarySourceIDs("memory", base),
					SourceEventIDs:   summarySourceIDs("event", base),
					StartTick:        int64(base*64 + 1),
					EndTick:          int64((base + 1) * 64),
					Importance:       importance,
					Reason:           "archive_capacity",
					CreatedRevision:  uint64(generation + 1),
					LastRecalledTick: int64(base),
				})
			}
			var err error
			current, err = mergeMemorySummaries(
				"session.long-summary",
				"npc.mira",
				summaries,
				uint64(generation+1),
			)
			if err != nil {
				t.Fatalf("generation %d: %v", generation, err)
			}
			for _, canary := range []string{
				"OLDEST-ANCHOR",
				fmt.Sprintf("IMPORTANT-CANARY-%02d", generation),
				fmt.Sprintf("LATEST-CANARY-%02d", generation),
			} {
				if !strings.Contains(current.Summary, canary) {
					t.Fatalf("generation %d lost %q: %q", generation, canary, current.Summary)
				}
			}
			if generation > 1 {
				previousImportant := fmt.Sprintf(
					"IMPORTANT-CANARY-%02d",
					generation-1,
				)
				if !strings.Contains(current.Summary, previousImportant) {
					t.Fatalf(
						"generation %d gave no next-level retention opportunity to %q: %q",
						generation,
						previousImportant,
						current.Summary,
					)
				}
			}
			if got := len(current.SourceMemoryIDs); got != maxSummarySources {
				t.Fatalf("generation %d source memory count = %d", generation, got)
			}
			if got := len(current.SourceEventIDs); got != maxSummarySources {
				t.Fatalf("generation %d source event count = %d", generation, got)
			}
			if current.SourceMemoryIDs[0] != "memory.root.00" ||
				current.SourceEventIDs[0] != "event.root.00" {
				t.Fatalf("generation %d lost the oldest source anchors", generation)
			}
			wantMemoryTail := fmt.Sprintf("memory.%03d.63", generation*3+3)
			wantEventTail := fmt.Sprintf("event.%03d.63", generation*3+3)
			if current.SourceMemoryIDs[len(current.SourceMemoryIDs)-1] != wantMemoryTail ||
				current.SourceEventIDs[len(current.SourceEventIDs)-1] != wantEventTail {
				t.Fatalf(
					"generation %d newest source anchors = %q/%q, want %q/%q",
					generation,
					current.SourceMemoryIDs[len(current.SourceMemoryIDs)-1],
					current.SourceEventIDs[len(current.SourceEventIDs)-1],
					wantMemoryTail,
					wantEventTail,
				)
			}
			if got := utf8.RuneCountInString(current.Summary); got > maxMemorySummaryRunes {
				t.Fatalf("generation %d summary length = %d", generation, got)
			}
		}
		coveredPeriods := make(map[string]struct{})
		for _, id := range current.SourceMemoryIDs {
			parts := strings.Split(id, ".")
			if len(parts) >= 3 {
				coveredPeriods[parts[1]] = struct{}{}
			}
		}
		if len(coveredPeriods) < 32 {
			t.Fatalf(
				"representative sources cover only %d periods after repeated merges: %v",
				len(coveredPeriods),
				current.SourceMemoryIDs,
			)
		}
		return current
	}

	first := build(t)
	second := build(t)
	if !reflect.DeepEqual(first, second) {
		t.Fatal("identical hierarchical inputs produced different summaries")
	}
}

func TestSummaryTextIncludesEachShortFragmentOnce(t *testing.T) {
	values := []summaryTextFragment{
		{text: "oldest-fragment", importance: 1},
		{text: "middle-fragment", importance: 2},
		{text: "important-fragment", importance: 5},
		{text: "newest-fragment", importance: 1},
	}
	summary := joinSummaryText(values)
	for _, value := range values {
		if count := strings.Count(summary, value.text); count != 1 {
			t.Fatalf("fragment %q occurs %d times in %q", value.text, count, summary)
		}
	}
}

func TestSummaryTextUsesSpareBudgetAfterShortRecentFragments(t *testing.T) {
	oldest := "OLDEST-ANCHOR " + strings.Repeat("o", maxMemorySummaryRunes)
	summary := joinSummaryText([]summaryTextFragment{
		{text: oldest, importance: 1},
		{text: "recent", importance: 5},
	})
	if got := utf8.RuneCountInString(summary); got != maxMemorySummaryRunes {
		t.Fatalf("summary used %d runes, want full %d-rune budget", got, maxMemorySummaryRunes)
	}
	if !strings.HasPrefix(summary, "OLDEST-ANCHOR") ||
		!strings.HasSuffix(summary, " | recent") {
		t.Fatalf("summary lost oldest/recent anchors: %q", summary)
	}
}

func summarySourceIDs(prefix string, generation int) []string {
	values := make([]string, maxSummarySources)
	for index := range values {
		if generation == 0 {
			values[index] = fmt.Sprintf("%s.%02d", prefix, index)
			continue
		}
		values[index] = fmt.Sprintf("%s.%03d.%02d", prefix, generation, index)
	}
	return values
}

func fixedID(value int) string {
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	if value == 0 {
		return "0"
	}
	buffer := make([]byte, 0, 8)
	for value > 0 {
		buffer = append(buffer, alphabet[value%len(alphabet)])
		value /= len(alphabet)
	}
	for left, right := 0, len(buffer)-1; left < right; left, right = left+1, right-1 {
		buffer[left], buffer[right] = buffer[right], buffer[left]
	}
	return string(buffer)
}
