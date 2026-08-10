package io.github.sunrioa.rin;

import java.io.IOException;
import java.util.LinkedHashMap;
import java.util.Map;
import java.util.Objects;
import java.util.Set;
import java.util.function.LongSupplier;

/**
 * Engine-neutral lease and operation client for {@code rin.control/v1}.
 * Game state must still be published and mutated by the engine adapter.
 */
public final class HostControlSession {
    public static final String CONTROL_CONTRACT = "rin.control/v1";
    private static final int MIN_LEASE_MILLIS = 5_000;
    private static final int MAX_LEASE_MILLIS = 60_000;
    private static final long MAX_JSON_SAFE_INTEGER = 9_007_199_254_740_991L;
    private static final Set<String> LEASE_KEYS = Set.of(
            "host_id", "instance_id", "lease_id", "expires_at_unix_millis");
    private static final Set<String> RUN_REQUIRED_KEYS = Set.of(
            "operation_id", "status", "progress_seq", "progress", "updated_at");
    private static final Set<String> RUN_ALLOWED_KEYS = Set.of(
            "operation_id", "status", "progress_seq", "progress", "updated_at", "message");
    private static final Set<String> RUN_STATUSES = Set.of(
            "queued", "running", "succeeded", "failed", "cancelled",
            "interrupted", "stale", "outcome-unknown");
    private static final Set<String> OUTCOME_REQUIRED_KEYS = Set.of(
            "operation_id", "status", "summary", "epoch", "world_seq", "occurred_at");
    private static final Set<String> OUTCOME_ALLOWED_KEYS = Set.of(
            "operation_id", "status", "code", "summary", "epoch", "world_seq",
            "occurred_at", "output");
    private static final Set<String> TERMINAL_STATUSES = Set.of(
            "succeeded", "failed", "cancelled", "interrupted", "stale",
            "outcome-unknown");
    private static final Set<String> TIMEPOINT_KEYS = Set.of("clock", "value");
    private static final Set<String> GATEWAY_RESULT_REQUIRED_KEYS = Set.of(
            "gateway_request_id");
    private static final Set<String> GATEWAY_RESULT_ALLOWED_KEYS = Set.of(
            "gateway_request_id", "binding", "snapshot", "error_code", "error_message");

    private final HostControlTransport transport;
    private final LongSupplier now;
    private final String instanceId;
    private final Map<String, Object> manifest;
    private final int leaseTtlMillis;
    private String hostId;
    private String leaseId;
    private long leaseExpiresAtMillis;
    private long lastLeaseRenewalAtMillis;

    public HostControlSession(
            HostControlTransport transport,
            LongSupplier now,
            String instanceId,
            Map<String, ?> manifest,
            int leaseTtlMillis) {
        this.transport = Objects.requireNonNull(transport, "transport");
        this.now = Objects.requireNonNull(now, "now");
        this.instanceId = identifier(instanceId, "instanceId");
        if (manifest == null || manifest.isEmpty()) {
            throw new IllegalArgumentException("Host manifest is required");
        }
        this.manifest = copyObject(manifest);
        if (leaseTtlMillis < MIN_LEASE_MILLIS || leaseTtlMillis > MAX_LEASE_MILLIS) {
            throw new IllegalArgumentException("Host lease TTL must be between 5 and 60 seconds");
        }
        this.leaseTtlMillis = leaseTtlMillis;
    }

    public synchronized void publish(String requestedHostId, Map<String, ?> publication)
            throws IOException, InterruptedException {
        String validatedHostId = identifier(requestedHostId, "hostId");
        if (publication == null || publication.isEmpty()) {
            throw new IllegalArgumentException("Host publication is required");
        }
        synchronized (this) {
            if (hostId != null && !hostId.equals(validatedHostId)) clearLeaseLocked();
        }
        ensureLease(validatedHostId);
        transport.post("/control/v1/publish", mapOf(
                "host_id", hostId(),
                "lease_id", leaseId(),
                "publication", copyObject(publication)));
    }

