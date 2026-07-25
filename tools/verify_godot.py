#!/usr/bin/env python3
"""Parse and exercise the pinned Godot reference without editor UI."""

from __future__ import annotations

import argparse
import pathlib
import subprocess
import tempfile


SCRIPTS = (
    "res://rin_client.gd",
    "res://rin_workflow.gd",
    "res://example_npc.gd",
    "res://tests/test_workflow.gd",
)
ERROR_MARKERS = ("SCRIPT ERROR", "Failed to load script")


def run(command: list[str]) -> str:
    completed = subprocess.run(
        command,
        check=False,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
    )
    output = completed.stdout
    if completed.returncode != 0 or any(marker in output for marker in ERROR_MARKERS):
        raise SystemExit(
            f"Godot verification failed ({completed.returncode}):\n{output}"
        )
    return output


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--godot", required=True, type=pathlib.Path)
    parser.add_argument(
        "--project",
        type=pathlib.Path,
        default=pathlib.Path("examples/godot"),
    )
    args = parser.parse_args()
    godot = str(args.godot.resolve())
    project = str(args.project.resolve())
    version = run([godot, "--version"]).strip()
    if not version.startswith("4.7.1."):
        raise SystemExit(f"expected Godot 4.7.1, got {version!r}")
    with tempfile.TemporaryDirectory(prefix="rin-godot-") as temporary:
        log = str(pathlib.Path(temporary) / "godot.log")
        for script in SCRIPTS:
            run(
                [
                    godot,
                    "--headless",
                    "--path",
                    project,
                    "--check-only",
                    "--script",
                    script,
                    "--log-file",
                    log,
                ]
            )
        output = run(
            [
                godot,
                "--headless",
                "--path",
                project,
                "--script",
                "res://tests/test_workflow.gd",
                "--log-file",
                log,
            ]
        )
    if "Rin Godot workflow restart tests passed" not in output:
        raise SystemExit(f"Godot workflow test did not report success:\n{output}")
    print(f"Godot reference verified with {version}")


if __name__ == "__main__":
    main()
