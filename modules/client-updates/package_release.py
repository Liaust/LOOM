#!/usr/bin/env python3
"""Create a local code-only release. Never download, publish or install."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import stat
import tempfile

FILES = ("main.js", "manifest.json", "styles.css", "loom-client-build.json", "loom-release.json")
MAX_FILE = 16 * 1024 * 1024
MAX_TOTAL = 32 * 1024 * 1024
IDENTITY_KEYS = {"schema", "component", "adapter", "pluginId", "releaseId", "sequence", "version", "channel", "sourceCommit", "upstream", "compatibility"}
COMPATIBILITY = {"minAppVersion": "1.7.2", "platforms": ["desktop", "mobile"], "notesProtocol": 1, "handoffAPI": 1, "journalSchema": 1, "activation": "reload", "rollback": "same-schema"}


def json_bytes(value):
    return (json.dumps(value, indent=2) + "\n").encode()


def read_file(path, limit=MAX_FILE):
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(fd, "rb") as source:
        info = os.fstat(source.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or not 0 < info.st_size <= limit:
            raise ValueError("invalid package input: " + path.name)
        data = source.read(limit + 1)
        if len(data) > limit:
            raise ValueError("package input too large")
        return data


def package(source, destination):
    source, destination = Path(source), Path(destination)
    if source.is_symlink() or not source.is_dir() or destination.exists() or destination.is_symlink():
        raise ValueError("use an existing owned build and a new output directory")
    if any(p.name == "data.json" or p.name.startswith(("loom-intent-backup-", ".env")) for p in source.iterdir()):
        raise ValueError("private settings/recovery input is not a release")
    payloads = {name: read_file(source / name, 65536 if name.endswith(".json") else MAX_FILE) for name in FILES}
    if sum(map(len, payloads.values())) > MAX_TOTAL:
        raise ValueError("package total too large")
    receipt = json.loads(payloads["loom-client-build.json"])
    identity = json.loads(payloads["loom-release.json"])
    plugin = json.loads(payloads["manifest.json"])
    if set(identity) != IDENTITY_KEYS or identity["schema"] != "loom.client-bundle.v1":
        raise ValueError("unknown bundle identity")
    if (identity["component"], identity["adapter"], identity["pluginId"], identity["channel"]) != ("notes-workspace", "obsidian-plugin", "obsidian-livesync", "stable"):
        raise ValueError("unknown component/channel")
    if not re.fullmatch(r"(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)", identity["version"]):
        raise ValueError("stable semantic version required")
    if identity["releaseId"] != "client-notes-" + identity["version"] or type(identity["sequence"]) is not int or not 0 < identity["sequence"] <= 2**53 - 1:
        raise ValueError("stable release ID and positive sequence required")
    if not re.fullmatch(r"[a-f0-9]{40}", identity["sourceCommit"]):
        raise ValueError("exact source commit required")
    upstream = {"revision": "7b3b6ff854f2ff10452be3eaf8e0cd76ac65ac2b", "version": "1.0.32"}
    if identity["upstream"] != upstream or identity["compatibility"] != COMPATIBILITY or any(
        type(identity["compatibility"].get(key)) is not int for key in ("notesProtocol", "handoffAPI", "journalSchema")
    ):
        raise ValueError("unsupported upstream/compatibility")
    if plugin.get("id") != identity["pluginId"] or plugin.get("version") != upstream["version"] or plugin.get("minAppVersion") != COMPATIBILITY["minAppVersion"] or plugin.get("isDesktopOnly") is not False:
        raise ValueError("native manifest identity mismatch")
    expected = {name: hashlib.sha256(payloads[name]).hexdigest() for name in FILES if name != "loom-client-build.json"}
    if (set(receipt) != {"upstream", "patchManifestSha256", "node", "files", "release", "installed"} or
        receipt.get("upstream") != upstream["revision"] or
        not re.fullmatch(r"[a-f0-9]{64}", receipt.get("patchManifestSha256", "")) or
        not re.fullmatch(r"v\d+\.\d+\.\d+", receipt.get("node", "")) or
        receipt.get("files") != expected or receipt.get("installed") is not False or receipt.get("release") != identity):
        raise ValueError("build receipt/hash mismatch")
    release = {**identity, "schema": "loom.client-release.v1", "files": [
        {"name": name, "bytes": len(payloads[name]), "sha256": hashlib.sha256(payloads[name]).hexdigest()} for name in FILES
    ]}
    parent = destination.parent
    parent.mkdir(parents=True, exist_ok=True)
    temporary = Path(tempfile.mkdtemp(prefix=".loom-package-", dir=parent))
    try:
        for name, data in payloads.items():
            (temporary / name).write_bytes(data)
        (temporary / "release.json").write_bytes(json_bytes(release))
        license_path = Path(__file__).resolve().parent.parent / "notes-workspace-client/LICENSE.upstream"
        (temporary / "LICENSE.upstream").write_bytes(read_file(license_path, 65536))
        # A concurrent destination must never be merged or replaced.
        os.mkdir(destination)
        try:
            for item in temporary.iterdir():
                os.rename(item, destination / item.name)
        except BaseException:
            shutil.rmtree(destination)
            raise
    finally:
        shutil.rmtree(temporary)
    return release


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("build", type=Path)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    try:
        result = package(args.build, args.output)
    except (OSError, ValueError, KeyError, TypeError) as error:
        parser.exit(1, str(error) + "\n")
    print(result["releaseId"] + ": local package ready; nothing published or installed")
