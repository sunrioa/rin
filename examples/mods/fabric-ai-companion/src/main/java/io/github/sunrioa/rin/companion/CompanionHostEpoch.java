package io.github.sunrioa.rin.companion;

import java.util.Objects;
import java.util.UUID;

record CompanionHostEpoch(UUID worldId, long hostGeneration, long timelineGeneration) {
    CompanionHostEpoch {
        Objects.requireNonNull(worldId, "worldId");
        if (hostGeneration <= 0 || timelineGeneration <= 0) throw new IllegalArgumentException("invalid companion host epoch");
    }
}
