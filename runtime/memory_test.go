package runtime

import (
	"reflect"
	"testing"

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
