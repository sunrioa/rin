#!/usr/bin/env python3
"""Build and verify deterministic, install-ready BepInEx plugin ZIPs."""

from __future__ import annotations

import argparse
from dataclasses import dataclass
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import tempfile
from typing import Iterable, Optional, Sequence, Union
import zipfile
import xml.etree.ElementTree as ET


PROJECT = Path(__file__).resolve().parent
BACKEND_SUFFIXES = {
    "mono": "Mono",
    "il2cpp": "IL2CPP",
}
REQUIRED_MONO_RUNTIME = frozenset(
    {
        "Microsoft.Bcl.AsyncInterfaces.dll",
        "System.Buffers.dll",
        "System.Memory.dll",
        "System.Numerics.Vectors.dll",
        "System.Runtime.CompilerServices.Unsafe.dll",
        "System.Text.Encodings.Web.dll",
        "System.Text.Json.dll",
        "System.Threading.Tasks.Extensions.dll",
    }
)
MONO_THIRD_PARTY_SHA256 = {
    "LICENSE-DOTNET.txt":
        "cfc21f5e8bd655ae997eec916138b707b1d290b83272c02a95c9f821b8c87310",
    "THIRD-PARTY-NOTICES-DOTNET-STANDARD-2.0.txt":
        "06cf69d8c3f1170895d57ce881d3e0ab22676fc2cfa41459d035c4f699f2fa83",
    "THIRD-PARTY-NOTICES-MICROSOFT-BCL-8.0.txt":
        "7238e7fd468427aa3fe45b1d0cee1c3e2d93ff96692820768521e9780225d473",
    "THIRD-PARTY-NOTICES-MONO.txt":
        "2b63490489a1ad5dc49eaf3146c2c7b6b8e5b2b8a815d1e234fa9e0e3ffdfc52",
    "THIRD-PARTY-NOTICES-NUMERICS-VECTORS-4.4.txt":
        "a8bc8b3b6cababd6da43e4c776a77cceb4859eb4df06d5e5da2aabb22d19542d",
    "THIRD-PARTY-NOTICES-RUNTIME-UNSAFE-6.0.txt":
        "df255d595f29db06c6d462ceb7c04b33d627e98ac2f1745e1f1fb9f08eaaecb0",
    "THIRD-PARTY-NOTICES-TEXT-JSON-8.0.6.txt":
        "97c1a7b3da6a4c6ad516448719f45114b41a4d4c5aa300a944476e2e4f5da438",
}
ZIP_TIME = (1980, 1, 1, 0, 0, 0)
WINDOWS_RESERVED = frozenset(
    {"CON", "PRN", "AUX", "NUL"}
    | {f"COM{index}" for index in range(1, 10)}
    | {f"LPT{index}" for index in range(1, 10)}
    | {f"COM{suffix}" for suffix in ("¹", "²", "³")}
    | {f"LPT{suffix}" for suffix in ("¹", "²", "³")}
)
MAX_WINDOWS_SEGMENT_UTF16 = 255


@dataclass(frozen=True)
class Variant:
    name: str
    suffix: str
    code_name: str
    project: Path

    @property
    def plugin(self) -> str:
        return f"{self.code_name}.{self.suffix}.dll"

    @property
    def core(self) -> str:
        return f"{self.code_name}.Core.dll"

    @property
    def install_root(self) -> str:
        return f"BepInEx/plugins/{self.code_name}"

    @property
    def archive_prefix(self) -> str:
        return re.sub(
            r"(?<=[a-z0-9])(?=[A-Z])",
            "-",
            self.code_name,
        ).lower()


def github_escape(value: str) -> str:
    return (
        value.replace("%", "%25")
        .replace("\r", "%0D")
        .replace("\n", "%0A")
    )


