package changefeed

import "testing"

func TestSlowConsumerFallsBackWithoutLosingConcurrentAppend(t *testing.T) {
	var feed Feed[int]
	feed.Append(1)
	values, cursor, overflow := feed.Since(0)
	if overflow || cursor != 1 || len(values) != 1 || values[0] != 1 {
		t.Fatal("initial event missing")
	}
	feed.Append(2)
	next, _, overflow := feed.Since(cursor)
	if overflow || len(next) != 1 || next[0] != 2 {
		t.Fatal("append after snapshot lost")
	}
	for i := 0; i < Capacity; i++ {
		feed.Append(i)
	}
	if _, _, overflow := feed.Since(cursor); !overflow {
		t.Fatal("expired cursor silently skipped events")
	}
	if _, _, overflow := feed.Since(cursor + 10000); !overflow {
		t.Fatal("restart cursor was not reset")
	}
}
