import errno
import io
import json
import socket
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
        self.calls = 0
        self.polls = 0
        self.generation_polls = 0
        self.authorization = ""
        self.user_agent = ""
        self.last_payload = None
        self.last_path = ""

    def open(self, request, timeout):
        self.calls += 1
        self.authorization = request.get_header("Authorization", "")
        self.user_agent = request.get_header("User-agent", "")
        path = request.full_url.split("//", 1)[-1]
        path = path[path.find("/"):] if "/" in path else "/"
        self.last_path = path
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
                    "data": _proposal_job("running"),
                })
            return _Response(200, {
                "ok": True,
                "data": _proposal_job("succeeded"),
            })
        if request.get_method() == "DELETE" and path == "/v1/jobs/job.fixture":
            return _Response(200, {
                "ok": True,
                "data": _proposal_job("canceled"),
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
                    "data": {
                        "job_id": "gen.fixture",
                        "request_id": "generation.fixture",
                        "status": "running",
                    },
                })
            return _Response(200, {
                "ok": True,
                "data": {
                    "job_id": "gen.fixture",
                    "request_id": "generation.fixture",
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
                "data": {
                    "job_id": "gen.fixture",
                    "request_id": "generation.fixture",
                    "status": "canceled",
                },
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


def _valid_proposal(request=None, **changes):
    request = request or _proposal_request()
    proposal = {
        "id": "proposal.fixture",
        "session_id": request["session_id"],
        "request_id": request["request_id"],
        "actor_id": request["actor_id"],
        "tick": request["tick"],
        "action": json.loads(json.dumps(request["candidate_actions"][0])),
        "policy_source": "deterministic",
    }
    proposal.update(changes)
    return proposal


def _proposal_job(
    status,
    *,
    request=None,
    job_id="job.fixture",
    proposal=None,
    error=None,
):
    request = request or _proposal_request()
    job = {
        "job_id": job_id,
        "session_id": request["session_id"],
        "request_id": request["request_id"],
        "status": status,
    }
    if status == "succeeded":
        job["proposal"] = proposal if proposal is not None else _valid_proposal(request)
    if error is not None:
        job["error"] = error
    return job


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


class _AdvancingClock:
    def __init__(self):
        self.value = 0.0

    def now(self):
        return self.value

    def sleep(self, seconds):
        self.value += seconds


class RinClientTests(unittest.TestCase):
    def test_contract_versions_and_user_agent(self):
        self.assertEqual(rin_client.SDK_VERSION, "0.6.0")
        client = _client_with_opener()
        result = client.health()
        self.assertEqual(result["status"], "ok")
        self.assertEqual(client._opener.user_agent, "rin-renpy/" + rin_client.SDK_VERSION)

    def test_living_world_routes(self):
        client = _client_with_opener()
        cases = (
            (client.commit_batch, "/v1/action/commit-batch"),
            (client.set_actor_activity, "/v1/session/activity"),
            (client.arbitrate, "/v1/world/arbitrate"),
            (client.timeline, "/v1/session/timeline"),
            (client.replay, "/v1/session/replay"),
        )
        for method, expected_path in cases:
            with self.subTest(path=expected_path):
                method({"protocol_version": rin_client.PROTOCOL_VERSION})
                self.assertEqual(client._opener.last_path, expected_path)

    def test_false_commit_flags_are_serialized(self):
        client = _client_with_opener()
        client.commit({"accepted": False})
        self.assertIn("accepted", client._opener.last_payload)
        self.assertIs(client._opener.last_payload["accepted"], False)
        client.commit_batch({"items": [{"accepted": False}]})
        item = client._opener.last_payload["items"][0]
        self.assertIn("accepted", item)
        self.assertIs(item["accepted"], False)

    def test_invalid_json_numbers_cycles_and_depth_fail_before_transport(self):
        client = _client_with_opener()
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
                with self.assertRaises(rin_client.RinProtocolError) as caught:
                    client.commit(payload)
                self.assertEqual(caught.exception.code, "invalid_request")
        self.assertEqual(client._opener.calls, 0)

    def test_nonfinite_response_number_is_rejected(self):
        client = rin_client.RinClient()

        class NonfiniteOpener:
            def open(self, request, timeout):
                del request, timeout
                return _Response(200, {"ok": True, "data": {"value": float("nan")}})

        client._opener = NonfiniteOpener()
        with self.assertRaises(rin_client.RinProtocolError) as caught:
            client.health()
        self.assertEqual(caught.exception.code, "invalid_response")

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

    def test_definite_connection_refusal_uses_authored_fallback(self):
        client = rin_client.RinClient()

        class FailingOpener:
            def open(self, request, timeout):
                raise URLError(ConnectionRefusedError(errno.ECONNREFUSED, "connection refused"))

        client._opener = FailingOpener()
        result = client.propose_with_fallback(
            _proposal_request(),
            fallback_action_id="wait",
        )
        self.assertEqual(result["source"], "offline")
        self.assertFalse(result["committable"])
        self.assertEqual(result["fallback_reason"], "transport_unavailable")
        self.assertEqual(result["proposal"]["action"]["id"], "wait")
        self.assertEqual(result["proposal"]["policy_source"], "adapter-offline")
        self.assertNotIn("fixture-token", json.dumps(result))

    def test_ambiguous_submission_timeout_does_not_execute_fallback(self):
        client = rin_client.RinClient()

        class TimingOutOpener:
            def open(self, request, timeout):
                raise URLError(socket.timeout("response was lost"))

        client._opener = TimingOutOpener()
        with self.assertRaises(rin_client.RinJobError) as caught:
            client.propose_with_fallback(
                _proposal_request(),
                fallback_action_id="wait",
            )
        self.assertEqual(caught.exception.code, "proposal_outcome_unknown")

    def test_gateway_error_after_submission_does_not_execute_fallback(self):
        client = rin_client.RinClient()

        class GatewayOpener:
            def open(self, request, timeout):
                payload = json.dumps({
                    "ok": False,
                    "error": {"code": "gateway_timeout", "message": "Upstream response was lost"},
                }).encode("utf-8")
                raise HTTPError(
                    request.full_url,
                    504,
                    "Gateway Timeout",
                    {"Content-Length": str(len(payload))},
                    io.BytesIO(payload),
                )

        client._opener = GatewayOpener()
        with self.assertRaises(rin_client.RinJobError) as caught:
            client.propose_with_fallback(
                _proposal_request(),
                fallback_action_id="wait",
            )
        self.assertEqual(caught.exception.status, 504)
        self.assertEqual(caught.exception.code, "proposal_outcome_unknown")

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

    def test_background_registry_retains_and_recovers_unresolved_attempts(self):
        for unresolved_code in (
            "proposal_outcome_unknown",
            "job_outcome_unknown",
            "job_cancel_unconfirmed",
        ):
            with self.subTest(code=unresolved_code):
                class RecoveringClient:
                    def __init__(self):
                        self.calls = []

                    def propose_with_fallback(self, request, **options):
                        self.calls.append({
                            "request": json.loads(json.dumps(request)),
                            "known_job_id": options.get("known_job_id", ""),
                        })
                        retain = options["persist_job_id"]
                        if len(self.calls) == 1:
                            self.assert_retained = retain("job.retained")
                            raise rin_client.RinJobError(
                                unresolved_code,
                                "Outcome remains unresolved",
                                job_id="job.retained",
                            )
                        return {
                            "source": "sidecar",
                            "committable": True,
                            "fallback_reason": "",
                            "job_id": "job.retained",
                            "proposal": {"id": "proposal.recovered"},
                        }

                recovering = RecoveringClient()
                registry = rin_client.BackgroundProposalRegistry(recovering, maximum=1)
                request = _proposal_request()
                request_id = registry.schedule(request, lambda worker: worker())

                self.assertTrue(recovering.assert_retained)
                self.assertEqual(registry.status(request_id), "unresolved")
                self.assertIsNone(registry.consume(request_id))
                attempt = registry.attempt(request_id)
                self.assertEqual(attempt["request"], request)
                self.assertEqual(attempt["job_id"], "job.retained")
                self.assertEqual(attempt["error_code"], unresolved_code)
                json.dumps(attempt)

                other = dict(request)
                other["request_id"] = "request.other"
                with self.assertRaises(rin_client.RinProtocolError) as full:
                    registry.schedule(other, lambda worker: worker())
                self.assertEqual(full.exception.code, "registry_full")

                registry.schedule(request, lambda worker: worker())
                self.assertEqual(registry.status(request_id), "complete")
                self.assertEqual(recovering.calls[1]["known_job_id"], "job.retained")
                consumed = registry.consume(request_id)
                self.assertEqual(consumed["result"]["proposal"]["id"], "proposal.recovered")
                self.assertEqual(consumed["job_id"], "job.retained")

    def test_empty_job_attempt_resume_never_uses_offline_fallback(self):
        request = _proposal_request()

        # The game persists this pending record before the worker's first POST,
        # then the process exits before the worker starts.
        original = rin_client.BackgroundProposalRegistry(rin_client.RinClient())
        request_id = original.schedule(request, lambda _worker: None)
        persisted = original.attempt(request_id)
        self.assertEqual(persisted["job_id"], "")
        self.assertFalse(persisted["allow_offline_before_submit"])

        class RefusingOpener:
            def open(self, _request, timeout):
                raise URLError(
                    ConnectionRefusedError(errno.ECONNREFUSED, "connection refused")
                )

        restarted_client = rin_client.RinClient()
        restarted_client._opener = RefusingOpener()
        restarted = rin_client.BackgroundProposalRegistry(restarted_client)
        restarted.schedule(
            persisted["request"],
            lambda worker: worker(),
            fallback_action_id=persisted["fallback_action_id"],
            known_job_id=persisted["job_id"],
            allow_offline_before_submit=persisted["allow_offline_before_submit"],
        )

        self.assertEqual(restarted.status(request_id), "unresolved")
        self.assertIsNone(restarted.consume(request_id))
        unresolved = restarted.attempt(request_id)
        self.assertEqual(unresolved["request"], request)
        self.assertEqual(unresolved["job_id"], "")
        self.assertEqual(unresolved["error_code"], "proposal_outcome_unknown")

        # A later exact-request resume may recover normally, still with offline
        # disabled because it is the same durable attempt.
        restarted_client._opener = _Opener()
        restarted.schedule(request, lambda worker: worker())
        self.assertEqual(restarted.status(request_id), "complete")
        recovered = restarted.consume(request_id)
        self.assertEqual(recovered["result"]["source"], "sidecar")

    def test_known_job_not_found_reposts_exact_request_once(self):
        client = rin_client.RinClient()
        request = _proposal_request()
        gets = []
        submissions = []
        retained = []

        def get_job(job_id):
            gets.append(job_id)
            if job_id == "job.previous":
                raise rin_client.RinAPIError(
                    "job_not_found",
                    "Job expired",
                    status=404,
                )
            return _proposal_job(
                "succeeded",
                request=request,
                job_id="job.recovered",
                proposal=_valid_proposal(request, id="proposal.recovered"),
            )

        def submit(payload):
            submissions.append(json.loads(json.dumps(payload)))
            return {"job_id": "job.recovered"}

        client.get_proposal_job = get_job
        client.submit_proposal_job = submit
        result = client.propose_with_fallback(
            request,
            known_job_id="job.previous",
            persist_job_id=lambda job_id: retained.append(job_id) or True,
        )

        self.assertEqual(gets, ["job.previous", "job.recovered"])
        self.assertEqual(submissions, [request])
        self.assertEqual(retained, ["job.recovered"])
        self.assertEqual(result["proposal"]["id"], "proposal.recovered")

    def test_terminal_unknown_reposts_once_then_remains_unresolved(self):
        client = rin_client.RinClient()
        request = _proposal_request()
        submissions = []
        gets = []

        def get_job(job_id):
            gets.append(job_id)
            return _proposal_job(
                "failed",
                request=request,
                job_id=job_id,
                error={
                    "code": "proposal_outcome_unknown",
                    "message": "Durability confirmation is still unknown",
                },
            )

        client.get_proposal_job = get_job
        client.submit_proposal_job = lambda payload: (
            submissions.append(json.loads(json.dumps(payload)))
            or {"job_id": "job.recovered"}
        )

        with self.assertRaises(rin_client.RinJobError) as caught:
            client.propose_with_fallback(
                request,
                known_job_id="job.previous",
            )

        self.assertEqual(caught.exception.code, "proposal_outcome_unknown")
        self.assertEqual(caught.exception.job_id, "job.recovered")
        self.assertEqual(submissions, [request])
        self.assertEqual(gets, ["job.previous", "job.recovered"])

    def test_proposal_job_identity_mismatches_fail_closed_for_every_status(self):
        request = _proposal_request()
        for status in ("queued", "running", "failed", "stale", "canceled", "succeeded"):
            for field, wrong_value in (
                ("job_id", "job.crossed"),
                ("session_id", "session.crossed"),
                ("request_id", "request.crossed"),
            ):
                with self.subTest(status=status, field=field):
                    client = rin_client.RinClient()
                    job = _proposal_job(
                        status,
                        request=request,
                        error=(
                            {"code": "state_changed", "message": "Terminal"}
                            if status in ("failed", "stale", "canceled")
                            else None
                        ),
                    )
                    job[field] = wrong_value
                    client.get_proposal_job = lambda _job_id, value=job: value
                    client.submit_proposal_job = lambda _request: self.fail(
                        "identity mismatch must not trigger a recovery POST"
                    )

                    with self.assertRaises(rin_client.RinJobError) as caught:
                        client.propose_with_fallback(
                            request,
                            known_job_id="job.fixture",
                        )

                    self.assertEqual(caught.exception.code, "job_outcome_unknown")
                    self.assertEqual(caught.exception.job_id, "job.fixture")

    def test_public_wait_without_request_validates_a_self_consistent_result(self):
        client = rin_client.RinClient()
        request = _proposal_request()
        client.get_proposal_job = lambda _job_id: _proposal_job(
            "succeeded",
            request=request,
        )
        job = client.wait_for_proposal("job.fixture")
        self.assertEqual(job["proposal"]["id"], "proposal.fixture")

        crossed = _valid_proposal(request, request_id="request.other")
        client.get_proposal_job = lambda _job_id: _proposal_job(
            "succeeded",
            request=request,
            proposal=crossed,
        )
        with self.assertRaises(rin_client.RinJobError) as caught:
            client.wait_for_proposal("job.fixture")
        self.assertEqual(caught.exception.code, "job_outcome_unknown")

    def test_successful_proposal_identity_and_numeric_mismatches_fail_closed(self):
        request = _proposal_request()
        cases = (
            ("empty_id", {"id": ""}),
            ("wrong_session", {"session_id": "session.crossed"}),
            ("wrong_request", {"request_id": "request.crossed"}),
            ("wrong_actor", {"actor_id": "npc.crossed"}),
            ("bool_tick", {"tick": True}),
            ("float_tick", {"tick": 2.0}),
            ("oversized_tick", {"tick": rin_client.MAX_JSON_SAFE_INTEGER + 1}),
            ("negative_tick", {"tick": -1}),
            ("missing_action", {"action": {}}),
            ("non_candidate_action", {
                "action": {
                    "id": "attack",
                    "kind": "combat",
                    "description": "Attack",
                },
            }),
            ("mutated_candidate_action", {
                "action": {
                    "id": "talk",
                    "kind": "dialogue",
                    "description": "Different semantics",
                },
            }),
        )
        for name, changes in cases:
            with self.subTest(case=name):
                client = rin_client.RinClient()
                job = _proposal_job(
                    "succeeded",
                    request=request,
                    proposal=_valid_proposal(request, **changes),
                )
                client.get_proposal_job = lambda _job_id, value=job: value
                client.submit_proposal_job = lambda _request: self.fail(
                    "malformed success must not trigger a recovery POST"
                )

                with self.assertRaises(rin_client.RinJobError) as caught:
                    client.propose_with_fallback(
                        request,
                        known_job_id="job.fixture",
                    )

                self.assertEqual(caught.exception.code, "job_outcome_unknown")
                self.assertEqual(caught.exception.job_id, "job.fixture")

    def test_registry_retains_crossed_job_as_unresolved(self):
        request = _proposal_request()
        client = rin_client.RinClient()
        crossed = _proposal_job("running", request=request)
        crossed["session_id"] = "session.crossed"
        client.get_proposal_job = lambda _job_id: crossed
        registry = rin_client.BackgroundProposalRegistry(client, maximum=1)

        request_id = registry.schedule(
            request,
            lambda worker: worker(),
            known_job_id="job.fixture",
        )

        self.assertEqual(registry.status(request_id), "unresolved")
        self.assertIsNone(registry.consume(request_id))
        attempt = registry.attempt(request_id)
        self.assertEqual(attempt["request"], request)
        self.assertEqual(attempt["job_id"], "job.fixture")
        self.assertEqual(attempt["error_code"], "job_outcome_unknown")

    def test_delete_race_malformed_success_remains_unknown(self):
        request = _proposal_request()
        client = rin_client.RinClient()
        canceled = threading.Event()
        canceled.set()
        client.submit_proposal_job = lambda _request: {"job_id": "job.fixture"}
        client.cancel_proposal_job = lambda _job_id: _proposal_job(
            "succeeded",
            request=request,
            proposal=_valid_proposal(request, tick=True),
        )

        with self.assertRaises(rin_client.RinJobError) as caught:
            client.propose_with_fallback(
                request,
                fallback_action_id="wait",
                cancel_event=canceled,
            )

        self.assertEqual(caught.exception.code, "job_outcome_unknown")
        self.assertEqual(caught.exception.job_id, "job.fixture")

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

    def test_explicit_cancellation_consumes_raced_success(self):
        client = rin_client.RinClient()
        request = _proposal_request()
        canceled = threading.Event()
        canceled.set()
        client.cancel_proposal_job = lambda _job_id: _proposal_job(
            "succeeded",
            request=request,
            proposal=_valid_proposal(request, id="proposal.cancel-race"),
        )
        proposal_job = client.wait_for_proposal(
            "job.fixture",
            cancel_event=canceled,
            expected_request=request,
        )
        self.assertEqual(proposal_job["proposal"]["id"], "proposal.cancel-race")

        client.cancel_generation_job = lambda _job_id: {
            "job_id": "gen.fixture",
            "request_id": "generation.fixture",
            "status": "succeeded",
            "result": {"content": "finished before cancellation"},
        }
        generation_job = client.wait_for_generation("gen.fixture", cancel_event=canceled)
        self.assertEqual(generation_job["result"]["content"], "finished before cancellation")

    def test_explicit_cancellation_reports_unconfirmed_transport(self):
        client = rin_client.RinClient()
        canceled = threading.Event()
        canceled.set()

        def fail_cancel(_job_id):
            raise rin_client.RinTransportError("transport_failed", "Unavailable")

        client.cancel_proposal_job = fail_cancel
        with self.assertRaises(rin_client.RinJobError) as caught:
            client.wait_for_proposal("job.fixture", cancel_event=canceled)
        self.assertEqual(caught.exception.code, "job_cancel_unconfirmed")

    def test_explicit_cancellation_never_turns_terminal_failure_into_fallback(self):
        client = rin_client.RinClient()
        request = _proposal_request()
        canceled = threading.Event()
        canceled.set()
        client.submit_proposal_job = lambda _request: {"job_id": "job.fixture"}
        client.cancel_proposal_job = lambda _job_id: _proposal_job(
            "stale",
            request=request,
            error={"code": "state_changed", "message": "World changed"},
        )
        with self.assertRaises(rin_client.RinJobError) as caught:
            client.propose_with_fallback(
                request,
                fallback_action_id="wait",
                cancel_event=canceled,
            )
        self.assertEqual(caught.exception.code, "state_changed")

    def test_terminal_unknown_outcome_from_poll_never_executes_fallback(self):
        client = rin_client.RinClient()
        request = _proposal_request()
        client.submit_proposal_job = lambda _request: {"job_id": "job.fixture"}
        client.get_proposal_job = lambda _job_id: _proposal_job(
            "failed",
            request=request,
            error={
                "code": "proposal_outcome_unknown",
                "message": "Provider outcome could not be established",
            },
        )

        with self.assertRaises(rin_client.RinJobError) as caught:
            client.propose_with_fallback(
                request,
                fallback_action_id="wait",
            )

        self.assertEqual(caught.exception.code, "proposal_outcome_unknown")

    def test_terminal_unknown_outcome_from_delete_never_executes_fallback(self):
        clock = _AdvancingClock()
        client = rin_client.RinClient(clock=clock.now, sleeper=clock.sleep)
        request = _proposal_request()
        client.submit_proposal_job = lambda _request: {"job_id": "job.fixture"}
        client.get_proposal_job = lambda _job_id: _proposal_job(
            "running",
            request=request,
        )
        client.cancel_proposal_job = lambda _job_id: _proposal_job(
            "failed",
            request=request,
            error={
                "code": "proposal_outcome_unknown",
                "message": "Cancellation found an indeterminate provider outcome",
            },
        )

        with self.assertRaises(rin_client.RinJobError) as caught:
            client.propose_with_fallback(
                request,
                fallback_action_id="wait",
                deadline_seconds=0.05,
                poll_interval=0.01,
            )

        self.assertEqual(caught.exception.code, "proposal_outcome_unknown")

    def test_failed_proposal_error_code_must_be_an_exact_protocol_id(self):
        request = _proposal_request()
        invalid_codes = (7, "", "job_canceled\x00", "x" * 97, " job_canceled ")
        for route in ("poll", "cancel"):
            for error_code in invalid_codes:
                with self.subTest(route=route, error_code=repr(error_code)):
                    client = rin_client.RinClient()
                    canceled = threading.Event()
                    if route == "cancel":
                        canceled.set()
                        client.cancel_proposal_job = lambda _job_id, code=error_code: _proposal_job(
                            "failed",
                            request=request,
                            error={"code": code, "message": "Malformed terminal error"},
                        )
                    else:
                        client.get_proposal_job = lambda _job_id, code=error_code: _proposal_job(
                            "failed",
                            request=request,
                            error={"code": code, "message": "Malformed terminal error"},
                        )

                    with self.assertRaises(rin_client.RinJobError) as caught:
                        client.propose_with_fallback(
                            request,
                            known_job_id="job.fixture",
                            fallback_action_id="wait",
                            cancel_event=canceled if route == "cancel" else None,
                        )

                    self.assertEqual(caught.exception.code, "job_outcome_unknown")
                    self.assertEqual(caught.exception.job_id, "job.fixture")

    def test_timeout_consumes_proposal_cancel_race_result(self):
        clock = _AdvancingClock()
        client = rin_client.RinClient(clock=clock.now, sleeper=clock.sleep)
        request = _proposal_request()
        client.get_proposal_job = lambda _job_id: _proposal_job(
            "running",
            request=request,
        )
        client.cancel_proposal_job = lambda _job_id: _proposal_job(
            "succeeded",
            request=request,
            proposal=_valid_proposal(request, id="proposal.race"),
        )

        job = client.wait_for_proposal(
            "job.fixture",
            deadline_seconds=0.05,
            poll_interval=0.01,
            expected_request=request,
        )

        self.assertEqual(job["proposal"]["id"], "proposal.race")

    def test_timeout_consumes_generation_cancel_race_result(self):
        clock = _AdvancingClock()
        client = rin_client.RinClient(clock=clock.now, sleeper=clock.sleep)
        client.get_generation_job = lambda _job_id: {
            "job_id": "gen.fixture",
            "request_id": "generation.fixture",
            "status": "queued",
        }
        client.cancel_generation_job = lambda _job_id: {
            "job_id": "gen.fixture",
            "request_id": "generation.fixture",
            "status": "succeeded",
            "result": {"content": "finished at the deadline"},
        }

        job = client.wait_for_generation(
            "gen.fixture",
            deadline_seconds=0.05,
            poll_interval=0.01,
        )

        self.assertEqual(job["result"]["content"], "finished at the deadline")

    def test_generation_job_identity_is_bound_on_get_and_delete(self):
        class CrossedOpener:
            def open(self, _request, timeout):
                return _Response(200, {
                    "ok": True,
                    "data": {
                        "job_id": "gen.crossed",
                        "request_id": "generation.fixture",
                        "status": "running",
                    },
                })

        for method_name in ("get_generation_job", "cancel_generation_job"):
            with self.subTest(method=method_name, direct=True):
                client = rin_client.RinClient()
                client._opener = CrossedOpener()
                with self.assertRaises(rin_client.RinProtocolError) as caught:
                    getattr(client, method_name)("gen.fixture")
                self.assertEqual(caught.exception.code, "invalid_job")

        for status in ("queued", "running", "failed", "succeeded"):
            with self.subTest(method="GET", status=status):
                client = rin_client.RinClient()
                job = {
                    "job_id": "gen.crossed",
                    "request_id": "generation.fixture",
                    "status": status,
                }
                if status == "failed":
                    job["error"] = {"code": "generation_failed"}
                if status == "succeeded":
                    job["result"] = {"content": "crossed"}
                client.get_generation_job = lambda _job_id, value=job: value
                with self.assertRaises(rin_client.RinProtocolError) as caught:
                    client.wait_for_generation("gen.fixture")
                self.assertEqual(caught.exception.code, "invalid_job")

        client = rin_client.RinClient()
        canceled = threading.Event()
        canceled.set()
        client.cancel_generation_job = lambda _job_id: {
            "job_id": "gen.crossed",
            "request_id": "generation.fixture",
            "status": "succeeded",
            "result": {"content": "crossed"},
        }
        with self.assertRaises(rin_client.RinProtocolError) as caught:
            client.wait_for_generation("gen.fixture", cancel_event=canceled)
        self.assertEqual(caught.exception.code, "invalid_job")

    def test_generation_success_result_structure_is_strict(self):
        invalid_results = (
            ("missing_content", {}),
            ("non_string", {"content": 7}),
            ("empty", {"content": ""}),
            ("whitespace", {"content": " \t\r\n"}),
            ("nul", {"content": "ok\x00"}),
            ("invalid_utf8", {"content": "\ud800"}),
            (
                "too_large",
                {"content": "x" * (rin_client.MAX_GENERATION_CONTENT_BYTES + 1)},
            ),
            ("model_type", {"content": "ok", "model": 7}),
            ("prompt_tokens_bool", {"content": "ok", "prompt_tokens": True}),
            ("negative_output_tokens", {"content": "ok", "output_tokens": -1}),
            ("cache_hit_type", {"content": "ok", "cache_hit": "yes"}),
        )
        for case, result in invalid_results:
            with self.subTest(case=case):
                client = rin_client.RinClient()
                client.get_generation_job = lambda _job_id, value=result: {
                    "job_id": "gen.fixture",
                    "request_id": "generation.fixture",
                    "status": "succeeded",
                    "result": value,
                }
                with self.assertRaises(rin_client.RinProtocolError) as caught:
                    client.wait_for_generation("gen.fixture")
                self.assertEqual(caught.exception.code, "invalid_job")

        class MalformedSuccessOpener:
            def open(self, _request, timeout):
                return _Response(200, {
                    "ok": True,
                    "data": {
                        "job_id": "gen.fixture",
                        "request_id": "generation.fixture",
                        "status": "succeeded",
                        "result": {"content": 7},
                    },
                })

        client = rin_client.RinClient()
        client._opener = MalformedSuccessOpener()
        with self.assertRaises(rin_client.RinProtocolError) as caught:
            client.get_generation_job("gen.fixture")
        self.assertEqual(caught.exception.code, "invalid_job")

    def test_generation_job_request_id_is_a_protocol_identifier(self):
        invalid_request_ids = (None, 7, "", "generation\x00fixture", "x" * 97, " bad ")
        for request_id in invalid_request_ids:
            with self.subTest(request_id=repr(request_id)):
                client = rin_client.RinClient()
                client.get_generation_job = lambda _job_id, value=request_id: {
                    "job_id": "gen.fixture",
                    "request_id": value,
                    "status": "running",
                }
                with self.assertRaises(rin_client.RinProtocolError) as caught:
                    client.wait_for_generation("gen.fixture")
                self.assertEqual(caught.exception.code, "invalid_job")

    def test_generate_json_rejects_non_finite_constants(self):
        for content in ('{"value":NaN}', '{"value":Infinity}', '{"value":-Infinity}'):
            with self.subTest(content=content):
                client = rin_client.RinClient()
                client.submit_generation_job = lambda _request: {"job_id": "gen.fixture"}
                client.wait_for_generation = lambda *_args, value=content, **_kwargs: {
                    "job_id": "gen.fixture",
                    "request_id": "generation.fixture",
                    "status": "succeeded",
                    "result": {"content": value},
                }
                with self.assertRaises(rin_client.RinProtocolError) as caught:
                    client.generate_json(_generation_request())
                self.assertEqual(caught.exception.code, "invalid_generation_json")

    def test_timeout_uses_cancel_terminal_state_and_validates_raced_success(self):
        clock = _AdvancingClock()
        client = rin_client.RinClient(clock=clock.now, sleeper=clock.sleep)
        request = _proposal_request()
        client.get_proposal_job = lambda _job_id: _proposal_job(
            "running",
            request=request,
        )
        client.cancel_proposal_job = lambda _job_id: _proposal_job(
            "canceled",
            request=request,
            error={"code": "job_canceled", "message": "Canceled"},
        )
        with self.assertRaises(rin_client.RinJobError) as caught:
            client.wait_for_proposal(
                "job.fixture",
                deadline_seconds=0.05,
                poll_interval=0.01,
                expected_request=request,
            )
        self.assertEqual(caught.exception.code, "job_canceled")

        clock.value = 0.0
        client.cancel_proposal_job = lambda _job_id: _proposal_job(
            "succeeded",
            request=request,
            proposal={},
        )
        with self.assertRaises(rin_client.RinJobError) as caught:
            client.wait_for_proposal(
                "job.fixture",
                deadline_seconds=0.05,
                poll_interval=0.01,
                expected_request=request,
            )
        self.assertEqual(caught.exception.code, "job_outcome_unknown")

    def test_timeout_follows_cancel_api_rin_error(self):
        clock = _AdvancingClock()
        client = rin_client.RinClient(clock=clock.now, sleeper=clock.sleep)
        client.get_generation_job = lambda _job_id: {
            "job_id": "gen.fixture",
            "request_id": "generation.fixture",
            "status": "running",
        }

        def fail_cancel(_job_id):
            raise rin_client.RinTransportError("transport_failed", "Unavailable")

        client.cancel_generation_job = fail_cancel
        with self.assertRaises(rin_client.RinJobError) as caught:
            client.wait_for_generation(
                "gen.fixture",
                deadline_seconds=0.05,
                poll_interval=0.01,
            )
        self.assertEqual(caught.exception.code, "job_timeout")

    def test_unconfirmed_timeout_does_not_execute_fallback(self):
        clock = _AdvancingClock()
        client = rin_client.RinClient(clock=clock.now, sleeper=clock.sleep)
        request = _proposal_request()
        client.submit_proposal_job = lambda _request: {"job_id": "job.fixture"}
        client.get_proposal_job = lambda _job_id: _proposal_job(
            "running",
            request=request,
        )

        def fail_cancel(_job_id):
            raise rin_client.RinTransportError("transport_failed", "response lost")

        client.cancel_proposal_job = fail_cancel
        with self.assertRaises(rin_client.RinJobError) as caught:
            client.propose_with_fallback(
                request,
                fallback_action_id="wait",
                deadline_seconds=0.05,
                poll_interval=0.01,
            )
        self.assertEqual(caught.exception.code, "job_outcome_unknown")

    def test_response_size_limit_is_enforced(self):
        client = rin_client.RinClient(max_response_bytes=1024)

        class LargeOpener:
            def open(self, request, timeout):
                return _Response(200, {"ok": True, "data": {"value": "x" * 2048}})

        client._opener = LargeOpener()
        with self.assertRaises(rin_client.RinProtocolError) as caught:
            client.health()
        self.assertEqual(caught.exception.code, "response_too_large")

    def test_default_response_limit_matches_inline_transport_budget(self):
        self.assertEqual(rin_client.DEFAULT_MAX_RESPONSE_BYTES, 32 * 1024 * 1024)
        self.assertEqual(
            rin_client.RinClient().max_response_bytes,
            rin_client.DEFAULT_MAX_RESPONSE_BYTES,
        )

    def test_renpy_bridge_python_block_parses(self):
        source = Path(__file__).with_name("rin_bridge.rpy").read_text(encoding="utf-8")
        marker = "init -30 python:\n"
        self.assertIn(marker, source)
        python_source = textwrap.dedent(source.split(marker, 1)[1])
        ast.parse(python_source)
        self.assertNotIn("default _RIN_", source)
        self.assertIn("def rin_proposal_attempt(", source)
        self.assertIn("def rin_resume_proposal(", source)
        self.assertIn("resuming=True", source)
        self.assertIn("allow_offline_before_submit=False", source)


if __name__ == "__main__":
    unittest.main()
