# Rin Console

Rin Console is the local management UI embedded in `rin serve`. It is not a
second control plane and adds no Electron runtime, Node service, or database.
Its default address is:

```text
http://127.0.0.1:7375/console/
```

`rin console` checks local health before opening that address. The page uses the
same bearer token as the Control API. The token is kept only in the current
browser tab's `sessionStorage` and is removed when the Console is locked or the
tab closes.

## Views

- **Overview**: Control health, Hosts, worlds, actors, authority source, and faults.
- **Actors**: every actor published by every Adapter; acquire, renew, or release its controller lease,
  use emergency stop, and pre-fill a long goal.
- **Tasks**: start `auto` or `required` goals, run, resume, or cancel them, and page through the complete public timeline.
- **Memory**: search, add, edit, pin, and forget common memory cards.
- **Persona**: edit the default Persona inherited by actors without a specific binding,
  including initiative, triggers, relationships, boundaries, and exact bindings.
- **Skills**: inspect generic and Adapter-specific entries; create, edit, import,
  or remove one exact learned Skill version. Multiple versions of one Skill ID may coexist.
- **Operations**: combine Host, World, Actor, Task, and status filters over authoritative
  Operations, then confirm or cancel eligible work.
- **Connections**: inspect the shared Control/MCP/Adapter topology and local commands.
- **Settings**: save the internal model and optional remote embedding configuration,
  and manage the generic gameplay policy. Provider keys use a separate private file
  and are never echoed.

Runtime views refresh every five seconds. Model calls, disk work, and game
execution never run on the browser thread or a game tick.
Gameplay policy changes are applied and persisted immediately. Model and embedding
changes take effect after Rin restarts.

## Sharing boundary

The default Persona is the fallback for every Internal Agent attached to one Rin
instance. Exact actor or controller bindings still override it. On first storage
creation only, a configured actor Persona without a global binding is also made
the shared default. Existing stores are never silently rewritten.
The `* :: *` binding shown by Console is the shared fallback. Persona changes
presentation, initiative, and decision preferences; it never grants capabilities,
scopes, or gameplay authority.

Common cards use the `common-semantic` domain and may be retrieved by Internal
Agents in the same Rin instance. The following never become common memory
implicitly: Adapter-owned world or story canon, actor-private memory,
controller-private memory, an external MCP Agent's private persona or memory,
model guesses, unconfirmed outcomes, and API keys.

Edits append a replacement and hide the prior version; forgetting removes the
visible lineage. Public timelines contain references, public summaries, model
latency and token usage, policy decisions, and authoritative operation state.
They do not reveal hidden reasoning, complete prompts, private memory text, or credentials.

## One Rin, multiple games

Every Adapter connects to one resident `rin serve`. A game Mod implements the
Host/Adapter contract, not another MCP server. Codex, Claude Code, or OpenClaw
installs `rin mcp` once. Games and Mods still use their own distribution and
update channels.
