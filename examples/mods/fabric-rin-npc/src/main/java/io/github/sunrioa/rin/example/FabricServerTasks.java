package io.github.sunrioa.rin.example;

import net.minecraft.server.MinecraftServer;
import net.minecraft.server.network.ServerPlayerEntity;
import net.minecraft.text.Text;

import java.util.UUID;
import java.util.concurrent.CompletableFuture;
import java.util.function.Supplier;

final class FabricServerTasks {
    private FabricServerTasks() { }

    static <T> CompletableFuture<T> call(
            MinecraftServer server,
            Supplier<T> work) {
        CompletableFuture<T> future = new CompletableFuture<>();
        server.execute(() -> {
            try {
                future.complete(work.get());
            } catch (Throwable failure) {
                future.completeExceptionally(failure);
            }
        });
        return future;
    }

    static void send(MinecraftServer server, UUID playerId, String message) {
        server.execute(() -> {
            ServerPlayerEntity player =
                    server.getPlayerManager().getPlayer(playerId);
            if (player != null) player.sendMessage(Text.literal(message), false);
        });
    }
}