    public synchronized Map<String, Object> poll(int limit, int waitMillis)
            throws IOException, InterruptedException {
        if (limit < 1 || limit > 64 || waitMillis < 0 || waitMillis > 25_000) {
            throw new IllegalArgumentException("Invalid Host poll bounds");
        }
        ensureCurrentLease();
        return transport.post("/control/v1/poll", mapOf(
                "host_id", hostId(),
                "lease_id", leaseId(),
                "limit", limit,
                "wait_millis", waitMillis));
    }

    public synchronized void acknowledge(
            String operationId,
            boolean accepted,
            String code,
            String message) throws IOException, InterruptedException {
        ensureCurrentLease();
        Map<String, Object> acknowledgement = mapOf(
                "operation_id", identifier(operationId, "operationId"),
                "accepted", accepted);
        if (!accepted) {
            acknowledgement.put("code", identifier(code, "code"));
            acknowledgement.put("message", boundedText(message, 500, "message"));
        }
        transport.post("/control/v1/ack", mapOf(
                "host_id", hostId(),
                "lease_id", leaseId(),
                "acknowledgement", acknowledgement));
    }

    public synchronized void reportRun(Map<String, ?> run)
            throws IOException, InterruptedException {
        ensureCurrentLease();
        requireShape(run, RUN_REQUIRED_KEYS, RUN_ALLOWED_KEYS, "run");
        identifier(inputText(run.get("operation_id"), "operation_id"), "operationId");
        requireStatus(run.get("status"), RUN_STATUSES, "run status");
        inputWhole(run.get("progress_seq"), 1, MAX_JSON_SAFE_INTEGER, "progress_seq");
        inputWhole(run.get("progress"), 0, 100, "progress");
        validateTimepoint(run.get("updated_at"), "updated_at");
        if (run.containsKey("message")) {
            boundedText(inputText(run.get("message"), "message"), 500, "message");
        }
        transport.post("/control/v1/run", mapOf(
                "host_id", hostId(),
                "lease_id", leaseId(),
                "run", copyObject(run)));
    }

    public synchronized void reportOutcome(Map<String, ?> outcomeWithOptionalOutput)
            throws IOException, InterruptedException {
        ensureCurrentLease();
        requireShape(outcomeWithOptionalOutput, OUTCOME_REQUIRED_KEYS,
                OUTCOME_ALLOWED_KEYS, "outcome");
        identifier(inputText(outcomeWithOptionalOutput.get("operation_id"), "operation_id"),
                "operationId");
        requireStatus(outcomeWithOptionalOutput.get("status"),
                TERMINAL_STATUSES, "outcome status");
        boundedText(inputText(outcomeWithOptionalOutput.get("summary"), "summary"),
                1_000, "summary");
        if (outcomeWithOptionalOutput.containsKey("code")) {
            identifier(inputText(outcomeWithOptionalOutput.get("code"), "code"), "code");
        }
        inputObject(outcomeWithOptionalOutput.get("epoch"), "epoch");
        inputWhole(outcomeWithOptionalOutput.get("world_seq"),
                0, MAX_JSON_SAFE_INTEGER, "world_seq");
        validateTimepoint(outcomeWithOptionalOutput.get("occurred_at"), "occurred_at");
        Map<String, Object> outcome = new LinkedHashMap<>(
                copyObject(outcomeWithOptionalOutput));
        Object output = outcome.remove("output");
        Map<String, Object> request = mapOf(
                "host_id", hostId(),
                "lease_id", leaseId(),
                "outcome", outcome);
        if (output != null) {
            if (!(output instanceof Map<?, ?> map)) {
                throw new IllegalArgumentException("Host outcome output must be an object");
            }
            request.put("output", copyObject(map));
        }
        transport.post("/control/v1/outcome", request);
    }

