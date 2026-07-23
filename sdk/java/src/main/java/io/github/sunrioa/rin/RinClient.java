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
import java.util.Collections;
import java.util.List;
import java.util.LinkedHashMap;
import java.util.Map;
import java.util.Objects;
import java.util.Set;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.CompletionException;
import java.util.concurrent.CompletionStage;
import java.util.concurrent.Flow;
import java.util.concurrent.TimeUnit;
import java.util.function.Function;

public final class RinClient {
    public static final String PROTOCOL_VERSION = "rin.protocol/v1";
    public static final String DEFAULT_BASE_URL = "http://127.0.0.1:7374";
    public static final int DEFAULT_MAX_RESPONSE_BYTES = 2 * 1024 * 1024;

    private final String baseUrl;
    private final String token;
    private final Duration timeout;
    private final int maxResponseBytes;
    private final JsonCodec codec;
    private final HttpClient http;

    public RinClient(JsonCodec codec) {
        this(DEFAULT_BASE_URL, "", Duration.ofSeconds(5), DEFAULT_MAX_RESPONSE_BYTES, codec);
    }

    public RinClient(String baseUrl, String token, Duration timeout, int maxResponseBytes, JsonCodec codec) {
        this.token = validateToken(token);
        this.baseUrl = normalizeBaseUrl(baseUrl, this.token);
        this.timeout = Objects.requireNonNull(timeout, "timeout");
        if (timeout.compareTo(Duration.ofMillis(50)) < 0 || timeout.compareTo(Duration.ofSeconds(120)) > 0) {
            throw new RinConfigurationException("invalid_timeout", "Timeout must be between 50 ms and 120 seconds");
        }
        if (maxResponseBytes < 1024 || maxResponseBytes > 32 * 1024 * 1024) {
            throw new RinConfigurationException("invalid_response_limit", "Response limit must be between 1 KiB and 32 MiB");
        }
        this.maxResponseBytes = maxResponseBytes;
        this.codec = Objects.requireNonNull(codec, "codec");
        this.http = HttpClient.newBuilder()
                .connectTimeout(timeout)
                .followRedirects(HttpClient.Redirect.NEVER)
                .build();
    }

    public CompletableFuture<Map<String, Object>> health() {
        return request("GET", "/health", null, Set.of(200));
    }

    public CompletableFuture<Map<String, Object>> createSession(Map<String, ?> payload) {
        return post("/v1/session/create", payload, 200);
    }

    public CompletableFuture<Map<String, Object>> observe(Map<String, ?> payload) {
        return post("/v1/session/observe", payload, 200);
    }

    public CompletableFuture<Map<String, Object>> propose(Map<String, ?> payload) {
        return post("/v1/agent/propose", payload, 200);
    }

    public CompletableFuture<Map<String, Object>> submitProposalJob(Map<String, ?> payload) {
        return post("/v1/jobs/propose", payload, 202);
    }

    public CompletableFuture<Map<String, Object>> getProposalJob(String jobId) {
        return request("GET", "/v1/jobs/" + pathId(jobId), null, Set.of(200));
    }

    public CompletableFuture<Map<String, Object>> cancelProposalJob(String jobId) {
        return request("DELETE", "/v1/jobs/" + pathId(jobId), null, Set.of(200));
    }

    public CompletableFuture<Map<String, Object>> submitGenerationJob(Map<String, ?> payload) {
        return post("/v1/generation/jobs", payload, 202);
    }

    public CompletableFuture<Map<String, Object>> getGenerationJob(String jobId) {
        return request("GET", "/v1/generation/jobs/" + pathId(jobId), null, Set.of(200));
    }

    public CompletableFuture<Map<String, Object>> cancelGenerationJob(String jobId) {
        return request("DELETE", "/v1/generation/jobs/" + pathId(jobId), null, Set.of(200));
    }

    public CompletableFuture<Map<String, Object>> commit(Map<String, ?> payload) {
        return post("/v1/action/commit", payload, 200);
    }

    public CompletableFuture<Map<String, Object>> commitBatch(Map<String, ?> payload) {
        return post("/v1/action/commit-batch", payload, 200);
    }

    public CompletableFuture<Map<String, Object>> setActorActivity(Map<String, ?> payload) {
        return post("/v1/session/activity", payload, 200);
    }

    public CompletableFuture<Map<String, Object>> arbitrate(Map<String, ?> payload) {
        return post("/v1/world/arbitrate", payload, 200);
    }

