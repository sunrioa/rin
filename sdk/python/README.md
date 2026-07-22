# Rin Python SDK

Requires Python 3.9+ and has no third-party dependencies.

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
