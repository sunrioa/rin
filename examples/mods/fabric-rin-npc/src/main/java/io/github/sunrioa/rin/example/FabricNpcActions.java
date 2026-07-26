package io.github.sunrioa.rin.example;

import io.github.sunrioa.rin.ProposalFreshness;
import net.minecraft.server.network.ServerPlayerEntity;
import net.minecraft.text.Text;

import java.util.Map;
import java.util.Set;
import java.util.UUID;
import java.util.concurrent.CompletableFuture;

final class FabricNpcActions {
    private static final Set<String> ALLOWED_OFFERS =
            Set.of("offer.talk", "offer.wait", "offer.refuse");

    private FabricNpcActions() { }

    static CompletableFuture<Plan> plan(
            FabricHostRuntime host,
            UUID playerId,
            String sessionId,
            Map<String, Object> proposal,
            ProposalFreshness.Decision freshness) {
        return host.call(() -> {
            if (freshness != ProposalFreshness.Decision.FRESH
                    || !host.epoch().matchesProposal(sessionId, proposal)) {
                return new Plan(
                        false,
                        "The game rejected a stale or unverifiable proposal.",
                        "");
            }
            if (host.player(playerId) == null) {
                return new Plan(
                        false,
                        "The player left before the proposal could be applied.",
                        "");
            }
            String actionId = text(object(proposal.get("action")).get("offer_id"));
            if (!ALLOWED_OFFERS.contains(actionId)) {
                return new Plan(
                        false,
                        "The game rejected an action outside its allowlist.",
                        "");
            }
            String line = switch (actionId) {
                case "offer.talk" ->
                        "Guide: Check the nearby terrain, then choose a route with cover.";
                case "offer.wait" ->
                        "Guide: Let us watch one more cycle before acting.";
                case "offer.refuse" ->
                        "Guide: I cannot help with actions that break server rules.";
                default -> throw new IllegalStateException("Action allowlist changed");
            };
            return new Plan(true, line, line);
        });
    }

    static CompletableFuture<Void> apply(
            FabricHostRuntime host,
            UUID playerId,
            Plan plan) {
        return host.call(() -> {
            if (!plan.playerMessage().isEmpty()) {
                ServerPlayerEntity player = host.player(playerId);
                if (player != null) {
                    player.sendMessage(Text.literal(plan.playerMessage()), false);
                }
            }
            return null;
        });
    }

    @SuppressWarnings("unchecked")
    private static Map<String, Object> object(Object value) {
        return value instanceof Map<?, ?> map
                ? (Map<String, Object>) map
                : Map.of();
    }

    private static String text(Object value) {
        return value instanceof String string ? string : "";
    }

    record Plan(boolean accepted, String outcome, String playerMessage) { }
}
