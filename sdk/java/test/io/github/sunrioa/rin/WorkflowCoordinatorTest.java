package io.github.sunrioa.rin;

import com.sun.net.httpserver.HttpServer;

import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.CompletionStage;
import java.util.concurrent.atomic.AtomicReference;
import java.util.function.Function;

final class WorkflowCoordinatorTest {
    private WorkflowCoordinatorTest() { }

    static void run() throws Exception {
        try {
            new HostDurability(
                    1,
                    HostDurabilityProfile.TRANSACTIONAL_ACTION,
                    false,
                    false,
                    false,
                    false,
                    false);
            throw new AssertionError("inflated transactional durability was accepted");
        } catch (RinConfigurationException expected) {
            require(
                    expected.code().equals("invalid_host_durability"),
                    "invalid durability returned the wrong code");
        }

        JsonCodec unusedCodec = new JsonCodec() {
            public String encode(Map<String, ?> value) {
                return "{}";
            }

            public Map<String, Object> decodeObject(String json) {
                return Map.of("ok", true, "data", Map.of());
            }
        };
        RinClient client = new RinClient(unusedCodec);
        TestStore store = new TestStore();
        WorkflowCoordinator workflow = new WorkflowCoordinator(
                client,
                store,
                HostDurability.idempotentAction());
        Map<String, Object> request = new LinkedHashMap<>();
        request.put("protocol_version", RinClient.PROTOCOL_VERSION);
        request.put("session_id", "session.workflow");
        request.put("request_id", "request.workflow");
        request.put("actor_id", "actor.workflow");
        request.put("intent", "Talk");
        request.put("candidate_actions", List.of(
                Map.of("id", "talk", "kind", "dialogue", "description", "Talk")));
        PendingTurn pendingTurn = workflow.begin("operation.workflow", request).join();
        request.put("intent", "mutated after persistence");
        require(
                pendingTurn.request().get("intent").equals("Talk"),
                "Pending Turn did not retain a defensive request copy");

        Map<String, Object> proposal = Map.of(
                "id", "proposal.workflow",
                "session_id", "session.workflow",
                "request_id", "request.workflow",
                "actor_id", "actor.workflow",
                "tick", 7L);
        Map<String, Object> commit = Map.of(
                "protocol_version", RinClient.PROTOCOL_VERSION,
                "session_id", "session.workflow",
                "request_id", "commit.workflow",
                "proposal_id", "proposal.workflow",
                "event_id", "event.workflow",
                "accepted", true);
        AtomicReference<String> operation = new AtomicReference<>("");
        workflow.applyAndEnqueueOutcome(
                pendingTurn,
                proposal,
                commit,
                HostDurabilityProfile.IDEMPOTENT_ACTION,
                operationId -> {
                    operation.set(operationId);
                    return CompletableFuture.completedFuture(null);
                }).join();
        require(
                operation.get().equals("operation.workflow"),
                "idempotent apply did not receive the stable operation ID");
        require(store.pendingTurn == null, "completed Pending Turn was retained");
        require(store.outcomes.size() == 1, "Outcome was not enqueued");
        store.outcomes.clear();

        request.put("request_id", "request.failed");
        PendingTurn failedTurn = workflow.begin("operation.failed", request).join();
        try {
            workflow.applyAndEnqueueOutcome(
                    failedTurn,
                    Map.of(
                            "id", "proposal.workflow",
                            "session_id", "session.workflow",
                            "request_id", "request.failed"),
                    Map.of(
                            "protocol_version", RinClient.PROTOCOL_VERSION,
                            "session_id", "session.workflow",
                            "request_id", "commit.failed",
                            "proposal_id", "proposal.workflow",
                            "event_id", "event.failed",
                            "accepted", true),
                    HostDurabilityProfile.IDEMPOTENT_ACTION,
                    ignored -> CompletableFuture.failedFuture(
                            new IllegalStateException("game save failed")))
                    .join();
            throw new AssertionError("failed apply was accepted");
        } catch (java.util.concurrent.CompletionException expected) {
            require(
                    expected.getCause() instanceof IllegalStateException &&
                            expected.getCause().getMessage().equals("game save failed"),
                    "failed apply changed error");
        }
        require(
                store.pendingTurn != null &&
                        store.pendingTurn.operationId().equals("operation.failed"),
                "failed apply removed the Pending Turn");
        require(store.outcomes.isEmpty(), "failed apply enqueued an Outcome");

        TestStore advisoryStore = new TestStore();
        WorkflowCoordinator advisory = new WorkflowCoordinator(client, advisoryStore);
        PendingTurn advisoryTurn = advisory.begin("operation.advisory", request).join();
        try {
            advisory.applyAndEnqueueOutcome(
                    advisoryTurn,
                    Map.of(
                            "id", "proposal.workflow",
                            "session_id", "session.workflow",
                            "request_id", "request.workflow"),
                    commit,
                    HostDurabilityProfile.IDEMPOTENT_ACTION,
                    ignored -> CompletableFuture.completedFuture(null))
                    .join();
            throw new AssertionError("advisory host accepted an idempotent action");
        } catch (java.util.concurrent.CompletionException expected) {
            require(
                    expected.getCause() instanceof RinConfigurationException &&
                            ((RinConfigurationException) expected.getCause()).code()
                                    .equals("host_durability_insufficient"),
                    "insufficient durability returned the wrong code");
        }
        require(
                advisoryStore.pendingTurn != null,
                "durability rejection removed the Pending Turn");

        require(
                ProposalFreshness.evaluate(
                        Map.of(
                                "revision", 8L,
                                "proposals", Map.of(
                                        "proposal.workflow",
                                        Map.of("status", "pending"))),
                        Map.of(
                                "id", "proposal.workflow",
                                "created_revision", 8L))
                        == ProposalFreshness.Decision.FRESH,
                "fresh non-world Proposal was rejected");
        require(
                ProposalFreshness.evaluate(
                        Map.of(
                                "world_revision", 9L,
                                "proposals", Map.of(
                                        "proposal.workflow",
                                        Map.of("status", "pending"))),
                        Map.of(
                                "id", "proposal.workflow",
                                "based_on_world_revision", 8L))
                        == ProposalFreshness.Decision.STALE,
                "stale world Proposal was accepted");

        verifyEvictedJobRecovery(request);
        verifyTerminalCommitFallback();
    }

