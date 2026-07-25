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
