"""Shared, dependency-free transport safety helpers for Rin clients."""

from __future__ import annotations

import math
from typing import Any, Callable
from urllib.request import HTTPRedirectHandler


SDK_VERSION = "0.7.0"
_MAX_JSON_SAFE_INTEGER = 9_007_199_254_740_991
_MAX_JSON_DEPTH = 64


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


def _validate_request_json(value: Any) -> None:
    def visit(current: Any, depth: int, active: set[int]) -> None:
        if depth > _MAX_JSON_DEPTH:
            raise RinProtocolError("invalid_request", "Rin payload exceeds the JSON nesting limit")
        if current is None or isinstance(current, (str, bool)):
            return
        if isinstance(current, int):
            if not -_MAX_JSON_SAFE_INTEGER <= current <= _MAX_JSON_SAFE_INTEGER:
                raise RinProtocolError("invalid_request", "Rin payload contains an unsafe JSON integer")
            return
        if isinstance(current, float):
            if not math.isfinite(current):
                raise RinProtocolError("invalid_request", "Rin payload contains a non-finite JSON number")
            if current.is_integer() and not -_MAX_JSON_SAFE_INTEGER <= current <= _MAX_JSON_SAFE_INTEGER:
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


def _reject_json_constant(value: str) -> None:
    raise ValueError("Non-finite JSON number is not permitted: " + value)


def _validate_token(value: str) -> str:
    token = str(value or "")
    if (
        token != token.strip()
        or any(character in token for character in ("\x00", "\r", "\n"))
        or len(token) > 4096
    ):
        raise RinConfigurationError(
            "invalid_token",
            "Rin token must be a bounded single-line value",
        )
    return token


def _safe_text(value: Any, maximum: int) -> str:
    return " ".join(str(value or "").replace("\x00", "").split())[:maximum]
