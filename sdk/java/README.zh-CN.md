# Rin Java SDK

[English](README.md) | [简体中文](README.zh-CN.md)

面向 Java 17+、支持注入 JSON 边界的异步客户端。

Transport 使用 JDK `HttpClient`；游戏可以复用已有 JSON Codec，不会产生
第二套依赖图。

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
拥有的游戏线程。配置的 Deadline 直接使用 JDK `HttpRequest.timeout`；取消返回的
Future 会取消同一个 Network Future，不会另留第二个 Delayed Timeout Task。
本 Package 只实现 `transport` Profile；大 lineage Session Transfer 需要
`streaming` SDK Target。

SDK 与具体引擎无关。`HostControlSession` 通过注入的 `HostControlTransport`
实现通用 `rin.control/v1` Host 租约与 Operation 生命周期，包括注册、发布、
Poll、ACK、进度、权威 Outcome 和注销。Minecraft、RPG、视觉小说或其他接入方
自行提供 Manifest、Observation、游戏线程执行器及 JSON/HTTP Transport。

`WorkflowCoordinator` 负责可复用的 Pending Turn、Job 恢复、结算与 Outcome
Outbox 状态机。接入方提供持久 `WorkflowStore` 和已校验的
`HostDurability`。`idempotent-action` Apply Callback 会收到稳定 Operation
ID；`transactional-action` 会把 Apply 与入队交给同一个宿主事务。所有错误都
保留精确 Action Report，绝不把它转换为 Observation。只有目标 Session 的完整
`MutationResult` 才算 ACK：Revision 为正的 JSON-safe 整数、Head 为小写
SHA-256，且 Duplicate 显式为布尔值；确认缺失、不完整或 Session 不一致时，
会在调用 Store 回调前失败并保留待发送项。
`ProposalFreshness.evaluate` 负责统一的最终
Pending/Revision 校验。`advisory` 宿主不能提供要求更强 Profile 的动作。参见
[宿主持久保证分级](../../docs/host-durability.zh-CN.md)。

使用 JDK 17 编译 SDK 和无依赖 Smoke Test：

```bash
make test-sdk-java
```
