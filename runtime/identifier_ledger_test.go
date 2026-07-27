package runtime

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/sunrioa/rin/protocol"
)

func TestIdentifierLedgerKeepsOnlyBoundedHotMaps(t *testing.T) {
	ledger, err := identifierLedgerFromHistory(newIdentifierHistory(true))
	if err != nil {
		t.Fatal(err)
	}
	expected := newIdentifierHistory(true)
	const mutations = 4_096
	for revision := 1; revision <= mutations; revision++ {
		requestID := fmt.Sprintf("request.segmented.%05d", revision)
		eventID := fmt.Sprintf("event.segmented.%05d", revision)
		request := protocol.RequestIdentity{
			Kind:           EventObserved,
			RequestHash:    strings.Repeat("a", 64),
			ResultRevision: uint64(revision),
			ResultHeadHash: strings.Repeat("b", 64),
		}
		event := protocol.EventIdentity{
			Kind:      EventObserved,
			RequestID: requestID,
			Revision:  uint64(revision),
		}
		ledger, err = ledger.withDelta(identifierEventDelta{
			request: request,
			events: []identifiedEvent{{
				id: eventID, kind: EventObserved,
			}},
			event: protocol.EventRecord{
				Sequence:  uint64(revision),
				Type:      EventObserved,
				RequestID: requestID,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		expected.Requests[requestID] = request
		expected.Events[eventID] = event
	}
	if len(ledger.hotRequests)+len(ledger.hotEvents) >=
		identifierHotEntryLimit {
		t.Fatalf(
			"hot identifier maps grew past their bound: requests=%d events=%d",
			len(ledger.hotRequests),
			len(ledger.hotEvents),
		)
	}
	if len(ledger.segments) < 2 {
		t.Fatalf("identifier history was not segmented: %d", len(ledger.segments))
	}
	if ledger.identityBytes() == 0 || ledger.retainedBytes == 0 {
		t.Fatalf(
			"identifier byte accounting omitted retained data: total=%d cold=%d",
			ledger.identityBytes(),
			ledger.retainedBytes,
		)
	}
	for _, revision := range []int{1, mutations / 2, mutations} {
		requestID := fmt.Sprintf("request.segmented.%05d", revision)
		identity, found, err := ledger.request(requestID)
		if err != nil || !found ||
			identity.ResultRevision != uint64(revision) {
			t.Fatalf(
				"request lookup %s = %+v found=%v err=%v",
				requestID,
				identity,
				found,
				err,
			)
		}
		eventID := fmt.Sprintf("event.segmented.%05d", revision)
		event, found, err := ledger.event(eventID)
		if err != nil || !found ||
			event.Revision != uint64(revision) {
			t.Fatalf(
				"event lookup %s = %+v found=%v err=%v",
				eventID,
				event,
				found,
				err,
			)
		}
	}
	if _, found, err := ledger.request("request.missing"); err != nil ||
		found {
		t.Fatalf("missing request lookup found=%v err=%v", found, err)
	}
	corrupted := ledger.capture()
	for index := range corrupted.segments {
		corrupted.segments[index].payload = []byte("{")
	}
	if _, found, err := corrupted.request(
		"request.definitely-not-indexed",
	); err != nil || found {
		t.Fatalf(
			"new identifier scanned cold payloads: found=%v err=%v",
			found,
			err,
		)
	}
	materialized, err := ledger.materialize()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(materialized, expected) {
		t.Fatal("segmented identifier ledger did not materialize exactly")
	}
}

func TestIdentifierLedgerInPlaceImportUsesReportedByteBudget(t *testing.T) {
	ledger := newIdentifierLedger(true)
	const mutations = 16_384
	for revision := 1; revision <= mutations; revision++ {
		requestID := fmt.Sprintf("request.import.%05d", revision)
		if err := ledger.applyDelta(identifierEventDelta{
			request: protocol.RequestIdentity{
				Kind:           EventObserved,
				RequestHash:    strings.Repeat("a", 64),
				ResultRevision: uint64(revision),
				ResultHeadHash: strings.Repeat("b", 64),
			},
			events: []identifiedEvent{{
				id:   "event.import." + requestID,
				kind: EventObserved,
			}},
			event: protocol.EventRecord{
				Sequence:  uint64(revision),
				Type:      EventObserved,
				RequestID: requestID,
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if len(ledger.hotRequests)+len(ledger.hotEvents) >=
		identifierHotEntryLimit {
		t.Fatalf(
			"in-place import hot maps grew past their bound: requests=%d events=%d",
			len(ledger.hotRequests),
			len(ledger.hotEvents),
		)
	}
	if ledger.identityBytes() == 0 ||
		ledger.identityBytes() >= DefaultMaxTransferIdentityBytes {
		t.Fatalf(
			"in-place import reported implausible identity bytes: %d",
			ledger.identityBytes(),
		)
	}
}

func TestIdentifierLedgerPreservesAmbiguousMergeAndCapture(t *testing.T) {
	history := newIdentifierHistory(true)
	history.Requests["request.old"] = protocol.RequestIdentity{
		Kind:           EventObserved,
		RequestHash:    strings.Repeat("a", 64),
		ResultRevision: 1,
		ResultHeadHash: strings.Repeat("b", 64),
	}
	ledger, err := identifierLedgerFromHistory(history)
	if err != nil {
		t.Fatal(err)
	}
	captured := ledger.capture()
	imported := newIdentifierHistory(true)
	imported.Requests["request.old"] = protocol.RequestIdentity{
		Kind:           EventActionReported,
		RequestHash:    strings.Repeat("c", 64),
		ResultRevision: 2,
		ResultHeadHash: strings.Repeat("d", 64),
	}
	updated, err := ledger.withDelta(identifierEventDelta{
		imported: &imported,
		request: protocol.RequestIdentity{
			Kind:           EventSessionRestored,
			RequestHash:    strings.Repeat("e", 64),
			ResultRevision: 3,
			ResultHeadHash: strings.Repeat("f", 64),
		},
		event: protocol.EventRecord{
			Sequence:  3,
			Type:      EventSessionRestored,
			RequestID: "request.restore",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	identity, found, err := updated.request("request.old")
	if err != nil || !found ||
		!identity.Ambiguous ||
		identity.Kind != "" ||
		identity.RequestHash != "" {
		t.Fatalf(
			"ambiguous merge = %+v found=%v err=%v",
			identity,
			found,
			err,
		)
	}
	if _, found, err := captured.request("request.restore"); err != nil ||
		found {
		t.Fatalf("captured ledger changed: found=%v err=%v", found, err)
	}
}
