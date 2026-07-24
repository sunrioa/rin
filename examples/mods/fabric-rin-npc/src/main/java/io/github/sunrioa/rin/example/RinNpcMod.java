package io.github.sunrioa.rin.example;

import com.google.gson.Gson;
import com.mojang.brigadier.Command;
import io.github.sunrioa.rin.RinApiException;
import io.github.sunrioa.rin.RinClient;
import io.github.sunrioa.rin.RinException;
import io.github.sunrioa.rin.RinProtocolException;
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
    private static final int MAX_PROPOSAL_POSTS_PER_ENTRY = 2;
    private static final Set<String> ALLOWED_ACTIONS = Set.of("talk", "wait", "refuse");
    private static final Set<String> TERMINAL_COMMIT_ERRORS =
            Set.of("session_not_found", "unknown_proposal", "proposal_resolved");
    private static final Set<String> AMBIGUOUS_PROPOSAL_ERRORS =
            Set.of("job_cancel_unconfirmed", "job_outcome_unknown",
                    "job_timeout", "proposal_outcome_unknown");

    private final String runId = UUID.randomUUID().toString();
    private final AtomicLong sequence = new AtomicLong();
    private final Map<String, SessionRegistration> sessions = new ConcurrentHashMap<>();
    private final Map<String, AppliedAction> appliedOperations = new ConcurrentHashMap<>();
    private final Map<String, PendingOutcome> outcomeOutbox = new ConcurrentHashMap<>();
    private final Set<UUID> activePlayers = ConcurrentHashMap.newKeySet();
    private final Object persistenceLock = new Object();
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
        SessionRegistration registration = sessions.computeIfAbsent(
                sessionId,
                ignored -> newSessionRegistration(sessionId, player.getName().getString()));
        source.sendFeedback(() -> Text.literal("The Rin guide is considering the situation..."), false);

        ensureSession(registration)
                .handle((ignored, createError) -> {
                    if (createError == null) {
                        return runOnlineTurn(server, playerId, registration);
                    }
                    // Authored fallback is permitted only before Rin has ever
                    // supplied a proposal. A retained report must be flushed
                    // first, and an unresolved attempt must retain its exact
                    // identity, so an outage cannot start another turn.
                    if (hasPendingOutcome(sessionId)
                            || retainedProposalAttempt(registration) != null) {
                        return CompletableFuture.<Void>failedFuture(unwrap(createError));
                    }
                    long turn = sequence.incrementAndGet();
                    return applyAuthoredOfflineFallback(server, playerId, registration, turn);
                })
                .thenCompose(future -> future)
                .exceptionally(error -> {
                    invalidateSessionIfNotFound(sessionId, error);
                    String code = safeCode(error);
                    boolean blockedByDurableState =
                            hasPendingOutcome(sessionId)
                                    || retainedProposalAttempt(registration) != null;
                    String message = blockedByDurableState
                            ? "A durable outcome or unresolved proposal blocks a new turn: " + code
                            : "Rin request failed before applying a game action: " + code;
                    server.execute(() -> {
                        ServerPlayerEntity current = server.getPlayerManager().getPlayer(playerId);
                        if (current != null) current.sendMessage(Text.literal(message), false);
                    });
                    return null;
                })
                .whenComplete((ignored, error) -> activePlayers.remove(playerId));
    }

    private CompletableFuture<Void> runOnlineTurn(
            MinecraftServer server,
            UUID playerId,
            SessionRegistration registration) {
        String sessionId = registration.sessionId;
        return flushOutcomeOutbox(sessionId)
                .thenCompose(ignored -> {
                    ProposalAttempt attempt = retainNewProposalAttempt(
                            registration, server.getTicks());
                    // Replaying the exact Observe closes an ambiguous response
                    // before resuming this same persisted Propose identity.
                    return rin.observe(attempt.observeRequest)
                            .thenCompose(observed -> resolveProposalAttempt(
                                    registration,
                                    attempt,
                                    MAX_PROPOSAL_POSTS_PER_ENTRY));
                })
                .thenCompose(resolution -> {
                    if (resolution.useAuthoredFallback) {
                        if ("session_not_found".equals(resolution.reason)) {
                            invalidateSession(sessionId);
                        }
                        return applyRetainedAuthoredFallback(
                                server,
                                playerId,
                                registration,
                                resolution.attempt);
                    }
                    // A temporary State failure fails closed. It must never
                    // turn an already-online model proposal into fallback.
                    return revalidateApplyAndReport(
                            server,
                            playerId,
                            registration,
                            resolution.attempt,
                            resolution.proposal);
                })
                .thenRun(() -> server.execute(() -> {
                    ServerPlayerEntity current = server.getPlayerManager().getPlayer(playerId);
                    if (current != null) current.sendMessage(Text.literal("Rin outcome acknowledged."), false);
                }));
    }

    private ProposalAttempt retainNewProposalAttempt(
            SessionRegistration registration,
            long observedTick) {
        synchronized (persistenceLock) {
            if (registration.proposalAttempt != null) {
                return registration.proposalAttempt;
            }
            long turn = sequence.updateAndGet(Math::incrementExact);
            String operationId = runId + "." + turn;
            String requestId = "propose." + operationId;
            long proposeTick = Math.incrementExact(observedTick);
            ProposalAttempt retained = new ProposalAttempt(
                    registration.sessionId,
                    operationId,
                    turn,
                    requestId,
                    proposeTick,
                    mapOf(
                            "protocol_version", RinClient.PROTOCOL_VERSION,
                            "session_id", registration.sessionId,
                            "request_id", "observe." + operationId,
                            "event_id", "event." + operationId,
                            "tick", observedTick,
                            "observer_ids", List.of(ACTOR_ID),
                            "source", "fabric-example",
                            "kind", "dialogue",
                            "summary", "The player asked the guide what to do next.",
                            "tags", List.of("conversation", "player-request"),
                            "importance", 3),
                    mapOf(
                            "protocol_version", RinClient.PROTOCOL_VERSION,
                            "session_id", registration.sessionId,
                            "request_id", requestId,
                            "actor_id", ACTOR_ID,
                            "tick", proposeTick,
                            "intent", "Choose one bounded response to the player.",
                            "tags", List.of("conversation"),
                            "candidate_actions", List.of(
                                    mapOf("id", "talk", "kind", "dialogue",
                                            "description", "offer one concrete hint"),
                                    mapOf("id", "wait", "kind", "wait",
                                            "description", "ask the player to observe first"),
                                    mapOf("id", "refuse", "kind", "refuse",
                                            "description", "decline an unsafe request"))));

            // PRODUCTION PERSISTENCE HOOK: store the complete Observe and
            // Propose payloads, operation ID, sequence, and empty optional job
            // ID in player/world data before the first proposal POST.
            registration.proposalAttempt = retained;
            return retained;
        }
    }

    private ProposalAttempt retainedProposalAttempt(SessionRegistration registration) {
        synchronized (persistenceLock) {
            return registration.proposalAttempt;
        }
    }

    private CompletableFuture<ProposalResolution> resolveProposalAttempt(
            SessionRegistration registration,
            ProposalAttempt attempt,
            int remainingPosts) {
        String jobId = retainedProposalJobId(registration, attempt);
        if (jobId.isEmpty()) {
            return repostProposalAttempt(registration, attempt, remainingPosts);
        }
        return waitForRetainedProposal(
                registration, attempt, jobId, remainingPosts);
    }

    private CompletableFuture<ProposalResolution> repostProposalAttempt(
            SessionRegistration registration,
            ProposalAttempt attempt,
            int remainingPosts) {
        if (remainingPosts <= 0) {
            return CompletableFuture.failedFuture(unknownProposalOutcome(null));
        }
        return rin.submitProposalJob(attempt.proposeRequest)
                .thenApply(queued -> {
                    String jobId = text(queued, "job_id");
                    if (jobId.isEmpty()) {
                        throw new IllegalStateException(
                                "Rin response is missing proposal job_id");
                    }
                    // Persist the 202 handle before any GET/wait begins.
                    persistProposalJobId(registration, attempt, jobId);
                    return jobId;
                })
                .thenCompose(jobId -> waitForRetainedProposal(
                        registration,
                        attempt,
                        jobId,
                        remainingPosts - 1));
    }

    private CompletableFuture<ProposalResolution> waitForRetainedProposal(
            SessionRegistration registration,
            ProposalAttempt attempt,
            String jobId,
            int remainingPosts) {
        return inspectAndWaitForRetainedProposal(attempt, jobId)
                .handle((resolution, error) -> {
                    if (error == null) {
                        return CompletableFuture.completedFuture(resolution);
                    }

                    Throwable cause = unwrap(error);
                    String code = safeCode(cause);
                    if (shouldRepostProposal(code)) {
                        // Job retention expired, or the durable Proposal result
                        // was unknown. Drop only the lookup handle and boundedly
                        // POST the exact same request_id/payload again.
                        persistProposalJobId(registration, attempt, "");
                        if (remainingPosts <= 0) {
                            return CompletableFuture.<ProposalResolution>failedFuture(
                                    unknownProposalOutcome(cause));
                        }
                        return repostProposalAttempt(
                                registration, attempt, remainingPosts);
                    }
                    if (isConfirmedSafeTerminal(cause)) {
                        return CompletableFuture.completedFuture(
                                ProposalResolution.authoredFallback(
                                        attempt, code));
                    }
                    return CompletableFuture.<ProposalResolution>failedFuture(cause);
                })
                .thenCompose(future -> future);
    }

    private CompletableFuture<ProposalResolution> inspectAndWaitForRetainedProposal(
            ProposalAttempt attempt,
            String jobId) {
        // Validate the immutable Job envelope before SDK polling. If it later
        // reports a terminal error, that terminal belongs to this exact
        // retained session/request/job identity.
        return rin.getProposalJob(jobId)
                .thenCompose(currentJob -> {
                    validateJobIdentity(attempt, jobId, currentJob);
                    String status = text(currentJob, "status");
                    if ("succeeded".equals(status)) {
                        return CompletableFuture.completedFuture(
                                ProposalResolution.fromProposal(
                                        attempt,
                                        validateProposalIdentity(
                                                attempt, jobId, currentJob)));
                    }
                    if (Set.of("failed", "stale", "canceled").contains(status)) {
                        return CompletableFuture.<ProposalResolution>failedFuture(
                                terminalJobError(currentJob, status));
                    }
                    if (!"queued".equals(status) && !"running".equals(status)) {
                        return CompletableFuture.failedFuture(
                                new RinProtocolException(
                                        "invalid_job",
                                        "Rin returned an unknown proposal Job status"));
                    }
                    return rin.waitForProposal(jobId)
                            .thenApply(job -> ProposalResolution.fromProposal(
                                    attempt,
                                    validateProposalIdentity(
                                            attempt, jobId, job)));
                });
    }

    private String retainedProposalJobId(
            SessionRegistration registration,
            ProposalAttempt attempt) {
        synchronized (persistenceLock) {
            if (registration.proposalAttempt != attempt) {
                throw new IllegalStateException(
                        "proposal attempt changed while resolving its job");
            }
            return attempt.jobId;
        }
    }

    private void persistProposalJobId(
            SessionRegistration registration,
            ProposalAttempt attempt,
            String jobId) {
        synchronized (persistenceLock) {
            if (registration.proposalAttempt != attempt) {
                throw new IllegalStateException(
                        "proposal attempt changed before its job ID was persisted");
            }
            // PRODUCTION PERSISTENCE HOOK: atomically update the optional job
            // ID immediately after a 202 and before the first GET.
            attempt.jobId = jobId;
        }
    }

    private static boolean shouldRepostProposal(String code) {
        return "job_not_found".equals(code)
                || "proposal_outcome_unknown".equals(code);
    }

    private static boolean isConfirmedSafeTerminal(Throwable error) {
        Throwable cause = unwrap(error);
        return cause instanceof RinApiException apiError
                && apiError.status() == 0
                && !AMBIGUOUS_PROPOSAL_ERRORS.contains(apiError.code());
    }

    private static RinApiException unknownProposalOutcome(Throwable cause) {
        String message = "Proposal outcome remains unknown after bounded same-request retries";
        if (cause != null && cause.getMessage() != null) {
            message += ": " + cause.getMessage();
        }
        return new RinApiException(
                "proposal_outcome_unknown", message, 0, "");
    }

    private static Map<String, Object> validateProposalIdentity(
            ProposalAttempt attempt,
            String expectedJobId,
            Map<String, Object> job) {
        validateJobIdentity(attempt, expectedJobId, job);

        Map<String, Object> proposal = object(job.get("proposal"));
        if (text(proposal, "id").isEmpty()
                || !attempt.sessionId.equals(text(proposal, "session_id"))
                || !attempt.requestId.equals(text(proposal, "request_id"))
                || !ACTOR_ID.equals(text(proposal, "actor_id"))
                || integer(proposal.get("tick"), Long.MIN_VALUE)
                        != attempt.proposeTick) {
            throw new RinProtocolException(
                    "proposal_identity_mismatch",
                    "Rin returned a Proposal for a different retained proposal attempt");
        }
        return proposal;
    }

    private static void validateJobIdentity(
            ProposalAttempt attempt,
            String expectedJobId,
            Map<String, Object> job) {
        if (!expectedJobId.equals(text(job, "job_id"))
                || !attempt.sessionId.equals(text(job, "session_id"))
                || !attempt.requestId.equals(text(job, "request_id"))) {
            throw new RinProtocolException(
                    "proposal_identity_mismatch",
                    "Rin returned a Job for a different retained proposal attempt");
        }
    }

    private static RinApiException terminalJobError(
            Map<String, Object> job,
            String status) {
        Map<String, Object> detail = object(job.get("error"));
        String code = text(detail, "code");
        String message = text(detail, "message");
        return new RinApiException(
                code.isEmpty() ? "job_" + status : code,
                message.isEmpty()
                        ? "Rin proposal Job ended as " + status
                        : message,
                0,
                "");
    }

    private void invalidateSessionIfNotFound(String sessionId, Throwable error) {
        Throwable current = unwrap(error);
        while (current != null) {
            if (current instanceof RinException rinError
                    && "session_not_found".equals(rinError.code())) {
                invalidateSession(sessionId);
                return;
            }
            current = current.getCause();
        }
    }

    private FreshnessDecision unavailableFreshness(
            String sessionId,
            Throwable stateError) {
        invalidateSessionIfNotFound(sessionId, stateError);
        return FreshnessDecision.UNAVAILABLE;
    }

    private SessionRegistration newSessionRegistration(String sessionId, String playerName) {
        long seed = Integer.toUnsignedLong(sessionId.hashCode());
        Map<String, Object> request = mapOf(
                "protocol_version", RinClient.PROTOCOL_VERSION,
                "request_id", "create." + sessionId,
                "session_id", sessionId,
                "binding", mapOf(
                        "game_id", "minecraft-fabric",
                        "content_id", "rin-npc-example",
                        "content_version", "0.6.0",
                        "content_hash", "sha256:" + "0".repeat(64)),
                "seed", seed,
                "features", List.of("outcome-reporting-v1"),
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
                        "enabled", true)));
        // The entire request, including request_id and seed, is retained and
        // reused byte-for-byte semantically after ambiguous create failures.
        return new SessionRegistration(sessionId, request);
    }

    private CompletableFuture<Void> ensureSession(SessionRegistration registration) {
        synchronized (registration) {
            if (registration.createAttempt != null) return registration.createAttempt;
            CompletableFuture<Void> attempt = rin.createSession(registration.createRequest)
                    .thenApply(ignored -> null);
            registration.createAttempt = attempt;
            attempt.whenComplete((ignored, error) -> {
                if (error != null) {
                    synchronized (registration) {
                        if (registration.createAttempt == attempt) registration.createAttempt = null;
                    }
                }
            });
            return attempt;
        }
    }

    private void invalidateSession(String sessionId) {
        SessionRegistration registration = sessions.get(sessionId);
        if (registration == null) return;
        synchronized (registration) {
            registration.createAttempt = null;
        }
    }

    private CompletableFuture<Void> revalidateApplyAndReport(
            MinecraftServer server,
            UUID playerId,
            SessionRegistration registration,
            ProposalAttempt attempt,
            Map<String, Object> proposal) {
        String proposalId = text(proposal, "id");
        if (proposalId.isEmpty()) {
            return CompletableFuture.failedFuture(
                    new IllegalStateException("Rin response is missing proposal.id"));
        }
        return rin.state(mapOf(
                        "protocol_version", RinClient.PROTOCOL_VERSION,
                        "session_id", registration.sessionId))
                .handle((state, stateError) -> applyAndEnqueueOnServerThread(
                        server,
                        playerId,
                        registration,
                        attempt,
                        proposal,
                        stateError == null
                                ? proposalFreshness(state, proposal)
                                : unavailableFreshness(
                                        registration.sessionId,
                                        stateError)))
                .thenCompose(future -> future)
                .thenCompose(ignored -> reportOutcome(attempt.operationId));
    }

    private static FreshnessDecision proposalFreshness(
            Map<String, Object> state,
            Map<String, Object> proposal) {
        String proposalId = text(proposal, "id");
        Map<String, Object> retained = object(object(state.get("proposals")).get(proposalId));
        if (!"pending".equals(text(retained, "status"))) {
            return FreshnessDecision.STALE;
        }

        long basedOnWorldRevision = integer(proposal.get("based_on_world_revision"), 0);
        if (basedOnWorldRevision > 0) {
            return integer(state.get("world_revision"), -1) == basedOnWorldRevision
                    ? FreshnessDecision.FRESH
                    : FreshnessDecision.STALE;
        }
        return integer(state.get("revision"), -1)
                == integer(proposal.get("created_revision"), -2)
                ? FreshnessDecision.FRESH
                : FreshnessDecision.STALE;
    }

    private CompletableFuture<AppliedAction> applyAndEnqueueOnServerThread(
            MinecraftServer server,
            UUID playerId,
            SessionRegistration registration,
            ProposalAttempt completedAttempt,
            Map<String, Object> proposal,
            FreshnessDecision freshness) {
        String operationId = completedAttempt.operationId;
        AppliedAction stored = appliedOperations.get(operationId);
        if (stored != null) return CompletableFuture.completedFuture(stored);

        CompletableFuture<AppliedAction> completion = new CompletableFuture<>();
        server.execute(() -> {
            try {
                AppliedAction existing = appliedOperations.get(operationId);
                if (existing != null) {
                    completion.complete(existing);
                    return;
                }
                Map<String, Object> action = object(proposal.get("action"));
                String actionId = text(action, "id");
                ServerPlayerEntity player = server.getPlayerManager().getPlayer(playerId);
                AppliedAction planned;
                Runnable gameEffect;
                if (freshness == FreshnessDecision.UNAVAILABLE) {
                    planned = new AppliedAction(false,
                            "The game rejected the proposal because freshness could not be verified.");
                    gameEffect = () -> { };
                } else if (freshness == FreshnessDecision.STALE) {
                    planned = new AppliedAction(false,
                            "The game rejected a stale proposal before applying it.");
                    gameEffect = () -> { };
                } else if (player == null) {
                    planned = new AppliedAction(false,
                            "The player left before the proposal could be applied.");
                    gameEffect = () -> { };
                } else if (!ALLOWED_ACTIONS.contains(actionId)) {
                    planned = new AppliedAction(false,
                            "The game rejected an action outside its allowlist.");
                    gameEffect = () -> { };
                } else {
                    String line = switch (actionId) {
                        case "talk" -> "Guide: Check the nearby terrain, then choose a route with cover.";
                        case "wait" -> "Guide: Let us watch one more cycle before acting.";
                        case "refuse" -> "Guide: I cannot help with an action that breaks the server rules.";
                        default -> throw new IllegalStateException("allowlist changed during apply");
                    };
                    planned = new AppliedAction(true, line);
                    gameEffect = () -> player.sendMessage(Text.literal(line), false);
                }

                // The outcome cannot precede either the retained request or
                // the returned Proposal, even in the same server tick.
                long occurrenceTick = Math.max(
                        server.getTicks(),
                        Math.max(
                                completedAttempt.proposeTick,
                                integer(
                                        proposal.get("tick"),
                                        completedAttempt.proposeTick)));
                PendingOutcome pending = commitPending(
                        registration.sessionId,
                        operationId,
                        text(proposal, "id"),
                        occurrenceTick,
                        planned);
                if (!persistAuthoritativeTransaction(
                        registration,
                        completedAttempt,
                        operationId,
                        planned,
                        pending,
                        gameEffect)) {
                    throw new IllegalStateException("authoritative game transaction was not persisted");
                }
                completion.complete(planned);
            } catch (Throwable error) {
                completion.completeExceptionally(error);
            }
        });
        return completion;
    }

    private CompletableFuture<Void> applyAuthoredOfflineFallback(
            MinecraftServer server,
            UUID playerId,
            SessionRegistration registration,
            long turn) {
        return applyAuthoredFallbackTransaction(
                server,
                playerId,
                registration,
                runId + ".offline." + turn,
                null);
    }

    private CompletableFuture<Void> applyRetainedAuthoredFallback(
            MinecraftServer server,
            UUID playerId,
            SessionRegistration registration,
            ProposalAttempt completedAttempt) {
        return applyAuthoredFallbackTransaction(
                        server,
                        playerId,
                        registration,
                        completedAttempt.operationId,
                        completedAttempt)
                .thenCompose(ignored -> reportOutcome(completedAttempt.operationId));
    }

    private CompletableFuture<Void> applyAuthoredFallbackTransaction(
            MinecraftServer server,
            UUID playerId,
            SessionRegistration registration,
            String operationId,
            ProposalAttempt completedAttempt) {
        CompletableFuture<Void> completion = new CompletableFuture<>();
        server.execute(() -> {
            try {
                ServerPlayerEntity player = server.getPlayerManager().getPlayer(playerId);
                String line = "Guide (offline): Stay safe, preserve your resources, and observe before acting.";
                AppliedAction applied;
                Runnable effect;
                if (player == null) {
                    applied = new AppliedAction(
                            false,
                            "The player left before the authored fallback could be applied.");
                    effect = () -> { };
                } else {
                    applied = new AppliedAction(true, line);
                    effect = () -> player.sendMessage(Text.literal(line), false);
                }
                long occurrenceTick = completedAttempt == null
                        ? server.getTicks()
                        : Math.max(
                                server.getTicks(),
                                completedAttempt.proposeTick);
                PendingOutcome pending = observePending(
                        registration.sessionId,
                        operationId,
                        occurrenceTick,
                        applied);
                if (!persistAuthoritativeTransaction(
                        registration,
                        completedAttempt,
                        operationId,
                        applied,
                        pending,
                        effect)) {
                    throw new IllegalStateException("offline game transaction was not persisted");
                }
                completion.complete(null);
            } catch (Throwable error) {
                completion.completeExceptionally(error);
            }
        });
        return completion;
    }

    private PendingOutcome commitPending(
            String sessionId,
            String operationId,
            String proposalId,
            long occurrenceTick,
            AppliedAction applied) {
        Map<String, Object> commitRequest = mapOf(
                "protocol_version", RinClient.PROTOCOL_VERSION,
                "session_id", sessionId,
                "request_id", "commit." + operationId,
                "proposal_id", proposalId,
                "event_id", "outcome." + operationId,
                "tick", occurrenceTick,
                "accepted", applied.accepted,
                "outcome", applied.outcome,
                "tags", List.of("fabric-example", "conversation"));
        return new PendingOutcome(
                sessionId,
                OutcomeKind.COMMIT,
                commitRequest,
                safeObserveRequest(sessionId, operationId, occurrenceTick, applied));
    }

    private PendingOutcome observePending(
            String sessionId,
            String operationId,
            long occurrenceTick,
            AppliedAction applied) {
        Map<String, Object> observe = safeObserveRequest(
                sessionId, operationId, occurrenceTick, applied);
        return new PendingOutcome(sessionId, OutcomeKind.OBSERVE, observe, observe);
    }

    private static Map<String, Object> safeObserveRequest(
            String sessionId,
            String operationId,
            long occurrenceTick,
            AppliedAction applied) {
        // Degraded reporting is intentionally limited to episodic memory and
        // an absolute fact. It never replays relative goal/progress deltas.
        return mapOf(
                "protocol_version", RinClient.PROTOCOL_VERSION,
                "session_id", sessionId,
                "request_id", "fallback.observe." + operationId,
                // Commit and degraded Observe describe one occurrence, so
                // they deliberately share the same idempotency event ID.
                "event_id", "outcome." + operationId,
                "tick", occurrenceTick,
                "observer_ids", List.of(ACTOR_ID),
                "source", "fabric-example",
                "kind", "action_outcome",
                "summary", applied.outcome,
                "tags", List.of("outcome", "degraded-report"),
                "importance", 3,
                "facts", List.of(mapOf(
                        "subject_id", ACTOR_ID,
                        "predicate", "last_action_outcome",
                        "object", applied.accepted ? "accepted" : "rejected",
                        "visibility", List.of(ACTOR_ID),
                        "confidence", 100)));
    }

    private boolean persistAuthoritativeTransaction(
            SessionRegistration registration,
            ProposalAttempt completedAttempt,
            String operationId,
            AppliedAction result,
            PendingOutcome pending,
            Runnable applyGameState) {
        // PRODUCTION PERSISTENCE HOOK: replace this whole body with one
        // fallible, atomic world/player-data transaction. The actual game
        // mutation, applied marker, complete Commit plus degraded-Observe
        // Outbox entry, session request, runId, sequence, and deletion of the
        // completed Proposal attempt must commit or roll back together. The
        // demo rollback prevents a throwing effect callback from leaving an
        // accepted marker/outbox and retains the attempt, but only the real
        // game save transaction can roll back an already-mutated world.
        synchronized (persistenceLock) {
            if (appliedOperations.containsKey(operationId)) return true;
            if (completedAttempt != null
                    && registration.proposalAttempt != completedAttempt) {
                return false;
            }
            appliedOperations.put(operationId, result);
            outcomeOutbox.put(operationId, pending);
            try {
                applyGameState.run();
                if (completedAttempt != null) {
                    registration.proposalAttempt = null;
                }
                return true;
            } catch (Throwable error) {
                outcomeOutbox.remove(operationId, pending);
                appliedOperations.remove(operationId, result);
                throw error;
            }
        }
    }

    private CompletableFuture<Void> flushOutcomeOutbox(String sessionId) {
        List<String> operationIds = outcomeOutbox.entrySet().stream()
                .filter(entry -> entry.getValue().sessionId.equals(sessionId))
                .map(Map.Entry::getKey)
                .sorted()
                .toList();
        CompletableFuture<Void> retries = CompletableFuture.completedFuture(null);
        for (String operationId : operationIds) {
            retries = retries.thenCompose(ignored -> reportOutcome(operationId));
        }
        return retries;
    }

    private CompletableFuture<Void> reportOutcome(String operationId) {
        PendingOutcome pending = outcomeOutbox.get(operationId);
        if (pending == null) return CompletableFuture.completedFuture(null);
        if (pending.kind == OutcomeKind.OBSERVE) {
            return rin.observe(pending.request)
                    .thenCompose(ignored -> acknowledgeOutcome(operationId, pending));
        }
        return rin.commit(pending.request)
                .handle((ignored, error) -> {
                    if (error == null) return acknowledgeOutcome(operationId, pending);
                    Throwable cause = unwrap(error);
                    String code = safeCode(cause);
                    if (!TERMINAL_COMMIT_ERRORS.contains(code)) {
                        return CompletableFuture.<Void>failedFuture(cause);
                    }
                    PendingOutcome converted = pending.asDegradedObserve();
                    if (!persistOutboxConversion(operationId, pending, converted)) {
                        return CompletableFuture.<Void>failedFuture(
                                new IllegalStateException("outbox conversion was not persisted"));
                    }
                    outcomeOutbox.replace(operationId, pending, converted);
                    if ("session_not_found".equals(code)) {
                        invalidateSession(pending.sessionId);
                        // Re-create from the exact retained Create request on
                        // the next entry, then flush this Observe before a turn.
                        return CompletableFuture.<Void>failedFuture(cause);
                    }
                    return rin.observe(converted.request)
                            .thenCompose(result -> acknowledgeOutcome(operationId, converted));
                })
                .thenCompose(future -> future);
    }

    private boolean persistOutboxConversion(
            String operationId,
            PendingOutcome original,
            PendingOutcome converted) {
        // PRODUCTION PERSISTENCE HOOK: atomically replace the unrecoverable
        // Commit with its pre-recorded safe Observe. Return false on any save
        // failure; the exact original Commit then remains retryable.
        return outcomeOutbox.get(operationId) == original
                && converted.kind == OutcomeKind.OBSERVE;
    }

    private CompletableFuture<Void> acknowledgeOutcome(
            String operationId,
            PendingOutcome acknowledged) {
        // Durable ACK/delete succeeds before the in-memory entry is evicted.
        if (!persistOutboxAcknowledgement(operationId, acknowledged)) {
            return CompletableFuture.failedFuture(
                    new IllegalStateException("outbox acknowledgement was not persisted"));
        }
        outcomeOutbox.remove(operationId, acknowledged);
        return CompletableFuture.completedFuture(null);
    }

    private boolean persistOutboxAcknowledgement(
            String operationId,
            PendingOutcome acknowledged) {
        // PRODUCTION PERSISTENCE HOOK: atomically persist acknowledged Outbox
        // deletion and return false on failure.
        return outcomeOutbox.get(operationId) == acknowledged;
    }

    private boolean hasPendingOutcome(String sessionId) {
        return outcomeOutbox.values().stream()
                .anyMatch(pending -> pending.sessionId.equals(sessionId));
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

    private static long integer(Object value, long fallback) {
        if (!(value instanceof Number number)) return fallback;
        double checked = number.doubleValue();
        if (!Double.isFinite(checked) || checked != Math.rint(checked)) return fallback;
        return number.longValue();
    }

    private static Throwable unwrap(Throwable error) {
        Throwable cause = error;
        while ((cause instanceof CompletionException)
                && cause.getCause() != null) cause = cause.getCause();
        return cause;
    }

    private static String safeCode(Throwable error) {
        Throwable cause = unwrap(error);
        return cause instanceof RinException rinError ? rinError.code() : "integration_failed";
    }

    private enum OutcomeKind { COMMIT, OBSERVE }
    private enum FreshnessDecision { FRESH, STALE, UNAVAILABLE }

    private static final class SessionRegistration {
        private final String sessionId;
        private final Map<String, Object> createRequest;
        private CompletableFuture<Void> createAttempt;
        private ProposalAttempt proposalAttempt;

        private SessionRegistration(String sessionId, Map<String, Object> createRequest) {
            this.sessionId = sessionId;
            this.createRequest = createRequest;
        }
    }

    private static final class ProposalAttempt {
        private final String sessionId;
        private final String operationId;
        private final long sequence;
        private final String requestId;
        private final long proposeTick;
        private final Map<String, Object> observeRequest;
        private final Map<String, Object> proposeRequest;
        private String jobId = "";

        private ProposalAttempt(
                String sessionId,
                String operationId,
                long sequence,
                String requestId,
                long proposeTick,
                Map<String, Object> observeRequest,
                Map<String, Object> proposeRequest) {
            this.sessionId = sessionId;
            this.operationId = operationId;
            this.sequence = sequence;
            this.requestId = requestId;
            this.proposeTick = proposeTick;
            this.observeRequest = observeRequest;
            this.proposeRequest = proposeRequest;
        }
    }

    private static final class ProposalResolution {
        private final ProposalAttempt attempt;
        private final Map<String, Object> proposal;
        private final boolean useAuthoredFallback;
        private final String reason;

        private ProposalResolution(
                ProposalAttempt attempt,
                Map<String, Object> proposal,
                boolean useAuthoredFallback,
                String reason) {
            this.attempt = attempt;
            this.proposal = proposal;
            this.useAuthoredFallback = useAuthoredFallback;
            this.reason = reason;
        }

        private static ProposalResolution fromProposal(
                ProposalAttempt attempt,
                Map<String, Object> proposal) {
            return new ProposalResolution(attempt, proposal, false, "");
        }

        private static ProposalResolution authoredFallback(
                ProposalAttempt attempt,
                String reason) {
            return new ProposalResolution(attempt, Map.of(), true, reason);
        }
    }

    private static final class AppliedAction {
        private final boolean accepted;
        private final String outcome;

        private AppliedAction(boolean accepted, String outcome) {
            this.accepted = accepted;
            this.outcome = outcome;
        }
    }

    private static final class PendingOutcome {
        private final String sessionId;
        private final OutcomeKind kind;
        private final Map<String, Object> request;
        private final Map<String, Object> degradedObserveRequest;

        private PendingOutcome(
                String sessionId,
                OutcomeKind kind,
                Map<String, Object> request,
                Map<String, Object> degradedObserveRequest) {
            this.sessionId = sessionId;
            this.kind = kind;
            this.request = request;
            this.degradedObserveRequest = degradedObserveRequest;
        }

        private PendingOutcome asDegradedObserve() {
            return new PendingOutcome(
                    sessionId,
                    OutcomeKind.OBSERVE,
                    degradedObserveRequest,
                    degradedObserveRequest);
        }
    }
}
