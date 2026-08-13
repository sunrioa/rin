package io.github.sunrioa.rin;

import java.io.ByteArrayOutputStream;
import java.net.URI;
import java.net.URISyntaxException;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.net.http.HttpTimeoutException;
import java.nio.ByteBuffer;
import java.nio.charset.CharacterCodingException;
import java.nio.charset.CodingErrorAction;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.ArrayList;
import java.util.Collections;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.Objects;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.CompletionException;
import java.util.concurrent.CompletionStage;
import java.util.concurrent.Flow;

/** Thin loopback client for the engine-neutral Control V2 contract. */
public final class RinControlClient {
    public static final String VERSION = "0.7.0";
    public static final String CONTRACT_VERSION = "rin.control/v2";
    public static final String DEFAULT_BASE_URL = "http://127.0.0.1:7375";
    public static final int MAX_RESPONSE_BYTES = 8 * 1024 * 1024;

    private final String baseUrl;
    private final String token;
    private final Duration timeout;
    private final int maxResponseBytes;
    private final JsonValueCodec codec;
    private final HttpClient http;

    public RinControlClient(String token, JsonValueCodec codec) {
        this(DEFAULT_BASE_URL, token, Duration.ofSeconds(30), MAX_RESPONSE_BYTES, codec);
    }

    public RinControlClient(
            String baseUrl,
            String token,
            Duration timeout,
            int maxResponseBytes,
            JsonValueCodec codec) {
        this.baseUrl = normalizeBaseUrl(baseUrl);
        this.token = validateToken(token);
        this.timeout = Objects.requireNonNull(timeout, "timeout");
        if (timeout.compareTo(Duration.ofMillis(50)) < 0 ||
                timeout.compareTo(Duration.ofSeconds(120)) > 0) {
            throw new RinConfigurationException(
                    "invalid_timeout",
                    "Control timeout must be between 50 ms and 120 seconds");
        }
        if (maxResponseBytes < 1024 || maxResponseBytes > MAX_RESPONSE_BYTES) {
            throw new RinConfigurationException(
                    "invalid_response_limit",
                    "Control response limit must be between 1 KiB and 8 MiB");
        }
        this.maxResponseBytes = maxResponseBytes;
        this.codec = Objects.requireNonNull(codec, "codec");
        this.http = HttpClient.newBuilder()
                .connectTimeout(timeout)
                .followRedirects(HttpClient.Redirect.NEVER)
                .build();
    }

    public CompletableFuture<Map<String, Object>> info() {
        return requestObject("GET", "/control/v2/info", null).thenApply(info -> {
            if (!CONTRACT_VERSION.equals(info.get("contract_version"))) {
                throw new RinProtocolException(
                        "control_contract_mismatch",
                        "Control Daemon returned an unsupported contract");
            }
            return info;
        });
    }

    public CompletableFuture<List<Map<String, Object>>> listWorlds() {
        return requestObjectList("/control/v2/worlds", Map.of());
    }

    public CompletableFuture<List<Map<String, Object>>> listActors(Map<String, ?> input) {
        return requestObjectList("/control/v2/actors", input);
    }

    public CompletableFuture<Map<String, Object>> getActor(Map<String, ?> input) {
        return postObject("/control/v2/actor", input);
    }

    public CompletableFuture<Map<String, Object>> waitActor(Map<String, ?> input) {
        return postObject("/control/v2/wait-actor", input);
    }

    public CompletableFuture<Map<String, Object>> observeActor(Map<String, ?> input) {
        return postObject("/control/v2/observe", input);
    }

    public CompletableFuture<Map<String, Object>> listCapabilities(Map<String, ?> input) {
        return postObject("/control/v2/capabilities", input);
    }

    public CompletableFuture<Map<String, Object>> describeCapability(Map<String, ?> input) {
        return postObject("/control/v2/capability", input);
    }

    public CompletableFuture<Map<String, Object>> acquireController(Map<String, ?> input) {
        return postObject("/control/v2/controllers/acquire", input);
    }

    public CompletableFuture<Map<String, Object>> renewController(Map<String, ?> input) {
        return postObject("/control/v2/controllers/renew", input);
    }

    public CompletableFuture<Map<String, Object>> releaseController(Map<String, ?> input) {
        return postObject("/control/v2/controllers/release", input);
    }

