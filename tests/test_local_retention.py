import importlib.util
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("local_retention", ROOT / "scripts/loom-local-retention.py")
retention = importlib.util.module_from_spec(spec)
spec.loader.exec_module(retention)


class LocalRetentionTest(unittest.TestCase):
    def fixture(self, root, mode="plan"):
        base = root / "backups"
        base.mkdir()
        request = base / "local-retention-request.json"
        request.write_text(json.dumps({"schema": "loom.local_retention.request.v1", "worker_run_id": "completed",
                                       "archive": "history-exact", "manifest_sha256": "a" * 64}))
        request.chmod(0o644)
        return {"data_dir": str(root), "loom": "/fixed/loom", "main_config": "/fixed/loom.env",
                "cloud_config": "/fixed/cloud.json", "mode": mode}, request

    def test_plan_only_is_coalesced_and_never_applies(self):
        with tempfile.TemporaryDirectory() as name:
            cfg, request = self.fixture(Path(name))
            def run(command, **kwargs):
                self.assertIn("plan", command)
                self.assertNotIn("apply", command)
                return SimpleNamespace(returncode=0, stdout=b'{"plan_hash":"review-current"}')
            with patch.object(retention.subprocess, "run", side_effect=run) as process:
                self.assertEqual(retention.execute(cfg)["status"], "planned")
                self.assertEqual(process.call_count, 2)
                self.assertEqual(retention.execute(cfg)["status"], "already_processed")
                self.assertEqual(process.call_count, 2)
            self.assertTrue(request.exists())

    def test_busy_does_not_acknowledge_and_newer_request_is_preserved(self):
        with tempfile.TemporaryDirectory() as name:
            cfg, request = self.fixture(Path(name))
            with patch.object(retention.subprocess, "run", return_value=SimpleNamespace(returncode=1)) as process:
                self.assertEqual(retention.execute(cfg)["status"], "deferred")
                self.assertEqual(process.call_count, 1)
                self.assertFalse((request.parent / "local-retention-ack.json").exists())
            def run(command, **kwargs):
                payload = json.loads(request.read_text())
                payload["worker_run_id"] += "newer"
                request.write_text(json.dumps(payload))
                return SimpleNamespace(returncode=0, stdout=b'{"plan_hash":"review-current"}')
            with patch.object(retention.subprocess, "run", side_effect=run):
                self.assertEqual(retention.execute(cfg)["status"], "newer_request_pending")
            self.assertFalse((request.parent / "local-retention-ack.json").exists())

    def test_apply_requires_policy_and_uses_fresh_scope_plans(self):
        with tempfile.TemporaryDirectory() as name:
            cfg, _ = self.fixture(Path(name), "apply")
            with patch.object(retention.subprocess, "run") as process:
                with self.assertRaisesRegex(ValueError, "approved policy"):
                    retention.execute(cfg)
                process.assert_not_called()
            cfg["policy_revision"] = "fixture-approved-policy"
            calls = []
            def run(command, **kwargs):
                scope = command[command.index("--scope") + 1]
                action = command[5]
                calls.append((action, scope))
                if action == "plan":
                    return SimpleNamespace(returncode=0, stdout=json.dumps({"plan_hash": scope + "-fresh"}).encode())
                self.assertEqual(command[command.index("--plan-hash") + 1], scope + "-fresh")
                self.assertIn("--yes", command)
                return SimpleNamespace(returncode=0, stdout=b'{"status":"applied"}')
            with patch.object(retention.subprocess, "run", side_effect=run):
                self.assertEqual(retention.execute(cfg)["status"], "applied")
            self.assertEqual(calls, [("plan", "notes"), ("apply", "notes"), ("plan", "operational"), ("apply", "operational")])
