#!/usr/bin/env python3
"""Run the shared wire and five-SDK corpus against a real Rin Sidecar."""

from __future__ import annotations

import argparse
import copy
import http.server
import json
import os
import pathlib
import re
import signal
import socket
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.request
from collections.abc import Iterable
from typing import Any, Optional


ROOT = pathlib.Path(__file__).resolve().parents[1]
CORPUS_PATH = ROOT / "sdk" / "conformance" / "sidecar-corpus.json"
TOKEN = "sdk-corpus-token"
LANGUAGES = ("python", "javascript", "csharp", "java", "lua")


def load_corpus(path: pathlib.Path = CORPUS_PATH) -> dict[str, Any]:
    value = json.loads(path.read_text(encoding="utf-8"))
    if (
        value.get("version") != 1
        or not isinstance(value.get("create_session"), dict)
        or not isinstance(value.get("expectations"), dict)
        or not isinstance(value.get("request_body_limit"), int)
        or value["request_body_limit"] < 512
        or not isinstance(value.get("slow_response_ms"), int)
        or value["slow_response_ms"] < 100
    ):
        raise ValueError("sidecar corpus has an invalid root shape")
    expected_shapes = {
        "first_mutation": ("status", "duplicate"),
        "exact_retry": ("status", "duplicate"),
        "altered_retry": ("status", "code"),
        "unknown_field": ("status", "code"),
        "duplicate_member": ("status", "code"),
        "body_limit": ("status", "code"),
        "timeout": ("code",),
    }
    expectations = value["expectations"]
    for name, fields in expected_shapes.items():
        expectation = expectations.get(name)
        if not isinstance(expectation, dict) or any(
            field not in expectation for field in fields
        ):
            raise ValueError(f"sidecar corpus expectation {name!r} is invalid")
    return value


def materialize_request(corpus: dict[str, Any], client: str) -> dict[str, Any]:
    if re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9-]*", client) is None:
        raise ValueError("corpus client name must contain only letters, digits, or hyphens")

    def replace(value: Any) -> Any:
        if isinstance(value, str):
            return value.replace("__client__", client)
        if isinstance(value, list):
            return [replace(item) for item in value]
        if isinstance(value, dict):
            return {key: replace(item) for key, item in value.items()}
        return value

    request = replace(copy.deepcopy(corpus["create_session"]))
    encoded = compact_json(request)
    if (
        "__client__" in encoded
        or len(encoded.encode()) >= corpus["request_body_limit"]
    ):
        raise ValueError("materialized corpus request is invalid or exceeds its Sidecar limit")
    return request


def compact_json(value: Any) -> str:
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"), allow_nan=False)


def free_tcp_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as listener:
        listener.bind(("127.0.0.1", 0))
        return int(listener.getsockname()[1])


def request(
    base_url: str,
    method: str,
    path: str,
    *,
    body: bytes | None = None,
    timeout: float = 5,
) -> tuple[int, dict[str, Any]]:
    headers = {
        "Accept": "application/json",
        "Authorization": "Bearer " + TOKEN,
    }
    if body is not None:
        headers["Content-Type"] = "application/json; charset=utf-8"
    target = urllib.request.Request(
        base_url + path,
        data=body,
        headers=headers,
        method=method,
    )
    try:
        response = urllib.request.urlopen(target, timeout=timeout)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        payload = response.read()
        status = int(response.status)
    try:
        envelope = json.loads(payload)
    except json.JSONDecodeError as error:
        raise AssertionError(
            f"{method} {path} returned non-JSON status {status}: {payload[:200]!r}"
        ) from error
    if not isinstance(envelope, dict):
        raise AssertionError(f"{method} {path} returned a non-object envelope")
    return status, envelope


def expect_error(
    result: tuple[int, dict[str, Any]],
    expectation: dict[str, Any],
    case: str,
) -> None:
    status, envelope = result
    detail = envelope.get("error")
    if (
        status != expectation["status"]
        or envelope.get("ok") is not False
        or not isinstance(detail, dict)
        or detail.get("code") != expectation["code"]
    ):
        raise AssertionError(
            f"{case} returned {status} {envelope!r}, expected "
            f"{expectation['status']} {expectation['code']}"
        )


