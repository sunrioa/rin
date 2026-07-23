"""Strict standard-library client for Rin Protocol v1."""

from __future__ import annotations

import ipaddress
import json
import time
from typing import Any, Callable, Dict, Optional, Sequence, Tuple
from urllib.error import HTTPError, URLError
from urllib.parse import quote, urlsplit, urlunsplit
from urllib.request import HTTPRedirectHandler, Request, build_opener


PROTOCOL_VERSION = "rin.protocol/v1"
DEFAULT_BASE_URL = "http://127.0.0.1:7374"
DEFAULT_MAX_RESPONSE_BYTES = 2 * 1024 * 1024
_TERMINAL_JOB_STATES = frozenset(("succeeded", "failed", "stale", "canceled"))


class RinError(RuntimeError):
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
    def __init__(self, code: str, message: str, *, status: int = 0, field: str = "") -> None:
        self.status = int(status or 0)
        self.field = _safe_text(field, 160)
        super().__init__(code, message)


class _NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class RinClient:
    def __init__(
        self,
        base_url: str = DEFAULT_BASE_URL,
        *,
        token: str = "",
        timeout: float = 5.0,
        max_response_bytes: int = DEFAULT_MAX_RESPONSE_BYTES,
        clock: Callable[[], float] = time.monotonic,
        sleeper: Callable[[float], None] = time.sleep,
    ) -> None:
        self.token = _validate_token(token)
        self.base_url = _normalize_base_url(base_url, self.token)
        self.timeout = float(timeout)
        if not 0.05 <= self.timeout <= 120.0:
            raise RinConfigurationError("invalid_timeout", "timeout must be between 0.05 and 120 seconds")
        self.max_response_bytes = int(max_response_bytes)
        if not 1024 <= self.max_response_bytes <= 32 * 1024 * 1024:
            raise RinConfigurationError("invalid_response_limit", "response limit must be between 1 KiB and 32 MiB")
        self._opener = build_opener(_NoRedirect())
        self._clock = clock
        self._sleeper = sleeper

    def health(self) -> Dict[str, Any]:
        return self._request("GET", "/health")

    def create_session(self, payload: Dict[str, Any]) -> Dict[str, Any]:
        return self._post("/v1/session/create", payload)

    def observe(self, payload: Dict[str, Any]) -> Dict[str, Any]:
        return self._post("/v1/session/observe", payload)

    def propose(self, payload: Dict[str, Any]) -> Dict[str, Any]:
        return self._post("/v1/agent/propose", payload)

    def submit_proposal_job(self, payload: Dict[str, Any]) -> Dict[str, Any]:
        return self._request("POST", "/v1/jobs/propose", payload, (202,))

    def get_proposal_job(self, job_id: str) -> Dict[str, Any]:
        return self._request("GET", "/v1/jobs/" + _path_id(job_id))

    def cancel_proposal_job(self, job_id: str) -> Dict[str, Any]:
        return self._request("DELETE", "/v1/jobs/" + _path_id(job_id))

    def submit_generation_job(self, payload: Dict[str, Any]) -> Dict[str, Any]:
        return self._request("POST", "/v1/generation/jobs", payload, (202,))

    def get_generation_job(self, job_id: str) -> Dict[str, Any]:
        return self._request("GET", "/v1/generation/jobs/" + _path_id(job_id))

    def cancel_generation_job(self, job_id: str) -> Dict[str, Any]:
        return self._request("DELETE", "/v1/generation/jobs/" + _path_id(job_id))

    def commit(self, payload: Dict[str, Any]) -> Dict[str, Any]:
        return self._post("/v1/action/commit", payload)

    def commit_batch(self, payload: Dict[str, Any]) -> Dict[str, Any]:
        return self._post("/v1/action/commit-batch", payload)

    def set_actor_activity(self, payload: Dict[str, Any]) -> Dict[str, Any]:
        return self._post("/v1/session/activity", payload)

    def arbitrate(self, payload: Dict[str, Any]) -> Dict[str, Any]:
        return self._post("/v1/world/arbitrate", payload)

    def state(self, payload: Dict[str, Any]) -> Dict[str, Any]:
        return self._post("/v1/session/get", payload)

    def snapshot(self, payload: Dict[str, Any]) -> Dict[str, Any]:
        return self._post("/v1/session/snapshot", payload)

    def restore(self, payload: Dict[str, Any]) -> Dict[str, Any]:
        return self._post("/v1/session/restore", payload)

    def timeline(self, payload: Dict[str, Any]) -> Dict[str, Any]:
        return self._post("/v1/session/timeline", payload)

    def replay(self, payload: Dict[str, Any]) -> Dict[str, Any]:
        return self._post("/v1/session/replay", payload)

    def due_agents(self, payload: Dict[str, Any]) -> Dict[str, Any]:
        return self._post("/v1/scheduler/due", payload)

    def wait_for_proposal(self, job_id: str, *, deadline: float = 25.0, interval: float = 0.1) -> Dict[str, Any]:
        return self._wait_job(job_id, self.get_proposal_job, self.cancel_proposal_job, deadline, interval)

    def wait_for_generation(self, job_id: str, *, deadline: float = 45.0, interval: float = 0.1) -> Dict[str, Any]:
        return self._wait_job(job_id, self.get_generation_job, self.cancel_generation_job, deadline, interval)

    def _wait_job(
        self,
        job_id: str,
        getter: Callable[[str], Dict[str, Any]],
        canceler: Callable[[str], Dict[str, Any]],
        deadline: float,
        interval: float,
    ) -> Dict[str, Any]:
        if not 0.05 <= deadline <= 300.0 or not 0.01 <= interval <= 5.0:
            raise RinConfigurationError("invalid_polling", "job deadline or interval is out of range")
        expires = self._clock() + deadline
        while True:
            job = getter(job_id)
            status = str(job.get("status", ""))
            if status == "succeeded":
                return job
            if status in _TERMINAL_JOB_STATES:
                detail = job.get("error") if isinstance(job.get("error"), dict) else {}
                raise RinAPIError(
                    _safe_text(detail.get("code"), 96) or "job_" + status,
                    _safe_text(detail.get("message"), 500) or "Rin job ended as " + status,
                )
            if status not in ("queued", "running"):
                raise RinProtocolError("invalid_job", "Rin returned an unknown job status")
            remaining = expires - self._clock()
            if remaining <= 0:
                try:
                    canceler(job_id)
                except RinError:
                    pass
                raise RinAPIError("job_timeout", "Rin job exceeded its deadline")
            self._sleeper(min(interval, remaining))

    def _post(self, path: str, payload: Dict[str, Any]) -> Dict[str, Any]:
        return self._request("POST", path, payload)

    def _request(
        self,
        method: str,
        path: str,
        payload: Optional[Dict[str, Any]] = None,
        expected_statuses: Sequence[int] = (200,),
    ) -> Dict[str, Any]:
        if not path.startswith("/") or "//" in path or ".." in path:
            raise RinConfigurationError("invalid_path", "Rin request path is invalid")
        body = None
        headers = {"Accept": "application/json", "User-Agent": "rin-python/0.5"}
        if payload is not None:
            if not isinstance(payload, dict):
                raise RinProtocolError("invalid_request", "Rin payload must be an object")
            try:
                body = json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
            except (TypeError, ValueError) as exc:
                raise RinProtocolError("invalid_request", "Rin payload is not JSON serializable") from exc
            headers["Content-Type"] = "application/json"
        if self.token:
            headers["Authorization"] = "Bearer " + self.token
        request = Request(self.base_url + path, data=body, headers=headers, method=method)
        try:
            with self._opener.open(request, timeout=self.timeout) as response:
                return self._decode(response, int(response.getcode()), tuple(expected_statuses))
        except HTTPError as exc:
            try:
                return self._decode_error(exc, int(exc.code))
            finally:
                exc.close()
        except (URLError, TimeoutError, OSError) as exc:
            raise RinTransportError("transport_failed", "Rin is unavailable") from exc

    def _decode(self, response: Any, status: int, expected: Tuple[int, ...]) -> Dict[str, Any]:
        declared = response.headers.get("Content-Length", "")
        if declared:
            try:
                length = int(declared)
                if length < 0:
                    raise RinProtocolError("invalid_response", "Rin returned an invalid Content-Length")
                if length > self.max_response_bytes:
                    raise RinProtocolError("response_too_large", "Rin response exceeds the configured limit")
            except ValueError as exc:
                raise RinProtocolError("invalid_response", "Rin returned an invalid Content-Length") from exc
        raw = response.read(self.max_response_bytes + 1)
        if len(raw) > self.max_response_bytes:
            raise RinProtocolError("response_too_large", "Rin response exceeds the configured limit")
        envelope = _parse_envelope(raw)
        if status not in expected or envelope.get("ok") is not True:
            raise _api_error(envelope, status)
        data = envelope.get("data")
        if not isinstance(data, dict):
            raise RinProtocolError("invalid_response", "Rin response data must be an object")
        return data

    def _decode_error(self, response: HTTPError, status: int) -> Dict[str, Any]:
        if 300 <= status < 400:
            raise RinTransportError("redirect_rejected", "Rin endpoint attempted to redirect")
        raw = response.read(self.max_response_bytes + 1)
        if len(raw) > self.max_response_bytes:
            raise RinProtocolError("response_too_large", "Rin error response exceeds the configured limit")
        try:
            envelope = _parse_envelope(raw)
        except RinProtocolError:
            envelope = {}
        raise _api_error(envelope, status)


