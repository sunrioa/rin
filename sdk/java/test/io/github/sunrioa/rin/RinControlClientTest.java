package io.github.sunrioa.rin;

import com.sun.net.httpserver.HttpServer;

import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Duration;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.CompletionException;
import java.util.function.Supplier;

final class RinControlClientTest {
    private record RequestCase(
            Supplier<CompletableFuture<?>> call,
            String method,
            String path) { }

    private RinControlClientTest() { }

    static void run() throws Exception {
        String fixture = Files.readString(
                Path.of("api/control-v2-fixtures.json"),
                StandardCharsets.UTF_8);
        require(fixture.contains("\"contract_version\": \"rin.control/v2\""),
                "shared Control fixture has the wrong contract");
        require(fixture.contains("\"request_id\": \"request.fixture\""),
                "shared Control fixture lost its action request");
        String planFixture = Files.readString(
                Path.of("api/task-plan-v1-fixtures.json"),
                StandardCharsets.UTF_8);
        require(planFixture.contains("\"contract_version\": \"rin.task-plan/v1\""),
                "shared task plan fixture has the wrong contract");

        String[] lastRequest = new String[4];
        String[] mode = {"normal"};
        HttpServer server = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        server.createContext("/", exchange -> {
            lastRequest[0] = exchange.getRequestMethod();
            lastRequest[1] = exchange.getRequestURI().getPath();
            lastRequest[2] = exchange.getRequestHeaders().getFirst("Authorization");
            lastRequest[3] = new String(
                    exchange.getRequestBody().readAllBytes(),
                    StandardCharsets.UTF_8);
            if (mode[0].equals("slow")) {
                try {
                    Thread.sleep(200);
                } catch (InterruptedException interrupted) {
                    Thread.currentThread().interrupt();
                }
            }
            String response = lastRequest[1].equals("/control/v2/info")
                    ? "info"
                    : lastRequest[1].equals("/control/v2/worlds") ||
                            lastRequest[1].equals("/control/v2/actors")
                            ? "array"
                            : "object";
            int status = 200;
            if (mode[0].equals("api-error")) {
                response = "api-error";
                status = 409;
            } else if (mode[0].equals("redirect")) {
                status = 302;
            } else if (mode[0].equals("oversized")) {
                response = "x".repeat(2048);
            }
            if (!mode[0].equals("wrong-content-type")) {
                exchange.getResponseHeaders().set("Content-Type", "application/json; charset=utf-8");
            }
            byte[] body = response.getBytes(StandardCharsets.UTF_8);
            exchange.sendResponseHeaders(status, body.length);
            exchange.getResponseBody().write(body);
            exchange.close();
        });
        server.start();

        try {
            FixtureCodec codec = new FixtureCodec();
            String baseUrl = "http://127.0.0.1:" + server.getAddress().getPort();
            RinControlClient client = new RinControlClient(
                    baseUrl,
                    "control-fixture-token-32-bytes!!",
                    Duration.ofSeconds(2),
                    RinControlClient.MAX_RESPONSE_BYTES,
                    codec);
            Map<String, Object> world = Map.of(
                    "host_id", "host.fixture",
                    "world_id", "world.fixture");
            Map<String, Object> actor = Map.of(
                    "host_id", "host.fixture",
                    "world_id", "world.fixture",
                    "actor_id", "actor.fixture");
            Map<String, Object> operation = Map.of("operation_id", "operation.fixture");

            List<RequestCase> requests = List.of(
                    new RequestCase(client::info, "GET", "/control/v2/info"),
                    new RequestCase(client::listWorlds, "POST", "/control/v2/worlds"),
                    new RequestCase(() -> client.listActors(world), "POST", "/control/v2/actors"),
                    new RequestCase(() -> client.getActor(actor), "POST", "/control/v2/actor"),
                    new RequestCase(() -> client.waitActor(waitActor()), "POST", "/control/v2/wait-actor"),
                    new RequestCase(() -> client.observeActor(actor), "POST", "/control/v2/observe"),
                    new RequestCase(() -> client.listCapabilities(actor), "POST", "/control/v2/capabilities"),
                    new RequestCase(() -> client.describeCapability(describeCapability()), "POST", "/control/v2/capability"),
                    new RequestCase(() -> client.acquireController(acquireController()), "POST", "/control/v2/controllers/acquire"),
                    new RequestCase(() -> client.renewController(renewController()), "POST", "/control/v2/controllers/renew"),
                    new RequestCase(() -> client.releaseController(releaseController()), "POST", "/control/v2/controllers/release"),
                    new RequestCase(() -> client.getController(actor), "POST", "/control/v2/controllers/get"),
                    new RequestCase(() -> client.submitAction(submitAction()), "POST", "/control/v2/actions/submit"),
                    new RequestCase(() -> client.confirmAction(operation), "POST", "/control/v2/actions/confirm"),
                    new RequestCase(() -> client.getOperation(operation), "POST", "/control/v2/operations/get"),
                    new RequestCase(() -> client.waitOperation(waitOperation()), "POST", "/control/v2/operations/wait"),
                    new RequestCase(() -> client.getTaskTimeline(taskTimeline()), "POST", "/control/v2/tasks/timeline/get"),
                    new RequestCase(() -> client.waitTaskTimeline(waitTaskTimeline()), "POST", "/control/v2/tasks/timeline/wait"),
                    new RequestCase(() -> client.cancelOperation(operation), "POST", "/control/v2/operations/cancel"),
                    new RequestCase(() -> client.setEmergencyStop(emergencyStop()), "POST", "/control/v2/emergency-stop"),
                    new RequestCase(() -> client.createTaskPlan(actor), "POST", "/plans/v1/create"),
                    new RequestCase(() -> client.getTaskPlan(actor), "POST", "/plans/v1/get"),
                    new RequestCase(() -> client.waitTaskPlan(actor), "POST", "/plans/v1/wait"),
                    new RequestCase(() -> client.reviseTaskPlan(actor), "POST", "/plans/v1/revise"),
                    new RequestCase(() -> client.setTaskPlanStatus(actor), "POST", "/plans/v1/status"),
                    new RequestCase(() -> client.requestTaskStepTransition(actor), "POST", "/plans/v1/transition"),
                    new RequestCase(() -> client.submitTaskStepAction(actor), "POST", "/plans/v1/submit-step-action"));

            for (RequestCase request : requests) {
                request.call().get().join();
                require(request.method().equals(lastRequest[0]),
                        "Control method changed for " + request.path() + ": " + lastRequest[0]);
                require(request.path().equals(lastRequest[1]),
                        "Control route changed for " + request.path() + ": " + lastRequest[1]);
                require("Bearer control-fixture-token-32-bytes!!".equals(lastRequest[2]),
                        "Control bearer token was not sent");
                require(request.method().equals("GET") || !lastRequest[3].isEmpty(),
                        "Control POST body was omitted for " + request.path());
            }
            require(codec.encodedInputs.size() == 26, "Control client did not encode every POST body");

            mode[0] = "api-error";
            expectCode(() -> client.getActor(actor).join(), RinApiException.class, "stale");
            mode[0] = "wrong-content-type";
            expectCode(() -> client.getActor(actor).join(), RinProtocolException.class, "invalid_response");
            mode[0] = "redirect";
            expectCode(() -> client.getActor(actor).join(), RinTransportException.class, "redirect_rejected");
            mode[0] = "oversized";
            RinControlClient small = new RinControlClient(
                    baseUrl,
                    "control-fixture-token-32-bytes!!",
                    Duration.ofSeconds(2),
                    1024,
                    codec);
            expectCode(() -> small.getActor(actor).join(), RinProtocolException.class, "response_too_large");
            mode[0] = "slow";
            RinControlClient slow = new RinControlClient(
                    baseUrl,
                    "control-fixture-token-32-bytes!!",
                    Duration.ofMillis(50),
                    RinControlClient.MAX_RESPONSE_BYTES,
                    codec);
            expectCode(() -> slow.getActor(actor).join(), RinTransportException.class, "transport_timeout");

            expectCode(
                    () -> new RinControlClient("short", codec),
                    RinConfigurationException.class,
                    "invalid_token");
            expectCode(
                    () -> new RinControlClient(
                            "https://example.com:7375",
                            "control-fixture-token-32-bytes!!",
                            Duration.ofSeconds(1),
                            1024,
                            codec),
                    RinConfigurationException.class,
                    "invalid_base_url");
        } finally {
            server.stop(0);
        }
    }

