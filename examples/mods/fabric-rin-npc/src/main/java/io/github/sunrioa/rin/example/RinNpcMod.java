package io.github.sunrioa.rin.example;

import com.google.gson.Gson;
import com.mojang.brigadier.Command;
import io.github.sunrioa.rin.HostDurability;
import io.github.sunrioa.rin.HostDurabilityProfile;
import io.github.sunrioa.rin.PendingTurn;
import io.github.sunrioa.rin.ProposalFreshness;
import io.github.sunrioa.rin.ResolvedPendingTurn;
import io.github.sunrioa.rin.RinClient;
import io.github.sunrioa.rin.RinException;
import io.github.sunrioa.rin.RinTransportException;
import io.github.sunrioa.rin.WorkflowCoordinator;
import net.fabricmc.api.ModInitializer;
import net.fabricmc.fabric.api.command.v2.CommandRegistrationCallback;
import net.minecraft.server.MinecraftServer;
import net.minecraft.server.command.ServerCommandSource;
import net.minecraft.server.network.ServerPlayerEntity;
import net.minecraft.text.Text;

import java.time.Duration;
import java.util.Map;
import java.util.Set;
import java.util.UUID;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.CompletionException;
import java.util.concurrent.ConcurrentHashMap;

import static net.minecraft.server.command.CommandManager.literal;

public final class RinNpcMod implements ModInitializer {
    private static final HostDurability DURABILITY =
            HostDurability.advisory(true);

    private final Set<UUID> activePlayers = ConcurrentHashMap.newKeySet();
    private final RinClient rin = new RinClient(
            System.getenv().getOrDefault("RIN_URL", RinClient.DEFAULT_BASE_URL),
            System.getenv().getOrDefault("RIN_TOKEN", ""),
            Duration.ofSeconds(5),
            RinClient.DEFAULT_MAX_RESPONSE_BYTES,
            new GsonJsonCodec(new Gson()));

    @Override
    public void onInitialize() {
        FabricHostRuntime.install();
        CommandRegistrationCallback.EVENT.register((dispatcher, registryAccess, environment) ->
                dispatcher.register(literal("rin-npc")
                        .then(literal("ask").executes(context -> {
                            requestTurn(context.getSource());
                            return Command.SINGLE_SUCCESS;
                        }))));
    }

    private void requestTurn(ServerCommandSource source) {
        ServerPlayerEntity player;
        try {
            player = source.getPlayerOrThrow();
        } catch (Exception ignored) {
            source.sendError(Text.literal("This command must be run by a player."));
            return;
        }
        UUID playerId = player.getUuid();
        if (!activePlayers.add(playerId)) {
            source.sendError(Text.literal("A Rin turn is already running for this player."));
            return;
        }

        MinecraftServer server = source.getServer();
        FabricHostRuntime host;
        try {
            host = FabricHostRuntime.current(server);
        } catch (IllegalStateException unavailable) {
            activePlayers.remove(playerId);
            source.sendError(Text.literal("The Rin Host is not accepting work."));
            return;
        }
        RinFabricState state = host.state();
        String sessionId = "fabric." + state.worldId + "." + playerId;
        FabricSessionState session = state.session(
                sessionId,
                RinNpcRequests.create(sessionId, player.getName().getString()));
        FabricWorkflowStore store = new FabricWorkflowStore(host, state, session);
        WorkflowCoordinator workflow = new WorkflowCoordinator(rin, store, DURABILITY);

        source.sendFeedback(
                () -> Text.literal("The Rin guide is considering the situation..."),
                false);
        ensureSession(session)
                .thenCompose(ignored -> workflow.drainOutbox())
                .thenCompose(ignored -> preparePendingTurn(
                        server, host, state, session, sessionId))
                .thenCompose(prepared -> rin.observe(prepared.observe)
                        .thenCompose(ignored -> workflow.resumePendingWork()))
                .thenCompose(resolved -> settle(
                        server, host, playerId, sessionId, workflow, resolved))
                .thenCompose(ignored -> workflow.drainOutbox())
                .thenRun(() -> host.send(playerId, "Rin outcome acknowledged."))
                .exceptionallyCompose(failure -> host.call(() -> {
                    Throwable cause = unwrap(failure);
                    boolean retained =
                            session.pendingTurn != null || !session.outcomes.isEmpty();
                    String message = !retained && isConnectionFailure(cause)
                            ? "Guide (offline): Stay safe, preserve resources, and observe first."
                            : "Rin did not apply a new action; retained work will resume: "
                                    + errorCode(cause);
                    ServerPlayerEntity current =
                            server.getPlayerManager().getPlayer(playerId);
                    if (current != null) current.sendMessage(Text.literal(message), false);
                    return null;
                }))
                .whenComplete((ignored, failure) -> activePlayers.remove(playerId));
    }

