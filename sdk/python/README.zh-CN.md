# Rin Python Control SDK

[English](README.md) | [简体中文](README.zh-CN.md)

适用于 Python 3.9+ 的零运行时依赖 `rin.control/v2` 客户端。

同一客户端也通过原始 JSON 方法提供固定的 `/plans/v1/*` 任务计划和
`/signals/v1/*` Signal Inbox 接口。

```bash
python3 -m pip install -e ./sdk/python
```

```python
import os
from rin_sdk import RinControlClient

control = RinControlClient(token=os.environ["RIN_CONTROL_TOKEN"])
print(control.info())
print(control.list_worlds())
```

默认只连接 `http://127.0.0.1:7375`。构造器可以配置 `base_url`、`timeout`
和 `max_response_bytes`，但 URL 仍必须是无凭据、无路径的回环 HTTP Origin。

客户端暴露世界、Actor、Observation、Capability、Controller Lease、Action、
Operation、Emergency Stop，以及 Host 注册、发布、轮询、确认、进度和 Outcome
生命周期的全部 Control V2 路由，同时支持配置、发布、列出和等待有界 Actor Signal。
请求参数保持为普通 `dict`，
字段以仓库根目录的 `api/control-openapi.json` 为准。

异常分为 `RinConfigurationError`、`RinTransportError`、`RinProtocolError` 和
`RinAPIError`。不要把超时或 `queued` 当作执行成功；等待终态并检查
`execution_confirmed` 和 `outcome`。
