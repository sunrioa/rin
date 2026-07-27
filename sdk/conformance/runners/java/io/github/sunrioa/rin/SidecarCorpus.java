package io.github.sunrioa.rin;

import java.time.Duration;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.concurrent.CompletionException;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/** Runs the Java transport against the shared live Sidecar corpus. */
public final class SidecarCorpus {
    private static final Pattern STRING_FIELD =
            Pattern.compile("\"([a-z_]+)\"\\s*:\\s*\"([^\"]*)\"");
    private static final Pattern INTEGER_FIELD =
            Pattern.compile("\"revision\"\\s*:\\s*([0-9]+)");

    private SidecarCorpus() { }

    public static void main(String[] args) {
        String body = required("RIN_SDK_CORPUS_BODY");
        String token = required("RIN_SDK_CORPUS_TOKEN");
        JsonCodec codec = new CorpusCodec(body);
        RinClient client = new RinClient(
                required("RIN_SDK_CORPUS_BASE_URL"),
                token,
                Duration.ofSeconds(5),
                RinClient.DEFAULT_MAX_RESPONSE_BYTES,
                codec);
        Map<String, Object> health = client.health().join();
        require(RinClient.PROTOCOL_VERSION.equals(health.get("protocol_version")),
                "Java SDK received an invalid health response");
        Map<String, Object> payload = createPayload();
        Map<String, Object> first = client.createSession(payload).join();
        Map<String, Object> retry = client.createSession(payload).join();
        require(Boolean.FALSE.equals(first.get("duplicate")), "first mutation was duplicate");
        require(Boolean.TRUE.equals(retry.get("duplicate")), "exact retry was not duplicate");
        require(first.get("revision").equals(retry.get("revision")), "retry revision changed");
        require(first.get("head_hash").equals(retry.get("head_hash")), "retry head changed");

        RinClient slow = new RinClient(
                required("RIN_SDK_CORPUS_SLOW_URL"),
                token,
                Duration.ofMillis(50),
                RinClient.DEFAULT_MAX_RESPONSE_BYTES,
                codec);
        try {
            slow.createSession(payload).join();
            throw new AssertionError("Java SDK did not enforce its network timeout");
        } catch (CompletionException error) {
            require(error.getCause() instanceof RinTransportException
                            && ((RinTransportException) error.getCause()).code()
                                    .equals("transport_timeout"),
                    "Java timeout error mapping changed: " + error.getCause());
        }
        System.out.println("Java SDK live Sidecar corpus passed");
    }

    private static Map<String, Object> createPayload() {
        String client = required("RIN_SDK_CORPUS_CLIENT");
        Map<String, Object> payload = new LinkedHashMap<>();
        payload.put("protocol_version", RinClient.PROTOCOL_VERSION);
        payload.put("request_id", "create." + client);
        payload.put("session_id", "session." + client);
        payload.put("binding", Map.of(
                "game_id", "conformance",
                "content_id", "sdk-corpus",
                "content_version", "1",
                "content_hash", "sha256:conformance"));
        payload.put("actors", List.of(Map.of(
                "id", "npc." + client,
                "kind", "npc",
                "display_name", "SDK Corpus NPC",
                "think_every_ticks", 1,
                "enabled", true)));
        return payload;
    }

    private static String required(String name) {
        String value = System.getenv(name);
        if (value == null || value.isEmpty()) {
            throw new IllegalStateException(name + " is required");
        }
        return value;
    }

    private static void require(boolean condition, String message) {
        if (!condition) throw new AssertionError(message);
    }

    private static final class CorpusCodec implements JsonCodec {
        private final String requestBody;

        private CorpusCodec(String requestBody) {
            this.requestBody = requestBody;
        }

        @Override
        public String encode(Map<String, ?> value) {
            return requestBody;
        }

        @Override
        public Map<String, Object> decodeObject(String json) {
            Map<String, Object> values = new LinkedHashMap<>();
            Matcher strings = STRING_FIELD.matcher(json);
            while (strings.find()) values.put(strings.group(1), strings.group(2));
            Matcher revision = INTEGER_FIELD.matcher(json);
            if (revision.find()) values.put("revision", Long.parseLong(revision.group(1)));
            values.put("duplicate", json.matches("(?s).*\"duplicate\"\\s*:\\s*true.*"));
            if (json.matches("(?s).*\"ok\"\\s*:\\s*true.*")) {
                return Map.of("ok", true, "data", values);
            }
            return Map.of("ok", false, "error", values);
        }
    }
}
