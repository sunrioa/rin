from rin_sdk import PROTOCOL_VERSION, RinClient


client = RinClient("http://127.0.0.1:7374")
print(client.health())

session = {
    "protocol_version": PROTOCOL_VERSION,
    "request_id": "create.python-quickstart",
    "session_id": "python-quickstart",
    "binding": {
        "game_id": "python-demo",
        "content_id": "base",
        "content_version": "1",
        "content_hash": "demo-content-hash",
    },
    "seed": 42,
    "actors": [{
        "id": "npc.guide",
        "kind": "npc",
        "display_name": "Guide",
        "think_every_ticks": 1,
        "enabled": True,
    }],
}

try:
    print(client.create_session(session))
except Exception as exc:
    print("Session may already exist:", getattr(exc, "code", "rin_error"))