def run(command: Sequence[str], project: Path = PROJECT) -> None:
    completed = subprocess.run(
        list(command),
        cwd=project,
        check=False,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
    )
    print(completed.stdout, end="")
    if completed.returncode == 0:
        return
    if os.environ.get("GITHUB_ACTIONS") == "true":
        detail = (
            f"command={' '.join(command)}\n"
            f"exit_code={completed.returncode}\n"
            f"{completed.stdout}"
        )
        print(
            "::error title=BepInEx package command failed::"
            + github_escape(detail)
        )
    raise subprocess.CalledProcessError(completed.returncode, command)


def validate_restored_packages(
    project_file: Union[Path, str],
    project: Path = PROJECT,
) -> None:
    assets = project / Path(project_file).parent / "obj" / "project.assets.json"
    if not assets.is_file():
        raise RuntimeError(f"restore did not create {assets}")


def discover_variants(
    project: Path,
    selected: Optional[Iterable[str]] = None,
) -> list[Variant]:
    requested = set(selected or BACKEND_SUFFIXES)
    unknown = requested - set(BACKEND_SUFFIXES)
    if unknown:
        raise RuntimeError(f"unsupported BepInEx backend(s): {sorted(unknown)}")
    variants: list[Variant] = []
    for name in sorted(requested):
        suffix = BACKEND_SUFFIXES[name]
        matches = sorted(project.glob(f"*.{suffix}/*.{suffix}.csproj"))
        if len(matches) > 1:
            raise RuntimeError(
                f"multiple {name} plugin projects found: "
                + ", ".join(str(item.relative_to(project)) for item in matches)
            )
        if not matches:
            if selected is not None:
                raise RuntimeError(f"no {name} plugin project found under {project}")
            continue
        project_file = matches[0]
        directory_suffix = "." + suffix
        directory_name = project_file.parent.name
        if (
            not directory_name.endswith(directory_suffix)
            or project_file.name != directory_name + ".csproj"
        ):
            raise RuntimeError(
                f"{project_file} must use matching <CodeName>.{suffix} names"
            )
        code_name = directory_name[: -len(directory_suffix)]
        _validate_portable_segment(code_name)
        core = project / f"{code_name}.Core" / f"{code_name}.Core.csproj"
        if not core.is_file():
            raise RuntimeError(
                f"{project_file.relative_to(project)} requires missing "
                f"{core.relative_to(project)}"
            )
        local_client = project / "Rin.Client" / "Rin.Client.csproj"
        reference_client = (
            project.parents[2] / "sdk" / "csharp" / "Rin.Client" /
            "Rin.Client.csproj"
        )
        if not local_client.is_file() and not (
            _is_reference_project(project) and reference_client.is_file()
        ):
            raise RuntimeError(
                f"{project_file.relative_to(project)} requires missing "
                "Rin.Client/Rin.Client.csproj"
            )
        variants.append(
            Variant(
                name=name,
                suffix=suffix,
                code_name=code_name,
                project=project_file.relative_to(project),
            )
        )
    if not variants:
        raise RuntimeError(f"no BepInEx plugin project found under {project}")
    code_names = {variant.code_name for variant in variants}
    if len(code_names) != 1:
        raise RuntimeError(
            f"BepInEx backend projects use different code names: {sorted(code_names)}"
        )
    return variants


def read_version(project: Path) -> str:
    props = project / "Directory.Build.props"
    try:
        version = ET.parse(props).getroot().findtext("./PropertyGroup/Version")
    except (OSError, ET.ParseError) as error:
        raise RuntimeError(f"read {props}: {error}") from error
    return _validate_version(version, "Directory.Build.props Version")


def _validate_version(value: object, field: str) -> str:
    if (
        not isinstance(value, str)
        or len(value) > 17
        or not re.fullmatch(
            r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)",
            value,
        )
        or any(int(component) > 65534 for component in value.split("."))
    ):
        raise RuntimeError(
            f"{field} must be numeric major.minor.patch, at most 17 ASCII "
            "characters, with every component between 0 and 65534"
        )
    return value


