package io.github.sunrioa.rin.example;

import java.util.Map;
import java.util.Objects;

record FabricHostEpoch(
        String worldId,
        long host,
        long world,
        long timeline,
        AuthorityKind authorityKind) {
    static final long MAX_GENERATION = 9_007_199_254_740_991L;

    FabricHostEpoch {
        Objects.requireNonNull(worldId, "worldId");
        Objects.requireNonNull(authorityKind, "authorityKind");
        if (worldId.isBlank() || !safeGeneration(host)
                || !safeGeneration(world) || !safeGeneration(timeline)) {
            throw new IllegalArgumentException("Fabric Host Epoch is invalid");
        }
    }

    Map<String, Object> wire(String sessionId) {
        return Map.of(
                "session_id", sessionId,
                "world_id", "minecraft." + worldId,
                "host", host,
                "world", world,
                "timeline", timeline);
    }

    boolean matchesProposal(String sessionId, Map<String, Object> proposal) {
        Map<String, Object> expected = wire(sessionId);
        Map<String, Object> window = object(proposal.get("decision_window"));
        Map<String, Object> action = object(proposal.get("action"));
        return sessionId.equals(proposal.get("session_id"))
                && expected.equals(window.get("epoch"))
                && expected.equals(action.get("expected_epoch"));
    }

    String authorityTag() {
        return "authority." + authorityKind.wireName();
    }

    private static boolean safeGeneration(long value) {
        return value > 0 && value <= MAX_GENERATION;
    }

    @SuppressWarnings("unchecked")
    private static Map<String, Object> object(Object value) {
        return value instanceof Map<?, ?> map
                ? (Map<String, Object>) map
                : Map.of();
    }

    enum AuthorityKind {
        INTEGRATED("integrated"),
        DEDICATED("dedicated");

        private final String wireName;

        AuthorityKind(String wireName) {
            this.wireName = wireName;
        }

        String wireName() {
            return wireName;
        }

        static AuthorityKind fromDedicated(boolean dedicated) {
            return dedicated ? DEDICATED : INTEGRATED;
        }

        static AuthorityKind fromWire(String value) {
            for (AuthorityKind kind : values()) {
                if (kind.wireName.equals(value)) return kind;
            }
            throw new IllegalArgumentException("Unknown Fabric authority kind");
        }
    }
}
