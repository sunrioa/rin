import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest import mock

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
            with (
                mock.patch.object(package_bepinex, "PROJECT", Path(temporary)),
                self.assertRaisesRegex(RuntimeError, "project.assets.json"),
            ):
                package_bepinex.validate_restored_packages("Plugin/Plugin.csproj")


if __name__ == "__main__":
    unittest.main()