    private static void verifyTerminalCommitFallback() throws Exception {
        HttpServer server = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        int[] observes = new int[1];
        server.createContext("/", exchange -> {
            String path = exchange.getRequestURI().getPath();
            boolean commit = path.equals("/v1/action/commit");
            if (path.equals("/v1/session/observe")) observes[0]++;
            byte[] bytes = (commit ? "terminal-commit" : "observed")
                    .getBytes(StandardCharsets.UTF_8);
            exchange.sendResponseHeaders(commit ? 409 : 200, bytes.length);
            exchange.getResponseBody().write(bytes);
            exchange.close();
        });
        server.start();
        try {
            JsonCodec codec = new JsonCodec() {
                public String encode(Map<String, ?> value) {
                    return "{}";
                }

                public Map<String, Object> decodeObject(String json) {
                    if (json.equals("terminal-commit")) {
                        return Map.of(
                                "ok", false,
                                "error", Map.of(
                                        "code", "unknown_proposal",
                                        "message", "evicted"));
                    }
                    return Map.of("ok", true, "data", Map.of("duplicate", false));
                }
            };
            RinClient client = new RinClient(
                    "http://127.0.0.1:" + server.getAddress().getPort(),
                    "",
                    Duration.ofSeconds(2),
                    1024 * 1024,
                    codec);
            TestStore store = new TestStore();
            store.outcomes.add(new OutcomeOutboxEntry(
                    "outcome.fallback",
                    Map.of(
                            "request_id", "commit.fallback",
                            "event_id", "event.fallback"),
                    Map.of(
                            "request_id", "observe.fallback",
                            "event_id", "event.fallback")));
            int drained = new WorkflowCoordinator(client, store).drainOutbox().join();
            require(drained == 1, "terminal fallback did not count the drained entry");
            require(observes[0] == 1, "terminal Commit did not report its safe Observe");
            require(store.fallbackConversions == 1, "fallback conversion was not persisted");
            require(store.outcomes.isEmpty(), "fallback Observe was not acknowledged");
        } finally {
            server.stop(0);
        }
    }

