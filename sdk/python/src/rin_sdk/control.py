"""Zero-dependency client for the loopback Rin Control V2 daemon."""

from __future__ import annotations

import ipaddress
import json
import socket
import time
from typing import Any, Callable, Dict, Optional
from urllib.error import HTTPError, URLError
from urllib.parse import urlsplit, urlunsplit
from urllib.request import Request, build_opener

from ._common import (
    SDK_VERSION,
    RinAPIError,
    RinConfigurationError,
    RinError,
    RinProtocolError,
    RinTransportError,
    _NoRedirect,
    _read_bounded_response,
    _reject_json_constant,
    _remaining_timeout,
    _safe_text,
    _validate_request_json,
    _validate_token,
)


CONTROL_CONTRACT_VERSION = "rin.control/v2"
CONTROL_DEFAULT_BASE_URL = "http://127.0.0.1:7375"
CONTROL_MAX_RESPONSE_BYTES = 8 * 1024 * 1024


class RinControlClient:
    """Thin fixed-principal client; authority is configured on the daemon."""

    def __init__(
        self,
        base_url: str = CONTROL_DEFAULT_BASE_URL,
        *,
        token: str,
        timeout: float = 30.0,
        max_response_bytes: int = CONTROL_MAX_RESPONSE_BYTES,
        clock: Callable[[], float] = time.monotonic,
    ) -> None:
        self.token = _validate_control_token(token)
        self.base_url = _normalize_control_base_url(base_url)
        self.timeout = float(timeout)
        if not 0.05 <= self.timeout <= 120.0:
            raise RinConfigurationError(
                "invalid_timeout",
                "Control timeout must be between 0.05 and 120 seconds",
            )
        self.max_response_bytes = int(max_response_bytes)
        if not 1024 <= self.max_response_bytes <= CONTROL_MAX_RESPONSE_BYTES:
            raise RinConfigurationError(
                "invalid_response_limit",
                "Control response limit must be between 1 KiB and 8 MiB",
            )
        self._clock = clock
        self._opener = build_opener(_NoRedirect())

    def info(self) -> Dict[str, Any]:
        value = self._request("GET", "/control/v2/info")
        if not isinstance(value, dict) or value.get("contract_version") != CONTROL_CONTRACT_VERSION:
            raise RinProtocolError(
                "control_contract_mismatch",
                "Control Daemon returned an unsupported contract",
            )
        return value

    def list_worlds(self) -> Any:
        return self._post("/control/v2/worlds", {})

    def list_actors(self, payload: Dict[str, Any]) -> Any:
        return self._post("/control/v2/actors", payload)

    def get_actor(self, payload: Dict[str, Any]) -> Any:
        return self._post("/control/v2/actor", payload)

    def wait_actor(self, payload: Dict[str, Any]) -> Any:
        return self._post("/control/v2/wait-actor", payload)

    def observe_actor(self, payload: Dict[str, Any]) -> Any:
        return self._post("/control/v2/observe", payload)

    def list_capabilities(self, payload: Dict[str, Any]) -> Any:
        return self._post("/control/v2/capabilities", payload)

    def describe_capability(self, payload: Dict[str, Any]) -> Any:
        return self._post("/control/v2/capability", payload)

    def acquire_controller(self, payload: Dict[str, Any]) -> Any:
        return self._post("/control/v2/controllers/acquire", payload)

    def renew_controller(self, payload: Dict[str, Any]) -> Any:
        return self._post("/control/v2/controllers/renew", payload)

    def release_controller(self, payload: Dict[str, Any]) -> Any:
        return self._post("/control/v2/controllers/release", payload)

    def get_controller(self, payload: Dict[str, Any]) -> Any:
        return self._post("/control/v2/controllers/get", payload)

    def submit_action(self, payload: Dict[str, Any]) -> Any:
        return self._post("/control/v2/actions/submit", payload)

    def confirm_action(self, payload: Dict[str, Any]) -> Any:
        return self._post("/control/v2/actions/confirm", payload)

    def get_operation(self, payload: Dict[str, Any]) -> Any:
        return self._post("/control/v2/operations/get", payload)

    def wait_operation(self, payload: Dict[str, Any]) -> Any:
        return self._post("/control/v2/operations/wait", payload)

    def get_task_timeline(self, payload: Dict[str, Any]) -> Any:
        return self._post("/control/v2/tasks/timeline/get", payload)

    def wait_task_timeline(self, payload: Dict[str, Any]) -> Any:
        return self._post("/control/v2/tasks/timeline/wait", payload)

    def cancel_operation(self, payload: Dict[str, Any]) -> Any:
        return self._post("/control/v2/operations/cancel", payload)

    def set_emergency_stop(self, payload: Dict[str, Any]) -> Any:
        return self._post("/control/v2/emergency-stop", payload)

    def create_task_plan(self, payload: Dict[str, Any]) -> Any:
        return self._post("/plans/v1/create", payload)

    def get_task_plan(self, payload: Dict[str, Any]) -> Any:
        return self._post("/plans/v1/get", payload)

    def wait_task_plan(self, payload: Dict[str, Any]) -> Any:
        return self._post("/plans/v1/wait", payload)

    def revise_task_plan(self, payload: Dict[str, Any]) -> Any:
        return self._post("/plans/v1/revise", payload)

    def set_task_plan_status(self, payload: Dict[str, Any]) -> Any:
        return self._post("/plans/v1/status", payload)

    def request_task_step_transition(self, payload: Dict[str, Any]) -> Any:
        return self._post("/plans/v1/transition", payload)

    def submit_task_step_action(self, payload: Dict[str, Any]) -> Any:
        return self._post("/plans/v1/submit-step-action", payload)

    def _post(self, path: str, payload: Dict[str, Any]) -> Any:
        return self._request("POST", path, payload)

    def _request(
        self,
        method: str,
        path: str,
        payload: Optional[Dict[str, Any]] = None,
    ) -> Any:
        if (
            not path.startswith(("/control/v2/", "/plans/v1/"))
            or "//" in path
            or ".." in path
        ):
            raise RinConfigurationError("invalid_path", "Control request path is invalid")
        body = None
        headers = {
            "Accept": "application/json",
            "Authorization": "Bearer " + self.token,
            "User-Agent": "rin-control-python/" + SDK_VERSION,
        }
        if payload is not None:
            if not isinstance(payload, dict):
                raise RinProtocolError("invalid_request", "Control payload must be an object")
            _validate_request_json(payload)
            try:
                body = json.dumps(
                    payload,
                    ensure_ascii=False,
                    separators=(",", ":"),
                    allow_nan=False,
                ).encode("utf-8")
            except (TypeError, ValueError, UnicodeEncodeError) as exc:
                raise RinProtocolError(
                    "invalid_request",
                    "Control payload is not JSON serializable",
                ) from exc
            headers["Content-Type"] = "application/json"
        request = Request(self.base_url + path, data=body, headers=headers, method=method)
        deadline = self._clock() + self.timeout
        try:
            with self._opener.open(
                request,
                timeout=_remaining_timeout(deadline, self._clock),
            ) as response:
                return self._decode(response, int(response.getcode()), deadline)
        except HTTPError as exc:
            try:
                return self._decode(exc, int(exc.code), deadline)
            finally:
                exc.close()
        except RinError:
            raise
        except (URLError, TimeoutError, OSError) as exc:
            reason = getattr(exc, "reason", None)
            if isinstance(exc, (TimeoutError, socket.timeout)) or isinstance(
                reason, (TimeoutError, socket.timeout)
            ):
                raise RinTransportError(
                    "transport_timeout",
                    "Control Daemon request timed out",
                ) from exc
            raise RinTransportError(
                "transport_failed",
                "Control Daemon is unavailable",
            ) from exc

    def _decode(self, response: Any, status: int, deadline: float) -> Any:
        if 300 <= status < 400:
            raise RinTransportError(
                "redirect_rejected",
                "Control Daemon attempted to redirect",
            )
        content_type = response.headers.get("Content-Type", "")
        if content_type.split(";", 1)[0].strip().lower() != "application/json":
            raise RinProtocolError(
                "invalid_response",
                "Control Daemon response must be application/json",
            )
        declared = response.headers.get("Content-Length", "")
        if declared:
            try:
                length = int(declared)
            except ValueError as exc:
                raise RinProtocolError(
                    "invalid_response",
                    "Control Daemon returned an invalid Content-Length",
                ) from exc
            if length < 0:
                raise RinProtocolError(
                    "invalid_response",
                    "Control Daemon returned an invalid Content-Length",
                )
            if length > self.max_response_bytes:
                raise RinProtocolError(
                    "response_too_large",
                    "Control Daemon response exceeds the configured limit",
                )
        raw = _read_bounded_response(
            response,
            self.max_response_bytes,
            deadline,
            self._clock,
            "Control Daemon response exceeds the configured limit",
        )
        try:
            value = json.loads(raw.decode("utf-8"), parse_constant=_reject_json_constant)
        except (UnicodeDecodeError, ValueError) as exc:
            raise RinProtocolError(
                "invalid_response",
                "Control Daemon returned invalid JSON",
            ) from exc
        if not isinstance(value, (dict, list)):
            raise RinProtocolError(
                "invalid_response",
                "Control Daemon response must be an object or array",
            )
        if not 200 <= status < 300:
            detail = value if isinstance(value, dict) else {}
            raise RinAPIError(
                _safe_text(detail.get("code"), 96) or _control_error_code(status),
                _safe_text(detail.get("error"), 500) or "Control Daemon request failed",
                status=status,
            )
        return value


