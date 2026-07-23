# BepInEx Rin NPC example

[English](README.md) | [简体中文](README.zh-CN.md)

A minimal plugin integration template for the Rin agent runtime.

This source overlay targets BepInEx 6 on a modern Unity/.NET runtime.

1. Create a plugin from the official BepInEx plugin template for the target
   game's backend and framework version.
2. Add a project reference to `sdk/csharp/Rin.Client/Rin.Client.csproj`, or
   copy its compiled assembly into the plugin's reference directory.
3. Add `Plugin.cs`, start Rin, and build the plugin into `BepInEx/plugins`.
4. Configure only `BaseUrl` in the generated BepInEx config. Supply a remote
   bearer token through the `RIN_TOKEN` process environment variable.
5. Press F8 for the isolated demo turn, or call `RequestNpcTurn` from the
   target game's actual dialogue or interaction hook.

`Update` only drains a bounded main-thread queue and detects the optional demo
key. HTTP runs asynchronously. The plugin validates `talk`, `wait`, or
`refuse`, invokes `NpcActionReady` on Unity's main thread, and commits only
after that application step. A real game-specific plugin should subscribe to
the event and map those IDs to its own NPC APIs.

Official plugin tutorial: https://docs.bepinex.dev/articles/dev_guide/plugin_tutorial/index.html
Configuration guide: https://docs.bepinex.dev/articles/dev_guide/plugin_tutorial/4_configuration.html
