# Rin AI Companion

This Fabric mod targets Minecraft 26.2. It creates a persistent, player-shaped companion with normal entity movement and sends private companion chat through a local Rin process to an OpenAI-compatible model. DeepSeek is the default; administrators can change the Base URL and model in game.

Phase 1 covers chat, follow, stop, recall, skin-name storage, and restart recovery. The companion does not harvest blocks, use an inventory, craft, fight, load distant chunks, or build. Those actions remain disabled until later phases can enforce recipes, resources, tools, durability, and other survival rules.

## Versions

- Minecraft 26.2
- Fabric Loader 0.19.3
- Fabric API 0.155.2+26.2
- Java 25
- Rin 0.7.0 / `rin.protocol/v2`

## Build

Run from this directory:

```powershell
$env:JAVA_HOME='C:\Program Files\Eclipse Adoptium\jdk-25.0.4.7-hotspot'
$env:PATH="$env:JAVA_HOME\bin;$env:PATH"
.\gradlew.bat clean check runGameTest build --no-daemon
```

The installable artifact is `build/libs/rin-ai-companion-0.1.0.jar`.

## Install and configure

Copy the mod JAR into the instance `mods` directory and keep Fabric API installed. Put the Windows Rin binary at `<instance>\rin\rin.exe`.

Set the provider key before starting the launcher:

```powershell
[Environment]::SetEnvironmentVariable('RIN_MODEL_API_KEY', 'your-key', 'User')
```

Restart the launcher after changing the environment. The key is never accepted by a command and is not stored in config, saves, or logs.

Player commands:

```text
/companion spawn
/companion recall
/companion pause
/companion resume
/companion status
/companion skin <Mojang player name>
@伙伴 hello, follow me
```

Administrator commands:

```text
/companion model show
/companion model baseurl https://api.deepseek.com/v1
/companion model name deepseek-chat
/companion model apply
```

Base URLs require HTTPS. Plain HTTP is accepted only for `localhost`, `127.0.0.1`, and `::1`.

Model settings live in `config/rin-ai-companion.properties`. Companion identity, ownership, mode, Session state, Pending Turn, and Outcome Outbox are saved with the world. If Rin or the provider is unavailable, Minecraft stays running and the companion uses a short local Chinese fallback response.
