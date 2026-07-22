"""Dependency-free Rin Protocol v1 client for Ren'Py and regular Python.

The client deliberately keeps threads, cancellation events, and transport state
outside Ren'Py saves. Call ``propose_with_fallback`` from a background worker,
then store only its returned JSON-compatible dictionary on the main thread.
"""

from __future__ import annotations

import hashlib
import ipaddress
import json
import socket
import threading
import time
from typing import Any, Callable, Dict, Optional, Sequence
from urllib.error import HTTPError, URLError
from urllib.parse import urlsplit, urlunsplit
from urllib.request import HTTPRedirectHandler, HTTPSHandler, Request, build_opener


PROTOCOL_VERSION = "rin.protocol/v1"
DEFAULT_BASE_URL = "http://127.0.0.1:7374"
DEFAULT_MAX_RESPONSE_BYTES = 2 * 1024 * 1024
TERMINAL_JOB_STATES = frozenset((
    "succeeded",
    "failed",
    "stale",
    "canceled",
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
    pass


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
        return self._request("POST", "/v1/session/create", request)

    def observe(self, request: Dict[str, Any]) -> Dict[str, Any]:
        return self._request("POST", "/v1/session/observe", request)

    def propose(self, request: Dict[str, Any]) -> Dict[str, Any]:
        return self._request("POST", "/v1/agent/propose", request)

    def submit_proposal_job(self, request: Dict[str, Any]) -> Dict[str, Any]:
        return self._request("POST", "/v1/jobs/propose", request, expected_statuses=(202,))

    def get_proposal_job(self, job_id: str) -> Dict[str, Any]:
        return self._request("GET", "/v1/jobs/" + _path_identifier(job_id))

    def cancel_proposal_job(self, job_id: str) -> Dict[str, Any]:
        return self._request("DELETE", "/v1/jobs/" + _path_identifier(job_id))

    def commit(self, request: Dict[str, Any]) -> Dict[str, Any]:
        return self._request("POST", "/v1/action/commit", request)

    def state(self, request: Dict[str, Any]) -> Dict[str, Any]:
        return self._request("POST", "/v1/session/get", request)

    def snapshot(self, request: Dict[str, Any]) -> Dict[str, Any]:
        return self._request("POST", "/v1/session/snapshot", request)

    def restore(self, request: Dict[str, Any]) -> Dict[str, Any]:
        return self._request("POST", "/v1/session/restore", request)

    def due_agents(self, request: Dict[str, Any]) -> Dict[str, Any]:
        return self._request("POST", "/v1/scheduler/due", request)

    def wait_for_proposal(
        self,
        job_id: str,
        *,
        deadline_seconds: float = 25.0,
        poll_interval: float = 0.1,
        cancel_event: Optional[threading.Event] = None,
    ) -> Dict[str, Any]:
        deadline_seconds = float(deadline_seconds)
        poll_interval = float(poll_interval)
        if not 0.05 <= deadline_seconds <= 300.0:
            raise RinConfigurationError("invalid_deadline", "Job deadline must be between 0.05 and 300 seconds")
        if not 0.01 <= poll_interval <= 5.0:
            raise RinConfigurationError("invalid_poll_interval", "Job poll interval must be between 0.01 and 5 seconds")
        deadline = self._clock() + deadline_seconds
        while True:
            if cancel_event is not None and cancel_event.is_set():
                self._cancel_quietly(job_id)
                raise RinJobError("job_canceled", "Proposal job was canceled")
            job = self.get_proposal_job(job_id)
            status = str(job.get("status", ""))
            if status == "succeeded":
                proposal = job.get("proposal")
                if not isinstance(proposal, dict):
                    raise RinProtocolError("invalid_job", "Successful proposal job did not include a proposal")
                return job
            if status in TERMINAL_JOB_STATES:
                detail = job.get("error", {})
                if not isinstance(detail, dict):
                    detail = {}
                raise RinJobError(
                    _safe_text(detail.get("code"), 96) or "job_" + status,
                    _safe_text(detail.get("message"), 500) or "Proposal job ended as " + status,
                    field=_safe_text(detail.get("field"), 160),
                )
            if status not in ("queued", "running"):
                raise RinProtocolError("invalid_job", "Proposal job returned an unknown status")
            remaining = deadline - self._clock()
            if remaining <= 0:
                self._cancel_quietly(job_id)
                raise RinJobError("job_timeout", "Proposal job exceeded its deadline")
            delay = min(poll_interval, remaining)
            if cancel_event is not None:
                cancel_event.wait(delay)
            else:
                self._sleeper(delay)

    def propose_with_fallback(
        self,
        request: Dict[str, Any],
        *,
        fallback_action_id: str = "",
        deadline_seconds: float = 25.0,
        poll_interval: float = 0.1,
        cancel_event: Optional[threading.Event] = None,
    ) -> Dict[str, Any]:
        job_id = ""
        try:
            submission = self.submit_proposal_job(request)
            job_id = str(submission.get("job_id", ""))
            if not job_id:
                raise RinProtocolError("invalid_submission", "Rin did not return a proposal job id")
            job = self.wait_for_proposal(
                job_id,
                deadline_seconds=deadline_seconds,
                poll_interval=poll_interval,
                cancel_event=cancel_event,
            )
            return {
                "source": "sidecar",
                "committable": True,
                "fallback_reason": "",
                "job_id": job_id,
                "proposal": _json_clone(job["proposal"]),
            }
        except RinJobError as exc:
            if exc.code == "job_canceled":
                raise
            return offline_proposal_result(
                request,
                fallback_action_id=fallback_action_id,
                reason=exc.code,
                job_id=job_id,
            )
        except RinError as exc:
            return offline_proposal_result(
                request,
                fallback_action_id=fallback_action_id,
                reason=exc.code,
                job_id=job_id,
            )

    def _cancel_quietly(self, job_id: str) -> None:
        try:
            self.cancel_proposal_job(job_id)
        except RinError:
            pass

    def _request(
        self,
        method: str,
        path: str,
        payload: Optional[Dict[str, Any]] = None,
        *,
        expected_statuses: Sequence[int] = (200,),
    ) -> Dict[str, Any]:
        body = None
        headers = {"Accept": "application/json", "User-Agent": "rin-renpy/0.3"}
        if payload is not None:
            if not isinstance(payload, dict):
                raise RinProtocolError("invalid_request", "Rin request payload must be an object")
            body = json.dumps(
                payload,
                ensure_ascii=False,
                separators=(",", ":"),
            ).encode("utf-8")
            headers["Content-Type"] = "application/json"
        if self.token:
            headers["Authorization"] = "Bearer " + self.token
        request = Request(self.base_url + path, data=body, headers=headers, method=method)
        try:
            with self._opener.open(request, timeout=self.timeout) as response:
                status = int(getattr(response, "status", response.getcode()))
                response_payload = self._read_response(response)
        except HTTPError as exc:
            response_payload = self._read_response(exc)
            decoded = _decode_json(response_payload, allow_failure=True)
            raise _error_from_envelope(decoded, exc.code) from None
        except (socket.timeout, TimeoutError) as exc:
            raise RinTransportError("transport_timeout", "Rin request timed out") from exc
        except URLError as exc:
            reason = getattr(exc, "reason", None)
            code = "transport_timeout" if isinstance(reason, (socket.timeout, TimeoutError)) else "transport_failed"
            message = "Rin request timed out" if code == "transport_timeout" else "Could not reach the Rin Sidecar"
            raise RinTransportError(code, message) from exc
        except OSError as exc:
            raise RinTransportError("transport_failed", "Could not reach the Rin Sidecar") from exc
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

    def _read_response(self, response: Any) -> bytes:
        length = response.headers.get("Content-Length") if getattr(response, "headers", None) else None
        if length:
            try:
                if int(length) > self.max_response_bytes:
                    raise RinProtocolError("response_too_large", "Rin response exceeded the configured limit")
            except ValueError:
                raise RinProtocolError("invalid_response", "Rin response had an invalid Content-Length")
        payload = response.read(self.max_response_bytes + 1)
        if len(payload) > self.max_response_bytes:
            raise RinProtocolError("response_too_large", "Rin response exceeded the configured limit")
        return payload


def _decode_json(payload: bytes, *, allow_failure: bool = False) -> Dict[str, Any]:
    try:
        decoded = json.loads(payload.decode("utf-8"))
    except (UnicodeDecodeError, ValueError) as exc:
        if allow_failure:
            return {}
        raise RinProtocolError("invalid_response", "Rin returned invalid JSON") from exc
    if not isinstance(decoded, dict):
        if allow_failure:
            return {}
        raise RinProtocolError("invalid_response", "Rin response must be a JSON object")
    return decoded


def _path_identifier(value: str) -> str:
    text = str(value or "")
    if not text or len(text) > 96 or not text[0].isalnum():
        raise RinProtocolError("invalid_id", "Rin identifier is invalid")
    if any(not (character.isascii() and (character.isalnum() or character in "._-")) for character in text):
        raise RinProtocolError("invalid_id", "Rin identifier is invalid")
    return text


def offline_proposal_result(
    request: Dict[str, Any],
    *,
    fallback_action_id: str = "",
    reason: str = "offline",
    job_id: str = "",
) -> Dict[str, Any]:
    """Build an authored, non-committable fallback from the candidate list.

    Candidate order remains game-authored priority. Callers can name a safer
    fallback action explicitly for consent, privacy, combat, or economy paths.
    """

    if not isinstance(request, dict):
        raise RinProtocolError("invalid_request", "Proposal request must be an object")
    actions = request.get("candidate_actions")
    if not isinstance(actions, list) or not actions:
        raise RinProtocolError("invalid_request", "Proposal request needs candidate actions")
    normalized = [item for item in actions if isinstance(item, dict) and str(item.get("id", ""))]
    if not normalized:
        raise RinProtocolError("invalid_request", "Proposal request has no valid candidate action")
    selected = None
    if fallback_action_id:
        selected = next(
            (item for item in normalized if str(item.get("id")) == fallback_action_id),
            None,
        )
        if selected is None:
            raise RinProtocolError("invalid_fallback", "Fallback action is not in the candidate list")
    if selected is None:
        selected = normalized[0]
    selected = _json_clone(selected)
    kind = str(selected.get("kind", ""))
    stance = kind if kind in ("engage", "partial", "redirect", "refuse", "wait") else "engage"
    canonical = json.dumps(
        {"request": request, "action_id": selected.get("id")},
        ensure_ascii=False,
        sort_keys=True,
        separators=(",", ":"),
    ).encode("utf-8")
    proposal_id = "offline." + hashlib.sha256(canonical).hexdigest()[:24]
    return {
        "source": "offline",
        "committable": False,
        "fallback_reason": _safe_text(reason, 96) or "offline",
        "job_id": _safe_text(job_id, 96),
        "proposal": {
            "id": proposal_id,
            "session_id": _safe_text(request.get("session_id"), 96),
            "request_id": _safe_text(request.get("request_id"), 96),
            "actor_id": _safe_text(request.get("actor_id"), 96),
            "tick": max(0, int(request.get("tick", 0) or 0)),
            "based_on_revision": 0,
            "based_on_head_hash": "offline",
            "created_revision": 0,
            "action": selected,
            "stance": stance,
            "summary": "The game used its authored offline fallback.",
            "rationale": "The Rin Sidecar was unavailable; world state remains game-owned.",
            "policy_source": "adapter-offline",
            "recalled_memory_ids": [],
            "status": "offline",
        },
    }


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
        fallback_action_id: str = "",
        deadline_seconds: float = 25.0,
        poll_interval: float = 0.1,
    ) -> str:
        request_id = _path_identifier(str(request.get("request_id", "")))
        request_fingerprint = hashlib.sha256(json.dumps(
            request,
            ensure_ascii=False,
            sort_keys=True,
            separators=(",", ":"),
        ).encode("utf-8")).hexdigest()
        with self._lock:
            if request_id in self._entries:
                if self._entries[request_id]["request_fingerprint"] != request_fingerprint:
                    raise RinProtocolError(
                        "request_id_conflict",
                        "Request id was already used with a different proposal payload",
                    )
                return request_id
            self._prune_locked()
            if len(self._entries) >= self.maximum:
                raise RinProtocolError("registry_full", "Rin background registry is full")
            cancel_event = threading.Event()
            self._entries[request_id] = {
                "status": "pending",
                "request_fingerprint": request_fingerprint,
                "cancel_event": cancel_event,
                "result": None,
                "error_code": "",
            }

        request_snapshot = _json_clone(request)

        def worker() -> None:
            try:
                result = self.client.propose_with_fallback(
                    request_snapshot,
                    fallback_action_id=fallback_action_id,
                    deadline_seconds=deadline_seconds,
                    poll_interval=poll_interval,
                    cancel_event=cancel_event,
                )
                status = "complete"
                error_code = ""
            except RinError as exc:
                result = None
                status = "canceled" if exc.code == "job_canceled" else "failed"
                error_code = exc.code
            with self._lock:
                entry = self._entries.get(request_id)
                if entry is not None:
                    entry["status"] = status
                    entry["result"] = _json_clone(result) if result is not None else None
                    entry["error_code"] = error_code

        try:
            launch(worker)
        except Exception:
            with self._lock:
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
            if not entry or entry["status"] not in ("complete", "failed", "canceled"):
                return None
            self._entries.pop(str(request_id), None)
            return {
                "status": entry["status"],
                "error_code": entry["error_code"],
                "result": _json_clone(entry["result"]) if entry["result"] is not None else None,
            }

    def cancel(self, request_id: str) -> bool:
        with self._lock:
            entry = self._entries.get(str(request_id))
            if not entry or entry["status"] != "pending":
                return False
            entry["cancel_event"].set()
            return True

    def _prune_locked(self) -> None:
        terminal = [
            key
            for key, entry in self._entries.items()
            if entry.get("status") in ("complete", "failed", "canceled")
        ]
        while len(self._entries) >= self.maximum and terminal:
            self._entries.pop(terminal.pop(0), None)
