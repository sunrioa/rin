# Rin Java SDK

[English](README.md) | [简体中文](README.zh-CN.md)

Java 17 source-first SDK 包含三部分：

- `RinControlClient`：异步 `rin.control/v2` 客户端；
- 同一客户端中的 `/plans/v1/*` 任务计划原始 JSON 方法；
- `RinAgentClient`：异步内部 Agent Task API 客户端；
- `HostActionContract` / `HostControlSession`：游戏 Adapter 的 V2 Host 辅助边界。

当前仓库没有承诺已发布 Maven Artifact。请固定源码 Revision，并把
`src/main/java` 编入你的项目。

```java
JsonValueCodec codec = new YourJsonCodec();
RinControlClient control = new RinControlClient(
    System.getenv("RIN_CONTROL_TOKEN"), codec);

control.info().thenAccept(System.out::println).join();
control.listWorlds().thenAccept(System.out::println).join();
```

`JsonValueCodec` 由游戏选择已经使用的 JSON 库实现，Rin 不额外绑定 Jackson、
Gson 或引擎专用序列化器。Control Client 使用标准 `java.net.http.HttpClient`，
禁止 Redirect，限制响应体和超时，并返回 `CompletableFuture`。

`HostControlSession` 只负责连接 Control Daemon 和传递 V2 Host 数据；目标解析、
Effect Preview、主线程执行、取消和结果验证仍必须由游戏 Adapter 实现。
