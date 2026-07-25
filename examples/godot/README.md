# Rin Godot reference

[English](README.md) | [简体中文](README.zh-CN.md)

This is a runnable Godot 4.7.1 project and a source-first integration kit:

- `rin_client.gd` owns bounded asynchronous HTTP and Proposal Job transport;
- `rin_workflow.gd` owns stable save-slot identity, Pending Turn recovery,
  freshness, per-slot concurrency, shutdown cancellation, and Outcome Outbox;
- `example_npc.gd` is a 221-line game-owned policy and UI example.

Open this directory in Godot 4.7.1, start Rin on
`http://127.0.0.1:7374`, run the scene, then call
`ask_npc_to_respond()` from your game UI or debugger. For another project,
copy `rin_client.gd` and `rin_workflow.gd`, add `RinClient` as a child Node,
then construct one `RinWorkflow` per save slot.

The default slot is stored as bounded JSON under
`user://rin/default.json`. A generated 128-bit run ID, stable Create request,
sequence, logical tick high-water, complete Pending Turn/Observe, Job ID, and
up to 64 Outcome entries survive scene and process restarts. The coordinator
persists the turn before the first request and the Job ID before polling.
Terminal Commit errors are persisted as safe Observe fallbacks before retry,
and an ACK is persisted before eviction.

**Host capability profile: `advisory`.** After `FileAccess.flush()`, a
same-directory target/backup rename protocol makes interrupted replacement
recoverable with Windows-safe paths. The two renames are not one atomic
operation, and Godot does not make an arbitrary game-world effect part of that
file transaction. A crash between the effect callback and state replacement
can repeat the effect. Use a genuinely idempotent game operation or
transactional save provider before declaring a stronger profile.

The client accepts plaintext HTTP only for an exact loopback host, disables
redirects, caps response bodies, and performs no blocking wait. Remote HTTPS
tokens come from `RIN_TOKEN` at runtime rather than an exported scene property;
they are never written to workflow state. `shutdown()` asks an in-flight
Proposal Job to cancel; retained state remains recoverable if cancellation
cannot be confirmed.

Run the pinned headless verification:

```bash
python3 tools/verify_godot.py --godot /path/to/Godot
```

CI downloads the official Godot 4.7.1 binaries, verifies their SHA-512 hashes,
then parses every script and runs restart, retained-Job, Outbox conversion,
ACK, malformed-state, and write-failure tests on Linux and Windows.

References:

- [Godot command-line and headless mode](https://docs.godotengine.org/en/stable/tutorials/editor/command_line_tutorial.html)
- [Godot project data paths](https://docs.godotengine.org/en/stable/tutorials/io/data_paths.html)
- [Godot `DirAccess`](https://docs.godotengine.org/en/stable/classes/class_diraccess.html)