def validate_publish_layout(variant: Variant, published: Path) -> list[Path]:
    files = sorted(
        (item for item in published.glob("*.dll") if item.is_file()),
        key=lambda item: (item.name.casefold(), item.name),
    )
    by_case: dict[str, str] = {}
    for item in files:
        _validate_portable_segment(item.name)
        folded = item.name.casefold()
        if folded in by_case:
            raise RuntimeError(
                f"publish contains case-colliding DLLs "
                f"{by_case[folded]!r} and {item.name!r}"
            )
        by_case[folded] = item.name
    names = {item.name for item in files}
    required = {"Rin.Client.dll", variant.core, variant.plugin}
    if variant.name == "mono":
        required.update(REQUIRED_MONO_RUNTIME)
    missing = sorted(required - names)
    unexpected = sorted(names - required)
    if missing or unexpected:
        raise RuntimeError(
            f"{variant.name} publish layout is unsafe; "
            f"missing={missing}, unexpected={unexpected}. "
            "Review every new managed dependency and its redistribution "
            "notices before extending the package allowlist."
        )
    return files


def archive(
    variant: Variant,
    published: Path,
    destination: Path,
    version: str,
    license_payload: bytes,
    third_party_payloads: Optional[dict[str, bytes]] = None,
) -> None:
    files = validate_publish_layout(variant, published)
    version = _validate_version(version, "package version")
    if not license_payload.strip():
        raise RuntimeError("LICENSE-RIN.txt must not be empty")
    package_files = [
        (item.name, item.read_bytes())
        for item in files
    ]
    package_files.append(("LICENSE-RIN.txt", license_payload))
    third_party_payloads = dict(third_party_payloads or {})
    if variant.name == "mono":
        validate_mono_third_party_assets(
            third_party_payloads,
            "BepInEx Mono package input",
        )
        package_files.extend(third_party_payloads.items())
    elif third_party_payloads:
        raise RuntimeError(
            "IL2CPP packages must not claim Mono .NET third-party notices"
        )
    package_files.sort(key=lambda item: (item[0].casefold(), item[0]))
    manifest = {
        "format_version": 1,
        "variant": variant.name,
        "project": variant.code_name,
        "version": version,
        "install_root": variant.install_root,
        "files": [
            {
                "name": name,
                "sha256": hashlib.sha256(payload).hexdigest(),
            }
            for name, payload in package_files
        ],
    }
    destination.parent.mkdir(parents=True, exist_ok=True)
    descriptor, temporary_name = tempfile.mkstemp(
        prefix="." + destination.name + ".",
        suffix=".tmp",
        dir=destination.parent,
    )
    os.close(descriptor)
    temporary = Path(temporary_name)
    try:
        with zipfile.ZipFile(
            temporary,
            "w",
            compression=zipfile.ZIP_DEFLATED,
            compresslevel=9,
        ) as bundle:
            prefix = variant.install_root + "/"
            for name, payload in package_files:
                _write_zip_entry(bundle, prefix + name, payload)
            _write_zip_entry(
                bundle,
                prefix + "manifest.json",
                (json.dumps(manifest, indent=2, sort_keys=True) + "\n").encode(
                    "utf-8"
                ),
            )
        verify_archive(temporary, variant)
        os.replace(temporary, destination)
    finally:
        try:
            temporary.unlink()
        except FileNotFoundError:
            pass


