# Long-session storage and accelerated-year validation

[English] | [简体中文](long-session-validation.zh-CN.md)

Rin does not keep an NPC's complete lifetime in application logs or one
ever-growing prompt. It separates four storage classes:

| Data | Retention and retrieval |
| --- | --- |
| Authoritative events and permanent request/Event identities | Append-only per Session; paged through Timeline/Replay or bounded Session Transfer |
| Runtime memory projection | Fixed detailed-memory window plus deterministic summaries when `memory-archive-v1` is enabled |
| Optional semantic search | Disposable `MemoryIndex`; rebuild or delete it without changing authoritative history |
| Operational logs/telemetry | Content-free request and lifecycle metadata; external log rotation owns retention |

The bundled File Store gzip-compresses rebuildable checkpoints while keeping
the authoritative hash-chained event log as plain JSONL.

Archiving a Session freezes its event-chain anchor and makes the Session
read-only. It does not rewrite, truncate, or silently delete authoritative
events. Use authenticated Session stats for capacity monitoring, bounded
export for backup/migration, and archive-then-delete plus the documented
tombstone policy when data must be removed.

## Automated accelerated-year workload

`TestAcceleratedYearSession` advances one NPC through:

- 365 simulated days;
- one observation every six simulated hours (1,460 observations);
- one proposal and terminal outcome per simulated day;
- monthly snapshots;
- automatic checkpoints;
- process shutdown through `Engine.Close`, File Store close, and restart;
- tail Timeline retrieval and exact revision-1,000 Replay;
- storage statistics and final Session archive.

The test asserts that the authoritative revision/head survives restart,
detailed memories remain bounded, older memories have summaries, event/index/
snapshot byte accounting is nonzero, historical queries still work, and
shutdown drains checkpoint workers before the Store is closed.

The complete 365-day capacity test runs as a dedicated ordinary-test gate. Race
builds exclude that one disk-volume test so they remain inside Go's standard
test timeout; the full Race suite still covers File Store artifact concurrency
and Engine operation, transfer, and checkpoint shutdown paths.

This is a deterministic capacity/lifecycle regression. It is not a one-year
wall-clock soak, a game-frame benchmark, a provider availability claim, or
proof for a particular Mod loader. The separate real-host gate still requires
at least 1,000 turns or two hours per claimed host/backend with process kills,
network failures, and game saves.

## Production sizing

Interaction rate and payload size determine growth; calendar time alone does
not. Before shipping a persistent NPC:

1. run the game's representative event rate and payload distribution;
2. monitor `event_log`, `indexes`, `checkpoints`, `snapshots`, and total bytes;
3. page Timeline/Replay instead of loading the whole lineage;
4. keep provider prompts, raw audio, and full player text out of operational
   logs;
5. define backup, archive, deletion, and external log-rotation policies;
6. call `Engine.Close(ctx)` before closing the Store on every unload path.
