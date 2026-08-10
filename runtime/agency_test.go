package runtime_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sunrioa/rin/cognition"
	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
	"github.com/sunrioa/rin/store"
)

type agencyCountingPolicy struct {
	calls atomic.Int32
}

func (p *agencyCountingPolicy) Propose(
	_ context.Context,
	_ rinruntime.DecisionContext,
) (rinruntime.DecisionDraft, error) {
	p.calls.Add(1)
	return rinruntime.DecisionDraft{
		OfferID: "talk", Stance: "engage", PolicySource: "test",
	}, nil
}

func TestActorAgencyUpdateIsIdempotentAndReplayable(t *testing.T) {
	eventStore := store.NewMemory()
	engine := newEngine(t, eventStore, cognition.Deterministic{})
	create := agencyCreateRequest("session.agency-update")
	if _, err := engine.CreateSession(create); err != nil {
		t.Fatal(err)
	}
	request := actorAgencyUpdate(create.SessionID, "agency.update.1", 5)
	first, err := engine.SetActorAgency(request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := engine.SetActorAgency(request)
	if err != nil || !second.Duplicate || second.Revision != first.Revision ||
		second.HeadHash != first.HeadHash {
		t.Fatalf("idempotence failed: first=%+v second=%+v err=%v", first, second, err)
	}

	state, err := engine.State(sessionRequest(create.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	actor := state.Actors["npc.mira"]
	if actor.Agency == nil || actor.Agency.Initiative != protocol.InitiativeActions ||
		actor.AgencyState == nil || actor.AgencyState.UpdatedTick != request.Tick ||
		actor.AgencyState.UpdatedRevision != first.Revision {
		t.Fatalf("agency update was not projected: %+v", actor)
	}
	if state.WorldRevision != 2 {
		t.Fatalf("agency update did not advance world revision: %d", state.WorldRevision)
	}
	if snapshot, err := engine.Snapshot(sessionRequest(create.SessionID)); err != nil {
		t.Fatal(err)
	} else if err := rinruntime.ValidateSnapshot(snapshot); err != nil {
		t.Fatalf("agency snapshot is invalid: %v", err)
	}

	reloaded := newEngine(t, eventStore, cognition.Deterministic{})
	replayed, err := reloaded.State(sessionRequest(create.SessionID))
	if err != nil || replayed.Actors["npc.mira"].Agency == nil ||
		replayed.Actors["npc.mira"].Agency.Initiative != protocol.InitiativeActions {
		t.Fatalf("agency did not replay: %+v err=%v", replayed, err)
	}
}

func TestActorAgencyUpdateRejectsInvalidSessionMutation(t *testing.T) {
	t.Run("feature disabled", func(t *testing.T) {
		engine := newEngine(t, store.NewMemory(), cognition.Deterministic{})
		create := createRequest("session.agency-disabled")
		if _, err := engine.CreateSession(create); err != nil {
			t.Fatal(err)
		}
		if _, err := engine.SetActorAgency(actorAgencyUpdate(create.SessionID, "agency.disabled", 1)); err == nil {
			t.Fatal("agency update succeeded without feature")
		}
	})

	t.Run("unknown actor", func(t *testing.T) {
		engine := newEngine(t, store.NewMemory(), cognition.Deterministic{})
		create := agencyCreateRequest("session.agency-unknown")
		if _, err := engine.CreateSession(create); err != nil {
			t.Fatal(err)
		}
		request := actorAgencyUpdate(create.SessionID, "agency.unknown", 1)
		request.Updates[0].ActorID = "npc.unknown"
		if _, err := engine.SetActorAgency(request); err == nil {
			t.Fatal("agency update accepted an unknown actor")
		}
	})

	t.Run("tick regression", func(t *testing.T) {
		engine := newEngine(t, store.NewMemory(), cognition.Deterministic{})
		create := agencyCreateRequest("session.agency-tick")
		if _, err := engine.CreateSession(create); err != nil {
			t.Fatal(err)
		}
		if _, err := engine.SetActorAgency(actorAgencyUpdate(create.SessionID, "agency.tick.1", 5)); err != nil {
			t.Fatal(err)
		}
		if _, err := engine.SetActorAgency(actorAgencyUpdate(create.SessionID, "agency.tick.2", 4)); err == nil {
			t.Fatal("agency update accepted a regressed tick")
		}
	})

	t.Run("altered retry", func(t *testing.T) {
		engine := newEngine(t, store.NewMemory(), cognition.Deterministic{})
		create := agencyCreateRequest("session.agency-retry")
		if _, err := engine.CreateSession(create); err != nil {
			t.Fatal(err)
		}
		request := actorAgencyUpdate(create.SessionID, "agency.retry", 1)
		if _, err := engine.SetActorAgency(request); err != nil {
			t.Fatal(err)
		}
		altered := request
		altered.Updates = append([]protocol.ActorAgencyUpdate(nil), request.Updates...)
		altered.Updates[0].Policy.Obedience = protocol.ObedienceIndependent
		if _, err := engine.SetActorAgency(altered); err == nil {
			t.Fatal("agency update accepted a changed retry payload")
		}
	})
}

func TestAgencyGateRejectsInitiativeBeforePolicyCall(t *testing.T) {
	tests := []struct {
		name       string
		initiative string
		turn       string
	}{
		{name: "passive dialogue", initiative: protocol.InitiativePassive, turn: protocol.TurnProactiveDialogue},
		{name: "dialogue action", initiative: protocol.InitiativeDialogue, turn: protocol.TurnProactiveAction},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			selectedPolicy := &agencyCountingPolicy{}
			engine := newEngine(t, store.NewMemory(), selectedPolicy)
			create := agencyCreateRequest("session.agency-gate-" + test.initiative)
			actorPolicy := agencyPolicy(test.initiative, 0, 2)
			create.Actors[0].Agency = &actorPolicy
			if _, err := engine.CreateSession(create); err != nil {
				t.Fatal(err)
			}
			_, _, err := engine.Propose(context.Background(), agencyProposeRequest(
				create.SessionID, "propose.agency-gate", 1, test.turn,
			))
			if !errors.Is(err, rinruntime.ErrNotDue) || rinruntime.ErrorCode(err) != "agency_initiative" {
				t.Fatalf("initiative gate returned %v", err)
			}
			if selectedPolicy.calls.Load() != 0 {
				t.Fatal("initiative gate called the policy")
			}
		})
	}
}

func TestAgencyCooldownAndConsecutiveTurnLimitRunBeforePolicy(t *testing.T) {
	t.Run("cooldown", func(t *testing.T) {
		selectedPolicy := &agencyCountingPolicy{}
		engine := newEngine(t, store.NewMemory(), selectedPolicy)
		create := agencyCreateRequest("session.agency-cooldown")
		actorPolicy := agencyPolicy(protocol.InitiativeDialogue, 10, 4)
		create.Actors[0].Agency = &actorPolicy
		if _, err := engine.CreateSession(create); err != nil {
			t.Fatal(err)
		}
		first, _, err := engine.Propose(context.Background(), agencyProposeRequest(
			create.SessionID, "propose.agency-cooldown.1", 10, protocol.TurnProactiveDialogue,
		))
		if err != nil || first.Agency == nil {
			t.Fatalf("first proactive dialogue failed: %+v err=%v", first, err)
		}
		_, _, err = engine.Propose(context.Background(), agencyProposeRequest(
			create.SessionID, "propose.agency-cooldown.2", 15, protocol.TurnProactiveDialogue,
		))
		if !errors.Is(err, rinruntime.ErrNotDue) || rinruntime.ErrorCode(err) != "agency_cooldown" {
			t.Fatalf("cooldown gate returned %v", err)
		}
		if selectedPolicy.calls.Load() != 1 {
			t.Fatalf("cooldown gate called policy %d times", selectedPolicy.calls.Load())
		}
	})

	t.Run("consecutive turns", func(t *testing.T) {
		selectedPolicy := &agencyCountingPolicy{}
		engine := newEngine(t, store.NewMemory(), selectedPolicy)
		create := agencyCreateRequest("session.agency-turn-limit")
		actorPolicy := agencyPolicy(protocol.InitiativeActions, 0, 1)
		create.Actors[0].Agency = &actorPolicy
		if _, err := engine.CreateSession(create); err != nil {
			t.Fatal(err)
		}
		if _, _, err := engine.Propose(context.Background(), agencyProposeRequest(
			create.SessionID, "propose.agency-limit.1", 1, protocol.TurnProactiveAction,
		)); err != nil {
			t.Fatal(err)
		}
		_, _, err := engine.Propose(context.Background(), agencyProposeRequest(
			create.SessionID, "propose.agency-limit.2", 2, protocol.TurnProactiveAction,
		))
		if !errors.Is(err, rinruntime.ErrNotDue) || rinruntime.ErrorCode(err) != "agency_turn_limit" {
			t.Fatalf("turn limit gate returned %v", err)
		}
		if selectedPolicy.calls.Load() != 1 {
			t.Fatalf("turn limit gate called policy %d times", selectedPolicy.calls.Load())
		}
	})
}

func TestAgencyResponsiveTurnResetsConsecutiveBudget(t *testing.T) {
	selectedPolicy := &agencyCountingPolicy{}
	engine := newEngine(t, store.NewMemory(), selectedPolicy)
	create := agencyCreateRequest("session.agency-responsive")
	actorPolicy := agencyPolicy(protocol.InitiativeActions, 0, 1)
	create.Actors[0].Agency = &actorPolicy
	if _, err := engine.CreateSession(create); err != nil {
		t.Fatal(err)
	}
	if _, _, err := engine.Propose(context.Background(), agencyProposeRequest(
		create.SessionID, "propose.agency-responsive.1", 1, protocol.TurnProactiveAction,
	)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := engine.Propose(context.Background(), agencyProposeRequest(
		create.SessionID, "propose.agency-responsive.2", 2, protocol.TurnResponsive,
	)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := engine.Propose(context.Background(), agencyProposeRequest(
		create.SessionID, "propose.agency-responsive.3", 3, protocol.TurnProactiveAction,
	)); err != nil {
		t.Fatalf("responsive turn did not reset proactive budget: %v", err)
	}
	state, err := engine.State(sessionRequest(create.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Actors["npc.mira"].AgencyState.ConsecutiveProactiveTurns; got != 1 {
		t.Fatalf("unexpected proactive turn count: %d", got)
	}
}

func TestAgencyFeatureAndTurnMustBePresentTogether(t *testing.T) {
	selectedPolicy := &agencyCountingPolicy{}
	engine := newEngine(t, store.NewMemory(), selectedPolicy)
	create := agencyCreateRequest("session.agency-required")
	if _, err := engine.CreateSession(create); err != nil {
		t.Fatal(err)
	}
	request := proposeRequest(create.SessionID, "propose.agency-required", 1, nil)
	request.Urgent = true
	if _, _, err := engine.Propose(context.Background(), request); err == nil {
		t.Fatal("agency session accepted a proposal without a turn")
	}

	engine = newEngine(t, store.NewMemory(), selectedPolicy)
	create = createRequest("session.agency-unexpected")
	if _, err := engine.CreateSession(create); err != nil {
		t.Fatal(err)
	}
	request = agencyProposeRequest(create.SessionID, "propose.agency-unexpected", 1, protocol.TurnResponsive)
	if _, _, err := engine.Propose(context.Background(), request); err == nil {
		t.Fatal("legacy session accepted an agency turn")
	}
	if selectedPolicy.calls.Load() != 0 {
		t.Fatal("feature mismatch called the policy")
	}
}

func TestAgencyConcurrentTurnsCannotExceedActorBudget(t *testing.T) {
	selectedPolicy := &firstCallBlockingPolicy{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	defer func() {
		select {
		case <-selectedPolicy.release:
		default:
			close(selectedPolicy.release)
		}
	}()
	engine := newEngine(t, store.NewMemory(), selectedPolicy)
	create := agencyCreateRequest("session.agency-concurrent")
	create.Features = append(create.Features, protocol.FeatureArbitration)
	actorPolicy := agencyPolicy(protocol.InitiativeActions, 0, 1)
	create.Actors[0].Agency = &actorPolicy
	if _, err := engine.CreateSession(create); err != nil {
		t.Fatal(err)
	}

	firstResult := make(chan error, 1)
	go func() {
		_, _, err := engine.Propose(context.Background(), agencyProposeRequest(
			create.SessionID, "propose.agency-concurrent.1", 1, protocol.TurnProactiveAction,
		))
		firstResult <- err
	}()
	select {
	case <-selectedPolicy.started:
	case <-time.After(time.Second):
		t.Fatal("first agency policy call did not start")
	}
	if _, _, err := engine.Propose(context.Background(), agencyProposeRequest(
		create.SessionID, "propose.agency-concurrent.2", 1, protocol.TurnProactiveAction,
	)); err != nil {
		t.Fatal(err)
	}
	close(selectedPolicy.release)
	if err := <-firstResult; !errors.Is(err, rinruntime.ErrStale) || rinruntime.ErrorCode(err) != "state_changed" {
		t.Fatalf("concurrent agency turn was not rejected as stale: %v", err)
	}
}

func TestAgencyObeyRejectsPolicyOutsideDirective(t *testing.T) {
	selectedPolicy := &agencyCountingPolicy{}
	engine := newEngine(t, store.NewMemory(), selectedPolicy)
	create := agencyCreateRequest("session.agency-obey")
	actorPolicy := agencyPolicy(protocol.InitiativePassive, 0, 2)
	actorPolicy.Obedience = protocol.ObedienceObey
	create.Actors[0].Agency = &actorPolicy
	if _, err := engine.CreateSession(create); err != nil {
		t.Fatal(err)
	}
	request := agencyProposeRequest(create.SessionID, "propose.agency-obey", 1, protocol.TurnResponsive)
	request.Agency.Directive = true
	request.Agency.DirectiveOfferIDs = []string{"wait"}
	if _, _, err := engine.Propose(context.Background(), request); rinruntime.ErrorCode(err) != "invalid_policy_output" {
		t.Fatalf("policy escaped obey directive: %v", err)
	}
}

func agencyCreateRequest(sessionID string) protocol.CreateSessionRequest {
	request := createRequest(sessionID)
	request.Features = append(request.Features, protocol.FeatureActorAgency)
	policy := protocol.AgencyPolicy{
		Initiative:           protocol.InitiativePassive,
		Obedience:            protocol.ObedienceObey,
		MessageCooldownTicks: 1200,
		MaxConsecutiveTurns:  2,
	}
	request.Actors[0].Agency = &policy
	return request
}

func actorAgencyUpdate(sessionID, requestID string, tick int64) protocol.SetActorAgencyRequest {
	return protocol.SetActorAgencyRequest{
		ProtocolVersion: protocol.Version,
		SessionID:       sessionID,
		RequestID:       requestID,
		Tick:            tick,
		Updates: []protocol.ActorAgencyUpdate{{
			ActorID: "npc.mira",
			Policy: protocol.AgencyPolicy{
				Initiative:           protocol.InitiativeActions,
				Obedience:            protocol.ObedienceNegotiate,
				MessageCooldownTicks: 600,
				MaxConsecutiveTurns:  3,
			},
		}},
	}
}

func agencyPolicy(initiative string, cooldown int64, turns int) protocol.AgencyPolicy {
	return protocol.AgencyPolicy{
		Initiative:           initiative,
		Obedience:            protocol.ObedienceIndependent,
		MessageCooldownTicks: cooldown,
		MaxConsecutiveTurns:  turns,
	}
}

func agencyProposeRequest(sessionID, requestID string, tick int64, kind string) protocol.ProposeRequest {
	request := proposeRequest(sessionID, requestID, tick, nil)
	request.Urgent = true
	maximum := agencyPolicy(protocol.InitiativeActions, 0, 8)
	request.Agency = &protocol.AgencyTurn{
		Kind:         kind,
		HostCeiling:  maximum,
		ServerPolicy: maximum,
	}
	return request
}
