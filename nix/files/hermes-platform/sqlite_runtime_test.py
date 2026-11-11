"""Check the packaged interpreter against an owned database, never MINA's data."""
from contextlib import closing
from pathlib import Path
import sqlite3
import tempfile
import unittest

from hermes_cli.sqlite_runtime import is_sqlite_wal_reset_vulnerable
from hermes_state_wal import apply_wal_with_fallback


class SQLiteRuntimeTests(unittest.TestCase):
    def test_linked_library_has_wal_reset_fix(self):
        self.assertGreaterEqual(sqlite3.sqlite_version_info, (3, 51, 3))
        self.assertFalse(is_sqlite_wal_reset_vulnerable(sqlite3.sqlite_version_info))
        with closing(sqlite3.connect(":memory:")) as connection:
            self.assertTrue(connection.execute("select sqlite_source_id()").fetchone()[0])
            connection.execute("create virtual table search using fts5(body)")
            connection.execute("insert into search values ('owned SQLite acceptance')")
            self.assertEqual(connection.execute(
                "select count(*) from search where search match 'acceptance'"
            ).fetchone()[0], 1)

    def test_native_wal_settings_preserve_commits_across_connections(self):
        with tempfile.TemporaryDirectory(prefix="loom-hermes-sqlite-") as directory:
            path = Path(directory) / "owned.db"
            with closing(sqlite3.connect(path)) as writer, \
                    closing(sqlite3.connect(path)) as reader:
                self.assertEqual(apply_wal_with_fallback(writer, db_label="owned.db"), "wal")
                writer.execute("create table notes (body text)")
                writer.execute("insert into notes values ('before')")
                writer.commit()
                reader.execute("begin")
                self.assertEqual(reader.execute("select body from notes").fetchall(), [("before",)])
                writer.execute("insert into notes values ('after')")
                writer.commit()
                writer.execute("pragma wal_checkpoint(passive)")
                self.assertEqual(reader.execute("select body from notes").fetchall(), [("before",)])
                reader.rollback()
                self.assertEqual(reader.execute("select body from notes").fetchall(),
                                 [("before",), ("after",)])
                self.assertEqual(writer.execute("pragma journal_mode").fetchone()[0], "wal")
            with closing(sqlite3.connect(path)) as reopened:
                self.assertEqual(reopened.execute("select body from notes").fetchall(),
                                 [("before",), ("after",)])
                self.assertEqual(reopened.execute("pragma integrity_check").fetchall(), [("ok",)])


if __name__ == "__main__":
    unittest.main()
