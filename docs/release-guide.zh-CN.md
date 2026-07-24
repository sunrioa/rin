# Rin 发布与 Tag 指南

[English](release-guide.md) | [简体中文](release-guide.zh-CN.md)

Rin `0.6.0` 是 Preview、pre-1.0 版本。本清单只从已经通过验证的精确主分支 Commit
创建 `v0.6.0`，不承诺语言 Registry Package 或 Binary Artifact。

## 发布不变量

- `api/openapi.json` 是唯一 Wire Schema 来源，并标记版本 `0.6.0`、协议
  `rin.protocol/v1` 和 Preview 状态。
- README、Changelog、兼容矩阵、迁移指南、Roadmap、Security 与 Protocol 使用
  同一个发布状态。
- Tag 指向已经存在于远程主分支的 Commit。
- Tag 不可变。已发布版本若有错误，创建新的 Patch 版本；绝不移动或复用既有 Tag。
- 发布材料不得包含 Secret、Provider Response、游戏存档、完整事件日志或本地计划。

## 验证

从干净的主分支 Worktree 执行：

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

最后一条必须输出 `0.6.0`。还需确认：

- 每个本地 Markdown Link 都能解析；
- `api/openapi.json` 是合法 JSON，且包含与
  `sdk/conformance/routes.json` 相同的 20 个 Route Operation；
- 中英文发布文档互相链接；
- 迁移清单覆盖安全整数、`accepted` 必填、UTF-8、错误层次、Proposal
  Attempt/Outbox 恢复和 Restore Binding；
- 没有跟踪文件声称 SDK 已发布到语言 Registry；
- 尚未完成的真实游戏人工检查作为 Preview 限制可见，没有被写成已经通过的测试。

本地缺少的语言 Toolchain 必须在打 Tag 前由对应 CI Job 执行。源码 Marker Scan
或 Route Name Scan 只能证明预期文字存在，不能替代 Client 行为测试。Tag Push
触发的 Contract CI Job 还会要求 Git Tag 等于 OpenAPI `info.version` 对应的
`v${info.version}`。

## 创建 Tag

已验证 Commit 推送到远程主分支且必要检查通过后：

```bash
git switch main
git pull --ff-only origin main
git tag -a v0.6.0 -m "Rin v0.6.0 Preview"
git push origin v0.6.0
```

推送 Tag 前检查：

```bash
git show --stat --oneline v0.6.0
git rev-parse v0.6.0^{}
git rev-parse origin/main
```

Tag 解引用后的 Commit 必须与计划发布的 `origin/main` Commit 相同。

## Release Notes

使用[变更日志](../CHANGELOG.zh-CN.md)中的 `0.6.0` 章节，标题保留“Preview”，并包括：

- 精确 Tag 与 Commit；
- 支持的语言/Runtime 下限；
- 随附 File Store 的平台和文件系统边界；
- Snapshot 与请求/响应大小限制；
- 迁移和兼容链接；
- SDK 采用源码优先分发；
- 尚未完成的人工接入检查。

若另行发布 Binary Artifact，应在受控环境中从 Tag Commit 构建并发布 SHA-256
Checksum。当前仓库不宣称具有自动 Binary Release Pipeline。

## 发布后

确认 Fresh Clone 可以 Checkout `v0.6.0`、运行 `go test ./...`，并用
`VERSION=0.6.0` 构建 CLI。发现缺陷时不得更改既有 Tag；应记录问题并准备新的
Patch Release。
