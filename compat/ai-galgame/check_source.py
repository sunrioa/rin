#!/usr/bin/env python3
"""Verify Rin compatibility vectors against a local ai-galgame checkout."""

import argparse
import hashlib
import json
from pathlib import Path, PurePosixPath


def _read_json(path, maximum):
    payload = path.read_bytes()
    if len(payload) > maximum:
        raise ValueError("{} is too large".format(path.name))
    value = json.loads(payload.decode("utf-8"))
    if not isinstance(value, dict):
        raise ValueError("{} must contain a JSON object".format(path.name))
    return value


def _safe_relative(value):
    path = PurePosixPath(str(value))
    if path.is_absolute() or ".." in path.parts or "\\" in str(value):
        raise ValueError("manifest contains an unsafe path")
    return path


def verify(game_root, vectors_path):
    game_root = Path(game_root).resolve()
    pack_root = game_root / "renpy_project" / "game" / "content" / "packs" / "unsent-letters-rebuild"
    manifest = _read_json(pack_root / "manifest.json", 1024 * 1024)
    vectors = _read_json(Path(vectors_path), 4 * 1024 * 1024)
    source = vectors.get("source", {})
    if not isinstance(source, dict):
        raise ValueError("vectors source must be an object")
    files = manifest.get("files", {})
    if (
        manifest.get("pack_id") != source.get("pack_id")
        or manifest.get("version") != source.get("version")
        or files != source.get("files")
    ):
        raise ValueError("vector metadata does not match the content-pack manifest")
    fingerprint_payload = json.dumps(
        {
            "pack_id": manifest["pack_id"],
            "version": manifest["version"],
            "files": files,
        },
        ensure_ascii=True,
        sort_keys=True,
        separators=(",", ":"),
    ).encode("utf-8")
    fingerprint = hashlib.sha256(fingerprint_payload).hexdigest()
    if fingerprint != source.get("fingerprint"):
        raise ValueError("vector fingerprint does not match the content-pack manifest")
    for relative, expected in files.items():
        path = pack_root.joinpath(*_safe_relative(relative).parts)
        if hashlib.sha256(path.read_bytes()).hexdigest() != expected:
            raise ValueError("content-pack file hash mismatch: {}".format(relative))
    return {
        "pack_id": manifest["pack_id"],
        "version": manifest["version"],
        "fingerprint": fingerprint,
        "files": len(files),
        "cases": len(vectors.get("cases", [])),
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--game-root", required=True)
    parser.add_argument(
        "--vectors",
        default=str(Path(__file__).with_name("vectors.json")),
    )
    args = parser.parse_args()
    summary = verify(args.game_root, args.vectors)
    print(json.dumps(summary, sort_keys=True, separators=(",", ":")))


if __name__ == "__main__":
    main()
