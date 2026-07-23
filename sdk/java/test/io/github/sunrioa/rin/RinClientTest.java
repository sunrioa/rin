package io.github.sunrioa.rin;

import com.sun.net.httpserver.HttpServer;

import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.List;
import java.util.Map;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.CompletionException;
import java.util.function.Supplier;

public final class RinClientTest {
    private record RequestCase(Supplier<CompletableFuture<Map<String, Object>>> call, String method, String path) { }

    public static void main(String[] args) throws Exception {
        String[] lastRequest = new String[3];
        String[] mode = {"normal"};
        HttpServer server = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        server.createContext("/", exchange -> {
            lastRequest[0] = exchange.getRequestMethod();
            lastRequest[1] = exchange.getRequestURI().getPath();
            lastRequest[2] = exchange.getRequestHeaders().getFirst("Authorization");
            if (mode[0].equals("slow")) {
                try {
                    Thread.sleep(200);
                } catch (InterruptedException interrupted) {
                    Thread.currentThread().interrupt();
                }
            }
            byte[] body;
            if (mode[0].equals("oversized")) {
                body = new byte[2048];
            } else if (mode[0].equals("proposal-race")) {
                body = (lastRequest[0].equals("DELETE") ? "proposal-succeeded" : "job-running")
                        .getBytes(StandardCharsets.UTF_8);
            } else if (mode[0].equals("generation-race")) {
                body = (lastRequest[0].equals("DELETE") ? "generation-succeeded" : "generation-running")
                        .getBytes(StandardCharsets.UTF_8);
            } else if (mode[0].equals("terminal-cancel")) {
                body = (lastRequest[0].equals("DELETE") ? "job-stale" : "job-running")
                        .getBytes(StandardCharsets.UTF_8);
            } else if (mode[0].equals("crossed-get")) {
                body = "job-crossed".getBytes(StandardCharsets.UTF_8);
            } else if (mode[0].equals("malformed-delete")) {
                body = (lastRequest[0].equals("DELETE") ? "proposal-malformed" : "job-running")
                        .getBytes(StandardCharsets.UTF_8);
            } else {
                body = "{}".getBytes(StandardCharsets.UTF_8);
            }
            int status = (lastRequest[1].equals("/v1/jobs/propose") || lastRequest[1].equals("/v1/generation/jobs")) ? 202 : 200;
            exchange.sendResponseHeaders(status, body.length);
            exchange.getResponseBody().write(body);
            exchange.close();
        });
        server.start();

        JsonCodec codec = new JsonCodec() {
            public String encode(Map<String, ?> value) { return "{}"; }
            public Map<String, Object> decodeObject(String json) {
                if (json.equals("job-running")) {
                    return Map.of("ok", true, "data", proposalJob("running"));
                }
                if (json.equals("proposal-succeeded")) {
                    return Map.of(
                            "ok", true,
                            "data", proposalJob(
                                    "succeeded",
                                    Map.of(
                                            "id", "proposal.race",
                                            "session_id", "session.fixture",
                                            "request_id", "request.fixture",
                                            "actor_id", "actor.fixture",
                                            "tick", 7L)));
                }
                if (json.equals("generation-running")) {
                    return Map.of("ok", true, "data", generationJob("running"));
                }
                if (json.equals("generation-succeeded")) {
                    return Map.of(
                            "ok", true,
                            "data", generationJob(
                                    "succeeded",
                                    Map.of("content", "finished at the deadline")));
                }
                if (json.equals("job-stale")) {
                    return Map.of(
                            "ok", true,
                            "data", proposalJob(
                                    "stale",
                                    "error",
                                    Map.of("code", "proposal_stale", "message", "World changed")));
                }
                if (json.equals("job-crossed")) {
                    return Map.of("ok", true, "data", proposalJob("running", "job_id", "job.other"));
                }
                if (json.equals("proposal-malformed")) {
                    return Map.of(
                            "ok", true,
                            "data", proposalJob(
                                    "succeeded",
                                    Map.of(
                                            "id", "proposal.race",
                                            "session_id", "session.fixture",
                                            "request_id", "request.fixture",
                                            "actor_id", "actor.fixture",
                                            "tick", Double.valueOf(1.5))));
                }
                return Map.of("ok", true, "data", Map.of("status", "ok"));
            }
        };
        RinClient client = new RinClient(
                "http://127.0.0.1:" + server.getAddress().getPort(),
                "fixture",
                Duration.ofSeconds(2),
                1024 * 1024,
                codec);
        Map<String, Object> payload = Map.of();
        List<RequestCase> cases = List.of(
                new RequestCase(client::health, "GET", "/health"),
                new RequestCase(() -> client.createSession(payload), "POST", "/v1/session/create"),
                new RequestCase(() -> client.observe(payload), "POST", "/v1/session/observe"),
                new RequestCase(() -> client.propose(payload), "POST", "/v1/agent/propose"),
                new RequestCase(() -> client.submitProposalJob(payload), "POST", "/v1/jobs/propose"),
                new RequestCase(() -> client.getProposalJob("job.fixture"), "GET", "/v1/jobs/job.fixture"),
                new RequestCase(() -> client.cancelProposalJob("job.fixture"), "DELETE", "/v1/jobs/job.fixture"),
                new RequestCase(() -> client.submitGenerationJob(payload), "POST", "/v1/generation/jobs"),
                new RequestCase(() -> client.getGenerationJob("job.fixture"), "GET", "/v1/generation/jobs/job.fixture"),
                new RequestCase(() -> client.cancelGenerationJob("job.fixture"), "DELETE", "/v1/generation/jobs/job.fixture"),
                new RequestCase(() -> client.commit(payload), "POST", "/v1/action/commit"),
                new RequestCase(() -> client.commitBatch(payload), "POST", "/v1/action/commit-batch"),
                new RequestCase(() -> client.setActorActivity(payload), "POST", "/v1/session/activity"),
                new RequestCase(() -> client.arbitrate(payload), "POST", "/v1/world/arbitrate"),
                new RequestCase(() -> client.state(payload), "POST", "/v1/session/get"),
                new RequestCase(() -> client.snapshot(payload), "POST", "/v1/session/snapshot"),
                new RequestCase(() -> client.restore(payload), "POST", "/v1/session/restore"),
                new RequestCase(() -> client.timeline(payload), "POST", "/v1/session/timeline"),
                new RequestCase(() -> client.replay(payload), "POST", "/v1/session/replay"),
                new RequestCase(() -> client.dueAgents(payload), "POST", "/v1/scheduler/due")
        );
        try {
            for (RequestCase test : cases) {
                test.call().get().join();
                require(test.method().equals(lastRequest[0]), "wrong method for " + test.path());
                require(test.path().equals(lastRequest[1]), "wrong path for " + test.path());
                require("Bearer fixture".equals(lastRequest[2]), "missing bearer token");
            }
            try {
                client.getProposalJob("\u4f5c\u4e1a");
                throw new AssertionError("Unicode path ID was accepted");
            } catch (RinConfigurationException expected) {
                require("invalid_identifier".equals(expected.code()), "wrong identifier error");
            }

            mode[0] = "oversized";
            RinClient limited = new RinClient(
                    "http://127.0.0.1:" + server.getAddress().getPort(),
                    "",
                    Duration.ofSeconds(2),
                    1024,
                    codec);
            try {
                limited.health().join();
                throw new AssertionError("oversized streamed response was accepted");
            } catch (CompletionException expected) {
                require(rootCause(expected) instanceof RinProtocolException, "wrong response limit error");
            }

            mode[0] = "slow";
            RinClient impatient = new RinClient(
                    "http://127.0.0.1:" + server.getAddress().getPort(),
                    "",
                    Duration.ofMillis(50),
                    1024,
                    codec);
            try {
                impatient.health().join();
                throw new AssertionError("slow response exceeded the request deadline");
            } catch (CompletionException expected) {
                Throwable cause = rootCause(expected);
                require(cause instanceof RinTransportException, "wrong timeout error type");
                require("transport_timeout".equals(((RinTransportException) cause).code()), "wrong timeout error code");
            }
            Thread.sleep(200);

            mode[0] = "proposal-race";
            Map<String, Object> proposalRace = client.waitForProposal(
                    "job.fixture",
                    Duration.ofMillis(50),
                    Duration.ofMillis(10)).join();
            Map<?, ?> proposal = (Map<?, ?>) proposalRace.get("proposal");
            require("proposal.race".equals(proposal.get("id")), "proposal cancellation race result was discarded");

            mode[0] = "generation-race";
            Map<String, Object> generationRace = client.waitForGeneration(
                    "job.fixture",
                    Duration.ofMillis(50),
                    Duration.ofMillis(10)).join();
            Map<?, ?> generationResult = (Map<?, ?>) generationRace.get("result");
            require(
                    "finished at the deadline".equals(generationResult.get("content")),
                    "generation cancellation race result was discarded");

            mode[0] = "terminal-cancel";
            try {
                client.waitForProposal(
                        "job.fixture",
                        Duration.ofMillis(50),
                        Duration.ofMillis(10)).join();
                throw new AssertionError("terminal cancellation result was discarded");
            } catch (CompletionException expected) {
                Throwable cause = rootCause(expected);
                require(cause instanceof RinApiException, "terminal cancellation returned wrong error type");
                require(
                        "proposal_stale".equals(((RinApiException) cause).code()),
                        "terminal cancellation result became job_timeout");
            }

            mode[0] = "crossed-get";
            try {
                client.waitForProposal("job.fixture").join();
                throw new AssertionError("crossed GET job identity was accepted");
            } catch (CompletionException expected) {
                Throwable cause = rootCause(expected);
                require(cause instanceof RinProtocolException, "crossed GET returned wrong error type");
                require("invalid_job".equals(((RinProtocolException) cause).code()), "crossed GET returned wrong error");
            }

            mode[0] = "malformed-delete";
            try {
                client.waitForProposal(
                        "job.fixture",
                        Duration.ofMillis(50),
                        Duration.ofMillis(10)).join();
                throw new AssertionError("malformed DELETE proposal identity was accepted");
            } catch (CompletionException expected) {
                Throwable cause = rootCause(expected);
                require(cause instanceof RinProtocolException, "malformed DELETE returned wrong error type");
                require(
                        "invalid_job".equals(((RinProtocolException) cause).code()),
                        "malformed DELETE returned wrong error");
            }
        } finally {
            server.stop(0);
        }
    }

