package io.github.sunrioa.rin;

import java.io.IOException;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;

final class HostControlSessionTest {
    private HostControlSessionTest() { }

    static void run() throws Exception {
        long[] now = {1_000_000L};
        List<String> paths = new ArrayList<>();
        List<Map<String, ?>> bodies = new ArrayList<>();
        HostControlTransport transport = (path, body) -> {
            paths.add(path);
            bodies.add(body);
            if (path.equals("/control/v2/host/register")
                    || path.equals("/control/v2/host/renew")) {
                return Map.of(
                        "host_id", "host.fixture",
                        "instance_id", "instance.fixture",
                        "lease_id", "lease.fixture",
                        "expires_at_unix_millis", now[0] + 15_000L);
            }
            if (path.equals("/control/v2/host/poll")) {
                return Map.of(
                        "gateway_requests", List.of(),
                        "requests", List.of(),
                        "cancellations", List.of());
            }
            return Map.of();
        };
        HostControlSession session = new HostControlSession(
                transport,
                () -> now[0],
                "instance.fixture",
                Map.of(
                        "contract_version", "rin.host/v1",
                        "adapter_id", "fixture.adapter"),
                15_000);

        session.publish("host.fixture", Map.of("worlds", List.of()));
        require(paths.equals(List.of(
                        "/control/v2/host/register", "/control/v2/host/publish")),
                "Host registration did not precede publication");
        require(session.health().leased(), "Host lease was not recorded");
        session.poll(8, 0);
        session.acknowledge("operation.fixture", true, "", "");
        session.reportRun(Map.of(
                "operation_id", "operation.fixture",
                "status", "running",
                "progress_seq", 1L,
                "progress", 10,
                "updated_at", Map.of("clock", "step", "value", 2L)));
        session.reportOutcome(Map.of(
                "operation_id", "operation.fixture",
                "status", "succeeded",
                "summary", "Applied one fixture action.",
                "epoch", Map.of(
                        "session_id", "session.fixture",
                        "world_id", "world.fixture",
                        "host", 1L,
                        "world", 1L,
                        "timeline", 1L),
                "world_seq", 2L,
                "occurred_at", Map.of("clock", "step", "value", 3L),
                "output", Map.of("effect_state", "applied")));
        session.reportGatewayResult(Map.of(
                "gateway_request_id", "gateway.fixture",
                "snapshot", Map.of(
                        "now", Map.of("clock", "step", "value", 3L),
                        "epoch", Map.of(
                                "session_id", "session.fixture",
                                "world_id", "world.fixture",
                                "host", 1L,
                                "world", 1L,
                                "timeline", 1L),
                        "observation_sequence", 2L)));
        require(paths.containsAll(List.of(
                        "/control/v2/host/poll", "/control/v2/host/ack",
                        "/control/v2/host/run", "/control/v2/host/outcome",
                        "/control/v2/host/gateway-result")),
                "Host operation lifecycle was incomplete");
        Map<String, ?> outcomeRequest = bodies.get(
                paths.indexOf("/control/v2/host/outcome"));
        require(outcomeRequest.get("output") instanceof Map<?, ?>
                        && outcomeRequest.get("outcome") instanceof Map<?, ?> outcome
                        && !outcome.containsKey("output"),
                "Host output was not separated from the authoritative outcome");
        requireRejected(() -> session.reportRun(Map.of(
                "operation_id", "operation.fixture",
                "status", "running",
                "progress_seq", 2L,
                "progress", 101,
                "updated_at", Map.of("clock", "step", "value", 3L))));
        requireRejected(() -> session.reportOutcome(Map.of(
                "operation_id", "operation.fixture",
                "status", "running",
                "summary", "Not terminal.",
                "epoch", Map.of(),
                "world_seq", 3L,
                "occurred_at", Map.of("clock", "step", "value", 3L))));
        requireRejected(() -> session.reportGatewayResult(Map.of(
                "gateway_request_id", "gateway.fixture")));

        now[0] += 11_000L;
        session.poll(8, 0);
        require(paths.get(paths.size() - 2).equals("/control/v2/host/renew")
                        && paths.get(paths.size() - 1).equals("/control/v2/host/poll"),
                "Polling did not renew an expiring Host lease");
        session.unregister();
        require(!session.health().leased() && paths.get(paths.size() - 1)
                        .equals("/control/v2/host/unregister"),
                "Host lease was not cleared on unregister");

        requireRejected(() -> session.poll(0, 0));
        requireRejected(() -> new HostControlSession(
                transport, () -> now[0], "bad instance", Map.of("x", true), 15_000));
    }

    private static void requireRejected(ThrowingAction action) {
        try {
            action.run();
            throw new AssertionError("Invalid Host Control input was accepted");
        } catch (IllegalArgumentException | IOException expected) {
            // Expected.
        } catch (Exception unexpected) {
            throw new AssertionError("Unexpected Host Control failure", unexpected);
        }
    }

    private static void require(boolean condition, String message) {
        if (!condition) throw new AssertionError(message);
    }

    @FunctionalInterface
    private interface ThrowingAction {
        void run() throws Exception;
    }
}
