package io.github.sunrioa.rin;

import java.time.Duration;
import java.util.Map;
import java.util.Objects;
import java.util.concurrent.CompletableFuture;

/** Thin client for the task-only internal Agent API hosted by rin-control. */
public final class RinAgentClient {
    public static final String CONTRACT_VERSION = "rin.agent/v1";
    public static final String DEFAULT_BASE_URL = RinControlClient.DEFAULT_BASE_URL;
    public static final int MAX_RESPONSE_BYTES = RinControlClient.MAX_RESPONSE_BYTES;

    private final RinControlClient transport;

    public RinAgentClient(String token, JsonValueCodec codec) {
        this(DEFAULT_BASE_URL, token, Duration.ofSeconds(30), MAX_RESPONSE_BYTES, codec);
    }

    public RinAgentClient(
            String baseUrl,
            String token,
            Duration timeout,
            int maxResponseBytes,
            JsonValueCodec codec) {
        transport = new RinControlClient(
                baseUrl, token, timeout, maxResponseBytes,
                Objects.requireNonNull(codec, "codec"));
    }

    public CompletableFuture<Map<String, Object>> info() {
        return transport.requestObject("GET", "/agent/v1/info", null)
                .thenApply(info -> {
                    if (!CONTRACT_VERSION.equals(info.get("contract_version"))) {
                        throw new RinProtocolException(
                                "agent_contract_mismatch",
                                "Agent API returned an unsupported contract");
                    }
                    return info;
                });
    }

    public CompletableFuture<Map<String, Object>> startTask(Map<String, ?> input) {
        return post("/agent/v1/tasks/start", input);
    }

    public CompletableFuture<Map<String, Object>> getTask(Map<String, ?> input) {
        return post("/agent/v1/tasks/get", input);
    }

    public CompletableFuture<Map<String, Object>> runTask(Map<String, ?> input) {
        return post("/agent/v1/tasks/run", input);
    }

    public CompletableFuture<Map<String, Object>> resumeTask(Map<String, ?> input) {
        return post("/agent/v1/tasks/resume", input);
    }

    public CompletableFuture<Map<String, Object>> cancelTask(Map<String, ?> input) {
        return post("/agent/v1/tasks/cancel", input);
    }

    private CompletableFuture<Map<String, Object>> post(
            String path,
            Map<String, ?> input) {
        return transport.requestObject(
                "POST", path, Objects.requireNonNull(input, "input"));
    }
}
