"""Dependency-free Rin Protocol v2 client for Ren'Py and regular Python.

The client deliberately keeps threads, cancellation events, and transport state
outside Ren'Py saves. Call ``run_proposal_job`` from a background worker,
then store only its returned JSON-compatible dictionary on the main thread.
"""

from __future__ import annotations

import errno
import hashlib
import ipaddress
import json
import math
import socket
import threading
import time
from typing import Any, Callable, Dict, Optional, Sequence
from urllib.error import HTTPError, URLError
from urllib.parse import urlsplit, urlunsplit
from urllib.request import HTTPRedirectHandler, HTTPSHandler, Request, build_opener


SDK_VERSION = "0.7.0"
PROTOCOL_VERSION = "rin.protocol/v2"
DEFAULT_BASE_URL = "http://127.0.0.1:7374"
DEFAULT_MAX_RESPONSE_BYTES = 32 * 1024 * 1024
MAX_GENERATION_CONTENT_BYTES = 4 * 1024 * 1024
MAX_JSON_SAFE_INTEGER = 9_007_199_254_740_991
MAX_JSON_DEPTH = 64
TERMINAL_JOB_STATES = frozenset((
    "succeeded",
    "failed",
    "stale",
    "canceled",
))
UNRESOLVED_PROPOSAL_CODES = frozenset((
    "proposal_outcome_unknown",
    "job_outcome_unknown",
    "job_cancel_unconfirmed",
    "job_id_persistence_failed",
    "job_timeout",
))


class RinError(RuntimeError):
    """Base error with a safe machine-readable code."""

    def __init__(self, code: str, message: str) -> None:
        self.code = _safe_text(code, 96) or "rin_error"
        self.safe_message = _safe_text(message, 500) or "Rin request failed"
        super().__init__(self.safe_message)


class RinConfigurationError(RinError):
    pass


class RinTransportError(RinError):
    pass


class RinProtocolError(RinError):
    pass


class RinAPIError(RinError):
    def __init__(
        self,
        code: str,
        message: str,
        *,
        status: int = 0,
        field: str = "",
    ) -> None:
        self.status = int(status or 0)
        self.field = _safe_text(field, 160)
        super().__init__(code, message)


class RinJobError(RinAPIError):
    def __init__(
        self,
        code: str,
        message: str,
        *,
        status: int = 0,
        field: str = "",
        job_id: str = "",
    ) -> None:
        self.job_id = _safe_text(job_id, 96)
        super().__init__(code, message, status=status, field=field)


