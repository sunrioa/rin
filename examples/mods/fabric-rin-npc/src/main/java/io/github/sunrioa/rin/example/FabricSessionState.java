package io.github.sunrioa.rin.example;

import io.github.sunrioa.rin.JsonValues;
import io.github.sunrioa.rin.OutcomeOutboxEntry;
import io.github.sunrioa.rin.PendingTurn;

import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Set;

final class FabricSessionState {
    private static final Set<String> BASE_KEYS =
            Set.of("create_request", "outcomes");
    private static final Set<String> PENDING_KEYS =
            Set.of("create_request", "pending_turn", "pending_observe", "outcomes");
    private static final Set<String> TURN_KEYS =
            Set.of("version", "operation_id", "request", "job_id");
    final Map<String, Object> createRequest;
    PendingTurn pendingTurn;
    Map<String, Object> pendingObserve = Map.of();
    final Map<String, OutcomeOutboxEntry> outcomes = new LinkedHashMap<>();
    FabricSessionState(Map<String, Object> createRequest) {
        this.createRequest = immutable(createRequest);
    }

    boolean hasCreateRequest(Map<String, Object> value) {
        return JsonValues.equivalent(createRequest, immutable(value));
    }

    Map<String, Object> toJson() {
        Map<String, Object> result = new LinkedHashMap<>();
        result.put("create_request", createRequest);
        if (pendingTurn != null) {
            result.put("pending_turn", Map.of(
                    "version", pendingTurn.version(),
                    "operation_id", pendingTurn.operationId(),
                    "request", pendingTurn.request(),
                    "job_id", pendingTurn.jobId()));
            result.put("pending_observe", pendingObserve);
        }
        List<Object> encodedOutcomes = new ArrayList<>();
        outcomes.values().forEach(entry -> encodedOutcomes.add(Map.of(
                "key", entry.key(),
                "report", entry.report())));
        result.put("outcomes", encodedOutcomes);
        return result;
    }

    static FabricSessionState fromJson(Map<String, Object> value) {
        if (!value.keySet().equals(BASE_KEYS)
                && !value.keySet().equals(PENDING_KEYS)) {
            throw new IllegalStateException("Rin session state has an invalid shape");
        }
        FabricSessionState session =
                new FabricSessionState(object(value.get("create_request"), "create_request"));
        if (value.containsKey("pending_turn")) {
            Map<String, Object> encodedPending =
                    object(value.get("pending_turn"), "pending_turn");
            if (!encodedPending.keySet().equals(TURN_KEYS)) {
                throw new IllegalStateException("Pending Turn has an invalid shape");
            }
            session.pendingTurn = new PendingTurn(
                    Math.toIntExact(integer(encodedPending.get("version"))),
                    text(encodedPending.get("operation_id")),
                    object(encodedPending.get("request"), "pending_turn.request"),
                    text(encodedPending.get("job_id")));
            session.pendingObserve = immutable(
                    object(value.get("pending_observe"), "pending_observe"));
            if (session.pendingObserve.isEmpty()) {
                throw new IllegalStateException("Pending Turn is missing its Observe");
            }
        }
        Object rawOutcomes = value.get("outcomes");
        if (!(rawOutcomes instanceof List<?> outcomes)
                || outcomes.size() > RinFabricState.MAX_OUTCOMES_PER_SESSION) {
            throw new IllegalStateException("Rin saved state outcomes are invalid");
        }
        for (Object raw : outcomes) {
            Map<String, Object> encoded = object(raw, "outcome");
            if (!encoded.keySet().equals(Set.of("key", "report"))) {
                throw new IllegalStateException("Rin outcome has an invalid shape");
            }
            OutcomeOutboxEntry entry = new OutcomeOutboxEntry(
                    text(encoded.get("key")),
                    object(encoded.get("report"), "outcome.report"));
            if (session.outcomes.putIfAbsent(entry.key(), entry) != null) {
                throw new IllegalStateException("Rin saved state has duplicate outcomes");
            }
        }
        return session;
    }

    private static Map<String, Object> immutable(Map<String, Object> value) {
        return PendingTurn.copyJsonObject(value);
    }

    @SuppressWarnings("unchecked")
    private static Map<String, Object> object(Object value, String field) {
        if (!(value instanceof Map<?, ?> map)) {
            throw new IllegalStateException(field + " must be an object");
        }
        return (Map<String, Object>) map;
    }

    private static String text(Object value) {
        return value instanceof String string ? string : "";
    }

    private static long integer(Object value) {
        if (!(value instanceof Number number)) return 0;
        double floating = number.doubleValue();
        long integral = number.longValue();
        if (!Double.isFinite(floating) || floating != integral) {
            throw new IllegalStateException("Rin saved integer is malformed");
        }
        return integral;
    }
}
