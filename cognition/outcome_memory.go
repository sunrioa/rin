package cognition

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/sunrioa/rin/controlplane"
	"github.com/sunrioa/rin/host"
)

// OutcomeMemorySink projects every committed Host Outcome into the same
// idempotent actor-memory path, regardless of who submitted the action.
type OutcomeMemorySink struct {
	memory MemoryProvider
}

func NewOutcomeMemorySink(memory MemoryProvider) (*OutcomeMemorySink, error) {
	if memory == nil {
		return nil, ErrProviderNotFound
	}
	return &OutcomeMemorySink{memory: memory}, nil
}

func (sink *OutcomeMemorySink) RecordOutcome(
	ctx context.Context,
	evidence controlplane.OutcomeEvidence,
) error {
	payload, _ := json.Marshal(evidence)
	digest := sha256.Sum256(payload)
	idDigest := sha256.Sum256([]byte(evidence.OperationID))
	record := MemoryRecord{
		MemoryID: "outcome." + hex.EncodeToString(idDigest[:8]),
		Namespace: MemoryNamespace{
			SessionID: evidence.ExpectedEpoch.SessionID, ActorID: evidence.ActorID,
			Domain: MemoryActorEpisodic,
		},
		Content:        evidence.Outcome.Summary,
		Tags:           []string{evidence.Capability.ID},
		SourceEventIDs: []string{evidence.OperationID},
		Provenance: MemoryProvenance{
			Source: MemorySourceHostOutcome, SourceID: evidence.OperationID, Authoritative: true,
		},
		CanonRef: &MemoryCanonRef{
			HostID: evidence.HostID, WorldID: evidence.WorldID,
			Epoch: evidence.Outcome.Epoch, Sequence: evidence.Outcome.WorldSeq,
			Digest: hex.EncodeToString(digest[:]), Status: MemoryCanonCurrent,
		},
		Confidence: 1, Importance: outcomeMemoryImportance(evidence.Outcome.Status),
		CreatedAt: evidence.Outcome.OccurredAt,
	}
	_, err := sink.memory.Append(ctx, record)
	return err
}

func outcomeMemoryImportance(status host.ActionRunStatus) float64 {
	if status == host.ActionSucceeded {
		return 0.9
	}
	return 0.7
}

var _ controlplane.OutcomeSink = (*OutcomeMemorySink)(nil)
