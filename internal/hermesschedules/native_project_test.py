"""Focused adapter tests. Fake native API is not evidence of installed Hermes."""
import contextlib
import copy
from datetime import datetime, timezone
import json
from pathlib import Path
import tempfile
import types
import threading
import fcntl
import socket
import unittest
import native_project as owner


class ProjectOwnerTest(unittest.TestCase):
    def test_timeout_socket_live_probe_is_nonblocking(self):
        local, peer = socket.socketpair()
        self.addCleanup(local.close)
        self.addCleanup(peer.close)
        local.settimeout(0.05)
        owner.require_live_caller(local)
        self.assertEqual(local.gettimeout(), 0.05)
        peer.sendall(b"x")
        with self.assertRaises(ConnectionError):
            owner.require_live_caller(local)
        self.assertEqual(local.recv(1), b"x")
        peer.close()
        with self.assertRaises(ConnectionError):
            owner.require_live_caller(local)
        self.assertEqual(local.gettimeout(), 0.05)

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.home = Path(self.temp.name)
        (self.home / "cron").mkdir()
        self.path = self.home / "cron/jobs.json"
        self.path.write_text(json.dumps({"jobs": [{"id": "manual", "prompt": "private"}]}))
        self.writes = 0
        self.native = n = types.SimpleNamespace()
        n.use_cron_store = lambda home: contextlib.nullcontext()
        n._jobs_lock = contextlib.nullcontext
        n._jobs_file_lock = threading.RLock()
        n._jobs_lock_state = threading.local()
        n._jobs_lock_file = lambda: self.home / "cron/.jobs.lock"
        n.ensure_dirs = lambda: None
        n._hermes_now = lambda: datetime.now(timezone.utc)
        n._normalize_workdir = lambda p: p
        def parse_schedule(expr):
            if expr.startswith("every "):
                return dict(kind="interval", minutes=int(expr.split()[1][:-1]), display=expr)
            if "T" in expr:
                return dict(kind="once", run_at=datetime.fromisoformat(expr.replace("Z", "+00:00")).isoformat(), display=expr)
            return dict(kind="cron", expr=expr, display=expr)
        n.parse_schedule = parse_schedule
        n.load_jobs = lambda: owner.strict_jobs(self.home)
        def save(jobs):
            self.writes += 1
            self.path.write_text(json.dumps({"jobs": jobs}))
        n.save_jobs = save
        def create(**kw):
            job = dict(kw, id="aabbccddeeff", enabled=True, state="scheduled", paused_at=None, repeat={"times": None, "completed": 0})
            job["skill"] = job["skills"][0] if job["skills"] else None
            job["schedule"] = n.parse_schedule(job["schedule"])
            if job["schedule"]["kind"] == "once":
                job["repeat"]["times"] = 1
            jobs = n.load_jobs(); jobs.append(job); n.save_jobs(jobs)
            return job
        n.create_job = create
        def update(job_id, changes):
            jobs = n.load_jobs()
            job = next(j for j in jobs if j["id"] == job_id)
            was_terminal = owner.terminal(job)
            changes = copy.deepcopy(changes)
            # Pinned update_job normalizes an explicit bare repeat value and
            # retains completed; schedule changes alone never derive times.
            if "repeat" in changes:
                repeat = changes["repeat"]
                if not isinstance(repeat, dict):
                    repeat = {"times": repeat}
                repeat.setdefault("completed", job["repeat"]["completed"])
                changes["repeat"] = repeat
            job.update(copy.deepcopy(changes))
            if isinstance(job["schedule"], str):
                job["schedule"] = n.parse_schedule(job["schedule"])
            if "schedule" in changes and job["state"] != "paused":
                job["next_run_at"] = "future native occurrence"
            if was_terminal and (job["state"] not in ("completed", "error") or job["enabled"] or job.get("next_run_at") is not None):
                raise ValueError("Cannot activate terminal cron job; use cron resume --run-now or --at")
            job["skill"] = job["skills"][0] if job["skills"] else None
            n.save_jobs(jobs)
            return job
        n.update_job = update
        n.pause_job = lambda job_id, reason: update(job_id, dict(enabled=False, state="paused", paused_at="2026-09-28T12:00:00Z", paused_reason=reason))
        self.source = dict(host="main", profile="mina", revision=owner.REVISION)
        self.request = dict(operation="reconcile", source=self.source, project_id="project_test", resource="review", expected_revision="absent", expected_id="", token="first", input_hash=owner.digest("input"), spec=dict(schedule="every 60m", prompt="read AGENTS.md", skills=[], status="paused", workdir=str(self.home)))

    def execute(self, request=None):
        return owner.execute(self.native, self.home, self.source, request or self.request)

    def next_request(self, job, token, **changes):
        req = copy.deepcopy(self.request)
        req.update(expected_revision=job["revision"], expected_id=job["id"], token=token, **changes)
        return req

    def test_create_pause_atomic_replay_withdraw_restore_and_unowned(self):
        first = self.execute()[0]
        self.assertTrue(first["paused"])
        self.assertEqual(self.writes, 1)
        self.assertEqual(self.execute(), [first])
        self.assertEqual(self.writes, 1)
        req = self.next_request(first, "edit")
        req["spec"].update(status="active", prompt="changed context")
        second = self.execute(req)[0]
        self.assertTrue(second["paused"])
        self.assertFalse(second["drift"])
        req = self.next_request(second, "withdraw", operation="retire"); req.pop("spec")
        withdrawn = self.execute(req)[0]
        self.assertTrue(withdrawn["retired"])
        restored = self.execute(self.next_request(withdrawn, "restore"))[0]
        self.assertEqual(restored["id"], first["id"])
        self.assertTrue(restored["paused"])
        self.assertFalse(restored["retired"])
        self.assertEqual(owner.strict_jobs(self.home)[0], {"id": "manual", "prompt": "private"})

    def test_drift_conflict_and_stale_revision(self):
        first = self.execute()[0]
        self.native.update_job(first["id"], {"prompt": "manual edit"})
        listed = self.execute(dict(operation="list", source=self.source, project_id="project_test"))[0]
        self.assertTrue(listed["drift"])
        for snapshot in (first, listed):
            with self.assertRaises(owner.Conflict):
                self.execute(self.next_request(snapshot, "stale"))

    def test_once_to_recurring_removes_limit_without_resuming_or_erasing_history(self):
        for schedule in ("every 60m", "0 * * * *"):
            with self.subTest(schedule=schedule):
                self.path.write_text('{"jobs": []}')
                self.request["spec"]["schedule"] = "2099-01-01T12:00:00Z"
                first = self.execute()[0]
                # Native-shaped accumulated execution data is not config drift.
                self.native.update_job(first["id"], {"repeat": {"times": 1, "completed": 1}, "last_status": "success"})
                req = self.next_request(first, "recurring")
                req["spec"].update(schedule=schedule, status="active")
                second = self.execute(req)[0]
                job = owner.strict_jobs(self.home)[0]
                self.assertEqual(job["repeat"], {"times": None, "completed": 1})
                self.assertEqual(job["last_status"], "success")
                self.assertTrue(second["paused"])
                self.assertFalse(second["drift"])
                self.assertEqual(second["id"], first["id"])
                self.assertEqual(self.execute(req), [second])

    def test_recurring_to_once_allows_one_more_dispatch_and_preserves_history(self):
        for schedule in ("every 60m", "0 * * * *"):
            with self.subTest(schedule=schedule):
                self.path.write_text('{"jobs": []}')
                self.request["spec"]["schedule"] = schedule
                first = self.execute()[0]
                self.native.update_job(first["id"], {"repeat": {"times": None, "completed": 7}, "last_status": "success"})
                req = self.next_request(first, "once")
                req["spec"].update(schedule="2099-01-01T12:00:00Z", status="active")
                second = self.execute(req)[0]
                job = owner.strict_jobs(self.home)[0]
                self.assertEqual(job["repeat"], {"times": 8, "completed": 7})
                self.assertEqual(job["last_status"], "success")
                self.assertTrue(second["paused"])
                self.assertFalse(second["drift"])
                self.assertEqual(self.execute(req), [second])
                # Pinned claim_dispatch uses this cumulative comparison; one
                # dispatch remains and a second claim is disallowed.
                repeat = job["repeat"]
                self.assertLess(repeat["completed"], repeat["times"])
                repeat["completed"] += 1
                self.assertGreaterEqual(repeat["completed"], repeat["times"])
                # Moving the same unconsumed once occurrence is not a new quota.
                req = self.next_request(second, "move-once")
                req["spec"]["schedule"] = "2099-01-02T12:00:00Z"
                self.execute(req)
                self.assertEqual(owner.strict_jobs(self.home)[0]["repeat"], {"times": 8, "completed": 7})

    def test_terminal_timing_edits_require_native_resume_without_commit(self):
        self.request["spec"]["schedule"] = "2099-01-01T12:00:00Z"
        first = self.execute()[0]
        for state in ("completed", "error"):
            jobs = owner.strict_jobs(self.home)
            job = jobs[-1]
            job.update(state=state, enabled=False, next_run_at=None, repeat={"times": 1, "completed": 1}, last_status="retained")
            self.native.save_jobs(jobs)
            before = self.path.read_bytes()
            snapshot = owner.public(job)
            writes = self.writes
            for schedule in ("every 60m", "0 * * * *", "2099-01-02T12:00:00Z"):
                with self.subTest(state=state, schedule=schedule):
                    req = self.next_request(snapshot, "terminal-edit")
                    req["spec"].update(schedule=schedule, status="active")
                    with self.assertRaises(owner.TerminalRequiresResume):
                        self.execute(req)
                    self.assertEqual(self.path.read_bytes(), before)
                    self.assertEqual(self.writes, writes)
            # An unchanged declaration may be acknowledged, but never re-arms.
            req = self.next_request(snapshot, "unchanged-" + state)
            req["spec"]["status"] = "active"
            self.execute(req)
            retained = owner.strict_jobs(self.home)[-1]
            self.assertEqual(retained["state"], state)
            self.assertFalse(retained["enabled"])
            self.assertIsNone(retained["next_run_at"])
            self.assertEqual(retained["repeat"], {"times": 1, "completed": 1})

    def test_failure_before_commit_leaves_no_job_and_restores_native_functions(self):
        save = self.native.save_jobs
        self.native.pause_job = lambda *a, **kw: (_ for _ in ()).throw(ValueError("failure"))
        with self.assertRaises(ValueError):
            self.execute()
        self.assertEqual(self.writes, 0)
        self.assertEqual(len(owner.strict_jobs(self.home)), 1)
        self.assertIs(self.native.save_jobs, save)

    def test_archive_only_owned_and_never_resume(self):
        self.request["spec"]["status"] = "active"
        first = self.execute()[0]
        self.assertFalse(first["paused"])
        req = dict(operation="pause_project", source=self.source, project_id="project_test")
        archived = self.execute(req)[0]
        self.assertTrue(archived["paused"] and archived["retired"])
        restored = self.execute(self.next_request(archived, "restored"))[0]
        self.assertTrue(restored["paused"])
        self.assertEqual(owner.strict_jobs(self.home)[0], {"id": "manual", "prompt": "private"})

    def test_lock_contention_fails_closed_without_write(self):
        with open(self.native._jobs_lock_file(), "a+") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            old = owner.LOCK_WAIT_SECONDS
            try:
                owner.LOCK_WAIT_SECONDS = 0.01
                with self.assertRaises(TimeoutError):
                    self.execute()
            finally:
                owner.LOCK_WAIT_SECONDS = old
            self.assertEqual(self.writes, 0)


    def test_disconnect_before_commit_cannot_create_after_archive_overtakes(self):
        calls = 0
        def alive():
            nonlocal calls
            calls += 1
            if calls == 2:
                raise ConnectionError("caller gone")
        with self.assertRaises(ConnectionError):
            owner.execute(self.native, self.home, self.source, self.request, alive)
        self.assertEqual(self.writes, 0)
        self.assertEqual(len(owner.strict_jobs(self.home)), 1)


    def test_read_malformed_never_repairs(self):
        self.path.write_text('{"jobs":')
        with self.assertRaises(ValueError):
            self.execute(dict(operation="list", source=self.source, project_id="project_test"))
        self.assertEqual(self.path.read_text(), '{"jobs":')
        self.assertEqual(self.writes, 0)


if __name__ == "__main__":
    unittest.main()
