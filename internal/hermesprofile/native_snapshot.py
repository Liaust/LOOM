"""LOOM-only descriptor-bound adapter to the pinned Hermes SQLite backup API."""

import os
from pathlib import Path
import stat
import sys
import sqlite3
from types import SimpleNamespace
from urllib.parse import quote


def identity(value):
    return (value.st_dev, value.st_ino, value.st_mode, value.st_uid, value.st_nlink)


def bound(path, parent_fd, file_fd=None):
    parent = os.stat(path.parent, follow_symlinks=False)
    held_parent = os.fstat(parent_fd)
    if not stat.S_ISDIR(parent.st_mode) or identity(parent) != identity(held_parent):
        raise ValueError("parent binding")
    named = os.stat(path.name, dir_fd=parent_fd, follow_symlinks=False)
    if not stat.S_ISREG(named.st_mode) or named.st_nlink != 1 or named.st_mode & 0o7000:
        raise ValueError("file type")
    if file_fd is not None and identity(named) != identity(os.fstat(file_fd)):
        raise ValueError("file binding")
    return identity(named)


def snapshot(source, target, source_parent=3, source_fd=4, target_parent=5):
    # FDs 3/4/5 are the held source parent/source file/destination parent.
    # Output already exists as a new empty inode owned by the Go capture.
    source_id = bound(source, source_parent, source_fd)
    if os.fstat(source_fd).st_size <= 0:
        raise ValueError("empty live database")
    target_id = bound(target, target_parent)
    fd = os.open(target.name, os.O_RDWR | os.O_NOFOLLOW, dir_fd=target_parent)
    try:
        if identity(os.fstat(fd)) != target_id or os.fstat(fd).st_size != 0:
            raise ValueError("output binding")
        from hermes_cli.backup import _safe_copy_db

        # _safe_copy_db constructs a SQLite file: URI. Quote reserved path
        # characters without altering the real path used for descriptor checks.
        if not _safe_copy_db(quote(str(source), safe="/"), target):
            raise ValueError("native snapshot failed")
        # Only the private copy is finalized without WAL sidecars. The live
        # database, its journal mode and writers are never changed.
        captured = sqlite3.connect(str(target))
        try:
            if captured.execute("PRAGMA journal_mode=DELETE").fetchone()[0] != "delete":
                raise ValueError("snapshot finalization")
            if captured.execute("PRAGMA quick_check").fetchall() != [("ok",)]:
                raise ValueError("snapshot integrity")
        finally:
            captured.close()
        if bound(source, source_parent, source_fd) != source_id or bound(target, target_parent, fd) != target_id:
            raise ValueError("snapshot binding changed")
        os.fsync(fd)
    finally:
        os.close(fd)


def backup(profile, output):
    if str(profile) != os.environ.get("HERMES_HOME") or profile.name != ".capture" or output.parent != profile.parent or output.name != "profile.zip":
        raise ValueError("private capture paths")
    from hermes_cli.backup import run_backup
    import zipfile

    # Same backup-only timestamp contract as the packaged Hermes CLI. Calling
    # its native entry point avoids unrelated CLI initialization, update
    # checks, credential loading and log creation. No harness code is patched.
    original = zipfile.ZipFile.__init__
    def bounded_timestamps(self, file, mode="r", *args, **kwargs):
        if mode in ("w", "x", "a"):
            kwargs.setdefault("strict_timestamps", False)
        return original(self, file, mode, *args, **kwargs)
    zipfile.ZipFile.__init__ = bounded_timestamps
    try:
        run_backup(SimpleNamespace(output=str(output)))
    finally:
        zipfile.ZipFile.__init__ = original


if __name__ == "__main__":
    try:
        from hermes_cli import __version__, __release_date__
        if (__version__, __release_date__) != ("0.21.0", "2026.8.31"):
            raise ValueError("pinned package version")
        if len(sys.argv) != 4:
            raise ValueError("arguments")
        if sys.argv[1] == "snapshot":
            snapshot(Path(sys.argv[2]), Path(sys.argv[3]))
        elif sys.argv[1] == "backup":
            backup(Path(sys.argv[2]), Path(sys.argv[3]))
        else:
            raise ValueError("operation")
    except Exception:
        # Neither sqlite exceptions nor profile paths may enter service logs.
        sys.exit(1)
