package runtime

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

	"github.com/sunrioa/rin/protocol"
)

const (
	identifierHotEntryLimit = 512
	identifierBloomWords    = 128
)

type identifierLedger struct {
	version          string
	coverageComplete bool
	hotRequests      map[string]protocol.RequestIdentity
	hotEvents        map[string]protocol.EventIdentity
	segments         []identifierSegment
	requestIndex     identifierHashIndex
	eventIndex       identifierHashIndex
}

type identifierSegment struct {
	payload      []byte
	requestBloom identifierBloom
	eventBloom   identifierBloom
}

type identifierBloom [identifierBloomWords]uint64

// identifierHashIndex is a small immutable LSM-style index. Each occupied
// level is sorted; sealing a hot segment merges only colliding levels. New-ID
// checks therefore inspect logarithmically many compact slices instead of
// walking every cold segment.
type identifierHashIndex struct {
	levels [][]uint64
}

type identifierSegmentPayload struct {
	Requests []identifierRequestEntry `json:"requests,omitempty"`
	Events   []identifierEventEntry   `json:"events,omitempty"`
}

type identifierRequestEntry struct {
	ID       string                   `json:"id"`
	Identity protocol.RequestIdentity `json:"identity"`
}

type identifierEventEntry struct {
	ID       string                 `json:"id"`
	Identity protocol.EventIdentity `json:"identity"`
}

func identifierLedgerFromHistory(
	history protocol.IdentifierHistory,
) (identifierLedger, error) {
	normalizeIdentifierHistory(&history)
	ledger := identifierLedger{
		version:          history.Version,
		coverageComplete: history.CoverageComplete,
		hotRequests:      make(map[string]protocol.RequestIdentity),
		hotEvents:        make(map[string]protocol.EventIdentity),
	}
	requestIDs := make([]string, 0, len(history.Requests))
	for requestID := range history.Requests {
		requestIDs = append(requestIDs, requestID)
	}
	sort.Strings(requestIDs)
	for _, requestID := range requestIDs {
		ledger.hotRequests[requestID] = history.Requests[requestID]
		if err := ledger.sealHotIfFull(); err != nil {
			return identifierLedger{}, err
		}
	}
	eventIDs := make([]string, 0, len(history.Events))
	for eventID := range history.Events {
		eventIDs = append(eventIDs, eventID)
	}
	sort.Strings(eventIDs)
	for _, eventID := range eventIDs {
		ledger.hotEvents[eventID] = history.Events[eventID]
		if err := ledger.sealHotIfFull(); err != nil {
			return identifierLedger{}, err
		}
	}
	return ledger, nil
}

func (l identifierLedger) capture() identifierLedger {
	captured := identifierLedger{
		version:          l.version,
		coverageComplete: l.coverageComplete,
		hotRequests: make(
			map[string]protocol.RequestIdentity,
			len(l.hotRequests),
		),
		hotEvents: make(
			map[string]protocol.EventIdentity,
			len(l.hotEvents),
		),
		segments: append([]identifierSegment(nil), l.segments...),
		requestIndex: identifierHashIndex{
			levels: append([][]uint64(nil), l.requestIndex.levels...),
		},
		eventIndex: identifierHashIndex{
			levels: append([][]uint64(nil), l.eventIndex.levels...),
		},
	}
	for requestID, identity := range l.hotRequests {
		captured.hotRequests[requestID] = identity
	}
	for eventID, identity := range l.hotEvents {
		captured.hotEvents[eventID] = identity
	}
	return captured
}

func (l identifierLedger) materialize() (
	protocol.IdentifierHistory,
	error,
) {
	history := newIdentifierHistory(l.coverageComplete)
	if l.version != "" {
		history.Version = l.version
	}
	for _, segment := range l.segments {
		payload, err := decodeIdentifierSegment(segment)
		if err != nil {
			return protocol.IdentifierHistory{}, err
		}
		for _, entry := range payload.Requests {
			history.Requests[entry.ID] = entry.Identity
		}
		for _, entry := range payload.Events {
			history.Events[entry.ID] = entry.Identity
		}
	}
	for requestID, identity := range l.hotRequests {
		history.Requests[requestID] = identity
	}
	for eventID, identity := range l.hotEvents {
		history.Events[eventID] = identity
	}
	return history, nil
}