def run_wire_cases(base_url: str, corpus: dict[str, Any]) -> None:
    expectations = corpus["expectations"]
    payload = materialize_request(corpus, "wire")
    body = compact_json(payload).encode()
    first_status, first = request(base_url, "POST", "/v2/session/create", body=body)
    retry_status, retry = request(base_url, "POST", "/v2/session/create", body=body)
    first_data = first.get("data")
    retry_data = retry.get("data")
    if (
        first_status != expectations["first_mutation"]["status"]
        or retry_status != expectations["exact_retry"]["status"]
        or not isinstance(first_data, dict)
        or not isinstance(retry_data, dict)
        or first_data.get("duplicate") is not False
        or retry_data.get("duplicate") is not True
        or retry_data.get("revision") != first_data.get("revision")
        or retry_data.get("head_hash") != first_data.get("head_hash")
    ):
        raise AssertionError(f"wire exact retry changed: first={first!r}, retry={retry!r}")

    altered = copy.deepcopy(payload)
    altered["actors"][0]["display_name"] = "Altered Corpus NPC"
    expect_error(
        request(
            base_url,
            "POST",
            "/v2/session/create",
            body=compact_json(altered).encode(),
        ),
        expectations["altered_retry"],
        "altered retry",
    )

    unknown = copy.deepcopy(payload)
    unknown["unexpected"] = True
    expect_error(
        request(
            base_url,
            "POST",
            "/v2/session/create",
            body=compact_json(unknown).encode(),
        ),
        expectations["unknown_field"],
        "unknown field",
    )

    duplicate = body.replace(
        b'"session_id":"session.wire",',
        b'"session_id":"session.first","session_id":"session.wire",',
        1,
    )
    if duplicate == body:
        raise AssertionError("duplicate-member corpus did not alter the request")
    expect_error(
        request(base_url, "POST", "/v2/session/create", body=duplicate),
        expectations["duplicate_member"],
        "duplicate member",
    )

    oversized = compact_json(
        {"padding": "x" * (corpus["request_body_limit"] + 1)}
    ).encode()
    expect_error(
        request(base_url, "POST", "/v2/session/create", body=oversized),
        expectations["body_limit"],
        "body limit",
    )
    print("Sidecar wire corpus passed")


class _SlowHandler(http.server.BaseHTTPRequestHandler):
    delay_seconds = 0.4

    def do_GET(self) -> None:  # noqa: N802
        self._respond()

    def do_POST(self) -> None:  # noqa: N802
        self._respond()

    def _respond(self) -> None:
        time.sleep(self.delay_seconds)
        body = b'{"ok":true,"data":{}}'
        try:
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
        except (BrokenPipeError, ConnectionResetError):
            pass

    def log_message(self, _format: str, *_args: object) -> None:
        pass


class _SlowServer(http.server.ThreadingHTTPServer):
    daemon_threads = True


def clean_sidecar_environment() -> dict[str, str]:
    environment = {
        key: value
        for key, value in os.environ.items()
        if not key.startswith("RIN_")
    }
    environment["RIN_TOKEN"] = TOKEN
    return environment


def wait_ready(base_url: str, process: subprocess.Popen[bytes]) -> None:
    deadline = time.monotonic() + 20
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise RuntimeError(f"Rin Sidecar exited before readiness ({process.returncode})")
        try:
            status, envelope = request(base_url, "GET", "/health", timeout=0.25)
            if status == 200 and envelope.get("ok") is True:
                return
        except (AssertionError, OSError, TimeoutError):
            pass
        time.sleep(0.05)
    raise TimeoutError("Rin Sidecar did not become ready within 20 seconds")


def stop_process(process: subprocess.Popen[bytes]) -> None:
    if process.poll() is not None:
        return
    try:
        if os.name == "nt":
            process.send_signal(signal.CTRL_BREAK_EVENT)
        else:
            process.send_signal(signal.SIGINT)
        process.wait(timeout=10)
        return
    except (OSError, subprocess.TimeoutExpired):
        process.terminate()
    try:
        process.wait(timeout=5)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait(timeout=5)


def runner_environment(
    corpus: dict[str, Any],
    base_url: str,
    slow_url: str,
    language: str,
) -> dict[str, str]:
    environment = os.environ.copy()
    environment.update(
        {
            "RIN_SDK_CORPUS_BASE_URL": base_url,
            "RIN_SDK_CORPUS_SLOW_URL": slow_url,
            "RIN_SDK_CORPUS_TOKEN": TOKEN,
            "RIN_SDK_CORPUS_CLIENT": language,
            "RIN_SDK_CORPUS_BODY": compact_json(
                materialize_request(corpus, language)
            ),
        }
    )
    return environment


def run_checked(command: Iterable[str], environment: dict[str, str]) -> None:
    completed = subprocess.run(
        list(command),
        cwd=ROOT,
        env=environment,
        check=False,
        timeout=180,
    )
    if completed.returncode != 0:
        raise RuntimeError(
            f"SDK corpus command failed with {completed.returncode}: "
            + " ".join(command)
        )