def verify_archive(path: Path, expected: Optional[Variant] = None) -> None:
    with zipfile.ZipFile(path, "r") as bundle:
        infos = bundle.infolist()
        if not infos:
            raise RuntimeError(f"{path} is empty")
        if bundle.comment:
            raise RuntimeError(f"{path} contains a non-canonical ZIP comment")
        seen: dict[str, str] = {}
        for info in infos:
            _validate_archive_path(info.filename)
            if info.is_dir():
                raise RuntimeError(
                    f"{path} contains unsupported directory entry "
                    f"{info.filename!r}"
                )
            folded = info.filename.casefold()
            if folded in seen:
                raise RuntimeError(
                    f"{path} contains case-colliding entries "
                    f"{seen[folded]!r} and {info.filename!r}"
                )
            seen[folded] = info.filename
            if info.date_time != ZIP_TIME:
                raise RuntimeError(
                    f"{path} entry {info.filename!r} has a nondeterministic timestamp"
                )
            if info.compress_type != zipfile.ZIP_DEFLATED:
                raise RuntimeError(
                    f"{path} entry {info.filename!r} is not deflated"
                )
            if info.flag_bits & 0x1:
                raise RuntimeError(
                    f"{path} entry {info.filename!r} is encrypted"
                )
            mode = info.external_attr >> 16
            if (
                info.create_system != 3
                or not stat.S_ISREG(mode)
                or stat.S_IMODE(mode) != 0o644
            ):
                raise RuntimeError(
                    f"{path} entry {info.filename!r} is not a canonical "
                    "regular file"
                )
            if info.extra or info.comment:
                raise RuntimeError(
                    f"{path} entry {info.filename!r} contains "
                    "non-canonical ZIP metadata"
                )
        manifest_infos = [
            info for info in infos if info.filename.endswith("/manifest.json")
        ]
        if len(manifest_infos) != 1:
            raise RuntimeError(f"{path} must contain exactly one manifest.json")
        manifest_info = manifest_infos[0]
        manifest = _decode_manifest(bundle.read(manifest_info), path)
        variant = _variant_from_manifest(manifest)
        if expected is not None and variant != expected:
            raise RuntimeError(
                f"{path} identifies {variant}, expected {expected}"
            )
        prefix = variant.install_root + "/"
        if manifest_info.filename != prefix + "manifest.json":
            raise RuntimeError(f"{path} manifest is outside its install root")
        declared_files = manifest.get("files")
        if not isinstance(declared_files, list):
            raise RuntimeError(f"{path} manifest files must be an array")
        declared: dict[str, str] = {}
        for entry in declared_files:
            if not isinstance(entry, dict) or set(entry) != {"name", "sha256"}:
                raise RuntimeError(f"{path} contains an invalid manifest file entry")
            name = entry["name"]
            digest = entry["sha256"]
            if (
                not isinstance(name, str)
                or not isinstance(digest, str)
                or not re.fullmatch(r"[0-9a-f]{64}", digest)
            ):
                raise RuntimeError(f"{path} contains invalid file metadata")
            _validate_portable_segment(name)
            folded = name.casefold()
            if folded in {item.casefold() for item in declared}:
                raise RuntimeError(f"{path} manifest contains duplicate file {name!r}")
            declared[name] = digest
        archive_files = {
            info.filename[len(prefix) :]: info
            for info in infos
            if info is not manifest_info and info.filename.startswith(prefix)
        }
        if len(archive_files) != len(infos) - 1:
            raise RuntimeError(f"{path} contains files outside its install root")
        if set(archive_files) != set(declared):
            raise RuntimeError(
                f"{path} manifest/archive file mismatch; "
                f"declared={sorted(declared)}, actual={sorted(archive_files)}"
            )
        for name, digest in declared.items():
            actual = hashlib.sha256(bundle.read(archive_files[name])).hexdigest()
            if actual != digest:
                raise RuntimeError(f"{path} hash mismatch for {name}")
        _validate_archive_dependencies(path, variant, set(declared))
        if variant.name == "mono":
            validate_mono_third_party_assets(
                {
                    name: bundle.read(archive_files[name])
                    for name in MONO_THIRD_PARTY_SHA256
                },
                str(path),
            )


def _decode_manifest(payload: bytes, path: Path) -> dict:
    def reject_duplicates(pairs: list[tuple[str, object]]) -> dict:
        result = {}
        for key, value in pairs:
            if key in result:
                raise RuntimeError(f"{path} manifest repeats key {key!r}")
            result[key] = value
        return result

    try:
        value = json.loads(payload.decode("utf-8"), object_pairs_hook=reject_duplicates)
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise RuntimeError(f"{path} contains an invalid manifest: {error}") from error
    if not isinstance(value, dict):
        raise RuntimeError(f"{path} manifest must be an object")
    return value