    public CompletableFuture<Map<String, Object>> getController(Map<String, ?> input) {
        return postObject("/control/v2/controllers/get", input);
    }

    public CompletableFuture<Map<String, Object>> submitAction(Map<String, ?> input) {
        return postObject("/control/v2/actions/submit", input);
    }

    public CompletableFuture<Map<String, Object>> confirmAction(Map<String, ?> input) {
        return postObject("/control/v2/actions/confirm", input);
    }

    public CompletableFuture<Map<String, Object>> getOperation(Map<String, ?> input) {
        return postObject("/control/v2/operations/get", input);
    }

    public CompletableFuture<Map<String, Object>> waitOperation(Map<String, ?> input) {
        return postObject("/control/v2/operations/wait", input);
    }

    public CompletableFuture<Map<String, Object>> getTaskTimeline(Map<String, ?> input) {
        return postObject("/control/v2/tasks/timeline/get", input);
    }

    public CompletableFuture<Map<String, Object>> waitTaskTimeline(Map<String, ?> input) {
        return postObject("/control/v2/tasks/timeline/wait", input);
    }

    public CompletableFuture<Map<String, Object>> cancelOperation(Map<String, ?> input) {
        return postObject("/control/v2/operations/cancel", input);
    }

    public CompletableFuture<Map<String, Object>> setEmergencyStop(Map<String, ?> input) {
        return postObject("/control/v2/emergency-stop", input);
    }

    private CompletableFuture<Map<String, Object>> postObject(String path, Map<String, ?> input) {
        return requestObject("POST", path, Objects.requireNonNull(input, "input"));
    }

    private CompletableFuture<List<Map<String, Object>>> requestObjectList(
            String path,
            Map<String, ?> input) {
        return request("POST", path, Objects.requireNonNull(input, "input"))
                .thenApply(RinControlClient::asObjectList);
    }

    CompletableFuture<Map<String, Object>> requestObject(
            String method,
            String path,
            Map<String, ?> input) {
        return request(method, path, input).thenApply(RinControlClient::asObject);
    }

    private CompletableFuture<Object> request(
            String method,
            String path,
            Map<String, ?> input) {
        HttpRequest.BodyPublisher body = HttpRequest.BodyPublishers.noBody();
        HttpRequest.Builder builder = HttpRequest.newBuilder(URI.create(baseUrl + path))
                .timeout(timeout)
                .header("Accept", "application/json")
                .header("Authorization", "Bearer " + token)
                .header("User-Agent", "rin-control-java/" + VERSION);
        if (input != null) {
            JsonValues.validateRequest(input);
            final String encoded;
            try {
                encoded = codec.encodeObject(input);
            } catch (Exception exception) {
                throw new RinProtocolException(
                        "invalid_request",
                        "Control payload is not JSON serializable",
                        exception);
            }
            if (encoded == null || hasUnpairedSurrogate(encoded)) {
                throw new RinProtocolException(
                        "invalid_request",
                        "Control JSON codec returned invalid output");
            }
            body = HttpRequest.BodyPublishers.ofString(encoded, StandardCharsets.UTF_8);
            builder.header("Content-Type", "application/json; charset=utf-8");
        }
        builder.method(method, body);

        CompletableFuture<HttpResponse<byte[]>> network = http.sendAsync(
                builder.build(),
                ignored -> new BoundedBodySubscriber(maxResponseBytes));
        CompletableFuture<Object> result = new CompletableFuture<>();
        network.whenComplete((response, failure) -> {
            if (failure != null) {
                Throwable cause = unwrap(failure);
                if (cause instanceof RinException) {
                    result.completeExceptionally(cause);
                } else if (cause instanceof HttpTimeoutException) {
                    result.completeExceptionally(new RinTransportException(
                            "transport_timeout",
                            "Control Daemon request timed out",
                            cause));
                } else {
                    result.completeExceptionally(new RinTransportException(
                            "transport_failed",
                            "Control Daemon is unavailable",
                            cause));
                }
                return;
            }
            try {
                result.complete(decodeResponse(response));
            } catch (RuntimeException exception) {
                result.completeExceptionally(exception);
            }
        });
        result.whenComplete((ignored, failure) -> {
            if (result.isCancelled()) network.cancel(true);
        });
        return result;
    }

