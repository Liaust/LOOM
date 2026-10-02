import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("notes_recovery", ROOT / "scripts/loom-notes-recovery.py")
recovery = importlib.util.module_from_spec(spec)
spec.loader.exec_module(recovery)


class NotesRecoveryTest(unittest.TestCase):
    def test_backup_requires_successful_matching_commit(self):
        value = {"ok": True, "data": {"worker": {"last_run": {"run_status": "succeeded", "worker_run_id": "now"},
                 "latest_checkpoint": {"updated_by_run_id": "now", "checkpoint_json": {"committed": True}}}}}
        self.assertTrue(recovery.verify_backup_result(value)["committed"])
        value["data"]["worker"]["last_run"]["run_status"] = "failed"
        with self.assertRaises(ValueError):
            recovery.verify_backup_result(value)
        value["data"]["worker"]["last_run"]["run_status"] = "succeeded"
        value["data"]["worker"]["latest_checkpoint"]["updated_by_run_id"] = "old"
        with self.assertRaises(ValueError):
            recovery.verify_backup_result(value)

    def test_client_capture_requires_export_and_preserves_variants(self):
        import io
        import tarfile
        with tempfile.TemporaryDirectory() as name:
            root = Path(name).resolve()
            (root / "local-variant.md").write_text("unsent local edit")
            command = [sys.executable, str(ROOT / "scripts/loom-notes-client-snapshot.py"), str(root)]
            self.assertNotEqual(subprocess.run(command, capture_output=True).returncode, 0)
            export = root / ".obsidian/plugins/obsidian-livesync/loom-intent-backup-0.json"
            export.parent.mkdir(parents=True)
            export.write_text(json.dumps({"schema": "loom.client_recovery.v1", "vaultId": "one", "capturedAt": "now", "state": {"version": 1}}))
            result = subprocess.run(command, capture_output=True, check=True)
            with tarfile.open(fileobj=io.BytesIO(result.stdout)) as archive:
                self.assertEqual(archive.extractfile("vault/local-variant.md").read(), b"unsent local edit")
            refused = subprocess.run([*command, "1"], capture_output=True)
            self.assertNotEqual(refused.returncode, 0)
            self.assertEqual(refused.stdout, b"")
            self.assertNotEqual(subprocess.run([*command, "0"], capture_output=True).returncode, 0)
            # Sparse fixture crosses the old pilot limit without allocating a
            # second large vault or buffering uncompressed contents in memory.
            with (root / "attachment.bin").open("wb") as stream:
                stream.truncate(257 * 1024 * 1024)
            with tempfile.TemporaryFile() as output:
                subprocess.run(command, stdout=output, stderr=subprocess.PIPE, check=True)
                output.seek(0)
                with tarfile.open(fileobj=output) as archive:
                    self.assertEqual(archive.getmember("vault/attachment.bin").size, 257 * 1024 * 1024)
            (root / "external").symlink_to(root.parent)
            self.assertNotEqual(subprocess.run(command, capture_output=True).returncode, 0)


if __name__ == "__main__":
    unittest.main()