    private static Map<String, Object> waitActor() {
        Map<String, Object> value = new LinkedHashMap<>();
        value.put("host_id", "host.fixture");
        value.put("world_id", "world.fixture");
        value.put("actor_id", "actor.fixture");
        value.put("after_observation_seq", 7L);
        value.put("after_authority_revision", 3L);
        value.put("after_controller_lease_id", "controller-lease.fixture");
        value.put("after_emergency_stop_revision", 2L);
        value.put("wait_millis", 25000L);
        return value;
    }

    private static Map<String, Object> describeCapability() {
        Map<String, Object> value = new LinkedHashMap<>(Map.of(
                "host_id", "host.fixture",
                "world_id", "world.fixture",
                "actor_id", "actor.fixture"));
        value.put("capability", Map.of(
                "id", "companion.navigation.move",
                "version", "2.0.0"));
        return value;
    }

    private static Map<String, Object> acquireController() {
        Map<String, Object> value = new LinkedHashMap<>(Map.of(
                "host_id", "host.fixture",
                "world_id", "world.fixture",
                "actor_id", "actor.fixture"));
        value.put("controller_id", "controller.fixture");
        value.put("lease_ttl_millis", 30000L);
        return value;
    }

    private static Map<String, Object> renewController() {
        Map<String, Object> value = new LinkedHashMap<>(Map.of(
                "host_id", "host.fixture",
                "world_id", "world.fixture",
                "actor_id", "actor.fixture"));
        value.put("lease_id", "controller-lease.fixture");
        value.put("lease_ttl_millis", 30000L);
        return value;
    }

