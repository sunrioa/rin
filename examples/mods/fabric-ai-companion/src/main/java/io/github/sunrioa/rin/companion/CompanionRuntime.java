package io.github.sunrioa.rin.companion;

import net.minecraft.server.MinecraftServer;
import net.minecraft.server.level.ServerLevel;
import net.minecraft.server.level.ServerPlayer;
import net.minecraft.network.chat.Component;
import net.minecraft.world.entity.EntitySpawnReason;
import net.minecraft.world.phys.Vec3;

import java.util.HashMap;
import java.util.IdentityHashMap;
import java.util.Map;
import java.util.UUID;
import java.util.concurrent.CompletableFuture;
import java.util.Set;
import java.util.concurrent.ConcurrentHashMap;
import java.util.function.Supplier;
import java.nio.file.Path;

final class CompanionRuntime {
    private static final Map<MinecraftServer, CompanionRuntime> INSTANCES = new IdentityHashMap<>();
    private final MinecraftServer server;
    private final CompanionSavedState state;
    private final CompanionConfigStore configStore;
    private final ManagedRinSidecar sidecar;
    private CompanionModelConfig modelConfig;
    private final Map<UUID, CompanionEntity> liveCompanions = new HashMap<>();
    private final Map<UUID, CompanionEntity> ownerCompanions = new HashMap<>();
    private final Set<UUID> activeTurns = ConcurrentHashMap.newKeySet();
    private boolean closed;

    private CompanionRuntime(MinecraftServer server) {
        this.server = server;
        this.state = server.overworld().getDataStorage().computeIfAbsent(CompanionSavedState.TYPE);
        Path serverDirectory = server.getServerDirectory();
        this.configStore = new CompanionConfigStore(serverDirectory.resolve("config/rin-ai-companion.properties"));
        this.modelConfig = configStore.load();
        this.sidecar = new ManagedRinSidecar(serverDirectory.resolve("rin/rin.exe"),
                serverDirectory.resolve("rin/data"), 7374, modelConfig, System::getenv,
                ManagedRinSidecar.systemProcessFactory(), ManagedRinSidecar.httpReadinessProbe());
        state.hostGeneration++;
        state.timelineGeneration++;
        state.setDirty();
    }

    static synchronized CompanionRuntime forServer(MinecraftServer server) {
        return INSTANCES.computeIfAbsent(server, CompanionRuntime::new);
    }

    static synchronized void close(MinecraftServer server) {
        CompanionRuntime runtime = INSTANCES.remove(server);
        if (runtime != null) {
            runtime.closed = true;
            runtime.sidecar.close();
        }
    }

    <T> CompletableFuture<T> call(Supplier<T> work) {
        if (closed) return CompletableFuture.failedFuture(new IllegalStateException("companion runtime is closed"));
        return server.isSameThread() ? CompletableFuture.completedFuture(work.get()) : server.submit(work);
    }

    boolean spawn(CompanionEntity entity) {
        if (closed || entity == null || liveCompanions.containsKey(entity.getUUID())) return false;
        liveCompanions.put(entity.getUUID(), entity);
        if (entity.ownerId() != null) ownerCompanions.put(entity.ownerId(), entity);
        entity.level().addFreshEntity(entity);
        return true;
    }

    boolean spawnFor(UUID ownerId, ServerLevel level, Vec3 position) {
        CompanionEntity existing = ownerCompanions.get(ownerId);
        if (existing != null && !existing.isRemoved()) return false;
        CompanionEntity entity = CompanionEntities.TYPE.create(level, EntitySpawnReason.COMMAND);
        if (entity == null) return false;
        entity.setUUID(UUID.randomUUID());
        entity.setOwnerId(ownerId);
        entity.setMode(CompanionEntity.Mode.STOPPED);
        entity.setCustomName(Component.translatable("entity.rin_ai_companion.companion"));
        entity.setCustomNameVisible(true);
        entity.setPos(position);
        if (!spawn(entity)) return false;
        CompanionSessionState session = CompanionSessionState.create(state.worldId, ownerId, entity.getUUID(),
                "伙伴", "", entity.mode().name(), Map.of("owner_id", ownerId.toString()));
        state.sessions.put(session.sessionId, session);
        state.setDirty();
        return true;
    }