    public CompletableFuture<Map<String, Object>> state(Map<String, ?> payload) {
        return post("/v1/session/get", payload, 200);
    }

    public CompletableFuture<Map<String, Object>> snapshot(Map<String, ?> payload) {
        return post("/v1/session/snapshot", payload, 200);
    }

    public CompletableFuture<Map<String, Object>> restore(Map<String, ?> payload) {
        return post("/v1/session/restore", payload, 200);
    }

    public CompletableFuture<Map<String, Object>> timeline(Map<String, ?> payload) {
        return post("/v1/session/timeline", payload, 200);
    }

    public CompletableFuture<Map<String, Object>> replay(Map<String, ?> payload) {
        return post("/v1/session/replay", payload, 200);
    }

    public CompletableFuture<Map<String, Object>> dueAgents(Map<String, ?> payload) {
        return post("/v1/scheduler/due", payload, 200);
    }

    public CompletableFuture<Map<String, Object>> waitForProposal(String jobId) {
        return waitForJob(jobId, this::getProposalJob, this::cancelProposalJob, Duration.ofSeconds(25), Duration.ofMillis(100));
    }

    public CompletableFuture<Map<String, Object>> waitForProposal(String jobId, Duration deadline, Duration interval) {
        return waitForJob(jobId, this::getProposalJob, this::cancelProposalJob, deadline, interval);
    }

    public CompletableFuture<Map<String, Object>> waitForGeneration(String jobId) {
        return waitForJob(jobId, this::getGenerationJob, this::cancelGenerationJob, Duration.ofSeconds(45), Duration.ofMillis(100));
    }

    public CompletableFuture<Map<String, Object>> waitForGeneration(String jobId, Duration deadline, Duration interval) {
        return waitForJob(jobId, this::getGenerationJob, this::cancelGenerationJob, deadline, interval);
    }

    private CompletableFuture<Map<String, Object>> waitForJob(
            String jobId,
            Function<String, CompletableFuture<Map<String, Object>>> getter,
            Function<String, CompletableFuture<Map<String, Object>>> canceler,
            Duration deadline,
            Duration interval) {
        if (deadline == null || interval == null || deadline.compareTo(Duration.ofMillis(50)) < 0 ||
                deadline.compareTo(Duration.ofMinutes(5)) > 0 || interval.compareTo(Duration.ofMillis(10)) < 0 ||
                interval.compareTo(Duration.ofSeconds(5)) > 0) {
            throw new RinConfigurationException("invalid_polling", "Job deadline or interval is out of range");
        }
        long expires = System.nanoTime() + deadline.toNanos();
        CompletableFuture<Map<String, Object>> result = new CompletableFuture<>();
        class Poller {
            void poll() {
                if (result.isDone()) return;
                getter.apply(jobId).whenComplete((job, failure) -> {
                    if (result.isDone()) return;
                    if (failure != null) {
                        result.completeExceptionally(unwrap(failure));
                        return;
                    }
                    String status = RinException.safeText(job.get("status"), 32, "");
                    if (status.equals("succeeded")) {
                        result.complete(job);
                        return;
                    }
                    if (status.equals("failed") || status.equals("stale") || status.equals("canceled")) {
                        Object value = job.get("error");
                        Map<?, ?> detail = value instanceof Map<?, ?> map ? map : Map.of();
                        result.completeExceptionally(new RinApiException(
                                RinException.safeText(detail.get("code"), 96, "job_" + status),
                                RinException.safeText(detail.get("message"), 500, "Rin job ended as " + status),
                                0,
                                ""));
                        return;
                    }
                    if (!status.equals("queued") && !status.equals("running")) {
                        result.completeExceptionally(new RinProtocolException("invalid_job", "Rin returned an unknown job status"));
                        return;
                    }
                    long remaining = expires - System.nanoTime();
                    if (remaining <= 0) {
                        try {
                            canceler.apply(jobId);
                        } catch (RinException ignored) {
                            // Timeout remains the useful result even if best-effort cancellation fails.
                        }
                        result.completeExceptionally(new RinApiException("job_timeout", "Rin job exceeded its deadline", 0, ""));
                        return;
                    }
                    long delay = Math.min(interval.toNanos(), remaining);
                    CompletableFuture.delayedExecutor(delay, TimeUnit.NANOSECONDS).execute(this::poll);
                });
            }
        }
        new Poller().poll();
        return result;
    }

    private CompletableFuture<Map<String, Object>> post(String path, Map<String, ?> payload, int expectedStatus) {
        return request("POST", path, Objects.requireNonNull(payload, "payload"), Set.of(expectedStatus));
    }