    private Object decodeResponse(HttpResponse<byte[]> response) {
        int status = response.statusCode();
        if (status >= 300 && status < 400) {
            throw new RinTransportException(
                    "redirect_rejected",
                    "Control Daemon attempted to redirect");
        }
        String mediaType = response.headers()
                .firstValue("Content-Type")
                .orElse("")
                .split(";", 2)[0]
                .strip()
                .toLowerCase(Locale.ROOT);
        if (!"application/json".equals(mediaType)) {
            throw new RinProtocolException(
                    "invalid_response",
                    "Control Daemon response must be application/json");
        }
        String contentLength = response.headers().firstValue("Content-Length").orElse(null);
        if (contentLength != null) {
            final long declared;
            try {
                declared = Long.parseLong(contentLength);
            } catch (NumberFormatException exception) {
                throw new RinProtocolException(
                        "invalid_response",
                        "Control Daemon returned an invalid Content-Length",
                        exception);
            }
            if (declared < 0 || declared > maxResponseBytes) {
                throw new RinProtocolException(
                        "response_too_large",
                        "Control Daemon response exceeds the configured limit");
            }
        }

        final String json;
        try {
            json = StandardCharsets.UTF_8.newDecoder()
                    .onMalformedInput(CodingErrorAction.REPORT)
                    .onUnmappableCharacter(CodingErrorAction.REPORT)
                    .decode(ByteBuffer.wrap(response.body()))
                    .toString();
        } catch (CharacterCodingException exception) {
            throw new RinProtocolException(
                    "invalid_response",
                    "Control Daemon returned invalid UTF-8",
                    exception);
        }

        final Object value;
        try {
            value = codec.decodeValue(json);
        } catch (Exception exception) {
            if (status < 200 || status >= 300) {
                throw new RinApiException(
                        errorCode(status),
                        "Control Daemon request failed",
                        status,
                        "");
            }
            throw new RinProtocolException(
                    "invalid_response",
                    "Control Daemon returned invalid JSON",
                    exception);
        }
        if (!(value instanceof Map<?, ?>) && !(value instanceof List<?>)) {
            throw new RinProtocolException(
                    "invalid_response",
                    "Control Daemon response must be an object or array");
        }
        if (status < 200 || status >= 300) {
            Map<?, ?> detail = value instanceof Map<?, ?> map ? map : Map.of();
            throw new RinApiException(
                    RinException.safeText(detail.get("code"), 96, errorCode(status)),
                    RinException.safeText(
                            detail.get("error"),
                            500,
                            "Control Daemon request failed"),
                    status,
                    "");
        }
        return value;
    }

    private static Map<String, Object> asObject(Object value) {
        if (!(value instanceof Map<?, ?> map)) {
            throw new RinProtocolException(
                    "invalid_response",
                    "Control Daemon response must be an object");
        }
        Map<String, Object> copy = new LinkedHashMap<>();
        for (Map.Entry<?, ?> entry : map.entrySet()) {
            if (!(entry.getKey() instanceof String key)) {
                throw new RinProtocolException(
                        "invalid_response",
                        "Control Daemon response contains a non-string key");
            }
            copy.put(key, entry.getValue());
        }
        return Collections.unmodifiableMap(copy);
    }

    private static List<Map<String, Object>> asObjectList(Object value) {
        if (!(value instanceof List<?> list)) {
            throw new RinProtocolException(
                    "invalid_response",
                    "Control Daemon response must be an array");
        }
        List<Map<String, Object>> copy = new ArrayList<>(list.size());
        for (Object item : list) copy.add(asObject(item));
        return Collections.unmodifiableList(copy);
    }

    private static String normalizeBaseUrl(String value) {
        String raw = value == null || value.isBlank() ? DEFAULT_BASE_URL : value.strip();
        while (raw.endsWith("/")) raw = raw.substring(0, raw.length() - 1);
        final URI uri;
        try {
            uri = new URI(raw);
        } catch (URISyntaxException exception) {
            throw new RinConfigurationException(
                    "invalid_base_url",
                    "Control Daemon URL must be a plain loopback HTTP origin",
                    exception);
        }
        String path = uri.getRawPath();
        if (!"http".equalsIgnoreCase(uri.getScheme()) ||
                uri.getHost() == null ||
                uri.getPort() < 1 ||
                uri.getRawUserInfo() != null ||
                uri.getRawQuery() != null ||
                uri.getRawFragment() != null ||
                (path != null && !path.isEmpty() && !"/".equals(path)) ||
                !isLoopback(uri.getHost())) {
            throw new RinConfigurationException(
                    "invalid_base_url",
                    "Control Daemon URL must be a plain loopback HTTP origin with an explicit port");
        }
        return "http://" + uri.getRawAuthority();
    }

