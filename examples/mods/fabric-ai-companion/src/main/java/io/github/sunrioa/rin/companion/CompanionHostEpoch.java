package io.github.sunrioa.rin.companion;

import java.util.Objects;
import java.util.Map;
import java.util.UUID;

record CompanionHostEpoch(UUID worldId, long hostGeneration, long timelineGeneration) {
    CompanionHostEpoch {
        Objects.requireNonNull(worldId, "worldId");
        if (hostGeneration <= 0 || timelineGeneration <= 0) throw new IllegalArgumentException("invalid companion host epoch");
    }

    Map<String, Object> wire(String sessionId) {
        return Map.of(
                "session_id", sessionId,
                "world_id", "minecraft." + worldId,
                "host", hostGeneration,
                "world", 1L,
                "timeline", timelineGeneration);
    }
}
