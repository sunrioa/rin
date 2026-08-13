package controlplane

import (
	"context"
	"errors"

	"github.com/sunrioa/rin/host"
)

// OutcomeEvidence is the engine-neutral projection supplied to optional local
// memory sinks after the authoritative operation commit succeeds.
type OutcomeEvidence struct {
	TaskID              string             `json:"task_id,omitempty"`
	OperationID         string             `json:"operation_id"`
	HostID              string             `json:"host_id"`
	WorldID             string             `json:"world_id"`
	ActorID             string             `json:"actor_id"`
	ControllerID        string             `json:"controller_id"`
	Capability          host.CapabilityRef `json:"capability"`
	ExpectedEpoch       host.Epoch         `json:"expected_epoch"`
	ObservationSequence uint64             `json:"observation_sequence"`
	PlanStep            *host.PlanStepRef  `json:"plan_step_ref,omitempty"`
	Outcome             host.ActionOutcome `json:"outcome"`
}

type OutcomeSink interface {
	RecordOutcome(context.Context, OutcomeEvidence) error
}

type outcomeSinkGroup []OutcomeSink

// JoinOutcomeSinks composes independent local projections without creating a
// new execution path. Nil sinks are ignored.
func JoinOutcomeSinks(sinks ...OutcomeSink) OutcomeSink {
	group := make(outcomeSinkGroup, 0, len(sinks))
	for _, sink := range sinks {
		if sink != nil {
			group = append(group, sink)
		}
	}
	if len(group) == 0 {
		return nil
	}
	return group
}

func (group outcomeSinkGroup) RecordOutcome(ctx context.Context, evidence OutcomeEvidence) error {
	var result error
	for _, sink := range group {
		result = errors.Join(result, sink.RecordOutcome(ctx, evidence))
	}
	return result
}