def _variant_from_manifest(manifest: dict) -> Variant:
    if manifest.get("format_version") != 1:
        raise RuntimeError("package manifest has an unsupported format_version")
    name = manifest.get("variant")
    suffix = BACKEND_SUFFIXES.get(name)
    code_name = manifest.get("project")
    version = manifest.get("version")
    if (
        suffix is None
        or not isinstance(code_name, str)
    ):
        raise RuntimeError("package manifest has invalid identity fields")
    _validate_version(version, "package manifest version")
    _validate_portable_segment(code_name)
    variant = Variant(
        name=name,
        suffix=suffix,
        code_name=code_name,
        project=Path(f"{code_name}.{suffix}/{code_name}.{suffix}.csproj"),
    )
    if manifest.get("install_root") != variant.install_root:
        raise RuntimeError("package manifest has an invalid install_root")
    return variant


def _validate_archive_dependencies(
    path: Path,
    variant: Variant,
    names: set[str],
) -> None:
    required = {
        "LICENSE-RIN.txt",
        "Rin.Client.dll",
        variant.core,
        variant.plugin,
    }
    if variant.name == "mono":
        required.update(REQUIRED_MONO_RUNTIME)
        required.update(MONO_THIRD_PARTY_SHA256)
    else:
        unexpected_notices = sorted(
            set(MONO_THIRD_PARTY_SHA256).intersection(names)
        )
        if unexpected_notices:
            raise RuntimeError(
                f"{path} IL2CPP package contains Mono-only third-party "
                f"notices: {unexpected_notices}"
            )
    missing = sorted(required - names)
    unexpected = sorted(names - required)
    if missing or unexpected:
        raise RuntimeError(
            f"{path} dependency layout is unsafe; "
            f"missing={missing}, unexpected={unexpected}"
        )


def _write_zip_entry(
    bundle: zipfile.ZipFile,
    name: str,
    payload: bytes,
) -> None:
    info = zipfile.ZipInfo(name, ZIP_TIME)
    info.create_system = 3
    info.compress_type = zipfile.ZIP_DEFLATED
    info.external_attr = (stat.S_IFREG | 0o644) << 16
    bundle.writestr(info, payload)


def _validate_archive_path(name: str) -> None:
    if (
        not name
        or "\\" in name
        or name.startswith("/")
        or re.match(r"^[A-Za-z]:", name)
    ):
        raise RuntimeError(f"unsafe ZIP path {name!r}")
    segments = name.split("/")
    if any(segment in {"", ".", ".."} for segment in segments):
        raise RuntimeError(f"unsafe ZIP path {name!r}")
    for segment in segments:
        _validate_portable_segment(segment)


def _validate_portable_segment(name: str) -> None:
    if (
        not name
        or name.rstrip(" .") != name
        or any(ord(character) < 32 for character in name)
        or any(character in '<>:"/\\|?*' for character in name)
    ):
        raise RuntimeError(f"unsafe Windows path segment {name!r}")
    stem = name.split(".", 1)[0].upper()
    if stem in WINDOWS_RESERVED:
        raise RuntimeError(f"Windows-reserved path segment {name!r}")
    if len(name.encode("utf-16-le")) // 2 > MAX_WINDOWS_SEGMENT_UTF16:
        raise RuntimeError(
            f"Windows path segment exceeds {MAX_WINDOWS_SEGMENT_UTF16} "
            f"UTF-16 code units: {name!r}"
        )


def read_rin_license(project: Path) -> bytes:
    candidates = [project / "LICENSE-RIN.txt"]
    if _is_reference_project(project):
        candidates.append(project.parents[2] / "LICENSE")
    for candidate in candidates:
        try:
            payload = candidate.read_bytes()
        except FileNotFoundError:
            continue
        if payload.strip():
            return payload
        raise RuntimeError(f"{candidate} is empty")
    raise RuntimeError(
        f"{project} has no LICENSE-RIN.txt and is not inside a Rin source checkout"
    )


