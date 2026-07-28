package io.github.sunrioa.rin.companion;

import io.github.sunrioa.rin.HostActions;
import io.github.sunrioa.rin.RinClient;

import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

final class CompanionRequests {
    static final String ACTOR_ID = "companion.partner";

    private CompanionRequests() {
    }

    static List<Map<String, Object>> offers(Map<String, Object> epoch, long observationSequence) {
        Map<String, Object> window = mapOf(
                "id", "window." + observationSequence,
                "epoch", epoch,
                "observation_seq", observationSequence,
                "deadline", timepoint(observationSequence + 1));
        return List.of(
                offer("reply", "dialogue.reply", "Reply to the owner", window),
                offer("follow", "movement.follow_owner", "Follow the owner", window),
                offer("stop", "movement.stop", "Stop moving", window),
                offer("wait", "activity.wait", "Wait safely", window),
                offer("refuse", "safety.refuse", "Refuse an unsafe request", window));
    }

    static Map<String, Object> propose(String sessionId, String operationId, long tick,
                                       Map<String, Object> epoch) {
        return mapOf(
                "protocol_version", RinClient.PROTOCOL_VERSION,
                "session_id", sessionId,
                "request_id", "propose." + operationId,
                "actor_id", ACTOR_ID,
                "tick", tick,
                "intent", "Choose one legal companion action.",
                "tags", List.of("companion", "owner-request"),
                "decision_window", mapOf(
                        "id", "window." + operationId,
                        "mode", "sequential",
                        "epoch", epoch,
                        "observation_seq", tick,
                        "opened_at", timepoint(tick),
                        "deadline", timepoint(tick + 1),
                        "actor_ids", List.of(ACTOR_ID)),
                "offers", offers(epoch, tick));
    }

    private static Map<String, Object> offer(String suffix, String capability, String description,
                                              Map<String, Object> window) {
        return HostActions.offer("offer." + suffix, ACTOR_ID, capability, "1", "a".repeat(64),
                description, Map.of(), window);
    }

    private static Map<String, Object> timepoint(long tick) {
        return mapOf("clock", "step", "value", tick);
    }

    private static Map<String, Object> mapOf(Object... entries) {
        Map<String, Object> result = new LinkedHashMap<>();
        for (int index = 0; index < entries.length; index += 2) {
            result.put((String) entries[index], entries[index + 1]);
        }
        return result;
    }
}
