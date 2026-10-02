"""Pinned Hermes project owner. Installed as a fixed socket-activated entry.

No timer and no model calls. Only cron.jobs performs native mutation. A tiny
in-memory transaction batches its public API writes under its own reentrant
cross-process lock, committing once so create+paused+ownership are indivisible.
The S1 read-only helper does not import or invoke this module.
"""
import contextlib
import fcntl
import time
import copy
import hashlib
import json
import os
from pathlib import Path
import re

REVISION = "29112bef099274229cadff79cdff7bf7b99c4b77"
MARKER = "loom_project_declaration"
KEY = re.compile(r"^[a-z][a-z0-9_-]{0,62}$")
PROJECT = re.compile(r"^project_[A-Za-z0-9_-]+$")
DIGEST = re.compile(r"^sha256:[0-9a-f]{64}$")
LIMIT = 4 << 20
LOCK_WAIT_SECONDS = 2


def digest(value):
    return "sha256:" + hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True).encode()).hexdigest()


def strict_jobs(home):
    # Never use load_jobs for reads: native load can repair malformed input.
    if not Path(home).is_dir():
        raise ValueError("profile unavailable")
    try:
        with (Path(home) / "cron/jobs.json").open("rb") as stream:
            raw = stream.read(LIMIT + 1)
    except FileNotFoundError:
        # Management can declare the first job in an existing fixed profile.
        # S1 retains its separate absent-source/unavailable observation contract.
        return []
    if len(raw) > LIMIT:
        raise ValueError("store too large")
    value = json.loads(raw.decode("utf-8-sig"))
    if not isinstance(value, dict) or not isinstance(value.get("jobs"), list):
        raise ValueError("malformed store")
    seen = set()
    for job in value["jobs"]:
        if not isinstance(job, dict) or not isinstance(job.get("id"), str) or job["id"] in seen:
            raise ValueError("malformed identity")
        seen.add(job["id"])
    return value["jobs"]


def marker(job):
    origin = job.get("origin")
    return origin.get(MARKER) if isinstance(origin, dict) else None


def paused(job):
    return job.get("enabled") is False or job.get("state") == "paused" or bool(job.get("paused_at"))


def configuration(job):
    # Include all execution-affecting fields, even ones this adapter cannot set.
    # Native dispatch/result counters and next-run timestamps are not drift.
    fields = ("prompt", "skills", "skill", "model", "provider", "base_url", "script", "no_agent",
              "monitor_script", "monitor_url", "context_from", "enabled_toolsets", "workdir",
              "deliver", "attach_to_session", "reasoning_effort", "provider_snapshot", "model_snapshot")
    out = {key: job.get(key) for key in fields}
    out["schedule"] = {k: v for k, v in (job.get("schedule") or {}).items() if k != "display"}
    out["repeat_times"] = (job.get("repeat") or {}).get("times")
    return out


def revision(job):
    m = marker(job)
    return digest({"id": job["id"], "config": configuration(job),
                   "paused": paused(job) if not terminal(job) else False,
                   "owner": {k: m[k] for k in ("project_id", "resource", "profile", "desired_hash", "config_hash", "retired")}})


def owned(jobs, project, profile):
    found = {}
    for job in jobs:
        m = marker(job)
        if m is None:
            continue
        if not isinstance(m, dict) or not PROJECT.fullmatch(m.get("project_id", "")) or not KEY.fullmatch(m.get("resource", "")) or m.get("profile") != profile:
            raise ValueError("invalid owner marker")
        if m["project_id"] != project:
            continue
        if m["resource"] in found:
            raise Conflict()
        found[m["resource"]] = job
    return found


def public(job):
    m = marker(job)
    receipt = m["receipt"]
    return dict(id=job["id"], project_id=m["project_id"], resource=m["resource"], profile=m["profile"],
                revision=revision(job), desired_hash=m["desired_hash"], paused=paused(job), retired=m["retired"],
                drift=digest(configuration(job)) != m["config_hash"], token=receipt["token"],
                input_hash=receipt["input_hash"], before_revision=receipt["before_revision"],
                before_id=receipt["before_id"], committed_revision=receipt["committed_revision"])


class Conflict(Exception):
    pass


class TerminalRequiresResume(Exception):
    pass


@contextlib.contextmanager
def transaction(native, initial):
    # This helper is single-request/single-thread; globals are never patched in
    # the Hermes gateway. Original save_jobs owns atomic replace/merge/fsync.
    current = copy.deepcopy(initial)
    load, save = native.load_jobs, native.save_jobs
    def buffered_save(jobs, **kwargs):
        nonlocal current
        if kwargs:
            raise ValueError("unsupported native save mode")
        current = copy.deepcopy(jobs)
    native.load_jobs = lambda: copy.deepcopy(current)
    native.save_jobs = buffered_save
    try:
        yield lambda: current
    finally:
        native.load_jobs, native.save_jobs = load, save


