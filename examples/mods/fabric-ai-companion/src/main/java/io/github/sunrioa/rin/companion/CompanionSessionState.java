package io.github.sunrioa.rin.companion;

import io.github.sunrioa.rin.OutcomeOutboxEntry;
import io.github.sunrioa.rin.PendingTurn;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.UUID;

final class CompanionSessionState {
    static final int MAX_OUTCOMES = 32;
    private static final Set<String> KEYS = Set.of("owner_id", "companion_id", "session_id", "display_name", "skin_profile", "mode", "create_request", "pending_observe", "pending_turn", "outcomes");
    private static final Set<String> TURN_KEYS = Set.of("version", "operation_id", "request", "job_id");
    final UUID ownerId;
    final UUID companionId;
    final String sessionId;
    String displayName;
    String skinProfile;
    String mode;
    final Map<String, Object> createRequest;
    Map<String, Object> pendingObserve = Map.of();
    PendingTurn pendingTurn;
    final LinkedHashMap<String, OutcomeOutboxEntry> outcomes = new LinkedHashMap<>();

    private CompanionSessionState(UUID ownerId, UUID companionId, String sessionId, String displayName, String skinProfile, String mode, Map<String, Object> createRequest) {
        this.ownerId = ownerId;
        this.companionId = companionId;
        this.sessionId = sessionId;
        this.displayName = displayName;
        this.skinProfile = skinProfile;
        this.mode = mode;
        this.createRequest = copy(createRequest);
    }

    static CompanionSessionState create(UUID worldId, UUID ownerId, UUID companionId, String displayName, String skinProfile, String mode, Map<String, Object> createRequest) {
        return new CompanionSessionState(ownerId, companionId, stableSessionId(worldId, ownerId, companionId), displayName, skinProfile, mode, createRequest);
    }

    static String stableSessionId(UUID worldId, UUID ownerId, UUID companionId) {
        return "fabric." + compact(worldId) + "." + compact(ownerId) + "." + compact(companionId);
    }

    Map<String, Object> toJson() {
        Map<String, Object> result = new LinkedHashMap<>();
        result.put("owner_id", ownerId.toString());
        result.put("companion_id", companionId.toString());
        result.put("session_id", sessionId);
        result.put("display_name", displayName);
        result.put("skin_profile", skinProfile);
        result.put("mode", mode);
        result.put("create_request", createRequest);
        result.put("pending_observe", pendingObserve);
        result.put("pending_turn", pendingTurn == null ? null : Map.of("version", pendingTurn.version(), "operation_id", pendingTurn.operationId(), "request", pendingTurn.request(), "job_id", pendingTurn.jobId()));
        List<Object> encodedOutcomes = new ArrayList<>();
        outcomes.values().forEach(entry -> encodedOutcomes.add(Map.of("key", entry.key(), "report", entry.report())));
        result.put("outcomes", encodedOutcomes);
        return result;
    }

    static CompanionSessionState fromJson(Map<String, Object> value) {
        if (!value.keySet().equals(KEYS)) throw invalid("session shape");
        CompanionSessionState session = new CompanionSessionState(uuid(value.get("owner_id"), "owner_id"), uuid(value.get("companion_id"), "companion_id"), text(value.get("session_id"), "session_id"), text(value.get("display_name"), "display_name"), string(value.get("skin_profile"), "skin_profile"), text(value.get("mode"), "mode"), object(value.get("create_request"), "create_request"));
        session.pendingObserve = copy(object(value.get("pending_observe"), "pending_observe"));
        if (value.get("pending_turn") != null) {
            Map<String, Object> turn = object(value.get("pending_turn"), "pending_turn");
            if (!turn.keySet().equals(TURN_KEYS)) throw invalid("pending turn shape");
            session.pendingTurn = new PendingTurn(integer(turn.get("version")), text(turn.get("operation_id"), "operation_id"), object(turn.get("request"), "request"), string(turn.get("job_id"), "job_id"));
        }
        if (!(value.get("outcomes") instanceof List<?> list) || list.size() > MAX_OUTCOMES) throw invalid("outcomes");
        for (Object raw : list) {
            Map<String, Object> encoded = object(raw, "outcome");
            if (!encoded.keySet().equals(Set.of("key", "report"))) throw invalid("outcome shape");
            OutcomeOutboxEntry entry = new OutcomeOutboxEntry(text(encoded.get("key"), "outcome.key"), object(encoded.get("report"), "outcome.report"));
            if (session.outcomes.putIfAbsent(entry.key(), entry) != null) throw invalid("duplicate outcome");
        }
        return session;
    }

    private static String compact(UUID id) { return id.toString().replace("-", ""); }
    private static Map<String, Object> copy(Map<String, Object> value) { return PendingTurn.copyJsonObject(value); }
    @SuppressWarnings("unchecked") private static Map<String, Object> object(Object value, String field) { if (!(value instanceof Map<?, ?> map)) throw invalid(field); return (Map<String, Object>) map; }
    private static UUID uuid(Object value, String field) { try { return UUID.fromString(text(value, field)); } catch (IllegalArgumentException exception) { throw invalid(field); } }
    private static String text(Object value, String field) { if (value instanceof String text && !text.isBlank()) return text; throw invalid(field); }
    private static String string(Object value, String field) { if (value instanceof String text) return text; throw invalid(field); }
    private static int integer(Object value) { if (!(value instanceof Number number) || number.doubleValue() != number.longValue()) throw invalid("integer"); return Math.toIntExact(number.longValue()); }
    private static IllegalStateException invalid(String field) { return new IllegalStateException("invalid companion " + field); }
}