    private static String validateToken(String value) {
        String token = value == null ? "" : value;
        if (token.length() > 4096 ||
                !token.equals(token.strip()) ||
                token.indexOf('\0') >= 0 ||
                token.indexOf('\r') >= 0 ||
                token.indexOf('\n') >= 0 ||
                token.getBytes(StandardCharsets.UTF_8).length < 32) {
            throw new RinConfigurationException(
                    "invalid_token",
                    "Control token must be a bounded single-line value containing at least 32 bytes");
        }
        return token;
    }

    private static boolean isLoopback(String value) {
        String host = value.toLowerCase(Locale.ROOT);
        if (host.equals("localhost") || host.equals("::1") || host.equals("0:0:0:0:0:0:0:1")) {
            return true;
        }
        String[] octets = host.split("\\.", -1);
        if (octets.length != 4) return false;
        try {
            if (Integer.parseInt(octets[0]) != 127) return false;
            for (String octet : octets) {
                int number = Integer.parseInt(octet);
                if (number < 0 || number > 255) return false;
            }
            return true;
        } catch (NumberFormatException ignored) {
            return false;
        }
    }

    private static String errorCode(int status) {
        if (status == 400) return "invalid";
        if (status == 401 || status == 403) return "forbidden";
        if (status == 404) return "not_found";
        if (status == 409) return "conflict";
        if (status == 410) return "unavailable";
        if (status == 429) return "capacity";
        return "unavailable";
    }

    private static boolean hasUnpairedSurrogate(String value) {
        for (int index = 0; index < value.length(); index++) {
            char current = value.charAt(index);
            if (Character.isHighSurrogate(current)) {
                if (index + 1 >= value.length() ||
                        !Character.isLowSurrogate(value.charAt(index + 1))) return true;
                index++;
            } else if (Character.isLowSurrogate(current)) {
                return true;
            }
        }
        return false;
    }

    private static Throwable unwrap(Throwable failure) {
        return failure instanceof CompletionException && failure.getCause() != null
                ? failure.getCause()
                : failure;
    }

    private static final class BoundedBodySubscriber
            implements HttpResponse.BodySubscriber<byte[]> {
        private final int maximum;
        private final ByteArrayOutputStream output;
        private final CompletableFuture<byte[]> body = new CompletableFuture<>();
        private Flow.Subscription subscription;
        private boolean done;

        private BoundedBodySubscriber(int maximum) {
            this.maximum = maximum;
            this.output = new ByteArrayOutputStream(Math.min(maximum, 8192));
        }

        @Override
        public CompletionStage<byte[]> getBody() {
            return body;
        }

        @Override
        public void onSubscribe(Flow.Subscription value) {
            if (subscription != null) {
                value.cancel();
                return;
            }
            subscription = Objects.requireNonNull(value, "subscription");
            subscription.request(1);
        }

        @Override
        public void onNext(List<ByteBuffer> buffers) {
            if (done) return;
            for (ByteBuffer buffer : buffers) {
                int count = buffer.remaining();
                if ((long) output.size() + count > maximum) {
                    fail(new RinProtocolException(
                            "response_too_large",
                            "Control Daemon response exceeds the configured limit"));
                    return;
                }
                byte[] chunk = new byte[count];
                buffer.get(chunk);
                output.write(chunk, 0, count);
            }
            subscription.request(1);
        }

        @Override
        public void onError(Throwable failure) {
            if (done) return;
            done = true;
            body.completeExceptionally(failure);
        }

        @Override
        public void onComplete() {
            if (done) return;
            done = true;
            body.complete(output.toByteArray());
        }

        private void fail(RuntimeException failure) {
            if (done) return;
            done = true;
            subscription.cancel();
            body.completeExceptionally(failure);
        }
    }
}
