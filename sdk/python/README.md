# Rin Python SDK

[English](README.md) | [简体中文](README.zh-CN.md)

A dependency-free synchronous client for Python 3.9+.

```python
from rin_sdk import PROTOCOL_VERSION, RinClient

client = RinClient("http://127.0.0.1:7374")
health = client.health()
```

Install from this checkout during development:

```bash
python3 -m pip install -e sdk/python
python3 -m unittest discover -s sdk/python/tests -p 'test_*.py'
```

The client is synchronous. Desktop tools and turn-based servers can call it
directly; a real-time game should run calls on its worker system and marshal
only the returned plain dictionaries back to the game thread.
Socket deadlines are reported as `RinTransportError("transport_timeout", ...)`;
other connection failures use `transport_failed`. Bounded response-body reads
spend from a monotonic deadline started before connection setup; a slowly
dripping body cannot restart that budget.

Use `RinControlClient` when an external application needs the same Control V2
surface as MCP:

```python
from rin_sdk import RinControlClient

control = RinControlClient(token="a-local-secret-containing-at-least-32-bytes")
worlds = control.list_worlds()
```

The Control client connects only to an explicit loopback HTTP port (default
`7375`). It covers actor observation, capability discovery, controller leases,
action submission and confirmation, Operation wait/cancel, and emergency stop.
Treat only a terminal Operation with a Host Outcome as executed.