    private static void require(boolean condition, String message) {
        if (!condition) throw new AssertionError(message);
    }

    private static Throwable rootCause(Throwable error) {
        Throwable result = error;
        while (result instanceof CompletionException && result.getCause() != null) result = result.getCause();
        return result;
    }

    private static Map<String, Object> proposalJob(String status) {
        return proposalJob(status, Map.of());
    }

    private static Map<String, Object> proposalJob(String status, Map<String, Object> proposal) {
        return proposalJob(status, "proposal", proposal);
    }

    private static Map<String, Object> proposalJob(String status, String key, Object value) {
        Map<String, Object> result = new java.util.LinkedHashMap<>();
        result.put("job_id", "job.fixture");
        result.put("session_id", "session.fixture");
        result.put("request_id", "request.fixture");
        result.put("status", status);
        if (value instanceof Map<?, ?> map && !map.isEmpty()) result.put(key, value);
        else if (!(value instanceof Map<?, ?>)) result.put(key, value);
        return result;
    }

    private static Map<String, Object> generationJob(String status, Map<String, Object> generationResult) {
        Map<String, Object> result = new java.util.LinkedHashMap<>();
        result.put("job_id", "job.fixture");
        result.put("request_id", "generation.fixture");
        result.put("status", status);
        if (!generationResult.isEmpty()) result.put("result", generationResult);
        return result;
    }

    private static Map<String, Object> generationJob(String status) {
        return generationJob(status, Map.of());
    }
}
