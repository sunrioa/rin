package io.github.sunrioa.rin.example;

import io.github.sunrioa.rin.PendingTurn;
import io.github.sunrioa.rin.HostActions;
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
                        "content_version", "0.7.0",
                        "content_hash", "sha256:" + "0".repeat(64)),
                "seed", Integer.toUnsignedLong(sessionId.hashCode()),
                "features", List.of(),
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
                "importance", 3,
                "epoch", epoch(sessionId),
                "observation_seq", tick);
    }

    static Map<String, Object> proposal(
            String sessionId,
            String operationId,
            long tick) {
        Map<String, Object> window = mapOf(
                "id", "window." + operationId,
                "mode", "sequential",
                "epoch", epoch(sessionId),
                "observation_seq", tick - 1,
                "opened_at", timepoint(tick),
                "deadline", timepoint(tick + 1),
                "actor_ids", List.of(ACTOR_ID));
        return mapOf(
                "protocol_version", RinClient.PROTOCOL_VERSION,
                "session_id", sessionId,
                "request_id", "propose." + operationId,
                "actor_id", ACTOR_ID,
                "tick", tick,
                "intent", "Choose one bounded response to the player.",
                "tags", List.of("conversation"),
                "decision_window", window,
                "offers", List.of(
                        offer("offer.talk", "dialogue.talk",
                                "offer one concrete hint", window),
                        offer("offer.wait", "world.wait",
                                "ask the player to observe first", window),
                        offer("offer.refuse", "dialogue.refuse",
                                "decline an unsafe request", window)));
    }

    static Map<String, Object> report(
            MinecraftServer server,
            PendingTurn pending,
            Map<String, Object> proposal,
            boolean accepted,
            String outcome) {
        long tick = Math.max(
                server.getTicks(),
                Math.max(integer(proposal.get("tick")), integer(pending.request().get("tick"))));
        String sessionId = (String) pending.request().get("session_id");
        return HostActions.immediateReport(
                sessionId, "report." + pending.operationId(),
                "outcome." + pending.operationId(), tick, proposal,
                pending.operationId(), accepted, outcome, epoch(sessionId),
                tick, timepoint(tick), List.of("fabric-example", "conversation"));
    }

    private static Map<String, Object> offer(
            String offerId,
            String capabilityId,
            String description,
            Map<String, Object> window) {
        return HostActions.offer(
                offerId, ACTOR_ID, capabilityId, "1", "a".repeat(64),
                description, Map.of(), window);
    }

    private static Map<String, Object> epoch(String sessionId) {
        return mapOf(
                "session_id", sessionId,
                "world_id", "minecraft.server",
                "host", 1L,
                "world", 1L,
                "timeline", 1L);
    }

    private static Map<String, Object> timepoint(long tick) {
        return mapOf("clock", "step", "value", tick);
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
