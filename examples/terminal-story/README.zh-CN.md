# Last Station 终端故事

[English](README.md) | [简体中文](README.zh-CN.md)

这个可安装的 Node.js 18+ 纵向切片使用优先 JavaScript SDK、强制安全语义基线、
持久 Proposal Attempt 和权威 Outcome Outbox，可在 Windows、macOS 与 Linux
运行。Mira 会记住玩家选择茶或咖啡，但只能选择游戏编写的白名单文本。

它是证据，不是刻意让 Rin 显得更好的展示。公平对照组会把同一偏好持久化到游戏
存档；对于这个单规则故事，它能以更少代码、更低延迟产生相同的玩家可见结果。
下产品结论前请阅读[玩家价值报告](../../docs/player-value.zh-CN.md)。

## 安装与游玩

在仓库根目录构建并启动 Sidecar：

```bash
go build -o bin/rin ./cmd/rin
./bin/rin serve -data ./rin-data
```

另开终端：

```bash
cd examples/terminal-story
npm install --ignore-scripts --offline
npm start
```

Windows PowerShell：

```powershell
go build -o bin\rin.exe .\cmd\rin
.\bin\rin.exe serve -data .\rin-data
```

再打开第二个 PowerShell：

```powershell
Set-Location examples\terminal-story
npm install --ignore-scripts --offline
npm start
```

`--mode baseline` 运行持久化规则树。默认 `--mode auto` 仅在启动健康检查证明尚未
发生任何 Rin 变更时回退；进入操作后的传输不确定性会 Fail Closed，等待精确恢复。

权威动作是故事存档中的 `shown_action_ids` 变更；该变更、Outcome Outbox Entry
与清除 Proposal Attempt 会通过一次文件替换共同发布。`presentAction` 只允许
执行非权威的终端/UI 呈现，并且仅在文件替换成功后运行；它不得修改世界状态。

非交互运行：

```bash
npm start -- --preference tea --json
```

默认存档位于 Windows 的 `LOCALAPPDATA`，其他平台位于 `XDG_DATA_HOME` 或
`~/.local/share`。可用 `--save PATH` 隔离测试存档。

## 复现基准

在仓库根目录：

```bash
go build -o bin/rin ./cmd/rin
cd examples/terminal-story
npm run benchmark -- --rin-bin ../../bin/rin --iterations 100
```

Windows 应传入 `--rin-bin ..\..\bin\rin.exe`。结果与机器相关，不要因为另一台
机器更快或更慢就覆盖仓库证据；应同时审查行为、成本与存储投影。
