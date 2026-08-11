package io.github.sunrioa.rin;

import com.sun.net.httpserver.HttpServer;

import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.List;
import java.util.Map;
import java.util.concurrent.CompletableFuture;
import java.util.function.Supplier;

final class RinAgentClientTest {
    private record RequestCase(
            Supplier<CompletableFuture<Map<String, Object>>> call,
            String method,
            String path) { }

    private RinAgentClientTest() { }

    static void run() throws Exception {
        String[] request = new String[4];
        HttpServer server = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        server.createContext("/", exchange -> {
            request[0] = exchange.getRequestMethod();
            request[1] = exchange.getRequestURI().getPath();
            request[2] = exchange.getRequestHeaders().getFirst("Authorization");
            request[3] = new String(
                    exchange.getRequestBody().readAllBytes(), StandardCharsets.UTF_8);
            String response = request[1].equals("/agent/v1/info")
                    ? "info" : "object";
            byte[] body = response.getBytes(StandardCharsets.UTF_8);
            exchange.getResponseHeaders().set(
                    "Content-Type", "application/json; charset=utf-8");
            exchange.sendResponseHeaders(200, body.length);
            exchange.getResponseBody().write(body);
            exchange.close();
        });
        server.start();

        try {
            AgentFixtureCodec codec = new AgentFixtureCodec();
            RinAgentClient client = new RinAgentClient(
                    "http://127.0.0.1:" + server.getAddress().getPort(),
                    "agent-fixture-token-32-bytes!!!!",
                    Duration.ofSeconds(2),
                    RinAgentClient.MAX_RESPONSE_BYTES,
                    codec);
            Map<String, Object> target = Map.of("task_id", "task.fixture");
            List<RequestCase> calls = List.of(
                    new RequestCase(client::info, "GET", "/agent/v1/info"),
                    new RequestCase(() -> client.startTask(Map.of(
                            "task_id", "task.fixture",
                            "host_id", "host.fixture",
                            "world_id", "world.fixture",
                            "actor_id", "actor.fixture",
                            "controller_id", "controller.fixture",
                            "goal", "Wait safely.")),
                            "POST", "/agent/v1/tasks/start"),
                    new RequestCase(() -> client.getTask(target),
                            "POST", "/agent/v1/tasks/get"),
                    new RequestCase(() -> client.runTask(target),
                            "POST", "/agent/v1/tasks/run"),
                    new RequestCase(() -> client.resumeTask(target),
                            "POST", "/agent/v1/tasks/resume"),
                    new RequestCase(() -> client.cancelTask(target),
                            "POST", "/agent/v1/tasks/cancel"));

            for (RequestCase call : calls) {
                call.call().get().join();
                require(call.method().equals(request[0]) && call.path().equals(request[1]),
                        "Agent client used the wrong route: " + request[0] + " " + request[1]);
                require("Bearer agent-fixture-token-32-bytes!!!!".equals(request[2]),
                        "Agent client omitted its dedicated bearer token");
                require(call.method().equals("GET") || !request[3].isEmpty(),
                        "Agent client omitted a POST body");
            }
        } finally {
            server.stop(0);
        }
    }

    private static void require(boolean condition, String message) {
        if (!condition) throw new AssertionError(message);
    }

    private static final class AgentFixtureCodec implements JsonValueCodec {
        @Override
        public String encodeObject(Map<String, ?> value) {
            return "{}";
        }

        @Override
        public Object decodeValue(String json) {
            return json.equals("info")
                    ? Map.of("contract_version", RinAgentClient.CONTRACT_VERSION)
                    : Map.of("task", Map.of("task_id", "task.fixture"));
        }
    }
}