func (l identifierLedger) request(
	requestID string,
) (protocol.RequestIdentity, bool, error) {
	if identity, found := l.hotRequests[requestID]; found {
		return identity, true, nil
	}
	hash := identifierBloomHash(requestID)
	if !l.requestIndex.contains(identifierHashValue(hash)) {
		return protocol.RequestIdentity{}, false, nil
	}
	for index := len(l.segments) - 1; index >= 0; index-- {
		segment := l.segments[index]
		if !segment.requestBloom.maybeContains(hash) {
			continue
		}
		payload, err := decodeIdentifierSegment(segment)
		if err != nil {
			return protocol.RequestIdentity{}, false, err
		}
		position := sort.Search(
			len(payload.Requests),
			func(position int) bool {
				return payload.Requests[position].ID >= requestID
			},
		)
		if position < len(payload.Requests) &&
			payload.Requests[position].ID == requestID {
			return payload.Requests[position].Identity, true, nil
		}
	}
	return protocol.RequestIdentity{}, false, nil
}

func (l identifierLedger) event(
	eventID string,
) (protocol.EventIdentity, bool, error) {
	if identity, found := l.hotEvents[eventID]; found {
		return identity, true, nil
	}
	hash := identifierBloomHash(eventID)
	if !l.eventIndex.contains(identifierHashValue(hash)) {
		return protocol.EventIdentity{}, false, nil
	}
	for index := len(l.segments) - 1; index >= 0; index-- {
		segment := l.segments[index]
		if !segment.eventBloom.maybeContains(hash) {
			continue
		}
		payload, err := decodeIdentifierSegment(segment)
		if err != nil {
			return protocol.EventIdentity{}, false, err
		}
		position := sort.Search(
			len(payload.Events),
			func(position int) bool {
				return payload.Events[position].ID >= eventID
			},
		)
		if position < len(payload.Events) &&
			payload.Events[position].ID == eventID {
			return payload.Events[position].Identity, true, nil
		}
	}
	return protocol.EventIdentity{}, false, nil
}

func (l identifierLedger) withDelta(
	delta identifierEventDelta,
) (identifierLedger, error) {
	next := l.capture()
	next.normalize()
	if delta.imported != nil {
		next.coverageComplete =
			next.coverageComplete && delta.imported.CoverageComplete
		for requestID, value := range delta.imported.Requests {
			existing, found, err := next.request(requestID)
			if err != nil {
				return identifierLedger{}, err
			}
			if !found {
				next.hotRequests[requestID] = value
			} else if !reflect.DeepEqual(existing, value) {
				next.hotRequests[requestID] = mergeRequestIdentity(
					existing,
					value,
				)
			}
		}
		for eventID, value := range delta.imported.Events {
			existing, found, err := next.event(eventID)
			if err != nil {
				return identifierLedger{}, err
			}
			if !found {
				next.hotEvents[eventID] = value
			} else if !reflect.DeepEqual(existing, value) {
				next.hotEvents[eventID] = mergeEventIdentity(
					existing,
					value,
				)
			}
		}
	}
	if existing, found, err := next.request(delta.event.RequestID); err != nil {
		return identifierLedger{}, err
	} else if found {
		next.hotRequests[delta.event.RequestID] = mergeRequestIdentity(
			existing,
			delta.request,
		)
	} else {
		next.hotRequests[delta.event.RequestID] = delta.request
	}
	for _, value := range delta.events {
		identity := protocol.EventIdentity{
			Kind:      value.kind,
			RequestID: delta.event.RequestID,
			Revision:  delta.event.Sequence,
		}
		existing, found, err := next.event(value.id)
		if err != nil {
			return identifierLedger{}, err
		}
		if found {
			identity = mergeEventIdentity(existing, identity)
		}
		if value.id != "" {
			next.hotEvents[value.id] = identity
		}
	}
	if err := next.sealHotIfFull(); err != nil {
		return identifierLedger{}, err
	}
	return next, nil
}

func (l *identifierLedger) normalize() {
	if l.version == "" {
		l.version = protocol.IdentifierHistoryVersion
	}
	if l.hotRequests == nil {
		l.hotRequests = make(map[string]protocol.RequestIdentity)
	}
	if l.hotEvents == nil {
		l.hotEvents = make(map[string]protocol.EventIdentity)
	}
}