def _normalize_control_base_url(value: str) -> str:
    parsed = urlsplit(str(value or CONTROL_DEFAULT_BASE_URL).strip())
    try:
        port = parsed.port
    except ValueError as exc:
        raise RinConfigurationError(
            "invalid_base_url",
            "Control Daemon URL has an invalid port",
        ) from exc
    if (
        parsed.scheme != "http"
        or not parsed.hostname
        or parsed.username is not None
        or parsed.password is not None
        or parsed.query
        or parsed.fragment
        or parsed.path not in ("", "/")
        or port is None
        or not 1 <= port <= 65535
        or not _is_loopback(parsed.hostname)
    ):
        raise RinConfigurationError(
            "invalid_base_url",
            "Control Daemon URL must be a plain loopback HTTP origin with an explicit port",
        )
    return urlunsplit((parsed.scheme, parsed.netloc, "", "", ""))


def _is_loopback(host: str) -> bool:
    if host.casefold() == "localhost":
        return True
    try:
        return ipaddress.ip_address(host).is_loopback
    except ValueError:
        return False


def _validate_control_token(value: str) -> str:
    token = _validate_token(value)
    if len(token.encode("utf-8")) < 32:
        raise RinConfigurationError(
            "invalid_token",
            "Control token must contain at least 32 bytes",
        )
    return token


def _control_error_code(status: int) -> str:
    if status == 400:
        return "invalid"
    if status in (401, 403):
        return "forbidden"
    if status == 404:
        return "not_found"
    if status == 409:
        return "conflict"
    if status == 410:
        return "unavailable"
    if status == 429:
        return "capacity"
    return "unavailable"
