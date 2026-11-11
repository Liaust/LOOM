#!/usr/bin/env python3
"""Capture an explicit vault and optionally fenced native Workspace state."""
import argparse
from contextlib import closing
import datetime as dt
import fcntl
import hashlib
import http.client
import json
from itertools import chain
import os
from pathlib import Path
import signal
import shutil
import socket
import sqlite3
import stat
import sys
import tarfile
import tempfile
import time

DEFAULT_MAX_BYTES = 16 * 1024 * 1024 * 1024
DEFAULT_TIMEOUT = 900
MAX_NATIVE_ENTRIES = 200000
NATIVE_PATHS = ("native-client/working-set", "native-client/local-recovery", "drafts.json", "roots.json", "boxes.json", "native-client/working-set-location.json")


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


def exact_root(value):
    root = Path(value).expanduser()
    if not root.is_absolute() or not root.is_dir() or any(p.is_symlink() for p in (root, *root.parents)):
        raise ValueError("invalid explicit client root")
    return root


def vault_checkpoint(root, max_bytes):
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
    return before


def native_working_root(root, mapped=True):
    locator = root / "native-client/working-set-location.json"
    if not mapped or not locator.exists():
        return root / NATIVE_PATHS[0]
    if locator.is_symlink() or locator.stat().st_size > 8192:
        raise ValueError("unsafe replica root locator")
    value = json.loads(locator.read_bytes())
    target = Path(value["path"])
    if (value.get("schema") != "loom.working_root.v1" or value.get("state") != "ready"
            or value.get("source_root") != str(root) or not target.is_absolute()
            or target.resolve() != target or not target.is_dir()):
        raise ValueError("replica root handover incomplete")
    info = target.stat()
    if value.get("identity") != f"{info.st_dev}:{info.st_ino}":
        raise ValueError("replica root identity differs")
    return target


def native_source(root, relative, mapped=True):
    prefix = NATIVE_PATHS[0]
    if relative == prefix or relative.startswith(prefix + "/"):
        return native_working_root(root, mapped) / relative[len(prefix):].lstrip("/")
    return root / relative


def native_inventory(root, max_bytes, mapped=True):
    result, total = {}, 0
    for name in NATIVE_PATHS:
        target = native_source(root, name, mapped)
        for parent in (target, *target.parents):
            if parent == root:
                break
            if parent.is_symlink():
                raise ValueError("native client symlink")
        if not target.exists():
            continue
        if target.is_dir() != (name in NATIVE_PATHS[:2]):
            raise ValueError("native capture path has wrong entry type")
        paths = chain((target,), target.rglob("*")) if target.is_dir() else (target,)
        for path in paths:
            info = path.lstat()
            relative = name + ("/" + path.relative_to(target).as_posix() if path != target else "")
            if len(result) >= MAX_NATIVE_ENTRIES or not (stat.S_ISREG(info.st_mode) or stat.S_ISDIR(info.st_mode)):
                raise ValueError("native entry type or count")
            if stat.S_ISDIR(info.st_mode):
                result[relative] = ("directory", stat.S_IMODE(info.st_mode))
                continue
            total += info.st_size
            if total > max_bytes:
                raise ValueError("native capture exceeds configured byte limit")
            with path.open("rb") as stream:
                h = hashlib.sha256()
                for data in iter(lambda: stream.read(65536), b""):
                    h.update(data)
            result[relative] = ("file", info.st_size, h.hexdigest(), stat.S_IMODE(info.st_mode))
    return result, total


def check_sqlite(path, deadline):
    with closing(sqlite3.connect(path.as_uri() + "?mode=ro", uri=True, timeout=max(0, min(5, deadline - time.monotonic())))) as database:
        database.set_progress_handler(lambda: int(time.monotonic() >= deadline), 1000)
        if database.execute("PRAGMA integrity_check").fetchall() != [("ok",)]:
            raise ValueError("native SQLite integrity check failed")


