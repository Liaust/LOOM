import contextlib
import fcntl
import hashlib
from http.server import BaseHTTPRequestHandler
import importlib.util
import io
import json
from pathlib import Path
import shlex
import socket
import socketserver
import sqlite3
import subprocess
import sys
import tarfile
import tempfile
import threading
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts/loom-notes-client-snapshot.py"


def load_script(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


snapshot = load_script("native_snapshot_test", SCRIPT)
recovery = load_script("native_recovery_test", ROOT / "scripts/loom-notes-recovery.py")


class NativeClientSnapshotTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="ncap-", dir="/tmp")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve()
        self.workspace = self.root / "userdata"
        (self.workspace / "native-client/working-set/box/empty").mkdir(parents=True)
        (self.workspace / "native-client/local-recovery").mkdir()
        (self.workspace / "native-client/working-set/box/note.md").write_text("unsent working bytes")
        (self.workspace / "native-client/local-recovery/original.txt").write_text("retained original")
        (self.workspace / "drafts.json").write_text('[{"text":"unsaved draft"}]')
        (self.workspace / "roots.json").write_text('[{"ref":"box"}]')
        (self.workspace / "boxes.json").write_text('[{"box_id":"box_company","box_key":"company","root_ref":"box:box_company"}]')
        (self.workspace / "native-client/working-set/boxes/company").mkdir(parents=True)
        (self.workspace / "native-client/working-set/boxes/company/note.md").write_text("Company pending bytes")
        (self.workspace / "file-cache").mkdir()
        (self.workspace / "file-cache/evictable.bin").write_bytes(b"do not capture")
        self.database = self.workspace / "native-client/notes.sqlite"
        self.db = sqlite3.connect(self.database)
        self.addCleanup(self.db.close)
        self.db.execute("PRAGMA journal_mode=WAL")
        self.db.executescript("CREATE TABLE operations(id TEXT,state TEXT,request TEXT);"
                              "CREATE TABLE continuous_selections(id TEXT,state TEXT);")
        self.db.execute("INSERT INTO operations VALUES('immutable-id','queued','exact pending request')")
        self.db.execute("INSERT INTO continuous_selections VALUES('selected-folder','paused')")
        self.db.commit()

    def capture(self, *args):
        return subprocess.run([sys.executable, str(SCRIPT), *map(str, args)], capture_output=True, timeout=10)

    def native(self, *args):
        return self.capture("--native", self.workspace, *args)

    def test_relocated_root_keeps_archive_namespace_and_pending_operations(self):
        old = self.workspace / "native-client/working-set"
        target = self.root / "loom-main"
        old.rename(target)
        info = target.stat()
        locator = {"schema": "loom.working_root.v1", "state": "ready", "source_root": str(self.workspace),
                   "path": str(target), "identity": f"{info.st_dev}:{info.st_ino}"}
        (self.workspace / "native-client/working-set-location.json").write_text(json.dumps(locator))
        old.write_text("moved; not a second writable tree")
        result = self.native()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.check_bundle(result.stdout)
        with tarfile.open(fileobj=io.BytesIO(result.stdout), mode="r:gz") as archive:
            self.assertEqual(archive.extractfile("workspace/native-client/working-set/box/note.md").read(), b"unsent working bytes")
        locator["identity"] = "0:0"
        (self.workspace / "native-client/working-set-location.json").write_text(json.dumps(locator))
        self.assertNotEqual(self.native().returncode, 0)

    def vault(self):
        vault = self.root / "vault"
        vault.mkdir()
        (vault / "old.md").write_text("old Obsidian bytes")
        (vault / "loom-intent-backup-0.json").write_text(json.dumps({
            "schema": "loom.client_recovery.v1", "state": {"version": 1}, "vaultId": "retained-vault", "capturedAt": "now"}))
        return vault

    def check_bundle(self, raw):
        with tarfile.open(fileobj=io.BytesIO(raw), mode="r:gz") as archive:
            names = archive.getnames()
            self.assertIn("workspace/native-client/working-set/box/empty", names)
            self.assertEqual(archive.extractfile("workspace/drafts.json").read(), b'[{"text":"unsaved draft"}]')
            self.assertEqual(archive.extractfile("workspace/native-client/local-recovery/original.txt").read(), b"retained original")
            catalog=json.load(archive.extractfile("workspace/boxes.json"))
            self.assertEqual(catalog[0]["root_ref"], "box:box_company")
            self.assertEqual(archive.extractfile("workspace/native-client/working-set/boxes/company/note.md").read(), b"Company pending bytes")
            database = self.root / "verified.sqlite"
            database.write_bytes(archive.extractfile("workspace/native-client/notes.sqlite").read())
        with contextlib.closing(sqlite3.connect(database)) as db:
            self.assertEqual(db.execute("PRAGMA integrity_check").fetchall(), [("ok",)])
            self.assertEqual(db.execute("SELECT * FROM operations").fetchall(), [("immutable-id", "queued", "exact pending request")])
            self.assertEqual(db.execute("SELECT * FROM continuous_selections").fetchall(), [("selected-folder", "paused")])
        self.assertFalse(any("file-cache" in name or name.endswith(("-wal", "-shm", ".sock", ".lock")) for name in names))

    def test_stopped_backup_reads_wal_and_preserves_unsent_state(self):
        self.assertTrue(Path(str(self.database) + "-wal").is_file())
        result = self.native()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.check_bundle(result.stdout)
        with tarfile.open(fileobj=io.BytesIO(result.stdout)) as archive:
            manifest = json.load(archive.extractfile("workspace/capture.json"))
            self.assertEqual(manifest["mode"], "stopped")
            self.assertTrue(any(entry["path"] == "native-client/notes.sqlite" for entry in manifest["files"]))
        self.assertEqual((self.workspace / "native-client/working-set/box/note.md").read_text(), "unsent working bytes")
        self.assertEqual(self.db.execute("SELECT state FROM operations").fetchone(), ("queued",))

    def test_composite_keeps_existing_vault_namespace_and_native_state(self):
        vault = self.vault()
        result = self.capture(vault, "--workspace", self.workspace)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.check_bundle(result.stdout)
        with tarfile.open(fileobj=io.BytesIO(result.stdout)) as archive:
            self.assertEqual(archive.extractfile("vault/old.md").read(), b"old Obsidian bytes")
            self.assertIn("vault/loom-intent-backup-0.json", archive.getnames())
        legacy = self.capture(vault)
        self.assertEqual(legacy.returncode, 0, legacy.stderr)
        with tarfile.open(fileobj=io.BytesIO(legacy.stdout)) as archive:
            self.assertFalse(any(name.startswith("workspace") for name in archive.getnames()))

    def test_combined_byte_limit_and_native_only_no_vault_requirement(self):
        native = self.native()
        self.assertEqual(native.returncode, 0, native.stderr)
        with tarfile.open(fileobj=io.BytesIO(native.stdout)) as archive:
            native_size = sum(entry.size for entry in archive if entry.isfile())
        vault = self.vault()
        (vault / "large.md").write_bytes(b"x" * native_size)
        result = self.capture(vault, native_size + 1024, "--workspace", self.workspace)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(result.stdout, b"")
        for bound in (0, 1):
            result = self.native("--max-bytes", bound)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(result.stdout, b"")

    def test_live_lock_without_socket_refuses_stopped_fallback(self):
        with (self.workspace / "native-recovery.lock").open("w") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX)
            result = self.native()
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(result.stdout, b"")
        self.assertEqual(self.native().returncode, 0)

    def test_free_lifecycle_lock_with_stale_socket_refuses_stopped_capture(self):
        endpoint = self.workspace / "native-recovery.sock"
        with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as stale:
            stale.bind(str(endpoint))
        result = self.native()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(result.stdout, b"")
        self.assertTrue(endpoint.is_socket())

    def test_stopped_lock_is_held_through_capture_and_released_on_error(self):
        def capture(root, stage, *_):
            with (root / "native-recovery.lock").open("r+") as other:
                with self.assertRaises(BlockingIOError):
                    fcntl.flock(other, fcntl.LOCK_EX | fcntl.LOCK_NB)
            raise ValueError("injected failure")

        with patch.object(snapshot, "stopped_native", side_effect=capture):
            with self.assertRaises(ValueError):
                snapshot.capture_native(self.workspace, self.root / "unused", 1024, 10)
        with (self.workspace / "native-recovery.lock").open("r+") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)

    def test_selected_symlink_is_refused_but_unrelated_profile_is_not_read(self):
        outside = self.root / "unrelated"
        outside.write_text("not a native working file")
        (self.workspace / "unrelated-profile").symlink_to(outside)
        self.assertEqual(self.native().returncode, 0)
        (self.workspace / "native-client/working-set/escape").symlink_to(outside)
        result = self.native()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(result.stdout, b"")

    @contextlib.contextmanager
    def owner(self, payload, status=200, delay=0):
        requests = []

        class Handler(BaseHTTPRequestHandler):
            def do_POST(handler):
                requests.append((handler.path, json.loads(handler.rfile.read(int(handler.headers["Content-Length"])))))
                if delay:
                    import time
                    time.sleep(delay)
                try:
                    handler.send_response(status)
                    handler.send_header("Content-Type", "application/gzip")
                    handler.send_header("Content-Length", str(len(payload)))
                    handler.end_headers()
                    handler.wfile.write(payload)
                except BrokenPipeError:
                    pass

            def log_message(*_):
                pass

        with (self.workspace / "native-recovery.lock").open("w") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX)
            with socketserver.UnixStreamServer(str(self.workspace / "native-recovery.sock"), Handler) as server:
                thread = threading.Thread(target=server.serve_forever, daemon=True)
                thread.start()
                try:
                    yield requests
                finally:
                    server.shutdown()
                    thread.join()

    def running_payload(self, raw, change=None, omit_receipt=False):
        result = io.BytesIO()
        with tarfile.open(fileobj=io.BytesIO(raw)) as source, tarfile.open(fileobj=result, mode="w:gz") as target:
            for entry in source:
                data = source.extractfile(entry).read() if entry.isfile() else None
                if entry.name == "workspace/capture.json":
                    if omit_receipt:
                        continue
                    value = json.loads(data)
                    value["mode"] = "running"
                    if change:
                        change(value)
                    data = json.dumps(value).encode()
                    entry.size = len(data)
                target.addfile(entry, io.BytesIO(data) if data is not None else None)
        return result.getvalue()

    def test_live_capture_delegates_exact_socket_protocol_without_reading_live_files(self):
        payload = self.running_payload(self.native().stdout)
        self.db.close()
        self.database.unlink()
        with self.owner(payload) as requests:
            result = self.native("--max-bytes", 1048576, "--timeout-seconds", 5)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.check_bundle(result.stdout)
        self.assertEqual(requests, [("/capture", {"max_bytes": 1048576, "timeout_seconds": 5})])

    def test_running_receipt_mode_and_exact_inventory_are_required(self):
        original = self.native().stdout
        variants = [
            original,
            self.running_payload(original, omit_receipt=True),
            self.running_payload(original, lambda value: value.update(schema="unknown")),
            self.running_payload(original, lambda value: value["files"].pop()),
            self.running_payload(original, lambda value: value["files"][0].update(sha256="0" * 64)),
            self.running_payload(original, lambda value: value["directories"].pop()),
        ]
        for index, payload in enumerate(variants):
            with self.subTest(index=index):
                (self.workspace / "native-recovery.sock").unlink(missing_ok=True)
                with self.owner(payload):
                    result = self.native()
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(result.stdout, b"")

    def test_live_owner_failure_and_unsafe_archive_never_fall_back(self):
        bad = io.BytesIO()
        with tarfile.open(fileobj=bad, mode="w:gz") as archive:
            entry = tarfile.TarInfo("workspace/../escape")
            entry.size = 1
            archive.addfile(entry, io.BytesIO(b"x"))
        for payload, status in ((b"unavailable", 503), (bad.getvalue(), 200)):
            with self.subTest(status=status):
                endpoint = self.workspace / "native-recovery.sock"
                endpoint.unlink(missing_ok=True)
                with self.owner(payload, status):
                    result = self.native()
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(result.stdout, b"")
        self.assertFalse((self.root / "escape").exists())

    def test_live_owner_timeout_is_bounded_and_emits_no_partial_archive(self):
        with self.owner(b"", delay=2):
            result = self.native("--timeout-seconds", 1)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(result.stdout, b"")


