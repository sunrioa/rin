# Rin Console

Rin Console 是 `rin serve` 内嵌的本地管理界面，不是第二个 Control Plane，也不需要
Electron、Node 服务或新的数据库。默认地址为：

```text
http://127.0.0.1:7375/console/
```

使用 `rin console` 会先检查本地健康状态，再用默认浏览器打开该地址。页面使用与
Control API 相同的 Bearer Token；Token 只保存在当前浏览器标签页的 `sessionStorage`，
锁定页面或关闭标签页后清除。

## 页面

- **概览**：Control 健康、Host、世界、角色、控制来源和待处理故障。
- **角色**：查看所有 Adapter 发布的角色；选择角色后可在任务页自动带入 Host、World 和 Actor。
- **任务**：创建 `auto` 或 `required` 长目标，继续、恢复或取消任务，并查看人类可读时间线。
- **记忆**：搜索、新增、编辑、置顶和遗忘公共记忆卡片。
- **人格**：编辑所有未专门绑定角色共同继承的默认 Persona。
- **技能**：查看当前 Skill Catalog 中的通用和 Adapter 专属 Skill。
- **连接**：查看统一 Control/MCP/Adapter 拓扑和本机安装、诊断命令。

页面每五秒刷新运行状态；模型请求、磁盘操作和游戏执行不会在浏览器线程或游戏 Tick 中运行。

## 共享边界

默认 Persona 是同一 Rin 实例中所有 Internal Agent 的回退人格。角色或 Controller 的精确
binding 仍可覆盖它。首次配置只有一个角色 Persona、但没有全局 binding 时，Rin 会在首次
创建 Persona 存储时把该 Persona 设为共享默认；已有存储不会被静默改写。

公共记忆卡片进入 `common-semantic` 域，可被同一 Rin 下的 Internal Agent 检索。以下内容
不会自动成为公共记忆：

- 游戏 Adapter 持有的世界或剧情 Canon；
- Actor 私有记忆和 Controller 私有记忆；
- 外部 MCP Agent 自己的人格、Prompt 或私有记忆；
- 模型推测、未经确认的执行结果或 API Key。

卡片编辑采用追加新版本并隐藏旧版本，遗忘会处理整条版本链。公开任务时间线只显示记忆引用、
Skill 引用、公开理由、模型耗时与 Token、Policy 结果和权威 Operation 状态，不显示隐藏思维链、
完整 Prompt、记忆正文或凭据。

## 一个 Rin，多种游戏

所有 Adapter 连接同一个常驻 `rin serve`。游戏 Mod 只实现 Host/Adapter，不另做 MCP Server；
外部 Codex、Claude Code 或 OpenClaw 只需安装一次 `rin mcp`。游戏与 Mod 本身仍通过各自平台
安装和升级，Rin 不替代其分发系统。
