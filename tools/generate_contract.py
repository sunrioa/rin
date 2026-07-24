#!/usr/bin/env python3
"""Generate and verify Rin's contract-derived release projections.

The authoritative inputs live in ``api/openapi.json``. Generated and
source-first projections remain committed so every SDK can be vendored without
running this script. Use ``--write`` after changing the contract and ``--check``
in CI.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Dict, Iterable, List, Mapping, MutableMapping, Optional, Tuple


ROOT = Path(__file__).resolve().parents[1]
CONTRACT_PATH = ROOT / "api" / "openapi.json"
ROUTES_PATH = ROOT / "sdk" / "conformance" / "routes.json"
HTTP_METHODS = ("get", "post", "put", "patch", "delete", "head", "options", "trace")
SEMVER = re.compile(r"^[0-9]+\.[0-9]+\.[0-9]+$")
OPERATION_ID = re.compile(r"^[a-z][a-z0-9]*(?:_[a-z0-9]+)*$")


class ContractError(RuntimeError):
    """Raised when the authority document or a projection is invalid."""


@dataclass(frozen=True)
class Contract:
    release_version: str
    protocol_version: str
    release_status: str
    luanti_release: int
    max_json_safe_integer: int
    error_code_max_length: int
    error_message_max_length: int
    error_field_max_length: int
    document: Mapping[str, object]


@dataclass(frozen=True)
class Projection:
    path: str
    pattern: str
    replacement: str


@dataclass(frozen=True)
class Operation:
    operation_id: str
    method: str
    path: str
    success_status: int


def load_contract() -> Contract:
    try:
        document = json.loads(CONTRACT_PATH.read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise ContractError(f"missing authority document: {relative(CONTRACT_PATH)}") from exc
    except json.JSONDecodeError as exc:
        raise ContractError(f"{relative(CONTRACT_PATH)} is not valid JSON: {exc}") from exc
    if not isinstance(document, dict):
        raise ContractError(f"{relative(CONTRACT_PATH)} must contain one JSON object")
    openapi = document.get("openapi")
    if not isinstance(openapi, str) or not openapi.startswith("3.1."):
        raise ContractError("openapi must identify an OpenAPI 3.1 document")
    info = document.get("info")
    if not isinstance(info, dict):
        raise ContractError("info must be an object")
    release_version = info.get("version")
    if not isinstance(release_version, str) or not SEMVER.fullmatch(release_version):
        raise ContractError("info.version must be a plain MAJOR.MINOR.PATCH version")
    protocol_version = document.get("x-rin-protocol-version")
    if not isinstance(protocol_version, str) or not protocol_version:
        raise ContractError("x-rin-protocol-version must be a non-empty string")
    release_status = document.get("x-rin-release-status")
    if release_status not in ("preview", "stable", "deprecated"):
        raise ContractError("x-rin-release-status must be preview, stable, or deprecated")
    luanti_release = document.get("x-rin-luanti-release")
    if (
        not isinstance(luanti_release, int)
        or isinstance(luanti_release, bool)
        or luanti_release <= 0
    ):
        raise ContractError("x-rin-luanti-release must be a positive integer")
    if not isinstance(document.get("paths"), dict):
        raise ContractError("paths must be an object")
    try:
        schemas = document["components"]["schemas"]
        schema_protocol_version = schemas["ProtocolVersion"]["const"]
        health_properties = schemas["HealthData"]["properties"]
        health_release_version = health_properties["release_version"]["const"]
        health_release_status = health_properties["release_status"]["const"]
        signed_minimum = schemas["JSONSafeSignedInteger"]["minimum"]
        signed_maximum = schemas["JSONSafeSignedInteger"]["maximum"]
        max_json_safe_integer = schemas["JSONSafeUnsignedInteger"]["maximum"]
        error_properties = schemas["ErrorDetail"]["properties"]
        error_code_max_length = error_properties["code"]["maxLength"]
        error_message_max_length = error_properties["message"]["maxLength"]
        error_field_max_length = error_properties["field"]["maxLength"]
    except (KeyError, TypeError) as exc:
        raise ContractError(
            "ProtocolVersion, HealthData release identity, JSON-safe integer, "
            "and ErrorDetail schemas are required"
        ) from exc
    if schema_protocol_version != protocol_version:
        raise ContractError(
            "components.schemas.ProtocolVersion.const must equal x-rin-protocol-version"
        )
    if health_release_version != release_version:
        raise ContractError(
            "components.schemas.HealthData.properties.release_version.const "
            "must equal info.version"
        )
    if health_release_status != release_status:
        raise ContractError(
            "components.schemas.HealthData.properties.release_status.const "
            "must equal x-rin-release-status"
        )
    if (
        not isinstance(max_json_safe_integer, int)
        or isinstance(max_json_safe_integer, bool)
        or max_json_safe_integer <= 0
    ):
        raise ContractError(
            "components.schemas.JSONSafeUnsignedInteger.maximum must be a positive integer"
        )
    if max_json_safe_integer != (1 << 53) - 1:
        raise ContractError(
            "components.schemas.JSONSafeUnsignedInteger.maximum must equal "
            "the interoperable JSON integer ceiling 2^53-1"
        )
    if (
        signed_maximum != max_json_safe_integer
        or signed_minimum != -max_json_safe_integer
    ):
        raise ContractError(
            "signed and unsigned JSON-safe integer schemas must share one symmetric bound"
        )
    for field_name, maximum in (
        ("code", error_code_max_length),
        ("message", error_message_max_length),
        ("field", error_field_max_length),
    ):
        if (
            not isinstance(maximum, int)
            or isinstance(maximum, bool)
            or maximum <= 0
        ):
            raise ContractError(
                "components.schemas.ErrorDetail.properties."
                f"{field_name}.maxLength must be a positive integer"
            )
    return Contract(
        release_version=release_version,
        protocol_version=protocol_version,
        release_status=release_status,
        luanti_release=luanti_release,
        max_json_safe_integer=max_json_safe_integer,
        error_code_max_length=error_code_max_length,
        error_message_max_length=error_message_max_length,
        error_field_max_length=error_field_max_length,
        document=document,
    )


def contract_operations(contract: Contract) -> List[Operation]:
    operations: List[Operation] = []
    paths = contract.document["paths"]
    assert isinstance(paths, dict)
    seen_ids = set()
    seen_routes = set()
    for path, path_item in paths.items():
        if not isinstance(path, str) or not path.startswith("/"):
            raise ContractError(f"invalid OpenAPI path {path!r}")
        if not isinstance(path_item, dict):
            raise ContractError(f"OpenAPI path {path!r} must be an object")
        for method, operation in path_item.items():
            normalized_method = method.lower()
            if normalized_method not in HTTP_METHODS:
                continue
            if not isinstance(operation, dict):
                raise ContractError(f"{method.upper()} {path} must be an object")
            operation_id = operation.get("operationId")
            if not isinstance(operation_id, str) or not operation_id:
                raise ContractError(f"{method.upper()} {path} is missing operationId")
            if not OPERATION_ID.fullmatch(operation_id):
                raise ContractError(
                    f"operationId {operation_id!r} must use lower_snake_case"
                )
            if operation_id in seen_ids:
                raise ContractError(f"duplicate operationId {operation_id!r}")
            route_key = (normalized_method.upper(), path)
            if route_key in seen_routes:
                raise ContractError(f"duplicate route {route_key[0]} {route_key[1]}")
            responses = operation.get("responses")
            if not isinstance(responses, dict):
                raise ContractError(f"{route_key[0]} {path} is missing responses")
            success_statuses = sorted(
                int(status)
                for status in responses
                if isinstance(status, str)
                and len(status) == 3
                and status.isdigit()
                and status.startswith("2")
            )
            if len(success_statuses) != 1:
                raise ContractError(
                    f"{route_key[0]} {path} must declare exactly one concrete 2xx response"
                )
            seen_ids.add(operation_id)
            seen_routes.add(route_key)
            operations.append(
                Operation(
                    operation_id=operation_id,
                    method=route_key[0],
                    path=path,
                    success_status=success_statuses[0],
                )
            )
    if not operations:
        raise ContractError("OpenAPI paths contain no HTTP operations")
    return operations


def render_routes(contract: Contract, operations: Iterable[Operation]) -> str:
    manifest = {
        "schema_version": 1,
        "release_version": contract.release_version,
        "release_status": contract.release_status,
        "protocol_version": contract.protocol_version,
        "operations": [
            {
                "name": operation.operation_id,
                "method": operation.method,
                "path": operation.path,
                "status": operation.success_status,
            }
            for operation in operations
        ],
    }
    return json.dumps(manifest, ensure_ascii=False, indent=2) + "\n"


def render_protocol_contract(contract: Contract) -> str:
    return (
        "// Code generated from api/openapi.json; DO NOT EDIT.\n"
        "\n"
        "package protocol\n"
        "\n"
        "const (\n"
        f'\tVersion                = "{contract.protocol_version}"\n'
        f'\tContractReleaseVersion = "{contract.release_version}"\n'
        f'\tContractReleaseStatus  = "{contract.release_status}"\n'
        f"\tContractLuantiRelease  = {contract.luanti_release}\n"
        f"\tMaxJSONSafeInteger     = {contract.max_json_safe_integer:_}\n"
        f"\tErrorCodeMaxLength     = {contract.error_code_max_length}\n"
        f"\tErrorMessageMaxLength  = {contract.error_message_max_length}\n"
        f"\tErrorFieldMaxLength    = {contract.error_field_max_length}\n"
        ")\n"
    )


def lower_camel(identifier: str) -> str:
    if not OPERATION_ID.fullmatch(identifier):
        raise ContractError(f"operationId {identifier!r} must use lower_snake_case")
    parts = identifier.split("_")
    return parts[0] + "".join(part[:1].upper() + part[1:] for part in parts[1:])


def render_go_routes(operations: Iterable[Operation]) -> str:
    operation_list = list(operations)
    method_constants = {
        "GET": "http.MethodGet",
        "POST": "http.MethodPost",
        "PUT": "http.MethodPut",
        "PATCH": "http.MethodPatch",
        "DELETE": "http.MethodDelete",
        "HEAD": "http.MethodHead",
        "OPTIONS": "http.MethodOptions",
    }
    status_constants = {
        200: "http.StatusOK",
        201: "http.StatusCreated",
        202: "http.StatusAccepted",
        204: "http.StatusNoContent",
    }
    handler_exceptions = {"state": "getSession"}
    route_lines: List[str] = []
    handler_names: List[Tuple[str, str]] = []
    for operation in operation_list:
        try:
            method_constant = method_constants[operation.method]
            status_constant = status_constants[operation.success_status]
        except KeyError as exc:
            raise ContractError(
                f"unsupported generated Go route value for {operation.operation_id}: {exc}"
            ) from exc
        handler = handler_exceptions.get(
            operation.operation_id,
            lower_camel(operation.operation_id),
        )
        route_lines.append(
            "\t{"
            f'OperationID: "{operation.operation_id}", '
            f"Method: {method_constant}, "
            f'Path: "{operation.path}", '
            f"SuccessStatus: {status_constant}"
            "},"
        )
        handler_names.append((operation.operation_id, handler))
    longest_id = max(len(operation_id) for operation_id, _ in handler_names)
    handler_lines = [
        f'\t\t"{operation_id}":'
        + " " * (longest_id - len(operation_id) + 1)
        + f"s.{handler},"
        for operation_id, handler in handler_names
    ]
    return (
        "// Code generated from api/openapi.json; DO NOT EDIT.\n"
        "\n"
        "package httpapi\n"
        "\n"
        "import (\n"
        '\t"context"\n'
        '\t"fmt"\n'
        '\t"net/http"\n'
        ")\n"
        "\n"
        "// ContractRoute is the generated HTTP projection of one OpenAPI operation.\n"
        "// It is exported for conformance tooling; callers must treat returned values as\n"
        "// immutable contract metadata.\n"
        "type ContractRoute struct {\n"
        "\tOperationID   string\n"
        "\tMethod        string\n"
        "\tPath          string\n"
        "\tSuccessStatus int\n"
        "}\n"
        "\n"
        "var generatedContractRoutes = [...]ContractRoute{\n"
        + "\n".join(route_lines)
        + "\n}\n"
        "\n"
        "// ContractRoutes returns a defensive copy of the generated route inventory.\n"
        "func ContractRoutes() []ContractRoute {\n"
        "\treturn append([]ContractRoute(nil), generatedContractRoutes[:]...)\n"
        "}\n"
        "\n"
        "type contractRouteContextKey struct{}\n"
        "\n"
        "func withContractRoute(route ContractRoute, handler http.HandlerFunc) http.HandlerFunc {\n"
        "\treturn func(response http.ResponseWriter, request *http.Request) {\n"
        "\t\tctx := context.WithValue(request.Context(), contractRouteContextKey{}, route)\n"
        "\t\thandler(response, request.WithContext(ctx))\n"
        "\t}\n"
        "}\n"
        "\n"
        "func contractSuccessStatus(request *http.Request) int {\n"
        "\troute, ok := request.Context().Value(contractRouteContextKey{}).(ContractRoute)\n"
        "\tif !ok {\n"
        '\t\tpanic("httpapi: request is missing generated OpenAPI route metadata")\n'
        "\t}\n"
        "\treturn route.SuccessStatus\n"
        "}\n"
        "\n"
        "func (s *Server) registerContractRoutes(mux *http.ServeMux) {\n"
        "\thandlers := map[string]http.HandlerFunc{\n"
        + "\n".join(handler_lines)
        + "\n\t}\n"
        "\tif len(handlers) != len(generatedContractRoutes) {\n"
        "\t\tpanic(fmt.Sprintf(\n"
        '\t\t\t"httpapi: generated contract has %d routes but server has %d handlers",\n'
        "\t\t\tlen(generatedContractRoutes),\n"
        "\t\t\tlen(handlers),\n"
        "\t\t))\n"
        "\t}\n"
        "\tseenPatterns := make(map[string]string, len(generatedContractRoutes))\n"
        "\tfor _, route := range generatedContractRoutes {\n"
        "\t\thandler, exists := handlers[route.OperationID]\n"
        "\t\tif !exists {\n"
        '\t\t\tpanic("httpapi: no handler for OpenAPI operation " + route.OperationID)\n'
        "\t\t}\n"
        '\t\tpattern := route.Method + " " + route.Path\n'
        "\t\tif previous, duplicate := seenPatterns[pattern]; duplicate {\n"
        "\t\t\tpanic(fmt.Sprintf(\n"
        '\t\t\t\t"httpapi: OpenAPI operations %s and %s share route %s",\n'
        "\t\t\t\tprevious,\n"
        "\t\t\t\troute.OperationID,\n"
        "\t\t\t\tpattern,\n"
        "\t\t\t))\n"
        "\t\t}\n"
        "\t\tseenPatterns[pattern] = route.OperationID\n"
        "\t\tmux.HandleFunc(pattern, withContractRoute(route, handler))\n"
        "\t\tdelete(handlers, route.OperationID)\n"
        "\t}\n"
        "\tif len(handlers) != 0 {\n"
        '\t\tpanic("httpapi: server has handlers absent from the generated OpenAPI route table")\n'
        "\t}\n"
        "}\n"
    )


def projection_rules(contract: Contract) -> Iterable[Projection]:
    version = contract.release_version
    protocol = contract.protocol_version
    release = str(contract.luanti_release)
    safe_integer = str(contract.max_json_safe_integer)
    readable_safe_integer = f"{contract.max_json_safe_integer:_}"
    return (
        Projection("Makefile", r"(?m)^VERSION \?= .+$", f"VERSION ?= {version}"),
        Projection(
            "cmd/rin/main.go",
            r'(?m)^var version = "[^"]+"$',
            f'var version = "{version}"',
        ),
        Projection(
            "sdk/python/pyproject.toml",
            r'(?m)^(version = )"[^"]+"$',
            rf'\g<1>"{version}"',
        ),
        Projection(
            "sdk/python/src/rin_sdk/client.py",
            r'(?m)^SDK_VERSION = "[^"]+"$',
            f'SDK_VERSION = "{version}"',
        ),
        Projection(
            "sdk/python/src/rin_sdk/client.py",
            r'(?m)^PROTOCOL_VERSION = "[^"]+"$',
            f'PROTOCOL_VERSION = "{protocol}"',
        ),
        Projection(
            "sdk/python/src/rin_sdk/client.py",
            r"(?m)^_MAX_JSON_SAFE_INTEGER = .+$",
            f"_MAX_JSON_SAFE_INTEGER = {readable_safe_integer}",
        ),
        Projection(
            "sdk/javascript/package.json",
            r'(?m)^(\s*"version"\s*:\s*)"[^"]+"(,?)$',
            rf'\g<1>"{version}"\g<2>',
        ),
        Projection(
            "sdk/javascript/src/index.js",
            r'(?m)^export const SDK_VERSION = "[^"]+";$',
            f'export const SDK_VERSION = "{version}";',
        ),
        Projection(
            "sdk/javascript/src/index.js",
            r'(?m)^export const PROTOCOL_VERSION = "[^"]+";$',
            f'export const PROTOCOL_VERSION = "{protocol}";',
        ),
        Projection(
            "sdk/javascript/src/index.d.ts",
            r'(?m)^export const SDK_VERSION: "[^"]+";$',
            f'export const SDK_VERSION: "{version}";',
        ),
        Projection(
            "sdk/javascript/src/index.d.ts",
            r'(?m)^export const PROTOCOL_VERSION: "[^"]+";$',
            f'export const PROTOCOL_VERSION: "{protocol}";',
        ),
        Projection(
            "sdk/csharp/Rin.Client/Rin.Client.csproj",
            r"<Version>[^<]+</Version>",
            f"<Version>{version}</Version>",
        ),
        Projection(
            "sdk/csharp/Rin.Client/RinClient.cs",
            r'(?m)^\s*public const string ClientVersion = "[^"]+";$',
            f'    public const string ClientVersion = "{version}";',
        ),
        Projection(
            "sdk/csharp/Rin.Client/RinClient.cs",
            r'(?m)^\s*public const string ProtocolVersion = "[^"]+";$',
            f'    public const string ProtocolVersion = "{protocol}";',
        ),
        Projection(
            "sdk/csharp/Rin.Client/RinClient.cs",
            r"(?m)^\s*private const decimal MaxJsonSafeInteger = .+;$",
            f"    private const decimal MaxJsonSafeInteger = {readable_safe_integer}m;",
        ),
        Projection(
            "sdk/csharp/Rin.Client.Tests/Program.cs",
            r'Require\(RinClient\.ClientVersion == "[^"]+"',
            f'Require(RinClient.ClientVersion == "{version}"',
        ),
        Projection(
            "sdk/java/src/main/java/io/github/sunrioa/rin/RinClient.java",
            r'(?m)^\s*public static final String VERSION = "[^"]+";$',
            f'    public static final String VERSION = "{version}";',
        ),
        Projection(
            "sdk/java/src/main/java/io/github/sunrioa/rin/RinClient.java",
            r'(?m)^\s*public static final String PROTOCOL_VERSION = "[^"]+";$',
            f'    public static final String PROTOCOL_VERSION = "{protocol}";',
        ),
        Projection(
            "sdk/java/src/main/java/io/github/sunrioa/rin/RinClient.java",
            r"(?m)^\s*private static final long MAX_SAFE_DOUBLE_INTEGER = .+;$",
            f"    private static final long MAX_SAFE_DOUBLE_INTEGER = {readable_safe_integer}L;",
        ),
        Projection(
            "sdk/java/test/io/github/sunrioa/rin/RinClientTest.java",
            r'"[^"]+"\.equals\(RinClient\.VERSION\)',
            f'"{version}".equals(RinClient.VERSION)',
        ),
        Projection(
            "sdk/lua/rin.lua",
            r'(?m)^(\s*VERSION = )"[^"]+"(,)$',
            rf'\g<1>"{version}"\g<2>',
        ),
        Projection(
            "sdk/lua/rin.lua",
            r'(?m)^(\s*PROTOCOL_VERSION = )"[^"]+"(,)$',
            rf'\g<1>"{protocol}"\g<2>',
        ),
        Projection(
            "sdk/lua/rin.lua",
            r"(?m)^local max_safe_float_integer = [0-9]+$",
            f"local max_safe_float_integer = {safe_integer}",
        ),
        Projection(
            "sdk/lua/test_client.lua",
            r'rin\.VERSION == "[^"]+"',
            f'rin.VERSION == "{version}"',
        ),
        Projection(
            "adapters/renpy/rin_client.py",
            r'(?m)^SDK_VERSION = "[^"]+"$',
            f'SDK_VERSION = "{version}"',
        ),
        Projection(
            "adapters/renpy/rin_client.py",
            r'(?m)^PROTOCOL_VERSION = "[^"]+"$',
            f'PROTOCOL_VERSION = "{protocol}"',
        ),
        Projection(
            "adapters/renpy/rin_client.py",
            r"(?m)^MAX_JSON_SAFE_INTEGER = .+$",
            f"MAX_JSON_SAFE_INTEGER = {readable_safe_integer}",
        ),
        Projection(
            "adapters/renpy/test_rin_client.py",
            r'self\.assertEqual\(rin_client\.SDK_VERSION, "[^"]+"\)',
            f'self.assertEqual(rin_client.SDK_VERSION, "{version}")',
        ),
        Projection(
            "examples/godot/rin_client.gd",
            r'(?m)^const PROTOCOL_VERSION := "[^"]+"$',
            f'const PROTOCOL_VERSION := "{protocol}"',
        ),
        Projection(
            "examples/unity/RinClient.cs",
            r'(?m)^\s*public const string ProtocolVersion = "[^"]+";$',
            f'    public const string ProtocolVersion = "{protocol}";',
        ),
        Projection(
            "examples/mods/bepinex-rin-npc/Plugin.cs",
            r'(?m)^\s*public const string PluginVersion = "[^"]+";$',
            f'    public const string PluginVersion = "{version}";',
        ),
        Projection(
            "examples/mods/fabric-rin-npc/src/main/resources/fabric.mod.json",
            r'(?m)^(\s*"version"\s*:\s*)"[^"]+"(,?)$',
            rf'\g<1>"{version}"\g<2>',
        ),
        Projection(
            "examples/mods/fabric-rin-npc/src/main/java/io/github/sunrioa/rin/example/RinNpcMod.java",
            r'"content_version", "[^"]+"',
            f'"content_version", "{version}"',
        ),
        Projection(
            "examples/mods/luanti-rin-npc/mod.conf",
            r"(?m)^release = [0-9]+$",
            f"release = {release}",
        ),
        Projection(
            "examples/mods/luanti-rin-npc/init.lua",
            r'local user_agent = "rin-luanti-example/[^"]+"',
            f'local user_agent = "rin-luanti-example/{version}"',
        ),
        Projection(
            "examples/mods/luanti-rin-npc/init.lua",
            r'content_version = "[^"]+"',
            f'content_version = "{version}"',
        ),
    )


def apply_projection(
    content: str,
    projection: Projection,
) -> str:
    updated, count = re.subn(projection.pattern, projection.replacement, content)
    if count != 1:
        raise ContractError(
            f"{projection.path}: projection pattern matched {count} times, want exactly 1: "
            f"{projection.pattern}"
        )
    return updated


def desired_files(contract: Contract) -> Dict[Path, str]:
    grouped: MutableMapping[str, List[Projection]] = {}
    for projection in projection_rules(contract):
        grouped.setdefault(projection.path, []).append(projection)
    desired: Dict[Path, str] = {}
    for path_string, projections in grouped.items():
        path = ROOT / path_string
        try:
            content = path.read_text(encoding="utf-8")
        except FileNotFoundError as exc:
            raise ContractError(f"missing projection target: {path_string}") from exc
        for projection in projections:
            content = apply_projection(content, projection)
        desired[path] = content
    operations = contract_operations(contract)
    desired[ROUTES_PATH] = render_routes(contract, operations)
    desired[ROOT / "protocol" / "contract_gen.go"] = render_protocol_contract(contract)
    desired[ROOT / "httpapi" / "routes_gen.go"] = render_go_routes(operations)

    lua_source = ROOT / "sdk" / "lua" / "rin.lua"
    vendored_lua = ROOT / "examples" / "mods" / "luanti-rin-npc" / "rin.lua"
    desired[vendored_lua] = desired[lua_source]
    return desired


def write_or_check(desired: Mapping[Path, str], write: bool) -> bool:
    stale: List[Path] = []
    for path, expected in desired.items():
        try:
            actual = path.read_text(encoding="utf-8")
        except FileNotFoundError:
            actual = ""
        if actual == expected:
            continue
        stale.append(path)
        if write:
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(expected, encoding="utf-8")
    if stale and not write:
        for path in stale:
            print(f"out of date: {relative(path)}", file=sys.stderr)
        print(
            "run: python3 tools/generate_contract.py --write",
            file=sys.stderr,
        )
        return False
    action = "updated" if write else "verified"
    print(f"{action} {len(desired)} contract projections")
    return True


def requested_tag(argument: Optional[str]) -> Optional[str]:
    ci_tag = os.environ.get("GITHUB_REF_TYPE") == "tag"
    if argument is None and not ci_tag:
        return None
    if argument not in (None, "__auto__"):
        return argument
    environment_tag = os.environ.get("GITHUB_REF_NAME", "") if ci_tag else ""
    if environment_tag:
        return environment_tag
    result = subprocess.run(
        ["git", "tag", "--points-at", "HEAD"],
        cwd=ROOT,
        check=True,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    tags = [line for line in result.stdout.splitlines() if line]
    if len(tags) != 1:
        raise ContractError(
            "tag verification needs exactly one tag at HEAD; pass --tag vMAJOR.MINOR.PATCH"
        )
    return tags[0]


def verify_tag(contract: Contract, observed_tag: Optional[str]) -> None:
    if observed_tag is None:
        return
    expected_tag = "v" + contract.release_version
    if observed_tag != expected_tag:
        raise ContractError(f"release tag {observed_tag!r} must equal {expected_tag!r}")
    print(f"verified release tag {observed_tag}")


def relative(path: Path) -> str:
    try:
        return str(path.relative_to(ROOT))
    except ValueError:
        return str(path)


def parse_arguments() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument("--write", action="store_true", help="update committed projections")
    mode.add_argument("--check", action="store_true", help="fail when a projection is stale")
    parser.add_argument(
        "--tag",
        nargs="?",
        const="__auto__",
        metavar="TAG",
        help="also require TAG (or the current CI/Git tag) to match info.version",
    )
    return parser.parse_args()


def main() -> int:
    arguments = parse_arguments()
    try:
        contract = load_contract()
        desired = desired_files(contract)
        if not write_or_check(desired, arguments.write):
            return 1
        verify_tag(contract, requested_tag(arguments.tag))
        return 0
    except (ContractError, OSError, subprocess.CalledProcessError) as exc:
        print(f"contract generation failed: {exc}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
