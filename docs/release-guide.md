# Rin release and tag guide

[English](release-guide.md) | [简体中文](release-guide.zh-CN.md)

Rin `0.6.0` is a Preview, pre-1.0 release. This checklist creates `v0.6.0`
only from the exact main-branch commit that passed verification. It does not
promise registry packages or binary artifacts.

## Release invariants

- `api/openapi.json` is the single wire-schema source and identifies version
  `0.6.0`, protocol `rin.protocol/v1`, and Preview status.
- README, Changelog, compatibility matrix, migration guide, Roadmap, Security,
  and Protocol use the same release status.
- The tag points to a commit already present on the remote main branch.
- Tags are immutable. A published mistake receives a new patch version; never
  move or reuse an existing tag.
- No secret, provider response, game save, complete event log, or local plan is
  included in release material.

## Verification

From a clean main-branch worktree:

```bash
go test ./...
go test -race ./...
go vet ./...
python3 -m unittest discover -s adapters/renpy -p 'test_*.py'
make test-sdks
CGO_ENABLED=0 go build -trimpath ./cmd/rin
python3 tools/generate_contract.py --check --tag v0.6.0
make build VERSION=0.6.0
./bin/rin version
```

The last command must print `0.6.0`. Also verify:

- every local Markdown link resolves;
- `api/openapi.json` parses as JSON and contains the same 20 route operations as
  `sdk/conformance/routes.json`;
- English and Chinese release documents link to each other;
- the migration checklist covers safe integers, required `accepted`, UTF-8,
  error layers, Proposal Attempt/Outbox recovery, and Restore Binding;
- no tracked file claims that SDKs are published to a language registry;
- the real-game manual checks that remain incomplete are visible as Preview
  limitations rather than reported as completed tests.

Language toolchains that are unavailable locally must be executed by the
corresponding CI job before tagging. A source-marker scan or route-name scan
only proves that expected text exists; it is not a substitute for a client
behavior test. On a pushed tag, the contract CI job also requires the Git tag
to equal `v${info.version}` from OpenAPI.

## Tagging

After the verified commit is pushed to the remote main branch and the required
checks pass:

```bash
git switch main
git pull --ff-only origin main
git tag -a v0.6.0 -m "Rin v0.6.0 Preview"
git push origin v0.6.0
```

Before pushing the tag, inspect it:

```bash
git show --stat --oneline v0.6.0
git rev-parse v0.6.0^{}
git rev-parse origin/main
```

The peeled tag commit and intended `origin/main` commit must match.

## Release notes

Use the `0.6.0` section of the [Changelog](../CHANGELOG.md). Keep the word
“Preview” in the title and include:

- the exact tag and commit;
- supported language/runtime floors;
- the bundled File Store platform/filesystem boundary;
- Snapshot and request/response size limits;
- the migration and compatibility links;
- the fact that SDKs are source-first;
- remaining manual integration checks.

If binary artifacts are published separately, build them from the tagged
commit in a controlled environment and publish SHA-256 checksums. The
repository does not currently claim an automated binary-release pipeline.

## After release

Verify that a fresh clone can check out `v0.6.0`, run `go test ./...`, and build
the CLI with `VERSION=0.6.0`. Do not change the existing tag if a defect is
found; document it and prepare a new patch release.
