package io.github.sunrioa.rin.example;

import net.fabricmc.fabric.api.event.lifecycle.v1.ServerLifecycleEvents;
import net.minecraft.server.MinecraftServer;
import net.minecraft.server.network.ServerPlayerEntity;
import net.minecraft.text.Text;

import java.security.SecureRandom;
import java.util.IdentityHashMap;
import java.util.Map;
import java.util.UUID;
import java.util.concurrent.CompletableFuture;
import java.util.function.Supplier;

final class FabricHostRuntime {
    private static final System.Logger LOG =
            System.getLogger("io.github.sunrioa.rin.fabric");
    private static final Map<MinecraftServer, FabricHostRuntime> RUNTIMES =
            new IdentityHashMap<>();
    private static final SecureRandom RANDOM = new SecureRandom();

    private final MinecraftServer server;
    private final RinFabricState state;
    private final FabricHostEpoch epoch;
    private volatile boolean closed;

    private FabricHostRuntime(
            MinecraftServer server,
            RinFabricState state,
            FabricHostEpoch epoch) {
        this.server = server;
        this.state = state;
        this.epoch = epoch;
    }

    static void install() {
        ServerLifecycleEvents.SERVER_STARTED.register(FabricHostRuntime::start);
        ServerLifecycleEvents.SERVER_STOPPING.register(FabricHostRuntime::stop);
    }

    static synchronized FabricHostRuntime current(MinecraftServer server) {
        FabricHostRuntime runtime = RUNTIMES.get(server);
        if (runtime == null || runtime.closed) {
            throw new IllegalStateException("Rin Fabric Host is not accepting work");
        }
        return runtime;
    }

    FabricHostEpoch epoch() {
        return epoch;
    }

    RinFabricState state() {
        return state;
    }

    ServerPlayerEntity player(UUID playerId) {
        if (!server.isOnThread()) {
            throw new IllegalStateException("Minecraft player access requires server thread");
        }
        return server.getPlayerManager().getPlayer(playerId);
    }

    <T> CompletableFuture<T> call(Supplier<T> work) {
        if (closed) {
            return CompletableFuture.failedFuture(
                    new IllegalStateException("Rin Fabric Host is stopping"));
        }
        if (server.isOnThread()) {
            return complete(work);
        }
        CompletableFuture<T> future = new CompletableFuture<>();
        server.execute(() -> {
            if (closed) {
                future.completeExceptionally(
                        new IllegalStateException("Rin Fabric Host is stopping"));
                return;
            }
            try {
                future.complete(work.get());
            } catch (Throwable failure) {
                future.completeExceptionally(failure);
            }
        });
        return future;
    }

    void send(UUID playerId, String message) {
        call(() -> {
            ServerPlayerEntity player = player(playerId);
            if (player != null) player.sendMessage(Text.literal(message), false);
            return null;
        });
    }

    private static synchronized void start(MinecraftServer server) {
        if (!server.isOnThread() || RUNTIMES.containsKey(server)) {
            throw new IllegalStateException("Invalid Fabric server-start lifecycle");
        }
        RinFabricState state = RinFabricState.get(server);
        FabricHostEpoch.AuthorityKind kind =
                FabricHostEpoch.AuthorityKind.fromDedicated(server.isDedicated());
        long host = RANDOM.nextLong(1, FabricHostEpoch.MAX_GENERATION + 1);
        FabricHostEpoch epoch = state.beginHost(host, kind);
        RUNTIMES.put(server, new FabricHostRuntime(server, state, epoch));
        LOG.log(
                System.Logger.Level.INFO,
                "Rin Fabric Host bound {0} logical-server authority at Host {1}, Timeline {2}",
                kind.wireName(),
                host,
                epoch.timeline());
    }

    private static synchronized void stop(MinecraftServer server) {
        if (!server.isOnThread()) {
            throw new IllegalStateException("Invalid Fabric server-stop lifecycle");
        }
        FabricHostRuntime runtime = RUNTIMES.remove(server);
        if (runtime != null) runtime.closed = true;
    }

    private static <T> CompletableFuture<T> complete(Supplier<T> work) {
        try {
            return CompletableFuture.completedFuture(work.get());
        } catch (Throwable failure) {
            return CompletableFuture.failedFuture(failure);
        }
    }
}
