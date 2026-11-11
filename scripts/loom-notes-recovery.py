#!/usr/bin/env python3
"""Cold Notes cohort through existing backup/cloud owners; run by its Nix unit."""
import datetime as dt
import ctypes
import fcntl
import hashlib
import json
import math
import os
from pathlib import Path
import re
import shlex
import signal
import stat
import subprocess
import sys
import tempfile
import time
import traceback
import uuid
from contextlib import contextmanager

TABLES = ("files", "bases", "reference_payloads", "reference_dedup_runs",
          "operations", "recoveries", "path_history", "sync_replicas", "sync_records")


@contextmanager
def recovery_request(state):
    path = state / "recovery.request"
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    identity = os.fstat(fd)
    os.close(fd)
    try:
        yield
    finally:
        if path.exists():
            current = path.lstat()
            if (current.st_dev, current.st_ino) != (identity.st_dev, identity.st_ino):
                raise ValueError("recovery request was replaced")
            path.unlink()


@contextmanager
def lock_file(path):
    fd = os.open(path, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, "r+") as lock:
        info = os.fstat(lock.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1:
            raise ValueError("invalid recovery lock")
        yield lock


def acquire_lock(lock, timeout=120):
    deadline = time.monotonic() + timeout
    while True:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            return
        except BlockingIOError:
            if time.monotonic() >= deadline:
                raise TimeoutError("Notes replica did not quiesce") from None
            time.sleep(0.1)


@contextmanager
def publisher_lock(state):
    # Serialize publishers/stop cleanup independently of the replica fence.
    with lock_file(state / "recovery.publisher.lock") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        yield


@contextmanager
def operational_lease(root):
    # Shares the existing backup/update/cloud lifetime fence with operator GC.
    # Root publication paths themselves are checked by exact_directory.
    path = str(exact_directory(root)) + ".retention.lock"
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(fd, "rb") as lock:
        info = os.fstat(lock.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1:
            raise ValueError("invalid operational retention lease")
        fcntl.flock(lock, fcntl.LOCK_SH | fcntl.LOCK_NB)
        yield


@contextmanager
def recovery_lock(state, timeout=120):
    # The persistent replica releases its fence only after seeing this request.
    # Close the fence before removing the marker so the resume wake can acquire it.
    with publisher_lock(state), recovery_request(state), lock_file(state / "recovery.lock") as lock:
        import pwd
        owner = pwd.getpwnam("loom")
        os.fchown(lock.fileno(), owner.pw_uid, owner.pw_gid)
        with phase("replica_quiescence"):
            acquire_lock(lock, timeout)
        yield


def cleanup(config_path):
    """Systemd backstop after the entire capture cgroup has stopped."""
    cfg = json.loads(Path(config_path).read_text())
    state = exact_directory(cfg["state_dir"])
    with publisher_lock(state):
        try:
            resume_source_writers(cfg, state)
        finally:
            resume_notes_transport(state)


def resume_notes_transport(state):
    run("systemctl", "start", "couchdb.service")
    run("systemctl", "is-active", "--quiet", "couchdb.service")
    path = state / "recovery.request"
    try:
        info = path.lstat()
    except FileNotFoundError:
        return
    if (not stat.S_ISREG(info.st_mode) or info.st_uid != 0
            or info.st_nlink != 1 or info.st_size != 0 or stat.S_IMODE(info.st_mode) != 0o600):
        raise ValueError("invalid recovery request")
    with lock_file(state / "recovery.lock") as lock:
        acquire_lock(lock)
        if path.lstat() != info:
            raise ValueError("recovery request changed during cleanup")
    path.unlink()


@contextmanager
def phase(name):
    started = time.monotonic()
    print(json.dumps({"phase": name, "status": "started"}), flush=True)
    try:
        yield
    finally:
        print(json.dumps({"phase": name, "elapsed_seconds": round(time.monotonic() - started, 3)}), flush=True)


def run(*args, output=False):
    return subprocess.run(args, check=True, text=True, stdout=subprocess.PIPE if output else subprocess.DEVNULL,
                          stderr=subprocess.PIPE).stdout


def source_writer_bindings(cfg):
    bindings = cfg.get("source_writers", {})
    for root, units in bindings.items():
        if not Path(root).is_absolute() or str(Path(root)) != root or ".." in Path(root).parts:
            raise ValueError("invalid Notes source writer root")
        if not isinstance(units, list) or not units:
            raise ValueError("invalid Notes source writer units")
        for unit in units:
            if not isinstance(unit, str) or not re.fullmatch(r"loom-application@la-[0-9a-f]{24}\.service", unit):
                raise ValueError("invalid Notes source writer service")
    return bindings


def sync_directory(path):
    fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def resume_source_writers(cfg, state):
    path = state / "recovery.writers.json"
    try:
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    except FileNotFoundError:
        return
    with os.fdopen(fd, "rb") as stream:
        info = os.fstat(stream.fileno())
        if (not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid()
                or info.st_nlink != 1 or stat.S_IMODE(info.st_mode) != 0o600 or info.st_size > 16384):
            raise ValueError("invalid source writer resume receipt")
        value = json.load(stream)
    allowed = {unit for units in source_writer_bindings(cfg).values() for unit in units}
    units = value.get("units")
    if (value.get("schema") != "loom.notes_recovery.writers.v1" or not isinstance(units, list)
            or any(not isinstance(unit, str) or unit not in allowed for unit in units)
            or len(units) != len(set(units))):
        raise ValueError("invalid source writer resume identity")
    failure = None
    for unit in units:
        try:
            run("systemctl", "start", unit)
            run("systemctl", "is-active", "--quiet", unit)
        except Exception as error:
            failure = failure or error
    if failure is not None:
        raise failure
    current = path.lstat()
    if (info.st_dev, info.st_ino) != (current.st_dev, current.st_ino):
        raise ValueError("source writer resume receipt was replaced")
    path.unlink()
    sync_directory(state)


@contextmanager
def quiesce_source_writers(cfg, state, roots):
    # Only host-declared writers whose source intersects this enrollment are
    # stopped. Persist restart intent before stop; ExecStopPost handles SIGKILL.
    bindings = source_writer_bindings(cfg)
    selected = sorted({unit for root, units in bindings.items()
                       if any(Path(root) == path or Path(root) in path.parents or path in Path(root).parents
                              for path in roots) for unit in units})
    path = state / "recovery.writers.json"
    if path.exists() or path.is_symlink():
        raise ValueError("unresolved source writer resume receipt")
    active = []
    for unit in selected:
        properties = dict(line.split("=", 1) for line in run("systemctl", "show", unit,
                          "--property=LoadState,ActiveState", output=True).splitlines())
        if properties.get("LoadState") != "loaded":
            raise ValueError("Notes source writer service unavailable")
        status = properties.get("ActiveState")
        if status == "active":
            active.append(unit)
        elif status not in ("inactive", "failed"):
            raise ValueError("Notes source writer service is transitioning")
    if not active:
        yield []
        return
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, "w") as stream:
        json.dump({"schema": "loom.notes_recovery.writers.v1", "units": active}, stream)
        stream.flush()
        os.fsync(stream.fileno())
    sync_directory(state)
    try:
        for unit in active:
            run("systemctl", "stop", unit)
            if run("systemctl", "show", unit, "--property=ActiveState", "--value", output=True).strip() not in ("inactive", "failed"):
                raise ValueError("Notes source writer did not stop")
        yield active
    finally:
        resume_source_writers(cfg, state)


def compare_archive(archive):
    try:
        run("tar", "--compare", "--acls", "--xattrs", "--numeric-owner", "-zf", str(archive), "-C", "/", output=True)
    except subprocess.CalledProcessError as error:
        # Tar describes differences on stdout. Keep bounded private diagnostics,
        # not command arguments or source names in the public service journal.
        path = archive.parent / "comparison-failure.json"
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        with os.fdopen(fd, "w") as stream:
            json.dump({"schema": "loom.notes_recovery.comparison_failure.v1",
                       "returncode": error.returncode,
                       "stdout": (error.stdout or "")[:8192],
                       "stderr": (error.stderr or "")[:8192]}, stream)
            stream.flush()
            os.fsync(stream.fileno())
        raise


def create_archive(archive, relative):
    try:
        run("tar", "--acls", "--xattrs", "--numeric-owner", "--use-compress-program=pigz -1 -p 2",
            "-cf", str(archive), "-C", "/", "--", *relative)
    except subprocess.CalledProcessError as error:
        atomic_recovery_state(archive.parent / "archive-failure.json", {
            "schema": "loom.notes_recovery.archive_failure.v1", "returncode": error.returncode,
            "stderr": (error.stderr or "")[:8192], "stdout": (error.stdout or "")[:8192]})
        raise


def digest(path):
    with open(path, "rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def copy_verified_package(source, destination):
    """Hash while copying, so the publication attests to the actual copy."""
    with (source / "manifest.json").open("rb") as reader:
        manifest_raw = reader.read(4 * 1024 * 1024 + 1)
    if len(manifest_raw) > 4 * 1024 * 1024:
        raise ValueError("operational manifest exceeds bound")
    manifest = json.loads(manifest_raw)
    manifest_hash = hashlib.sha256(manifest_raw).hexdigest()
    expected = {"manifest.json": (len(manifest_raw), manifest_hash)}
    hash_raw = (manifest_hash + "  manifest.json\n").encode()
    expected["manifest.sha256"] = (len(hash_raw), hashlib.sha256(hash_raw).hexdigest())
    for item in manifest["artifacts"]:
        name = item["path"]
        if not name or Path(name).name != name or name in expected:
            raise ValueError("invalid operational copy inventory")
        expected[name] = (item["size_bytes"], item["sha256"])
    if len(expected) > 30 or {p.name for p in source.iterdir()} != set(expected):
        raise ValueError("operational copy inventory differs")
    destination.mkdir(mode=0o700)
    for name, (size, checksum) in expected.items():
        fd = os.open(source / name, os.O_RDONLY | os.O_NOFOLLOW)
        with os.fdopen(fd, "rb") as reader, (destination / name).open("xb") as writer:
            before = os.fstat(reader.fileno())
            if not stat.S_ISREG(before.st_mode) or before.st_nlink != 1 or before.st_size != size:
                raise ValueError("invalid operational copy input")
            os.fchmod(writer.fileno(), 0o600)
            sha, copied = hashlib.sha256(), 0
            while chunk := reader.read(1024 * 1024):
                copied += len(chunk)
                if copied > size:
                    raise ValueError("operational copy grew")
                sha.update(chunk)
                writer.write(chunk)
            after = os.fstat(reader.fileno())
            identity = lambda st: (st.st_dev, st.st_ino, st.st_size, st.st_mtime_ns, st.st_ctime_ns, st.st_nlink)
            if identity(before) != identity(after) or copied != size or sha.hexdigest() != checksum:
                raise ValueError("operational copy changed or hash differs")
            writer.flush()
            os.fsync(writer.fileno())
    if (source / "manifest.json").read_bytes() != manifest_raw:
        raise ValueError("operational manifest changed during copy")
    return manifest_hash


def write_operational_reference(package, checkpoint, destination):
    with (package / "manifest.json").open("rb") as stream:
        raw = stream.read(4 * 1024 * 1024 + 1)
    if len(raw) > 4 * 1024 * 1024:
        raise ValueError("operational manifest exceeds bound")
    manifest = json.loads(raw)
    sha = hashlib.sha256(raw).hexdigest()
    if (manifest.get("schema") != "loom.operational_backup.v1" or manifest.get("package_id") != package.name
            or checkpoint.get("manifest_sha256") != sha):
        raise ValueError("operational reference differs from the committed backup")
    reference = {"schema": "loom.notes_recovery.operational_reference.v1",
                 "package_id": package.name, "manifest_sha256": sha}
    payload = (json.dumps(reference, sort_keys=True) + "\n").encode()
    with destination.open("xb") as stream:
        stream.write(payload)
        stream.flush()
        os.fsync(stream.fileno())
    return sha, hashlib.sha256(payload).hexdigest()


def sql_fingerprint(database):
    result = {}
    for table in TABLES:
        # Keep the existing count|md5 receipt exactly, without string_agg's
        # unbounded aggregate allocation. PostgreSQL sorts only 32-byte hashes.
        query = ("COPY (SELECT hash FROM (SELECT md5(to_jsonb(t)::text) AS hash FROM notes_workspace."
                 + table + " t) rows ORDER BY hash COLLATE \"C\") TO STDOUT")
        with phase("database_fingerprint_" + table), tempfile.TemporaryFile() as errors:
            with subprocess.Popen(["runuser", "-u", "postgres", "--", "psql", "-XqAt",
                                   "-d", database, "-c", query], stdout=subprocess.PIPE, stderr=errors) as process:
                try:
                    fingerprint = fingerprint_rows(process.stdout)
                except BaseException:
                    process.kill()
                    raise
                if process.wait() != 0:
                    raise ValueError("database fingerprint failed")
                result[table] = fingerprint
    return result


def fingerprint_rows(lines):
    checksum, count = hashlib.md5(), 0
    for line in lines:
        value = line.rstrip(b"\n")
        if len(value) != 32 or any(c not in b"0123456789abcdef" for c in value):
            raise ValueError("invalid database row fingerprint")
        checksum.update(value)
        count += 1
    return str(count) + "|" + checksum.hexdigest()


def exact_directory(path):
    p = Path(path)
    if not p.is_absolute() or str(p) != path or not p.is_dir():
        raise ValueError("invalid recovery directory")
    if any(parent.is_symlink() for parent in (p, *p.parents)):
        raise ValueError("recovery path is a symlink")
    return p


def atomic_recovery_state(path, value):
    payload = (json.dumps(value, sort_keys=True) + "\n").encode()
    if len(payload) > 64 * 1024:
        raise ValueError("recovery state exceeds bound")
    fd, temporary = tempfile.mkstemp(prefix=".recovery-state-", dir=path.parent)
    try:
        with os.fdopen(fd, "wb") as stream:
            os.fchmod(stream.fileno(), 0o600)
            stream.write(payload)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
        dfd = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(dfd)
        finally:
            os.close(dfd)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def record_attempt(state, attempt):
    directory = state / "recovery-attempts"
    directory.mkdir(mode=0o700, exist_ok=True)
    exact_directory(str(directory))
    atomic_recovery_state(directory / (attempt["id"] + ".json"), attempt)
    atomic_recovery_state(state / "recovery-attempt.json", attempt)


@contextmanager
def capture_phase(name, state, attempt):
    attempt.update(phase=name, phase_started_at=dt.datetime.now(dt.timezone.utc).isoformat())
    attempt.pop("phase_finished_at", None)
    record_attempt(state, attempt)
    with phase(name):
        yield
    attempt["phase_finished_at"] = dt.datetime.now(dt.timezone.utc).isoformat()
    record_attempt(state, attempt)


def failed_inventory(root):
    """Identity/metadata only: retaining a failed archive does not verify its bytes."""
    entries, stack = [], [(root, "", 0)]
    while stack:
        path, relative, depth = stack.pop()
        info = path.lstat()
        directory = stat.S_ISDIR(info.st_mode)
        if (len(entries) >= 10000 or depth > 4 or info.st_uid != os.geteuid()
                or stat.S_IMODE(info.st_mode) & 0o077
                or not (directory or stat.S_ISREG(info.st_mode))
                or not directory and info.st_nlink != 1):
            raise ValueError("failed capture custody or inventory invalid")
        entries.append({"path": relative, "device": info.st_dev, "inode": info.st_ino,
                        "mode": info.st_mode, "uid": info.st_uid, "gid": info.st_gid,
                        "size": info.st_size, "mtime_ns": info.st_mtime_ns,
                        "ctime_ns": info.st_ctime_ns if relative else 0,
                        "allocated_bytes": info.st_blocks * 512})
        if directory:
            for child in path.iterdir():
                if len(entries) + len(stack) >= 10000:
                    raise ValueError("failed capture inventory exceeds bound")
                stack.append((child, str(child.relative_to(root)), depth + 1))
    return sorted(entries, key=lambda item: item["path"])


def read_attempt(state, capture_id):
    for path in (state / "recovery-attempts" / (capture_id + ".json"), state / "recovery-attempt.json"):
        try:
            fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
        except FileNotFoundError:
            continue
        with os.fdopen(fd, "rb") as stream:
            info = os.fstat(stream.fileno())
            if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or info.st_uid != os.geteuid() or info.st_size > 65536:
                raise ValueError("invalid attempt evidence")
            raw = stream.read(65537)
        value = json.loads(raw)
        if value.get("id") == capture_id:
            return value
    raise ValueError("matching capture attempt evidence missing")


def failed_capture_id(name):
    match = re.fullmatch(r"(notes-[0-9]{8}T[0-9]{6}Z-[0-9a-f]{8})-[a-z0-9_]{6,16}", name)
    if not match:
        raise ValueError("invalid failed capture name")
    return match[1]


def failed_capture_roots(cfg):
    staging, held = (exact_directory(cfg[key]) for key in ("staging", "held_staging"))
    if staging.stat().st_dev != held.stat().st_dev or staging == held or staging in held.parents or held in staging.parents:
        raise ValueError("failed capture hold requires separate same-filesystem roots")
    return staging, held


def failed_capture_plan(cfg, state, name):
    capture_id = failed_capture_id(name)
    staging, held = failed_capture_roots(cfg)
    if run("systemctl", "show", "loom-notes-recovery.service", "--property=ActiveState", "--value", output=True).strip() not in ("inactive", "failed"):
        raise ValueError("recovery producer is active or unknown")
    attempt = read_attempt(state, capture_id)
    if attempt.get("status") != "failed" or attempt.get("staging_name") != name:
        raise ValueError("capture is not a proven failed attempt")
    source, target = staging / name, held / name
    if target.exists() or target.is_symlink():
        raise ValueError("failed capture hold destination exists")
    exact_directory(str(source))
    if (source / "cohort.json").exists():
        raise ValueError("published/coherent evidence is not a failed staging hold")
    plan = {"schema": "loom.notes_recovery.failed_hold.v1", "id": capture_id, "name": name,
            "source": str(source), "destination": str(target), "inventory": failed_inventory(source),
            "attempt": attempt}
    plan["plan_hash"] = "sha256:" + hashlib.sha256(json.dumps(plan, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
    return plan


def rename_noreplace(source, destination):
    library = ctypes.CDLL(None, use_errno=True)
    rename = getattr(library, "renameat2", None)
    if rename is None:
        raise ValueError("atomic no-replace rename unavailable")
    rename.argtypes = [ctypes.c_int, ctypes.c_char_p, ctypes.c_int, ctypes.c_char_p, ctypes.c_uint]
    rename.restype = ctypes.c_int
    if rename(-100, os.fsencode(source), -100, os.fsencode(destination), 1) != 0:
        code = ctypes.get_errno()
        raise OSError(code, os.strerror(code))


def require_operator():
    if os.geteuid() != 0:
        raise ValueError("failed capture review requires root")


def review_failed_capture(config_path, name, expected=None, reason=None):
    require_operator()
    failed_capture_id(name)
    cfg = json.loads(Path(config_path).read_text())
    failed_capture_roots(cfg)
    state = exact_directory(cfg["state_dir"])
    with operational_lease(cfg["operational_root"]), publisher_lock(state):
        records = state / "recovery-holds"
        records.mkdir(mode=0o700, exist_ok=True)
        exact_directory(str(records))
        receipt_path = records / (name + ".json")
        prior = None
        if expected and receipt_path.exists():
            with os.fdopen(os.open(receipt_path, os.O_RDONLY | os.O_NOFOLLOW), "rb") as stream:
                info = os.fstat(stream.fileno())
                if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or info.st_uid != os.geteuid() or info.st_size > 65536:
                    raise ValueError("invalid failed capture hold receipt")
                prior = json.load(stream)
            if prior.get("plan_hash") != expected or prior.get("reason") != reason:
                raise ValueError("failed capture hold replay conflict")
            if (prior.get("schema") != "loom.notes_recovery.failed_hold.v1" or prior.get("name") != name
                    or prior.get("source") != str(Path(cfg["staging"]) / name)
                    or prior.get("destination") != str(Path(cfg["held_staging"]) / name)):
                raise ValueError("failed capture hold receipt scope invalid")
            source, target = Path(prior["source"]), Path(prior["destination"])
            if not source.exists() and target.exists() and failed_inventory(target) == prior["inventory"]:
                prior["status"] = "held"
                atomic_recovery_state(receipt_path, prior)
                return prior
            if prior.get("status") != "prepared" or not source.exists() or target.exists():
                raise ValueError("failed capture hold requires exact operator reconciliation")
        plan = failed_capture_plan(cfg, state, name)
        if expected is None:
            return plan
        if plan["plan_hash"] != expected or not reason or len(reason) > 1000 or any(ord(c) < 32 for c in reason):
            raise ValueError("reviewed failed capture identity/reason changed")
        receipt = prior or {**plan, "status": "prepared", "reason": reason,
                            "reviewed_at": dt.datetime.now(dt.timezone.utc).isoformat()}
        atomic_recovery_state(receipt_path, receipt)
        # Recheck after durable intent; no user payload is read, copied or deleted.
        if failed_capture_plan(cfg, state, name)["plan_hash"] != expected:
            raise ValueError("failed capture changed after review")
        rename_noreplace(plan["source"], plan["destination"])
        for parent in (Path(plan["source"]).parent, Path(plan["destination"]).parent):
            fd = os.open(parent, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
            try:
                os.fsync(fd)
            finally:
                os.close(fd)
        if failed_inventory(Path(plan["destination"])) != plan["inventory"]:
            raise ValueError("held capture identity requires review")
        receipt["status"] = "held"
        atomic_recovery_state(receipt_path, receipt)
        return receipt


def bounded_recovery_allocation(root, seen=None):
    """Metadata only, with a fixed bound even for malformed/unknown trees."""
    count, total = 0, 0
    seen = set() if seen is None else seen
    stack = [(root, 0)]
    while stack:
        path, depth = stack.pop()
        info = path.lstat()
        count += 1
        if count > 10000 or depth > 4 or not (stat.S_ISDIR(info.st_mode) or stat.S_ISREG(info.st_mode)):
            raise ValueError("recovery inventory is unbounded or contains an unknown entry")
        key = (info.st_dev, info.st_ino)
        if key not in seen:
            total += info.st_blocks * 512
            seen.add(key)
        if stat.S_ISDIR(info.st_mode):
            for entry in path.iterdir():
                if count + len(stack) >= 10000:
                    raise ValueError("recovery inventory exceeds entry bound")
                stack.append((entry, depth + 1))
    return total


def recovery_admission(cfg, state):
    policy = cfg.get("capacity", {})
    mode = policy.get("mode", "off")
    if mode not in ("off", "observe", "enforce"):
        raise ValueError("invalid capacity admission mode")
    result = {"schema": "loom.notes_recovery.admission.v1", "observed_at": dt.datetime.now(dt.timezone.utc).isoformat(),
              "mode": mode, "allowed": True, "reasons": []}
    if mode == "off":
        return result
    gib = 1024 ** 3
    settings = {"operational_budget_bytes": 96 * gib, "notes_budget_bytes": 192 * gib,
                "operational_free_floor_bytes": 50 * gib, "notes_free_floor_bytes": 100 * gib,
                "initial_operational_bytes": 16 * gib, "initial_notes_bytes": 64 * gib, "max_pending": 1}
    for key, default in settings.items():
        value = policy.get(key, default)
        if type(value) is not int or value <= 0:
            raise ValueError("invalid capacity setting: " + key)
        settings[key] = value
    try:
        operational, destination, staging = (exact_directory(cfg[key]) for key in ("operational_root", "destination", "staging"))
        plan = json.loads(run(cfg["loom"], "--config", "/etc/loom/loom.env", "backup", "retention", "plan",
                              "--scope", "notes", "--cloud-config", cfg["cloud_config"], "--json", output=True))
        entries = plan["entries"]
        pending = sum(1 for entry in entries if not entry.get("protection_sha256")) + sum(1 for _ in staging.iterdir())
        op_bytes = bounded_recovery_allocation(operational)
        shared = set()
        notes_bytes = bounded_recovery_allocation(destination, shared) + bounded_recovery_allocation(staging, shared)
        # Reviewed historical failures are not active attempts, but their bytes
        # still count. Only an operator may move identities into this hold.
        held_count = 0
        if cfg.get("held_staging"):
            held = exact_directory(cfg["held_staging"])
            notes_bytes += bounded_recovery_allocation(held, shared)
            held_count = sum(1 for _ in held.iterdir())
        op_sizes = [bounded_recovery_allocation(entry) for entry in operational.iterdir() if entry.is_dir() and not entry.is_symlink()]
        op_forecast = max(op_sizes, default=settings["initial_operational_bytes"]) * 5 // 4
        # A custom dump can coexist with the package copy before publication.
        op_peak = op_forecast * 2
        notes_forecast = max((entry.get("allocated_bytes", 0) for entry in entries), default=0)
        notes_forecast = (notes_forecast or settings["initial_notes_bytes"]) * 5 // 4
        op_stat, notes_stat = os.statvfs(operational), os.statvfs(staging)
        op_free, notes_free = op_stat.f_bavail * op_stat.f_frsize, notes_stat.f_bavail * notes_stat.f_frsize
        reasons = result["reasons"]
        if not plan.get("accounting_complete"):
            reasons.append("capture_inventory_incomplete")
        if pending >= settings["max_pending"]:
            reasons.append("pending_or_failed_capture_requires_attention")
        if op_bytes + op_forecast > settings["operational_budget_bytes"]:
            reasons.append("operational_working_set_over_budget")
        if notes_bytes + notes_forecast > settings["notes_budget_bytes"]:
            reasons.append("notes_working_set_over_budget")
        if op_free < settings["operational_free_floor_bytes"] + op_peak:
            reasons.append("operational_headroom_insufficient")
        if notes_free < settings["notes_free_floor_bytes"] + notes_forecast:
            reasons.append("notes_headroom_insufficient")
        if operational.stat().st_dev == staging.stat().st_dev and min(op_free, notes_free) < max(settings["operational_free_floor_bytes"], settings["notes_free_floor_bytes"]) + op_peak + notes_forecast:
            reasons.append("shared_filesystem_headroom_insufficient")
        result.update(pending_captures=pending, historical_held_captures=held_count,
                      operational_allocated_bytes=op_bytes, notes_allocated_bytes=notes_bytes,
                      operational_forecast_bytes=op_forecast, notes_forecast_bytes=notes_forecast,
                      operational_peak_bytes=op_peak,
                      operational_available_bytes=op_free, notes_available_bytes=notes_free)
    except Exception:
        result["reasons"] = ["recovery_inventory_unavailable"]
    result["allowed"] = mode != "enforce" or not result["reasons"]
    atomic_recovery_state(state / "recovery-admission.json", result)
    return result


def verify_backup_result(value):
    worker = value["data"]["worker"]
    checkpoint = worker["latest_checkpoint"]
    if not value["ok"] or worker["last_run"]["run_status"] != "succeeded" or not checkpoint["checkpoint_json"]["committed"]:
        raise ValueError("operational backup did not commit")
    if checkpoint["updated_by_run_id"] != worker["last_run"]["worker_run_id"]:
        raise ValueError("operational backup returned an older checkpoint")
    return checkpoint["checkpoint_json"]


def runtime_digest(runtime):
    # Match RuntimeConfig's optional-field normalization and Go's sorted map JSON.
    value = dict(runtime)
    if not value.get("watch_seconds"):
        value.pop("watch_seconds", None)
    if not value.get("layout_publication"):
        value.pop("layout_publication", None)
    value["scopes"] = [dict(scope) for scope in value["scopes"]]
    for scope in value["scopes"]:
        for key in ("path_prefix", "previous_generation", "read_only", "desired_root", "work_state", "working_root",
                    "archived", "layout_from", "layout_generation", "layout_read_only"):
            if not scope.get(key):
                scope.pop(key, None)
    raw = json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
    for char, escaped in (("<", "\\u003c"), (">", "\\u003e"), ("&", "\\u0026"),
                          ("\u2028", "\\u2028"), ("\u2029", "\\u2029")):
        raw = raw.replace(char, escaped)
    return "sha256:" + hashlib.sha256(raw.encode()).hexdigest()


def prepared_recovery_inventory(cfg, refresh=False):
    args = [cfg["loom"], "--config", "/etc/loom/loom.env", "backup", "notes-inventory", "--json"]
    if refresh:
        args.append("--refresh")
    envelope = json.loads(run(*args, output=True))
    inventory = envelope.get("data")
    if not isinstance(inventory, dict) or inventory.get("schema_version") != "notes_workspace.recovery_enrollment.v1":
        raise ValueError("current Notes recovery preparation unavailable")
    return inventory


def recovery_membership(cfg, runtime, state, prepared=None):
    path = cfg.get("enrollment_inventory")
    if not path:
        if {s["Collection"] for s in runtime["scopes"]} != set(cfg["collections"]):
            raise ValueError("Notes enrollment changed; update legacy recovery collections")
        return cfg["collections"], None, None
    if Path(path) != state / "enrollment.json":
        raise ValueError("Notes enrollment inventory is outside the recovery fence")
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(fd, "rb") as stream:
        info = os.fstat(stream.fileno())
        # Runtime ACL management grants read-only agents access to non-secret inventory.
        if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or info.st_mode & 0o022:
            raise ValueError("invalid Notes enrollment inventory file")
        raw = stream.read(4 * 1024 * 1024 + 1)
    if len(raw) > 4 * 1024 * 1024:
        raise ValueError("Notes enrollment inventory exceeds limit")
    inventory = json.loads(raw) if prepared is None else prepared
    schema = "notes_workspace.enrollment.v1" if prepared is None else "notes_workspace.recovery_enrollment.v1"
    if (inventory.get("schema_version") != schema
            or inventory.get("replica") != runtime["replica"]
            or inventory.get("config_sha256") != runtime_digest(runtime)):
        raise ValueError("Notes enrollment inventory identity/configuration mismatch")
    if prepared is not None:
        raw = (json.dumps(inventory, sort_keys=True, indent=2) + "\n").encode()
        if len(raw) > 4 * 1024 * 1024:
            raise ValueError("Notes recovery inventory exceeds limit")
    collections, ids, sources, display_roots = {}, set(), set(), set()
    archive_entries = 0
    for entry in inventory["collections"]:
        key, source, display = entry["Collection"], entry["SourceCollection"], entry["Root"]
        if key in ids or source in sources or display in display_roots:
            raise ValueError("duplicate Notes enrollment identity")
        ids.add(key)
        sources.add(source)
        display_roots.add(display)
        if entry["status"] == "withdrawn":
            continue
        if entry["status"] in ("reference", "reference_held"):
            if entry.get("archived") is not True:
                raise ValueError("Notes archive reference admission is held: " + key)
            if entry["status"] == "reference":
                operation, manifest = entry.get("archive_operation"), entry.get("archive_manifest")
                generation = entry.get("source_generation")
                if (not isinstance(operation, str)
                        or re.fullmatch(r"workspace_archive_operation_[0-7][0-9A-HJKMNP-TV-Z]{25}", operation) is None
                        or not isinstance(manifest, str) or re.fullmatch(r"sha256:[0-9a-f]{64}", manifest) is None
                        or not isinstance(generation, str) or not generation):
                    raise ValueError("Notes archive reference identity is held: " + key)
                for counter in ("reference_files", "reference_bytes"):
                    count = entry.get(counter, 0)
                    if type(count) is not int or count < 0:
                        raise ValueError("invalid Notes archive reference inventory: " + key)
            # Preserve the exact inventory, including unadmitted qualifications.
            # Archive custody/protection stays with its existing provider; never
            # walk a retained payload as another live source or infer cloud cover.
            archive_entries += 1
            continue
        expected_status = "active" if prepared is None else "current_snapshot"
        if entry["status"] != expected_status or not entry.get("source_generation"):
            raise ValueError("Notes source admission is held: " + key)
        if prepared is not None:
            missing = entry.get("recovery_missing_deferred", 0)
            if type(missing) is not int or missing < 0 or entry.get("source_intake_stage") != "current_filesystem_snapshot":
                raise ValueError("invalid Notes recovery qualification: " + key)
        source_path = entry["source_path"]
        prefix = entry.get("path_prefix", "")
        if prefix and (Path(prefix).is_absolute() or ".." in Path(prefix).parts):
            raise ValueError("invalid Notes recovery prefix")
        collections[key] = str(exact_directory(str(Path(source_path) / prefix)))
    if not collections and not archive_entries:
        raise ValueError("Notes enrollment inventory contains no active sources")
    return collections, raw, inventory


def capture_client(cfg, work):
    client = cfg["client"]
    client_tar = work / "mac-client.tar.gz"
    timeout = client.get("timeoutSeconds", 900)
    deadline = time.monotonic() + timeout
    workspace = client.get("workspace")
    vault = client.get("vault")
    result = {"client_recovery": "device_unavailable_or_capture_unstable"}
    if workspace:
        result["workspace_recovery"] = "device_unavailable_or_capture_unstable"
    # Either owner may be unavailable independently. Preserve a successful
    # native checkpoint even when the retired vault cannot be captured.
    attempts = (["composite"] if workspace and vault else []) + (["native"] if workspace else []) + (["vault"] if vault else [])
    for mode in attempts:
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            break
        limit = str(client.get("maxBytes", 16 * 1024 * 1024 * 1024))
        args = ["python3", "-"]
        args += ["--native", workspace, "--max-bytes", limit] if mode == "native" else [vault, limit]
        args += ["--timeout-seconds", str(math.ceil(remaining))]
        if mode == "composite":
            args += ["--workspace", workspace]
        try:
            with client_tar.open("wb") as stream:
                completed = subprocess.run(["runuser", "-u", "agents", "--", "ssh", "-o", "BatchMode=yes",
                    "-o", "ConnectTimeout=10", client["host"], shlex.join(args)],
                    input=Path(cfg["client_script"]).read_bytes(), stdout=stream, stderr=subprocess.PIPE,
                    timeout=remaining)
            ok = completed.returncode == 0 and client_tar.stat().st_size > 0
        except (subprocess.TimeoutExpired, OSError):
            ok = False
        if ok:
            result.update(client_recovery="vault_and_intent_exports_not_native_database", client_sha256=digest(client_tar))
            if mode == "composite":
                result.update(client_recovery="vault_and_native_workspace_checkpoint", workspace_recovery="consistent_native_checkpoint")
            elif mode == "native":
                result.update(client_recovery="native_workspace_checkpoint_without_vault", workspace_recovery="consistent_native_checkpoint",
                              vault_recovery="unavailable_or_not_captured" if vault else "not_configured")
            return result
        client_tar.unlink(missing_ok=True)  # Only this run's incomplete transfer.
    return result


def main(config_path):
    if os.geteuid() != 0:
        raise ValueError("Notes recovery requires its root service")
    os.umask(0o077)
    cfg = json.loads(Path(config_path).read_text())
    capture_format = cfg.get("capture_format", "self-contained-v1")
    if capture_format not in ("self-contained-v1", "reference-v2"):
        raise ValueError("unsupported Notes capture format")
    source_writer_bindings(cfg)
    cloud = json.loads(Path(cfg["cloud_config"]).read_text())["snapshots"]
    if cloud["backend"] != "borg" or cloud["borg"]["encryption"] not in ("repokey-blake2", "repokey", "keyfile", "keyfile-blake2"):
        raise ValueError("protected Notes recovery requires the existing encrypted Borg backend")
    runtime_path = Path(cfg["runtime_config"])
    runtime_raw = runtime_path.read_bytes()
    runtime = json.loads(runtime_raw)
    state = exact_directory(cfg["state_dir"])
    replica = exact_directory(runtime["replica_dir"])
    if replica.parent != state:
        raise ValueError("replica is outside the recovery fence")
    # Main's intentional operator-owned -> root-owned path transition is not
    # accepted by generic tmpfiles. Create only these configured child roots.
    for path in (cfg["destination"], cfg["staging"]):
        exact_directory(str(Path(path).parent))
        Path(path).mkdir(mode=0o700, exist_ok=True)
    destination = exact_directory(cfg["destination"])
    staging = exact_directory(cfg["staging"])
    if destination.stat().st_dev != staging.stat().st_dev:
        raise ValueError("publication requires a same-filesystem staging directory")

    # No shared-group access: agents also belong to loom. Only the backup user
    # receives a named read ACL on the completed secret-bearing cohort.
    run("setfacl", "-b", str(destination))
    os.chmod(destination, 0o700)
    run("setfacl", "-m", "u:loom:rx", str(destination))
    with operational_lease(cfg["operational_root"]), recovery_lock(state):
        admission = recovery_admission(cfg, state)
        if not admission["allowed"]:
            print(json.dumps({"status": "deferred", "reason": admission["reasons"], "source_services_unchanged": True}))
            return
        prepared = prepared_recovery_inventory(cfg, refresh=True) if cfg.get("enrollment_inventory") else None
        collections, enrollment_raw, enrollment = recovery_membership(cfg, runtime, state, prepared)
        legacy_enrollment_raw = Path(cfg["enrollment_inventory"]).read_bytes() if enrollment_raw is not None else None
        roots = [exact_directory(p) for p in collections.values()]
        roots += [replica, exact_directory(cfg["couch_dir"]), exact_directory(cfg["secrets_dir"])]
        run("systemctl", "is-active", "--quiet", "loomd.service")
        run("systemctl", "is-active", "--quiet", "couchdb.service")
        capture_id = "notes-" + dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ-") + uuid.uuid4().hex[:8]
        work = Path(tempfile.mkdtemp(prefix=capture_id + "-", dir=staging))
        started = dt.datetime.now(dt.timezone.utc).isoformat()
        attempt = {"schema": "loom.notes_recovery.attempt.v1", "id": capture_id, "status": "capturing", "started_at": started,
                   "staging_name": work.name}
        record_attempt(state, attempt)
        if prepared is not None:
            (work / "recovery-enrollment.json").write_bytes(enrollment_raw)
        stopped = False
        try:
            run("systemctl", "stop", "couchdb.service")
            stopped = True
            before = sql_fingerprint(cfg["database"])
            tar = work / "main-state.tar.gz"
            relative = [str(p).lstrip("/") for p in roots] + [str(runtime_path).lstrip("/")]
            if enrollment_raw is not None:
                relative.append(str(Path(cfg["enrollment_inventory"])).lstrip("/"))
            with capture_phase("operational_backup", state, attempt):
                backup = json.loads(run("runuser", "-u", "loomadmin", "--", cfg["loom"], "--config", "/etc/loom/loom.env",
                                    "backup", "create", "--production", "--reason", "notes-recovery-cohort",
                                    "--idempotency-key", capture_id, "--json", output=True))
            checkpoint = verify_backup_result(backup)
            package = exact_directory(checkpoint["package_dir"])
            if package.parent != Path(cfg["operational_root"]) or not package.name.startswith("operational-"):
                raise ValueError("unexpected operational package path")
            with phase("source_writer_quiescence"), quiesce_source_writers(cfg, state, roots) as stopped_writers:
                with capture_phase("main_state_archive", state, attempt):
                    create_archive(tar, relative)
                with capture_phase("main_state_compare", state, attempt):
                    compare_archive(tar)
            if before != sql_fingerprint(cfg["database"]) or runtime_path.read_bytes() != runtime_raw:
                raise ValueError("Notes state changed during capture")
            if legacy_enrollment_raw is not None and Path(cfg["enrollment_inventory"]).read_bytes() != legacy_enrollment_raw:
                raise ValueError("Notes enrollment changed during capture")
            if prepared is not None and prepared_recovery_inventory(cfg) != prepared:
                raise ValueError("Notes recovery membership changed during capture")
            with capture_phase("operational_verify_and_copy", state, attempt):
                reference_hash = None
                if capture_format == "reference-v2":
                    manifest_hash, reference_hash = write_operational_reference(package, checkpoint, work / "operational-reference.json")
                else:
                    run("runuser", "-u", "loomadmin", "--", cfg["loom"], "--config", "/etc/loom/loom.env",
                        "backup", "verify", package.name, "--json")
                    manifest_hash = copy_verified_package(package, work / "operational-package")
            # A published cohort never relies on the mutable latest-backup link.
            receipt = {"schema": "loom.notes_recovery.v2" if reference_hash else "loom.notes_recovery.v1", "id": capture_id, "started_at": started,
                       "captured_at": dt.datetime.now(dt.timezone.utc).isoformat(),
                       "package_id": package.name, "manifest_sha256": manifest_hash,
                       "main_state_sha256": digest(tar), "notes_tables": before,
                       "runtime_config_sha256": hashlib.sha256(runtime_raw).hexdigest(),
                       "source_paths": [str(p) for p in roots], "cloud_verified": False,
                       "quiesced_source_writers": stopped_writers,
                       "client_recovery": "separate_device_backup_required"}
            if reference_hash:
                receipt["operational_reference_sha256"] = reference_hash
            receipt["collections"] = collections
            if enrollment_raw is not None:
                receipt["enrollment_inventory_sha256"] = hashlib.sha256(enrollment_raw).hexdigest()
                receipt["enrollment_inventory"] = enrollment
            if prepared is not None:
                receipt["recovery_inventory_sha256"] = hashlib.sha256(enrollment_raw).hexdigest()
                receipt["legacy_enrollment_inventory_sha256"] = hashlib.sha256(legacy_enrollment_raw).hexdigest()
                receipt["source_snapshot_semantics"] = "current_filesystem_with_unresolved_absences_preserved"
            if cfg.get("client"):
                with capture_phase("client_capture", state, attempt):
                    receipt.update(capture_client(cfg, work))
            (work / "cohort.json").write_text(json.dumps(receipt, indent=2) + "\n")
            for path in [work, *work.rglob("*")]:
                if path.is_symlink() or not (path.is_dir() or path.is_file()):
                    raise ValueError("non-regular recovery artifact")
                run("setfacl", "-b", str(path))
                os.chmod(path, 0o700 if path.is_dir() else 0o600)
                run("setfacl", "-m", "u:loom:rx" if path.is_dir() else "u:loom:r", str(path))
                if path.is_file():
                    with path.open("rb") as stream:
                        os.fsync(stream.fileno())
                else:
                    dfd = os.open(path, os.O_RDONLY | os.O_DIRECTORY)
                    try:
                        os.fsync(dfd)
                    finally:
                        os.close(dfd)
            os.rename(work, destination / capture_id)
            dfd = os.open(destination, os.O_RDONLY | os.O_DIRECTORY)
            try:
                os.fsync(dfd)
            finally:
                os.close(dfd)
            publication_recorded = False
            try:
                run(cfg["loom"], "--config", "/etc/loom/loom.env", "backup", "retention",
                    "record-capture", capture_id, "--json", output=True)
                publication_recorded = True
            except Exception:
                # The immutable capture already exists. Hold retirement rather
                # than reporting a lost backup and creating another full copy.
                print("Notes publication receipt unavailable; capture remains held", file=sys.stderr)
            print(json.dumps({"status": "published", "id": capture_id, "package_id": package.name,
                              "manifest_sha256": manifest_hash, "cloud_verified": False,
                              "publication_recorded": publication_recorded}))
            attempt.update(status="published", publication_recorded=publication_recorded)
            try:
                record_attempt(state, attempt)
            except Exception:
                print("Published capture outcome metadata unavailable; capture remains retained", file=sys.stderr)
        except BaseException as error:
            attempt.update(status="failed", error_class=type(error).__name__,
                           failed_at=dt.datetime.now(dt.timezone.utc).isoformat())
            try:
                record_attempt(state, attempt)
            except Exception:
                pass  # Never prevent writer/transport resumption below.
            raise
        finally:
            if stopped:
                run("systemctl", "start", "couchdb.service")
                run("systemctl", "is-active", "--quiet", "couchdb.service")
            # Incomplete evidence is retained outside the cloud walker. No user,
            # archive, or previous cohort cleanup belongs to this publisher.


if __name__ == "__main__":
    def terminated(signum, frame):
        signal.signal(signal.SIGTERM, signal.SIG_IGN)
        raise InterruptedError("capture terminated")

    signal.signal(signal.SIGTERM, terminated)
    try:
        if sys.argv[1] == "--cleanup":
            cleanup(sys.argv[2])
        elif sys.argv[1] == "--failed-plan" and len(sys.argv) == 4:
            print(json.dumps(review_failed_capture(sys.argv[2], sys.argv[3])))
        elif sys.argv[1] == "--failed-hold" and len(sys.argv) == 6:
            print(json.dumps(review_failed_capture(sys.argv[2], sys.argv[3], sys.argv[4], sys.argv[5])))
        else:
            main(sys.argv[1])
    except Exception as error:
        # Commands can include private diagnostic output; never print stderr or
        # credential-bearing content in the systemd journal.
        location = next(frame for frame in reversed(traceback.extract_tb(error.__traceback__))
                        if frame.filename == __file__ and frame.name != "run")
        print("Notes recovery failed: " + type(error).__name__ + " at " + Path(location.filename).name + ":" + str(location.lineno) + "; incomplete capture retained", file=sys.stderr)
        sys.exit(1)