    private CompletableFuture<PreparedTurn> preparePendingTurn(
            MinecraftServer server,
            FabricHostRuntime host,
            RinFabricState state,
            FabricSessionState session,
            String sessionId) {
        return host.call(() -> {
            if (session.pendingTurn == null) {
                long turn = state.nextSequence();
                String operationId = state.worldId + "." + turn;
                long observedTick = server.getTicks();
                session.pendingObserve =
                        RinNpcRequests.observe(
                                sessionId, operationId, observedTick, host.epoch());
                session.pendingTurn = PendingTurn.create(
                        operationId,
                        RinNpcRequests.proposal(
                                sessionId,
                                operationId,
                                Math.incrementExact(observedTick),
                                host.epoch()));
                state.markDirty();
            }
            return new PreparedTurn(session.pendingTurn, session.pendingObserve);
        });
    }

    private CompletableFuture<Void> settle(
            MinecraftServer server,
            FabricHostRuntime host,
            UUID playerId,
            String sessionId,
            WorkflowCoordinator workflow,
            ResolvedPendingTurn resolved) {
        Map<String, Object> proposal = resolved.proposal();
        return rin.state(Map.of(
                        "protocol_version", RinClient.PROTOCOL_VERSION,
                        "session_id", sessionId))
                .handle((sessionState, stateFailure) -> stateFailure == null
                        ? ProposalFreshness.evaluate(sessionState, proposal)
                        : ProposalFreshness.Decision.STALE)
                .thenCompose(freshness -> FabricNpcActions.plan(
                        host, playerId, sessionId, proposal, freshness))
                .thenCompose(plan -> {
                    PendingTurn pending = resolved.pendingTurn();
                    Map<String, Object> report = RinNpcRequests.report(
                            server,
                            pending,
                            proposal,
                            plan.accepted(),
                            plan.outcome(),
                            host.epoch());
                    return workflow.applyAndEnqueueOutcome(
                            pending,
                            proposal,
                            report,
                            HostDurabilityProfile.ADVISORY,
                            ignored -> FabricNpcActions.apply(host, playerId, plan));
                });
    }

    private CompletableFuture<Void> ensureSession(
            FabricSessionState session) {
        return rin.createSession(session.createRequest).thenApply(ignored -> null);
    }

    private static boolean isConnectionFailure(Throwable failure) {
        return failure instanceof RinTransportException
                && Set.of("transport_failed", "transport_timeout")
                        .contains(((RinTransportException) failure).code());
    }

    private static String errorCode(Throwable failure) {
        return failure instanceof RinException rin ? rin.code() : "host_error";
    }

    private static Throwable unwrap(Throwable failure) {
        Throwable current = failure;
        while ((current instanceof CompletionException
                || current instanceof java.util.concurrent.ExecutionException)
                && current.getCause() != null) {
            current = current.getCause();
        }
        return current;
    }

    private record PreparedTurn(
            PendingTurn pending, Map<String, Object> observe) { }
}
