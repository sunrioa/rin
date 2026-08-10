# Rin Java SDK

[English](README.md) | [简体中文](README.zh-CN.md)

An asynchronous client for Java 17+ with an injectable JSON boundary.

Transport uses the JDK `HttpClient`; a game can reuse its existing JSON codec
without creating a second dependency graph.

```java
JsonCodec codec = new GsonJsonCodec(gameGson);
RinClient rin = new RinClient(
    "http://127.0.0.1:7374",
    System.getenv().getOrDefault("RIN_TOKEN", ""),
    Duration.ofSeconds(5),
    RinClient.DEFAULT_MAX_RESPONSE_BYTES,
    codec
);

rin.health().thenAccept(data -> System.out.println(data.get("status")));
```

External controllers use `RinControlClient` with a host-supplied
`JsonValueCodec` capable of decoding both JSON objects and arrays:

```java
RinControlClient control = new RinControlClient(
        "a-local-secret-containing-at-least-32-bytes",
        jsonValueCodec);
control.listWorlds().thenAccept(System.out::println).join();
```

It covers actor observation, capability discovery, controller leases, action
submission and confirmation, Operation wait/cancel, and emergency stop. It is
loopback-only, requires a token, and treats only a terminal Operation with a
Host Outcome as proof of execution.

`JsonCodec.decodeObject` must reject a non-object root. Calls return
`CompletableFuture`; schedule any Minecraft or other engine mutation back on
the owning game thread. The configured deadline is the JDK
`HttpRequest.timeout`; canceling the returned future cancels that same network
future, with no second delayed timeout task. This package implements the
`transport` profile only; large-lineage Session Transfer requires a
`streaming` SDK target.

The SDK is engine-neutral. `HostControlSession` implements the generic
`rin.control/v2` Host lease and operation lifecycle over an injected
`HostControlTransport`: register, publish, poll, acknowledge, report progress,
report an authoritative outcome, and unregister. Minecraft, RPG, visual novel,
or other adapters provide their own manifest, observations, game-thread
executor, and JSON/HTTP transport.

`WorkflowCoordinator` owns the reusable Pending Turn, Job recovery, settlement,
and Outcome Outbox state machine. Supply a persistent `WorkflowStore` and a
validated `HostDurability` value. `idempotent-action` apply callbacks receive
the stable operation ID; `transactional-action` delegates apply and enqueue to
one host transaction. Every error preserves the exact Action Report; it is
never converted into an Observation. Only a complete same-Session
`MutationResult` with a positive JSON-safe revision, lowercase SHA-256 head and
explicit boolean duplicate flag is accepted; a missing, partial or
crossed-Session acknowledgement fails closed before the Store callback.
`ProposalFreshness.evaluate` performs the shared final
pending/revision check. An `advisory` host cannot offer actions that require
either stronger profile. See
[Host durability profiles](../../docs/host-durability.md).

Compile the SDK and its dependency-free smoke test with JDK 17:

```bash
make test-sdk-java
```
