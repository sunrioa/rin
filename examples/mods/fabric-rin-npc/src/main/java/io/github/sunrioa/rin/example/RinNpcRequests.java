package io.github.sunrioa.rin.example;

import io.github.sunrioa.rin.PendingTurn;
import io.github.sunrioa.rin.RinClient;
import net.minecraft.server.MinecraftServer;

import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

final class RinNpcRequests {
    static final String ACTOR_ID = "npc.rin.guide";

    private RinNpcRequests() { }

    static Map<String, Object> create(String sessionId, String playerName) {
        return mapOf(
                "protocol_version", RinClient.PROTOCOL_VERSION,
                "request_id", "create." + sessionId,
                "session_id", sessionId,
                "binding", mapOf(
                        "game_id", "minecraft-fabric",
                        "content_id", "rin-npc-example",
                        "content_version", "0.6.0",
                        "content_hash", "sha256:" + "0".repeat(64)),
                "seed", Integer.toUnsignedLong(sessionId.hashCode()),
                "features", List.of("outcome-reporting-v1"),
                "actors", List.of(mapOf(
                        "id", ACTOR_ID,
                        "kind", "npc",
                        "display_name", "Rin Guide",
                        "traits", List.of("observant", "careful"),
                        "boundaries", List.of(mapOf(
                                "id", "boundary.no-griefing",
                                "description", "Never suggest griefing or bypassing server rules.",
                                "trigger_tags", List.of("unsafe"),
                                "response", "refuse")),
                        "goals", List.of(mapOf(
                                "id", "goal.help-player",
                                "description", "Help " + playerName + " make one informed choice.",
                                "priority", 4,
                                "preferred_actions", List.of("talk"),
                                "progress", 0,
                                "target_progress", 3,
                                "status", "active")),
                        "think_every_ticks", 20,
                        "enabled", true)));
    }

    static Map<String, Object> observe(
            String sessionId,
            String operationId,
            long tick) {
        return mapOf(
                "protocol_version", RinClient.PROTOCOL_VERSION,
                "session_id", sessionId,
                "request_id", "observe." + operationId,
                "event_id", "question." + operationId,
                "tick", tick,
                "observer_ids", List.of(ACTOR_ID),
                "source", "fabric-example",
                "kind", "dialogue",
                "summary", "The player asked the guide what to do next.",
                "tags", List.of("conversation", "player-request"),
                "importance", 3);
    }

    static Map<String, Object> proposal(
            String sessionId,
            String operationId,
            long tick) {
        return mapOf(
                "protocol_version", RinClient.PROTOCOL_VERSION,
                "session_id", sessionId,
                "request_id", "propose." + operationId,
                "actor_id", ACTOR_ID,
                "tick", tick,
                "intent", "Choose one bounded response to the player.",
                "tags", List.of("conversation"),
                "candidate_actions", List.of(
                        mapOf("id", "talk", "kind", "dialogue",
                                "description", "offer one concrete hint"),
                        mapOf("id", "wait", "kind", "wait",
                                "description", "ask the player to observe first"),
                        mapOf("id", "refuse", "kind", "refuse",
                                "description", "decline an unsafe request")));
    }

    static Map<String, Object> commit(
            MinecraftServer server,
            PendingTurn pending,
            Map<String, Object> proposal,
            boolean accepted,
            String outcome) {
        long tick = Math.max(
                server.getTicks(),
                Math.max(integer(proposal.get("tick")), integer(pending.request().get("tick"))));
        return mapOf(
                "protocol_version", RinClient.PROTOCOL_VERSION,
                "session_id", pending.request().get("session_id"),
                "request_id", "commit." + pending.operationId(),
                "proposal_id", proposal.get("id"),
                "event_id", "outcome." + pending.operationId(),
                "tick", tick,
                "accepted", accepted,
                "outcome", outcome,
                "tags", List.of("fabric-example", "conversation"));
    }

    static Map<String, Object> safeObserve(
            Map<String, Object> commit,
            String operationId) {
        boolean accepted = Boolean.TRUE.equals(commit.get("accepted"));
        return mapOf(
                "protocol_version", RinClient.PROTOCOL_VERSION,
                "session_id", commit.get("session_id"),
                "request_id", "fallback.observe." + operationId,
                "event_id", commit.get("event_id"),
                "tick", commit.get("tick"),
                "observer_ids", List.of(ACTOR_ID),
                "source", "fabric-example",
                "kind", "action_outcome",
                "summary", commit.get("outcome"),
                "tags", List.of("outcome", "degraded-report"),
                "importance", 3,
                "facts", List.of(mapOf(
                        "subject_id", ACTOR_ID,
                        "predicate", "last_action_outcome",
                        "object", accepted ? "accepted" : "rejected",
                        "visibility", List.of(ACTOR_ID),
                        "confidence", 100)));
    }

    private static long integer(Object value) {
        return value instanceof Number number ? number.longValue() : 0;
    }

    private static Map<String, Object> mapOf(Object... entries) {
        if (entries.length % 2 != 0) {
            throw new IllegalArgumentException("Map entries must be key/value pairs");
        }
        Map<String, Object> result = new LinkedHashMap<>();
        for (int index = 0; index < entries.length; index += 2) {
            result.put((String) entries[index], entries[index + 1]);
        }
        return result;
    }
}
