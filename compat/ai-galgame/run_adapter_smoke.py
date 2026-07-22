#!/usr/bin/env python3
"""Drive compatibility vectors through the real Ren'Py Python adapter."""

import argparse
import json
import os
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "adapters" / "renpy"))

import rin_client  # noqa: E402


def _read_vectors(path):
    payload = Path(path).read_bytes()
    if len(payload) > 4 * 1024 * 1024:
        raise ValueError("compatibility vectors are too large")
    value = json.loads(payload.decode("utf-8"))
    if not isinstance(value, dict) or not isinstance(value.get("cases"), list):
        raise ValueError("compatibility vectors are invalid")
    return value


def _assert_equal(actual, expected, label):
    if actual != expected:
        raise RuntimeError("{} mismatch".format(label))


def run(base_url, vectors_path):
    vectors = _read_vectors(vectors_path)
    client = rin_client.RinClient(
        base_url,
        token=os.environ.get("RIN_TOKEN", ""),
        timeout=5,
    )
    health = client.health()
    if not health.get("async_jobs"):
        raise RuntimeError("Rin Sidecar does not expose async jobs")
    summaries = []
    for test_case in vectors["cases"]:
        created = client.create_session(test_case["create"])
        last_proposal_id = ""
        operations = []
        for step in test_case["steps"]:
            operation = step["operation"]
            if operation == "observe":
                client.observe(step["observe"])
            elif operation == "propose":
                if step.get("expect_error_code"):
                    try:
                        client.propose(step["propose"])
                    except rin_client.RinAPIError as exc:
                        _assert_equal(exc.code, step["expect_error_code"], "proposal error")
                    else:
                        raise RuntimeError("expected proposal error was not returned")
                else:
                    result = client.propose_with_fallback(
                        step["propose"],
                        deadline_seconds=5,
                        poll_interval=0.02,
                    )
                    if not result["committable"]:
                        raise RuntimeError("adapter unexpectedly used offline fallback")
                    proposal = result["proposal"]
                    _assert_equal(proposal["action"]["id"], step["expect_action_id"], "proposal action")
                    _assert_equal(proposal["stance"], step["expect_stance"], "proposal stance")
                    last_proposal_id = proposal["id"]
            elif operation == "commit":
                request = dict(step["commit"])
                if request.get("proposal_id") == "$last":
                    request["proposal_id"] = last_proposal_id
                client.commit(request)
            elif operation == "due":
                result = client.due_agents(step["due"])
                ids = [item["actor_id"] for item in result.get("agents", [])]
                _assert_equal(ids, step.get("expect_due_actor_ids", []), "due actors")
            elif operation == "assert_state":
                state = client.state({
                    "protocol_version": rin_client.PROTOCOL_VERSION,
                    "session_id": test_case["create"]["session_id"],
                })
                actors = state.get("actors", {})
                for actor_id, expected in step.get("expect_actor_memory_counts", {}).items():
                    _assert_equal(len(actors[actor_id].get("memories", [])), expected, "memory count")
            else:
                raise RuntimeError("unsupported compatibility operation")
            operations.append(operation)
        summaries.append({
            "case": test_case["name"],
            "operations": operations,
            "revision": client.state({
                "protocol_version": rin_client.PROTOCOL_VERSION,
                "session_id": test_case["create"]["session_id"],
            })["revision"],
            "created_duplicate": bool(created.get("duplicate")),
        })
    return {
        "protocol_version": rin_client.PROTOCOL_VERSION,
        "policy_mode": health.get("policy_mode", ""),
        "cases": summaries,
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--base-url", default=rin_client.DEFAULT_BASE_URL)
    parser.add_argument(
        "--vectors",
        default=str(Path(__file__).with_name("vectors.json")),
    )
    args = parser.parse_args()
    print(json.dumps(run(args.base_url, args.vectors), sort_keys=True, separators=(",", ":")))


if __name__ == "__main__":
    main()
