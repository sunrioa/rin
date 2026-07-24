import io
import json
import sys
import unittest
from pathlib import Path


sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "src"))

from rin_sdk import (  # noqa: E402
    DEFAULT_MAX_RESPONSE_BYTES,
    PROTOCOL_VERSION,
    SDK_VERSION,
    RinAPIError,
    RinClient,
    RinConfigurationError,
    RinProtocolError,
    RinTransportError,
)


class _Response:
    def __init__(self, status, payload):
        self.status = status
        self.payload = json.dumps(payload).encode("utf-8")
        self.headers = {"Content-Length": str(len(self.payload))}
        self.stream = io.BytesIO(self.payload)

    def getcode(self):
        return self.status

    def read(self, maximum=-1):
        return self.stream.read(maximum)

    def __enter__(self):
        return self

    def __exit__(self, *_args):
        return False


class _Opener:
    def __init__(self):
        self.calls = 0
        self.path = ""
        self.method = ""
        self.status = 0
        self.authorization = ""
        self.user_agent = ""
        self.payload = None

    def open(self, request, timeout):
        del timeout
        self.calls += 1
        self.path = request.full_url.split("7374", 1)[-1]
        self.method = request.get_method()
        self.authorization = request.get_header("Authorization", "")
        self.user_agent = request.get_header("User-agent", "")
        self.payload = json.loads(request.data.decode("utf-8")) if request.data is not None else None
        status = 202 if self.path in ("/v1/jobs/propose", "/v1/generation/jobs") else 200
        self.status = status
        return _Response(status, {"ok": True, "data": {"status": "ok", "job_id": "job.fixture"}})


class _AdvancingClock:
    def __init__(self):
        self.value = 0.0

    def now(self):
        return self.value

    def sleep(self, seconds):
        self.value += seconds


def _proposal_job(status="running", *, job_id="job.fixture", proposal=None, error=None):
    job = {
        "job_id": job_id,
        "session_id": "session.fixture",
        "request_id": "request.fixture",
        "status": status,
    }
    if proposal is not None:
        job["proposal"] = proposal
    if error is not None:
        job["error"] = error
    return job


def _proposal(**overrides):
    proposal = {
        "id": "proposal.fixture",
        "session_id": "session.fixture",
        "request_id": "request.fixture",
        "actor_id": "actor.fixture",
        "tick": 7,
    }
    proposal.update(overrides)
    return proposal


def _generation_job(status="running", *, job_id="job.fixture", result=None, error=None):
    job = {
        "job_id": job_id,
        "request_id": "generation.fixture",
        "status": status,
    }
    if result is not None:
        job["result"] = result
    if error is not None:
        job["error"] = error
    return job


