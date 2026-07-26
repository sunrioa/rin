"""Rollback-safe Host Epoch coordination for Ren'Py.

Save ``EpochRuntime.bind`` results in the normal Ren'Py store. Keep the
runtime's ``persistent_ledger`` in Ren'Py ``persistent`` so loading or rolling
back an older store value cannot reuse an earlier Host or Timeline generation.
"""

from __future__ import annotations

import json
import re
from typing import Any, Dict, Optional, Tuple

MAX_GENERATION = 9_007_199_254_740_991
MAX_SESSIONS = 256
STATE_VERSION = 1
SAFE_ID = re.compile(r"^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$")
FORK_REASONS = frozenset(("load", "rollback", "manual"))
STATE_KEYS = frozenset(
    (
        "version",
        "session_id",
        "world_id",
        "host",
        "world",
        "timeline",
        "fork_reason",
    )
)
LEDGER_KEYS = frozenset(("version", "host", "timelines"))
WIRE_EPOCH_KEYS = (
    "session_id",
    "world_id",
    "host",
    "world",
    "timeline",
)


class EpochStateError(ValueError):
    """Raised when persisted Epoch data is unsafe or inconsistent."""


def _clone(value: Dict[str, Any]) -> Dict[str, Any]:
    return json.loads(
        json.dumps(value, ensure_ascii=False, separators=(",", ":"))
    )


def _safe_generation(field: str, value: Any) -> int:
    if (
        isinstance(value, bool)
        or not isinstance(value, int)
        or value <= 0
        or value > MAX_GENERATION
    ):
        raise EpochStateError(field + " must be a positive JSON-safe integer")
    return value


def _safe_id(field: str, value: Any) -> str:
    if (
        not isinstance(value, str)
        or len(value.encode("utf-8")) > 128
        or SAFE_ID.fullmatch(value) is None
    ):
        raise EpochStateError(field + " must be a lowercase safe identifier")
    return value


def _increment(field: str, value: int) -> int:
    if value >= MAX_GENERATION:
        raise EpochStateError(field + " exhausted the JSON-safe integer range")
    return value + 1


def _validate_state(value: Any) -> Dict[str, Any]:
    if not isinstance(value, dict) or frozenset(value) != STATE_KEYS:
        raise EpochStateError("saved Epoch has an invalid shape")
    if value.get("version") != STATE_VERSION:
        raise EpochStateError("saved Epoch version is unsupported")
    state = {
        "version": STATE_VERSION,
        "session_id": _safe_id("session_id", value.get("session_id")),
        "world_id": _safe_id("world_id", value.get("world_id")),
        "host": _safe_generation("host", value.get("host")),
        "world": _safe_generation("world", value.get("world")),
        "timeline": _safe_generation("timeline", value.get("timeline")),
        "fork_reason": str(value.get("fork_reason", "")),
    }
    if state["fork_reason"] not in FORK_REASONS and state["fork_reason"] != "":
        raise EpochStateError("saved Epoch fork_reason is unsupported")
    return state


def _validate_ledger(value: Optional[Dict[str, Any]]) -> Dict[str, Any]:
    if value is None:
        return {"version": STATE_VERSION, "host": 0, "timelines": {}}
    if not isinstance(value, dict) or frozenset(value) != LEDGER_KEYS:
        raise EpochStateError("persistent Epoch ledger has an invalid shape")
    if value.get("version") != STATE_VERSION:
        raise EpochStateError("persistent Epoch ledger version is unsupported")
    host = value.get("host")
    if isinstance(host, bool) or not isinstance(host, int) or host < 0:
        raise EpochStateError("persistent host generation is invalid")
    if host > MAX_GENERATION:
        raise EpochStateError("persistent host generation is not JSON-safe")
    timelines = value.get("timelines")
    if not isinstance(timelines, dict) or len(timelines) > MAX_SESSIONS:
        raise EpochStateError("persistent timeline ledger is invalid or full")
    normalized = {}
    for session_id, generation in timelines.items():
        normalized[_safe_id("timeline session_id", session_id)] = (
            _safe_generation("timeline generation", generation)
        )
    return {
        "version": STATE_VERSION,
        "host": host,
        "timelines": normalized,
    }


