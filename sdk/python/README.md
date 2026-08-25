# Rin Python Control SDK

[English](README.md) | [简体中文](README.zh-CN.md)

A dependency-free `rin.control/v2` client for Python 3.9 and newer.

The same client exposes the fixed `/plans/v1/*` task-plan and `/signals/v1/*`
Signal Inbox routes as raw JSON methods.

From the repository root:

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

The authenticated routes wrapped by this client cover worlds, actors,
observations, capabilities, controller leases, actions, operations, emergency
stop, and the Host register, publish, poll, acknowledgement, progress, and
outcome lifecycle. The client also wraps bounded Actor signals and task plans;
it does not cover unauthenticated health endpoints. Payloads remain ordinary
dictionaries; use the repository-root
[`api/control-openapi.json`](../../api/control-openapi.json) for exact fields.

Errors are separated into `RinConfigurationError`, `RinTransportError`,
`RinProtocolError`, and `RinAPIError`. Never treat a timeout or `queued` status
as execution success; wait for a terminal operation and inspect
`execution_confirmed` and `outcome`.
