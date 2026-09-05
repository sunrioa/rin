// Package changefeed provides bounded process-local invalidations. It is never
// an authority log; consumers fall back to a state scan if their cursor expires.
package changefeed

const Capacity = 512

type Feed[T any] struct {
	revision uint64
	entries  [Capacity]T
}

// The owner supplies synchronization for both Append and Since.
func (feed *Feed[T]) Append(value T) {
	feed.entries[feed.revision%Capacity] = value
	feed.revision++
}

func (feed *Feed[T]) Since(after uint64) (values []T, revision uint64, overflow bool) {
	revision = feed.revision
	if after > revision || revision-after > Capacity {
		return nil, revision, true
	}
	if after == revision {
		return nil, revision, false
	}
	values = make([]T, 0, revision-after)
	for cursor := after; cursor < revision; cursor++ {
		values = append(values, feed.entries[cursor%Capacity])
	}
	return values, revision, false
}
