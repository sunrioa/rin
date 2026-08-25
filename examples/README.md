# Rin examples

[简体中文](README.zh-CN.md)

The checked-in examples exercise the engine-neutral Harness V2 contract. They
do not duplicate game-engine projects or claim production support for a
specific game.

## Grid adapter

[adapters/grid](adapters/grid/) is the compact reference for observations,
capability binding, effect policy, resource collection, container transfer,
cancellation, restart rejection, and authoritative outcomes.

~~~sh
go test ./examples/adapters/grid
~~~

## Story adapter

[adapters/story](adapters/story/) applies the same HostKit contract to
dialogue, relationship changes, story progress, and enforceable character
boundaries. Its integration tests drive the scene through both the internal
Agent Runtime and an in-memory MCP session.

~~~sh
go test ./examples/adapters/story
~~~

## Terminal story

[terminal-story](terminal-story/) is a runnable end-to-end slice. It proves
that an embedded Host and an in-process controller carrying external decision
authority use the shared policy and authoritative Operation path.

~~~sh
go run ./examples/terminal-story --line "The light feels familiar." --json
~~~

Real game adapters belong in their own repositories. Generate a portable
contract skeleton with the [Host scaffolding workflow](../docs/host-scaffolding.md):

~~~sh
./bin/rin init host -engine custom -runtime java -id my_game_host -output ./my-game-host
~~~

Then validate the game-owned authority thread, save identity, policy,
idempotency, cancellation, restart, and emergency-stop boundaries in the
actual game.
