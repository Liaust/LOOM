#!/usr/bin/env python3
"""Snapshot one explicit vault, not a live Chromium/IndexedDB directory."""
import hashlib
import json
from pathlib import Path
import shutil
import sys
import tarfile
import tempfile

DEFAULT_MAX_BYTES = 16 * 1024 * 1024 * 1024


def inventory(root, max_bytes=DEFAULT_MAX_BYTES):
    result = {}
    total = 0
    for path in sorted(root.rglob("*")):
        if path.is_symlink():
            raise ValueError("client symlink")
        if path.is_dir():
            continue
        if not path.is_file():
            raise ValueError("client non-regular entry")
        size = path.stat().st_size
        total += size
        if total > max_bytes:
            raise ValueError("client capture exceeds configured byte limit")
        with path.open("rb") as stream:
            h = hashlib.sha256()
            for data in iter(lambda: stream.read(65536), b""):
                h.update(data)
        result[str(path.relative_to(root))] = (size, h.hexdigest())
    return result


def main():
    root = Path(sys.argv[1]).expanduser()
    max_bytes = int(sys.argv[2]) if len(sys.argv) > 2 else DEFAULT_MAX_BYTES
    if max_bytes <= 0:
        raise ValueError("client capture requires a positive byte limit")
    if not root.is_absolute() or not root.is_dir() or any(p.is_symlink() for p in (root, *root.parents)):
        raise ValueError("invalid explicit vault")
    before = inventory(root, max_bytes)
    exports = [name for name in before if name.endswith(("loom-intent-backup-0.json", "loom-intent-backup-1.json"))]
    if not exports:
        raise ValueError("client recovery export not installed")
    valid = []
    for name in exports:
        try:
            value = json.loads((root / name).read_text())
            if value["schema"] == "loom.client_recovery.v1" and value["state"]["version"] == 1 and value["vaultId"]:
                valid.append(value["capturedAt"])
        except (ValueError, KeyError):
            pass
    if not valid:
        raise ValueError("no complete client checkpoint")
    # Verify a disk-spooled archive before emitting any bytes. Vault size must
    # not become the process's memory requirement.
    with tempfile.TemporaryFile() as output:
        with tarfile.open(fileobj=output, mode="w:gz") as archive:
            archive.add(root, arcname="vault", recursive=True)
        if before != inventory(root, max_bytes):
            raise ValueError("client changed during capture")
        output.seek(0)
        shutil.copyfileobj(output, sys.stdout.buffer, length=65536)


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        print("Notes client capture unavailable: " + type(error).__name__, file=sys.stderr)
        sys.exit(1)