def stopped_native(root, stage, max_bytes, deadline):
    before, size = native_inventory(root, max_bytes)
    database = root / "native-client/notes.sqlite"
    if any(p.is_symlink() for p in (database, database.parent)) or not database.is_file():
        raise ValueError("native SQLite database unavailable")
    target = stage / "native-client/notes.sqlite"
    target.parent.mkdir(mode=0o700, parents=True)
    remaining = max_bytes - size

    def progress(*_):
        if time.monotonic() >= deadline:
            raise TimeoutError("native SQLite backup timed out")
        if target.stat().st_size > remaining:
            raise ValueError("native database exceeds configured byte limit")

    # Read committed WAL content through SQLite; never copy live WAL/SHM files.
    with closing(sqlite3.connect(database.as_uri() + "?mode=ro", uri=True, timeout=max(0, min(5, deadline - time.monotonic())))) as source:
        pages = source.execute("PRAGMA page_count").fetchone()[0]
        page_size = source.execute("PRAGMA page_size").fetchone()[0]
        if pages * page_size > remaining:
            raise ValueError("native database exceeds configured byte limit")
        with closing(sqlite3.connect(target)) as destination:
            source.backup(destination, pages=128, progress=progress)
            destination.execute("PRAGMA journal_mode=DELETE")
    check_sqlite(target, deadline)
    for relative, record in before.items():
        destination = stage / relative
        if record[0] == "directory":
            destination.mkdir(mode=record[1], parents=True, exist_ok=True)
        else:
            destination.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
            written = 0
            with native_source(root, relative).open("rb") as source, destination.open("xb") as output:
                while data := source.read(65536):
                    written += len(data)
                    if written > record[1]:
                        raise ValueError("native file grew during capture")
                    output.write(data)
            shutil.copystat(native_source(root, relative), destination, follow_symlinks=False)
    if before != native_inventory(root, max_bytes)[0] or before != native_inventory(stage, max_bytes, mapped=False)[0]:
        raise ValueError("native files changed during capture")
    files = inventory(stage, max_bytes)
    receipt = {"schema": "loom.workspace_client_recovery.v1", "captured_at": dt.datetime.now(dt.timezone.utc).isoformat(),
               "mode": "stopped", "cache_included": False, "source_root": str(root),
               "files": [{"path": name, "size": size, "sha256": sha} for name, (size, sha) in files.items()],
               "directories": sorted(path.relative_to(stage).as_posix() for path in stage.rglob("*") if path.is_dir())}
    (stage / "capture.json").write_text(json.dumps(receipt) + "\n")


def native_member(name, directory):
    if not name or name.startswith("/") or "\\" in name or len(name) > 4096:
        return False
    parts = name.rstrip("/").split("/")
    if any(part in ("", ".", "..") for part in parts):
        return False
    relative = "/".join(parts)
    if directory and relative in ("workspace", "workspace/native-client"):
        return True
    if relative in ("workspace/native-client/notes.sqlite", "workspace/capture.json", *("workspace/" + name for name in NATIVE_PATHS[2:])):
        return not directory
    return any(relative == "workspace/" + prefix and directory or relative.startswith("workspace/" + prefix + "/")
               for prefix in NATIVE_PATHS[:2])


def running_native(root, stage, max_bytes, timeout):
    deadline = time.monotonic() + timeout
    endpoint = root / "native-recovery.sock"
    info = endpoint.lstat()
    if not stat.S_ISSOCK(info.st_mode) or info.st_uid != os.geteuid():
        raise ValueError("native capture socket unavailable")
    connection = http.client.HTTPConnection("localhost", timeout=timeout)
    try:
        connection.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        connection.sock.settimeout(timeout)
        connection.sock.connect(str(endpoint))
        body = json.dumps({"max_bytes": max_bytes, "timeout_seconds": timeout})
        connection.request("POST", "/capture", body, {"Content-Type": "application/json"})
        response = connection.getresponse()
        if response.status != 200 or response.getheader("Content-Type", "").split(";")[0] != "application/gzip":
            raise ValueError("native owner refused capture")
        # A response is bounded both on the wire and after gzip expansion.
        with tempfile.TemporaryFile() as received:
            wire = 0
            wire_limit = max_bytes + max_bytes // 100 + MAX_NATIVE_ENTRIES * 1024 + 1048576
            while data := response.read(65536):
                wire += len(data)
                if wire > wire_limit:
                    raise ValueError("native response exceeds byte limit")
                received.write(data)
            received.seek(0)
            seen, total = set(), 0
            with tarfile.open(fileobj=received, mode="r:gz") as archive:
                for entry in archive:
                    name = entry.name.rstrip("/")
                    if (name in seen or len(seen) >= MAX_NATIVE_ENTRIES or not (entry.isfile() or entry.isdir())
                            or not native_member(entry.name, entry.isdir()) or entry.size < 0):
                        raise ValueError("unsafe native capture member")
                    seen.add(name)
                    destination = stage.joinpath(*name.split("/")[1:])
                    if entry.isdir():
                        destination.mkdir(mode=0o700, parents=True, exist_ok=True)
                        continue
                    total += entry.size
                    if total > max_bytes:
                        raise ValueError("native capture exceeds byte limit")
                    destination.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
                    with archive.extractfile(entry) as source, destination.open("xb") as output:
                        shutil.copyfileobj(source, output, length=65536)
            if "workspace/native-client/notes.sqlite" not in seen:
                raise ValueError("native owner omitted SQLite backup")
    finally:
        connection.close()
    check_sqlite(stage / "native-client/notes.sqlite", deadline)
    verify_native_receipt(stage, max_bytes, "running")


