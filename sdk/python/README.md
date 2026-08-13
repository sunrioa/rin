# Rin Python Control SDK

[English](README.md) | [简体中文](README.zh-CN.md)

A dependency-free `rin.control/v2` client for Python 3.9 and newer.

The same client exposes the fixed `/plans/v1/*` task-plan and `/signals/v1/*`
Signal Inbox routes as raw JSON methods.

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

The default endpoint is `http://127.0.0.1:7375`. The constructor also accepts
`base_url`, `timeout`, and `max_response_bytes`, while the URL must remain a
credential-free loopback HTTP origin with no path.

The client exposes every Control V2 route for worlds, actors, observations,
capabilities, controller leases, actions, operations, emergency stop, and the
Host register, publish, poll, acknowledgement, progress, and outcome lifecycle.
It also configures, publishes, lists, and waits for bounded Actor signals.
Payloads remain ordinary dictionaries; use the repository's
`api/control-openapi.json` for exact fields.

Errors are separated into `RinConfigurationError`, `RinTransportError`,
`RinProtocolError`, and `RinAPIError`. Never treat a timeout or `queued` status
as execution success; wait for a terminal operation and inspect
`execution_confirmed` and `outcome`.
