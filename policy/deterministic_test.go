package policy_test

import (
	"context"
	"errors"
	"testing"

	"github.com/sunrioa/rin/policy"
	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
)

func TestDeterministicPolicyUsesGoalAndMemory(t *testing.T) {
	input := policyInput()
	draft, err := (policy.Deterministic{}).Propose(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if draft.ActionID != "talk" || draft.GoalID != "goal.connect" {
		t.Fatalf("unexpected draft: %+v", draft)
	}
	if len(draft.RecalledMemoryIDs) != 2 || draft.RecalledMemoryIDs[0] != "memory.relevant" {
		t.Fatalf("unexpected recall order: %v", draft.RecalledMemoryIDs)
	}
	repeated, err := (policy.Deterministic{}).Propose(context.Background(), input)
	if err != nil || repeated.ActionID != draft.ActionID || repeated.Rationale != draft.Rationale {
		t.Fatalf("policy should be deterministic: first=%+v second=%+v err=%v", draft, repeated, err)
	}
}

func TestDeterministicPolicyProtectsBoundary(t *testing.T) {
	input := policyInput()
	input.Request.Tags = []string{"private"}
	draft, err := (policy.Deterministic{}).Propose(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if draft.ActionID != "refuse" || draft.Stance != "refuse" {
		t.Fatalf("unexpected boundary draft: %+v", draft)
	}
	input.Request.CandidateActions = input.Request.CandidateActions[:1]
	if _, err := (policy.Deterministic{}).Propose(context.Background(), input); !errors.Is(err, rinruntime.ErrNoSafeAction) {
		t.Fatalf("expected no safe action, got %v", err)
	}
}

func TestDeterministicPolicyHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (policy.Deterministic{}).Propose(ctx, policyInput()); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled policy, got %v", err)
	}
}

func policyInput() rinruntime.PolicyContext {
	actor := protocol.ActorState{
		ActorSeed: protocol.ActorSeed{
			ID: "npc.mira", Kind: "npc", DisplayName: "Mira", Enabled: true, ThinkEveryTicks: 5,
			Boundaries: []protocol.Boundary{{ID: "boundary.private", Description: "Keep letters private.", TriggerTags: []string{"private"}, Response: "refuse"}},
			Goals:      []protocol.Goal{{ID: "goal.connect", Description: "Build trust.", Priority: 4, PreferredActions: []string{"talk"}, TargetProgress: 3, Status: "active"}},
		},
		Memories: []protocol.Memory{
			{ID: "memory.old", EventID: "event.old", Tick: 1, Summary: "An old event.", Tags: []string{"weather"}, Importance: 2},
			{ID: "memory.relevant", EventID: "event.relevant", Tick: 5, Summary: "The player waited.", Quote: "Take your time.", Tags: []string{"trust"}, Importance: 4},
		},
	}
	request := protocol.ProposeRequest{
		ProtocolVersion: protocol.Version, SessionID: "session.policy", RequestID: "request.policy", ActorID: actor.ID,
		Tick: 6, Intent: "Respond", Tags: []string{"trust"},
		CandidateActions: []protocol.ActionSpec{
			{ID: "talk", Kind: "dialogue", Description: "ask a question"},
			{ID: "refuse", Kind: "refuse", Description: "protect a boundary"},
			{ID: "wait", Kind: "wait", Description: "wait"},
		},
	}
	return rinruntime.PolicyContext{
		State: protocol.SessionState{ProtocolVersion: protocol.Version, SessionID: "session.policy", Seed: 42},
		Actor: actor, Request: request,
	}
}
