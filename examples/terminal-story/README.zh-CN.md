# 终端故事

[English](README.md) | [简体中文](README.zh-CN.md)

终端故事是一个不需要模型、API Key 或外部服务的自包含嵌入式 Control V2 示例。
它用于证明非战斗类游戏也能通过引擎无关的 Adapter、Effect Policy、控制权租约、
Operation 和权威 Outcome 接入 Rin，并与其他游戏适配器共用同一条执行链。

参考场景提供四个强类型能力：

- `story.character.speak`
- `story.topic.change`
- `story.task.accept`
- `story.scene.wait`

Rin 核心不包含对白、章节、好感度或视觉小说规则。Story Adapter 把游戏状态转换为
`social.dialogue`、`relation.update` 和 `story.progress` Effect。密封信件话题会被
标记为 `story.character-boundary`，因此内部与外部控制器都会被同一个 Policy 拒绝。

## 运行

需要 Go 1.25 或更高版本。在仓库根目录执行：

```bash
go run ./examples/terminal-story
```

确定性冒烟运行：

```bash
go run ./examples/terminal-story \
  --line "The light in this photograph feels familiar." \
  --topic festival \
  --task prepare-exhibit \
  --json
```

查看权威角色边界拒绝：

```bash
go run ./examples/terminal-story \
  --line "Let us begin with the photograph." \
  --topic sealed-letter \
  --task restore-photograph
```

该命令运行嵌入式 Host，以及携带外部决策权限的进程内 Controller；它不会启动内部
Agent Runtime 或 MCP Client。集成测试还会使用内部 Agent Runtime 和真实的 MCP
内存会话驱动同一个场景：

```bash
go test ./examples/adapters/story
```

这些结果证明的是通用契约与集成链路，不代表其他引擎的线程、持久化或打包已经达到
生产可用状态。
