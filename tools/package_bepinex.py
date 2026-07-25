#!/usr/bin/env python3
"""Build deterministic, install-ready BepInEx Mono and IL2CPP plugin ZIPs."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile
import zipfile
import xml.etree.ElementTree as ET


ROOT = Path(__file__).resolve().parents[1]
PROJECT = ROOT / "examples" / "mods" / "bepinex-rin-npc"
VARIANTS = {
    "mono": ("RinNpc.Mono/RinNpc.Mono.csproj", "RinNpc.Mono.dll"),
    "il2cpp": ("RinNpc.IL2CPP/RinNpc.IL2CPP.csproj", "RinNpc.IL2CPP.dll"),
}
REQUIRED_COMMON = {"Rin.Client.dll", "RinNpc.Core.dll"}
FORBIDDEN_PREFIXES = ("BepInEx.", "UnityEngine", "Il2Cpp")
ZIP_TIME = (1980, 1, 1, 0, 0, 0)


def run(command: list[str]) -> None:
    subprocess.run(command, cwd=PROJECT, check=True)


def archive(variant: str, published: Path, destination: Path) -> None:
    project, plugin = VARIANTS[variant]
    del project
    files = sorted(published.glob("*.dll"), key=lambda item: item.name.lower())
    names = {item.name for item in files}
    required = REQUIRED_COMMON | {plugin}
    if variant == "mono":
        required.add("System.Text.Json.dll")
    missing = sorted(required - names)
    forbidden = sorted(
        name for name in names if name.startswith(FORBIDDEN_PREFIXES)
    )
    if missing or forbidden:
        raise RuntimeError(
            f"{variant} publish layout is unsafe; missing={missing}, forbidden={forbidden}"
        )

    manifest = {
        "format_version": 1,
        "variant": variant,
        "install_root": "BepInEx/plugins/RinNpc",
        "files": [
            {
                "name": item.name,
                "sha256": hashlib.sha256(item.read_bytes()).hexdigest(),
            }
            for item in files
        ],
    }
    destination.parent.mkdir(parents=True, exist_ok=True)
    with zipfile.ZipFile(
        destination,
        "w",
        compression=zipfile.ZIP_DEFLATED,
        compresslevel=9,
    ) as bundle:
        prefix = "BepInEx/plugins/RinNpc/"
        for item in files:
            info = zipfile.ZipInfo(prefix + item.name, ZIP_TIME)
            info.compress_type = zipfile.ZIP_DEFLATED
            info.external_attr = 0o100644 << 16
            bundle.writestr(info, item.read_bytes())
        info = zipfile.ZipInfo(prefix + "manifest.json", ZIP_TIME)
        info.compress_type = zipfile.ZIP_DEFLATED
        info.external_attr = 0o100644 << 16
        bundle.writestr(
            info,
            (json.dumps(manifest, indent=2, sort_keys=True) + "\n").encode("utf-8"),
        )


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--dotnet", default=os.environ.get("DOTNET", "dotnet"))
    parser.add_argument("--output", type=Path, default=PROJECT / "dist")
    args = parser.parse_args()
    version = ET.parse(PROJECT / "Directory.Build.props").getroot().findtext(
        "./PropertyGroup/Version"
    )
    if not version:
        raise RuntimeError("Directory.Build.props does not declare Version")

    for project, _ in VARIANTS.values():
        run(
            [
                args.dotnet,
                "restore",
                project,
                "--locked-mode",
                "--nologo",
                "-p:RestoreDisableParallel=true",
                "-m:1",
            ]
        )

    with tempfile.TemporaryDirectory(prefix="rin-bepinex-package-") as temporary:
        stage = Path(temporary)
        for variant, (project, _) in VARIANTS.items():
            published = stage / variant
            run(
                [
                    args.dotnet,
                    "publish",
                    project,
                    "-c",
                    "Release",
                    "--no-restore",
                    "--nologo",
                    "-m:1",
                    "-p:UseSharedCompilation=false",
                    "-p:BuildInParallel=false",
                    "-o",
                    str(published),
                ]
            )
            archive(
                variant,
                published,
                args.output / f"rin-npc-bepinex-{variant}-{version}.zip",
            )


if __name__ == "__main__":
    main()