    private CompletableFuture<Map<String, Object>> request(
            String method,
            String path,
            Map<String, ?> payload,
            Set<Integer> expectedStatuses) {
        if (!path.startsWith("/") || path.contains("//") || path.contains("..")) {
            throw new RinConfigurationException("invalid_path", "Rin request path is invalid");
        }

        HttpRequest.BodyPublisher body = HttpRequest.BodyPublishers.noBody();
        HttpRequest.Builder builder = HttpRequest.newBuilder(URI.create(baseUrl + path))
                .timeout(timeout)
                .header("Accept", "application/json")
                .header("User-Agent", "rin-java/0.5");
        if (payload != null) {
            final String encoded;
            try {
                encoded = codec.encode(payload);
            } catch (Exception exception) {
                throw new RinProtocolException("invalid_request", "Rin payload is not JSON serializable", exception);
            }
            if (encoded == null) {
                throw new RinProtocolException("invalid_request", "JSON codec returned a null request");
            }
            body = HttpRequest.BodyPublishers.ofString(encoded, StandardCharsets.UTF_8);
            builder.header("Content-Type", "application/json; charset=utf-8");
        }
        if (!token.isEmpty()) builder.header("Authorization", "Bearer " + token);
        builder.method(method, body);

        CompletableFuture<HttpResponse<byte[]>> network = http.sendAsync(
                builder.build(),
                ignored -> new BoundedBodySubscriber(maxResponseBytes));
        CompletableFuture<HttpResponse<byte[]>> wire = new CompletableFuture<>();
        network.whenComplete((response, failure) -> {
            if (failure == null) wire.complete(response);
            else wire.completeExceptionally(unwrap(failure));
        });
        CompletableFuture.delayedExecutor(timeout.toMillis(), TimeUnit.MILLISECONDS).execute(() -> {
            if (wire.completeExceptionally(new HttpTimeoutException("Rin request timed out"))) {
                network.cancel(true);
            }
        });
        CompletableFuture<Map<String, Object>> result = new CompletableFuture<>();
        wire.whenComplete((response, failure) -> {
            if (failure != null) {
                Throwable cause = unwrap(failure);
                if (cause instanceof RinException) {
                    result.completeExceptionally(cause);
                } else if (cause instanceof HttpTimeoutException) {
                    result.completeExceptionally(new RinTransportException(
                            "transport_timeout", "Rin request timed out", cause));
                } else {
                    result.completeExceptionally(new RinTransportException(
                            "transport_failed", "Rin is unavailable", cause));
                }
                return;
            }
            try {
                result.complete(decodeResponse(response, expectedStatuses));
            } catch (RuntimeException exception) {
                result.completeExceptionally(exception);
            }
        });
        result.whenComplete((ignored, failure) -> {
            if (result.isCancelled()) {
                wire.cancel(true);
                network.cancel(true);
            }
        });
        return result;
    }

    private Map<String, Object> decodeResponse(HttpResponse<byte[]> response, Set<Integer> expectedStatuses) {
        int status = response.statusCode();
        if (status >= 300 && status < 400) {
            throw new RinTransportException("redirect_rejected", "Rin endpoint attempted to redirect");
        }
        String contentLength = response.headers().firstValue("Content-Length").orElse(null);
        if (contentLength != null) {
            final long declared;
            try {
                declared = Long.parseLong(contentLength);
            } catch (NumberFormatException exception) {
                throw new RinProtocolException("invalid_response", "Rin returned an invalid Content-Length", exception);
            }
            if (declared < 0 || declared > maxResponseBytes) {
                throw new RinProtocolException("response_too_large", "Rin response exceeds the configured limit");
            }
        }

        byte[] bytes = response.body();

        final String json;
        try {
            json = StandardCharsets.UTF_8.newDecoder()
                    .onMalformedInput(CodingErrorAction.REPORT)
                    .onUnmappableCharacter(CodingErrorAction.REPORT)
                    .decode(ByteBuffer.wrap(bytes))
                    .toString();
        } catch (CharacterCodingException exception) {
            throw new RinProtocolException("invalid_response", "Rin returned invalid UTF-8", exception);
        }

        final Map<String, Object> envelope;
        try {
            envelope = codec.decodeObject(json);
        } catch (Exception exception) {
            if (!expectedStatuses.contains(status)) {
                throw new RinApiException("http_error", "Rin request failed", status, "");
            }
            throw new RinProtocolException("invalid_response", "Rin returned invalid JSON", exception);
        }
        if (envelope == null) {
            throw new RinProtocolException("invalid_response", "Rin response must be an object");
        }
        if (!expectedStatuses.contains(status) || !Boolean.TRUE.equals(envelope.get("ok"))) {
            throw apiError(envelope, status);
        }
        Object data = envelope.get("data");
        if (!(data instanceof Map<?, ?> map)) {
            throw new RinProtocolException("invalid_response", "Rin response data must be an object");
        }
        Map<String, Object> result = new LinkedHashMap<>();
        for (Map.Entry<?, ?> entry : map.entrySet()) {
            if (!(entry.getKey() instanceof String key)) {
                throw new RinProtocolException("invalid_response", "Rin response data contains a non-string key");
            }
            result.put(key, entry.getValue());
        }
        return Collections.unmodifiableMap(result);
    }