    boolean setMode(UUID ownerId, CompanionEntity.Mode mode) {
        CompanionEntity entity = owned(ownerId);
        if (entity == null) return false;
        entity.setMode(mode);
        session(entity).mode = mode.name();
        state.setDirty();
        return true;
    }

    boolean recall(UUID ownerId, ServerLevel level, Vec3 position) {
        CompanionEntity entity = owned(ownerId);
        if (entity == null || entity.level() != level) return false;
        entity.setPos(position);
        entity.getNavigation().stop();
        return true;
    }

    String status(UUID ownerId) {
        CompanionEntity entity = owned(ownerId);
        return entity == null ? "missing" : entity.mode().name().toLowerCase();
    }

    boolean setSkin(UUID ownerId, String profile) {
        if (!profile.matches("[A-Za-z0-9_]{3,16}")) return false;
        CompanionEntity entity = owned(ownerId);
        if (entity == null) return false;
        session(entity).skinProfile = profile;
        state.setDirty();
        return true;
    }

    void handleChat(ServerPlayer player, String message) {
        CompanionEntity entity = owned(player.getUUID());
        if (entity == null) {
            player.sendSystemMessage(Component.translatable("rin_ai_companion.chat.missing"));
            return;
        }
        if (!activeTurns.add(entity.getUUID())) {
            player.sendSystemMessage(Component.literal("伙伴：我正在想上一件事。"));
            return;
        }
        long requestSequence = ++state.sequence;
        state.setDirty();
        CompletableFuture.runAsync(() -> {
            try {
                sidecar.start();
                CompanionDialogue.generate(sidecar.client(), "generate." + requestSequence, message,
                        "dialogue.reply", entity.mode().name(), "玩家在当前世界与伙伴对话。")
                        .thenAccept(line -> server.execute(() ->
                                player.sendSystemMessage(Component.literal("伙伴：" + line))))
                        .whenComplete((ignored, failure) -> activeTurns.remove(entity.getUUID()));
            } catch (RuntimeException unavailable) {
                activeTurns.remove(entity.getUUID());
                server.execute(() -> player.sendSystemMessage(
                        Component.literal("伙伴：" + CompanionDialogue.fallback("dialogue.reply"))));
            }
        });
    }

    CompanionModelConfig modelConfig() {
        return modelConfig;
    }

    boolean setBaseUrl(String value) {
        try {
            modelConfig = CompanionModelConfig.create(value, modelConfig.model());
            configStore.save(modelConfig);
            return true;
        } catch (RuntimeException invalid) {
            return false;
        }
    }

    boolean setModel(String value) {
        try {
            modelConfig = CompanionModelConfig.create(modelConfig.baseUrl().toString(), value);
            configStore.save(modelConfig);
            return true;
        } catch (RuntimeException invalid) {
            return false;
        }
    }

    boolean applyModelConfig() {
        try {
            sidecar.applyConfig(modelConfig);
            return true;
        } catch (RuntimeException unavailable) {
            return false;
        }
    }

    private CompanionEntity owned(UUID ownerId) {
        CompanionEntity entity = ownerCompanions.get(ownerId);
        return entity == null || entity.isRemoved() ? null : entity;
    }

    private CompanionSessionState session(CompanionEntity entity) {
        String id = CompanionSessionState.stableSessionId(state.worldId, entity.ownerId(), entity.getUUID());
        CompanionSessionState session = state.sessions.get(id);
        if (session == null) throw new IllegalStateException("companion session is missing");
        return session;
    }
}
