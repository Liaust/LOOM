"""Small native SQLite contracts; run using the pinned Hermes Python env."""

import importlib.util
import os
from pathlib import Path
import sqlite3
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("capture", Path(__file__).with_name("native_snapshot.py"))
capture = importlib.util.module_from_spec(spec)
spec.loader.exec_module(capture)


class NativeSnapshot(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="loom-native-capture-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve() / ".loom-acceptance"
        self.root.mkdir(mode=0o700)
        self.source = self.root / "source.db"
        self.target = self.root / "captured.db"
        self.target.touch(mode=0o600)

    def database(self, wal=False):
        db = sqlite3.connect(self.source)
        self.addCleanup(db.close)
        if wal:
            db.execute("pragma journal_mode=wal")
        db.execute("create table messages (id integer primary key, text text)")
        db.execute("insert into messages values (1, 'committed message')")
        db.commit()
        return db

    def invoke(self):
        parent = os.open(self.root, os.O_RDONLY | os.O_DIRECTORY)
        source = os.open(self.source, os.O_RDONLY | os.O_NOFOLLOW)
        try:
            capture.snapshot(self.source, self.target, parent, source, parent)
        finally:
            os.close(source)
            os.close(parent)

    def test_wal_snapshot_and_private_finalization(self):
        live = self.database(wal=True)
        self.invoke()
        live.execute("insert into messages values (2, 'after snapshot')")
        live.commit()
        with sqlite3.connect(self.target) as restored:
            self.assertEqual(restored.execute("select * from messages").fetchall(), [(1, "committed message")])
            self.assertEqual(restored.execute("pragma integrity_check").fetchone(), ("ok",))
            self.assertEqual(restored.execute("pragma journal_mode").fetchone(), ("delete",))
        restored.close()
        self.assertEqual(live.execute("pragma journal_mode").fetchone(), ("wal",))
        self.assertFalse(Path(str(self.target) + "-wal").exists())
        self.assertFalse(Path(str(self.target) + "-shm").exists())

    def test_reserved_uri_characters_bind_exact_source(self):
        self.source = self.root / "source?#%.db"
        self.database(wal=True)
        self.invoke()
        self.assertGreater(self.target.stat().st_size, 0)

    def test_busy_database_refuses_without_raw_copy(self):
        db = self.database()
        db.execute("begin exclusive")
        from hermes_cli.backup import _safe_copy_db
        with patch("hermes_cli.backup._safe_copy_db", side_effect=lambda src, dst: _safe_copy_db(src, dst, timeout_seconds=0.01)):
            with self.assertRaises(ValueError):
                self.invoke()
        self.assertFalse(self.target.exists())
        db.rollback()

    def test_empty_and_invalid_databases_refuse(self):
        for content in (b"", b"not a SQLite database"):
            with self.subTest(content=bool(content)):
                self.source.write_bytes(content)
                self.target.touch(mode=0o600)
                with self.assertRaises(ValueError):
                    self.invoke()

    def test_output_symlink_and_hardlink_refuse(self):
        self.database()
        outside = self.root / "outside"
        outside.write_bytes(b"unchanged")
        self.target.unlink()
        self.target.symlink_to(outside)
        with self.assertRaises(ValueError):
            self.invoke()
        self.target.unlink()
        os.link(outside, self.target)
        with self.assertRaises(ValueError):
            self.invoke()
        self.assertEqual(outside.read_bytes(), b"unchanged")

    def test_source_descriptor_substitution_refuses(self):
        self.database()
        parent = os.open(self.root, os.O_RDONLY | os.O_DIRECTORY)
        source = os.open(self.source, os.O_RDONLY | os.O_NOFOLLOW)
        try:
            self.source.rename(self.root / "retained.db")
            self.source.write_bytes(b"replacement")
            with self.assertRaises(ValueError):
                capture.snapshot(self.source, self.target, parent, source, parent)
        finally:
            os.close(source)
            os.close(parent)
        self.assertEqual(self.target.read_bytes(), b"")


if __name__ == "__main__":
    unittest.main()
