import copy
import importlib.machinery
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch


SOURCE = Path(__file__).resolve().parents[1] / "scripts" / "loom-provenance-review-gate"
loader = importlib.machinery.SourceFileLoader("review_gate", str(SOURCE))
spec = importlib.util.spec_from_loader(loader.name, loader)
gate = importlib.util.module_from_spec(spec)
loader.exec_module(gate)


def response(items=None):
    return {"ok": True, "data": {"schema_version": "1.0", "as_of": "2026-10-12T10:00:00Z",
                                 "items": items or [], "truncated": False}}


def item(reason="unreviewed_pending"):
    return {"object_kind": "candidate", "object_id": "candidate-real", "change_token": "a" * 32,
            "effective_state": "pending", "review_reason": reason, "changed_at": "2026-10-12T09:00:00Z",
            "domain": "project.real", "visibility": "private"}


class ReviewGateTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        self.prompt = self.root / "prompt.md"
        self.prompt.write_text("Review exact sources.\n")
        self.cursor = self.root / "private-cursor.json"
        self.cursor.write_text('{"legacy_candidate_cursor":"preserve"}\n')
        self.env = patch.dict(os.environ, {"LOOM_RESULT_FILE": str(self.root / "result.json"),
                                          "LOOM_ARTIFACT_DIR": str(self.root), "LOOM_TEMP_DIR": str(self.root)})
        self.env.start()
        self.addCleanup(self.env.stop)

    def result(self):
        return json.loads((self.root / "result.json").read_text())

    def execute(self, envelope, native=None, exit_code=0):
        calls = []

        def command(args, **kwargs):
            calls.append(args)
            if len(calls) == 1:
                kwargs["stdout"].write(json.dumps(envelope).encode())
                return subprocess.CompletedProcess(args, 0)
            self.assertIn("loom-codex-exec", args[0])
            prompt = Path(args[args.index("--prompt-file") + 1]).read_text()
            self.assertIn('"object_id":"candidate-real"', prompt)
            gate.atomic_json(self.root / "result.json", native)
            return subprocess.CompletedProcess(args, exit_code)

        with patch.object(gate.subprocess, "run", side_effect=command):
            code = gate.run(self.prompt)
        self.assertEqual(self.cursor.read_text(), '{"legacy_candidate_cursor":"preserve"}\n')
        return code, calls

    def test_empty_effective_read_starts_no_agent(self):
        code, calls = self.execute(response())
        self.assertEqual(code, 0)
        self.assertEqual(len(calls), 1)
        self.assertIn("--exclude-domain", calls[0])
        self.assertIn(gate.FIXTURE_DOMAINS[0], calls[0])
        self.assertEqual(self.result()["outputs"]["review_status"], "no_work")
        self.assertFalse(self.result()["outputs"]["agent_started"])
        self.assertNotIn("thread_id", self.result()["outputs"])
        self.assertEqual(self.result()["artifacts"][0]["key"], "provenance_review")

    def test_selected_work_preserves_native_result(self):
        native = {"status": "completed", "outputs": {"protocol_version": 2, "thread_id": "saved-mina",
                                                     "outcome": "completed", "mode": "mina"},
                  "artifacts": [{"key": "codex_run", "path": "codex-run.json", "type": "file"}]}
        for reason in ("unreviewed_pending", "changed_evidence", "deferral_due"):
            with self.subTest(reason=reason):
                code, calls = self.execute(response([item(reason)]), copy.deepcopy(native))
                self.assertEqual(code, 0)
                self.assertEqual(len(calls), 2)
                self.assertEqual(self.result()["outputs"]["thread_id"], "saved-mina")
                self.assertEqual(self.result()["outputs"]["selected_count"], 1)
                self.assertEqual(len(self.result()["artifacts"]), 3)
                self.assertTrue((self.root / "provenance-review-prompt.md").is_file())

    def test_failed_native_turn_does_not_consume_checkpoints(self):
        native = {"status": "failed", "outputs": {"protocol_version": 2, "thread_id": "failed-mina",
                                                  "outcome": "failed"}, "artifacts": []}
        code, calls = self.execute(response([item()]), native, exit_code=1)
        self.assertEqual(code, 1)
        self.assertEqual(len(calls), 2)
        self.assertEqual(self.result()["status"], "failed")
        self.assertEqual(self.result()["outputs"]["thread_id"], "failed-mina")

    def test_failed_or_incomplete_read_is_never_no_work(self):
        invalid = [None, {"ok": False}, {"ok": True}, {"ok": False, "error": None}, response()]
        invalid[-1]["data"]["truncated"] = True
        incomplete_page = response([item()])
        incomplete_page["data"].update(truncated=True, next_cursor="returned-cursor")
        invalid.append(incomplete_page)
        for envelope in invalid:
            with self.subTest(envelope=envelope):
                code, calls = self.execute(envelope)
                self.assertEqual(code, 1)
                self.assertEqual(len(calls), 1)
                self.assertEqual(self.result()["outputs"]["review_status"], "gate_failed")

    def test_nonempty_truncated_page_is_bounded_useful_work(self):
        items = [item()]
        for index in range(1, gate.LIMIT):
            entry = item()
            entry["object_id"] = "candidate-real-" + str(index)
            items.append(entry)
        envelope = response(items)
        envelope["data"].update(truncated=True, next_cursor="returned-cursor")
        native = {"status": "completed", "outputs": {"thread_id": "real"}, "artifacts": []}
        code, calls = self.execute(envelope, native)
        self.assertEqual(code, 0)
        self.assertEqual(len(calls), 2)

    def test_fixture_duplicate_malformed_or_oversized_selection_is_rejected(self):
        fixture = item()
        fixture["domain"] = gate.FIXTURE_DOMAINS[0]
        bad_token = item()
        bad_token["change_token"] = ""
        for items in ([fixture], [item(), item()], [bad_token], [item()] * 11):
            with self.subTest(items=items):
                code, calls = self.execute(response(items))
                self.assertEqual(code, 1)
                self.assertEqual(len(calls), 1)

    def test_transport_failure_is_bounded_and_redacted(self):
        with patch.object(gate.subprocess, "run", side_effect=subprocess.TimeoutExpired("review", 30)):
            self.assertEqual(gate.run(self.prompt), 1)
        self.assertFalse(self.result()["outputs"]["agent_started"])
        self.assertEqual(self.result()["outputs"]["failure_kind"], "TimeoutExpired")


if __name__ == "__main__":
    unittest.main()
