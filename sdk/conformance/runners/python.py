#!/usr/bin/env python3
"""Run the Python SDK against the shared live Sidecar corpus."""

from __future__ import annotations

import json
import os
import pathlib
import sys


ROOT = pathlib.Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT / "sdk" / "python" / "src"))

from rin_sdk import RinClient, RinTransportError  # noqa: E402


def main() -> None:
    body = json.loads(os.environ["RIN_SDK_CORPUS_BODY"])
    client = RinClient(
        os.environ["RIN_SDK_CORPUS_BASE_URL"],
        token=os.environ["RIN_SDK_CORPUS_TOKEN"],
    )
    if client.health().get("protocol_version") != "rin.protocol/v2":
        raise AssertionError("Python SDK received an invalid health response")
    first = client.create_session(body)
    retry = client.create_session(body)
    assert first["duplicate"] is False
    assert retry["duplicate"] is True
    assert retry["revision"] == first["revision"]
    assert retry["head_hash"] == first["head_hash"]

    slow = RinClient(
        os.environ["RIN_SDK_CORPUS_SLOW_URL"],
        token=os.environ["RIN_SDK_CORPUS_TOKEN"],
        timeout=0.05,
    )
    try:
        slow.create_session(body)
    except RinTransportError as error:
        assert error.code == "transport_timeout"
    else:
        raise AssertionError("Python SDK did not enforce its network timeout")

    print("Python SDK live Sidecar corpus passed")


if __name__ == "__main__":
    main()
