# Rin Java SDK

[English](README.md) | [简体中文](README.zh-CN.md)

The source-first Java 17 SDK contains four surfaces:

- `RinControlClient`: asynchronous `rin.control/v2` client;
- `/plans/v1/*` task-plan methods on the same client using raw JSON maps;
- `RinAgentClient`: asynchronous internal Agent Task API client;
- `HostActionContract` and `HostControlSession`: V2 Host adapter helpers.

This repository does not promise a published Maven artifact. Pin a source
revision and compile `src/main/java` into the integration.

```java
// Integration sketch: implement this placeholder with the game's JSON library.
JsonValueCodec codec = new YourJsonCodec();
RinControlClient control = new RinControlClient(
    System.getenv("RIN_CONTROL_TOKEN"), codec);

control.info().thenAccept(System.out::println).join();
control.listWorlds().thenAccept(System.out::println).join();
```

`YourJsonCodec` is a placeholder, not a class shipped by Rin. The game
implements `JsonValueCodec` using its existing JSON library, so Rin does not
impose Jackson, Gson, or engine-specific serialization. The Control client uses
the standard `java.net.http.HttpClient`, rejects redirects, bounds response
bodies and timeouts, and returns `CompletableFuture` values.

`HostControlSession` only connects the game to the Control Daemon and carries
V2 Host data. Target resolution, effect previews, authority-thread execution,
cancellation, and outcome verification remain game adapter responsibilities.
