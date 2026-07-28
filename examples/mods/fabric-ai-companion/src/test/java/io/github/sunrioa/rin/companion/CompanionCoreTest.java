package io.github.sunrioa.rin.companion;

import io.github.sunrioa.rin.OutcomeOutboxEntry;
import io.github.sunrioa.rin.PendingTurn;

import java.nio.file.Files;
import java.nio.file.Path;
import java.util.List;
import java.util.Map;
import java.util.UUID;
import java.util.Set;
import java.util.stream.Collectors;

public final class CompanionCoreTest {
    private CompanionCoreTest() {
    }

    public static void main(String[] args) throws Exception {
        require(CompanionChat.parse("@伙伴 你好").orElseThrow().equals("你好"),
                "Chinese companion chat was not parsed");
        require(CompanionChat.parse("普通聊天").isEmpty(),
                "ordinary chat was intercepted");
        require(CompanionChat.parse("@伙伴   ").isEmpty(),
                "empty companion chat was accepted");

        CompanionModelConfig defaults = CompanionModelConfig.defaults();
        require(defaults.baseUrl().toString().equals("https://api.deepseek.com/v1"),
                "unexpected DeepSeek default");
        require(defaults.model().equals("deepseek-chat"), "unexpected model default");
        requireThrows(() -> CompanionModelConfig.create("http://example.com/v1", "x"));
        requireThrows(() -> CompanionModelConfig.create("https://user@example.com/v1", "x"));
        requireThrows(() -> CompanionModelConfig.create("https://example.com/v1?q=x", "x"));
        requireThrows(() -> CompanionModelConfig.create("https://example.com/v1", "bad model"));

        Path configPath = Files.createTempDirectory("rin-companion-test")
                .resolve("rin-ai-companion.properties");
        CompanionConfigStore configStore = new CompanionConfigStore(configPath);
        configStore.save(CompanionModelConfig.create(
                "https://api.deepseek.com/v1", "deepseek-chat"));
        require(configStore.load().equals(CompanionModelConfig.defaults()),
                "model config did not round-trip");

        UUID worldId = UUID.fromString("00000000-0000-0000-0000-000000000001");
        UUID ownerId = UUID.fromString("00000000-0000-0000-0000-000000000002");
        UUID companionId = UUID.fromString("00000000-0000-0000-0000-000000000003");
        CompanionSavedState state = new CompanionSavedState(worldId);
        state.hostGeneration = 4;
        state.timelineGeneration = 2;
        state.sequence = 7;
        CompanionSessionState session = CompanionSessionState.create(worldId, ownerId, companionId,
                "伙伴", "player", "FOLLOW", Map.of("request_id", "create.1"));
        session.pendingObserve = Map.of("request_id", "observe.7", "distance", 3L);
        session.pendingTurn = PendingTurn.create("turn.7", Map.of(
                "request_id", "propose.7", "session_id", "session.7", "intent", "talk"));
        OutcomeOutboxEntry outcome = new OutcomeOutboxEntry("outcome.7", Map.of("decision", "accepted"));
        session.outcomes.put(outcome.key(), outcome);
        state.sessions.put(session.sessionId, session);
        CompanionSavedState restored = CompanionSavedState.fromJson(state.toJson());
        CompanionSessionState restoredSession = restored.sessions.get(session.sessionId);
        require(restored.worldId.equals(worldId), "world identity changed");
        require(restoredSession != null && restoredSession.ownerId.equals(ownerId), "session identity changed");
        require(restoredSession.pendingTurn.equals(session.pendingTurn), "pending turn changed");
        require(restoredSession.pendingObserve.equals(session.pendingObserve), "pending observe changed");
        require(restoredSession.outcomes.get(outcome.key()).equals(outcome), "outbox changed");
        require(CompanionSessionState.stableSessionId(worldId, ownerId, companionId)
                        .equals(session.sessionId), "stable session id changed");
        require(session.sessionId.length() <= 96, "stable session id exceeds Rin protocol limits");
        requireRejected(() -> CompanionSavedState.fromJson("{}"));
        requireRejected(() -> CompanionSavedState.fromJson("{\"version\":1,\"world_id\":\"bad\",\"host_generation\":1,\"timeline_generation\":1,\"sequence\":0,\"sessions\":{}}"));
        requireRejected(() -> CompanionSavedState.fromJson(state.toJson().replace("\"sessions\":{", "\"unknown\":1,\"sessions\":{")));
        String encodedSession = new GsonJsonCodec().encode(session.toJson());
        String duplicateSession = "{\"version\":1,\"world_id\":\"" + worldId +
                "\",\"host_generation\":4,\"timeline_generation\":2,\"sequence\":7,\"sessions\":{" +
                "\"" + session.sessionId + "\":" + encodedSession + ",\"" + session.sessionId +
                "\":" + encodedSession + "}}";
        requireRejected(() -> CompanionSavedState.fromJson(duplicateSession));
        CompanionSavedState tooMany = new CompanionSavedState(worldId);
        for (int i = 0; i < CompanionSavedState.MAX_SESSIONS + 1; i++) {
            UUID id = new UUID(0, i + 10L);
            CompanionSessionState extra = CompanionSessionState.create(worldId, ownerId, id,
                    "伙伴", "", "STOPPED", Map.of("request_id", "create." + i));
            tooMany.sessions.put(extra.sessionId, extra);
        }
        requireRejected(tooMany::toJson);
        CompanionSavedState tooManyOutcomes = new CompanionSavedState(worldId);
        CompanionSessionState crowded = CompanionSessionState.create(worldId, ownerId, companionId,
                "伙伴", "", "FOLLOW", Map.of("request_id", "create.crowded"));
        for (int i = 0; i < CompanionSessionState.MAX_OUTCOMES + 1; i++) {
            OutcomeOutboxEntry extra = new OutcomeOutboxEntry("outcome." + i, Map.of("index", i));
            crowded.outcomes.put(extra.key(), extra);
        }
        tooManyOutcomes.sessions.put(crowded.sessionId, crowded);
        requireRejected(tooManyOutcomes::toJson);
        requireRejected(() -> CompanionSavedState.fromJson(" ".repeat(CompanionSavedState.MAX_JSON_CHARS + 1)));
        requireRejected(() -> CompanionSavedState.fromJson("[1,2,3]"));

        CompanionHostEpoch epoch = new CompanionHostEpoch(worldId, 4, 2);
        List<Map<String, Object>> offers = CompanionRequests.offers(epoch.wire(session.sessionId), 7);
        Set<String> capabilities = offers.stream()
                .map(offer -> (Map<?, ?>) offer.get("capability"))
                .map(capability -> (String) capability.get("id"))
                .collect(Collectors.toSet());
        require(capabilities.equals(Set.of("dialogue.reply", "movement.follow_owner", "movement.stop",
                "activity.wait", "safety.refuse")), "unexpected Phase 1 offers");
        requireRejected(() -> CompanionActions.requireAllowed("world.break_block"));

        CompanionSessionState workflowSession = CompanionSessionState.create(worldId, ownerId,
                UUID.fromString("00000000-0000-0000-0000-000000000004"), "伙伴", "", "STOPPED",
                Map.of("request_id", "create.workflow"));
        CompanionWorkflowStore workflowStore = new CompanionWorkflowStore(state, workflowSession);
        PendingTurn workflowTurn = PendingTurn.create("operation.8", Map.of(
                "request_id", "propose.8", "session_id", workflowSession.sessionId));
        require(workflowStore.createPendingTurn(workflowTurn).toCompletableFuture().join(),
                "pending turn was not created");
        require(!workflowStore.createPendingTurn(workflowTurn).toCompletableFuture().join(),
                "second pending turn was accepted");
        PendingTurn assigned = workflowTurn.withJobId("job.8");
        workflowStore.savePendingTurn(assigned).toCompletableFuture().join();
        workflowStore.completePendingTurn(assigned, Map.of("id", "proposal.8"),
                Map.of("session_id", workflowSession.sessionId, "request_id", "report.8"))
                .toCompletableFuture().join();
        OutcomeOutboxEntry pendingOutcome = workflowStore.listOutcomeReports()
                .toCompletableFuture().join().getFirst();
        requireRejected(() -> workflowStore.acknowledgeOutcome(pendingOutcome,
                Map.of("session_id", "wrong.session")).toCompletableFuture().join());
        require(workflowStore.listOutcomeReports().toCompletableFuture().join().size() == 1,
                "invalid acknowledgement removed the outbox entry");
        workflowStore.acknowledgeOutcome(pendingOutcome,
                Map.of("session_id", workflowSession.sessionId)).toCompletableFuture().join();
        require(workflowStore.listOutcomeReports().toCompletableFuture().join().isEmpty(),
                "valid acknowledgement retained the outbox entry");

        FakeSidecarFactory sidecarFactory = new FakeSidecarFactory();
        ManagedRinSidecar sidecar = new ManagedRinSidecar(Path.of("rin.exe"), Path.of("rin-data"), 7374,
                CompanionModelConfig.defaults(), key -> "RIN_MODEL_API_KEY".equals(key) ? "secret" : null,
                sidecarFactory, (uri, token) -> true);
        sidecar.start();
        require(sidecarFactory.starts == 1, "sidecar was not started");
        require(sidecarFactory.environment.get("RIN_MODEL").equals("deepseek-chat"),
                "model was not passed to the sidecar");
        require(sidecarFactory.environment.get("RIN_MODEL_API_KEY").equals("secret"),
                "API key was not inherited by the child");
        sidecar.applyConfig(CompanionModelConfig.create("https://example.com/v1", "deepseek-reasoner"));
        require(sidecarFactory.starts == 2 && sidecarFactory.stops == 1,
                "config apply did not restart the owned sidecar");
        sidecar.close();
        sidecar.close();
        require(sidecarFactory.stops == 2, "sidecar shutdown was not idempotent");

        FakeSidecarFactory failedFactory = new FakeSidecarFactory();
        ManagedRinSidecar failedSidecar = new ManagedRinSidecar(Path.of("rin.exe"), Path.of("rin-data"), 7374,
                CompanionModelConfig.defaults(), key -> null, failedFactory, (uri, token) -> false);
        requireRejected(failedSidecar::start);
        require(failedFactory.stops == 1, "failed readiness left the child running");

        require(CompanionDialogue.parse("{\"line\":\"我们沿着河边走。\"}").equals("我们沿着河边走。"),
                "valid Chinese dialogue was rejected");
        requireRejected(() -> CompanionDialogue.parse("[]"));
        requireRejected(() -> CompanionDialogue.parse("{}"));
        requireRejected(() -> CompanionDialogue.parse("{\"line\":1}"));
        requireRejected(() -> CompanionDialogue.parse("{\"line\":\"\"}"));
        requireRejected(() -> CompanionDialogue.parse("{\"line\":\"a\\u0000b\"}"));
        requireRejected(() -> CompanionDialogue.parse("{\"line\":\"" + "好".repeat(301) + "\"}"));
        requireRejected(() -> CompanionDialogue.parse("{\"line\":\"好\",\"extra\":true}"));
        require(CompanionDialogue.fallback("movement.follow_owner").equals("好，我跟着你。"),
                "dialogue fallback changed");
    }

    private static void require(boolean condition, String message) {
        if (!condition) {
            throw new AssertionError(message);
        }
    }

    private static void requireThrows(ThrowingRunnable action) throws Exception {
        try {
            action.run();
        } catch (IllegalArgumentException expected) {
            return;
        }
        throw new AssertionError("expected IllegalArgumentException");
    }

    private static void requireRejected(ThrowingRunnable action) throws Exception {
        try {
            action.run();
        } catch (RuntimeException expected) {
            return;
        }
        throw new AssertionError("expected malformed state rejection");
    }

    @FunctionalInterface
    private interface ThrowingRunnable {
        void run() throws Exception;
    }

    private static final class FakeSidecarFactory implements ManagedRinSidecar.ProcessFactory {
        int starts;
        int stops;
        Map<String, String> environment = Map.of();

        @Override
        public ManagedRinSidecar.SidecarProcess start(List<String> command, Map<String, String> environment,
                                                       Path workingDirectory) {
            starts++;
            this.environment = Map.copyOf(environment);
            return () -> stops++;
        }
    }
}
