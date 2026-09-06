# Execution storage and migration

[English](execution-storage.md) | [简体中文](execution-storage.zh-CN.md)

The daemon stores execution state under `RIN_CONTROL_DATA_DIR`. SQLite stores use
WAL, `synchronous=FULL`, private files and one process writer. Low-frequency
configuration remains JSON. A successful authoritative write returns after commit;
SQLite does not make game execution or separate databases one transaction.

| Store | Schema | Contents |
| --- | --- | --- |
| `agent/tasks.db` | 2 | Bounded working task rows, archived settled tasks, snapshot revision |
| `operations.db` | 2 | Working operations, settled archive, outcome backlog, Policy/controller/emergency checkpoints |
| `agent/signals.db` | 1 | Bounded inboxes, settings, cooldowns, cursors, accepted Signals and delivery state |
| `agent/decision-records.db` | 1 | Bounded diagnostic decision rows and revision |
| `agent/taskstate.db` | 2 | Plans, steps, events, operation evidence and status indexes |
| `agent/memory.db` | Existing memory schema | Shared and isolated memory records and retrieval indexes |

## Working capacity and retained evidence

Task CAS updates one row and the snapshot revision. When the working set reaches
its configured limit (default 1,024), creating a task atomically archives the oldest
settled task and admits the replacement. Unknown outcomes, pending actions/macros
and pending skill-learning work cannot be archived. Old IDs remain reserved;
`GetTask` and Console archive queries can read their history. An archived task
cannot be reactivated. A late learning checkpoint can update its terminal record.
Plan capacity counts only planned, active, blocked and paused plans; closed plans
retain their identity and evidence and remain accessible by Plan/Task ID.

Operations update changed rows and the Policy, controller, emergency-stop and
cursor checkpoint together. Once settled, an operation with no active children
moves to the archive on retention expiry or when the execution pool needs a slot.
Archive insertion and removal from the working pool are one transaction. Operation
ID lookup and exact action idempotency lookup continue to find archived results.
Unknown outcomes stay in the working pool until the Host reconciles them.

The outcome backlog is independent of execution capacity. An authoritative Outcome
and its pending subscriber entries commit together; a successful subscriber ACK
and backlog removal also commit together, including after operation archival.
Each subscriber retries independently. Retry attempt counts and deadlines persist.
Removing a subscriber does not delete its pending evidence. Restore the same ID to
resume delivery. New subscriber IDs receive retained working outcomes on startup;
archived history is not automatically backfilled to newly introduced subscribers.

Console's execution page shows pending count, oldest evidence, subscriber, attempt
count and whether the subscriber is configured. `GET /management/v1/outcomes/backlog`
returns the earliest 100 entries. `POST /management/v1/outcomes/retry` takes
`operation_id` and `subscriber` and schedules a retry of existing evidence. It never
submits a game action. Both endpoints require the existing management authorization.
The in-memory and legacy JSON Control stores retain their original bounded retention
behavior; durable backlog separation is provided by the default SQLite backend.

Task and Operation working projections retain their 64 MiB logical limits, with a
32 MiB new-operation admission budget. Cold Task/Plan/Operation audit history is
retained indefinitely on disk and excluded from working capacity. There is no
automatic destructive purge: provision disk space and back up the whole data
folder. These limits do not bound the physical database or WAL size.

Decision records use incremental row insertion and oldest-row eviction (default
4,096 records, 64 MiB logical budget), replacing full diagnostic JSON rewrites.
Signal inboxes retain their per-actor bounds, Epoch and TTL rules; only accepted,
unexpired Signals can resume after restart. The total stored inbox budget is
64 MiB. Reaching a Signal's retry limit or expiry still ends that short-lived hint;
important long-lived goals should be durable Tasks.

A failed or ambiguous Task/Signal/decision commit does not expose uncommitted cached
success; an ambiguous decision commit blocks the recorder until reopen. Operation
writes preserve their retry semantics by reloading durable row identities after a
write error. Delivery counters and ActionRun progress keep their checkpoint
semantics; new requests, ACKs, cancellation, Outcomes and subscriber ACKs are
synchronous.

## Upgrade and rollback

1. Stop the daemon and back up the complete `RIN_CONTROL_DATA_DIR`.
2. Start the new binary. New task databases import `tasks.json` v3/v4/v5/v6; new
   operation databases import `operations.json` v5/v6; a new decision recorder
   imports `decision-records.json` v1. Existing Task and Operation schema 1
   databases add their archive/backlog tables. Signal storage starts empty on the
   first upgrade because the previous process-only inbox had no durable source.
3. Source JSON files remain untouched backups. A committed database is authoritative
   on later opens. Incomplete schema transactions can retry; malformed source state
   and unsupported versions fail startup. Legacy file open functions reject a
   sibling migrated `.db`; Task/decision imports hold the legacy writer lock too.

New caller-created tasks default to human confirmation; existing task policies
remain unchanged. Automatic short-lived initiative explicitly uses model-declared
acceptance. See [completion criteria](internal-agent-runtime.md#independent-completion-criteria).

Back up after stopping the daemon or through SQLite's supported backup mechanism.
Copying a live `.db` without its WAL is insufficient. Rollback requires stopping the
new binary and restoring the complete pre-upgrade folder; do not mix old task JSON
with newer Operation, Policy, Plan, Signal or Memory state.

## Reproducible measurements

```sh
go test ./cognition ./controlplane -run '^$' -bench 'Benchmark(Task.*CAS|Operation.*Commit)' -benchtime=200ms -count=2
go test ./cognition -run '^$' -bench BenchmarkTaskLoadUnderWrites -benchtime=200ms -count=2
go test ./cognition -run '^$' -bench 'Benchmark(DecisionPersistence|TaskSchedulingSelection)$' -benchtime=200ms -count=2
```

These are local storage and selection microbenchmarks, excluding initialization and
migration. Task fixtures retain 64 events per task; Operation fixtures retain about
8 KiB of result data. The decision benchmark preloads 1,000 records before appending;
the scheduling comparison reads 1,000 task histories versus one indexed actor.
They do not measure model latency, Host throughput or end-to-end task performance.
