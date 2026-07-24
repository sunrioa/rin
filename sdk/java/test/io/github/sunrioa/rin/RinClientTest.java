package io.github.sunrioa.rin;

import com.sun.net.httpserver.HttpServer;

import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Duration;
import java.util.ArrayList;
import java.util.Collections;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.CompletionException;
import java.util.function.Supplier;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

public final class RinClientTest {
    private record RequestCase(
            String name,
            Supplier<CompletableFuture<Map<String, Object>>> call,
            String method,
            String path) { }

    public static void main(String[] args) throws Exception {
        require(
                RinClient.DEFAULT_MAX_RESPONSE_BYTES == 32 * 1024 * 1024,
                "default response limit does not match the inline transport budget");
        require("0.6.0".equals(RinClient.VERSION), "client version projection is stale");
        String[] lastRequest = new String[5];
        int[] lastResponseStatus = new int[1];
        int[] transportCalls = new int[1];
        int[] codecEncodeCalls = new int[1];
        String[] mode = {"normal"};
        HttpServer server = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        server.createContext("/", exchange -> {
            transportCalls[0]++;
            lastRequest[0] = exchange.getRequestMethod();
            lastRequest[1] = exchange.getRequestURI().getPath();
            lastRequest[2] = exchange.getRequestHeaders().getFirst("Authorization");
            lastRequest[3] = exchange.getRequestHeaders().getFirst("User-Agent");
            lastRequest[4] = new String(exchange.getRequestBody().readAllBytes(), StandardCharsets.UTF_8);
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
            } else if (mode[0].equals("malformed-double-delete")) {
                body = (lastRequest[0].equals("DELETE") ? "proposal-malformed-double" : "job-running")
                        .getBytes(StandardCharsets.UTF_8);
            } else if (mode[0].equals("malformed-float-delete")) {
                body = (lastRequest[0].equals("DELETE") ? "proposal-malformed-float" : "job-running")
                        .getBytes(StandardCharsets.UTF_8);
            } else if (mode[0].equals("api-error")) {
                body = "api-error".getBytes(StandardCharsets.UTF_8);
            } else {
                body = "{}".getBytes(StandardCharsets.UTF_8);
            }
            int status = mode[0].equals("api-error")
                    ? 400
                    : (lastRequest[1].equals("/v1/jobs/propose")
                            || lastRequest[1].equals("/v1/generation/jobs"))
                            ? 202
                            : 200;
            lastResponseStatus[0] = status;
            exchange.sendResponseHeaders(status, body.length);
            exchange.getResponseBody().write(body);
            exchange.close();
        });
        server.start();

