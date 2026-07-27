#!/usr/bin/env python3
"""Load the Luanti reference in a real dedicated server twice."""

from __future__ import annotations

import argparse
import json
import os
import pathlib
import re
import shutil
import subprocess
import tempfile


VERSION = "5.16.1"
MARKER = re.compile(r"\[rin_lifecycle\]\s+(\{.*\})")
SERVER_TIMEOUT_SECONDS = 90


def run(command: list[str], environment: dict[str, str]) -> str:
    try:
        completed = subprocess.run(
            command,
            check=False,
            env=environment,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
            timeout=SERVER_TIMEOUT_SECONDS,
        )
    except subprocess.TimeoutExpired as error:
        output = error.stdout or ""
        if isinstance(output, bytes):
            output = output.decode(errors="replace")
        raise SystemExit(
            f"Luanti verification timed out after "
            f"{SERVER_TIMEOUT_SECONDS} seconds:\n{output}"
        ) from error
    if completed.returncode != 0:
        raise SystemExit(
            f"Luanti verification failed ({completed.returncode}):\n"
            f"{completed.stdout}"
        )
    return completed.stdout


def epoch(output: str) -> dict[str, object]:
    matches = MARKER.findall(output)
    if len(matches) != 1:
        raise SystemExit(f"expected one Luanti lifecycle marker:\n{output}")
    value = json.loads(matches[0])
    if (
        not isinstance(value, dict)
        or not isinstance(value.get("world_id"), str)
        or len(value["world_id"]) != 32
        or not all(
            isinstance(value.get(field), int) and value[field] > 0
            for field in ("host", "world", "timeline")
        )
    ):
        raise SystemExit(f"invalid Luanti lifecycle Epoch: {value!r}")
    return value


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--luanti", required=True, type=pathlib.Path)
    parser.add_argument(
        "--mod",
        type=pathlib.Path,
        default=pathlib.Path("examples/mods/luanti-rin-npc"),
    )
    args = parser.parse_args()
    executable = str(args.luanti.resolve())
    version = subprocess.run(
        [executable, "--version"],
        check=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
        timeout=30,
    ).stdout
    if not version.startswith(f"Luanti {VERSION} "):
        raise SystemExit(f"expected Luanti {VERSION}, got {version!r}")

    with tempfile.TemporaryDirectory(prefix="rin-luanti-") as temporary:
        root = pathlib.Path(temporary)
        user = root / "user"
        game = user / "games" / "rin_test"
        world = user / "worlds" / "rin_test"
        mod_source = args.mod.resolve()
        mod_configuration = (mod_source / "mod.conf").read_text(encoding="utf-8")
        name_match = re.search(r"(?m)^name\s*=\s*([a-z0-9_]+)\s*$", mod_configuration)
        if not name_match:
            raise SystemExit("Luanti mod.conf has no valid name")
        mod_name = name_match.group(1)
        mod = game / "mods" / mod_name
        mod.parent.mkdir(parents=True)
        world.mkdir(parents=True)
        shutil.copytree(mod_source, mod)
        shutil.copy2(
            pathlib.Path("sdk/lua/test_client.lua").resolve(),
            mod / "test_client.lua",
        )
        shutil.copy2(
            pathlib.Path("sdk/conformance/routes.json").resolve(),
            mod / "routes.json",
        )
        (game / "game.conf").write_text(
            "title = Rin lifecycle test\n"
            "name = rin_test\n"
            "author = Rin contributors\n",
            encoding="utf-8",
        )
        (world / "world.mt").write_text(
            "gameid = rin_test\n"
            "backend = sqlite3\n"
            "player_backend = sqlite3\n"
            "auth_backend = sqlite3\n"
            "mod_storage_backend = sqlite3\n"
            "creative_mode = true\n"
            "enable_damage = false\n",
            encoding="utf-8",
        )
        config = root / "luanti.conf"
        config.write_text(
            f"secure.http_mods = {mod_name}\n"
            f"{mod_name}.lifecycle_test = true\n"
            "server_announce = false\n"
            "enable_ipv6 = false\n"
            "bind_address = 127.0.0.1\n",
            encoding="utf-8",
        )
        environment = os.environ.copy()
        environment["LUANTI_USER_PATH"] = str(user)
        command = [
            executable,
            "--server",
            "--world",
            str(world),
            "--gameid",
            "rin_test",
            "--config",
            str(config),
            "--port",
            "0",
            "--color",
            "never",
            "--log-timestamp",
            "none",
            "--logfile",
            "",
        ]
        first = epoch(run(command, environment))
        second = epoch(run(command, environment))
        if (
            second["world_id"] != first["world_id"]
            or second["world"] != first["world"]
            or second["host"] <= first["host"]
            or second["timeline"] <= first["timeline"]
        ):
            raise SystemExit(
                "Luanti restart did not preserve World identity and advance "
                f"Host/Timeline: first={first!r}, second={second!r}"
            )
    print(f"Luanti reference verified with {VERSION}")


if __name__ == "__main__":
    main()
