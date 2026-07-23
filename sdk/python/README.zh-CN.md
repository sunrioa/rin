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
