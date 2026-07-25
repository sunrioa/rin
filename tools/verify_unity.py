#!/usr/bin/env python3
"""Compile and execute the Unity package harness without claiming an Editor run."""

from __future__ import annotations

import argparse
import pathlib
import subprocess


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--dotnet", default="dotnet", type=pathlib.Path)
    args = parser.parse_args()
    root = pathlib.Path(__file__).resolve().parents[1]
    project = root / "tools" / "unity-harness" / "UnityHarness.csproj"
    command = [
        str(args.dotnet),
        "run",
        "--project",
        str(project),
        "--nologo",
    ]
    result = subprocess.run(command, cwd=root, text=True, capture_output=True)
    output = result.stdout + result.stderr
    if result.returncode or "Rin Unity workflow restart tests passed" not in output:
        raise SystemExit(f"Unity package verification failed:\n{output}")
    print("Unity package compiler and restart harness passed")


if __name__ == "__main__":
    main()
