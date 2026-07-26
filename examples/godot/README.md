# Rin Godot reference

[English](README.md) | [简体中文](README.zh-CN.md)

This is a runnable Godot 4.6.3 project and a source-first integration kit:

- `rin_client.gd` owns bounded asynchronous HTTP and Proposal Job transport;
- `rin_host_contract.gd` validates complete Decision Window/Offer bindings and
  JSON-safe Host values;
- `rin_workflow.gd` owns stable save-slot identity, authority Epochs, Pending
  Turn/Active Run recovery, per-slot concurrency, shutdown cancellation, and
  Outcome Outbox;
- `example_npc.gd` is a sub-250-line game-owned policy and UI example.

Open this directory in Godot 4.6.3, start Rin on
`http://127.0.0.1:7374`, run the scene, then call
`ask_npc_to_respond()` from your game UI or debugger. For another project,
copy the three `rin_*.gd` files, add `RinClient` as a child Node, then
construct one `RinWorkflow` per save slot.

The default slot is stored as bounded JSON under
`user://rin/default.json`. A generated 128-bit run ID, stable Create request,
world identity, Host/World/Timeline generations, sequence, logical tick
high-water, complete Pending Turn/Observe, Job ID, Active Run, and up to 64
Outcome entries survive scene and process restarts. Opening a new Host lifetime
advances Host/Timeline; call `advance_epoch()` after authoritative world
replacement or rollback. The coordinator persists the turn before the first
request, the Job ID before polling, and an Active Run before game code.
Restarting an Active Run emits one `outcome-unknown` report rather than blindly
repeating the effect. Returned Proposals must exactly match the durable actor,
tick, Decision Window, and complete authored Offer. Reports remain exact while
retrying, and an ACK is persisted before eviction.

**Host durability profile: `advisory`.** After `FileAccess.flush()`, a
same-directory target/backup rename protocol makes interrupted replacement
recoverable with Windows-safe paths. The two renames are not one atomic
operation, and Godot does not make an arbitrary game-world effect part of that
file transaction. An uncertain crash is therefore reported
`outcome-unknown`; use a genuinely idempotent game operation or transactional
save provider before declaring a stronger profile.

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

CI downloads the official Godot 4.6.3 binaries, verifies the SHA-256 digests
published in GitHub Release metadata, then parses every script and runs
authority-generation, stale-offer, retained-Job, exact-Outbox, Active Run,
ACK, malformed-state, and write-failure tests on Linux and Windows. The local
verification command has a hard 30-second timeout so a broken coroutine cannot
hang CI.

References:

- [Godot command-line and headless mode](https://docs.godotengine.org/en/stable/tutorials/editor/command_line_tutorial.html)
- [Godot project data paths](https://docs.godotengine.org/en/stable/tutorials/io/data_paths.html)
- [Godot `DirAccess`](https://docs.godotengine.org/en/stable/classes/class_diraccess.html)