    private static void verifyEvictedJobRecovery(Map<String, Object> sourceRequest)
            throws Exception {
        HttpServer server = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        server.createContext("/", exchange -> {
            String path = exchange.getRequestURI().getPath();
            String body;
            int status;
            if (path.equals("/v1/jobs/job.old")) {
                body = "job-not-found";
                status = 404;
            } else if (path.equals("/v1/jobs/propose")) {
                body = "submitted";
                status = 202;
            } else if (path.equals("/v1/jobs/job.new")) {
                body = "succeeded";
                status = 200;
            } else {
                body = "unexpected";
                status = 500;
            }
            byte[] bytes = body.getBytes(StandardCharsets.UTF_8);
            exchange.sendResponseHeaders(status, bytes.length);
            exchange.getResponseBody().write(bytes);
            exchange.close();
        });
        server.start();
        try {
            JsonCodec codec = new JsonCodec() {
                public String encode(Map<String, ?> value) {
                    return "{}";
                }

                public Map<String, Object> decodeObject(String json) {
                    if (json.equals("job-not-found")) {
                        return Map.of(
                                "ok", false,
                                "error", Map.of(
                                        "code", "job_not_found",
                                        "message", "evicted"));
                    }
                    if (json.equals("submitted")) {
                        return Map.of(
                                "ok", true,
                                "data", Map.of(
                                        "job_id", "job.new",
                                        "status", "queued",
                                        "duplicate", true));
                    }
                    if (json.equals("succeeded")) {
                        return Map.of(
                                "ok", true,
                                "data", Map.of(
                                        "job_id", "job.new",
                                        "session_id", "session.workflow",
                                        "request_id", "request.recovery",
                                        "status", "succeeded",
                                        "duplicate", true,
                                        "proposal", Map.of(
                                                "id", "proposal.recovery",
                                                "session_id", "session.workflow",
                                                "request_id", "request.recovery",
                                                "actor_id", "actor.workflow",
                                                "tick", 9L)));
                    }
                    return Map.of(
                            "ok", false,
                            "error", Map.of("code", "unexpected", "message", "unexpected"));
                }
            };
            RinClient client = new RinClient(
                    "http://127.0.0.1:" + server.getAddress().getPort(),
                    "",
                    Duration.ofSeconds(2),
                    1024 * 1024,
                    codec);
            Map<String, Object> request = new LinkedHashMap<>(sourceRequest);
            request.put("request_id", "request.recovery");
            TestStore store = new TestStore();
            store.pendingTurn = PendingTurn.create("operation.recovery", request)
                    .withJobId("job.old");
            WorkflowCoordinator workflow = new WorkflowCoordinator(client, store);
            ResolvedPendingTurn recovered = workflow.resumePendingWork(
                    Duration.ofSeconds(1),
                    Duration.ofMillis(10)).join();
            require(
                    recovered.pendingTurn().jobId().equals("job.new"),
                    "evicted Job recovery did not persist the replacement Job ID");
            require(recovered.duplicate(), "reconstructed Proposal lost duplicate status");
            require(
                    store.pendingTurn.jobId().equals("job.new"),
                    "replacement Job ID was not retained by the Store");
        } finally {
            server.stop(0);
        }
    }

    private static void require(boolean condition, String message) {
        if (!condition) throw new AssertionError(message);
    }

    private static final class TestStore implements WorkflowStore {
        private PendingTurn pendingTurn;
        private final List<OutcomeOutboxEntry> outcomes = new ArrayList<>();
        private int fallbackConversions;

        public CompletionStage<PendingTurn> loadPendingTurn() {
            return CompletableFuture.completedFuture(pendingTurn);
        }

        public CompletionStage<Boolean> createPendingTurn(PendingTurn value) {
            if (pendingTurn != null) return CompletableFuture.completedFuture(false);
            pendingTurn = value;
            return CompletableFuture.completedFuture(true);
        }

        public CompletionStage<Void> savePendingTurn(PendingTurn value) {
            pendingTurn = value;
            return CompletableFuture.completedFuture(null);
        }

        public CompletionStage<Void> settleTransactional(
                PendingTurn value,
                Map<String, Object> proposal,
                Map<String, Object> commit,
                Function<String, CompletionStage<Void>> apply) {
            return apply.apply(value.operationId())
                    .thenRun(() -> complete(value, commit));
        }

        public CompletionStage<Void> completePendingTurn(
                PendingTurn value,
                Map<String, Object> proposal,
                Map<String, Object> commit) {
            complete(value, commit);
            return CompletableFuture.completedFuture(null);
        }

        public CompletionStage<List<OutcomeOutboxEntry>> listOutcomeReports() {
            return CompletableFuture.completedFuture(List.copyOf(outcomes));
        }

        public CompletionStage<OutcomeOutboxEntry> replaceOutcomeWithFallback(
                OutcomeOutboxEntry entry) {
            int index = outcomes.indexOf(entry);
            if (index < 0) {
                return CompletableFuture.failedFuture(
                        new IllegalStateException("fallback entry changed"));
            }
            OutcomeOutboxEntry converted = entry.asDegradedObserve();
            outcomes.set(index, converted);
            fallbackConversions++;
            return CompletableFuture.completedFuture(converted);
        }

        public CompletionStage<Void> acknowledgeOutcome(
                OutcomeOutboxEntry entry,
                Map<String, Object> result) {
            outcomes.remove(entry);
            return CompletableFuture.completedFuture(null);
        }

        private void complete(PendingTurn value, Map<String, Object> commit) {
            if (!value.equals(pendingTurn)) {
                throw new IllegalStateException("completion did not match retained Pending Turn");
            }
            outcomes.add(new OutcomeOutboxEntry("outcome.workflow", commit));
            pendingTurn = null;
        }
    }
}