def verify_native_receipt(stage, max_bytes, mode):
    receipt = stage / "capture.json"
    if not receipt.is_file() or receipt.stat().st_size > min(max_bytes, 32 * 1024 * 1024):
        raise ValueError("native capture receipt missing or oversized")
    value = json.loads(receipt.read_bytes())
    if value.get("schema") != "loom.workspace_client_recovery.v1" or value.get("mode") != mode:
        raise ValueError("native capture receipt schema or mode differs")
    declared = {}
    for item in value["files"]:
        name, size, sha = item["path"], item["size"], item["sha256"]
        if name in declared or name == "capture.json" or not isinstance(size, int) or isinstance(size, bool):
            raise ValueError("invalid native file inventory")
        declared[name] = (size, sha)
    actual = inventory(stage, max_bytes)
    actual.pop("capture.json")
    directories = sorted(path.relative_to(stage).as_posix() for path in stage.rglob("*") if path.is_dir())
    if declared != actual or sorted(value["directories"]) != directories:
        raise ValueError("native receipt differs from staged inventory")


def capture_native(root, stage, max_bytes, timeout):
    if root.stat().st_uid != os.geteuid():
        raise ValueError("native capture requires the client owner")
    lock = root / "native-recovery.lock"
    fd = os.open(lock, os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    try:
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or info.st_uid != os.geteuid():
            raise ValueError("unsafe native recovery lock")
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            # A live owner or another capture forbids blind filesystem fallback.
            running_native(root, stage, max_bytes, timeout)
        else:
            if os.path.lexists(root / "native-recovery.sock"):
                raise ValueError("native socket exists without its lifecycle lock; stopped capture refused")
            stopped_native(root, stage, max_bytes, time.monotonic() + timeout)
            if os.path.lexists(root / "native-recovery.sock"):
                raise ValueError("native socket appeared during stopped capture")
            verify_native_receipt(stage, max_bytes, "stopped")
        current = lock.lstat()
        if (current.st_dev, current.st_ino) != (info.st_dev, info.st_ino):
            raise ValueError("native lifecycle lock changed during capture")
    finally:
        os.close(fd)


def parse_args(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("vault", nargs="?")
    parser.add_argument("max_bytes", nargs="?", type=int, default=DEFAULT_MAX_BYTES)
    parser.add_argument("--workspace")
    parser.add_argument("--native", dest="native_workspace")
    parser.add_argument("--max-bytes", type=int, dest="limit")
    parser.add_argument("--timeout-seconds", type=int, default=DEFAULT_TIMEOUT)
    args = parser.parse_args(argv)
    if args.native_workspace:
        if args.workspace or args.max_bytes != DEFAULT_MAX_BYTES:
            parser.error("native-only mode cannot include a vault or workspace option")
        if args.vault is not None:
            args.max_bytes = int(args.vault)
        args.vault, args.workspace = None, args.native_workspace
    elif not args.vault:
        parser.error("an explicit vault or --native workspace is required")
    if args.limit is not None:
        args.max_bytes = args.limit
    if args.max_bytes <= 0 or args.timeout_seconds <= 0:
        parser.error("positive byte and time limits required")
    return args


def main():
    args = parse_args()
    root = exact_root(args.vault) if args.vault else None
    workspace = exact_root(args.workspace) if args.workspace else None
    if root and workspace and (root == workspace or root in workspace.parents or workspace in root.parents):
        raise ValueError("vault and native workspace must not overlap")

    def timed_out(*_):
        raise TimeoutError("client capture exceeded time limit")

    signal.signal(signal.SIGALRM, timed_out)
    signal.alarm(args.timeout_seconds)
    # Both modes spool and validate completely before emitting any archive bytes.
    with tempfile.TemporaryDirectory(prefix="loom-native-capture-") as name:
        stage = Path(name) / "workspace"
        # Reject an absent/invalid legacy vault before doing a native checkpoint
        # which would otherwise be discarded when the later vault check fails.
        before = vault_checkpoint(root, args.max_bytes) if root else None
        vault_bytes = sum(size for size, _ in before.values()) if before else 0
        native_bytes = 0
        if workspace:
            stage.mkdir(mode=0o700)
            capture_native(workspace, stage, args.max_bytes - vault_bytes, args.timeout_seconds)
            native_bytes = sum(size for size, _ in inventory(stage, args.max_bytes).values())
        if vault_bytes + native_bytes > args.max_bytes:
            raise ValueError("combined client capture exceeds configured byte limit")
        with tempfile.TemporaryFile() as output:
            with tarfile.open(fileobj=output, mode="w:gz") as archive:
                if root:
                    archive.add(root, arcname="vault", recursive=True)
                if workspace:
                    archive.add(stage, arcname="workspace", recursive=True)
            if root and before != inventory(root, args.max_bytes - native_bytes):
                raise ValueError("client changed during capture")
            output.seek(0)
            shutil.copyfileobj(output, sys.stdout.buffer, length=65536)
    signal.alarm(0)


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        print("Notes client capture unavailable: " + type(error).__name__, file=sys.stderr)
        sys.exit(1)
