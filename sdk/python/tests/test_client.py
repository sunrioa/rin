import io
import json
import sys
import unittest
from pathlib import Path


sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "src"))

from rin_sdk import (  # noqa: E402
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
        self.path = ""
        self.method = ""
        self.authorization = ""

    def open(self, request, timeout):
        del timeout
        self.path = request.full_url.split("7374", 1)[-1]
        self.method = request.get_method()
        self.authorization = request.get_header("Authorization", "")
        status = 202 if self.path in ("/v1/jobs/propose", "/v1/generation/jobs") else 200
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
    def test_routes_and_token(self):
        client = RinClient(token="fixture")
        client._opener = _Opener()
        cases = (
            (client.health, (), "GET", "/health"),
            (client.create_session, ({},), "POST", "/v1/session/create"),
            (client.observe, ({},), "POST", "/v1/session/observe"),
            (client.propose, ({},), "POST", "/v1/agent/propose"),
            (client.submit_proposal_job, ({},), "POST", "/v1/jobs/propose"),
            (client.get_proposal_job, ("job.fixture",), "GET", "/v1/jobs/job.fixture"),
            (client.cancel_proposal_job, ("job.fixture",), "DELETE", "/v1/jobs/job.fixture"),
            (client.submit_generation_job, ({},), "POST", "/v1/generation/jobs"),
            (client.get_generation_job, ("job.fixture",), "GET", "/v1/generation/jobs/job.fixture"),
            (client.cancel_generation_job, ("job.fixture",), "DELETE", "/v1/generation/jobs/job.fixture"),
            (client.commit, ({},), "POST", "/v1/action/commit"),
            (client.commit_batch, ({},), "POST", "/v1/action/commit-batch"),
            (client.set_actor_activity, ({},), "POST", "/v1/session/activity"),
            (client.arbitrate, ({},), "POST", "/v1/world/arbitrate"),
            (client.state, ({},), "POST", "/v1/session/get"),
            (client.snapshot, ({},), "POST", "/v1/session/snapshot"),
            (client.restore, ({},), "POST", "/v1/session/restore"),
            (client.timeline, ({},), "POST", "/v1/session/timeline"),
            (client.replay, ({},), "POST", "/v1/session/replay"),
            (client.due_agents, ({},), "POST", "/v1/scheduler/due"),
        )
        for method, args, http_method, path in cases:
            with self.subTest(path=path):
                method(*args)
                self.assertEqual(client._opener.path, path)
                self.assertEqual(client._opener.method, http_method)
                self.assertEqual(client._opener.authorization, "Bearer fixture")

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
            _proposal(tick=1 << 63),
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
