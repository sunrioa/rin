# Last Station terminal story

[English](README.md) | [简体中文](README.zh-CN.md)

This installable Node.js 18+ vertical slice uses the priority JavaScript SDK,
the mandatory safe semantic baseline, a durable Proposal Attempt, and an
authoritative Outcome Outbox. It runs on Windows, macOS, and Linux. Mira
remembers the player's tea or coffee preference and selects only game-authored
allowlisted text.

This is evidence, not a showcase designed to make Rin look good. The fair
baseline persists the same preference in the game save. It produces the same
player-visible result with less code and lower latency for this one-rule story.
See the [player-value report](../../docs/player-value.md) before drawing product
conclusions.

## Install and play

Build and start the Sidecar from the repository root:

```bash
go build -o bin/rin ./cmd/rin
./bin/rin serve -data ./rin-data
```

In another shell:

```bash
cd examples/terminal-story
npm install --ignore-scripts --offline
npm start
```

Windows PowerShell:

```powershell
go build -o bin\rin.exe .\cmd\rin
.\bin\rin.exe serve -data .\rin-data
```

Then, in a second PowerShell window:

```powershell
Set-Location examples\terminal-story
npm install --ignore-scripts --offline
npm start
```

`--mode baseline` runs the persistent rule tree. `--mode auto` is the default:
it falls back only when the startup health check proves no Rin mutation began.
Transport uncertainty after that point fails closed for exact recovery.

The authoritative action is the `applied_action_ids` mutation in the story save.
That mutation, the Outcome Outbox entry, and clearing the Proposal Attempt are
published by one file replacement. `presentAction` is deliberately limited to
non-authoritative terminal/UI presentation and runs only after that replacement
succeeds; it must not perform world-state mutation.
The Store uses copy-on-write: a failed write or rename leaves both its in-memory
document and the previous save unchanged, removes its temporary file, and keeps
the Proposal Attempt available for exact retry.

On POSIX, publication syncs the temporary file, each newly created directory
entry, and the target directory after rename. Portable Node.js cannot open a
Windows directory handle for `FlushFileBuffers`, so Windows syncs the temporary
file and reopens and syncs the renamed target. A game that requires a strict
power-loss transaction must put the effect, operation marker, and Outbox in its
authoritative database/save transaction. Save schema 2 intentionally rejects
the earlier preview field `shown_action_ids`, which incorrectly conflated a
durable game effect with later UI presentation.

If rename succeeds but its final durability fence fails, the Store adopts the
published document in memory and rejects every later mutation until `load()`
re-reads the file and successfully retries that fence. It never continues from
the stale pre-rename document.
Outcome acknowledgement also compares the complete durable entry, so a delayed
ACK cannot remove a same-key report that changed while the request was in
flight.

Every mutation takes a short cross-process hard-link lease and re-reads the
current file while holding it. This makes the complete-entry comparison a disk
CAS instead of an in-memory check. The Store never steals an existing lock:
automatic PID-based recovery has PID-reuse and check/use races. Any existing
lock therefore fails closed as `story_save_busy`. After a crash, remove the
exact `<save-path>.lock` file only after confirming that no writer still uses
that save. If lock release itself is uncertain, the committing call keeps its
successful result and freezes later writes until `load()` reacquires the lease
and refreshes the save.

For a non-interactive run:

```bash
npm start -- --preference tea --json
```

The default save is under `LOCALAPPDATA` on Windows and
`XDG_DATA_HOME` or `~/.local/share` elsewhere. Use `--save PATH` to isolate a
test slot.

## Reproduce the benchmark

From the repository root:

```bash
go build -o bin/rin ./cmd/rin
cd examples/terminal-story
npm run benchmark -- --rin-bin ../../bin/rin --iterations 100
```

On Windows, pass `--rin-bin ..\..\bin\rin.exe`. Results are machine-specific;
do not overwrite checked-in evidence merely because a different machine is
faster or slower. Review behavior, cost, and storage projection together.