def commit(native, jobs, alive):
    if len(json.dumps({"jobs": jobs}).encode()) > LIMIT - 256:
        raise ValueError("store limit")
    alive()
    native.save_jobs(jobs)


def terminal(job):
    return job.get("state") == "completed" or (job.get("state") == "error" and (job.get("schedule") or {}).get("kind") == "once")


@contextlib.contextmanager
def locked_native_store(native, home):
    # Pinned _jobs_lock degrades to in-process-only on flock timeout. An owner
    # CAS must instead fail closed. Acquire the SAME native lock file, then use
    # its documented nested depth while calling the unmodified native APIs.
    with native.use_cron_store(home), native._jobs_file_lock:
        native.ensure_dirs()
        with open(native._jobs_lock_file(), "a+") as lock:
            deadline = time.monotonic() + LOCK_WAIT_SECONDS
            while True:
                try:
                    fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
                    break
                except BlockingIOError:
                    if time.monotonic() >= deadline:
                        raise TimeoutError("native lock unavailable")
                    time.sleep(0.01)
            native._jobs_lock_state.depth = 1
            native._jobs_lock_state.load_stamp = None
            try:
                yield
            finally:
                native._jobs_lock_state.depth = 0
                native._jobs_lock_state.load_stamp = None
                fcntl.flock(lock, fcntl.LOCK_UN)


def execute(native, home, source, request, alive=lambda: None):
    if request.get("source") != source or source.get("revision") != REVISION or source.get("profile") != "mina":
        raise ValueError("source mismatch")
    project = request.get("project_id", "")
    if not PROJECT.fullmatch(project):
        raise ValueError("invalid project")
    operation = request.get("operation")
    allowed = {"operation", "source", "project_id", "resource", "expected_revision", "expected_id", "token", "input_hash", "spec"}
    if set(request) - allowed or operation not in ("list", "reconcile", "retire", "pause_project"):
        raise ValueError("invalid request")
    if operation == "list":
        selected = owned(strict_jobs(home), project, source["profile"])
        return [public(j) for k, j in sorted(selected.items()) if not request.get("resource") or request["resource"] == k]
    with locked_native_store(native, home):
        alive()
        initial = strict_jobs(home)
        selected = owned(initial, project, source["profile"])
        if operation == "pause_project":
            with transaction(native, initial) as current:
                for job in selected.values():
                    # Mark retired and retain the prior receipt. Restore/reapply
                    # cannot resume this native pause. Terminal jobs stay terminal.
                    m = copy.deepcopy(marker(job)); m["retired"] = True
                    updates = {"origin": {MARKER: m}}
                    if not terminal(job):
                        updates.update(enabled=False, state="paused", paused_at=native._hermes_now().isoformat(), paused_reason="LOOM project archive")
                    native.update_job(job["id"], updates)
                result = current()
            if selected:
                commit(native, result, alive)
            return [public(j) for j in owned(result, project, source["profile"]).values()]
        key = request.get("resource", "")
        if not KEY.fullmatch(key) or not request.get("token") or len(request["token"]) > 256 or not DIGEST.fullmatch(request.get("input_hash", "")):
            raise ValueError("invalid action")
        job = selected.get(key)
        if job and marker(job)["receipt"]["token"] == request["token"]:
            if marker(job)["receipt"]["input_hash"] != request["input_hash"]:
                raise Conflict()
            return [public(job)]
        before = revision(job) if job else "absent"
        before_id = job["id"] if job else ""
        if before != request.get("expected_revision") or before_id != request.get("expected_id", ""):
            raise Conflict()
        if operation == "retire" and not job:
            raise Conflict()
        spec = request.get("spec")
        if operation == "reconcile":
            if not isinstance(spec, dict) or set(spec) != {"schedule", "prompt", "skills", "status", "workdir"}:
                raise ValueError("invalid spec")
            if spec["status"] not in ("active", "paused") or not isinstance(spec["prompt"], str) or not spec["prompt"].strip() or len(spec["prompt"].encode()) > 32768:
                raise ValueError("invalid spec")
            if not isinstance(spec["skills"], list) or len(spec["skills"]) > 16 or any(not isinstance(x, str) or not KEY.fullmatch(x) for x in spec["skills"]):
                raise ValueError("invalid skills")
            native.parse_schedule(spec["schedule"])
            native._normalize_workdir(spec["workdir"])
        with transaction(native, initial) as current:
            m = copy.deepcopy(marker(job)) if job else dict(project_id=project, resource=key, profile=source["profile"])
            m["retired"] = operation == "retire"
            m["receipt"] = dict(token=request["token"], input_hash=request["input_hash"], before_revision=before, before_id=before_id)
            if operation == "reconcile":
                m["desired_hash"] = digest(spec)
                if job is None:
                    job = native.create_job(prompt=spec["prompt"], schedule=spec["schedule"], skills=spec["skills"], workdir=spec["workdir"], deliver="local", origin={MARKER: m}, name="LOOM " + key)
                else:
                    updates = dict(prompt=spec["prompt"], skills=spec["skills"], skill=None, workdir=spec["workdir"], deliver="local")
                    parsed = native.parse_schedule(spec["schedule"])
                    if {k:v for k,v in parsed.items() if k!="display"} != configuration(job)["schedule"]:
                        # Native update_job cannot re-arm terminal records. Do
                        # not reset their history or issue a successful receipt.
                        if terminal(job):
                            raise TerminalRequiresResume()
                        updates["schedule"] = spec["schedule"]
                        if parsed["kind"] != job["schedule"]["kind"]:
                            # Native repeat.times is a lifetime dispatch limit,
                            # not a remaining-run count. Keep completed/history
                            # and allow exactly one future one-shot occurrence.
                            completed = (job.get("repeat") or {}).get("completed", 0)
                            updates["repeat"] = completed + 1 if parsed["kind"] == "once" else None
                    # Never silently clear native execution overrides. Surface
                    # drift and require native operator review before reconciling.
                    if digest(configuration(job)) != m.get("config_hash"):
                        raise Conflict()
                    job = native.update_job(job["id"], updates)
                should_pause = spec["status"] == "paused" or paused(job)
            else:
                should_pause = True
            if should_pause and not terminal(job):
                job = native.pause_job(job["id"], reason="LOOM project declaration")
            m["config_hash"] = digest(configuration(job))
            job["origin"] = {MARKER: m}
            m["receipt"]["committed_revision"] = revision(job)
            # Exact owner metadata joins the last native API write, still buffered.
            native.update_job(job["id"], {"origin": {MARKER: m}})
            result = current()
        commit(native, result, alive)
        return [public(owned(result, project, source["profile"])[key])]


