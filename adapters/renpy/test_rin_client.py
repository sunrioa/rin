import io
import json
import ast
import textwrap
import threading
import unittest
from pathlib import Path
from urllib.error import HTTPError, URLError

import rin_client


class _Response:
    def __init__(self, status, value):
        self.status = status
        self.payload = json.dumps(value, separators=(",", ":")).encode("utf-8")
        self.headers = {"Content-Length": str(len(self.payload)), "Content-Type": "application/json"}
        self._stream = io.BytesIO(self.payload)

    def getcode(self):
        return self.status

    def read(self, maximum=-1):
        return self._stream.read(maximum)

    def __enter__(self):
        return self

    def __exit__(self, exc_type, exc, traceback):
        return False


class _Opener:
    def __init__(self):
        self.polls = 0
        self.generation_polls = 0
        self.authorization = ""
        self.last_payload = None

    def open(self, request, timeout):
        self.authorization = request.get_header("Authorization", "")
        path = request.full_url.split("//", 1)[-1]
        path = path[path.find("/"):] if "/" in path else "/"
        if request.data is not None:
            self.last_payload = json.loads(request.data.decode("utf-8"))
        if request.get_method() == "POST" and path == "/v1/jobs/propose":
            return _Response(202, {
                "ok": True,
                "data": {"job_id": "job.fixture", "status": "queued", "duplicate": False},
            })
        if request.get_method() == "GET" and path == "/v1/jobs/job.fixture":
            self.polls += 1
            if self.polls == 1:
                return _Response(200, {
                    "ok": True,
                    "data": {"job_id": "job.fixture", "status": "running"},
                })
            return _Response(200, {
                "ok": True,
                "data": {
                    "job_id": "job.fixture",
                    "status": "succeeded",
                    "proposal": {
                        "id": "proposal.fixture",
                        "action": {"id": "talk", "kind": "dialogue", "description": "Talk"},
                        "policy_source": "deterministic",
                    },
                },
            })
        if request.get_method() == "DELETE" and path == "/v1/jobs/job.fixture":
            return _Response(200, {
                "ok": True,
                "data": {"job_id": "job.fixture", "status": "canceled"},
            })
        if request.get_method() == "POST" and path == "/v1/generation/jobs":
            return _Response(202, {
                "ok": True,
                "data": {"job_id": "gen.fixture", "status": "queued", "duplicate": False},
            })
        if request.get_method() == "GET" and path == "/v1/generation/jobs/gen.fixture":
            self.generation_polls += 1
            if self.generation_polls == 1:
                return _Response(200, {
                    "ok": True,
                    "data": {"job_id": "gen.fixture", "status": "running"},
                })
            return _Response(200, {
                "ok": True,
                "data": {
                    "job_id": "gen.fixture",
                    "status": "succeeded",
                    "result": {
                        "content": '{"narration":"雨停了。"}',
                        "model": "fixture-model",
                    },
                },
            })
        if request.get_method() == "DELETE" and path == "/v1/generation/jobs/gen.fixture":
            return _Response(200, {
                "ok": True,
                "data": {"job_id": "gen.fixture", "status": "canceled"},
            })
        if path == "/redirect":
            payload = json.dumps({"ok": False}).encode("utf-8")
            raise HTTPError(
                request.full_url,
                302,
                "Found",
                {"Content-Length": str(len(payload)), "Location": "/health"},
                io.BytesIO(payload),
            )
        return _Response(200, {"ok": True, "data": {"status": "ok"}})


def _proposal_request():
    return {
        "protocol_version": rin_client.PROTOCOL_VERSION,
        "session_id": "session.fixture",
        "request_id": "request.fixture",
        "actor_id": "npc.mira",
        "tick": 2,
        "intent": "Respond",
        "candidate_actions": [
            {"id": "talk", "kind": "dialogue", "description": "Talk"},
            {"id": "wait", "kind": "wait", "description": "Wait"},
        ],
    }


def _generation_request():
    return {
        "protocol_version": rin_client.PROTOCOL_VERSION,
        "request_id": "generation.fixture",
        "kind": "scene",
        "context_hash": "a" * 64,
        "messages": [{"role": "user", "content": "Return JSON."}],
        "temperature": 0.6,
        "max_tokens": 512,
        "response_format": "json_object",
    }


def _client_with_opener(token=""):
    client = rin_client.RinClient(token=token)
    client._opener = _Opener()
    return client