class RinClientTests(unittest.TestCase):
    def test_default_response_limit_matches_the_inline_transport_budget(self):
        self.assertEqual(DEFAULT_MAX_RESPONSE_BYTES, 32 * 1024 * 1024)
        self.assertEqual(RinClient().max_response_bytes, DEFAULT_MAX_RESPONSE_BYTES)

    def test_routes_and_token(self):
        client = RinClient(token="fixture")
        client._opener = _Opener()
        payload = {
            "protocol_version": PROTOCOL_VERSION,
            "request_id": "request.fixture",
            "utf8": "雨",
        }
        cases = (
            ("health", client.health, (), "GET", "/health"),
            ("create_session", client.create_session, (payload,), "POST", "/v1/session/create"),
            ("observe", client.observe, (payload,), "POST", "/v1/session/observe"),
            ("propose", client.propose, (payload,), "POST", "/v1/agent/propose"),
            ("submit_proposal_job", client.submit_proposal_job, (payload,), "POST", "/v1/jobs/propose"),
            ("get_proposal_job", client.get_proposal_job, ("job.fixture",), "GET", "/v1/jobs/job.fixture"),
            ("cancel_proposal_job", client.cancel_proposal_job, ("job.fixture",), "DELETE", "/v1/jobs/job.fixture"),
            ("submit_generation_job", client.submit_generation_job, (payload,), "POST", "/v1/generation/jobs"),
            ("get_generation_job", client.get_generation_job, ("job.fixture",), "GET", "/v1/generation/jobs/job.fixture"),
            ("cancel_generation_job", client.cancel_generation_job, ("job.fixture",), "DELETE", "/v1/generation/jobs/job.fixture"),
            ("commit", client.commit, (payload,), "POST", "/v1/action/commit"),
            ("commit_batch", client.commit_batch, (payload,), "POST", "/v1/action/commit-batch"),
            ("set_actor_activity", client.set_actor_activity, (payload,), "POST", "/v1/session/activity"),
            ("arbitrate", client.arbitrate, (payload,), "POST", "/v1/world/arbitrate"),
            ("state", client.state, (payload,), "POST", "/v1/session/get"),
            ("snapshot", client.snapshot, (payload,), "POST", "/v1/session/snapshot"),
            ("restore", client.restore, (payload,), "POST", "/v1/session/restore"),
            ("timeline", client.timeline, (payload,), "POST", "/v1/session/timeline"),
            ("replay", client.replay, (payload,), "POST", "/v1/session/replay"),
            ("due_agents", client.due_agents, (payload,), "POST", "/v1/scheduler/due"),
        )
        observed_routes = []
        for operation_name, method, args, http_method, path in cases:
            with self.subTest(path=path):
                result = method(*args)
                self.assertEqual(client._opener.path, path)
                self.assertEqual(client._opener.method, http_method)
                self.assertEqual(client._opener.authorization, "Bearer fixture")
                self.assertEqual(client._opener.user_agent, "rin-python/" + SDK_VERSION)
                self.assertEqual(client._opener.payload, payload if http_method == "POST" else None)
                self.assertEqual(result["status"], "ok")
                observed_routes.append(
                    (
                        operation_name,
                        client._opener.method,
                        client._opener.path.replace("job.fixture", "{job_id}"),
                        client._opener.status,
                    )
                )

        manifest_path = Path(__file__).resolve().parents[2] / "conformance" / "routes.json"
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        expected_routes = sorted(
            (
                operation["name"],
                operation["method"],
                operation["path"],
                operation["status"],
            )
            for operation in manifest["operations"]
        )
        self.assertEqual(sorted(observed_routes), expected_routes)

    def test_false_commit_flags_are_serialized(self):
        client = RinClient()
        client._opener = _Opener()
        client.commit({"accepted": False})
        self.assertIn("accepted", client._opener.payload)
        self.assertIs(client._opener.payload["accepted"], False)
        client.commit_batch({"items": [{"accepted": False}]})
        item = client._opener.payload["items"][0]
        self.assertIn("accepted", item)
        self.assertIs(item["accepted"], False)

    def test_invalid_json_numbers_cycles_and_depth_fail_before_transport(self):
        client = RinClient()
        client._opener = _Opener()
        cycle = {}
        cycle["self"] = cycle
        deep = "leaf"
        for _ in range(66):
            deep = [deep]
        invalid_payloads = (
            {"nested": [{"unsafe": (1 << 53)}]},
            {"nested": float("nan")},
            {"nested": float("inf")},
            {1: "non-string key"},
            {"nested": "\ud800"},
            cycle,
            {"nested": deep},
        )
        for payload in invalid_payloads:
            with self.subTest(payload_type=type(payload).__name__):
                with self.assertRaises(RinProtocolError) as caught:
                    client.commit(payload)
                self.assertEqual(caught.exception.code, "invalid_request")
        self.assertEqual(client._opener.calls, 0)

    def test_nonfinite_response_number_is_rejected(self):
        client = RinClient()

        class NonfiniteOpener:
            def open(self, request, timeout):
                del request, timeout
                return _Response(200, {"ok": True, "data": {"value": float("nan")}})

        client._opener = NonfiniteOpener()
        with self.assertRaises(RinProtocolError) as caught:
            client.health()
        self.assertEqual(caught.exception.code, "invalid_response")

    def test_job_id_is_ascii_and_path_safe(self):
        client = RinClient()
        client._opener = _Opener()
        for invalid in ("", "../job", "job/other", "\u4f5c\u4e1a"):
            with self.subTest(job_id=invalid), self.assertRaises(RinConfigurationError):
                client.get_proposal_job(invalid)

    def test_remote_endpoint_requires_tls_and_token(self):
        with self.assertRaises(RinConfigurationError):
            RinClient("http://models.example", token="fixture")
        with self.assertRaises(RinConfigurationError):
            RinClient("https://models.example")
        self.assertEqual(RinClient("https://models.example", token="fixture").base_url, "https://models.example")

    def test_api_error_is_bounded(self):
        client = RinClient()

        class ErrorOpener:
            def open(self, request, timeout):
                del request, timeout
                from urllib.error import HTTPError

                body = json.dumps({"ok": False, "error": {"code": "invalid_request", "message": "safe"}}).encode()
                raise HTTPError("http://127.0.0.1", 400, "Bad", {}, io.BytesIO(body))

        client._opener = ErrorOpener()
        with self.assertRaises(RinAPIError) as caught:
            client.health()
        self.assertEqual(caught.exception.code, "invalid_request")
        self.assertEqual(caught.exception.status, 400)

    def test_invalid_payload_and_content_length_are_protocol_errors(self):
        client = RinClient()
        with self.assertRaises(RinProtocolError):
            client.observe({"recursive": object()})

        response = _Response(200, {"ok": True, "data": {}})
        response.headers["Content-Length"] = "-1"

        class NegativeLengthOpener:
            def open(self, request, timeout):
                del request, timeout
                return response

        client._opener = NegativeLengthOpener()
        with self.assertRaises(RinProtocolError):
            client.health()

    def test_redirect_is_rejected(self):
        client = RinClient()

        class RedirectOpener:
            def open(self, request, timeout):
                del request, timeout
                from urllib.error import HTTPError

                raise HTTPError("http://127.0.0.1", 302, "Found", {"Location": "https://example.com"}, io.BytesIO(b""))

        client._opener = RedirectOpener()
        with self.assertRaises(RinTransportError) as caught:
            client.health()
        self.assertEqual(caught.exception.code, "redirect_rejected")

    def test_proposal_that_finishes_during_timeout_cancellation_is_returned(self):
        clock = _AdvancingClock()
        client = RinClient(clock=clock.now, sleeper=clock.sleep)
        client.get_proposal_job = lambda _job_id: _proposal_job()
        client.cancel_proposal_job = lambda _job_id: _proposal_job(
            "succeeded",
            proposal=_proposal(),
        )

        job = client.wait_for_proposal("job.fixture", deadline=0.05, interval=0.01)

        self.assertEqual(job["status"], "succeeded")
        self.assertEqual(job["proposal"]["id"], "proposal.fixture")

    def test_generation_that_finishes_during_timeout_cancellation_is_returned(self):
        clock = _AdvancingClock()
        client = RinClient(clock=clock.now, sleeper=clock.sleep)
        client.get_generation_job = lambda _job_id: _generation_job("queued")
        client.cancel_generation_job = lambda _job_id: _generation_job(
            "succeeded",
            result={"content": "finished at the deadline"},
        )

        job = client.wait_for_generation("job.fixture", deadline=0.05, interval=0.01)

        self.assertEqual(job["result"]["content"], "finished at the deadline")

    def test_timeout_uses_terminal_cancel_error_and_validates_success_payload(self):
        clock = _AdvancingClock()
        client = RinClient(clock=clock.now, sleeper=clock.sleep)
        client.get_proposal_job = lambda _job_id: _proposal_job()
        client.cancel_proposal_job = lambda _job_id: _proposal_job(
            "stale",
            error={"code": "proposal_stale", "message": "World changed"},
        )
        with self.assertRaises(RinAPIError) as caught:
            client.wait_for_proposal("job.fixture", deadline=0.05, interval=0.01)
        self.assertEqual(caught.exception.code, "proposal_stale")

        clock.value = 0.0
        client.cancel_proposal_job = lambda _job_id: _proposal_job("succeeded")
        with self.assertRaises(RinProtocolError) as caught:
            client.wait_for_proposal("job.fixture", deadline=0.05, interval=0.01)
        self.assertEqual(caught.exception.code, "invalid_job")

    def test_wait_rejects_crossed_or_malformed_get_identity(self):
        client = RinClient()
        client.get_proposal_job = lambda _job_id: _proposal_job(job_id="job.other")
        with self.assertRaises(RinProtocolError) as caught:
            client.wait_for_proposal("job.fixture")
        self.assertEqual(caught.exception.code, "invalid_job")

        for malformed in (
            _proposal(session_id="session.other"),
            _proposal(request_id="request.other"),
            _proposal(tick=1.5),
            _proposal(tick=1 << 53),
        ):
            with self.subTest(proposal=malformed):
                client.get_proposal_job = lambda _job_id, value=malformed: _proposal_job(
                    "succeeded",
                    proposal=value,
                )
                with self.assertRaises(RinProtocolError) as caught:
                    client.wait_for_proposal("job.fixture")
                self.assertEqual(caught.exception.code, "invalid_job")

        malformed_generation = _generation_job()
        malformed_generation["request_id"] = 42
        client.get_generation_job = lambda _job_id: malformed_generation
        with self.assertRaises(RinProtocolError) as caught:
            client.wait_for_generation("job.fixture")
        self.assertEqual(caught.exception.code, "invalid_job")

    def test_wait_rejects_crossed_or_malformed_timeout_delete_identity(self):
        clock = _AdvancingClock()
        client = RinClient(clock=clock.now, sleeper=clock.sleep)
        client.get_proposal_job = lambda _job_id: _proposal_job()
        client.cancel_proposal_job = lambda _job_id: _proposal_job(
            "succeeded",
            job_id="job.other",
            proposal=_proposal(),
        )
        with self.assertRaises(RinProtocolError) as caught:
            client.wait_for_proposal("job.fixture", deadline=0.05, interval=0.01)
        self.assertEqual(caught.exception.code, "invalid_job")

        clock.value = 0.0
        client.get_generation_job = lambda _job_id: _generation_job()
        client.cancel_generation_job = lambda _job_id: _generation_job(
            "succeeded",
            result={"content": "x" * (4 * 1024 * 1024 + 1)},
        )
        with self.assertRaises(RinProtocolError) as caught:
            client.wait_for_generation("job.fixture", deadline=0.05, interval=0.01)
        self.assertEqual(caught.exception.code, "invalid_job")

    def test_cancel_api_error_remains_job_timeout(self):
        clock = _AdvancingClock()
        client = RinClient(clock=clock.now, sleeper=clock.sleep)
        client.get_generation_job = lambda _job_id: _generation_job()

        def fail_cancel(_job_id):
            raise RinAPIError("jobs_unavailable", "Unavailable")

        client.cancel_generation_job = fail_cancel
        with self.assertRaises(RinAPIError) as caught:
            client.wait_for_generation("job.fixture", deadline=0.05, interval=0.01)
        self.assertEqual(caught.exception.code, "job_timeout")


if __name__ == "__main__":
    unittest.main()
