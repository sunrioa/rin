import json
import hashlib
import os
from pathlib import Path
import stat
import subprocess
import tempfile
import unittest
from unittest import mock
import zipfile

from tools import package_bepinex


class PackageBepInExTest(unittest.TestCase):
    def test_github_escape_encodes_workflow_commands(self) -> None:
        self.assertEqual(
            "rate%25limit%0D%0Asecond line",
            package_bepinex.github_escape("rate%limit\r\nsecond line"),
        )

    def test_run_emits_actionable_github_annotation(self) -> None:
        failed = subprocess.CompletedProcess(
            ["dotnet", "restore"],
            1,
            stdout="NU1301: feed unavailable\n",
        )
        with (
            mock.patch.object(subprocess, "run", return_value=failed),
            mock.patch.dict(os.environ, {"GITHUB_ACTIONS": "true"}),
            mock.patch("builtins.print") as output,
            self.assertRaises(subprocess.CalledProcessError),
        ):
            package_bepinex.run(["dotnet", "restore"])

        annotation = output.call_args_list[-1].args[0]
        self.assertIn("::error title=BepInEx package command failed::", annotation)
        self.assertIn("NU1301: feed unavailable%0A", annotation)

    def test_validate_restored_packages_rejects_missing_assets(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            with self.assertRaisesRegex(RuntimeError, "project.assets.json"):
                package_bepinex.validate_restored_packages(
                    "Plugin/Plugin.csproj",
                    Path(temporary),
                )

    def test_discovers_only_the_backend_present_in_a_generated_project(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            project = Path(temporary)
            self._write_project(project, "GuideNpc", "Mono")
            previous = Path.cwd()
            try:
                os.chdir(project.parent)
                shallow_project = Path(project.name)
                variants = package_bepinex.discover_variants(shallow_project)
                self.assertEqual(
                    ["mono"],
                    [variant.name for variant in variants],
                )
                self.assertEqual("GuideNpc.Mono.dll", variants[0].plugin)
                self.assertEqual(
                    "BepInEx/plugins/GuideNpc",
                    variants[0].install_root,
                )
                with self.assertRaisesRegex(RuntimeError, "no il2cpp"):
                    package_bepinex.discover_variants(
                        shallow_project,
                        ["il2cpp"],
                    )
            finally:
                os.chdir(previous)

    def test_discovers_both_backends_in_the_canonical_reference_checkout(
        self,
    ) -> None:
        variants = package_bepinex.discover_variants(
            package_bepinex.PROJECT
        )
        self.assertEqual(
            ["il2cpp", "mono"],
            [variant.name for variant in variants],
        )

    def test_mono_archive_is_deterministic_and_complete(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            published = root / "publish"
            published.mkdir()
            variant = self._variant("mono")
            required = {
                "Rin.Client.dll",
                variant.core,
                variant.plugin,
            } | set(package_bepinex.REQUIRED_MONO_RUNTIME)
            for name in required:
                (published / name).write_bytes(("payload:" + name).encode("ascii"))

            first = root / "first.zip"
            second = root / "second.zip"
            license_payload = b"MIT License\n\nRin test notice.\n"
            third_party_payloads = (
                package_bepinex.read_mono_third_party_assets(
                    package_bepinex.PROJECT
                )
            )
            package_bepinex.archive(
                variant,
                published,
                first,
                "0.1.0",
                license_payload,
                third_party_payloads,
            )
            package_bepinex.archive(
                variant,
                published,
                second,
                "0.1.0",
                license_payload,
                third_party_payloads,
            )
            self.assertEqual(first.read_bytes(), second.read_bytes())
            package_bepinex.verify_archive(first, variant)

            with zipfile.ZipFile(first) as bundle:
                prefix = "BepInEx/plugins/GuideNpc/"
                names = set(bundle.namelist())
                for info in bundle.infolist():
                    self.assertEqual(package_bepinex.ZIP_TIME, info.date_time)
                    self.assertEqual(3, info.create_system)
                    self.assertTrue(
                        stat.S_ISREG(info.external_attr >> 16),
                        info.filename,
                    )
                    self.assertEqual(
                        0o644,
                        stat.S_IMODE(info.external_attr >> 16),
                        info.filename,
                    )
                self.assertIn(prefix + "System.Text.Json.dll", names)
                self.assertIn(prefix + "System.Memory.dll", names)
                self.assertIn(prefix + "LICENSE-RIN.txt", names)
                self.assertIn(prefix + "LICENSE-DOTNET.txt", names)
                self.assertIn(
                    prefix + "THIRD-PARTY-NOTICES-MONO.txt",
                    names,
                )
                self.assertIn(prefix + "manifest.json", names)
                manifest = json.loads(bundle.read(prefix + "manifest.json"))
                self.assertEqual("mono", manifest["variant"])
                self.assertEqual("0.1.0", manifest["version"])
                self.assertEqual(
                    sorted(
                        required
                        | {"LICENSE-RIN.txt"}
                        | set(package_bepinex.MONO_THIRD_PARTY_SHA256)
                    ),
                    sorted(entry["name"] for entry in manifest["files"]),
                )

    def test_mono_notice_assets_are_required_and_content_pinned(self) -> None:
        assets = package_bepinex.read_mono_third_party_assets(
            package_bepinex.PROJECT
        )
        missing = dict(assets)
        missing.pop("LICENSE-DOTNET.txt")
        with self.assertRaisesRegex(RuntimeError, "incomplete"):
            package_bepinex.validate_mono_third_party_assets(
                missing,
                "test",
            )
        modified = dict(assets)
        modified["LICENSE-DOTNET.txt"] += b"modified"
        with self.assertRaisesRegex(RuntimeError, "not the reviewed content"):
            package_bepinex.validate_mono_third_party_assets(
                modified,
                "test",
            )

    def test_mono_archive_rejects_missing_transitive_json_dependency(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            published = Path(temporary)
            variant = self._variant("mono")
            required = {
                "Rin.Client.dll",
                variant.core,
                variant.plugin,
            } | set(package_bepinex.REQUIRED_MONO_RUNTIME)
            required.remove("System.Memory.dll")
            for name in required:
                (published / name).write_bytes(b"dll")
            with self.assertRaisesRegex(RuntimeError, "System.Memory.dll"):
                package_bepinex.validate_publish_layout(variant, published)

    def test_il2cpp_archive_uses_actual_managed_output_and_rejects_host_runtime(
        self,
    ) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            published = root / "publish"
            published.mkdir()
            variant = self._variant("il2cpp")
            for name in {"Rin.Client.dll", variant.core, variant.plugin}:
                (published / name).write_bytes(("payload:" + name).encode("ascii"))
            destination = root / "il2cpp.zip"
            package_bepinex.archive(
                variant,
                published,
                destination,
                "0.1.0",
                b"MIT License\n\nRin test notice.\n",
            )
            package_bepinex.verify_archive(destination, variant)
            with zipfile.ZipFile(destination) as bundle:
                self.assertNotIn(
                    "BepInEx/plugins/GuideNpc/System.Text.Json.dll",
                    bundle.namelist(),
                )
                self.assertIn(
                    "BepInEx/plugins/GuideNpc/LICENSE-RIN.txt",
                    bundle.namelist(),
                )
                self.assertFalse(
                    set(package_bepinex.MONO_THIRD_PARTY_SHA256).intersection(
                        Path(name).name for name in bundle.namelist()
                    )
                )

            (published / "bEpInEx.Core.dll").write_bytes(b"host runtime")
            with self.assertRaisesRegex(RuntimeError, "unexpected"):
                package_bepinex.validate_publish_layout(variant, published)

    def test_project_dll_names_are_not_mistaken_for_extra_dependencies(self) -> None:
        for code_name in ("BepinexTools", "UnityHelper", "Il2cppHelper"):
            with self.subTest(code_name=code_name):
                with tempfile.TemporaryDirectory() as temporary:
                    root = Path(temporary)
                    published = root / "publish"
                    published.mkdir()
                    variant = package_bepinex.Variant(
                        name="il2cpp",
                        suffix="IL2CPP",
                        code_name=code_name,
                        project=Path(
                            f"{code_name}.IL2CPP/{code_name}.IL2CPP.csproj"
                        ),
                    )
                    for name in {
                        "Rin.Client.dll",
                        variant.core,
                        variant.plugin,
                    }:
                        (published / name).write_bytes(b"project output")
                    destination = root / "plugin.zip"
                    package_bepinex.archive(
                        variant,
                        published,
                        destination,
                        "0.1.0",
                        b"MIT License\n",
                    )
                    package_bepinex.verify_archive(destination, variant)

    def test_publish_layout_rejects_every_unreviewed_managed_dependency(
        self,
    ) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            published = Path(temporary)
            variant = self._variant("il2cpp")
            for name in {
                "Rin.Client.dll",
                variant.core,
                variant.plugin,
                "Helpful.Library.dll",
            }:
                (published / name).write_bytes(b"managed output")
            with self.assertRaisesRegex(
                RuntimeError,
                r"unexpected=.*Helpful\.Library\.dll",
            ):
                package_bepinex.validate_publish_layout(variant, published)

    def test_archive_verifier_requires_rin_license(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            destination = Path(temporary) / "missing-license.zip"
            variant = self._variant("il2cpp")
            payloads = {
                "Rin.Client.dll": b"client",
                variant.core: b"core",
                variant.plugin: b"plugin",
            }
            manifest = {
                "format_version": 1,
                "variant": variant.name,
                "project": variant.code_name,
                "version": "0.1.0",
                "install_root": variant.install_root,
                "files": [
                    {
                        "name": name,
                        "sha256": hashlib.sha256(payload).hexdigest(),
                    }
                    for name, payload in sorted(payloads.items())
                ],
            }
            with zipfile.ZipFile(destination, "w") as bundle:
                prefix = variant.install_root + "/"
                for name, payload in payloads.items():
                    self._write_zip_entry(bundle, prefix + name, payload)
                self._write_zip_entry(
                    bundle,
                    prefix + "manifest.json",
                    (json.dumps(manifest, sort_keys=True) + "\n").encode("utf-8"),
                )
            with self.assertRaisesRegex(RuntimeError, "LICENSE-RIN.txt"):
                package_bepinex.verify_archive(destination, variant)

    def test_version_matches_windows_assembly_component_limits(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            project = Path(temporary)
            props = project / "Directory.Build.props"
            props.write_text(
                "<Project><PropertyGroup><Version>"
                "65534.65534.65534"
                "</Version></PropertyGroup></Project>",
                encoding="utf-8",
            )
            self.assertEqual(
                "65534.65534.65534",
                package_bepinex.read_version(project),
            )
            props.write_text(
                "<Project><PropertyGroup><Version>"
                "65535.0.0"
                "</Version></PropertyGroup></Project>",
                encoding="utf-8",
            )
            with self.assertRaisesRegex(RuntimeError, "0 and 65534"):
                package_bepinex.read_version(project)

    def test_archive_verifier_rejects_cross_platform_traversal(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            destination = Path(temporary) / "unsafe.zip"
            with zipfile.ZipFile(destination, "w") as bundle:
                info = zipfile.ZipInfo("../escape.dll", (1980, 1, 1, 0, 0, 0))
                info.compress_type = zipfile.ZIP_DEFLATED
                bundle.writestr(info, b"escape")
            with self.assertRaisesRegex(RuntimeError, "unsafe ZIP path"):
                package_bepinex.verify_archive(destination)

    def test_archive_verifier_rejects_directory_and_symlink_entries(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            published = root / "publish"
            published.mkdir()
            variant = self._variant("il2cpp")
            for name in {"Rin.Client.dll", variant.core, variant.plugin}:
                (published / name).write_bytes(b"managed output")

            directory_archive = root / "directory.zip"
            package_bepinex.archive(
                variant,
                published,
                directory_archive,
                "0.1.0",
                b"MIT License\n",
            )
            with zipfile.ZipFile(directory_archive, "a") as bundle:
                info = zipfile.ZipInfo(
                    "../../escape/",
                    package_bepinex.ZIP_TIME,
                )
                info.create_system = 3
                info.compress_type = zipfile.ZIP_DEFLATED
                info.external_attr = (stat.S_IFDIR | 0o755) << 16
                bundle.writestr(info, b"")
            with self.assertRaisesRegex(
                RuntimeError,
                "unsafe ZIP path|directory entry",
            ):
                package_bepinex.verify_archive(directory_archive)

            symlink_archive = root / "symlink.zip"
            package_bepinex.archive(
                variant,
                published,
                symlink_archive,
                "0.1.0",
                b"MIT License\n",
            )
            with zipfile.ZipFile(symlink_archive, "a") as bundle:
                info = zipfile.ZipInfo(
                    variant.install_root + "/link.dll",
                    package_bepinex.ZIP_TIME,
                )
                info.create_system = 3
                info.compress_type = zipfile.ZIP_DEFLATED
                info.external_attr = (stat.S_IFLNK | 0o777) << 16
                bundle.writestr(info, b"target.dll")
            with self.assertRaisesRegex(RuntimeError, "regular file"):
                package_bepinex.verify_archive(symlink_archive)

    def test_windows_segment_validation_covers_unicode_devices_and_utf16(
        self,
    ) -> None:
        for name in ("COM¹.dll", "com².txt", "LPT³"):
            with self.subTest(name=name), self.assertRaisesRegex(
                RuntimeError,
                "Windows-reserved",
            ):
                package_bepinex.validate_portable_segment(name)
        with self.assertRaisesRegex(RuntimeError, "255 UTF-16"):
            package_bepinex.validate_portable_segment("😀" * 128)

    @staticmethod
    def _variant(name: str):
        suffix = "Mono" if name == "mono" else "IL2CPP"
        return package_bepinex.Variant(
            name=name,
            suffix=suffix,
            code_name="GuideNpc",
            project=Path(
                f"GuideNpc.{suffix}/GuideNpc.{suffix}.csproj"
            ),
        )

    @staticmethod
    def _write_project(project: Path, code_name: str, suffix: str) -> None:
        for path in (
            project / f"{code_name}.Core" / f"{code_name}.Core.csproj",
            project / "Rin.Client" / "Rin.Client.csproj",
            project / f"{code_name}.{suffix}" / f"{code_name}.{suffix}.csproj",
        ):
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("<Project />\n", encoding="utf-8")

    @staticmethod
    def _write_zip_entry(
        bundle: zipfile.ZipFile,
        name: str,
        payload: bytes,
    ) -> None:
        info = zipfile.ZipInfo(name, (1980, 1, 1, 0, 0, 0))
        info.create_system = 3
        info.compress_type = zipfile.ZIP_DEFLATED
        info.external_attr = (stat.S_IFREG | 0o644) << 16
        bundle.writestr(info, payload)


if __name__ == "__main__":
    unittest.main()