    /**
     * Reports one authoritative bind or snapshot result returned from
     * {@link #poll(int, int)} under {@code gateway_requests}. The Host must
     * cache results by {@code gateway_request_id} so redelivery does not bind
     * the same request twice.
     */
    public synchronized void reportGatewayResult(Map<String, ?> result)
            throws IOException, InterruptedException {
        ensureCurrentLease();
        requireShape(result, GATEWAY_RESULT_REQUIRED_KEYS,
                GATEWAY_RESULT_ALLOWED_KEYS, "gateway result");
        identifier(inputText(result.get("gateway_request_id"),
                "gateway_request_id"), "gatewayRequestId");
        boolean hasBinding = result.containsKey("binding");
        boolean hasSnapshot = result.containsKey("snapshot");
        boolean hasError = result.containsKey("error_code");
        if ((hasBinding ? 1 : 0) + (hasSnapshot ? 1 : 0) + (hasError ? 1 : 0) != 1) {
            throw new IllegalArgumentException(
                    "Host gateway result must contain exactly one result variant");
        }
        if (hasBinding) inputObject(result.get("binding"), "binding");
        if (hasSnapshot) inputObject(result.get("snapshot"), "snapshot");
        if (hasError) {
            identifier(inputText(result.get("error_code"), "error_code"), "errorCode");
            boundedText(inputText(result.get("error_message"), "error_message"),
                    500, "errorMessage");
        } else if (result.containsKey("error_message")) {
            throw new IllegalArgumentException("Host gateway error_message requires error_code");
        }
        transport.post("/control/v1/gateway-result", mapOf(
                "host_id", hostId(),
                "lease_id", leaseId(),
                "result", copyObject(result)));
    }

    public synchronized void unregister() throws IOException, InterruptedException {
        String currentHost;
        String currentLease;
        synchronized (this) {
            if (leaseId == null) return;
            currentHost = hostId;
            currentLease = leaseId;
        }
        try {
            transport.post("/control/v1/unregister", mapOf(
                    "host_id", currentHost,
                    "lease_id", currentLease));
        } finally {
            invalidateLease();
        }
    }

    public synchronized void invalidateLease() {
        clearLeaseLocked();
    }

    public synchronized LeaseHealth health() {
        long currentTime = now.getAsLong();
        return new LeaseHealth(
                leaseId != null && leaseExpiresAtMillis > currentTime,
                lastLeaseRenewalAtMillis,
                Math.max(0, leaseExpiresAtMillis - currentTime));
    }

    private synchronized void ensureLease(String requestedHostId)
            throws IOException, InterruptedException {
        long currentTime = now.getAsLong();
        synchronized (this) {
            if (leaseId != null && leaseExpiresAtMillis > currentTime
                    && leaseExpiresAtMillis - currentTime > leaseTtlMillis / 3L) {
                return;
            }
        }
        if (!leased()) {
            register(requestedHostId);
            return;
        }
        readLease(transport.post("/control/v1/renew", mapOf(
                "host_id", hostId(),
                "lease_id", leaseId())), requestedHostId);
    }

    private void ensureCurrentLease() throws IOException, InterruptedException {
        if (hostId == null) {
            throw new IOException("Host Control lease is unavailable");
        }
        ensureLease(hostId);
    }

    private void register(String requestedHostId)
            throws IOException, InterruptedException {
        Map<String, Object> response = transport.post("/control/v1/register", mapOf(
                "contract_version", CONTROL_CONTRACT,
                "host_id", requestedHostId,
                "instance_id", instanceId,
                "manifest", manifest,
                "lease_ttl_millis", leaseTtlMillis));
        readLease(response, requestedHostId);
    }

    private synchronized void readLease(
            Map<String, Object> response,
            String expectedHostId) throws IOException {
        if (response == null || !response.keySet().equals(LEASE_KEYS)) {
            throw new IOException("Host Control returned an invalid lease");
        }
        String returnedHost = responseIdentifier(response.get("host_id"));
        String returnedInstance = responseIdentifier(response.get("instance_id"));
        String returnedLease = responseIdentifier(response.get("lease_id"));
        long expires = responseWhole(response.get("expires_at_unix_millis"));
        if (!returnedHost.equals(expectedHostId) || !returnedInstance.equals(instanceId)
                || expires <= now.getAsLong()) {
            throw new IOException("Host Control returned a mismatched lease");
        }
        hostId = returnedHost;
        leaseId = returnedLease;
        leaseExpiresAtMillis = expires;
        lastLeaseRenewalAtMillis = now.getAsLong();
    }

    private synchronized boolean leased() {
        return leaseId != null && leaseExpiresAtMillis > now.getAsLong();
    }

