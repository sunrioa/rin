# Host V2 contract

[English](host-contract.md) | [简体中文](host-contract.zh-CN.md)

`rin.host/v2` is the engine-neutral contract between Rin and an authoritative
game adapter. It standardizes verifiable identity, observation, capability,
intent, effects, and outcomes without standardizing a game's world model.

## Host manifest

`HostManifest` declares static adapter facts:

- adapter, engine, runtime, and platform identity;
- `standalone`, `server`, or `client-advisory` authority;
- loopback, dedicated, remote HTTPS, embedded offline, or computer-control deployment;
- event, step, and realtime clocks plus sequential, simultaneous, and asynchronous decisions;
- maximum concurrent Actors and actual durability guarantees.

Durability describes crash and retry behavior only:

| Profile | Guarantee |
| --- | --- |
| `advisory` | no idempotent recovery promise for world mutations |
| `idempotent-action` | the same operation may be redelivered without duplicating effects |
| `transactional-action` | game effect and outcome record commit in one game transaction |

Declaring a stronger profile does not create the guarantee. A real adapter must
prove it with fault injection and the game's save mechanism.

## Epoch and clocks

An `Epoch` combines stable Session and World IDs with three positive generations:

- `host` changes when the authoritative Host instance is replaced;
- `world` changes when a scene, dimension, map, or world reloads;
- `timeline` changes on save load, rollback, or timeline branching.

Generations are not render frames or ticks. A `Timepoint` represents an event,
step, or realtime Host clock. Actions, bindings, leases, and outcomes use epochs
to fence stale timelines; clocks express deadlines, execution budgets, and
confirmation expiry.

## World publication

`WorldPublication` atomically replaces one Host-owned world read model. Its
`actors` contain only currently online, controllable Actors. Optional
`visible_principal_ids` keep the world discoverable to matching Principals with
`actor.read` while no Actor is present. The list creates no authority,
controller lease, or execution permission; actions are rebound from live state
after an Actor returns.

## Observation

An `ObservationEnvelope` is a bounded Host-authored snapshot containing:

- Host, World, Actor, epoch, monotonic sequence, and observation time;
- a game-specific payload identified by `SchemaRef`;
- standardized `facts`, `resources`, and path-free `artifacts`;
- an optional pagination continuation token.

`ObservationFact` fits scalar facts such as health, stance, or relationship
state. `ObservationResource` additionally declares kind, tags, ownership,
scope, quantity, and Host-validated attributes. An artifact carries only an ID,
media type, size, and SHA-256, never a filesystem path or arbitrary fetch URL.

A `HostRef` is opaque. A controller may copy it from an observation but cannot
construct or resolve its key. Only the owning adapter resolves it on the
authority thread. An `ephemeral` reference must not cross epochs or enter
long-term state.

## Capability

A `CapabilitySpec` describes one action type:

- exact namespaced ID and semantic version;
- closed input, output, and effect-attribute JSON Schemas;
- `atomic` or `macro` kind;
- immediate, queued, or long-running execution;
- unsupported, cooperative, or preemptive cancellation;
- risk floor, scopes, durability, and Host-clock execution budget;
- input, output, and effect limits plus child-operation declaration;
- an immutable digest over canonical fields.

Discovery says what the Host implements, not whether an invocation is
authorized. Actor authority, controller lease, target state, effect policy,
and adapter-local rules are checked independently.

## Schema

Rin uses self-contained JSON Schema 2020-12. Capability schemas are bounded,
closed root objects and cannot load external references. Rin hashes canonical
schemas and seals all three schemas plus execution limits into the capability
digest.

A controller submits the exact digest. Changing argument schema, risk, budget,
or execution semantics invalidates old requests even if the ID and version were
not changed. Published integrations should also increment the semantic version.

## ActionRequest

The only action intent authored by a controller is `ActionRequest`:

```text
request_id / idempotency_key
controller_id / actor_id / task_id
capability id + version + spec_digest
arguments / target_refs
expected_epoch / observation_sequence
```

Arguments match the capability input schema and targets originate in a trusted
observation. A request contains no effect, risk, ownership, authorization
result, or executable function.

## Binding and effect preview

The adapter runs `Bind` on the authority thread:

1. capture the current snapshot;
2. validate capability, digest, epoch, and observation sequence;
3. resolve arguments and `HostRef` values;
4. return normalized targets and validity deadline;
5. derive effects from real game objects;
6. let the Registry seal an immutable `BoundAction`.

Standard `Effect` fields include:

- kind and read/create/update/delete/transfer/consume/execute/communicate operation;
- subject, target, tags, ownership, scope, quantity, and unit;
- reversibility and low/moderate/high/critical risk;
- game-specific attributes validated by the capability effect schema.

These fields are Host-derived. Controller prose, an argument such as
`safe=true`, or model-reported risk cannot affect policy.

## Execution results

`ActionRun` reports operation ID, status, monotonic `progress_seq`, progress in
the 0-10000 range, and Host time. `ActionOutcome` is the sole terminal fact and
binds the epoch, world sequence, occurrence time, and optional evidence.

Cancellation is a request, not rollback. `cancelled` means the Host confirmed a
stop, `interrupted` means the environment interrupted execution, and
`outcome-unknown` means the final effect can no longer be proven. A successful
outcome also includes structured output matching the capability output schema.

## Adapter interface

The neutral Go HostKit `Adapter` boundary is:

```text
Manifest
Snapshot / Observe / ListCapabilities
Bind / Preview
Execute / Cancel / Verify
PolicyFacts
```

Every method that can inspect or mutate game state runs through an
`AuthorityDispatcher`. HostKit helps with schemas, binding, final epoch checks,
and output validation; it cannot implement a particular game's main-thread
dispatch, navigation, container transactions, asset ownership, or save system.

See `host/` for exact Go types and
[`api/control-openapi.json`](../api/control-openapi.json) for their HTTP projection.