def _parse_envelope(raw: bytes) -> Dict[str, Any]:
    try:
        value = json.loads(raw.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise RinProtocolError("invalid_response", "Rin returned invalid JSON") from exc
    if not isinstance(value, dict):
        raise RinProtocolError("invalid_response", "Rin response must be an object")
    return value


def _api_error(envelope: Dict[str, Any], status: int) -> RinAPIError:
    detail = envelope.get("error") if isinstance(envelope.get("error"), dict) else {}
    return RinAPIError(
        _safe_text(detail.get("code"), 96) or "http_error",
        _safe_text(detail.get("message"), 500) or "Rin request failed",
        status=status,
        field=_safe_text(detail.get("field"), 160),
    )


def _normalize_base_url(value: str, token: str) -> str:
    parsed = urlsplit(str(value or DEFAULT_BASE_URL).strip().rstrip("/"))
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
        raise RinConfigurationError("invalid_base_url", "Rin base URL must be an origin")
    loopback = _is_loopback(parsed.hostname)
    if parsed.scheme == "http" and not loopback:
        raise RinConfigurationError("insecure_base_url", "remote Rin endpoints must use HTTPS")
    if not loopback and not token:
        raise RinConfigurationError("missing_token", "remote Rin endpoints require a token")
    if port is not None and not 1 <= port <= 65535:
        raise RinConfigurationError("invalid_base_url", "Rin base URL has an invalid port")
    return urlunsplit((parsed.scheme, parsed.netloc, "", "", "")).rstrip("/")


def _is_loopback(host: str) -> bool:
    if host.casefold() == "localhost":
        return True
    try:
        return ipaddress.ip_address(host).is_loopback
    except ValueError:
        return False


def _validate_token(value: str) -> str:
    token = str(value or "")
    if token != token.strip() or any(character in token for character in ("\x00", "\r", "\n")) or len(token) > 4096:
        raise RinConfigurationError("invalid_token", "Rin token must be a bounded single-line value")
    return token


def _path_id(value: str) -> str:
    text = str(value or "")
    if (
        not text
        or len(text) > 96
        or not all(character.isascii() and (character.isalnum() or character in "._-") for character in text)
    ):
        raise RinConfigurationError("invalid_identifier", "Rin path identifier is invalid")
    return quote(text, safe="._-")


def _safe_text(value: Any, maximum: int) -> str:
    return " ".join(str(value or "").replace("\x00", "").split())[:maximum]
