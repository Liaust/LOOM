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
PLATFORM_REVISION = "d923a5eb487a17c0a68801bba9ef0f56c81040a8"
MARKER = "loom_project_declaration"
BOX_MARKER = "loom_box_schedule"
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
    if not isinstance(origin, dict):
        return None
    if MARKER in origin and BOX_MARKER in origin:
        raise ValueError("ambiguous schedule owner")
    return origin.get(MARKER, origin.get(BOX_MARKER))


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
    if isinstance(job.get("script"), str) and job["script"].startswith("loom-codex-"):
        from native_codex import source_hash
        out["dispatch_source_hash"] = source_hash(job["script"])
    return out


def revision(job):
    m = marker(job)
    fields = ("project_id", "resource", "profile", "desired_hash", "config_hash", "retired")
    if "box_id" in m:
        fields += ("box_id", "context_revision", "workdir")
    return digest({"id": job["id"], "config": configuration(job),
                   "paused": paused(job) if not terminal(job) else False,
                   "owner": {k: m[k] for k in fields}})


def owned(jobs, project, profile, box_id=""):
    found = {}
    for job in jobs:
        m = marker(job)
        if m is None:
            continue
        if isinstance(m, dict) and m.get("box_id", "") != box_id:
            continue
        if not isinstance(m, dict) or (not box_id and not PROJECT.fullmatch(m.get("project_id", ""))) or not KEY.fullmatch(m.get("resource", "")) or m.get("profile") != profile:
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
    extra = dict(box={k: m[k] for k in ("box_id", "profile", "workdir", "context_revision", "retired")}) if "box_id" in m else {}
    return dict(id=job["id"], project_id=m["project_id"], resource=m["resource"], profile=m["profile"],
                revision=revision(job), desired_hash=m["desired_hash"], paused=paused(job), retired=m["retired"],
                drift=digest(configuration(job)) != m["config_hash"], token=receipt["token"],
                input_hash=receipt["input_hash"], before_revision=receipt["before_revision"],
                before_id=receipt["before_id"], committed_revision=receipt["committed_revision"], **extra)


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