    private static RinApiException apiError(Map<String, Object> envelope, int status) {
        Object value = envelope.get("error");
        Map<?, ?> detail = value instanceof Map<?, ?> map ? map : Map.of();
        return new RinApiException(
                RinException.safeText(detail.get("code"), 96, "http_error"),
                RinException.safeText(detail.get("message"), 500, "Rin request failed"),
                status,
                RinException.safeText(detail.get("field"), 160, ""));
    }

    private static String normalizeBaseUrl(String value, String validatedToken) {
        String raw = value == null || value.isBlank() ? DEFAULT_BASE_URL : value.strip();
        while (raw.endsWith("/")) raw = raw.substring(0, raw.length() - 1);
        final URI uri;
        try {
            uri = new URI(raw);
        } catch (URISyntaxException exception) {
            throw new RinConfigurationException("invalid_base_url", "Rin base URL must be an origin", exception);
        }
        String scheme = uri.getScheme();
        String host = uri.getHost();
        String path = uri.getRawPath();
        if (!("http".equals(scheme) || "https".equals(scheme)) || host == null || host.isEmpty() ||
                uri.getRawUserInfo() != null || uri.getRawQuery() != null || uri.getRawFragment() != null ||
                (path != null && !path.isEmpty() && !"/".equals(path)) || uri.getPort() > 65535) {
            throw new RinConfigurationException("invalid_base_url", "Rin base URL must be an origin");
        }
        boolean loopback = isLoopback(host);
        if ("http".equals(scheme) && !loopback) {
            throw new RinConfigurationException("insecure_base_url", "Remote Rin endpoints must use HTTPS");
        }
        if (!loopback && validatedToken.isEmpty()) {
            throw new RinConfigurationException("missing_token", "Remote Rin endpoints require a token");
        }
        return uri.getScheme() + "://" + uri.getRawAuthority();
    }

    private static boolean isLoopback(String value) {
        String host = value.toLowerCase();
        if (host.startsWith("[") && host.endsWith("]")) host = host.substring(1, host.length() - 1);
        if (host.equals("localhost") || host.equals("::1") || host.equals("0:0:0:0:0:0:0:1")) return true;
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

    private static String validateToken(String value) {
        String candidate = value == null ? "" : value;
        if (candidate.length() > 4096 || !candidate.equals(candidate.strip()) ||
                candidate.indexOf('\0') >= 0 || candidate.indexOf('\r') >= 0 || candidate.indexOf('\n') >= 0) {
            throw new RinConfigurationException("invalid_token", "Rin token must be a bounded single-line value");
        }
        return candidate;
    }

    private static String pathId(String value) {
        if (value == null || value.isEmpty() || value.length() > 96 || !value.matches("[A-Za-z0-9._-]+")) {
            throw new RinConfigurationException("invalid_identifier", "Rin path identifier is invalid");
        }
        return value;
    }

    private static Throwable unwrap(Throwable failure) {
        return failure instanceof CompletionException && failure.getCause() != null ? failure.getCause() : failure;
    }

    private static final class BoundedBodySubscriber implements HttpResponse.BodySubscriber<byte[]> {
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
            try {
                for (ByteBuffer buffer : buffers) {
                    int count = buffer.remaining();
                    if ((long) output.size() + count > maximum) {
                        fail(new RinProtocolException(
                                "response_too_large", "Rin response exceeds the configured limit"));
                        return;
                    }
                    byte[] chunk = new byte[count];
                    buffer.get(chunk);
                    output.write(chunk, 0, count);
                }
                subscription.request(1);
            } catch (RuntimeException exception) {
                fail(exception);
            }
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