def merge_persistent_ledgers(
    old: Optional[Dict[str, Any]],
    new: Optional[Dict[str, Any]],
    current: Optional[Dict[str, Any]],
) -> Dict[str, Any]:
    """Merge Ren'Py persistent replicas without lowering any generation."""
    ledgers = [
        _validate_ledger(value)
        for value in (old, new, current)
        if value is not None
    ]
    if not ledgers:
        return _validate_ledger(None)
    merged = {"version": STATE_VERSION, "host": 0, "timelines": {}}
    for ledger in ledgers:
        merged["host"] = max(merged["host"], ledger["host"])
        for session_id, generation in ledger["timelines"].items():
            merged["timelines"][session_id] = max(
                merged["timelines"].get(session_id, 0),
                generation,
            )
    if len(merged["timelines"]) > MAX_SESSIONS:
        raise EpochStateError("merged persistent timeline ledger is full")
    return merged


def wire_epoch(saved_epoch: Dict[str, Any]) -> Dict[str, Any]:
    """Return the protocol Epoch represented by a saved Ren'Py state."""
    state = _validate_state(saved_epoch)
    return {field: state[field] for field in WIRE_EPOCH_KEYS}


def proposal_matches_epoch(
    request: Any,
    saved_epoch: Dict[str, Any],
) -> bool:
    """Return whether every Epoch assertion in a proposal is current."""
    expected = wire_epoch(saved_epoch)
    if not isinstance(request, dict):
        return False
    if request.get("session_id") != expected["session_id"]:
        return False
    window = request.get("decision_window")
    if not isinstance(window, dict) or window.get("epoch") != expected:
        return False
    offers = request.get("offers")
    return (
        isinstance(offers, list)
        and bool(offers)
        and all(
            isinstance(offer, dict)
            and offer.get("expected_epoch") == expected
            for offer in offers
        )
    )


class EpochRuntime:
    """Process-local coordinator; this object must never enter a Ren'Py save."""

    def __init__(self, persistent_ledger: Optional[Dict[str, Any]]) -> None:
        self._ledger = _validate_ledger(persistent_ledger)
        self._ledger["host"] = _increment("host", self._ledger["host"])
        self._host = self._ledger["host"]
        self._rollback_active = False

    @property
    def persistent_ledger(self) -> Dict[str, Any]:
        return _clone(self._ledger)

    def bind(
        self,
        saved_epoch: Optional[Dict[str, Any]],
        session_id: str,
        world_id: str,
    ) -> Dict[str, Any]:
        session_id = _safe_id("session_id", session_id)
        world_id = _safe_id("world_id", world_id)
        saved = _validate_state(saved_epoch) if saved_epoch is not None else None
        if saved is not None and saved["session_id"] == session_id:
            if saved["world_id"] == world_id:
                world_generation = saved["world"]
                timeline_generation = saved["timeline"]
            else:
                world_generation = _increment("world", saved["world"])
                timeline_generation = self._next_timeline(session_id, 0)
        else:
            world_generation = 1
            timeline_generation = self._next_timeline(session_id, 0)
        self._remember_timeline(session_id, timeline_generation)
        return {
            "version": STATE_VERSION,
            "session_id": session_id,
            "world_id": world_id,
            "host": self._host,
            "world": world_generation,
            "timeline": timeline_generation,
            "fork_reason": "",
        }

    def fork(
        self,
        saved_epoch: Dict[str, Any],
        reason: str,
    ) -> Dict[str, Any]:
        saved = _validate_state(saved_epoch)
        if reason not in FORK_REASONS:
            raise EpochStateError("fork reason is unsupported")
        timeline = self._next_timeline(
            saved["session_id"],
            saved["timeline"],
        )
        self._remember_timeline(saved["session_id"], timeline)
        return {
            "version": STATE_VERSION,
            "session_id": saved["session_id"],
            "world_id": saved["world_id"],
            "host": self._host,
            "world": saved["world"],
            "timeline": timeline,
            "fork_reason": reason,
        }

    def after_load(self, saved_epoch: Dict[str, Any]) -> Dict[str, Any]:
        self._rollback_active = True
        return self.fork(saved_epoch, "load")

    def observe_rollback(
        self,
        saved_epoch: Dict[str, Any],
        in_rollback: bool,
    ) -> Tuple[Dict[str, Any], bool]:
        if not in_rollback:
            self._rollback_active = False
            return _validate_state(saved_epoch), False
        if self._rollback_active:
            return _validate_state(saved_epoch), False
        self._rollback_active = True
        return self.fork(saved_epoch, "rollback"), True

    def _next_timeline(self, session_id: str, saved: int) -> int:
        current = self._ledger["timelines"].get(session_id, 0)
        return _increment("timeline", max(current, saved))

    def _remember_timeline(self, session_id: str, generation: int) -> None:
        timelines = self._ledger["timelines"]
        if session_id not in timelines and len(timelines) >= MAX_SESSIONS:
            raise EpochStateError("persistent timeline ledger is full")
        timelines[session_id] = max(timelines.get(session_id, 0), generation)
