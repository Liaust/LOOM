import importlib.util
import hashlib
import fcntl
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import time
from types import SimpleNamespace
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("notes_recovery", ROOT / "scripts/loom-notes-recovery.py")
recovery = importlib.util.module_from_spec(spec)
spec.loader.exec_module(recovery)


class NotesRecoveryTest(unittest.TestCase):
    writer = "loom-application@la-dcfe51bc335192b493985a10.service"

    def test_failed_capture_review_is_exact_and_replayable_without_payload_loss(self):
        with tempfile.TemporaryDirectory() as name:
            root = Path(name).resolve()
            for part in ("state", "operational", "cohorts", "staging", "held"):
                (root / part).mkdir(mode=0o700)
            (root / "operational.retention.lock").touch(mode=0o600)
            capture_id = "notes-20261031T010003Z-8a874b81"
            entry = capture_id + "-mivqhn1p"
            work = root / "staging" / entry
            work.mkdir(mode=0o700)
            archive = work / "main-state.tar.gz"
            archive.write_bytes(b"retained interrupted payload")
            archive.chmod(0o600)
            before = archive.stat()
            attempt = {"id": capture_id, "status": "failed", "staging_name": entry}
            recovery.atomic_recovery_state(root / "state/recovery-attempt.json", attempt)
            cfg = {"state_dir": str(root / "state"), "operational_root": str(root / "operational"),
                   "staging": str(root / "staging"), "held_staging": str(root / "held")}
            path = root / "config.json"
            path.write_text(json.dumps(cfg))

            def rename(source, target):
                if Path(target).exists():
                    raise FileExistsError(target)
                os.rename(source, target)

            with patch.object(recovery, "require_operator"), patch.object(recovery, "run", return_value="inactive\n"), patch.object(recovery, "rename_noreplace", side_effect=rename):
                plan = recovery.review_failed_capture(path, entry)
                with self.assertRaisesRegex(ValueError, "identity/reason"):
                    recovery.review_failed_capture(path, entry, "sha256:wrong", "reviewed failure")
                with patch.object(recovery, "rename_noreplace", side_effect=InterruptedError("before rename")):
                    with self.assertRaises(InterruptedError):
                        recovery.review_failed_capture(path, entry, plan["plan_hash"], "reviewed failure")
                result = recovery.review_failed_capture(path, entry, plan["plan_hash"], "reviewed failure")
                self.assertEqual(result["status"], "held")
                moved = root / "held" / entry / archive.name
                self.assertFalse(work.exists())
                self.assertEqual(moved.read_bytes(), b"retained interrupted payload")
                self.assertEqual(moved.stat().st_ino, before.st_ino)
                self.assertEqual(recovery.review_failed_capture(path, entry, plan["plan_hash"], "reviewed failure")["status"], "held")
                with self.assertRaisesRegex(ValueError, "replay conflict"):
                    recovery.review_failed_capture(path, entry, plan["plan_hash"], "changed reason")

    def test_failed_capture_review_refuses_active_changed_or_unsafe_entries(self):
        with tempfile.TemporaryDirectory() as name:
            root = Path(name).resolve()
            for part in ("state", "staging", "held"):
                (root / part).mkdir(mode=0o700)
            capture_id = "notes-20261031T010003Z-8a874b81"
            entry = capture_id + "-mivqhn1p"
            source = root / "staging" / entry
            source.mkdir(mode=0o700)
            data = source / "main-state.tar.gz"
            data.touch(mode=0o600)
            cfg = {"staging": str(root / "staging"), "held_staging": str(root / "held")}
            recovery.atomic_recovery_state(root / "state/recovery-attempt.json", {"id": capture_id, "status": "failed", "staging_name": entry})
            with patch.object(recovery, "run", return_value="active\n"):
                with self.assertRaisesRegex(ValueError, "producer"):
                    recovery.failed_capture_plan(cfg, root / "state", entry)
            with patch.object(recovery, "run", return_value="inactive\n"):
                first = recovery.failed_capture_plan(cfg, root / "state", entry)
                data.write_bytes(b"new bytes")
                self.assertNotEqual(first["plan_hash"], recovery.failed_capture_plan(cfg, root / "state", entry)["plan_hash"])
                (source / "cohort.json").touch(mode=0o600)
                with self.assertRaisesRegex(ValueError, "coherent"):
                    recovery.failed_capture_plan(cfg, root / "state", entry)
                with self.assertRaisesRegex(ValueError, "name"):
                    recovery.failed_capture_plan(cfg, root / "state", "../escape")
                os.symlink(data, source / "link")
                with self.assertRaisesRegex(ValueError, "custody"):
                    recovery.failed_inventory(source)

    def test_attempt_phases_and_archive_failure_retain_private_diagnostics(self):
        with tempfile.TemporaryDirectory() as name:
            root = Path(name).resolve()
            attempt = {"id": "notes-20261031T010003Z-8a874b81", "status": "capturing"}
            error = subprocess.CalledProcessError(1, ["tar"], stderr="private diagnostic" * 1000)
            with patch.object(recovery, "run", side_effect=error):
                with self.assertRaises(subprocess.CalledProcessError):
                    with recovery.capture_phase("main_state_archive", root, attempt):
                        recovery.create_archive(root / "main-state.tar.gz", ["owned/source"])
            self.assertEqual(json.loads((root / "recovery-attempts" / (attempt["id"] + ".json")).read_text())["phase"], "main_state_archive")
            evidence = root / "archive-failure.json"
            self.assertEqual(len(json.loads(evidence.read_text())["stderr"]), 8192)
            self.assertEqual(evidence.stat().st_mode & 0o777, 0o600)

    def test_capacity_defers_before_a_new_attempt_and_keeps_failed_payload(self):
        with tempfile.TemporaryDirectory() as name:
            root = Path(name).resolve()
            for part in ("state", "operational", "cohorts", "staging"):
                (root / part).mkdir()
            cfg = {"operational_root": str(root / "operational"), "destination": str(root / "cohorts"), "staging": str(root / "staging"),
                   "loom": "/fixed/loom", "cloud_config": "/fixed/cloud.json",
                   "capacity": {"mode": "enforce", "operational_budget_bytes": 2 ** 30, "notes_budget_bytes": 2 ** 30,
                                "operational_free_floor_bytes": 1, "notes_free_floor_bytes": 1,
                                "initial_operational_bytes": 1, "initial_notes_bytes": 1}}
            plan = json.dumps({"entries": [], "accounting_complete": True})
            with patch.object(recovery, "run", return_value=plan) as run:
                self.assertTrue(recovery.recovery_admission(cfg, root / "state")["allowed"])
                held = root / "staging/failed-attempt"
                held.mkdir()
                (held / "main-state.tar.gz").write_bytes(b"unique failed data")
                result = recovery.recovery_admission(cfg, root / "state")
                self.assertFalse(result["allowed"])
                self.assertIn("pending_or_failed_capture_requires_attention", result["reasons"])
                self.assertEqual((held / "main-state.tar.gz").read_bytes(), b"unique failed data")
                self.assertTrue(all("plan" in call.args and "systemctl" not in call.args and "create" not in call.args for call in run.call_args_list))
                cfg["capacity"]["mode"] = "observe"
                self.assertTrue(recovery.recovery_admission(cfg, root / "state")["allowed"])
                cfg["capacity"]["mode"] = "enforce"
                history = root / "historical-held"
                history.mkdir()
                held.rename(history / held.name)
                cfg["held_staging"] = str(history)
                result = recovery.recovery_admission(cfg, root / "state")
                self.assertTrue(result["allowed"])
                self.assertEqual(result["pending_captures"], 0)
                self.assertEqual(result["historical_held_captures"], 1)
                self.assertGreaterEqual(result["notes_allocated_bytes"], (history / held.name / "main-state.tar.gz").stat().st_blocks * 512)
                cfg["capacity"]["notes_budget_bytes"] = 1
                self.assertIn("notes_working_set_over_budget", recovery.recovery_admission(cfg, root / "state")["reasons"])
                cfg["capacity"]["notes_budget_bytes"] = 2 ** 30
                with patch.object(recovery.os, "statvfs", return_value=SimpleNamespace(f_bavail=0, f_frsize=4096)):
                    self.assertIn("shared_filesystem_headroom_insufficient", recovery.recovery_admission(cfg, root / "state")["reasons"])

    def test_capacity_allocation_deduplicates_hardlinks_and_refuses_symlinks(self):
        import os
        with tempfile.TemporaryDirectory() as name:
            root = Path(name)
            first = root / "first"
            first.write_bytes(b"x" * 8192)
            os.link(first, root / "second")
            expected = first.stat().st_blocks * 512 + root.stat().st_blocks * 512
            self.assertEqual(recovery.bounded_recovery_allocation(root), expected)
            os.symlink(first, root / "linked")
            with self.assertRaisesRegex(ValueError, "unknown entry"):
                recovery.bounded_recovery_allocation(root)

    def test_operational_reference_keeps_payload_in_place(self):
        with tempfile.TemporaryDirectory() as name:
            package = Path(name) / "operational-fixture"
            package.mkdir()
            raw = json.dumps({"schema": "loom.operational_backup.v1", "package_id": package.name}).encode()
            (package / "manifest.json").write_bytes(raw)
            sha = hashlib.sha256(raw).hexdigest()
            destination = Path(name) / "operational-reference.json"
            got, reference_sha = recovery.write_operational_reference(package, {"manifest_sha256": sha}, destination)
            self.assertEqual(got, sha)
            self.assertEqual(reference_sha, hashlib.sha256(destination.read_bytes()).hexdigest())
            self.assertEqual(json.loads(destination.read_text())["package_id"], package.name)
            self.assertFalse((Path(name) / "operational-package").exists())
            with self.assertRaisesRegex(ValueError, "committed backup"):
                recovery.write_operational_reference(package, {"manifest_sha256": "0" * 64}, Path(name) / "wrong")

    def test_operational_copy_checks_actual_copied_bytes(self):
        with tempfile.TemporaryDirectory() as name:
            root = Path(name)
            source = root / "source"
            source.mkdir()
            payload = b"small operational payload"
            (source / "postgres.dump").write_bytes(payload)
            manifest = json.dumps({"artifacts": [{"path": "postgres.dump", "size_bytes": len(payload),
                                                   "sha256": hashlib.sha256(payload).hexdigest()}]}).encode()
            sha = hashlib.sha256(manifest).hexdigest()
            (source / "manifest.json").write_bytes(manifest)
            (source / "manifest.sha256").write_text(sha + "  manifest.json\n")
            self.assertEqual(recovery.copy_verified_package(source, root / "copy"), sha)
            self.assertEqual((root / "copy/postgres.dump").read_bytes(), payload)
            (source / "postgres.dump").write_bytes(b"x" * len(payload))
            with self.assertRaisesRegex(ValueError, "hash differs"):
                recovery.copy_verified_package(source, root / "changed")
            (source / "unexpected").write_bytes(b"extra")
            with self.assertRaisesRegex(ValueError, "inventory differs"):
                recovery.copy_verified_package(source, root / "unknown")

    def test_archive_inventory_never_selects_an_archive_source_writer(self):
        with tempfile.TemporaryDirectory() as name:
            state = Path(name).resolve()
            live = state / "live"
            live.mkdir()
            archived = state / "retained-payload-not-opened"
            runtime = {"replica": "one", "scopes": []}
            entries = [
                {"Collection": "live", "SourceCollection": "live-source", "Root": "Projects/_Passive/Live/Notes",
                 "status": "active", "work_state": "passive", "source_path": str(live), "source_generation": "live"},
                {"Collection": "old", "SourceCollection": "old-source", "Root": "Projects/_Archived/Old/Notes",
                 "status": "reference", "archived": True, "source_path": str(archived), "source_generation": "archive",
                 "archive_operation": "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAV", "archive_manifest": "sha256:" + "b" * 64},
                {"Collection": "held", "SourceCollection": "held-source", "Root": "Projects/_Archived/Held/Notes",
                 "status": "reference_held", "archived": True}]
            path = state / "enrollment.json"
            path.write_text(json.dumps({"schema_version": "notes_workspace.enrollment.v1", "replica": "one",
                                       "config_sha256": recovery.runtime_digest(runtime), "collections": entries}))
            path.chmod(0o600)
            archive_writer = "loom-application@la-000000000000000000000000.service"
            cfg = {"enrollment_inventory": str(path), "source_writers": {str(live): [self.writer], str(archived): [archive_writer]}}
            collections, _, _ = recovery.recovery_membership(cfg, runtime, state)
            with patch.object(recovery, "run", return_value="LoadState=loaded\nActiveState=inactive\n") as run:
                with recovery.quiesce_source_writers(cfg, state, [Path(p) for p in collections.values()]):
                    pass
                self.assertEqual(run.call_count, 1)
                self.assertEqual(run.call_args.args[:3], ("systemctl", "show", self.writer))

    def test_source_writer_enrollment_and_prior_state(self):
        with tempfile.TemporaryDirectory() as name:
            state = Path(name).resolve()
            root = state / "source"
            root.mkdir()
            cfg = {"source_writers": {str(root): [self.writer]}}
            with patch.object(recovery, "run") as run:
                with recovery.quiesce_source_writers(cfg, state, [state / "unrelated"]):
                    run.assert_not_called()
                run.return_value = "LoadState=loaded\nActiveState=inactive\n"
                with recovery.quiesce_source_writers(cfg, state, [root / "child"]):
                    self.assertFalse((state / "recovery.writers.json").exists())
                self.assertFalse(any(call.args[1] in ("stop", "start") for call in run.call_args_list))
                run.return_value = "LoadState=loaded\nActiveState=activating\n"
                with self.assertRaises(ValueError):
                    with recovery.quiesce_source_writers(cfg, state, [root]):
                        self.fail("transitioning service admitted")
            cfg["source_writers"][str(root)] = ["postgresql.service"]
            with self.assertRaises(ValueError):
                recovery.source_writer_bindings(cfg)

    def test_source_writer_restart_intent_survives_failure(self):
        with tempfile.TemporaryDirectory() as name:
            state = Path(name).resolve()
            cfg = {"source_writers": {str(state / "source"): [self.writer]}}
            receipt = state / "recovery.writers.json"
            calls = []
            def run(*args, **kwargs):
                calls.append(args)
                if args[1] == "show":
                    return "inactive\n" if "--value" in args else "LoadState=loaded\nActiveState=active\n"
                if args[1] == "stop":
                    self.assertEqual(json.loads(receipt.read_text())["units"], [self.writer])
                    self.assertEqual(receipt.stat().st_mode & 0o777, 0o600)
                return ""
            with patch.object(recovery, "run", side_effect=run):
                with self.assertRaisesRegex(RuntimeError, "archive failure"):
                    with recovery.quiesce_source_writers(cfg, state, [state / "source"]) as units:
                        self.assertEqual(units, [self.writer])
                        raise RuntimeError("archive failure")
                self.assertFalse(receipt.exists())
                self.assertIn(("systemctl", "start", self.writer), calls)
                # Persisted pre-stop intent is also the SIGKILL cleanup input.
                receipt.write_text(json.dumps({"schema": "loom.notes_recovery.writers.v1", "units": [self.writer]}))
                receipt.chmod(0o600)
                with patch.object(recovery, "run", side_effect=RuntimeError("restart failure")):
                    with self.assertRaises(RuntimeError):
                        recovery.resume_source_writers(cfg, state)
                self.assertTrue(receipt.exists())
                recovery.resume_source_writers(cfg, state)
                self.assertFalse(receipt.exists())
                recovery.resume_source_writers(cfg, state)  # Idempotent cleanup.
                receipt.write_text(json.dumps({"schema": "loom.notes_recovery.writers.v1", "units": ["postgresql.service"]}))
                receipt.chmod(0o600)
                count = len(calls)
                with self.assertRaises(ValueError):
                    recovery.resume_source_writers(cfg, state)
                self.assertEqual(len(calls), count)

    def test_writer_resume_failure_does_not_skip_notes_transport_cleanup(self):
        with tempfile.TemporaryDirectory() as name:
            state = Path(name).resolve()
            config = state / "config.json"
            config.write_text(json.dumps({"state_dir": str(state)}))
            with patch.object(recovery, "resume_source_writers", side_effect=RuntimeError("writer unavailable")), patch.object(recovery, "run") as run:
                with self.assertRaisesRegex(RuntimeError, "writer unavailable"):
                    recovery.cleanup(config)
                run.assert_any_call("systemctl", "start", "couchdb.service")

    def test_live_source_replacement_requires_writer_quiescence(self):
        import shutil
        tar = shutil.which("tar")
        if not tar or "GNU tar" not in subprocess.run([tar, "--version"], capture_output=True, text=True).stdout:
            self.skipTest("GNU tar integration runs on Main")
        with tempfile.TemporaryDirectory() as name:
            state = Path(name).resolve()
            root = state / "source"
            root.mkdir()
            source = root / "notebook.pdf"
            source.write_bytes(b"original PDF fixture")
            archive = state / "sample.tar.gz"
            def capture():
                subprocess.run([tar, "--acls", "--xattrs", "--numeric-owner", "-czf", str(archive),
                                "-C", "/", "--", str(root).lstrip("/")], check=True, capture_output=True)
            capture()
            replacement = state / "new.pdf"
            replacement.write_bytes(b"newly uploaded PDF fixture")
            replacement.replace(source)
            with self.assertRaises(subprocess.CalledProcessError):
                recovery.compare_archive(archive)
            cfg = {"source_writers": {str(root): [self.writer]}}
            real_run = recovery.run
            active = True
            def run(*args, **kwargs):
                nonlocal active
                if args[0] != "systemctl":
                    return real_run(*args, **kwargs)
                if args[1] == "show":
                    return "inactive\n" if "--value" in args else "LoadState=loaded\nActiveState=active\n"
                if args[1] == "stop":
                    active = False
                if args[1] == "start":
                    active = True
                return ""
            with patch.object(recovery, "run", side_effect=run):
                with recovery.quiesce_source_writers(cfg, state, [root]):
                    capture()
                    self.assertFalse(active)
                    recovery.compare_archive(archive)
                self.assertTrue(active)

    def test_comparison_failure_preserves_bounded_private_diagnostics(self):
        with tempfile.TemporaryDirectory() as name:
            archive = Path(name) / "main-state.tar.gz"
            error = subprocess.CalledProcessError(1, ["tar"], output="file: Size differs\n" + "x" * 10000, stderr="warning")
            with patch.object(recovery, "run", side_effect=error) as run:
                with self.assertRaises(subprocess.CalledProcessError):
                    recovery.compare_archive(archive)
                self.assertTrue(run.call_args.kwargs["output"])
            receipt = archive.parent / "comparison-failure.json"
            value = json.loads(receipt.read_text())
            self.assertEqual(receipt.stat().st_mode & 0o777, 0o600)
            self.assertEqual(value["returncode"], 1)
            self.assertEqual(len(value["stdout"]), 8192)
            self.assertEqual(value["stderr"], "warning")
            with patch.object(recovery, "run", side_effect=error), self.assertRaises(FileExistsError):
                recovery.compare_archive(archive)
            self.assertEqual(json.loads(receipt.read_text()), value)

    def test_operational_lease_blocks_retention_not_other_readers(self):
        with tempfile.TemporaryDirectory() as name:
            root = Path(name).resolve()
            lock_path = Path(str(root) + ".retention.lock")
            try:
                lock_path.touch(mode=0o600)
                with recovery.operational_lease(str(root)), lock_path.open("rb") as operator:
                    with self.assertRaises(BlockingIOError):
                        fcntl.flock(operator, fcntl.LOCK_EX | fcntl.LOCK_NB)
                    fcntl.flock(operator, fcntl.LOCK_SH | fcntl.LOCK_NB)
                with lock_path.open("rb") as operator:
                    fcntl.flock(operator, fcntl.LOCK_EX | fcntl.LOCK_NB)
                    with self.assertRaises(BlockingIOError):
                        with recovery.operational_lease(str(root)):
                            self.fail("capture admitted during retention")
            finally:
                lock_path.unlink(missing_ok=True)

    def test_stream_fingerprint_matches_legacy_aggregate(self):
        self.assertIn("reference_payloads", recovery.TABLES)
        self.assertIn("reference_dedup_runs", recovery.TABLES)
        rows = sorted(hashlib.md5(value).hexdigest().encode() for value in (b"one", b"two", b"one"))
        expected = "3|" + hashlib.md5(b"".join(rows)).hexdigest()
        self.assertEqual(recovery.fingerprint_rows(row + b"\n" for row in rows), expected)
        self.assertEqual(recovery.fingerprint_rows(iter(())), "0|" + hashlib.md5(b"").hexdigest())
        with self.assertRaises(ValueError):
            recovery.fingerprint_rows([b"private command failure\n"])

    def test_cleanup_restores_transport_and_requires_exclusive_lock(self):
        with tempfile.TemporaryDirectory() as name:
            state = Path(name).resolve()
            (state / "recovery.lock").touch()
            request = state / "recovery.request"
            request.touch(mode=0o600)
            config = state / "config.json"
            config.write_text(json.dumps({"state_dir": str(state)}))
            # Local tests are unprivileged; model the service-owned marker only.
            original = Path.lstat
            def root_marker(path):
                info = original(path)
                if path == request:
                    values = list(info)
                    values[4] = 0
                    return recovery.os.stat_result(values)
                return info
            with (state / "recovery.lock").open("r+") as lock, patch.object(recovery, "run") as run, patch.object(Path, "lstat", root_marker):
                fcntl.flock(lock, fcntl.LOCK_EX)
                with patch.object(recovery, "acquire_lock", side_effect=TimeoutError), self.assertRaises(TimeoutError):
                    recovery.cleanup(config)
                self.assertTrue(request.exists())
                run.assert_any_call("systemctl", "start", "couchdb.service")
            with patch.object(recovery, "run"), patch.object(Path, "lstat", root_marker):
                recovery.cleanup(config)
                self.assertFalse(request.exists())
                recovery.cleanup(config)  # Normal exit already removed it.
                request.symlink_to(config)
                with self.assertRaises(ValueError):
                    recovery.cleanup(config)
                self.assertTrue(config.exists())

    def test_request_precedes_fence_and_resume_follows_release(self):
        with tempfile.TemporaryDirectory() as name:
            state = Path(name).resolve()
            request = state / "recovery.request"
            with recovery.lock_file(state / "recovery.lock") as native:
                fcntl.flock(native, fcntl.LOCK_EX)
                released = threading.Event()
                def native_watch():
                    deadline = time.monotonic() + 2
                    while not request.exists() and time.monotonic() < deadline:
                        time.sleep(0.01)
                    if request.exists():
                        fcntl.flock(native, fcntl.LOCK_UN)
                        released.set()
                watcher = threading.Thread(target=native_watch)
                watcher.start()
                with patch("pwd.getpwnam", return_value=SimpleNamespace(pw_uid=recovery.os.getuid(), pw_gid=recovery.os.getgid())):
                    with recovery.recovery_lock(state, timeout=3):
                        self.assertTrue(released.is_set())
                        self.assertTrue(request.exists())
                        with recovery.lock_file(state / "recovery.lock") as other:
                            with self.assertRaises(BlockingIOError):
                                fcntl.flock(other, fcntl.LOCK_EX | fcntl.LOCK_NB)
                        with self.assertRaises(BlockingIOError):
                            with recovery.publisher_lock(state):
                                self.fail("overlapping publisher admitted")
                watcher.join()
                self.assertFalse(request.exists())
                fcntl.flock(native, fcntl.LOCK_EX | fcntl.LOCK_NB)

    def test_timeout_removes_own_request_but_cleanup_without_request_needs_no_fence(self):
        with tempfile.TemporaryDirectory() as name:
            state = Path(name).resolve()
            config = state / "config.json"
            config.write_text(json.dumps({"state_dir": str(state)}))
            with recovery.lock_file(state / "recovery.lock") as native:
                fcntl.flock(native, fcntl.LOCK_EX)
                with patch("pwd.getpwnam", return_value=SimpleNamespace(pw_uid=recovery.os.getuid(), pw_gid=recovery.os.getgid())):
                    with self.assertRaises(TimeoutError):
                        with recovery.recovery_lock(state, timeout=0):
                            self.fail("busy fence admitted")
                self.assertFalse((state / "recovery.request").exists())
                with patch.object(recovery, "run"), patch.object(recovery, "acquire_lock") as acquire:
                    recovery.cleanup(config)
                    acquire.assert_not_called()
                    with recovery.publisher_lock(state), self.assertRaises(BlockingIOError):
                        recovery.cleanup(config)

    def test_recovery_request_is_exclusive_and_removed_after_failure(self):
        with tempfile.TemporaryDirectory() as name:
            state = Path(name)
            request = state / "recovery.request"
            with self.assertRaisesRegex(RuntimeError, "capture failed"):
                with recovery.recovery_request(state):
                    self.assertEqual(request.stat().st_mode & 0o777, 0o600)
                    with self.assertRaises(FileExistsError):
                        with recovery.recovery_request(state):
                            self.fail("overlapping capture admitted")
                    raise RuntimeError("capture failed")
            self.assertFalse(request.exists())

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
