# Rin Java SDK

[English](README.md) | [简体中文](README.zh-CN.md)

要求 Java 17+。Transport 使用 JDK `HttpClient`；JSON 通过接口注入，因此
游戏可以复用已有 Gson、Jackson 或引擎 Codec，不会产生第二套依赖图。

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

`JsonCodec.decodeObject` 必须拒绝非 Object 根节点。调用返回
`CompletableFuture`；Minecraft 或其他引擎状态修改必须重新安排到引擎
拥有的游戏线程。

使用 JDK 17 编译 SDK 和无依赖 Smoke Test：

```bash
make test-sdk-java
```
