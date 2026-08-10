# Rin Python SDK

[English](README.md) | [简体中文](README.zh-CN.md)

面向 Python 3.9+ 的无第三方依赖同步客户端。

```python
from rin_sdk import PROTOCOL_VERSION, RinClient

client = RinClient("http://127.0.0.1:7374")
health = client.health()
```

开发时从当前 Checkout 安装并测试：

```bash
python3 -m pip install -e sdk/python
python3 -m unittest discover -s sdk/python/tests -p 'test_*.py'
```

客户端是同步的。桌面工具和回合制服务器可以直接调用；实时游戏应在自己的
Worker 系统中运行请求，只把返回的普通 Dictionary 切回游戏线程。
Socket Deadline 会映射为 `RinTransportError("transport_timeout", ...)`；其他
连接失败使用 `transport_failed`。读取有界响应体时会沿用连接建立前启动的
单调时钟截止时间；慢速滴流响应体不能重置这份预算。

外部应用需要使用与 MCP 相同的 Control V2 接口时，使用 `RinControlClient`：

```python
from rin_sdk import RinControlClient

control = RinControlClient(token="a-local-secret-containing-at-least-32-bytes")
worlds = control.list_worlds()
```

Control 客户端只连接带显式端口的本机回环 HTTP（默认 `7375`），覆盖角色观察、
能力发现、控制租约、行动提交与确认、Operation 等待/取消和急停。只有带 Host
Outcome 的 Operation 终态才能视为游戏已执行。
