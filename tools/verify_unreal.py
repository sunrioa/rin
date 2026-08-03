#!/usr/bin/env python3
"""Static, dependency-free checks for the Unreal Runtime Plugin skeleton."""

from __future__ import annotations

import json
import re
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
PLUGIN = ROOT / "examples" / "unreal" / "RinHost"

REQUIRED = {
    "RinHost.uplugin": ('"Type": "Runtime"', '"LoadingPhase": "Default"'),
    "Source/RinHost/RinHost.Build.cs": (
        '"Core"',
        '"CoreUObject"',
        '"Engine"',
        '"AIModule"',
    ),
    "Source/RinHost/Public/RinHostSubsystem.h": (
        "UGameInstanceSubsystem",
        "ConfigureHostIdentity",
        "BindWorldIdentity",
        "RegisterCapability",
        "ReplaceActionOffers",
        "ObserveAuthoritativeClock",
        "AuthorizeAndQueueInvocation",
        "DispatchToGameThread",
    ),
    "Source/RinHost/Private/RinHostSubsystem.cpp": (
        "OnPostWorldInitialization",
        "MarkActiveRunsOutcomeUnknown",
        "IsInGameThread",
        "ENamedThreads::GameThread",
        "CanTransition",
        "Epoch.TimelineGeneration >= FRinHostEpoch::MaxJsonSafeInteger",
        "IsSafePositiveInteger(ProgressSequence)",
        "OfferMatchesInvocation",
        "MarkQueuedRunsStaleForCapability",
        "MarkExpiredQueuedRuns",
        "IsQueuedRunAuthorized",
    ),
    "Source/RinHost/Private/RinHostTypes.cpp": (
        "IsSafeIdentifier",
        "IsSafePositiveInteger",
        "IsExactVersion",
        "IsLowerHexDigest",
        "FRinActionOffer::IsValid",
        "OfferDigest",
    ),
    "Source/RinHost/Public/BTTask_RinHostMoveTo.h": (
        "UBTTask_MoveTo",
        "OperationIdKey",
    ),
    "Source/RinHost/Private/BTTask_RinHostMoveTo.cpp": (
        "ERinActionRunStatus::Running",
        "ERinActionRunStatus::Succeeded",
        "ERinActionRunStatus::Cancelled",
        "ActiveEpoch",
    ),
}

FORBIDDEN = (
    "TODO",
    "FIXME",
    "CreateProc",
    "ExecProcess",
    "IConsoleManager",
    "ProcessEvent(",
    "StaticLoadObject",
    "FGuid::NewGuid",
    "system(",
    "popen(",
)

WINDOWS_RESERVED = re.compile(
    r"^(con|prn|aux|nul|com[1-9]|lpt[1-9])(?:\..*)?$",
    re.IGNORECASE,
)


def function_body(source: str, name: str) -> str:
    start = source.find(f"URinHostSubsystem::{name}")
    if start < 0:
        raise SystemExit(f"RinHostSubsystem.cpp is missing {name}")
    opening = source.find("{", start)
    if opening < 0:
        raise SystemExit(f"RinHostSubsystem.cpp has no body for {name}")
    depth = 0
    for index in range(opening, len(source)):
        if source[index] == "{":
            depth += 1
        elif source[index] == "}":
            depth -= 1
            if depth == 0:
                return source[opening : index + 1]
    raise SystemExit(f"RinHostSubsystem.cpp has an incomplete body for {name}")


def verify_relative_paths(relatives: list[str]) -> None:
    seen_casefold: dict[str, str] = {}
    for relative in relatives:
        for segment in Path(relative).parts:
            if WINDOWS_RESERVED.match(segment) or segment.endswith((" ", ".")):
                raise SystemExit(f"Windows-incompatible path: {relative}")
            if any(value in segment for value in '<>:"\\|?*'):
                raise SystemExit(f"Windows-incompatible path: {relative}")
        folded = relative.casefold()
        if folded in seen_casefold:
            raise SystemExit(
                f"Windows path collision: {seen_casefold[folded]} and {relative}"
            )
        seen_casefold[folded] = relative


def verify_plugin(plugin: Path) -> None:
    manifest = json.loads((plugin / "RinHost.uplugin").read_text("utf-8"))
    if (
        manifest.get("FileVersion") != 3
        or manifest.get("CanContainContent") is not False
    ):
        raise SystemExit("invalid RinHost.uplugin")
    modules = manifest.get("Modules")
    if not isinstance(modules, list) or len(modules) != 1:
        raise SystemExit("RinHost.uplugin must declare exactly one module")

    files = [path for path in sorted(plugin.rglob("*")) if path.is_file()]
    relatives = [path.relative_to(plugin).as_posix() for path in files]
    verify_relative_paths(relatives)
    for path, relative in zip(files, relatives):
        source = path.read_text("utf-8")
        for forbidden in FORBIDDEN:
            if forbidden in source:
                raise SystemExit(f"{relative} contains forbidden token {forbidden!r}")

    for relative, fragments in REQUIRED.items():
        source = (plugin / relative).read_text("utf-8")
        for fragment in fragments:
            if fragment not in source:
                raise SystemExit(f"{relative} is missing {fragment!r}")
    subsystem = (
        plugin / "Source/RinHost/Private/RinHostSubsystem.cpp"
    ).read_text("utf-8")
    guarded_functions = {
        "AuthorizeAndQueueInvocation": (
            "ActionOffers.Find",
            "OfferMatchesInvocation",
            "AuthoritativeClocks.Find",
            "ActionOffers.Remove",
            "Descriptor->bActive",
        ),
        "RevokeCapability": (
            "Descriptor->bActive = false",
            "ActionOffers.Remove",
            "MarkQueuedRunsStaleForCapability",
        ),
        "ReportRun": (
            "IsQueuedRunAuthorized",
            "ERinActionRunStatus::Stale",
        ),
        "ObserveAuthoritativeClock": (
            "MarkExpiredQueuedRuns",
        ),
    }
    for function, fragments in guarded_functions.items():
        body = function_body(subsystem, function)
        for fragment in fragments:
            if fragment not in body:
                raise SystemExit(
                    f"{function} is missing security guard {fragment!r}"
                )
    types_header = (
        plugin / "Source/RinHost/Public/RinHostTypes.h"
    ).read_text("utf-8")
    identifier_limit = re.search(
        r"MaxIdentifierLength\s*=\s*(\d+)",
        types_header,
    )
    json_limit = re.search(
        r"MaxJsonSafeInteger\s*=\s*(\d+)LL",
        types_header,
    )
    if identifier_limit is None or int(identifier_limit.group(1)) != 96:
        raise SystemExit("Unreal identifier limit must match protocol value 96")
    if json_limit is None or int(json_limit.group(1)) != 9007199254740991:
        raise SystemExit("Unreal JSON integer limit does not match the protocol")
    print(f"Unreal Host skeleton verified: {len(files)} files")


def main() -> None:
    verify_plugin(PLUGIN)


if __name__ == "__main__":
    main()
