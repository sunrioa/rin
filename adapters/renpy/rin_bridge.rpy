# Copy rin_client.py and this file into a Ren'Py game's game/ directory.
# The bridge keeps all worker objects process-local and returns plain JSON data.

init -30 python:
    import hashlib
    import json
    import os

    import rin_client

    _RIN_CLIENT = None
    _RIN_REGISTRY = None
    _RIN_CONFIG_FINGERPRINT = None
    _RIN_LOCAL_RESULTS = {}

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

    def _rin_store_local_result(request_id, request, result):
        if len(_RIN_LOCAL_RESULTS) >= 128:
            oldest = next(iter(_RIN_LOCAL_RESULTS))
            _RIN_LOCAL_RESULTS.pop(oldest, None)
        _RIN_LOCAL_RESULTS[str(request_id)] = {
            "request_fingerprint": _rin_request_fingerprint(request),
            "result": json.loads(json.dumps(
                result,
                ensure_ascii=False,
                separators=(",", ":"),
            )),
        }

    def rin_schedule_proposal(request, fallback_action_id=""):
        """Start one proposal without blocking the Ren'Py interaction thread."""
        request_id = str(request.get("request_id", ""))
        if not request_id:
            raise rin_client.RinProtocolError("invalid_request", "Proposal request needs request_id")
        if request_id in _RIN_LOCAL_RESULTS:
            if _RIN_LOCAL_RESULTS[request_id]["request_fingerprint"] != _rin_request_fingerprint(request):
                raise rin_client.RinProtocolError(
                    "request_id_conflict",
                    "Request id was already used with a different proposal payload",
                )
            return request_id
        client, registry, disabled_reason = _rin_runtime()
        if registry is None:
            _rin_store_local_result(
                request_id,
                request,
                rin_client.offline_proposal_result(
                    request,
                    fallback_action_id=fallback_action_id,
                    reason=disabled_reason or "disabled",
                ),
            )
            return request_id
        config = _rin_config()
        return registry.schedule(
            request,
            renpy.invoke_in_thread,
            fallback_action_id=fallback_action_id,
            deadline_seconds=config["deadline"],
            poll_interval=config["poll_interval"],
        )

    def rin_proposal_status(request_id):
        request_id = str(request_id)
        if request_id in _RIN_LOCAL_RESULTS:
            return "ready"
        if _RIN_REGISTRY is None:
            return "missing"
        status = _RIN_REGISTRY.status(request_id)
        if status in ("complete", "failed", "canceled"):
            return "ready"
        return status

    def rin_consume_proposal(request_id):
        """Return a plain adapter result once; return None while still pending."""
        request_id = str(request_id)
        local = _RIN_LOCAL_RESULTS.pop(request_id, None)
        if local is not None:
            return local["result"]
        if _RIN_REGISTRY is None:
            return None
        entry = _RIN_REGISTRY.consume(request_id)
        if entry is None:
            return None
        if entry["status"] == "complete":
            return entry["result"]
        return {
            "source": "canceled" if entry["status"] == "canceled" else "error",
            "committable": False,
            "fallback_reason": entry["error_code"],
            "job_id": "",
            "proposal": None,
        }

    def rin_cancel_proposal(request_id):
        request_id = str(request_id)
        if request_id in _RIN_LOCAL_RESULTS:
            _RIN_LOCAL_RESULTS.pop(request_id, None)
            return True
        if _RIN_REGISTRY is None:
            return False
        return _RIN_REGISTRY.cancel(request_id)

    def rin_adapter_summary():
        config = _rin_config()
        return {
            "enabled": _rin_transport_enabled(),
            "base_url": config["base_url"],
            "token_configured": bool(config["token"]),
            "pending_results": len(_RIN_LOCAL_RESULTS),
        }
