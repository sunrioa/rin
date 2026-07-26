# Rin Unity adapter

[English](README.md) | [简体中文](README.zh-CN.md)

This directory is an installable Unity Package Manager package that declares
Unity `2021.3` as its minimum API level. Add it from a local checkout, attach `RinClient` and
`RinUnityWorkflow` to one persistent GameObject, assign the client field, then
keep the small `RinNpcExample` in game-owned code.

`RinUnityWorkflow` stores at most 1 MiB under
`Application.persistentDataPath/rin/default.json`. It preserves the stable
playthrough identity, complete Proposal attempt, Job ID, tick high-water,
applied marker, and up to 64 reports. A flushed temporary file and recoverable
target/backup rename sequence avoid relying on overwrite-style rename on
Windows.

The durability profile remains `advisory`: the example can roll back its demo
effect in process, but a general Unity world/save mutation cannot be atomic
with this sidecar state file. Production games should make the operation ID
idempotent or replace this boundary with their save transaction.

The token is read at runtime from `RIN_TOKEN`; it is never serialized into a
scene or prefab. The default loopback HTTP endpoint needs no token.

`tools/verify_unity.py` compiles the package against strict Unity API stubs and
runs persistence/restart tests on .NET 6. CI runs it on Linux and Windows.
This is a package/compiler verification, not a Unity Editor import or built
Player test. Run the
[real-host validation matrix](../../docs/mod-integration-validation.md) before
claiming compatibility with a particular Unity release or scripting backend.
