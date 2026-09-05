# Execution storage and migration

[English](execution-storage.md) | [简体中文](execution-storage.zh-CN.md)

The daemon now uses `agent/tasks.db` and `operations.db` under
`RIN_CONTROL_DATA_DIR`. Both use SQLite schema 1, WAL, `synchronous=FULL`, private
files and the existing single-writer lock rules. Configuration files remain JSON.

Task CAS commits one task row plus its snapshot revision. Scheduler reads use an
immutable cached projection; a failed or ambiguous commit blocks those reads until
the store is reopened. Operations rewrite changed rows only. Their Policy state,
controller leases, emergency stops, global timeline cursor and per-sink outcome
acknowledgements commit in the same transaction. After a write failure, row identities
are reloaded before retrying, including cleanup of potentially committed orphan rows.
The existing 64 MiB logical-state limits and 32 MiB new-operation admission budget
still apply; the physical database and WAL include additional SQLite overhead.

Delivery counters and ActionRun progress retain their existing checkpoint semantics:
the next authoritative commit or graceful shutdown folds them in. New requests,
ACKs, cancellation, Outcomes and subscriber acknowledgements remain synchronous.
SQLite does not make Host execution transactional with Rin.

## Upgrade

1. Stop the daemon and back up the complete `RIN_CONTROL_DATA_DIR`.
2. Start the new binary. A new task database imports `tasks.json` v3/v4/v5;
   a new operation database imports `operations.json` v5/v6. Validation and
   normalization precede the migration transaction, including schedule recovery,
   pending cancellation, Policy reservations and outcome delivery state.
3. Existing JSON files remain untouched backups. A committed database is the
   authority on subsequent opens; changing the old JSON cannot roll state back.

An interrupted migration can retry if its schema transaction did not commit.
Malformed source state or unsupported versions fail startup. Task import holds
both the JSON and SQLite writer locks; Operation import uses the same directory
lock as the legacy writer. Current legacy open functions reject a sibling `.db`
to prevent accidentally reviving obsolete snapshots.

Back up SQLite state after stopping the daemon, or use SQLite's supported backup
mechanism. Copying only a live `.db` while omitting its WAL is insufficient.
An older binary cannot continue from these databases. To roll back, stop the new
binary and restore the complete pre-upgrade backup together; do not combine old
task JSON with newer Operation, Policy, Plan or Memory state.

## Reproducible storage measurement

```sh
go test ./cognition ./controlplane -run '^$' -bench 'Benchmark(Task.*CAS|Operation.*Commit)' -benchtime=200ms -count=2
go test ./cognition -run '^$' -bench BenchmarkTaskLoadUnderWrites -benchtime=200ms -count=2
```

These are local storage microbenchmarks. Task fixtures retain 64 events per task;
operation fixtures retain about 8 KiB of result bytes per operation. Each iteration
updates one retained record. Database initialization and JSON import are excluded.
They do not measure model latency, Host throughput or a production task workload.
The second command measures public Task reads under a continuous writer, including
mutex contention and defensive-copy cost; it does not isolate mutex wait time.