def execute_schedule(native, home, source, request, alive=lambda: None, box=None):
    if request.get("source") != source or source.get("revision") not in (REVISION, PLATFORM_REVISION):
        raise ValueError("source mismatch")
    project = request.get("project_id", "")
    if not PROJECT.fullmatch(project) and not (box and project == ""):
        raise ValueError("invalid project")
    operation = request.get("operation")
    allowed = {"operation", "source", "project_id", "resource", "expected_revision", "expected_id", "token", "input_hash", "spec"}
    if set(request) - allowed or operation not in ("list", "reconcile", "retire", "pause_project"):
        raise ValueError("invalid request")
    owner_key = BOX_MARKER if box else MARKER
    box_context = {k: box[k] for k in ("box_id", "profile", "workdir", "context_revision")} if box else None
    def select(jobs):
        selected = owned(jobs, project, source["profile"], box["box_id"] if box else "")
        if box and any(any(marker(j).get(k) != box[k] for k in ("box_id", "profile", "workdir", "context_revision")) for j in selected.values()):
            raise Conflict()
        return selected
    if operation == "list":
        selected = select(strict_jobs(home))
        return [public(j) for k, j in sorted(selected.items()) if not request.get("resource") or request["resource"] == k]
    with locked_native_store(native, home):
        alive()
        initial = strict_jobs(home)
        selected = select(initial)
        if operation == "pause_project":
            with transaction(native, initial) as current:
                for job in selected.values():
                    # Mark retired and retain the prior receipt. Restore/reapply
                    # cannot resume this native pause. Terminal jobs stay terminal.
                    m = copy.deepcopy(marker(job)); m["retired"] = True
                    updates = {"origin": {owner_key: m}}
                    if not terminal(job):
                        updates.update(enabled=False, state="paused", paused_at=native._hermes_now().isoformat(), paused_reason="LOOM project archive")
                    native.update_job(job["id"], updates)
                result = current()
            if selected:
                commit(native, result, alive)
            return [public(j) for j in select(result).values()]
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
            if not isinstance(spec, dict) or set(spec) - {"schedule", "prompt", "skills", "status", "workdir", "codex"} or not {"schedule", "prompt", "skills", "status", "workdir"} <= set(spec):
                raise ValueError("invalid spec")
            if "codex" in spec:
                from native_codex import valid
                if source["revision"] != PLATFORM_REVISION or not valid(spec["codex"]) or spec["skills"]:
                    raise ValueError("invalid Codex target")
            if spec["status"] not in ("active", "paused") or not isinstance(spec["prompt"], str) or not spec["prompt"].strip() or len(spec["prompt"].encode()) > 32768:
                raise ValueError("invalid spec")
            if not isinstance(spec["skills"], list) or len(spec["skills"]) > 16 or any(not isinstance(x, str) or not KEY.fullmatch(x) for x in spec["skills"]):
                raise ValueError("invalid skills")
            native.parse_schedule(spec["schedule"])
            native._normalize_workdir(spec["workdir"])
            if box:
                workdir = Path(spec["workdir"])
                if not workdir.is_absolute() or not workdir.is_dir() or workdir.resolve() != workdir or not workdir.is_relative_to(Path(box["workdir"])):
                    raise ValueError("schedule workdir is outside the bound Box")
        with transaction(native, initial) as current:
            m = copy.deepcopy(marker(job)) if job else dict(project_id=project, resource=key, profile=source["profile"])
            if box:
                m.update({k: box[k] for k in ("box_id", "profile", "workdir", "context_revision")})
            m["retired"] = operation == "retire"
            m["receipt"] = dict(token=request["token"], input_hash=request["input_hash"], before_revision=before, before_id=before_id)
            if operation == "reconcile":
                m["desired_hash"] = digest(spec)
                dispatch = {}
                if spec.get("codex"):
                    from native_codex import script_name
                    dispatch = dict(no_agent=True, script=script_name(project, key, spec, box_context))
                if job is None:
                    job = native.create_job(prompt=spec["prompt"], schedule=spec["schedule"], skills=spec["skills"], workdir=spec["workdir"], deliver="local", origin={owner_key: m}, name="LOOM " + key, **dispatch)
                else:
                    updates = dict(prompt=spec["prompt"], skills=spec["skills"], skill=None, workdir=spec["workdir"], deliver="local")
                    if source["revision"] == PLATFORM_REVISION:
                        updates.update(dispatch or dict(no_agent=False, script=None))
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
                if spec.get("codex"):
                    from native_codex import install
                    install(home, project, key, job["id"], spec, box_context)
                should_pause = spec["status"] == "paused" or paused(job)
            else:
                should_pause = True
            if should_pause and not terminal(job):
                job = native.pause_job(job["id"], reason="LOOM project declaration")
            m["config_hash"] = digest(configuration(job))
            job["origin"] = {owner_key: m}
            m["receipt"]["committed_revision"] = revision(job)
            # Exact owner metadata joins the last native API write, still buffered.
            native.update_job(job["id"], {"origin": {owner_key: m}})
            result = current()
        commit(native, result, alive)
        return [public(select(result)[key])]


@contextlib.contextmanager
def profile_environment(home):
    from hermes_constants import set_hermes_home_override, reset_hermes_home_override
    from agent.secret_scope import build_profile_secret_scope, set_secret_scope, reset_secret_scope
    from tools.terminal_scope import install_and_reset_profile_terminal_scope
    token = set_hermes_home_override(str(home))
    secret = None
    try:
        secret = set_secret_scope(build_profile_secret_scope(Path(home)), profile_home=str(home))
        with install_and_reset_profile_terminal_scope(home):
            yield
    finally:
        if secret is not None:
            reset_secret_scope(secret)
        reset_hermes_home_override(token)


def scoped_schedule(native, home, source, request, alive):
    with profile_environment(home):
        return execute_schedule(native, home, source, request, alive)