def _is_reference_project(project: Path) -> bool:
    return (
        project.name == "bepinex-rin-npc"
        and project.parent.name == "mods"
        and project.parent.parent.name == "examples"
    )


def read_mono_third_party_assets(project: Path) -> dict[str, bytes]:
    directory = project / "third-party"
    assets = {}
    for name in MONO_THIRD_PARTY_SHA256:
        path = directory / name
        try:
            assets[name] = path.read_bytes()
        except FileNotFoundError as error:
            raise RuntimeError(
                f"BepInEx Mono packaging requires reviewed notice asset {path}"
            ) from error
    validate_mono_third_party_assets(assets, str(directory))
    return assets


def validate_mono_third_party_assets(
    assets: dict[str, bytes],
    context: str,
) -> None:
    expected_names = set(MONO_THIRD_PARTY_SHA256)
    actual_names = set(assets)
    if actual_names != expected_names:
        raise RuntimeError(
            f"{context} Mono third-party notice set is incomplete; "
            f"missing={sorted(expected_names - actual_names)}, "
            f"unexpected={sorted(actual_names - expected_names)}"
        )
    for name, expected_digest in MONO_THIRD_PARTY_SHA256.items():
        payload = assets[name]
        if not payload.strip():
            raise RuntimeError(f"{context} notice asset {name} is empty")
        actual_digest = hashlib.sha256(payload).hexdigest()
        if actual_digest != expected_digest:
            raise RuntimeError(
                f"{context} notice asset {name} is not the reviewed content; "
                f"sha256={actual_digest}, expected={expected_digest}"
            )


def main(arguments: Optional[Sequence[str]] = None) -> None:
    parser = argparse.ArgumentParser(
        description=(
            "Build deterministic single-backend or reference BepInEx install ZIPs"
        )
    )
    parser.add_argument("--dotnet", default=os.environ.get("DOTNET", "dotnet"))
    parser.add_argument("--project", type=Path, default=PROJECT)
    parser.add_argument("--output", type=Path)
    parser.add_argument(
        "--variant",
        action="append",
        choices=sorted(BACKEND_SUFFIXES),
        help="backend to package; defaults to every backend project present",
    )
    parser.add_argument(
        "--verify-archive",
        action="append",
        type=Path,
        help="verify an existing package without building",
    )
    args = parser.parse_args(arguments)
    project = args.project.resolve()
    if args.verify_archive:
        for archive_path in args.verify_archive:
            verify_archive(archive_path.resolve())
            print(f"verified {archive_path}")
        return
    variants = discover_variants(project, args.variant)
    version = read_version(project)
    license_payload = read_rin_license(project)
    mono_third_party = (
        read_mono_third_party_assets(project)
        if any(variant.name == "mono" for variant in variants)
        else {}
    )
    output = args.output
    if output is None:
        output = project / "dist"
    elif not output.is_absolute():
        output = project / output

    for variant in variants:
        run(
            [
                args.dotnet,
                "restore",
                variant.project.as_posix(),
                "--locked-mode",
                "--nologo",
                "-p:RestoreDisableParallel=true",
                "-m:1",
            ],
            project,
        )
        validate_restored_packages(variant.project, project)

    with tempfile.TemporaryDirectory(prefix="rin-bepinex-package-") as temporary:
        stage = Path(temporary)
        for variant in variants:
            published = stage / variant.name
            run(
                [
                    args.dotnet,
                    "publish",
                    variant.project.as_posix(),
                    "-c",
                    "Release",
                    "--no-restore",
                    "--nologo",
                    "-m:1",
                    "-p:UseSharedCompilation=false",
                    "-p:BuildInParallel=false",
                    "-o",
                    str(published),
                ],
                project,
            )
            destination = (
                output
                / f"{variant.archive_prefix}-bepinex-{variant.name}-{version}.zip"
            )
            archive(
                variant,
                published,
                destination,
                version,
                license_payload,
                mono_third_party if variant.name == "mono" else None,
            )
            print(f"created {destination}")


if __name__ == "__main__":
    main()
