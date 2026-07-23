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
            byte[] body = mode[0].equals("oversized")
                    ? new byte[2048]
                    : "{}".getBytes(StandardCharsets.UTF_8);
            int status = (lastRequest[1].equals("/v1/jobs/propose") || lastRequest[1].equals("/v1/generation/jobs")) ? 202 : 200;
            exchange.sendResponseHeaders(status, body.length);
            exchange.getResponseBody().write(body);
            exchange.close();
        });
        server.start();

        JsonCodec codec = new JsonCodec() {
            public String encode(Map<String, ?> value) { return "{}"; }
            public Map<String, Object> decodeObject(String json) {
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
}
