package runtime_test

import (
	"testing"

	"github.com/sunrioa/rin/policy"
	"github.com/sunrioa/rin/protocol"
	rinruntime "github.com/sunrioa/rin/runtime"
	"github.com/sunrioa/rin/store"
)

func TestActorAgencyUpdateIsIdempotentAndReplayable(t *testing.T) {
	eventStore := store.NewMemory()
	engine := newEngine(t, eventStore, policy.Deterministic{})
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

	reloaded := newEngine(t, eventStore, policy.Deterministic{})
	replayed, err := reloaded.State(sessionRequest(create.SessionID))
	if err != nil || replayed.Actors["npc.mira"].Agency == nil ||
		replayed.Actors["npc.mira"].Agency.Initiative != protocol.InitiativeActions {
		t.Fatalf("agency did not replay: %+v err=%v", replayed, err)
	}
}

func TestActorAgencyUpdateRejectsInvalidSessionMutation(t *testing.T) {
	t.Run("feature disabled", func(t *testing.T) {
		engine := newEngine(t, store.NewMemory(), policy.Deterministic{})
		create := createRequest("session.agency-disabled")
		if _, err := engine.CreateSession(create); err != nil {
			t.Fatal(err)
		}
		if _, err := engine.SetActorAgency(actorAgencyUpdate(create.SessionID, "agency.disabled", 1)); err == nil {
			t.Fatal("agency update succeeded without feature")
		}
	})

	t.Run("unknown actor", func(t *testing.T) {
		engine := newEngine(t, store.NewMemory(), policy.Deterministic{})
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
		engine := newEngine(t, store.NewMemory(), policy.Deterministic{})
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
		engine := newEngine(t, store.NewMemory(), policy.Deterministic{})
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
