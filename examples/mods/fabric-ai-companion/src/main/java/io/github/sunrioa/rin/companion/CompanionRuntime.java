package io.github.sunrioa.rin.companion;

import net.minecraft.server.MinecraftServer;

import java.util.HashMap;
import java.util.IdentityHashMap;
import java.util.Map;
import java.util.UUID;
import java.util.concurrent.CompletableFuture;
import java.util.function.Supplier;

final class CompanionRuntime {
    private static final Map<MinecraftServer, CompanionRuntime> INSTANCES = new IdentityHashMap<>();
    private final MinecraftServer server;
    private final CompanionSavedState state;
    private final Map<UUID, CompanionEntity> liveCompanions = new HashMap<>();
    private boolean closed;

    private CompanionRuntime(MinecraftServer server) {
        this.server = server;
        this.state = server.overworld().getDataStorage().computeIfAbsent(CompanionSavedState.TYPE);
        state.hostGeneration++;
        state.timelineGeneration++;
        state.setDirty();
    }

    static synchronized CompanionRuntime forServer(MinecraftServer server) {
        return INSTANCES.computeIfAbsent(server, CompanionRuntime::new);
    }

    static synchronized void close(MinecraftServer server) {
        CompanionRuntime runtime = INSTANCES.remove(server);
        if (runtime != null) runtime.closed = true;
    }

    <T> CompletableFuture<T> call(Supplier<T> work) {
        if (closed) return CompletableFuture.failedFuture(new IllegalStateException("companion runtime is closed"));
        return server.isSameThread() ? CompletableFuture.completedFuture(work.get()) : server.submit(work);
    }

    boolean spawn(CompanionEntity entity) {
        if (closed || entity == null || liveCompanions.containsKey(entity.getUUID())) return false;
        liveCompanions.put(entity.getUUID(), entity);
        entity.level().addFreshEntity(entity);
        return true;
    }
}
