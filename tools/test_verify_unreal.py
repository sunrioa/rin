from __future__ import annotations

import shutil
import tempfile
import unittest
from contextlib import contextmanager
from pathlib import Path
from typing import Iterator

from tools.verify_unreal import PLUGIN, verify_plugin, verify_relative_paths


@contextmanager
def plugin_copy() -> Iterator[Path]:
    with tempfile.TemporaryDirectory() as temporary:
        plugin = Path(temporary) / "RinHost"
        shutil.copytree(PLUGIN, plugin)
        yield plugin


class VerifyUnrealTests(unittest.TestCase):
    def test_checked_in_plugin_passes(self) -> None:
        verify_plugin(PLUGIN)

    def test_rejects_case_collisions(self) -> None:
        with self.assertRaisesRegex(SystemExit, "Windows path collision"):
            verify_relative_paths(["Source/Rin.cpp", "source/rin.cpp"])

    def test_rejects_windows_reserved_paths(self) -> None:
        with self.assertRaisesRegex(SystemExit, "Windows-incompatible path"):
            verify_relative_paths(["Source/CON.txt"])

    def test_rejects_forbidden_execution_surface(self) -> None:
        with plugin_copy() as plugin:
            source = plugin / "Source" / "RinHost" / "Private" / "Unsafe.cpp"
            source.write_text(
                'void f() { system("command"); }',
                encoding="utf-8",
            )
            with self.assertRaisesRegex(SystemExit, "forbidden token"):
                verify_plugin(plugin)

    def test_requires_exact_offer_binding_at_authorization(self) -> None:
        with plugin_copy() as plugin:
            source = plugin / "Source/RinHost/Private/RinHostSubsystem.cpp"
            contents = source.read_text(encoding="utf-8")
            source.write_text(
                contents.replace(
                    "!OfferMatchesInvocation(*Offer, Invocation) ||",
                    "Invocation.OfferId.IsEmpty() ||",
                    1,
                ),
                encoding="utf-8",
            )
            with self.assertRaisesRegex(SystemExit, "OfferMatchesInvocation"):
                verify_plugin(plugin)

    def test_requires_revocation_to_invalidate_queued_runs(self) -> None:
        with plugin_copy() as plugin:
            source = plugin / "Source/RinHost/Private/RinHostSubsystem.cpp"
            contents = source.read_text(encoding="utf-8")
            source.write_text(
                contents.replace(
                    "MarkQueuedRunsStaleForCapability(CapabilityKey);",
                    "static_cast<void>(CapabilityKey);",
                    1,
                ),
                encoding="utf-8",
            )
            with self.assertRaisesRegex(
                SystemExit,
                "MarkQueuedRunsStaleForCapability",
            ):
                verify_plugin(plugin)


if __name__ == "__main__":
    unittest.main()
