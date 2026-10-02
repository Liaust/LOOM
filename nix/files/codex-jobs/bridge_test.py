import importlib.util
import json
import os
from pathlib import Path
import socket
import sys
import tempfile
import threading
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("bridge", Path(__file__).with_name("bridge.py"))
bridge = importlib.util.module_from_spec(spec)
spec.loader.exec_module(bridge)


class BridgeTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.projects = self.root / "Projects"
        self.project = self.projects / "owned"
        self.project.mkdir(parents=True)
        self.home = self.root / "agents"
        (self.home / ".codex").mkdir(parents=True)
        (self.home / ".codex/auth.json").write_text("{}")
        self.binary = self.root / "fake-codex"
        self.request = dict(project_root=str(self.project), prompt="read context", timeout_seconds=1)
        self.server, self.client = socket.socketpair()
        self.addCleanup(self.server.close)
        self.addCleanup(self.client.close)

    def fake(self, body):
        self.binary.write_text("#!" + sys.executable + "\n" + body)
        self.binary.chmod(0o700)

    def execute(self):
        return bridge.execute(self.request, self.server, self.projects, str(self.binary), str(self.home))

    def test_existing_home_fixed_flags_and_answer(self):
        self.fake('''import os, pathlib, sys
assert "--ignore-user-config" in sys.argv and "--ephemeral" in sys.argv
assert sys.argv[sys.argv.index("--sandbox") + 1] == "read-only"
assert pathlib.Path(os.environ["CODEX_HOME"]) == pathlib.Path(os.environ["HOME"]) / ".codex"
assert os.getcwd() == sys.argv[sys.argv.index("--cd") + 1]
assert sys.stdin.read() == "read context"
pathlib.Path(sys.argv[sys.argv.index("--output-last-message") + 1]).write_text("answer")
''')
        result = self.execute()
        self.assertTrue(result["ok"])
        self.assertEqual(result["executor_uid"], os.getuid())
        self.assertEqual(result["answer"], "answer")

    def test_timeout_and_disconnect_reap_child(self):
        self.fake('''import os, pathlib, time
pathlib.Path("pid").write_text(str(os.getpid()))
time.sleep(10)
''')
        result = self.execute()
        self.assertEqual(result["exit_code"], 124)
        pid = int((self.project / "pid").read_text())
        with self.assertRaises(ProcessLookupError):
            os.kill(pid, 0)
        timer = threading.Timer(0.2, self.client.close)
        timer.start()
        try:
            with self.assertRaisesRegex(ValueError, "disconnected"):
                self.execute()
        finally:
            timer.join()
        pid = int((self.project / "pid").read_text())
        with self.assertRaises(ProcessLookupError):
            os.kill(pid, 0)

    def test_bad_request_does_not_run(self):
        for field, value in (("project_root", str(self.root)), ("timeout_seconds", 3601),
                             ("sandbox", "danger-full-access"), ("sandbox", None),
                             ("timeout_seconds", True), ("prompt", ""), ("codex_home", "/other")):
            request = dict(self.request)
            request[field] = value
            with self.subTest(field=field, value=value), self.assertRaises(ValueError):
                bridge.validate(request, self.projects)
        link = self.projects / "escape"
        link.symlink_to(self.home)
        with self.assertRaises(ValueError):
            bridge.validate(dict(self.request, project_root=str(link)), self.projects)

    def test_workspace_write_and_longer_timeout_are_explicit(self):
        self.request.update(sandbox="workspace-write", timeout_seconds=1800)
        self.fake('''import pathlib, sys
assert sys.argv[sys.argv.index("--sandbox") + 1] == "workspace-write"
pathlib.Path("owned-output.txt").write_text("edited")
pathlib.Path(sys.argv[sys.argv.index("--output-last-message") + 1]).write_text("done")
''')
        result = self.execute()
        self.assertTrue(result["ok"])
        self.assertEqual(result["sandbox"], "workspace-write")
        self.assertEqual(result["timeout_seconds"], 1800)
        self.assertEqual((self.project / "owned-output.txt").read_text(), "edited")
        bridge.validate(dict(self.request, timeout_seconds=3600), self.projects)

    def test_no_login_and_failed_codex_are_not_success(self):
        self.fake("import sys\nsys.exit(7)\n")
        result = self.execute()
        self.assertFalse(result["ok"])
        self.assertEqual(result["exit_code"], 7)
        self.assertNotIn("answer", result)
        (self.home / ".codex/auth.json").unlink()
        with self.assertRaisesRegex(ValueError, "login unavailable"):
            self.execute()

    def test_message_bound(self):
        self.client.sendall(json.dumps(self.request).encode() + b"\n")
        self.assertEqual(bridge.receive(self.server, bridge.MAX_REQUEST), self.request)
        self.client.sendall(b"x" * 11)
        with self.assertRaisesRegex(ValueError, "too large"):
            bridge.receive(self.server, 10)

    def test_job_artifact_is_published_in_artifact_directory(self):
        artifacts = self.root / "artifacts"
        output = self.root / "output"
        artifacts.mkdir()
        output.mkdir()
        result_file = self.root / "result.json"
        with patch.dict(os.environ, LOOM_ARTIFACT_DIR=str(artifacts), LOOM_OUTPUT_DIR=str(output), LOOM_RESULT_FILE=str(result_file)):
            bridge.write_job_result(dict(ok=True, executor="agents", answer="marker"))
        result = json.loads(result_file.read_text())
        self.assertEqual(result["status"], "ok")
        self.assertEqual((artifacts / result["artifacts"][0]["path"]).read_text(), "marker")
        self.assertEqual(list(output.iterdir()), [])
        self.assertNotIn("answer", result["outputs"])


if __name__ == "__main__":
    unittest.main()
