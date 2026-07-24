package runtime

import (
	"errors"
	"testing"

	"github.com/sunrioa/rin/protocol"
)

func TestGoalProgressAccumulatorStopsAtJSONIntegerCeiling(t *testing.T) {
	maximum := int64(protocol.MaxJSONSafeInteger)
	for _, test := range []struct {
		name        string
		accumulator int64
		delta       int
	}{
		{"positive", maximum, 1},
		{"negative", -maximum, -1},
	} {
		t.Run(test.name, func(t *testing.T) {
			actor := protocol.ActorState{ActorSeed: protocol.ActorSeed{
				Goals: []protocol.Goal{{
					ID:                  "goal.safe-integer",
					TargetProgress:      1,
					ProgressAccumulator: test.accumulator,
				}},
			}}
			err := applyGoalProgress(&actor, "goal.safe-integer", test.delta, "", 0, "")
			if !errors.Is(err, ErrCorruptLog) {
				t.Fatalf("error=%v, want ErrCorruptLog", err)
			}
			if actor.Goals[0].ProgressAccumulator != test.accumulator {
				t.Fatalf("overflow changed accumulator to %d", actor.Goals[0].ProgressAccumulator)
			}
		})
	}

	actor := protocol.ActorState{ActorSeed: protocol.ActorSeed{
		Goals: []protocol.Goal{{
			ID:                  "goal.safe-integer",
			TargetProgress:      1,
			ProgressAccumulator: maximum - 1,
		}},
	}}
	if err := applyGoalProgress(&actor, "goal.safe-integer", 1, "", 0, ""); err != nil {
		t.Fatalf("exact ceiling rejected: %v", err)
	}
	if actor.Goals[0].ProgressAccumulator != maximum {
		t.Fatalf("accumulator=%d, want %d", actor.Goals[0].ProgressAccumulator, maximum)
	}
}
