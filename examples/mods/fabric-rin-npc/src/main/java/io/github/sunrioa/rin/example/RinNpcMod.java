package io.github.sunrioa.rin.example;

import com.google.gson.Gson;
import com.mojang.brigadier.Command;
import io.github.sunrioa.rin.RinClient;
import io.github.sunrioa.rin.RinException;
import net.fabricmc.api.ModInitializer;
import net.fabricmc.fabric.api.command.v2.CommandRegistrationCallback;
import net.minecraft.server.MinecraftServer;
import net.minecraft.server.command.ServerCommandSource;
import net.minecraft.server.network.ServerPlayerEntity;
import net.minecraft.text.Text;

import java.time.Duration;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.UUID;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.CompletionException;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.atomic.AtomicLong;

import static net.minecraft.server.command.CommandManager.literal;

public final class RinNpcMod implements ModInitializer {
    private static final String ACTOR_ID = "npc.rin.guide";
    private static final Set<String> ALLOWED_ACTIONS = Set.of("talk", "wait", "refuse");

    private final String runId = UUID.randomUUID().toString().substring(0, 12);
    private final AtomicLong sequence = new AtomicLong();
    private final Map<String, CompletableFuture<Void>> sessions = new ConcurrentHashMap<>();
    private final Set<UUID> activePlayers = ConcurrentHashMap.newKeySet();
    private final RinClient rin = new RinClient(
            System.getenv().getOrDefault("RIN_URL", RinClient.DEFAULT_BASE_URL),
            System.getenv().getOrDefault("RIN_TOKEN", ""),
            Duration.ofSeconds(5),
            RinClient.DEFAULT_MAX_RESPONSE_BYTES,
            new GsonJsonCodec(new Gson()));

    @Override
    public void onInitialize() {
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
            source.sendError(Text.literal("This example command must be run by a player."));
            return;
        }

        MinecraftServer server = source.getServer();
        UUID playerId = player.getUuid();
        if (!activePlayers.add(playerId)) {
            source.sendError(Text.literal("A Rin turn is already running for this player."));
            return;
        }
        String sessionId = "fabric." + runId + "." + playerId;
        long turn = sequence.incrementAndGet();
        long tick = server.getTicks();
        source.sendFeedback(() -> Text.literal("The Rin guide is considering the situation..."), false);

