from __future__ import annotations

import unittest

from tools.verify_luanti import available_udp_port, validate_version


class VerifyLuantiTests(unittest.TestCase):
    def test_accepts_exact_windows_version(self) -> None:
        validate_version("Luanti 5.16.1\r\n")

    def test_accepts_version_with_build_details(self) -> None:
        validate_version("Luanti 5.16.1 (OSX)\n")

    def test_rejects_other_version(self) -> None:
        with self.assertRaisesRegex(SystemExit, "expected Luanti 5.16.1"):
            validate_version("Luanti 5.16.0\n")

    def test_rejects_version_prefix_collision(self) -> None:
        with self.assertRaisesRegex(SystemExit, "expected Luanti 5.16.1"):
            validate_version("Luanti 5.16.10\n")

    def test_allocates_valid_udp_port(self) -> None:
        self.assertIn(available_udp_port(), range(1, 65536))


if __name__ == "__main__":
    unittest.main()
