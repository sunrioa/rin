# Rin Java SDK

[English](README.md) | [简体中文](README.zh-CN.md)

Requires Java 17+. Transport uses the JDK `HttpClient`; JSON is injected so a
game can reuse its existing Gson, Jackson, or engine codec without creating a
second dependency graph.

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

`JsonCodec.decodeObject` must reject a non-object root. Calls return
`CompletableFuture`; schedule any Minecraft or other engine mutation back on
the owning game thread.

Compile the SDK and its dependency-free smoke test with JDK 17:

```bash
make test-sdk-java
```