def run_sdk(
    language: str,
    args: argparse.Namespace,
    corpus: dict[str, Any],
    base_url: str,
    slow_url: str,
    java_output: pathlib.Path,
) -> None:
    environment = runner_environment(corpus, base_url, slow_url, language)
    runner_root = ROOT / "sdk" / "conformance" / "runners"
    if language == "python":
        command = [args.python, str(runner_root / "python.py")]
    elif language == "javascript":
        command = [args.node, str(runner_root / "javascript.mjs")]
    elif language == "csharp":
        command = [
            args.dotnet,
            "run",
            "--project",
            str(runner_root / "csharp" / "Rin.Corpus.csproj"),
            "--nologo",
        ]
    elif language == "java":
        sources = sorted((ROOT / "sdk" / "java" / "src" / "main" / "java").rglob("*.java"))
        sources.append(
            runner_root
            / "java"
            / "io"
            / "github"
            / "sunrioa"
            / "rin"
            / "SidecarCorpus.java"
        )
        run_checked(
            [args.javac, "-d", str(java_output), *(str(path) for path in sources)],
            environment,
        )
        command = [
            args.java,
            "-cp",
            str(java_output),
            "io.github.sunrioa.rin.SidecarCorpus",
        ]
    elif language == "lua":
        command = [args.lua, str(runner_root / "lua.lua")]
    else:
        raise ValueError(f"unsupported SDK language {language!r}")
    run_checked(command, environment)


def parse_languages(value: str) -> tuple[str, ...]:
    result = tuple(item.strip() for item in value.split(",") if item.strip())
    unknown = sorted(set(result) - set(LANGUAGES))
    if not result or unknown:
        raise argparse.ArgumentTypeError(
            "languages must be a non-empty comma-separated subset of "
            + ",".join(LANGUAGES)
        )
    return result


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--rin", required=True, type=pathlib.Path)
    parser.add_argument("--languages", type=parse_languages, default=LANGUAGES)
    parser.add_argument("--python", default=sys.executable)
    parser.add_argument("--node", default="node")
    parser.add_argument("--dotnet", default="dotnet")
    parser.add_argument("--javac", default="javac")
    parser.add_argument("--java", default="java")
    parser.add_argument("--lua", default="lua")
    args = parser.parse_args()
    corpus = load_corpus()
    rin = args.rin.resolve()
    if not rin.is_file():
        raise SystemExit(f"Rin executable does not exist: {rin}")

    sidecar_port = free_tcp_port()
    slow_port = free_tcp_port()
    base_url = f"http://127.0.0.1:{sidecar_port}"
    slow_url = f"http://127.0.0.1:{slow_port}"
    _SlowHandler.delay_seconds = corpus["slow_response_ms"] / 1000
    slow_server = _SlowServer(("127.0.0.1", slow_port), _SlowHandler)
    slow_thread = threading.Thread(target=slow_server.serve_forever, daemon=True)
    slow_thread.start()

    with tempfile.TemporaryDirectory(prefix="rin-sdk-corpus-") as temporary:
        root = pathlib.Path(temporary)
        output = (root / "sidecar.log").open("wb")
        creation_flags = (
            subprocess.CREATE_NEW_PROCESS_GROUP if os.name == "nt" else 0
        )
        process = subprocess.Popen(
            [
                str(rin),
                "serve",
                "-addr",
                f"127.0.0.1:{sidecar_port}",
                "-data",
                str(root / "data"),
                "-max-body-bytes",
                str(corpus["request_body_limit"]),
                "-scrub-interval",
                "1h",
            ],
            cwd=ROOT,
            env=clean_sidecar_environment(),
            stdout=output,
            stderr=subprocess.STDOUT,
            creationflags=creation_flags,
        )
        failure: Optional[BaseException] = None
        try:
            wait_ready(base_url, process)
            run_wire_cases(base_url, corpus)
            java_output = root / "java"
            java_output.mkdir()
            for language in args.languages:
                run_sdk(language, args, corpus, base_url, slow_url, java_output)
        except BaseException as error:
            failure = error
        finally:
            stop_process(process)
            output.close()
            slow_server.shutdown()
            slow_server.server_close()
            slow_thread.join(timeout=5)
        log = (root / "sidecar.log").read_text(encoding="utf-8", errors="replace")
        if failure is not None:
            raise RuntimeError(
                f"SDK corpus failed: {failure}\nSidecar log:\n{log[-4000:]}"
            ) from failure
        if process.returncode != 0:
            raise RuntimeError(
                f"Rin Sidecar exited with {process.returncode}:\n{log[-4000:]}"
            )
    print("Shared live Sidecar corpus passed: " + ", ".join(args.languages))


if __name__ == "__main__":
    main()