def require_live_caller(conn):
    import socket
    # Python's timeout wrapper waits before recv even with MSG_DONTWAIT.
    # Switch the socket itself to nonblocking for this liveness probe.
    timeout = conn.gettimeout()
    try:
        conn.setblocking(False)
        try:
            conn.recv(1, socket.MSG_PEEK)
        except BlockingIOError:
            return
        raise ConnectionError("caller disconnected or extra request data")
    finally:
        conn.settimeout(timeout)


def serve(binding):
    """Called only by an immutable installed launcher; no CLI/binding override."""
    import pwd
    import signal
    import socket
    import struct
    import sys
    if len(sys.argv) != 1:
        raise SystemExit(2)
    signal.alarm(12)
    conn = socket.socket(fileno=os.dup(0))
    uid = struct.unpack("3i", conn.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, 12))[1]
    if uid == 0 or uid != pwd.getpwnam(binding["caller"]).pw_uid:
        raise SystemExit(2)
    conn.settimeout(10)
    source = binding["source"]
    try:
        stream = conn.makefile("rb")
        raw = stream.readline(65538)
        if len(raw) > 65536 or not raw.endswith(b"\n"):
            raise ValueError("request limit")
        # Pinned immutable launcher provides Python + module path. Select the
        # fixed profile before importing native code; never inherit ambient home.
        os.environ["HERMES_HOME"] = binding["home"]
        # Native creation snapshots provider/model resolution. Supply this
        # profile's own environment without allowing it to redirect the home.
        from dotenv import load_dotenv
        load_dotenv(Path(binding["home"]) / ".env", override=False)
        os.environ["HERMES_HOME"] = binding["home"]
        from cron import jobs as native
        def alive():
            # The caller's project lifecycle lock is held only while connected.
            # Check INSIDE the native lock before any commit: if archive already
            # overtook a cancelled waiter, that waiter must not create afterward.
            require_live_caller(conn)
        result = {"source": source, "jobs": execute(native, binding["home"], source, json.loads(raw), alive)}
    except Conflict:
        result = {"source": source, "jobs": None, "error": "conflict"}
    except TerminalRequiresResume:
        result = {"source": source, "jobs": None, "error": "terminal_requires_native_resume"}
    except Exception:
        result = {"source": source, "jobs": None, "error": "unavailable"}
    payload = json.dumps(result).encode() + b"\n"
    if len(payload) > LIMIT:
        raise SystemExit(2)
    conn.sendall(payload)
    conn.close()
