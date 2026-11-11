import importlib.util
import json
from pathlib import Path
import sqlite3
import tempfile
import unittest
import zipfile

spec = importlib.util.spec_from_file_location("capture", Path(__file__).with_name("capture.py"))
capture = importlib.util.module_from_spec(spec)
spec.loader.exec_module(capture)


class CaptureTest(unittest.TestCase):
    def test_online_database_prefixes_and_credential_exclusions(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            home = root / "home"
            home.mkdir()
            (home / "sessions").mkdir()
            (home / "sessions/one.jsonl").write_text('{"complete":true}\n{"partial":')
            (home / "auth.json").write_text("secret must not be selected")
            (home / "config.toml").write_text("secret config must not be selected")
            (home / "logs_2.sqlite").write_text("excluded logs")
            (home / "packages").mkdir()
            with sqlite3.connect(home / "state_5.sqlite") as db:
                db.execute("PRAGMA journal_mode=WAL")
                db.execute("CREATE TABLE owned(value TEXT)")
                db.execute("INSERT INTO owned VALUES ('durable')")
                db.commit()
                result = capture.capture(home, root / "recovery")
            package = root / "recovery" / result["id"]
            manifest = json.loads((package / "manifest.json").read_text())
            self.assertEqual([e["path"] for e in manifest["entries"]], ["sessions/one.jsonl", "state_5.sqlite"])
            with zipfile.ZipFile(package / "codex.zip") as archive:
                self.assertEqual(archive.read("sessions/one.jsonl"), b'{"complete":true}\n')
                archive.extract("state_5.sqlite", root / "inspect")
            with sqlite3.connect(root / "inspect/state_5.sqlite") as db:
                self.assertEqual(db.execute("SELECT value FROM owned").fetchone()[0], "durable")
            self.assertEqual((package / "codex.zip").stat().st_mode & 0o777, 0o440)
            self.assertTrue((home / "auth.json").exists())

    def test_selected_symlink_is_refused(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            home = root / "home"
            home.mkdir()
            source = root / "other"
            source.write_text("must not follow")
            (home / "installation_id").symlink_to(source)
            with self.assertRaises(OSError):
                capture.capture(home, root / "recovery")


if __name__ == "__main__":
    unittest.main()