    private static Map<String, Object> releaseController() {
        Map<String, Object> value = new LinkedHashMap<>(Map.of(
                "host_id", "host.fixture",
                "world_id", "world.fixture",
                "actor_id", "actor.fixture"));
        value.put("lease_id", "controller-lease.fixture");
        return value;
    }

    private static Map<String, Object> submitAction() {
        Map<String, Object> request = new LinkedHashMap<>();
        request.put("request_id", "request.fixture");
        request.put("controller_id", "controller.fixture");
        request.put("actor_id", "actor.fixture");
        request.put("capability", Map.of(
                "id", "companion.navigation.move",
                "version", "2.0.0"));
        request.put("spec_digest", "a".repeat(64));
        request.put("arguments", Map.of(
                "destination", Map.of("x", 12L, "y", 64L, "z", -4L)));
        request.put("target_refs", List.of());
        request.put("expected_epoch", Map.of(
                "session_id", "session.fixture",
                "world_id", "world.fixture",
                "host", 2L,
                "world", 4L,
                "timeline", 1L));
        request.put("observation_sequence", 7L);
        request.put("task_id", "task.fixture");
        request.put("idempotency_key", "action.fixture");
        return Map.of(
                "host_id", "host.fixture",
                "world_id", "world.fixture",
                "action_request", request,
                "parent_operation_id", "operation.parent.fixture");
    }

    private static Map<String, Object> waitOperation() {
        return Map.of(
                "operation_id", "operation.fixture",
                "after_cursor", "op2:queued:0:false:1000::0:false:0",
                "wait_millis", 25000L);
    }

    private static Map<String, Object> taskTimeline() {
        return Map.of(
                "task_id", "task.fixture",
                "after_cursor", "tl1:0",
                "limit", 64L);
    }

    private static Map<String, Object> waitTaskTimeline() {
        return Map.of(
                "task_id", "task.fixture",
                "after_cursor", "tl1:0",
                "limit", 64L,
                "wait_millis", 25000L);
    }

    private static Map<String, Object> emergencyStop() {
        return Map.of(
                "host_id", "host.fixture",
                "world_id", "world.fixture",
                "actor_id", "actor.fixture",
                "active", true);
    }

    private static void expectCode(
            Runnable operation,
            Class<? extends RinException> expectedType,
            String expectedCode) {
        try {
            operation.run();
            throw new AssertionError("expected " + expectedCode);
        } catch (RuntimeException failure) {
            Throwable cause = failure instanceof CompletionException && failure.getCause() != null
                    ? failure.getCause()
                    : failure;
            require(expectedType.isInstance(cause), "unexpected Control error: " + cause);
            require(expectedCode.equals(((RinException) cause).code()),
                    "unexpected Control error code: " + ((RinException) cause).code());
        }
    }

    private static void require(boolean condition, String message) {
        if (!condition) throw new AssertionError(message);
    }

    private static final class FixtureCodec implements JsonValueCodec {
        private final List<Map<String, ?>> encodedInputs = new ArrayList<>();

        @Override
        public String encodeObject(Map<String, ?> value) {
            encodedInputs.add(value);
            return "{\"fixture\":true}";
        }

        @Override
        public Object decodeValue(String json) {
            return switch (json) {
                case "info" -> Map.of(
                        "contract_version", RinControlClient.CONTRACT_VERSION,
                        "principal", Map.of("id", "principal.fixture"));
                case "array" -> List.of(Map.of("id", "fixture"));
                case "object" -> Map.of("status", "ok");
                case "api-error" -> Map.of("code", "stale", "error", "world changed");
                default -> throw new IllegalArgumentException("invalid fixture JSON");
            };
        }
    }
}
