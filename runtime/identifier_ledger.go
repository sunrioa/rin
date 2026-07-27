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
	hotBytes         uint64
	retainedBytes    uint64
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

func newIdentifierLedger(complete bool) identifierLedger {
	return identifierLedger{
		version:          protocol.IdentifierHistoryVersion,
		coverageComplete: complete,
		hotRequests:      make(map[string]protocol.RequestIdentity),
		hotEvents:        make(map[string]protocol.EventIdentity),
	}
}

func identifierLedgerFromHistory(
	history protocol.IdentifierHistory,
) (identifierLedger, error) {
	normalizeIdentifierHistory(&history)
	ledger := newIdentifierLedger(history.CoverageComplete)
	ledger.version = history.Version
	for requestID, identity := range history.Requests {
		if identity.Proposal != nil {
			proposal := *identity.Proposal
			canonicalizeProposalPresentation(&proposal)
			identity.Proposal = &proposal
		}
		if err := ledger.setHotRequest(requestID, identity); err != nil {
			return identifierLedger{}, err
		}
		if err := ledger.sealHotIfFull(); err != nil {
			return identifierLedger{}, err
		}
	}
	for eventID, identity := range history.Events {
		if err := ledger.setHotEvent(eventID, identity); err != nil {
			return identifierLedger{}, err
		}
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
		hotBytes:      l.hotBytes,
		retainedBytes: l.retainedBytes,
		segments:      append([]identifierSegment(nil), l.segments...),
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
	if err := next.applyDelta(delta); err != nil {
		return identifierLedger{}, err
	}
	return next, nil
}

func (l *identifierLedger) applyDelta(delta identifierEventDelta) error {
	l.normalize()
	if delta.imported != nil {
		l.coverageComplete =
			l.coverageComplete && delta.imported.CoverageComplete
		for requestID, value := range delta.imported.Requests {
			existing, found, err := l.request(requestID)
			if err != nil {
				return err
			}
			if !found {
				if err := l.setHotRequest(requestID, value); err != nil {
					return err
				}
			} else if !reflect.DeepEqual(existing, value) {
				if err := l.setHotRequest(
					requestID,
					mergeRequestIdentity(existing, value),
				); err != nil {
					return err
				}
			}
			if err := l.sealHotIfFull(); err != nil {
				return err
			}
		}
		for eventID, value := range delta.imported.Events {
			existing, found, err := l.event(eventID)
			if err != nil {
				return err
			}
			if !found {
				if err := l.setHotEvent(eventID, value); err != nil {
					return err
				}
			} else if !reflect.DeepEqual(existing, value) {
				if err := l.setHotEvent(
					eventID,
					mergeEventIdentity(existing, value),
				); err != nil {
					return err
				}
			}
			if err := l.sealHotIfFull(); err != nil {
				return err
			}
		}
	}
	if existing, found, err := l.request(delta.event.RequestID); err != nil {
		return err
	} else if found {
		if err := l.setHotRequest(
			delta.event.RequestID,
			mergeRequestIdentity(existing, delta.request),
		); err != nil {
			return err
		}
	} else {
		if err := l.setHotRequest(
			delta.event.RequestID,
			delta.request,
		); err != nil {
			return err
		}
	}
	if err := l.sealHotIfFull(); err != nil {
		return err
	}
	for _, value := range delta.events {
		identity := protocol.EventIdentity{
			Kind:      value.kind,
			RequestID: delta.event.RequestID,
			Revision:  delta.event.Sequence,
		}
		existing, found, err := l.event(value.id)
		if err != nil {
			return err
		}
		if found {
			identity = mergeEventIdentity(existing, identity)
		}
		if value.id != "" {
			if err := l.setHotEvent(value.id, identity); err != nil {
				return err
			}
		}
		if err := l.sealHotIfFull(); err != nil {
			return err
		}
	}
	return nil
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
	l.retainedBytes += uint64(len(encoded)) +
		uint64(2*identifierBloomWords*8) +
		uint64(len(requestHashes)+len(eventHashes))*8
	l.hotRequests = make(map[string]protocol.RequestIdentity)
	l.hotEvents = make(map[string]protocol.EventIdentity)
	l.hotBytes = 0
	return nil
}

func (l *identifierLedger) setHotRequest(
	requestID string,
	identity protocol.RequestIdentity,
) error {
	if existing, found := l.hotRequests[requestID]; found {
		size, err := identifierRequestEntryBytes(requestID, existing)
		if err != nil {
			return err
		}
		l.hotBytes -= size
	}
	size, err := identifierRequestEntryBytes(requestID, identity)
	if err != nil {
		return err
	}
	l.hotRequests[requestID] = identity
	l.hotBytes += size
	return nil
}

func (l *identifierLedger) setHotEvent(
	eventID string,
	identity protocol.EventIdentity,
) error {
	if eventID == "" {
		return nil
	}
	if existing, found := l.hotEvents[eventID]; found {
		size, err := identifierEventEntryBytes(eventID, existing)
		if err != nil {
			return err
		}
		l.hotBytes -= size
	}
	size, err := identifierEventEntryBytes(eventID, identity)
	if err != nil {
		return err
	}
	l.hotEvents[eventID] = identity
	l.hotBytes += size
	return nil
}

func identifierRequestEntryBytes(
	requestID string,
	identity protocol.RequestIdentity,
) (uint64, error) {
	encoded, err := json.Marshal(identifierRequestEntry{
		ID: requestID, Identity: identity,
	})
	if err != nil {
		return 0, fmt.Errorf("encode request identifier entry: %w", err)
	}
	return uint64(len(encoded) + 1), nil
}

func identifierEventEntryBytes(
	eventID string,
	identity protocol.EventIdentity,
) (uint64, error) {
	encoded, err := json.Marshal(identifierEventEntry{
		ID: eventID, Identity: identity,
	})
	if err != nil {
		return 0, fmt.Errorf("encode event identifier entry: %w", err)
	}
	return uint64(len(encoded) + 1), nil
}

func (l identifierLedger) identityBytes() uint64 {
	return l.retainedBytes + l.hotBytes
}

func validateIdentifierLedgerCoversState(
	ledger identifierLedger,
	state protocol.SessionState,
) error {
	retained := identifiersFromState(state)
	history := newIdentifierHistory(ledger.coverageComplete)
	for requestID := range retained.Requests {
		identity, found, err := ledger.request(requestID)
		if err != nil {
			return err
		}
		if found {
			history.Requests[requestID] = identity
		}
	}
	for eventID := range retained.Events {
		identity, found, err := ledger.event(eventID)
		if err != nil {
			return err
		}
		if found {
			history.Events[eventID] = identity
		}
	}
	return validateIdentifiersCoverState(history, state)
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
