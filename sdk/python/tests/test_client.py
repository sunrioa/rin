import io
import json
import sys
import unittest
from pathlib import Path


sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "src"))

from rin_sdk import RinAPIError, RinClient, RinConfigurationError  # noqa: E402


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
        for invalid in ("", "../job", "job/other", "作业"):
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


if __name__ == "__main__":
    unittest.main()
