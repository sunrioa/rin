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
        request.put("decision_window", Map.of("id", "window.workflow"));
        request.put("offers", List.of(Map.of("offer_id", "offer.talk")));
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
        Map<String, Object> report = reportRequest(
                "report.workflow",
                "proposal.workflow",
                "event.workflow");
        AtomicReference<String> operation = new AtomicReference<>("");
        workflow.applyAndEnqueueOutcome(
                pendingTurn,
                proposal,
                report,
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
                    reportRequest(
                            "report.failed",
                            "proposal.workflow",
                            "event.failed"),
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
                    report,
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
        verifyReportRetry();
    }

    private static void verifyReportRetry() throws Exception {
        HttpServer server = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        server.createContext("/", exchange -> {
            String path = exchange.getRequestURI().getPath();
            boolean report = path.equals("/v2/action/report");
            byte[] bytes = (report ? "temporary-report-error" : "unexpected")
                    .getBytes(StandardCharsets.UTF_8);
            exchange.sendResponseHeaders(report ? 503 : 404, bytes.length);
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
                    if (json.equals("temporary-report-error")) {
                        return Map.of(
                                "ok", false,
                                "error", Map.of(
                                        "code", "temporarily_unavailable",
                                        "message", "retry later"));
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
                    "outcome.retry",
                    reportRequest(
                            "report.retry",
                            "proposal.retry",
                            "event.retry")));
            try {
                new WorkflowCoordinator(client, store).drainOutbox().join();
                throw new AssertionError("failed report was acknowledged");
            } catch (java.util.concurrent.CompletionException expected) {
                require(
                        expected.getCause() instanceof RinApiException,
                        "report failure changed error type");
            }
            require(store.outcomes.size() == 1, "failed report was removed from the Outbox");
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
            if (path.equals("/v2/jobs/job.old")) {
                body = "job-not-found";
                status = 404;
            } else if (path.equals("/v2/jobs/propose")) {
                body = "submitted";
                status = 202;
            } else if (path.equals("/v2/jobs/job.new")) {
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

    private static Map<String, Object> reportRequest(
            String requestId,
            String proposalId,
            String eventId) {
        return Map.of(
                "protocol_version", RinClient.PROTOCOL_VERSION,
                "session_id", "session.workflow",
                "request_id", requestId,
                "tick", 7L,
                "report", Map.of(
                        "proposal_id", proposalId,
                        "event_id", eventId,
                        "decision", "rejected",
                        "summary", "host rejected the offer"));
    }

    private static final class TestStore implements WorkflowStore {
        private PendingTurn pendingTurn;
        private final List<OutcomeOutboxEntry> outcomes = new ArrayList<>();

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
                Map<String, Object> report,
                Function<String, CompletionStage<Void>> apply) {
            return apply.apply(value.operationId())
                    .thenRun(() -> complete(value, report));
        }

        public CompletionStage<Void> completePendingTurn(
                PendingTurn value,
                Map<String, Object> proposal,
                Map<String, Object> report) {
            complete(value, report);
            return CompletableFuture.completedFuture(null);
        }

        public CompletionStage<List<OutcomeOutboxEntry>> listOutcomeReports() {
            return CompletableFuture.completedFuture(List.copyOf(outcomes));
        }

        public CompletionStage<Void> acknowledgeOutcome(
                OutcomeOutboxEntry entry,
                Map<String, Object> result) {
            outcomes.remove(entry);
            return CompletableFuture.completedFuture(null);
        }

        private void complete(PendingTurn value, Map<String, Object> report) {
            if (!value.equals(pendingTurn)) {
                throw new IllegalStateException("completion did not match retained Pending Turn");
            }
            outcomes.add(new OutcomeOutboxEntry("outcome.workflow", report));
            pendingTurn = null;
        }
    }
}
