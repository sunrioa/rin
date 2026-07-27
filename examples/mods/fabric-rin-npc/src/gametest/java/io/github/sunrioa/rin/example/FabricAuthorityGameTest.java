package io.github.sunrioa.rin.example;

import net.fabricmc.fabric.api.gametest.v1.FabricGameTest;
import net.minecraft.server.MinecraftServer;
import net.minecraft.test.GameTest;
import net.minecraft.test.TestContext;

import java.util.concurrent.CompletableFuture;

public final class FabricAuthorityGameTest implements FabricGameTest {
    @GameTest(templateName = FabricGameTest.EMPTY_STRUCTURE)
    public void dedicatedServerOwnsAuthorityThread(TestContext context) {
        MinecraftServer server = context.getWorld().getServer();
        FabricHostRuntime runtime = FabricHostRuntime.current(server);

        context.assertTrue(server.isDedicated(), "GameTest did not start a dedicated server");
        context.assertTrue(
                server.isOnThread(),
                "authority_thread_nonblocking: GameTest is not on the server thread");
        context.assertEquals(
                FabricHostEpoch.AuthorityKind.DEDICATED,
                runtime.epoch().authorityKind(),
                "Rin Host authority kind is incorrect");
        CompletableFuture<Boolean> authorityCheck = runtime.call(server::isOnThread);
        context.assertTrue(authorityCheck.isDone(), "Server-thread dispatch was delayed");
        context.assertTrue(
                authorityCheck.getNow(false),
                "Fabric Host work left the owning server thread");
        context.complete();
    }
}