def execute(native, home, source, request, alive=lambda: None, boxes_file=None):
    if str(request.get("operation", "")).startswith("box_"):
        if source.get("profile") != "mina" or request.get("source") != source or source.get("revision") != PLATFORM_REVISION:
            raise ValueError("source mismatch")
        from native_boxes import execute as box_execute
        return box_execute(native, home, source, request, alive, boxes_file)
    if str(request.get("operation", "")).startswith("workspace_"):
        if source.get("profile") != "mina" or request.get("source") != source or source.get("revision") != PLATFORM_REVISION:
            raise ValueError("source mismatch")
        from native_workspace import execute as workspace_execute
        return workspace_execute(native, home, request, alive, boxes_file)
    box_event = request.get("operation") == "dispatch_event" and re.fullmatch(r"box_[A-Za-z0-9_-]+", request.get("box_id", "")) and request.get("project_id", "") == ""
    if source.get("profile") != "mina" or request.get("source") != source or source.get("revision") not in (REVISION, PLATFORM_REVISION) or not (PROJECT.fullmatch(request.get("project_id", "")) or box_event):
        raise ValueError("source mismatch")
    if source["revision"] == REVISION:
        return execute_schedule(native, home, source, request, alive)
    import native_profiles as profiles
    allowed = {"operation", "source", "project_id", "resource", "expected_revision", "expected_id", "token", "input_hash", "spec", "agent", "profile", "event", "box_id"}
    if set(request) - allowed:
        raise ValueError("invalid request")
    with profiles.locked(home):
        operation, project = request.get("operation"), request["project_id"]
        if operation == "dispatch_event":
            from native_events import dispatch
            return dispatch(native, home, request, alive, boxes_file)
        selected = profiles.profiles(home, project)
        if operation in ("reconcile_agent", "retire_agent"):
            def retire():
                name = profiles.profile_id(project, request.get("resource", ""))
                for directory, _ in [(d, m) for d, m in selected if d.name == name]:
                    local = dict(source, profile=directory.name)
                    scoped_schedule(native, directory, local, dict(operation="pause_project", source=local, project_id=project), alive)
            return profiles.execute(home, request, alive, retire)
        stores = [(Path(home), source)] + [(d, dict(source, profile=d.name)) for d, _ in selected]
        if operation in ("list", "pause_project"):
            result = []
            for directory, local in stores:
                result.extend(scoped_schedule(native, directory, local, dict(request, source=local), alive))
                if operation == "pause_project":
                    from native_events import pause_project
                    with profile_environment(directory):
                        pause_project(native, directory, project, alive)
            if operation == "list":
                result.extend(profiles.public(d, m) for d, m in selected if not request.get("resource") or m["resource"] == request["resource"])
            if boxes_file:
                from native_boxes import profiles as box_profiles, resolve as resolve_box_profile
                for directory, box in box_profiles(home):
                    if not owned(strict_jobs(directory), project, directory.name, box["box_id"]) and not any((j.get("origin") or {}).get("loom_internal_events", {}).get("project_id") == project for j in strict_jobs(directory)):
                        continue
                    directory, box = resolve_box_profile(home, box["box_id"], directory.name, boxes_file)
                    local = dict(source, profile=directory.name)
                    with profile_environment(directory):
                        result.extend(execute_schedule(native, directory, local, dict(request, source=local), alive, box=box))
                        if operation == "pause_project":
                            from native_events import pause_project
                            pause_project(native, directory, project, alive)
            return result
        spec = copy.deepcopy(request.get("spec"))
        name = spec.pop("profile", "") if isinstance(spec, dict) else ""
        if name:
            stores = [(d, s) for d, s in stores if s["profile"] == name]
            marker = next((m for d, m in selected if d.name == name), None)
            if marker is None or marker["retired"]:
                raise Conflict()
        else:
            # Existing jobs retain their original profile when withdrawn/edited.
            found = [(d, s) for d, s in stores if request.get("resource") in owned(strict_jobs(d), project, s["profile"])]
            stores = found or stores[:1]
        if len(stores) != 1:
            raise Conflict()
        directory, local = stores[0]
        forwarded = {k: v for k, v in request.items() if k != "agent"}
        forwarded["source"] = local
        if spec is not None:
            forwarded["spec"] = spec
        return scoped_schedule(native, directory, local, forwarded, alive)


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
        # Each selected profile loads only its own environment below, never
        # MINA's credentials into another profile's scheduling context.
        from cron import jobs as native
        if source["revision"] == REVISION:
            from dotenv import load_dotenv
            load_dotenv(Path(binding["home"]) / ".env", override=False)
            os.environ["HERMES_HOME"] = binding["home"]
        def alive():
            # The caller's project lifecycle lock is held only while connected.
            # Check INSIDE the native lock before any commit: if archive already
            # overtook a cancelled waiter, that waiter must not create afterward.
            require_live_caller(conn)
        request = json.loads(raw)
        value = execute(native, binding["home"], source, request, alive, binding.get("boxes_file"))
        result = {"source": source, "event" if request.get("operation") == "dispatch_event" else "jobs": value}
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
