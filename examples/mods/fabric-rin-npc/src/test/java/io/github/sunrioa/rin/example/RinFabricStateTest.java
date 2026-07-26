package io.github.sunrioa.rin.example;

import io.github.sunrioa.rin.OutcomeOutboxEntry;
import io.github.sunrioa.rin.PendingTurn;
import net.minecraft.nbt.NbtCompound;

import java.util.List;
import java.util.Map;

public final class RinFabricStateTest {
    private RinFabricStateTest() { }

    public static void main(String[] args) {
        RinFabricState state = new RinFabricState();
        FabricHostEpoch integrated = state.beginHost(
                101,
                FabricHostEpoch.AuthorityKind.fromDedicated(false));
        require(
                integrated.authorityKind() == FabricHostEpoch.AuthorityKind.INTEGRATED,
                "integrated logical server was misclassified");
        String sessionId = "fabric." + state.worldId + ".00000000-0000-0000-0000-000000000001";
        Map<String, Object> create = Map.of(
                "protocol_version", "rin.protocol/v2",
                "session_id", sessionId,
                "request_id", "create." + sessionId,
                "seed", 7);
        FabricSessionState session = state.session(sessionId, create);
        require(
                state.session(sessionId, create) == session,
                "identical session create request was not idempotent");
        try {
            state.session(sessionId, Map.of(
                    "protocol_version", "rin.protocol/v2",
                    "session_id", sessionId,
                    "request_id", "create.conflicting"));
            throw new AssertionError("conflicting session identity was accepted");
        } catch (IllegalStateException expected) {
            require(
                    expected.getMessage().contains("already bound"),
                    "session identity conflict returned the wrong error");
        }
        long sequence = state.nextSequence();
        session.pendingTurn = PendingTurn.create(
                state.worldId + "." + sequence,
                Map.of(
                        "protocol_version", "rin.protocol/v2",
                        "session_id", sessionId,
                        "request_id", "propose." + state.worldId + "." + sequence,
                        "actor_id", "npc.rin.guide",
                        "intent", "talk",
                        "offers", List.of()));
        session.pendingObserve = Map.of(
                "protocol_version", "rin.protocol/v2",
                "session_id", sessionId,
                "request_id", "observe." + state.worldId + "." + sequence);
        OutcomeOutboxEntry outcome = new OutcomeOutboxEntry(
                state.worldId + "." + sequence,
                Map.of(
                        "request_id", "report." + state.worldId + "." + sequence,
                        "tick", 7L,
                        "report", Map.of(
                                "proposal_id", "proposal.fixture",
                                "event_id", "outcome." + state.worldId + "." + sequence,
                                "decision", "rejected",
                                "summary", "host rejected the offer")));
        session.outcomes.put(outcome.key(), outcome);

        Map<String, Object> currentEpoch = integrated.wire(sessionId);
        Map<String, Object> proposal = Map.of(
                "session_id", sessionId,
                "decision_window", Map.of("epoch", currentEpoch),
                "action", Map.of("expected_epoch", currentEpoch));
        require(integrated.matchesProposal(sessionId, proposal), "current Epoch was rejected");

        Map<String, Object> staleEpoch = Map.of(
                "session_id", sessionId,
                "world_id", currentEpoch.get("world_id"),
                "host", 100L,
                "world", 1L,
                "timeline", 1L);
        Map<String, Object> staleProposal = Map.of(
                "session_id", sessionId,
                "decision_window", Map.of("epoch", staleEpoch),
                "action", Map.of("expected_epoch", staleEpoch));
        require(
                !integrated.matchesProposal(sessionId, staleProposal),
                "stale_epoch_rejection: replaced Host Epoch was accepted");

        NbtCompound nbt = state.writeNbt(new NbtCompound(), null);
        RinFabricState restored = RinFabricState.readNbt(nbt, null);
        require(restored.worldId.equals(state.worldId), "world identity changed");
        require(restored.sequence == 1, "sequence changed");
        require(restored.hostGeneration == 101, "Host generation changed");
        require(restored.timelineGeneration == 1, "Timeline generation changed");
        require(
                restored.authorityKind == FabricHostEpoch.AuthorityKind.INTEGRATED,
                "authority kind changed");
        FabricSessionState restoredSession = restored.sessions.get(sessionId);
        require(restoredSession != null, "session disappeared");
        require(
                restored.session(sessionId, create) == restoredSession,
                "numeric codec normalization broke create idempotency");
        require(restoredSession.pendingTurn.equals(session.pendingTurn), "Pending Turn changed");
        require(restoredSession.pendingObserve.equals(session.pendingObserve), "Observe changed");
        require(restoredSession.outcomes.get(outcome.key()).equals(outcome), "Outbox changed");
        require(
                restoredSession.outcomes.get(outcome.key()).report().get("tick")
                        instanceof Long,
                "integer tick lost its wire type");

        FabricHostEpoch dedicated = restored.beginHost(
                202,
                FabricHostEpoch.AuthorityKind.fromDedicated(true));
        require(
                dedicated.authorityKind() == FabricHostEpoch.AuthorityKind.DEDICATED,
                "dedicated logical server was misclassified");
        require(dedicated.host() != integrated.host(), "Host generation was reused");
        require(
                dedicated.timeline() == integrated.timeline() + 1,
                "server restart did not advance Timeline");
        require(
                !dedicated.matchesProposal(sessionId, proposal),
                "old integrated-server proposal survived dedicated-server start");

        expectMalformed("""
                {"version":3,"world_id":"00000000-0000-0000-0000-000000000001",
                "sequence":0,"sessions":{}}
                """);
        expectMalformed("""
                {"version":2,"world_id":"00000000-0000-0000-0000-000000000001",
                "sequence":0,"host":1,"timeline":1,"authority":"dedicated","sessions":[]}
                """);
        expectMalformed("""
                {"version":2,"world_id":"00000000-0000-0000-0000-000000000001",
                "sequence":9007199254740992,"host":1,"timeline":1,
                "authority":"dedicated","sessions":{}}
                """);
    }

    private static void expectMalformed(String json) {
        NbtCompound malformed = new NbtCompound();
        malformed.putString("json", json);
        try {
            RinFabricState.readNbt(malformed, null);
            throw new AssertionError("malformed save was silently accepted");
        } catch (IllegalStateException expected) {
            require(
                    expected.getMessage().contains("malformed"),
                    "malformed save returned an unexpected error");
        }
    }

    private static void require(boolean condition, String message) {
        if (!condition) throw new AssertionError(message);
    }
}
