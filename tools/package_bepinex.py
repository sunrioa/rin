#!/usr/bin/env python3
"""Repository entry point for the canonical BepInEx project packager."""

from __future__ import annotations

import importlib.util
from pathlib import Path
import sys


ROOT = Path(__file__).resolve().parents[1]
IMPLEMENTATION = (
    ROOT / "examples" / "mods" / "bepinex-rin-npc" / "package_bepinex.py"
)
_SPEC = importlib.util.spec_from_file_location(
    "rin_bepinex_packaging",
    IMPLEMENTATION,
)
if _SPEC is None or _SPEC.loader is None:
    raise RuntimeError(f"cannot load BepInEx packaging helper {IMPLEMENTATION}")
_MODULE = importlib.util.module_from_spec(_SPEC)
sys.modules[_SPEC.name] = _MODULE
_SPEC.loader.exec_module(_MODULE)

PROJECT = _MODULE.PROJECT
REQUIRED_MONO_RUNTIME = _MODULE.REQUIRED_MONO_RUNTIME
MONO_THIRD_PARTY_SHA256 = _MODULE.MONO_THIRD_PARTY_SHA256
ZIP_TIME = _MODULE.ZIP_TIME
Variant = _MODULE.Variant
archive = _MODULE.archive
discover_variants = _MODULE.discover_variants
github_escape = _MODULE.github_escape
read_mono_third_party_assets = _MODULE.read_mono_third_party_assets
read_version = _MODULE.read_version
read_rin_license = _MODULE.read_rin_license
run = _MODULE.run
validate_mono_third_party_assets = _MODULE.validate_mono_third_party_assets
validate_portable_segment = _MODULE._validate_portable_segment
validate_publish_layout = _MODULE.validate_publish_layout
validate_restored_packages = _MODULE.validate_restored_packages
verify_archive = _MODULE.verify_archive


def main() -> None:
    _MODULE.main()


if __name__ == "__main__":
    main()
