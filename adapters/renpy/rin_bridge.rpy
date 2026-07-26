# Copy rin_client.py, rin_epoch.py, and this file into a game's game/ directory.
# The bridge keeps all worker objects process-local and returns plain JSON data.

default rin_host_epoch_state = None
default persistent.rin_host_epoch_ledger = None

init -30 python:
    import hashlib
    import json
    import os

    import rin_client
    import rin_epoch

    renpy.register_persistent(
        "rin_host_epoch_ledger",
        rin_epoch.merge_persistent_ledgers,
    )

    _RIN_CLIENT = None
    _RIN_REGISTRY = None
    _RIN_CONFIG_FINGERPRINT = None
    _RIN_UNRESOLVED_ATTEMPTS = {}
    _RIN_EPOCH_RUNTIME = None

    def _rin_env_enabled(name, default="0"):
        value = os.environ.get(name, default).strip().lower()
        return value not in ("", "0", "false", "no", "off")

    def _rin_env_float(name, default, minimum, maximum):
        try:
            return max(minimum, min(maximum, float(os.environ.get(name, str(default)))))
        except Exception:
            return default

    def _rin_transport_enabled():
        if not _rin_env_enabled("RIN_ENABLED", "0"):
            return False
        if renpy.is_in_test() and not _rin_env_enabled("RIN_LIVE_TEST_ENABLED", "0"):
            return False
        return True

    def _rin_config():
        return {
            "base_url": os.environ.get("RIN_BASE_URL", rin_client.DEFAULT_BASE_URL),
            "token": os.environ.get("RIN_TOKEN", ""),
            "timeout": _rin_env_float("RIN_TIMEOUT_SECONDS", 5.0, 0.05, 120.0),
            "deadline": _rin_env_float("RIN_JOB_DEADLINE_SECONDS", 25.0, 0.05, 300.0),
            "poll_interval": _rin_env_float("RIN_POLL_INTERVAL_SECONDS", 0.1, 0.01, 5.0),
        }

    def _rin_runtime():
        global _RIN_CLIENT, _RIN_REGISTRY, _RIN_CONFIG_FINGERPRINT
        if not _rin_transport_enabled():
            return None, None, "disabled"
        config = _rin_config()
        fingerprint = json.dumps({
            "base_url": config["base_url"],
            "timeout": config["timeout"],
            "token_hash": (
                hashlib.sha256(config["token"].encode("utf-8")).hexdigest()
                if config["token"]
                else ""
            ),
        }, sort_keys=True, separators=(",", ":"))
        if _RIN_REGISTRY is not None and fingerprint == _RIN_CONFIG_FINGERPRINT:
            return _RIN_CLIENT, _RIN_REGISTRY, ""
        try:
            client = rin_client.RinClient(
                config["base_url"],
                token=config["token"],
                timeout=config["timeout"],
            )
        except rin_client.RinError as exc:
            renpy.log("Rin adapter configuration failed: " + exc.code)
            return None, None, exc.code
        _RIN_CLIENT = client
        _RIN_REGISTRY = rin_client.BackgroundProposalRegistry(client)
        _RIN_CONFIG_FINGERPRINT = fingerprint
        return _RIN_CLIENT, _RIN_REGISTRY, ""

    def _rin_request_fingerprint(request):
        payload = json.dumps(
            request,
            ensure_ascii=False,
            sort_keys=True,
            separators=(",", ":"),
        ).encode("utf-8")
        return hashlib.sha256(payload).hexdigest()

    def _rin_store_unresolved_attempt(request_id, request, job_id, error_code):
        _RIN_UNRESOLVED_ATTEMPTS[str(request_id)] = {
            "status": "unresolved",
            "request_fingerprint": _rin_request_fingerprint(request),
            "request": json.loads(json.dumps(
                request,
                ensure_ascii=False,
                separators=(",", ":"),
            )),
            "job_id": str(job_id or ""),
            "error_code": str(error_code or "job_outcome_unknown"),
        }

    def rin_schedule_proposal(
        request,
        known_job_id="",
    ):
        """Start one proposal without blocking the Ren'Py interaction thread."""
        if rin_host_epoch_state is None:
            raise rin_client.RinProtocolError(
                "epoch_not_bound",
                "Bind the Host Epoch before scheduling a proposal",
            )
        if not rin_epoch.proposal_matches_epoch(
            request,
            rin_host_epoch_state,
        ):
            raise rin_client.RinProtocolError(
                "stale_epoch",
                "Proposal does not match the current Host Epoch",
            )
        request_id = str(request.get("request_id", ""))
        if not request_id:
            raise rin_client.RinProtocolError("invalid_request", "Proposal request needs request_id")
        retained = _RIN_UNRESOLVED_ATTEMPTS.get(request_id)
        if retained is not None:
            if retained["status"] == "stale":
                raise rin_client.RinProtocolError(
                    "stale_epoch",
                    "Proposal belongs to an earlier Host Epoch",
                )
            if retained["request_fingerprint"] != _rin_request_fingerprint(request):
                raise rin_client.RinProtocolError(
                    "request_id_conflict",
                    "Request id was already used with a different proposal payload",
                )
            request = retained["request"]
            known_job_id = retained["job_id"]
        client, registry, disabled_reason = _rin_runtime()
        if registry is None:
            _rin_store_unresolved_attempt(
                request_id,
                request,
                known_job_id,
                disabled_reason or (
                    "job_outcome_unknown"
                    if known_job_id
                    else "proposal_outcome_unknown"
                ),
            )
            return request_id
        config = _rin_config()
        scheduled = registry.schedule(
            request,
            renpy.invoke_in_thread,
            deadline_seconds=config["deadline"],
            poll_interval=config["poll_interval"],
            known_job_id=known_job_id,
        )
        _RIN_UNRESOLVED_ATTEMPTS.pop(request_id, None)
        return scheduled

    def rin_proposal_status(request_id):
        request_id = str(request_id)
        if request_id in _RIN_UNRESOLVED_ATTEMPTS:
            if _RIN_UNRESOLVED_ATTEMPTS[request_id]["status"] == "stale":
                return "ready"
            return "unresolved"
        if _RIN_REGISTRY is None:
            return "missing"
        status = _RIN_REGISTRY.status(request_id)
        if status in ("complete", "failed", "canceled", "invalidated"):
            return "ready"
        return status

    def rin_consume_proposal(request_id):
        """Return a plain adapter result once; return None while still pending."""
        request_id = str(request_id)
        retained = _RIN_UNRESOLVED_ATTEMPTS.get(request_id)
        if retained is not None:
            if retained["status"] != "stale":
                return None
            _RIN_UNRESOLVED_ATTEMPTS.pop(request_id, None)
            return {
                "source": "stale",
                "error_code": "stale_epoch",
                "job_id": retained.get("job_id", ""),
                "proposal": None,
            }
        if _RIN_REGISTRY is None:
            return None
        entry = _RIN_REGISTRY.consume(request_id)
        if entry is None:
            return None
        if entry["status"] == "complete":
            return entry["result"]
        if entry["status"] == "invalidated":
            return {
                "source": "stale",
                "error_code": entry["error_code"],
                "job_id": entry.get("job_id", ""),
                "proposal": None,
            }
        return {
            "source": "canceled" if entry["status"] == "canceled" else "error",
            "error_code": entry["error_code"],
            "job_id": entry.get("job_id", ""),
            "proposal": None,
        }

    def rin_proposal_attempt(request_id):
        """Return a plain pending/unresolved record suitable for game persistence."""
        request_id = str(request_id)
        retained = _RIN_UNRESOLVED_ATTEMPTS.get(request_id)
        if retained is not None:
            if retained["status"] != "unresolved":
                return None
            return json.loads(json.dumps(retained, ensure_ascii=False, separators=(",", ":")))
        if _RIN_REGISTRY is None:
            return None
        return _RIN_REGISTRY.attempt(request_id)

    def rin_resume_proposal(attempt):
        """Resume a game-persisted attempt with its exact request and known Job."""
        if not isinstance(attempt, dict) or not isinstance(attempt.get("request"), dict):
            raise rin_client.RinProtocolError("invalid_attempt", "Proposal attempt is invalid")
        return rin_schedule_proposal(
            attempt["request"],
            known_job_id=str(attempt.get("job_id", "")),
        )

    def rin_cancel_proposal(request_id):
        request_id = str(request_id)
        if request_id in _RIN_UNRESOLVED_ATTEMPTS:
            return False
        if _RIN_REGISTRY is None:
            return False
        return _RIN_REGISTRY.cancel(request_id)

    def rin_adapter_summary():
        config = _rin_config()
        return {
            "enabled": _rin_transport_enabled(),
            "base_url": config["base_url"],
            "token_configured": bool(config["token"]),
            "pending_results": len(_RIN_UNRESOLVED_ATTEMPTS),
        }

    def _rin_epoch_runtime():
        global _RIN_EPOCH_RUNTIME
        if _RIN_EPOCH_RUNTIME is None:
            _RIN_EPOCH_RUNTIME = rin_epoch.EpochRuntime(
                persistent.rin_host_epoch_ledger
            )
            persistent.rin_host_epoch_ledger = (
                _RIN_EPOCH_RUNTIME.persistent_ledger
            )
            renpy.save_persistent()
        return _RIN_EPOCH_RUNTIME

    def _rin_store_epoch(state):
        global rin_host_epoch_state
        runtime = _rin_epoch_runtime()
        rin_host_epoch_state = dict(state)
        persistent.rin_host_epoch_ledger = runtime.persistent_ledger
        renpy.save_persistent()
        return dict(rin_host_epoch_state)

    def _rin_invalidate_epoch_work():
        if _RIN_REGISTRY is not None:
            _RIN_REGISTRY.invalidate_all("stale_epoch")
        for attempt in _RIN_UNRESOLVED_ATTEMPTS.values():
            attempt["status"] = "stale"
            attempt["error_code"] = "stale_epoch"

    def rin_bind_host_epoch(session_id, world_id):
        """Bind stable game-supplied identity and return a plain Epoch dict."""
        runtime = _rin_epoch_runtime()
        state = runtime.bind(rin_host_epoch_state, session_id, world_id)
        return _rin_store_epoch(state)

    def rin_fork_host_timeline(reason="manual"):
        """Invalidate pending work and advance the non-rollback timeline."""
        if rin_host_epoch_state is None:
            raise rin_epoch.EpochStateError("Host Epoch is not bound")
        runtime = _rin_epoch_runtime()
        state = runtime.fork(rin_host_epoch_state, str(reason))
        _rin_invalidate_epoch_work()
        return _rin_store_epoch(state)

    def rin_current_host_epoch():
        if rin_host_epoch_state is None:
            return None
        return json.loads(json.dumps(
            rin_host_epoch_state,
            ensure_ascii=False,
            separators=(",", ":"),
        ))

    def _rin_epoch_after_load():
        if rin_host_epoch_state is None:
            return
        runtime = _rin_epoch_runtime()
        state = runtime.after_load(rin_host_epoch_state)
        _rin_invalidate_epoch_work()
        _rin_store_epoch(state)
        renpy.block_rollback()

    def _rin_epoch_interact():
        if rin_host_epoch_state is None:
            return
        runtime = _rin_epoch_runtime()
        state, changed = runtime.observe_rollback(
            rin_host_epoch_state,
            bool(renpy.in_rollback()),
        )
        if changed:
            _rin_invalidate_epoch_work()
            _rin_store_epoch(state)

    config.after_load_callbacks.append(_rin_epoch_after_load)
    config.interact_callbacks.append(_rin_epoch_interact)
