package runtime

import (
	"encoding/json"
	"fmt"

	"github.com/sunrioa/rin/protocol"
)

// MaxInlineSnapshotBytes is the compact JSON size accepted by the portable
// inline Snapshot/Restore contract. The 16 MiB ceiling leaves ample room for
// the API envelope, Restore metadata, and EventRecord framing beneath the
// default 32 MiB HTTP request and client response limits.
const MaxInlineSnapshotBytes = 16 << 20

func checkInlineSnapshotSize(snapshot protocol.Snapshot, maximum int) (int, error) {
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return 0, err
	}
	size := len(payload)
	if size > maximum {
		return size, fmt.Errorf(
			"compact snapshot is %d bytes; inline limit is %d bytes",
			size,
			maximum,
		)
	}
	return size, nil
}

func snapshotTooLargeError(size int, cause error) error {
	return snapshotTooLargeErrorForLimit(size, MaxInlineSnapshotBytes, cause)
}

func snapshotTooLargeErrorForLimit(size int, maximum int, cause error) error {
	return NewFieldError(
		"snapshot_too_large",
		fmt.Sprintf(
			"compact snapshot is %d bytes and exceeds the %d-byte inline transport limit",
			size,
			maximum,
		),
		"snapshot",
		cause,
	)
}