func (l *identifierLedger) sealHotIfFull() error {
	l.normalize()
	if len(l.hotRequests)+len(l.hotEvents) < identifierHotEntryLimit {
		return nil
	}
	payload := identifierSegmentPayload{
		Requests: make(
			[]identifierRequestEntry,
			0,
			len(l.hotRequests),
		),
		Events: make(
			[]identifierEventEntry,
			0,
			len(l.hotEvents),
		),
	}
	for requestID, identity := range l.hotRequests {
		payload.Requests = append(payload.Requests, identifierRequestEntry{
			ID: requestID, Identity: identity,
		})
	}
	for eventID, identity := range l.hotEvents {
		payload.Events = append(payload.Events, identifierEventEntry{
			ID: eventID, Identity: identity,
		})
	}
	sort.Slice(payload.Requests, func(left, right int) bool {
		return payload.Requests[left].ID < payload.Requests[right].ID
	})
	sort.Slice(payload.Events, func(left, right int) bool {
		return payload.Events[left].ID < payload.Events[right].ID
	})
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode identifier segment: %w", err)
	}
	segment := identifierSegment{payload: encoded}
	requestHashes := make([]uint64, 0, len(payload.Requests))
	for _, entry := range payload.Requests {
		hash := identifierBloomHash(entry.ID)
		segment.requestBloom.add(hash)
		requestHashes = append(
			requestHashes,
			identifierHashValue(hash),
		)
	}
	eventHashes := make([]uint64, 0, len(payload.Events))
	for _, entry := range payload.Events {
		hash := identifierBloomHash(entry.ID)
		segment.eventBloom.add(hash)
		eventHashes = append(eventHashes, identifierHashValue(hash))
	}
	l.segments = append(l.segments, segment)
	l.requestIndex = l.requestIndex.add(requestHashes)
	l.eventIndex = l.eventIndex.add(eventHashes)
	l.hotRequests = make(map[string]protocol.RequestIdentity)
	l.hotEvents = make(map[string]protocol.EventIdentity)
	return nil
}

func decodeIdentifierSegment(
	segment identifierSegment,
) (identifierSegmentPayload, error) {
	var payload identifierSegmentPayload
	if err := json.Unmarshal(segment.payload, &payload); err != nil {
		return identifierSegmentPayload{},
			fmt.Errorf("decode identifier segment: %w", err)
	}
	return payload, nil
}

func identifierBloomHash(identifier string) [32]byte {
	return sha256.Sum256([]byte(identifier))
}

func identifierHashValue(hash [32]byte) uint64 {
	return binary.LittleEndian.Uint64(hash[:8])
}

func (index identifierHashIndex) add(values []uint64) identifierHashIndex {
	if len(values) == 0 {
		return index
	}
	carry := append([]uint64(nil), values...)
	sort.Slice(carry, func(left, right int) bool {
		return carry[left] < carry[right]
	})
	next := identifierHashIndex{
		levels: append([][]uint64(nil), index.levels...),
	}
	for level := 0; ; level++ {
		if level == len(next.levels) {
			next.levels = append(next.levels, carry)
			return next
		}
		if len(next.levels[level]) == 0 {
			next.levels[level] = carry
			return next
		}
		carry = mergeIdentifierHashes(next.levels[level], carry)
		next.levels[level] = nil
	}
}

func (index identifierHashIndex) contains(value uint64) bool {
	for _, level := range index.levels {
		position := sort.Search(
			len(level),
			func(position int) bool {
				return level[position] >= value
			},
		)
		if position < len(level) && level[position] == value {
			return true
		}
	}
	return false
}

func mergeIdentifierHashes(left, right []uint64) []uint64 {
	merged := make([]uint64, 0, len(left)+len(right))
	leftIndex := 0
	rightIndex := 0
	for leftIndex < len(left) && rightIndex < len(right) {
		if left[leftIndex] <= right[rightIndex] {
			merged = append(merged, left[leftIndex])
			leftIndex++
		} else {
			merged = append(merged, right[rightIndex])
			rightIndex++
		}
	}
	merged = append(merged, left[leftIndex:]...)
	merged = append(merged, right[rightIndex:]...)
	return merged
}

func (b *identifierBloom) add(hash [32]byte) {
	for offset := 0; offset < 8; offset += 2 {
		value := binary.LittleEndian.Uint16(hash[offset : offset+2])
		bit := uint64(value) % uint64(identifierBloomWords*64)
		b[bit/64] |= uint64(1) << (bit % 64)
	}
}

func (b identifierBloom) maybeContains(hash [32]byte) bool {
	for offset := 0; offset < 8; offset += 2 {
		value := binary.LittleEndian.Uint16(hash[offset : offset+2])
		bit := uint64(value) % uint64(identifierBloomWords*64)
		if b[bit/64]&(uint64(1)<<(bit%64)) == 0 {
			return false
		}
	}
	return true
}

func mergeRequestIdentity(
	existing protocol.RequestIdentity,
	value protocol.RequestIdentity,
) protocol.RequestIdentity {
	existing.Ambiguous = true
	existing.RequestHash = ""
	existing.ResultRevision = 0
	existing.ResultHeadHash = ""
	existing.Proposal = nil
	existing.Arbitration = nil
	if existing.Kind != value.Kind {
		existing.Kind = ""
	}
	return existing
}

func mergeEventIdentity(
	existing protocol.EventIdentity,
	value protocol.EventIdentity,
) protocol.EventIdentity {
	existing.Ambiguous = true
	if existing.Kind != value.Kind {
		existing.Kind = ""
	}
	if existing.RequestID != value.RequestID {
		existing.RequestID = ""
	}
	existing.Revision = 0
	return existing
}
