# Terminal Story

[English](README.md) | [简体中文](README.zh-CN.md)

Terminal Story is a self-contained, embedded Control V2 example that requires
no model, API key, or external service. It proves that a non-combat game can
expose dialogue and story progression through the same engine-neutral Adapter,
Effect Policy, controller lease, Operation, and authoritative Outcome path used
by other game integrations.

The reference scene offers four typed capabilities:

- `story.character.speak`
- `story.topic.change`
- `story.task.accept`
- `story.scene.wait`

Rin core does not contain dialogue, chapter, affection, or visual-novel rules.
The Story Adapter translates its game state to `social.dialogue`,
`relation.update`, and `story.progress` effects. The sealed-letter topic is
marked `story.character-boundary`, so the shared Policy rejects it for both
internal and external controllers.

## Run

Go 1.25 or later is required. From the repository root:

```bash
go run ./examples/terminal-story
```

For a deterministic smoke run:

```bash
go run ./examples/terminal-story \
  --line "The light in this photograph feels familiar." \
  --topic festival \
  --task prepare-exhibit \
  --json
```

To observe an authoritative character-boundary rejection:

```bash
go run ./examples/terminal-story \
  --line "Let us begin with the photograph." \
  --topic sealed-letter \
  --task restore-photograph
```

The command runs an embedded Host and an in-process controller carrying
external decision authority. It does not start the internal Agent Runtime or an
MCP client. The integration tests additionally drive the same scene through the
internal Agent Runtime and a real in-memory MCP session:

```bash
go test ./examples/adapters/story
```

These are contract and integration tests, not evidence that another engine's
threading, persistence, or packaging is production-ready.