class _NoRedirectHandler(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def _safe_text(value: Any, maximum: int) -> str:
    return " ".join(str(value or "").replace("\x00", "").split())[:maximum]


def _is_loopback(hostname: str) -> bool:
    if hostname.casefold() == "localhost":
        return True
    try:
        return ipaddress.ip_address(hostname).is_loopback
    except ValueError:
        return False


def _normalize_base_url(value: str, token: str) -> str:
    raw = str(value or DEFAULT_BASE_URL).strip().rstrip("/")
    parsed = urlsplit(raw)
    try:
        port = parsed.port
    except ValueError as exc:
        raise RinConfigurationError("invalid_base_url", "Rin base URL has an invalid port") from exc
    if (
        parsed.scheme not in ("http", "https")
        or not parsed.hostname
        or parsed.username is not None
        or parsed.password is not None
        or parsed.query
        or parsed.fragment
        or parsed.path not in ("", "/")
    ):
        raise RinConfigurationError(
            "invalid_base_url",
            "Rin base URL must be an origin without credentials, path, query, or fragment",
        )
    if port is not None and not 1 <= port <= 65535:
        raise RinConfigurationError("invalid_base_url", "Rin base URL has an invalid port")
    loopback = _is_loopback(parsed.hostname)
    if parsed.scheme == "http" and not loopback:
        raise RinConfigurationError(
            "insecure_base_url",
            "Non-loopback Rin endpoints must use HTTPS",
        )
    if not loopback and not token:
        raise RinConfigurationError(
            "missing_token",
            "Non-loopback Rin endpoints require a bearer token",
        )
    return urlunsplit((parsed.scheme, parsed.netloc, "", "", "")).rstrip("/")


def _validate_token(value: str) -> str:
    token = str(value or "")
    if token != token.strip() or any(character in token for character in ("\x00", "\r", "\n")):
        raise RinConfigurationError("invalid_token", "Rin token must be a trimmed single-line value")
    if len(token) > 4096:
        raise RinConfigurationError("invalid_token", "Rin token is too long")
    return token


def _json_clone(value: Any) -> Any:
    return json.loads(json.dumps(value, ensure_ascii=False, separators=(",", ":")))


def _error_from_envelope(payload: Any, status: int) -> RinAPIError:
    detail = payload.get("error", {}) if isinstance(payload, dict) else {}
    if not isinstance(detail, dict):
        detail = {}
    return RinAPIError(
        _safe_text(detail.get("code"), 96) or "http_error",
        _safe_text(detail.get("message"), 500) or "Rin request failed",
        status=status,
        field=_safe_text(detail.get("field"), 160),
    )


class RinClient:
    """Strict JSON/HTTP client for a Rin Sidecar."""

    def __init__(
        self,
        base_url: str = DEFAULT_BASE_URL,
        *,
        token: str = "",
        timeout: float = 5.0,
        max_response_bytes: int = DEFAULT_MAX_RESPONSE_BYTES,
        ca_file: str = "",
        clock: Callable[[], float] = time.monotonic,
        sleeper: Callable[[float], None] = time.sleep,
    ) -> None:
        self.token = _validate_token(token)
        self.base_url = _normalize_base_url(base_url, self.token)
        self.timeout = float(timeout)
        if not 0.05 <= self.timeout <= 120.0:
            raise RinConfigurationError("invalid_timeout", "Rin timeout must be between 0.05 and 120 seconds")
        self.max_response_bytes = int(max_response_bytes)
        if not 1024 <= self.max_response_bytes <= 32 * 1024 * 1024:
            raise RinConfigurationError(
                "invalid_response_limit",
                "Rin response limit must be between 1 KiB and 32 MiB",
            )
        handlers = [_NoRedirectHandler()]
        if ca_file:
            import ssl

            handlers.append(HTTPSHandler(context=ssl.create_default_context(cafile=ca_file)))
        self._opener = build_opener(*handlers)
        self._clock = clock
        self._sleeper = sleeper

    def health(self) -> Dict[str, Any]:
        return self._request("GET", "/health")

    def create_session(self, request: Dict[str, Any]) -> Dict[str, Any]:
        return self._request("POST", "/v2/session/create", request)

    def observe(self, request: Dict[str, Any]) -> Dict[str, Any]:
        return self._request("POST", "/v2/session/observe", request)

    def propose(self, request: Dict[str, Any]) -> Dict[str, Any]:
        return self._request("POST", "/v2/agent/propose", request)

    def submit_proposal_job(self, request: Dict[str, Any]) -> Dict[str, Any]:
        return self._request("POST", "/v2/jobs/propose", request, expected_statuses=(202,))

    def get_proposal_job(self, job_id: str) -> Dict[str, Any]:
        return self._request("GET", "/v2/jobs/" + _path_identifier(job_id))

    def cancel_proposal_job(self, job_id: str) -> Dict[str, Any]:
        return self._request("DELETE", "/v2/jobs/" + _path_identifier(job_id))

    def submit_generation_job(self, request: Dict[str, Any]) -> Dict[str, Any]:
        return self._request("POST", "/v2/generation/jobs", request, expected_statuses=(202,))

    def get_generation_job(self, job_id: str) -> Dict[str, Any]:
        expected_job_id = _path_identifier(job_id)
        job = self._request("GET", "/v2/generation/jobs/" + expected_job_id)
        _validate_generation_job_shape(job, expected_job_id)
        return job

    def cancel_generation_job(self, job_id: str) -> Dict[str, Any]:
        expected_job_id = _path_identifier(job_id)
        job = self._request("DELETE", "/v2/generation/jobs/" + expected_job_id)
        _validate_generation_job_shape(job, expected_job_id)
        return job

    def report_action(self, request: Dict[str, Any]) -> Dict[str, Any]:
        """Report a game-applied or rejected outcome; this does not execute it."""
        return self._request("POST", "/v2/action/report", request)

    def report_action_batch(self, request: Dict[str, Any]) -> Dict[str, Any]:
        """Atomically report game outcomes produced from one world revision."""
        return self._request("POST", "/v2/action/report-batch", request)

    def set_actor_activity(self, request: Dict[str, Any]) -> Dict[str, Any]:
        return self._request("POST", "/v2/session/activity", request)

    def arbitrate(self, request: Dict[str, Any]) -> Dict[str, Any]:
        return self._request("POST", "/v2/world/arbitrate", request)

    def state(self, request: Dict[str, Any]) -> Dict[str, Any]:
        return self._request("POST", "/v2/session/get", request)

    def snapshot(self, request: Dict[str, Any]) -> Dict[str, Any]:
        return self._request("POST", "/v2/session/snapshot", request)

    def restore(self, request: Dict[str, Any]) -> Dict[str, Any]:
        return self._request("POST", "/v2/session/restore", request)

    def timeline(self, request: Dict[str, Any]) -> Dict[str, Any]:
        return self._request("POST", "/v2/session/timeline", request)

    def replay(self, request: Dict[str, Any]) -> Dict[str, Any]:
        return self._request("POST", "/v2/session/replay", request)

    def due_agents(self, request: Dict[str, Any]) -> Dict[str, Any]:
        return self._request("POST", "/v2/scheduler/due", request)

    def wait_for_proposal(
        self,
        job_id: str,
        *,
        deadline_seconds: float = 25.0,
        poll_interval: float = 0.1,
        cancel_event: Optional[threading.Event] = None,
        expected_request: Optional[Dict[str, Any]] = None,
    ) -> Dict[str, Any]:
        deadline_seconds = float(deadline_seconds)
        poll_interval = float(poll_interval)
        if not 0.05 <= deadline_seconds <= 300.0:
            raise RinConfigurationError("invalid_deadline", "Job deadline must be between 0.05 and 300 seconds")
        if not 0.01 <= poll_interval <= 5.0:
            raise RinConfigurationError("invalid_poll_interval", "Job poll interval must be between 0.01 and 5 seconds")
        job_id = _path_identifier(job_id)
        if expected_request is not None:
            expected_request = _stable_proposal_request(expected_request)
        deadline = self._clock() + deadline_seconds
        while True:
            if cancel_event is not None and cancel_event.is_set():
                try:
                    canceled_job = self.cancel_proposal_job(job_id)
                except RinError:
                    raise RinJobError(
                        "job_cancel_unconfirmed",
                        "Proposal job cancellation could not be confirmed",
                        job_id=job_id,
                    ) from None
                resolved = self._resolve_proposal_job_or_unknown(
                    canceled_job,
                    job_id,
                    expected_request,
                )
                if resolved is not None:
                    return resolved
                raise RinJobError(
                    "job_cancel_unconfirmed",
                    "Proposal job cancellation did not reach a terminal state",
                    job_id=job_id,
                )
            try:
                job = self.get_proposal_job(job_id)
            except RinAPIError as exc:
                if exc.code == "job_not_found":
                    raise
                raise RinJobError(
                    "job_outcome_unknown",
                    "Proposal job lookup did not confirm an outcome",
                    status=exc.status,
                    field=exc.field,
                    job_id=job_id,
                ) from exc
            except RinError as exc:
                raise RinJobError(
                    "job_outcome_unknown",
                    "Proposal job lookup did not confirm an outcome",
                    job_id=job_id,
                ) from exc
            resolved = self._resolve_proposal_job_or_unknown(
                job,
                job_id,
                expected_request,
            )
            if resolved is not None:
                return resolved
            remaining = deadline - self._clock()
            if remaining <= 0:
                try:
                    canceled_job = self.cancel_proposal_job(job_id)
                except RinError:
                    raise RinJobError(
                        "job_outcome_unknown",
                        "Proposal deadline elapsed and cancellation could not be confirmed",
                        job_id=job_id,
                    ) from None
                resolved = self._resolve_proposal_job_or_unknown(
                    canceled_job,
                    job_id,
                    expected_request,
                )
                if resolved is not None:
                    return resolved
                raise RinJobError(
                    "job_outcome_unknown",
                    "Proposal deadline elapsed without a terminal cancellation result",
                    job_id=job_id,
                )
            delay = min(poll_interval, remaining)
            if cancel_event is not None:
                cancel_event.wait(delay)
            else:
                self._sleeper(delay)

    @staticmethod
    def _resolve_proposal_job(
        job: Dict[str, Any],
        job_id: str = "",
        expected_request: Optional[Dict[str, Any]] = None,
    ) -> Optional[Dict[str, Any]]:
        if not isinstance(job, dict):
            raise RinProtocolError("invalid_job", "Rin returned an invalid proposal job")
        _validate_proposal_job_identity(job, job_id, expected_request)
        status = job.get("status")
        if not isinstance(status, str):
            raise RinProtocolError("invalid_job", "Proposal job status must be a string")
        if status == "succeeded":
            if expected_request is None:
                # Preserve the public wait_for_proposal(job_id) API. Without
                # the original request it cannot prove the selected candidate,
                # but it can still require a self-consistent Job/Proposal pair.
                _validate_unbound_proposal_identity(job.get("proposal"), job)
            else:
                _validate_proposal_identity(job.get("proposal"), expected_request)
            return job
        if status in TERMINAL_JOB_STATES:
            detail = job.get("error", {})
            if not isinstance(detail, dict):
                detail = {}
            if status == "failed":
                error_code = detail.get("code")
                if not isinstance(error_code, str):
                    raise RinProtocolError(
                        "invalid_job",
                        "Failed proposal job error code must be a string",
                    )
                try:
                    error_code = _path_identifier(error_code)
                except RinProtocolError as exc:
                    raise RinProtocolError(
                        "invalid_job",
                        "Failed proposal job error code is invalid",
                    ) from exc
            else:
                error_code = _safe_text(detail.get("code"), 96) or "job_" + status
            raise RinJobError(
                error_code,
                _safe_text(detail.get("message"), 500) or "Proposal job ended as " + status,
                field=_safe_text(detail.get("field"), 160),
                job_id=job_id,
            )
        if status not in ("queued", "running"):
            raise RinProtocolError("invalid_job", "Proposal job returned an unknown status")
        return None

    @classmethod
    def _resolve_proposal_job_or_unknown(
        cls,
        job: Dict[str, Any],
        job_id: str,
        expected_request: Optional[Dict[str, Any]] = None,
    ) -> Optional[Dict[str, Any]]:
        try:
            return cls._resolve_proposal_job(job, job_id, expected_request)
        except RinJobError:
            raise
        except RinProtocolError as exc:
            raise RinJobError(
                "job_outcome_unknown",
                "Proposal job returned an invalid outcome",
                job_id=job_id,
            ) from exc

    def _submit_proposal_attempt(
        self,
        request: Dict[str, Any],
        persist_job_id: Optional[Callable[[str], bool]],
        previous_job_id: str,
    ) -> str:
        try:
            submission = self.submit_proposal_job(request)
        except RinTransportError as exc:
            raise RinJobError(
                "job_outcome_unknown" if previous_job_id else "proposal_outcome_unknown",
                "Proposal submission did not confirm a durable Job",
                job_id=previous_job_id,
            ) from exc
        except RinAPIError as exc:
            if 400 <= exc.status < 500 and exc.status != 408:
                # A bounded client error from Rin confirms that no new Job was
                # accepted. Gateway/server failures remain delivery-ambiguous.
                raise
            raise RinJobError(
                "job_outcome_unknown" if previous_job_id else "proposal_outcome_unknown",
                "Proposal submission did not confirm a durable Job",
                status=exc.status,
                field=exc.field,
                job_id=previous_job_id,
            ) from exc
        except RinError as exc:
            raise RinJobError(
                "job_outcome_unknown" if previous_job_id else "proposal_outcome_unknown",
                "Proposal submission returned an invalid or ambiguous response",
                job_id=previous_job_id,
            ) from exc
        job_id = str(submission.get("job_id", ""))
        try:
            job_id = _path_identifier(job_id)
        except RinProtocolError as exc:
            raise RinJobError(
                "job_outcome_unknown" if previous_job_id else "proposal_outcome_unknown",
                "Proposal submission did not return a usable Job identity",
                job_id=previous_job_id,
            ) from exc
        if persist_job_id is not None:
            try:
                persisted = bool(persist_job_id(job_id))
            except Exception:
                persisted = False
            if not persisted:
                raise RinJobError(
                    "job_id_persistence_failed",
                    "Proposal Job identity could not be retained",
                    job_id=job_id,
                )
        return job_id

    def run_proposal_job(
        self,
        request: Dict[str, Any],
        *,
        deadline_seconds: float = 25.0,
        poll_interval: float = 0.1,
        cancel_event: Optional[threading.Event] = None,
        known_job_id: str = "",
        persist_job_id: Optional[Callable[[str], bool]] = None,
    ) -> Dict[str, Any]:
        request = _stable_proposal_request(request)
        try:
            job_id = _path_identifier(known_job_id) if known_job_id else ""
        except RinProtocolError as exc:
            raise RinJobError(
                "job_outcome_unknown",
                "The retained Proposal Job identity is invalid",
                job_id=_safe_text(known_job_id, 96),
            ) from exc
        recovery_post_used = False
        if not job_id:
            job_id = self._submit_proposal_attempt(request, persist_job_id, "")
        while True:
            try:
                job = self.wait_for_proposal(
                    job_id,
                    deadline_seconds=deadline_seconds,
                    poll_interval=poll_interval,
                    cancel_event=cancel_event,
                    expected_request=request,
                )
            except RinJobError as exc:
                if (
                    exc.code == "proposal_outcome_unknown"
                    and not recovery_post_used
                    and not (cancel_event is not None and cancel_event.is_set())
                ):
                    recovery_post_used = True
                    job_id = self._submit_proposal_attempt(
                        request,
                        persist_job_id,
                        exc.job_id or job_id,
                    )
                    continue
                raise
            except RinAPIError as exc:
                if (
                    exc.code == "job_not_found"
                    and not recovery_post_used
                    and not (cancel_event is not None and cancel_event.is_set())
                ):
                    recovery_post_used = True
                    job_id = self._submit_proposal_attempt(
                        request,
                        persist_job_id,
                        job_id,
                    )
                    continue
                raise RinJobError(
                    "job_outcome_unknown",
                    "Proposal Job could not be recovered",
                    job_id=job_id,
                ) from exc
            return {
                "source": "sidecar",
                "error_code": "",
                "job_id": job_id,
                "proposal": _json_clone(job["proposal"]),
            }

    def wait_for_generation(
        self,
        job_id: str,
        *,
        deadline_seconds: float = 45.0,
        poll_interval: float = 0.1,
        cancel_event: Optional[threading.Event] = None,
    ) -> Dict[str, Any]:
        deadline_seconds = float(deadline_seconds)
        poll_interval = float(poll_interval)
        if not 0.05 <= deadline_seconds <= 300.0:
            raise RinConfigurationError("invalid_deadline", "Generation deadline must be between 0.05 and 300 seconds")
        if not 0.01 <= poll_interval <= 5.0:
            raise RinConfigurationError("invalid_poll_interval", "Generation poll interval must be between 0.01 and 5 seconds")
        job_id = _path_identifier(job_id)
        deadline = self._clock() + deadline_seconds
        while True:
            if cancel_event is not None and cancel_event.is_set():
                try:
                    canceled_job = self.cancel_generation_job(job_id)
                except RinError:
                    raise RinJobError(
                        "job_cancel_unconfirmed",
                        "Generation job cancellation could not be confirmed",
                        job_id=job_id,
                    ) from None
                resolved = self._resolve_generation_job(canceled_job, job_id)
                if resolved is not None:
                    return resolved
                raise RinJobError(
                    "job_cancel_unconfirmed",
                    "Generation job cancellation did not reach a terminal state",
                    job_id=job_id,
                )
            job = self.get_generation_job(job_id)
            resolved = self._resolve_generation_job(job, job_id)
            if resolved is not None:
                return resolved
            remaining = deadline - self._clock()
            if remaining <= 0:
                try:
                    canceled_job = self.cancel_generation_job(job_id)
                except RinError:
                    raise RinJobError(
                        "job_timeout",
                        "Generation job exceeded its deadline",
                        job_id=job_id,
                    ) from None
                resolved = self._resolve_generation_job(canceled_job, job_id)
                if resolved is not None:
                    return resolved
                raise RinJobError(
                    "job_timeout",
                    "Generation job exceeded its deadline",
                    job_id=job_id,
                )
            delay = min(poll_interval, remaining)
            if cancel_event is not None:
                cancel_event.wait(delay)
            else:
                self._sleeper(delay)

    @staticmethod
    def _resolve_generation_job(
        job: Dict[str, Any],
        expected_job_id: str,
    ) -> Optional[Dict[str, Any]]:
        if not isinstance(job, dict):
            raise RinProtocolError("invalid_job", "Rin returned an invalid generation job")
        status = _validate_generation_job_shape(job, expected_job_id)
        if status == "succeeded":
            return job
        if status in TERMINAL_JOB_STATES:
            detail = job.get("error", {})
            if not isinstance(detail, dict):
                detail = {}
            raise RinJobError(
                _safe_text(detail.get("code"), 96) or "job_" + status,
                _safe_text(detail.get("message"), 500) or "Generation job ended as " + status,
                field=_safe_text(detail.get("field"), 160),
                job_id=expected_job_id,
            )
        return None

    def generate_json(
        self,
        request: Dict[str, Any],
        *,
        deadline_seconds: float = 45.0,
        poll_interval: float = 0.1,
        cancel_event: Optional[threading.Event] = None,
    ) -> Dict[str, Any]:
        submission = self.submit_generation_job(request)
        job_id = str(submission.get("job_id", ""))
        if not job_id:
            raise RinProtocolError("invalid_submission", "Rin did not return a generation job id")
        job = self.wait_for_generation(
            job_id,
            deadline_seconds=deadline_seconds,
            poll_interval=poll_interval,
            cancel_event=cancel_event,
        )
        result = _json_clone(job["result"])
        try:
            response = json.loads(
                result["content"],
                parse_constant=_reject_json_constant,
            )
        except (TypeError, ValueError) as exc:
            raise RinProtocolError("invalid_generation_json", "Rin generation content was not valid JSON") from exc
        if not isinstance(response, dict):
            raise RinProtocolError("invalid_generation_json", "Rin generation content must be one JSON object")
        return {
            "source": "sidecar",
            "job_id": job_id,
            "response": response,
            "metadata": {key: value for key, value in result.items() if key != "content"},
        }

    def _request(
        self,
        method: str,
        path: str,
        payload: Optional[Dict[str, Any]] = None,
        *,
        expected_statuses: Sequence[int] = (200,),
    ) -> Dict[str, Any]:
        body = None
        headers = {"Accept": "application/json", "User-Agent": "rin-renpy/" + SDK_VERSION}
        if payload is not None:
            if not isinstance(payload, dict):
                raise RinProtocolError("invalid_request", "Rin request payload must be an object")
            _validate_request_json(payload)
            try:
                body = json.dumps(
                    payload,
                    ensure_ascii=False,
                    separators=(",", ":"),
                ).encode("utf-8")
            except (TypeError, ValueError, UnicodeEncodeError) as exc:
                raise RinProtocolError(
                    "invalid_request",
                    "Rin request payload is not JSON serializable",
                ) from exc
            headers["Content-Type"] = "application/json"
        if self.token:
            headers["Authorization"] = "Bearer " + self.token
        request = Request(self.base_url + path, data=body, headers=headers, method=method)
        deadline = self._clock() + self.timeout
        try:
            with self._opener.open(
                request,
                timeout=_remaining_timeout(deadline, self._clock),
            ) as response:
                status = int(getattr(response, "status", response.getcode()))
                response_payload = self._read_response(response, deadline)
        except HTTPError as exc:
            try:
                response_payload = self._read_response(exc, deadline)
            except (socket.timeout, TimeoutError) as timeout_exc:
                raise RinTransportError(
                    "transport_timeout",
                    "Rin request timed out",
                ) from timeout_exc
            decoded = _decode_json(response_payload, allow_failure=True)
            raise _error_from_envelope(decoded, exc.code) from None
        except (socket.timeout, TimeoutError) as exc:
            raise RinTransportError("transport_timeout", "Rin request timed out") from exc
        except URLError as exc:
            reason = getattr(exc, "reason", None)
            if isinstance(reason, (socket.timeout, TimeoutError)):
                code = "transport_timeout"
                message = "Rin request timed out"
            elif _definitely_not_delivered(reason):
                code = "transport_unavailable"
                message = "Could not connect to the Rin Sidecar"
            else:
                code = "transport_failed"
                message = "Rin transport failed after delivery became uncertain"
            raise RinTransportError(code, message) from exc
        except OSError as exc:
            if _definitely_not_delivered(exc):
                raise RinTransportError("transport_unavailable", "Could not connect to the Rin Sidecar") from exc
            raise RinTransportError("transport_failed", "Rin transport failed after delivery became uncertain") from exc
        if status not in expected_statuses:
            decoded = _decode_json(response_payload, allow_failure=True)
            raise _error_from_envelope(decoded, status)
        envelope = _decode_json(response_payload)
        if envelope.get("ok") is not True:
            raise _error_from_envelope(envelope, status)
        data = envelope.get("data")
        if not isinstance(data, dict):
            raise RinProtocolError("invalid_response", "Rin response data must be an object")
        return data

    def _read_response(self, response: Any, deadline: float) -> bytes:
        length = response.headers.get("Content-Length") if getattr(response, "headers", None) else None
        if length:
            try:
                declared = int(length)
                if declared < 0:
                    raise RinProtocolError(
                        "invalid_response",
                        "Rin response had an invalid Content-Length",
                    )
                if declared > self.max_response_bytes:
                    raise RinProtocolError("response_too_large", "Rin response exceeded the configured limit")
            except ValueError:
                raise RinProtocolError("invalid_response", "Rin response had an invalid Content-Length")
        return _read_bounded_response(
            response,
            self.max_response_bytes,
            deadline,
            self._clock,
            "Rin response exceeded the configured limit",
        )


def _remaining_timeout(deadline: float, clock: Callable[[], float]) -> float:
    remaining = deadline - clock()
    if remaining <= 0:
        raise TimeoutError("Rin request deadline expired")
    return remaining


def _read_bounded_response(
    response: Any,
    maximum: int,
    deadline: float,
    clock: Callable[[], float],
    too_large_message: str,
) -> bytes:
    reader = getattr(response, "read1", None)
    if not callable(reader):
        error_stream = getattr(response, "fp", None)
        reader = getattr(error_stream, "read1", None)
    if not callable(reader):
        reader = response.read

    payload = bytearray()
    while True:
        remaining = _remaining_timeout(deadline, clock)
        _set_response_timeout(response, remaining)
        chunk = reader(min(64 * 1024, maximum + 1 - len(payload)))
        if clock() > deadline:
            raise TimeoutError("Rin request deadline expired")
        if not isinstance(chunk, (bytes, bytearray)):
            raise RinProtocolError("invalid_response", "Rin response body was not bytes")
        if not chunk:
            return bytes(payload)
        payload.extend(chunk)
        if len(payload) > maximum:
            raise RinProtocolError("response_too_large", too_large_message)


def _set_response_timeout(response: Any, timeout: float) -> None:
    pending = [response]
    seen = set()
    for _ in range(16):
        if not pending:
            return
        current = pending.pop(0)
        identity = id(current)
        if identity in seen:
            continue
        seen.add(identity)
        setter = getattr(current, "settimeout", None)
        if callable(setter):
            setter(timeout)
            return
        for name in ("fp", "raw", "_sock", "sock"):
            child = getattr(current, name, None)
            if child is not None:
                pending.append(child)


def _decode_json(payload: bytes, *, allow_failure: bool = False) -> Dict[str, Any]:
    try:
        decoded = json.loads(
            payload.decode("utf-8"),
            parse_constant=_reject_json_constant,
        )
    except (UnicodeDecodeError, ValueError) as exc:
        if allow_failure:
            return {}
        raise RinProtocolError("invalid_response", "Rin returned invalid JSON") from exc
    if not isinstance(decoded, dict):
        if allow_failure:
            return {}
        raise RinProtocolError("invalid_response", "Rin response must be a JSON object")
    return decoded


def _definitely_not_delivered(reason: Any) -> bool:
    return isinstance(reason, OSError) and getattr(reason, "errno", None) in {
        errno.ECONNREFUSED,
        errno.ENETUNREACH,
        errno.EHOSTUNREACH,
    }


def _path_identifier(value: str) -> str:
    text = str(value or "")
    if not text or len(text) > 96 or not text[0].isalnum():
        raise RinProtocolError("invalid_id", "Rin identifier is invalid")
    if any(not (character.isascii() and (character.isalnum() or character in "._-")) for character in text):
        raise RinProtocolError("invalid_id", "Rin identifier is invalid")
    return text


def _reject_json_constant(value: str) -> None:
    raise ValueError("Non-finite JSON number is not permitted: " + value)


def _validate_request_json(value: Any) -> None:
    def visit(current: Any, depth: int, active: set) -> None:
        if depth > MAX_JSON_DEPTH:
            raise RinProtocolError("invalid_request", "Rin payload exceeds the JSON nesting limit")
        if current is None or isinstance(current, (str, bool)):
            return
        if isinstance(current, int):
            if not -MAX_JSON_SAFE_INTEGER <= current <= MAX_JSON_SAFE_INTEGER:
                raise RinProtocolError("invalid_request", "Rin payload contains an unsafe JSON integer")
            return
        if isinstance(current, float):
            if not math.isfinite(current):
                raise RinProtocolError("invalid_request", "Rin payload contains a non-finite JSON number")
            if current.is_integer() and not -MAX_JSON_SAFE_INTEGER <= current <= MAX_JSON_SAFE_INTEGER:
                raise RinProtocolError("invalid_request", "Rin payload contains an unsafe JSON integer")
            return
        if isinstance(current, (dict, list, tuple)):
            identity = id(current)
            if identity in active:
                raise RinProtocolError("invalid_request", "Rin payload contains a JSON cycle")
            active.add(identity)
            try:
                if isinstance(current, dict):
                    if any(not isinstance(key, str) for key in current):
                        raise RinProtocolError(
                            "invalid_request",
                            "Rin payload contains a non-string JSON object key",
                        )
                    children = current.values()
                else:
                    children = current
                for child in children:
                    visit(child, depth + 1, active)
            finally:
                active.remove(identity)

    visit(value, 0, set())


def _strict_nonnegative_json_safe_integer(value: Any) -> bool:
    return (
        isinstance(value, int)
        and not isinstance(value, bool)
        and 0 <= value <= MAX_JSON_SAFE_INTEGER
    )


def _normalized_action_offer(value: Any, *, field: str) -> Dict[str, Any]:
    if not isinstance(value, dict):
        raise RinProtocolError("invalid_job", field + " must be an object")
    allowed = {
        "offer_id",
        "decision_window_id",
        "actor_id",
        "capability",
        "descriptor_digest",
        "description",
        "arguments",
        "targets",
        "expected_epoch",
        "observation_seq",
        "deadline",
    }
    if any(key not in allowed for key in value):
        raise RinProtocolError("invalid_job", field + " contains an unknown field")

    for name in ("offer_id", "decision_window_id", "actor_id"):
        try:
            _path_identifier(value.get(name))
        except (RinProtocolError, TypeError) as exc:
            raise RinProtocolError("invalid_job", field + " contains an invalid " + name) from exc
    capability = value.get("capability")
    if not isinstance(capability, dict):
        raise RinProtocolError("invalid_job", field + " contains an invalid capability")
    for name in ("id", "version"):
        try:
            _path_identifier(capability.get(name))
        except (RinProtocolError, TypeError) as exc:
            raise RinProtocolError("invalid_job", field + " contains an invalid capability") from exc
    digest = value.get("descriptor_digest")
    if (
        not isinstance(digest, str)
        or len(digest) != 64
        or any(char not in "0123456789abcdef" for char in digest)
    ):
        raise RinProtocolError("invalid_job", field + " contains an invalid descriptor digest")
    description = value.get("description")
    if (
        not isinstance(description, str)
        or not description.strip()
        or len(description) > 300
        or "\x00" in description
    ):
        raise RinProtocolError("invalid_job", field + " contains an invalid description")
    if not isinstance(value.get("arguments"), dict):
        raise RinProtocolError("invalid_job", field + " arguments must be an object")
    if not _strict_nonnegative_json_safe_integer(value.get("observation_seq")):
        raise RinProtocolError("invalid_job", field + " observation_seq is invalid")
    if not isinstance(value.get("expected_epoch"), dict):
        raise RinProtocolError("invalid_job", field + " expected_epoch is invalid")
    deadline = value.get("deadline")
    if (
        not isinstance(deadline, dict)
        or deadline.get("clock") not in ("event", "step", "realtime")
        or not isinstance(deadline.get("value"), int)
        or isinstance(deadline.get("value"), bool)
    ):
        raise RinProtocolError("invalid_job", field + " deadline is invalid")
    return _json_clone(value)


def _stable_proposal_request(request: Any) -> Dict[str, Any]:
    if not isinstance(request, dict):
        raise RinProtocolError("invalid_request", "Proposal request must be an object")
    try:
        stable = _json_clone(request)
    except (TypeError, ValueError) as exc:
        raise RinProtocolError(
            "invalid_request",
            "Proposal request must be JSON serializable",
        ) from exc

    for field in ("session_id", "request_id", "actor_id"):
        value = stable.get(field)
        if not isinstance(value, str):
            raise RinProtocolError("invalid_request", field + " must be a string")
        try:
            _path_identifier(value)
        except RinProtocolError as exc:
            raise RinProtocolError("invalid_request", field + " is invalid") from exc
    if not _strict_nonnegative_json_safe_integer(stable.get("tick")):
        raise RinProtocolError(
            "invalid_request",
            "tick must be a non-negative JSON-safe integer",
        )
    window = stable.get("decision_window")
    if not isinstance(window, dict) or not isinstance(window.get("id"), str):
        raise RinProtocolError("invalid_request", "decision_window is invalid")
    offers = stable.get("offers")
    if not isinstance(offers, list) or not 1 <= len(offers) <= 32:
        raise RinProtocolError(
            "invalid_request",
            "offers must contain 1-32 authored actions",
        )
    try:
        normalized = [
            _normalized_action_offer(offer, field="offers")
            for offer in offers
        ]
    except RinProtocolError as exc:
        raise RinProtocolError("invalid_request", exc.safe_message) from exc
    if len({offer["offer_id"] for offer in normalized}) != len(normalized):
        raise RinProtocolError(
            "invalid_request",
            "offers must have unique offer_id values",
        )
    return stable


def _validate_proposal_job_identity(
    job: Dict[str, Any],
    expected_job_id: str,
    expected_request: Optional[Dict[str, Any]],
) -> None:
    actual_job_id = job.get("job_id")
    if not isinstance(actual_job_id, str):
        raise RinProtocolError("invalid_job", "Proposal job id must be a string")
    try:
        actual_job_id = _path_identifier(actual_job_id)
    except RinProtocolError as exc:
        raise RinProtocolError("invalid_job", "Proposal job id is invalid") from exc
    if actual_job_id != expected_job_id:
        raise RinProtocolError("invalid_job", "Proposal job id did not match the requested job")

    for field in ("session_id", "request_id"):
        actual = job.get(field)
        if not isinstance(actual, str):
            raise RinProtocolError("invalid_job", "Proposal job " + field + " must be a string")
        try:
            _path_identifier(actual)
        except RinProtocolError as exc:
            raise RinProtocolError("invalid_job", "Proposal job " + field + " is invalid") from exc
        if expected_request is not None and actual != expected_request.get(field):
            raise RinProtocolError(
                "invalid_job",
                "Proposal job " + field + " did not match the stable request",
            )


def _validate_proposal_identity(
    proposal: Any,
    expected_request: Dict[str, Any],
) -> None:
    if not isinstance(proposal, dict):
        raise RinProtocolError(
            "invalid_job",
            "Successful proposal job did not include a proposal",
        )
    proposal_id = proposal.get("id")
    if not isinstance(proposal_id, str):
        raise RinProtocolError("invalid_job", "Proposal id must be a string")
    try:
        _path_identifier(proposal_id)
    except RinProtocolError as exc:
        raise RinProtocolError("invalid_job", "Proposal id is invalid") from exc

    for field in ("session_id", "request_id", "actor_id"):
        actual = proposal.get(field)
        if not isinstance(actual, str) or actual != expected_request.get(field):
            raise RinProtocolError(
                "invalid_job",
                "Proposal " + field + " did not match the stable request",
            )
    tick = proposal.get("tick")
    if (
        not _strict_nonnegative_json_safe_integer(tick)
        or tick != expected_request.get("tick")
    ):
        raise RinProtocolError(
            "invalid_job",
            "Proposal tick did not match the stable request",
        )

    action = _normalized_action_offer(proposal.get("action"), field="proposal.action")
    expected_actions = [
        _normalized_action_offer(candidate, field="offers")
        for candidate in expected_request["offers"]
    ]
    if action not in expected_actions:
        raise RinProtocolError(
            "invalid_job",
            "Proposal action did not exactly match a candidate action",
        )


def _validate_unbound_proposal_identity(
    proposal: Any,
    job: Dict[str, Any],
) -> None:
    if not isinstance(proposal, dict):
        raise RinProtocolError(
            "invalid_job",
            "Successful proposal job did not include a proposal",
        )
    proposal_id = proposal.get("id")
    if not isinstance(proposal_id, str):
        raise RinProtocolError("invalid_job", "Proposal id must be a string")
    try:
        _path_identifier(proposal_id)
    except RinProtocolError as exc:
        raise RinProtocolError("invalid_job", "Proposal id is invalid") from exc

    for field in ("session_id", "request_id"):
        actual = proposal.get(field)
        if not isinstance(actual, str) or actual != job.get(field):
            raise RinProtocolError(
                "invalid_job",
                "Proposal " + field + " did not match its Job",
            )
    actor_id = proposal.get("actor_id")
    if not isinstance(actor_id, str):
        raise RinProtocolError("invalid_job", "Proposal actor_id must be a string")
    try:
        _path_identifier(actor_id)
    except RinProtocolError as exc:
        raise RinProtocolError("invalid_job", "Proposal actor_id is invalid") from exc
    if not _strict_nonnegative_json_safe_integer(proposal.get("tick")):
        raise RinProtocolError(
            "invalid_job",
            "Proposal tick must be a non-negative JSON-safe integer",
        )
    _normalized_action_offer(proposal.get("action"), field="proposal.action")


def _validate_generation_job_identity(
    job: Dict[str, Any],
    expected_job_id: str,
) -> None:
    actual_job_id = job.get("job_id")
    if not isinstance(actual_job_id, str):
        raise RinProtocolError("invalid_job", "Generation job id must be a string")
    try:
        actual_job_id = _path_identifier(actual_job_id)
    except RinProtocolError as exc:
        raise RinProtocolError("invalid_job", "Generation job id is invalid") from exc
    if actual_job_id != expected_job_id:
        raise RinProtocolError(
            "invalid_job",
            "Generation job id did not match the requested job",
        )
    request_id = job.get("request_id")
    if not isinstance(request_id, str):
        raise RinProtocolError(
            "invalid_job",
            "Generation job request id must be a string",
        )
    try:
        _path_identifier(request_id)
    except RinProtocolError as exc:
        raise RinProtocolError(
            "invalid_job",
            "Generation job request id is invalid",
        ) from exc


def _validate_generation_job_shape(
    job: Dict[str, Any],
    expected_job_id: str,
) -> str:
    if not isinstance(job, dict):
        raise RinProtocolError("invalid_job", "Rin returned an invalid generation job")
    _validate_generation_job_identity(job, expected_job_id)
    status = job.get("status")
    if not isinstance(status, str) or status not in (
        "queued",
        "running",
        "succeeded",
        "failed",
        "stale",
        "canceled",
    ):
        raise RinProtocolError("invalid_job", "Generation job returned an invalid status")
    if status == "succeeded":
        _validate_generation_result(job.get("result"))
    return status


def _validate_generation_result(result: Any) -> None:
    if not isinstance(result, dict) or not isinstance(result.get("content"), str):
        raise RinProtocolError(
            "invalid_job",
            "Successful generation job did not include string content",
        )
    content = result["content"]
    if not content.strip() or "\x00" in content:
        raise RinProtocolError(
            "invalid_job",
            "Successful generation job content is empty or contains NUL",
        )
    try:
        encoded_content = content.encode("utf-8")
    except UnicodeEncodeError as exc:
        raise RinProtocolError(
            "invalid_job",
            "Successful generation job content is not valid UTF-8",
        ) from exc
    if len(encoded_content) > MAX_GENERATION_CONTENT_BYTES:
        raise RinProtocolError(
            "invalid_job",
            "Successful generation job content exceeds 4 MiB",
        )
    for field in ("model", "finish_reason"):
        if field in result and not isinstance(result[field], str):
            raise RinProtocolError(
                "invalid_job",
                "Generation result " + field + " must be a string",
            )
    for field in ("prompt_tokens", "output_tokens", "total_tokens"):
        if field in result and not _strict_nonnegative_json_safe_integer(result[field]):
            raise RinProtocolError(
                "invalid_job",
                "Generation result " + field + " must be a non-negative integer",
            )
    if "cache_hit" in result and not isinstance(result["cache_hit"], bool):
        raise RinProtocolError(
            "invalid_job",
            "Generation result cache_hit must be a boolean",
        )


class BackgroundProposalRegistry:
    """Process-local worker registry suitable for ``renpy.invoke_in_thread``."""

    def __init__(self, client: RinClient, *, maximum: int = 128) -> None:
        self.client = client
        self.maximum = max(1, min(1024, int(maximum)))
        self._lock = threading.RLock()
        self._entries: Dict[str, Dict[str, Any]] = {}

    def schedule(
        self,
        request: Dict[str, Any],
        launch: Callable[[Callable[[], None]], Any],
        *,
        deadline_seconds: float = 25.0,
        poll_interval: float = 0.1,
        known_job_id: str = "",
    ) -> str:
        request_id = _path_identifier(str(request.get("request_id", "")))
        if known_job_id:
            known_job_id = _path_identifier(known_job_id)
        request_snapshot = _json_clone(request)
        request_fingerprint = hashlib.sha256(json.dumps(
            request_snapshot,
            ensure_ascii=False,
            sort_keys=True,
            separators=(",", ":"),
        ).encode("utf-8")).hexdigest()
        resumed = False
        with self._lock:
            if request_id in self._entries:
                entry = self._entries[request_id]
                if entry["request_fingerprint"] != request_fingerprint:
                    raise RinProtocolError(
                        "request_id_conflict",
                        "Request id was already used with a different proposal payload",
                    )
                if entry["status"] != "unresolved":
                    return request_id
                resumed = True
                cancel_event = threading.Event()
                entry["status"] = "pending"
                entry["cancel_event"] = cancel_event
                entry["result"] = None
                entry["error_code"] = ""
                request_snapshot = _json_clone(entry["request"])
                deadline_seconds = float(entry["deadline_seconds"])
                poll_interval = float(entry["poll_interval"])
                known_job_id = str(entry.get("job_id", ""))
            else:
                self._prune_locked()
                if len(self._entries) >= self.maximum:
                    raise RinProtocolError("registry_full", "Rin background registry is full")
                cancel_event = threading.Event()
                self._entries[request_id] = {
                    "status": "pending",
                    "request_fingerprint": request_fingerprint,
                    "request": request_snapshot,
                    "deadline_seconds": float(deadline_seconds),
                    "poll_interval": float(poll_interval),
                    "job_id": known_job_id,
                    "cancel_event": cancel_event,
                    "result": None,
                    "error_code": "",
                }

        def retain_job_id(job_id: str) -> bool:
            with self._lock:
                entry = self._entries.get(request_id)
                if (
                    entry is None
                    or entry["status"] == "invalidated"
                    or entry["request_fingerprint"] != request_fingerprint
                ):
                    return False
                entry["job_id"] = _path_identifier(job_id)
                return True

        def worker() -> None:
            try:
                result = self.client.run_proposal_job(
                    request_snapshot,
                    deadline_seconds=deadline_seconds,
                    poll_interval=poll_interval,
                    cancel_event=cancel_event,
                    known_job_id=known_job_id,
                    persist_job_id=retain_job_id,
                )
                status = "complete"
                error_code = ""
                retained_job_id = str(result.get("job_id", ""))
            except RinError as exc:
                result = None
                if exc.code in UNRESOLVED_PROPOSAL_CODES:
                    status = "unresolved"
                else:
                    status = "canceled" if exc.code == "job_canceled" else "failed"
                error_code = exc.code
                retained_job_id = getattr(exc, "job_id", "") or known_job_id
            with self._lock:
                entry = self._entries.get(request_id)
                if entry is not None and entry["status"] != "invalidated":
                    entry["status"] = status
                    entry["result"] = _json_clone(result) if result is not None else None
                    entry["error_code"] = error_code
                    if retained_job_id:
                        entry["job_id"] = _path_identifier(retained_job_id)

        try:
            launch(worker)
        except Exception:
            with self._lock:
                if resumed:
                    entry = self._entries.get(request_id)
                    if entry is not None:
                        entry["status"] = "unresolved"
                        entry["error_code"] = "worker_start_failed"
                else:
                    self._entries.pop(request_id, None)
            raise RinTransportError("worker_start_failed", "Could not start Rin background worker")
        return request_id

    def status(self, request_id: str) -> str:
        with self._lock:
            entry = self._entries.get(str(request_id))
            return str(entry.get("status", "")) if entry else "missing"

    def consume(self, request_id: str) -> Optional[Dict[str, Any]]:
        with self._lock:
            entry = self._entries.get(str(request_id))
            if not entry or entry["status"] not in (
                "complete",
                "failed",
                "canceled",
                "invalidated",
            ):
                return None
            self._entries.pop(str(request_id), None)
            return {
                "status": entry["status"],
                "error_code": entry["error_code"],
                "job_id": str(entry.get("job_id", "")),
                "result": _json_clone(entry["result"]) if entry["result"] is not None else None,
            }

    def attempt(self, request_id: str) -> Optional[Dict[str, Any]]:
        """Return a plain resumable record for a pending or unresolved attempt."""
        with self._lock:
            entry = self._entries.get(str(request_id))
            if not entry or entry["status"] not in ("pending", "unresolved"):
                return None
            return {
                "status": str(entry["status"]),
                "request": _json_clone(entry["request"]),
                "job_id": str(entry.get("job_id", "")),
                "error_code": str(entry.get("error_code", "")),
            }

    def cancel(self, request_id: str) -> bool:
        with self._lock:
            entry = self._entries.get(str(request_id))
            if not entry or entry["status"] != "pending":
                return False
            entry["cancel_event"].set()
            return True

    def invalidate_all(self, error_code: str = "stale_epoch") -> int:
        """Make every registered result unusable after an Epoch change."""
        invalidated = 0
        with self._lock:
            for entry in self._entries.values():
                if entry["status"] == "invalidated":
                    continue
                entry["cancel_event"].set()
                entry["status"] = "invalidated"
                entry["result"] = None
                entry["error_code"] = str(error_code)
                invalidated += 1
        return invalidated

    def _prune_locked(self) -> None:
        terminal = [
            key
            for key, entry in self._entries.items()
            if entry.get("status") in (
                "complete",
                "failed",
                "canceled",
                "invalidated",
            )
        ]
        while len(self._entries) >= self.maximum and terminal:
            self._entries.pop(terminal.pop(0), None)