    private synchronized void requireLease() throws IOException {
        if (!leased()) throw new IOException("Host Control lease is unavailable");
    }

    private synchronized String hostId() throws IOException {
        requireLease();
        return hostId;
    }

    private synchronized String leaseId() throws IOException {
        requireLease();
        return leaseId;
    }

    private void clearLeaseLocked() {
        hostId = null;
        leaseId = null;
        leaseExpiresAtMillis = 0;
    }

    private static void requireShape(
            Map<String, ?> value,
            Set<String> required,
            Set<String> allowed,
            String field) {
        if (value == null || !value.keySet().containsAll(required)
                || !allowed.containsAll(value.keySet())) {
            throw new IllegalArgumentException("Host " + field + " has an invalid shape");
        }
    }

    private static void requireStatus(Object value, Set<String> allowed, String field) {
        String status = inputText(value, field);
        if (!allowed.contains(status)) {
            throw new IllegalArgumentException("Invalid Host " + field);
        }
    }

    private static void validateTimepoint(Object value, String field) {
        Map<String, Object> timepoint = inputObject(value, field);
        if (!timepoint.keySet().equals(TIMEPOINT_KEYS)) {
            throw new IllegalArgumentException("Invalid Host " + field);
        }
        identifier(inputText(timepoint.get("clock"), field + ".clock"), field + ".clock");
        inputWhole(timepoint.get("value"), 0, MAX_JSON_SAFE_INTEGER, field + ".value");
    }

    private static String identifier(String value, String field) {
        if (value == null || value.length() > 96
                || !value.matches("[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*")) {
            throw new IllegalArgumentException("Invalid Host " + field);
        }
        return value;
    }

    private static String boundedText(String value, int maxCodePoints, String field) {
        if (value == null || value.isBlank()
                || value.codePointCount(0, value.length()) > maxCodePoints
                || value.codePoints().anyMatch(Character::isISOControl)) {
            throw new IllegalArgumentException("Invalid Host " + field);
        }
        return value;
    }

    private static String inputText(Object value, String field) {
        if (value instanceof String text && !text.isBlank()) return text;
        throw new IllegalArgumentException("Invalid Host " + field);
    }

    private static long inputWhole(Object value, long minimum, long maximum, String field) {
        if (value instanceof Number number
                && number.doubleValue() == number.longValue()
                && number.longValue() >= minimum
                && number.longValue() <= maximum) {
            return number.longValue();
        }
        throw new IllegalArgumentException("Invalid Host " + field);
    }

    @SuppressWarnings("unchecked")
    private static Map<String, Object> inputObject(Object value, String field) {
        if (!(value instanceof Map<?, ?> map)) {
            throw new IllegalArgumentException("Invalid Host " + field);
        }
        for (Object key : map.keySet()) {
            if (!(key instanceof String)) {
                throw new IllegalArgumentException("Invalid Host " + field);
            }
        }
        return (Map<String, Object>) map;
    }

    private static String responseIdentifier(Object value) throws IOException {
        if (value instanceof String text && text.length() <= 96
                && text.matches("[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*")) {
            return text;
        }
        throw new IOException("Host Control returned an invalid identifier");
    }

    private static long responseWhole(Object value) throws IOException {
        if (value instanceof Number number && number.doubleValue() == number.longValue()) {
            return number.longValue();
        }
        throw new IOException("Host Control returned invalid integer");
    }

    private static Map<String, Object> copyObject(Map<?, ?> value) {
        Map<String, Object> result = new LinkedHashMap<>();
        for (Map.Entry<?, ?> entry : value.entrySet()) {
            if (!(entry.getKey() instanceof String key)) {
                throw new IllegalArgumentException("Host JSON object contains a non-string key");
            }
            result.put(key, entry.getValue());
        }
        return PendingTurn.copyJsonObject(result);
    }

    private static Map<String, Object> mapOf(Object... entries) {
        Map<String, Object> result = new LinkedHashMap<>();
        for (int index = 0; index < entries.length; index += 2) {
            result.put((String) entries[index], entries[index + 1]);
        }
        return result;
    }

    public record LeaseHealth(
            boolean leased,
            long lastLeaseRenewalAtMillis,
            long leaseRemainingMillis) { }
}