class RinClientTests(unittest.TestCase):
    def test_async_proposal_flow_and_token(self):
        client = _client_with_opener("fixture-token")
        result = client.propose_with_fallback(
            _proposal_request(),
            deadline_seconds=1,
            poll_interval=0.01,
        )
        self.assertEqual(result["source"], "sidecar")
        self.assertTrue(result["committable"])
        self.assertEqual(result["proposal"]["action"]["id"], "talk")
        self.assertEqual(client._opener.authorization, "Bearer fixture-token")
        self.assertEqual(client._opener.last_payload["request_id"], "request.fixture")

    def test_transport_failure_uses_authored_fallback(self):
        client = rin_client.RinClient()

        class FailingOpener:
            def open(self, request, timeout):
                raise URLError("dial failed with fixture-token")

        client._opener = FailingOpener()
        result = client.propose_with_fallback(
            _proposal_request(),
            fallback_action_id="wait",
        )
        self.assertEqual(result["source"], "offline")
        self.assertFalse(result["committable"])
        self.assertEqual(result["fallback_reason"], "transport_failed")
        self.assertEqual(result["proposal"]["action"]["id"], "wait")
        self.assertEqual(result["proposal"]["policy_source"], "adapter-offline")
        self.assertNotIn("fixture-token", json.dumps(result))

    def test_structured_generation_flow(self):
        client = _client_with_opener("fixture-token")
        result = client.generate_json(
            _generation_request(),
            deadline_seconds=1,
            poll_interval=0.01,
        )
        self.assertEqual(result["source"], "sidecar")
        self.assertEqual(result["response"]["narration"], "雨停了。")
        self.assertEqual(result["metadata"]["model"], "fixture-model")
        self.assertEqual(client._opener.last_payload["kind"], "scene")

    def test_configuration_rejects_unsafe_remote_endpoints(self):
        invalid = (
            "http://models.example",
            "file:///tmp/rin.sock",
            "https://user@models.example",
            "https://models.example/path",
            "https://models.example?redirect=1",
        )
        for value in invalid:
            with self.subTest(value=value):
                with self.assertRaises(rin_client.RinConfigurationError):
                    rin_client.RinClient(value)
        secure = rin_client.RinClient("https://models.example", token="fixture")
        self.assertEqual(secure.base_url, "https://models.example")

    def test_redirect_is_not_followed(self):
        client = _client_with_opener()
        with self.assertRaises(rin_client.RinAPIError) as caught:
            client._request("GET", "/redirect")
        self.assertEqual(caught.exception.status, 302)

    def test_background_registry_returns_plain_result(self):
        client = _client_with_opener()
        registry = rin_client.BackgroundProposalRegistry(client)
        request_id = registry.schedule(
            _proposal_request(),
            lambda worker: worker(),
            deadline_seconds=1,
            poll_interval=0.01,
        )
        self.assertEqual(registry.status(request_id), "complete")
        entry = registry.consume(request_id)
        self.assertEqual(entry["status"], "complete")
        self.assertEqual(entry["result"]["source"], "sidecar")
        json.dumps(entry)
        self.assertEqual(registry.status(request_id), "missing")

    def test_background_registry_rejects_request_id_conflict(self):
        client = _client_with_opener()
        registry = rin_client.BackgroundProposalRegistry(client)
        request = _proposal_request()
        registry.schedule(request, lambda worker: None)
        changed = dict(request)
        changed["intent"] = "Different intent"
        with self.assertRaises(rin_client.RinProtocolError) as caught:
            registry.schedule(changed, lambda worker: None)
        self.assertEqual(caught.exception.code, "request_id_conflict")

    def test_invalid_fallback_is_rejected(self):
        with self.assertRaises(rin_client.RinProtocolError):
            rin_client.offline_proposal_result(
                _proposal_request(),
                fallback_action_id="not-advertised",
            )

    def test_cancellation_reaches_job_endpoint(self):
        client = _client_with_opener()
        canceled = threading.Event()
        canceled.set()
        with self.assertRaises(rin_client.RinJobError) as caught:
            client.propose_with_fallback(
                _proposal_request(),
                cancel_event=canceled,
            )
        self.assertEqual(caught.exception.code, "job_canceled")

    def test_response_size_limit_is_enforced(self):
        client = rin_client.RinClient(max_response_bytes=1024)

        class LargeOpener:
            def open(self, request, timeout):
                return _Response(200, {"ok": True, "data": {"value": "x" * 2048}})

        client._opener = LargeOpener()
        with self.assertRaises(rin_client.RinProtocolError) as caught:
            client.health()
        self.assertEqual(caught.exception.code, "response_too_large")

    def test_renpy_bridge_python_block_parses(self):
        source = Path(__file__).with_name("rin_bridge.rpy").read_text(encoding="utf-8")
        marker = "init -30 python:\n"
        self.assertIn(marker, source)
        python_source = textwrap.dedent(source.split(marker, 1)[1])
        ast.parse(python_source)
        self.assertNotIn("default _RIN_", source)


if __name__ == "__main__":
    unittest.main()
