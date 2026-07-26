# OpenSpiel decision-model validation

[English](open-spiel-validation.md) |
[简体中文](open-spiel-validation.zh-CN.md)

Rin does not depend on OpenSpiel at runtime. The repository uses the real
OpenSpiel `2.0.1` Python binding as a test oracle for game semantics that are
easy to flatten incorrectly in an NPC-only integration.

## Verified mappings

| OpenSpiel state | Rin Host projection |
| --- | --- |
| One active player | `DecisionWindow.mode = sequential` with that actor only |
| Simultaneous node | One `simultaneous` window; collect one legal choice per actor and apply one joint action |
| Chance node | Game-owned transition; no Decision Window and no model-selectable offer |
| Information state | Actor-private Observation payload; never the complete hidden State |
| `move_number()` | `step` clock input; never a render frame or wall clock |

The projection creates offers only from `legal_actions(player)`. The action
integer is bound inside a host-authored argument object; policy output cannot
invent an action outside that set. Applying a proposal rechecks the complete
Decision Window and current legal action set.

## Executable cases

[`tools/verify_open_spiel.py`](../tools/verify_open_spiel.py) runs:

- Tic-Tac-Toe for sequential ownership and stale Decision Window rejection;
- Rock-Paper-Scissors for simultaneous collection and atomic joint apply;
- Kuhn poker for explicit chance transitions and imperfect information.

The Kuhn test creates two worlds with the same private card for player 0 and
different opponent cards. Their full states differ, while player 0 receives
the same Observation and legal Offers. The card owner still sees a different
information state. This is an executable noninterference check, not a naming
or source-marker claim.

CI installs OpenSpiel with `--no-deps --require-hashes` from
[`tools/open_spiel_requirements.txt`](../tools/open_spiel_requirements.txt).
The file pins every published CPython 3.11–3.14 macOS arm64, Linux
x86-64/aarch64, and Windows x86-64 wheel hash for version `2.0.1`. The test
runs on macOS, Linux, and Windows.

## What this does not claim

OpenSpiel is a semantic oracle, not a Rin production adapter. The harness does
not prove game-thread dispatch, save durability, Sidecar recovery, visual
control, or long-running world actions. Chance probabilities remain owned by
the game. Hidden State must not be sent merely because a model provider can
accept a large prompt.

Primary references:

- [OpenSpiel concepts](https://openspiel.readthedocs.io/en/latest/concepts.html)
- [OpenSpiel state API](https://openspiel.readthedocs.io/en/latest/api_reference.html)
- [OpenSpiel installation](https://openspiel.readthedocs.io/en/latest/install.html)