        ensureSession(sessionId, player.getName().getString(), turn)
                .thenCompose(ignored -> rin.observe(mapOf(
                        "protocol_version", RinClient.PROTOCOL_VERSION,
                        "session_id", sessionId,
                        "request_id", "observe." + turn,
                        "event_id", "event." + turn,
                        "tick", tick,
                        "observer_ids", List.of(ACTOR_ID),
                        "source", "fabric-example",
                        "kind", "dialogue",
                        "summary", "The player asked the guide what to do next.",
                        "tags", List.of("conversation", "player-request"),
                        "importance", 3)))
                .thenCompose(ignored -> rin.submitProposalJob(mapOf(
                        "protocol_version", RinClient.PROTOCOL_VERSION,
                        "session_id", sessionId,
                        "request_id", "propose." + turn,
                        "actor_id", ACTOR_ID,
                        "tick", tick + 1,
                        "intent", "Choose one bounded response to the player.",
                        "tags", List.of("conversation"),
                        "candidate_actions", List.of(
                                mapOf("id", "talk", "kind", "dialogue", "description", "offer one concrete hint"),
                                mapOf("id", "wait", "kind", "wait", "description", "ask the player to observe first"),
                                mapOf("id", "refuse", "kind", "refuse", "description", "decline an unsafe request")))))
                .thenCompose(job -> rin.waitForProposal(text(job, "job_id")))
                .thenCompose(job -> applyAndCommit(server, playerId, sessionId, turn, tick + 2, job))
                .thenAccept(ignored -> server.execute(() -> {
                    ServerPlayerEntity current = server.getPlayerManager().getPlayer(playerId);
                    if (current != null) current.sendMessage(Text.literal("Rin turn committed."), false);
                }))
                .exceptionally(error -> {
                    String code = safeCode(error);
                    server.execute(() -> {
                        ServerPlayerEntity current = server.getPlayerManager().getPlayer(playerId);
                        if (current != null) current.sendMessage(Text.literal("Rin request failed: " + code), false);
                    });
                    return null;
                })
                .whenComplete((ignored, error) -> activePlayers.remove(playerId));
    }

    private CompletableFuture<Void> ensureSession(String sessionId, String playerName, long turn) {
        return sessions.computeIfAbsent(sessionId, key -> {
            CompletableFuture<Void> created = rin.createSession(mapOf(
                    "protocol_version", RinClient.PROTOCOL_VERSION,
                    "request_id", "create." + turn,
                    "session_id", sessionId,
                    "binding", mapOf(
                            "game_id", "minecraft-fabric",
                            "content_id", "rin-npc-example",
                            "content_version", "0.1.0",
                            "content_hash", "sha256:" + "0".repeat(64)),
                    "seed", turn,
                    "actors", List.of(mapOf(
                            "id", ACTOR_ID,
                            "kind", "npc",
                            "display_name", "Rin Guide",
                            "traits", List.of("observant", "careful"),
                            "boundaries", List.of(mapOf(
                                    "id", "boundary.no-griefing",
                                    "description", "Never suggest griefing or bypassing server rules.",
                                    "trigger_tags", List.of("unsafe"),
                                    "response", "refuse")),
                            "goals", List.of(mapOf(
                                    "id", "goal.help-player",
                                    "description", "Help " + playerName + " make one informed choice.",
                                    "priority", 4,
                                    "preferred_actions", List.of("talk"),
                                    "progress", 0,
                                    "target_progress", 3,
                                    "status", "active")),
                            "think_every_ticks", 20,
                            "enabled", true))))
                    .thenApply(ignored -> null);
            created.whenComplete((ignored, error) -> {
                if (error != null) sessions.remove(key, created);
            });
            return created;
        });
    }

    private CompletableFuture<Map<String, Object>> applyAndCommit(
            MinecraftServer server,
            UUID playerId,
            String sessionId,
            long turn,
            long tick,
            Map<String, Object> job) {
        Map<String, Object> proposal = object(job.get("proposal"));
        Map<String, Object> action = object(proposal.get("action"));
        String actionId = text(action, "id");
        String proposalId = text(proposal, "proposal_id");
        CompletableFuture<AppliedAction> applied = new CompletableFuture<>();

        server.execute(() -> {
            ServerPlayerEntity player = server.getPlayerManager().getPlayer(playerId);
            if (player == null) {
                applied.complete(new AppliedAction(false, "Player left before the proposal could be applied."));
                return;
            }
            if (!ALLOWED_ACTIONS.contains(actionId)) {
                applied.complete(new AppliedAction(false, "The game rejected an action outside its allowlist."));
                return;
            }
            String line = switch (actionId) {
                case "talk" -> "Guide: Check the nearby terrain, then choose a route with cover.";
                case "wait" -> "Guide: Let us watch one more cycle before acting.";
                case "refuse" -> "Guide: I cannot help with an action that breaks the server rules.";
                default -> throw new IllegalStateException("allowlist changed during apply");
            };
            player.sendMessage(Text.literal(line), false);
            applied.complete(new AppliedAction(true, line));
        });

        return applied.thenCompose(result -> rin.commit(mapOf(
                "protocol_version", RinClient.PROTOCOL_VERSION,
                "session_id", sessionId,
                "request_id", "commit." + turn,
                "proposal_id", proposalId,
                "event_id", "outcome." + turn,
                "tick", tick,
                "accepted", result.accepted(),
                "outcome", result.outcome(),
                "tags", List.of("fabric-example", "conversation"))));
    }

    private static Map<String, Object> mapOf(Object... entries) {
        if (entries.length % 2 != 0) throw new IllegalArgumentException("map entries must be key/value pairs");
        Map<String, Object> result = new LinkedHashMap<>();
        for (int index = 0; index < entries.length; index += 2) {
            result.put((String) entries[index], entries[index + 1]);
        }
        return result;
    }

    private static Map<String, Object> object(Object value) {
        if (!(value instanceof Map<?, ?> source)) return Map.of();
        Map<String, Object> result = new LinkedHashMap<>();
        source.forEach((key, item) -> {
            if (key instanceof String text) result.put(text, item);
        });
        return result;
    }

    private static String text(Map<String, Object> value, String key) {
        Object item = value.get(key);
        return item instanceof String text ? text : "";
    }

    private static String safeCode(Throwable error) {
        Throwable cause = error;
        while (cause instanceof CompletionException && cause.getCause() != null) cause = cause.getCause();
        return cause instanceof RinException rinError ? rinError.code() : "integration_failed";
    }

    private record AppliedAction(boolean accepted, String outcome) { }
}