class NormalRecoveryNativeSelectionTest(unittest.TestCase):
    def test_composite_selection_and_failed_native_preserve_vault_recovery(self):
        with tempfile.TemporaryDirectory() as name:
            work = Path(name)
            script = work / "snapshot.py"
            script.write_bytes(b"explicit snapshot implementation")
            cfg = {"client_script": str(script), "client": {"host": "approved-alias", "vault": "/old vault", "workspace": "/native data", "maxBytes": 100000, "timeoutSeconds": 30}}
            calls = []

            def run(args, **kwargs):
                calls.append((shlex.split(args[-1]), kwargs["timeout"]))
                self.assertEqual(args[:5], ["runuser", "-u", "agents", "--", "ssh"])
                kwargs["stdout"].write(b"verified composite archive" if len(calls) == 1 else b"verified vault archive")
                return subprocess.CompletedProcess(args, 0)

            with patch.object(recovery.subprocess, "run", side_effect=run):
                result = recovery.capture_client(cfg, work)
            self.assertEqual(result["workspace_recovery"], "consistent_native_checkpoint")
            self.assertEqual(result["client_sha256"], hashlib.sha256(b"verified composite archive").hexdigest())
            self.assertIn("--workspace", calls[0][0])
            self.assertTrue(0 < calls[0][1] <= 30)

            calls.clear()

            def native_refused(args, **kwargs):
                completed = run(args, **kwargs)
                if len(calls) <= 2:
                    completed.returncode = 1
                return completed

            with patch.object(recovery.subprocess, "run", side_effect=native_refused):
                result = recovery.capture_client(cfg, work)
            self.assertEqual(result["workspace_recovery"], "device_unavailable_or_capture_unstable")
            self.assertEqual(result["client_recovery"], "vault_and_intent_exports_not_native_database")
            self.assertEqual(result["client_sha256"], hashlib.sha256(b"verified vault archive").hexdigest())
            self.assertIn("--native", calls[1][0])
            self.assertNotIn("--workspace", calls[2][0])
            self.assertNotIn("--native", calls[2][0])
            self.assertLessEqual(calls[1][1], calls[0][1])

    def test_legacy_vault_failure_keeps_an_independent_native_checkpoint(self):
        with tempfile.TemporaryDirectory() as name:
            work=Path(name);script=work/'snapshot.py';script.write_bytes(b'snapshot implementation')
            cfg={'client_script':str(script),'client':{'host':'approved-alias','vault':'/old vault','workspace':'/native data','timeoutSeconds':30}}
            calls=[]
            def run(args,**kwargs):
                command=shlex.split(args[-1]);calls.append(command)
                kwargs['stdout'].write(b'native checkpoint' if '--native' in command else b'partial composite')
                return subprocess.CompletedProcess(args,0 if '--native' in command else 1)
            with patch.object(recovery.subprocess,'run',side_effect=run):result=recovery.capture_client(cfg,work)
            self.assertEqual(len(calls),2)
            self.assertEqual(result['client_recovery'],'native_workspace_checkpoint_without_vault')
            self.assertEqual(result['workspace_recovery'],'consistent_native_checkpoint')
            self.assertEqual(result['vault_recovery'],'unavailable_or_not_captured')
            self.assertEqual(result['client_sha256'],hashlib.sha256(b'native checkpoint').hexdigest())
            del cfg['client']['vault'];calls.clear()
            with patch.object(recovery.subprocess,'run',side_effect=run):result=recovery.capture_client(cfg,work)
            self.assertEqual(len(calls),1)
            self.assertEqual(result['vault_recovery'],'not_configured')

    def test_device_timeout_keeps_native_unavailable_and_removes_partial_transfer(self):
        with tempfile.TemporaryDirectory() as name:
            work = Path(name)
            script = work / "snapshot.py"
            script.write_bytes(b"snapshot implementation")
            cfg = {"client_script": str(script), "client": {"host": "approved-alias", "vault": "/vault", "workspace": "/workspace", "timeoutSeconds": 30}}

            def timeout(args, **kwargs):
                kwargs["stdout"].write(b"incomplete transfer")
                raise subprocess.TimeoutExpired(args, 30)

            with patch.object(recovery.time, "monotonic", side_effect=[0, 0, 31]), patch.object(recovery.subprocess, "run", side_effect=timeout) as run:
                result = recovery.capture_client(cfg, work)
            self.assertEqual(run.call_count, 1)
            self.assertEqual(result["workspace_recovery"], "device_unavailable_or_capture_unstable")
            self.assertNotIn("client_sha256", result)
            self.assertFalse((work / "mac-client.tar.gz").exists())


if __name__ == "__main__":
    unittest.main()
