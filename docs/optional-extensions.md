# Optional decision, memory, speech, and telemetry ports

[English] | [简体中文](optional-extensions.zh-CN.md)

Rin's authoritative core does not depend on a model vendor, agent framework,
vector database, speech service, or telemetry backend. Integrations implement
small Go ports and remain outside game authority.

## Port boundaries

| Port | Input and output | Authority rule |
| --- | --- | --- |
| `runtime.DecisionProvider` | immutable decision context -> structured `DecisionDraft` | May select only a current game-authored Offer |
| `provider.StructuredGenerationProvider` | messages plus JSON Schema -> untrusted structured content | Cannot mutate Session state or execute an action |
| `extension.MemoryIndex` | derived, provenanced documents -> bounded document IDs and scores | Event history remains authoritative |
| `extension.SpeechProvider` | approved display text -> immutable `AudioArtifactRef` | Cannot grant or execute game authority |
| `extension.TelemetrySink` | fixed, content-free lifecycle fields | Telemetry failure never changes a game outcome |

Implementations must be safe for concurrent use and cooperate with
`context.Context` cancellation. Provider SDK request/response types must be
translated at the adapter edge instead of leaking into these contracts.

## Decision and structured generation

`DecisionDraft` contains only structured identifiers, stance, and audit
associations. It deliberately has no free-form summary or rationale. The
runtime validates every returned ID and authors player-visible proposal text
from the selected, display-authorized `ActionOffer.description` and fixed
stance templates.

`StructuredGenerationProvider` is a lower-level content port. Its output is
untrusted data, not a command. Games must validate a closed schema and route
the result through their own authority rules before using it.

MCP, A2A, LangGraph, Microsoft Agent Framework, OpenAI Agents SDK, or another
agent runtime may be implemented as adapters behind these ports. They are not
Rin Core dependencies, and their tool calls do not become Host capabilities.

## Derived long-term memory

`MemoryIndex` contains a disposable search projection, never the canonical
record. Each `MemoryDocument`:

- belongs to one Session and Actor;
- carries one or more authoritative source Event IDs;
- has a framework-computed text SHA-256 binding;
- has bounded text, tags, source count, and tick range.

Use `RebuildMemoryIndex` to atomically replace one Session projection,
`SearchMemory` to validate bounded results, and `DeleteMemoryIndex` for privacy
deletion. The adapter receives cloned inputs and callers receive cloned
results. If the index is lost or corrupted, rebuild it from the protected event
history; never reconstruct authoritative events from vector-search results.

## Speech

`SpeechManager` synthesizes only text already approved for display. A request
is bound to Session, Actor, operation, language, voice, canonical audio media
type, and a framework-computed text hash. A provider returns metadata for an
immutable external artifact; raw audio is not stored in Rin Session state or
telemetry.

The manager provides:

- a bounded TTL/LRU cache isolated by Session and Actor;
- cooperative cancellation;
- text-only fallback for provider failures or invalid artifacts;
- fixed-field synthesis telemetry;
- an explicit playback report from the game.

The Host decides whether and how to play audio on its engine thread. A Ren'Py
adapter, for example, prepares speech in a worker, then schedules the returned
artifact through `renpy.invoke_in_main_thread`; save data stores only plain
correlation state, never a thread, callback, provider client, or raw audio.
Unity, Godot, Unreal, browser, and Mod hosts follow the same pattern with their
native audio presenter.

A Speech provider must keep a returned artifact dereferenceable for the
configured cache lifetime. Use opaque correlation IDs; do not encode dialogue,
credentials, prompts, or personal data in IDs.

## Telemetry and privacy

`TelemetryEvent` has a closed field set: event name, opaque correlation IDs,
status, duration, and timestamp. It has no arbitrary attributes, dialogue,
prompt, audio, credential, or save-payload field. Synthesis telemetry is
best-effort and cannot turn valid text into a failed decision. Explicit
playback reporting returns sink errors so the Host can retry its own
observability outbox if required.

## Computer control is a separate trust class

A screen-reading or input-emulation integration is not a semantic Host
adapter. It cannot provide the same exact object identity, Offer binding,
main-thread authority, or outcome proof. Keep any future
`ComputerControlHost` in a separate process and permission profile, require
explicit user opt-in, and review the target game's EULA and anti-cheat rules.
Its observations and suggested inputs remain untrusted and must not be
advertised as normal Host conformance.