        JsonCodec codec = new JsonCodec() {
            public String encode(Map<String, ?> value) {
                codecEncodeCalls[0]++;
                require(
                        RinClient.PROTOCOL_VERSION.equals(value.get("protocol_version")),
                        "codec did not receive protocol_version");
                require("request.fixture".equals(value.get("request_id")), "codec did not receive request_id");
                require("雨".equals(value.get("utf8")), "codec did not receive UTF-8 text");
                if (value.containsKey("accepted")) {
                    require(Boolean.FALSE.equals(value.get("accepted")), "commit accepted=false changed before codec");
                    return "{\"protocol_version\":\"" + RinClient.PROTOCOL_VERSION
                            + "\",\"request_id\":\"request.fixture\",\"utf8\":\"雨\",\"accepted\":false}";
                }
                if (value.containsKey("items")) {
                    Object itemsValue = value.get("items");
                    require(itemsValue instanceof List<?>, "batch items did not reach codec");
                    List<?> items = (List<?>) itemsValue;
                    require(items.size() == 1 && items.get(0) instanceof Map<?, ?>, "batch item changed before codec");
                    require(
                            Boolean.FALSE.equals(((Map<?, ?>) items.get(0)).get("accepted")),
                            "batch accepted=false changed before codec");
                    return "{\"protocol_version\":\"" + RinClient.PROTOCOL_VERSION
                            + "\",\"request_id\":\"request.fixture\",\"utf8\":\"雨\","
                            + "\"items\":[{\"accepted\":false}]}";
                }
                return "{\"protocol_version\":\"" + RinClient.PROTOCOL_VERSION
                        + "\",\"request_id\":\"request.fixture\",\"utf8\":\"雨\"}";
            }
            public Map<String, Object> decodeObject(String json) {
                if (json.equals("api-error")) {
                    return Map.of(
                            "ok", false,
                            "error", Map.of(
                                    "code", "invalid_request",
                                    "message", "safe",
                                    "field", "actor_id"));
                }
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
                                            "tick", Long.valueOf(9_007_199_254_740_992L))));
                }
                if (json.equals("proposal-malformed-double")) {
                    return Map.of(
                            "ok", true,
                            "data", proposalJob(
                                    "succeeded",
                                    Map.of(
                                            "id", "proposal.race",
                                            "session_id", "session.fixture",
                                            "request_id", "request.fixture",
                                            "actor_id", "actor.fixture",
                                            "tick", Double.valueOf(9_007_199_254_740_992d))));
                }
                if (json.equals("proposal-malformed-float")) {
                    return Map.of(
                            "ok", true,
                            "data", proposalJob(
                                    "succeeded",
                                    Map.of(
                                            "id", "proposal.race",
                                            "session_id", "session.fixture",
                                            "request_id", "request.fixture",
                                            "actor_id", "actor.fixture",
                                            "tick", Float.valueOf(16_777_216f))));
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
        Map<String, Object> payload = Map.of(
                "protocol_version", RinClient.PROTOCOL_VERSION,
                "request_id", "request.fixture",
                "utf8", "雨");
        List<RequestCase> cases = List.of(
                new RequestCase("health", client::health, "GET", "/health"),
                new RequestCase("create_session", () -> client.createSession(payload), "POST", "/v1/session/create"),
                new RequestCase("observe", () -> client.observe(payload), "POST", "/v1/session/observe"),
                new RequestCase("propose", () -> client.propose(payload), "POST", "/v1/agent/propose"),
                new RequestCase("submit_proposal_job", () -> client.submitProposalJob(payload), "POST", "/v1/jobs/propose"),
                new RequestCase("get_proposal_job", () -> client.getProposalJob("job.fixture"), "GET", "/v1/jobs/job.fixture"),
                new RequestCase("cancel_proposal_job", () -> client.cancelProposalJob("job.fixture"), "DELETE", "/v1/jobs/job.fixture"),
                new RequestCase("submit_generation_job", () -> client.submitGenerationJob(payload), "POST", "/v1/generation/jobs"),
                new RequestCase("get_generation_job", () -> client.getGenerationJob("job.fixture"), "GET", "/v1/generation/jobs/job.fixture"),
                new RequestCase("cancel_generation_job", () -> client.cancelGenerationJob("job.fixture"), "DELETE", "/v1/generation/jobs/job.fixture"),
                new RequestCase("commit", () -> client.commit(payload), "POST", "/v1/action/commit"),
                new RequestCase("commit_batch", () -> client.commitBatch(payload), "POST", "/v1/action/commit-batch"),
                new RequestCase("set_actor_activity", () -> client.setActorActivity(payload), "POST", "/v1/session/activity"),
                new RequestCase("arbitrate", () -> client.arbitrate(payload), "POST", "/v1/world/arbitrate"),
                new RequestCase("state", () -> client.state(payload), "POST", "/v1/session/get"),
                new RequestCase("snapshot", () -> client.snapshot(payload), "POST", "/v1/session/snapshot"),
                new RequestCase("restore", () -> client.restore(payload), "POST", "/v1/session/restore"),
                new RequestCase("timeline", () -> client.timeline(payload), "POST", "/v1/session/timeline"),
                new RequestCase("replay", () -> client.replay(payload), "POST", "/v1/session/replay"),
                new RequestCase("due_agents", () -> client.dueAgents(payload), "POST", "/v1/scheduler/due")
        );
        List<String> observedRoutes = new ArrayList<>();
        try {
            for (RequestCase test : cases) {
                Map<String, Object> result = test.call().get().join();
                require(test.method().equals(lastRequest[0]), "wrong method for " + test.path());
                require(test.path().equals(lastRequest[1]), "wrong path for " + test.path());
                require("Bearer fixture".equals(lastRequest[2]), "missing bearer token");
                require("ok".equals(result.get("status")), "response envelope was not decoded");
                require(
                        ("rin-java/" + RinClient.VERSION).equals(lastRequest[3]),
                        "wrong user agent");
                require(
                        (test.method().equals("POST")
                                ? "{\"protocol_version\":\"" + RinClient.PROTOCOL_VERSION
                                        + "\",\"request_id\":\"request.fixture\",\"utf8\":\"雨\"}"
                                : "")
                                .equals(lastRequest[4]),
                        "request body changed for " + test.path());
                observedRoutes.add(routeKey(
                        test.name(),
                        lastRequest[0],
                        lastRequest[1].replace("job.fixture", "{job_id}"),
                        lastResponseStatus[0]));
            }
            Collections.sort(observedRoutes);
            require(
                    observedRoutes.equals(contractRouteKeys()),
                    "actual SDK request method/path/status set differs from sdk/conformance/routes.json");

            Map<String, Object> falseCommit = new LinkedHashMap<>(payload);
            falseCommit.put("accepted", false);
            client.commit(falseCommit).join();
            require(lastRequest[4].contains("\"accepted\":false"), "commit accepted=false was omitted");
            Map<String, Object> falseBatch = new LinkedHashMap<>(payload);
            falseBatch.put("items", List.of(Map.of("accepted", false)));
            client.commitBatch(falseBatch).join();
            require(lastRequest[4].contains("\"accepted\":false"), "batch accepted=false was omitted");

            Map<String, Object> cyclicPayload = new LinkedHashMap<>();
            cyclicPayload.put("self", cyclicPayload);
            Object deepPayload = "leaf";
            for (int depth = 0; depth < 66; depth++) deepPayload = List.of(deepPayload);
            List<Map<String, ?>> invalidPayloads = new ArrayList<>();
            invalidPayloads.add(Map.of("nested", List.of(9_007_199_254_740_992L)));
            invalidPayloads.add(Map.of("nested", Double.NaN));
            invalidPayloads.add(Map.of("nested", Double.POSITIVE_INFINITY));
            invalidPayloads.add(Map.of("nested", "\ud800"));
            invalidPayloads.add(cyclicPayload);
            invalidPayloads.add(Map.of("nested", deepPayload));
            int transportCallsBeforeInvalidPayloads = transportCalls[0];
            int codecCallsBeforeInvalidPayloads = codecEncodeCalls[0];
            for (Map<String, ?> invalidPayload : invalidPayloads) {
                try {
                    client.commit(invalidPayload);
                    throw new AssertionError("invalid JSON payload was accepted");
                } catch (RinProtocolException expected) {
                    require("invalid_request".equals(expected.code()), "invalid JSON payload returned wrong error");
                }
            }
            require(
                    transportCalls[0] == transportCallsBeforeInvalidPayloads,
                    "invalid JSON payload reached the transport");
            require(
                    codecEncodeCalls[0] == codecCallsBeforeInvalidPayloads,
                    "invalid JSON payload reached the host codec");

            mode[0] = "api-error";
            try {
                client.health().join();
                throw new AssertionError("API error envelope was accepted");
            } catch (CompletionException expected) {
                Throwable cause = rootCause(expected);
                require(cause instanceof RinApiException, "API error returned wrong type");
                RinApiException apiError = (RinApiException) cause;
                require(apiError.status() == 400, "API error status changed");
                require("invalid_request".equals(apiError.code()), "API error code changed");
                require("actor_id".equals(apiError.field()), "API error field changed");
            }
            mode[0] = "normal";
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

            for (String malformedMode : List.of(
                    "malformed-delete",
                    "malformed-double-delete",
                    "malformed-float-delete")) {
                mode[0] = malformedMode;
                try {
                    client.waitForProposal(
                            "job.fixture",
                            Duration.ofMillis(50),
                            Duration.ofMillis(10)).join();
                    throw new AssertionError(
                            "malformed DELETE proposal identity was accepted for " + malformedMode);
                } catch (CompletionException expected) {
                    Throwable cause = rootCause(expected);
                    require(cause instanceof RinProtocolException, "malformed DELETE returned wrong error type");
                    require(
                            "invalid_job".equals(((RinProtocolException) cause).code()),
                            "malformed DELETE returned wrong error");
                }
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

    private static String routeKey(String name, String method, String path, int status) {
        return name + " " + method + " " + path + " " + status;
    }

    private static List<String> contractRouteKeys() throws Exception {
        String manifest = Files.readString(contractManifestPath(), StandardCharsets.UTF_8);
        Matcher matcher = Pattern.compile(
                "\"name\"\\s*:\\s*\"([a-z0-9_]+)\"\\s*,\\s*"
                        + "\"method\"\\s*:\\s*\"([A-Z]+)\"\\s*,\\s*"
                        + "\"path\"\\s*:\\s*\"([^\"]+)\"\\s*,\\s*"
                        + "\"status\"\\s*:\\s*(\\d+)")
                .matcher(manifest);
        List<String> routes = new ArrayList<>();
        while (matcher.find()) {
            routes.add(routeKey(
                    matcher.group(1),
                    matcher.group(2),
                    matcher.group(3),
                    Integer.parseInt(matcher.group(4))));
        }
        require(!routes.isEmpty(), "sdk/conformance/routes.json contains no operations");
        Collections.sort(routes);
        return routes;
    }

    private static Path contractManifestPath() {
        Path directory = Path.of("").toAbsolutePath();
        while (directory != null) {
            Path repositoryCandidate = directory.resolve("sdk/conformance/routes.json");
            if (Files.isRegularFile(repositoryCandidate)) return repositoryCandidate;
            Path sdkCandidate = directory.resolve("conformance/routes.json");
            if (Files.isRegularFile(sdkCandidate)) return sdkCandidate;
            directory = directory.getParent();
        }
        throw new IllegalStateException("cannot locate sdk/conformance/routes.json");
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
