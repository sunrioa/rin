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
        String sessionId = "fabric." + state.worldId + ".00000000-0000-0000-0000-000000000001";
        Map<String, Object> create = Map.of(
                "protocol_version", "0.6",
                "session_id", sessionId,
                "request_id", "create." + sessionId);
        RinFabricState.SessionState session = state.session(sessionId, create);
        long sequence = state.nextSequence();
        session.pendingTurn = PendingTurn.create(
                state.worldId + "." + sequence,
                Map.of(
                        "protocol_version", "0.6",
                        "session_id", sessionId,
                        "request_id", "propose." + state.worldId + "." + sequence,
                        "actor_id", "npc.rin.guide",
                        "intent", "talk",
                        "candidate_actions", List.of()));
        session.pendingObserve = Map.of(
                "protocol_version", "0.6",
                "session_id", sessionId,
                "request_id", "observe." + state.worldId + "." + sequence);
        OutcomeOutboxEntry outcome = new OutcomeOutboxEntry(
                state.worldId + "." + sequence,
                Map.of(
                        "request_id", "commit." + state.worldId + "." + sequence,
                        "event_id", "outcome." + state.worldId + "." + sequence,
                        "tick", 7L),
                Map.of(
                        "request_id", "fallback." + state.worldId + "." + sequence,
                        "event_id", "outcome." + state.worldId + "." + sequence,
                        "tick", 7L));
        session.outcomes.put(outcome.key(), outcome);

        NbtCompound nbt = state.writeNbt(new NbtCompound(), null);
        RinFabricState restored = RinFabricState.readNbt(nbt, null);
        require(restored.worldId.equals(state.worldId), "world identity changed");
        require(restored.sequence == 1, "sequence changed");
        RinFabricState.SessionState restoredSession = restored.sessions.get(sessionId);
        require(restoredSession != null, "session disappeared");
        require(restoredSession.pendingTurn.equals(session.pendingTurn), "Pending Turn changed");
        require(restoredSession.pendingObserve.equals(session.pendingObserve), "Observe changed");
        require(restoredSession.outcomes.get(outcome.key()).equals(outcome), "Outbox changed");
        require(
                restoredSession.outcomes.get(outcome.key()).commit().get("tick")
                        instanceof Long,
                "integer tick lost its wire type");

        NbtCompound unsupported = new NbtCompound();
        unsupported.putString("json", """
                {"version":2,"world_id":"00000000-0000-0000-0000-000000000001",
                "sequence":0,"sessions":{}}
                """);
        try {
            RinFabricState.readNbt(unsupported, null);
            throw new AssertionError("unsupported save version was silently reset");
        } catch (IllegalStateException expected) {
            require(
                    expected.getMessage().contains("malformed"),
                    "unsupported save returned an unexpected error");
        }
    }

    private static void require(boolean condition, String message) {
        if (!condition) throw new AssertionError(message);
    }
}
