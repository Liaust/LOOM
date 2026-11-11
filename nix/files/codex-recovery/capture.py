"""Capture selected private Codex history for the existing encrypted archive."""
import argparse
from contextlib import closing
from datetime import datetime, timezone
import fcntl
import hashlib
import json
import os
from pathlib import Path
import shutil
import sqlite3
import stat
import tempfile
import zipfile

DATABASES = ("state_5.sqlite", "thread_history_1.sqlite", "memories_1.sqlite", "goals_1.sqlite")
PLAIN = ("history.jsonl", "session_index.jsonl", "installation_id")
MAX_FILE = 512 * 1024 * 1024


def prefix_copy(source, destination):
    with os.fdopen(os.open(source, os.O_RDONLY | os.O_NOFOLLOW), "rb") as original:
        info = os.fstat(original.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or info.st_uid != os.getuid() or info.st_size > MAX_FILE:
            raise ValueError("unsafe selected capture file")
        remaining = info.st_size
        with destination.open("wb") as output:
            while remaining:
                block = original.read(min(1024 * 1024, remaining))
                if not block:
                    raise ValueError("selected source shortened during capture")
                output.write(block)
                remaining -= len(block)
    if source.suffix == ".jsonl":
        with destination.open("r+b") as output:
            output.seek(0, os.SEEK_END)
            end = output.tell()
            output.seek(max(0, end - 1024 * 1024))
            tail = output.read()
            complete = tail.rfind(b"\n")
            if end and complete < 0:
                raise ValueError("no bounded complete JSONL suffix")
            output.truncate(max(0, end - len(tail)) + complete + 1 if end else 0)


def capture(home, destination):
    home, destination = Path(home), Path(destination)
    if home.is_symlink() or destination.is_symlink():
        raise ValueError("capture roots must not be symlinks")
    home = home.resolve(strict=True)
    destination.mkdir(mode=0o750, parents=True, exist_ok=True)
    staging = destination / ".staging"
    staging.mkdir(mode=0o700, exist_ok=True)
    with (staging / "capture.lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        started = datetime.now(timezone.utc)
        name = "codex-" + started.strftime("%Y%m%d-%H%M%S-%f")
        with tempfile.TemporaryDirectory(prefix=name + "-", dir=staging) as temporary:
            temporary = Path(temporary)
            files = temporary / "files"
            files.mkdir()
            selected = list(PLAIN)
            for directory, suffix in (("sessions", ".jsonl"), ("loom-jobs", ".json")):
                base = home / directory
                if not base.exists():
                    continue
                for root, dirs, names in os.walk(base, followlinks=False):
                    if Path(root).is_symlink() or any((Path(root) / d).is_symlink() for d in dirs):
                        raise ValueError("selected capture directory is symlinked")
                    selected += [str((Path(root) / n).relative_to(home)) for n in sorted(names) if n.endswith(suffix)]
            for relative in sorted(selected):
                source = home / relative
                if not source.exists():
                    continue
                target = files / relative
                target.parent.mkdir(parents=True, exist_ok=True)
                prefix_copy(source, target)
            for relative in DATABASES:
                source = home / relative
                if not source.exists():
                    continue
                info = source.lstat()
                if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or info.st_uid != os.getuid() or info.st_size > MAX_FILE:
                    raise ValueError("unsafe selected database")
                with closing(sqlite3.connect(source.as_uri() + "?mode=ro", uri=True, timeout=30)) as original:
                    with closing(sqlite3.connect(files / relative)) as target:
                        original.backup(target)
                        if target.execute("PRAGMA quick_check").fetchone()[0] != "ok":
                            raise ValueError("captured database integrity failed")
                        target.execute("PRAGMA journal_mode=DELETE")
                if (files / relative).stat().st_size > MAX_FILE:
                    raise ValueError("captured database exceeds file bound")
            entries = []
            total = 0
            payload = temporary / "codex.zip"
            with zipfile.ZipFile(payload, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=1) as archive:
                for path in sorted(files.rglob("*")):
                    if not path.is_file():
                        continue
                    relative = str(path.relative_to(files))
                    total += path.stat().st_size
                    if len(entries) >= 16384 or total > 8 * 1024 * 1024 * 1024:
                        raise ValueError("selected capture exceeds bounded inventory")
                    with path.open("rb") as source:
                        digest = hashlib.file_digest(source, "sha256").hexdigest()
                    entries.append({"path": relative, "size": path.stat().st_size, "sha256": digest})
                    archive.write(path, relative)
            with payload.open("rb") as source:
                digest = hashlib.file_digest(source, "sha256").hexdigest()
            completed = datetime.now(timezone.utc)
            manifest = {"schema": "loom.codex.capture.v1", "id": name, "origin": str(home),
                        "capture_policy": "sqlite_online_and_complete_file_prefixes", "started_at": started.isoformat(),
                        "created_at": completed.isoformat(), "entries": entries,
                        "payload_size": payload.stat().st_size, "payload_sha256": digest}
            package = temporary / "package"
            package.mkdir(mode=0o750)
            shutil.move(payload, package / "codex.zip")
            (package / "manifest.json").write_text(json.dumps(manifest, sort_keys=True) + "\n")
            for path in package.iterdir():
                with path.open("rb") as source:
                    os.fsync(source.fileno())
                path.chmod(0o440)
            directory = os.open(package, os.O_RDONLY | os.O_DIRECTORY)
            try:
                os.fsync(directory)
            finally:
                os.close(directory)
            os.rename(package, destination / name)
            directory = os.open(destination, os.O_RDONLY | os.O_DIRECTORY)
            try:
                os.fsync(directory)
            finally:
                os.close(directory)
            return {"id": name, "entries": len(entries), "payload_bytes": manifest["payload_size"], "payload_sha256": digest}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--home", default="/home/agents/.codex")
    parser.add_argument("--destination", default="/srv/loom/agents/mina/recovery-codex")
    args = parser.parse_args()
    print(json.dumps(capture(args.home, args.destination)))
