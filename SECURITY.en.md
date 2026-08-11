# Security

[简体中文](SECURITY.md) | [English](SECURITY.en.md)

This document defines the Rin Harness V2 security boundary. The current source
is `0.7.0` Preview; Preview status does not relax fail-closed behavior, secret
isolation, or game authority.

## Trust model

Rin treats these components as its trusted computing base:

- the authoritative game Host and adapter;
- identities, scopes, and policy configuration supplied by the game owner or
  server administrator;
- the local `rin-control` process and its private data directory.

The following are always untrusted:

- model responses, external Agents, and MCP arguments;
- instructions inside player dialogue, mod text, memories, persona text, and
  skill text;
- arbitrary JSON not validated by an adapter, remote provider responses, and
  network error bodies.

The Host describes real world state. A model or external Agent may submit only
an `ActionRequest`; it cannot declare effects, ownership, risk, reversibility,
policy results, or execution success.

## Enforced execution path

Every world mutation traverses:

1. a valid exclusive controller lease;
2. an `ActionRequest` bound to the current Actor, observation sequence, and epoch;
3. target resolution and `BoundAction` creation by the Host on its authority thread;
4. a Host-authored effect preview;
5. deterministic gameplay policy allow, deny, or single-use confirmation;
6. recoverable operation delivery;
7. Host run, outcome, and evidence.

The built-in policy safety kernel cannot be disabled by configuration. It
denies effect kinds or tags for arbitrary code, file access, native calls,
authority forgery, and secret exposure. Unknown effects, scopes, and ownership
also fail closed. Even `open` and `privileged-custom` profiles cannot bypass
the kernel.

Capability discovery is not authorization. A macro must create auditable child
operations for world mutations and receives no bypass. The game must revalidate
objects, distance, region, ownership, resources, cooldowns, and current world
state immediately before execution to prevent TOCTOU errors.

## Execution results

None of these states prove that a game action executed: `queued`,
`awaiting-confirmation`, `delivered`, `accepted`, or `running`. The Control
Plane sets `execution_confirmed=true` only after the Host supplies a terminal
outcome.

A client timeout, cancelled wait, disconnected transport, or `outcome-unknown`
does not prove that the action did not occur. Reconcile the same operation ID;
do not create a semantically equivalent request under a new identity.

Host leases, controller leases, authority revisions, epochs, and observation
sequences reject commands from disconnected instances, replaced controllers,
or stale timelines. Emergency stop is controller-independent and blocks both
internal and external sources.

## Local network boundary

`rin-control` accepts loopback listen addresses only and defaults to
`127.0.0.1:7375`. The Control API requires:

- `RIN_CONTROL_TOKEN` with at least 32 bytes;
- a principal and scopes fixed by daemon startup configuration, never a request body;
- bearer authentication on HTTP requests;
- SDK rejection of redirects with bounded time, body size, JSON depth, and safe integers.

This release does not support exposing the Control API directly to the public
Internet. Cross-machine control requires a separately audited authentication
proxy and network isolation supplied by the deployer; it is outside the current
support boundary.

`rin-mcp` is a thin STDIO proxy to the local Control Daemon. It does not own game
state, start a second executor, or interpret tool acceptance as success. One MCP
installation can access multiple compatible Hosts visible to its configured
principal.

## Internal models and prompt injection

The Internal Agent places machine-selected allowed capabilities, target handles,
epochs, and budgets in a trusted contract. Persona, memory, skills, observation,
player text, and capability descriptions are placed under `untrusted_context`.
Model output must match a closed JSON Schema and is checked again:

- it may reference only capabilities, versions, and targets listed by the contract;
- it receives at most one bounded capability or skill inspection round;
- memory candidates are confidence- and TTL-bound subjective hypotheses, not
  authoritative world facts;
- it cannot report action success or generate directly executed code or engine calls.

Even when prompt injection produces a privileged intent, Host binding, policy,
and adapter pre-execution validation must reject it.

## Providers and secrets

Internal Agent configuration files must not contain API keys. Credentials enter
the process only through:

- `RIN_CONTROL_TOKEN` for the Control API;
- `RIN_AGENT_TOKEN` for the Agent Task API, distinct from the Control token;
- `RIN_AGENT_API_KEY` for an optional model provider, distinct from both daemon tokens.

Remote model URLs require HTTPS; only loopback providers may use HTTP. URLs may
not contain user information. The provider client rejects redirects and bounds
response size, timeout, retry, and circuit breaking. Errors do not echo API keys
or complete provider bodies.

Do not place tokens, API keys, private Agent configuration, filesystem paths, or
unfiltered player data in:

- observations, capabilities, actions, outcomes, or audit summaries;
- personas, memories, skills, or game saves;
- MCP tool output, logs, test fixtures, or version control.

If a credential appeared in chat, a screenshot, shell history, or a commit,
revoke and rotate it at the provider. Removing text from Git history does not
make an exposed credential safe again.

## Local state

Control operation, Agent task, memory, and related state directories use a
single-writer process lock. A second process cannot open the same directory at
the same time. Updates use bounded JSON, temporary files, synchronization, and
atomic replacement. Treat the entire directory as sensitive and keep it on a
local filesystem under a restricted account.

NFS, SMB, cloud-synchronized folders, and paths shared by multiple processes
are unsupported. Stop the daemon before copying state, or use an external
mechanism that provides a consistent snapshot.

## Adapter responsibilities

Rin cannot infer every game-specific rule. An adapter must:

- mutate the world only on the server or game authority thread;
- never expose object pointers, arbitrary paths, command executors, or private APIs;
- provide explicit local permissions for multiplayer, public servers, commands,
  containers, player assets, and destructive actions;
- keep navigation, combat, and other real-time actions interruptible and
  periodically recheck environmental danger;
- construct outcomes from trusted results, never from model prose.

“Maximum authority” should mean every capability that the adapter registered,
the Host can bind, and policy can inspect. It must not mean shell access,
arbitrary code, or bypassing server permissions.

## Reporting vulnerabilities

Use the repository's private GitHub security reporting channel. Do not attach
tokens, API keys, private Agent configuration, player saves, complete operation
state, or reproducible secrets to a public issue.
