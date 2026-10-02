#!/usr/bin/env python3
"""Cold Notes cohort through existing backup/cloud owners; run by its Nix unit."""
import datetime as dt
import fcntl
import hashlib
import json
import os
from pathlib import Path
import shutil
import shlex
import stat
import subprocess
import sys
import tempfile
import traceback
import uuid

TABLES = ("files", "bases", "operations", "recoveries", "path_history", "sync_replicas", "sync_records")


def run(*args, output=False):
    return subprocess.run(args, check=True, text=True, stdout=subprocess.PIPE if output else subprocess.DEVNULL,
                          stderr=subprocess.PIPE).stdout


def digest(path):
    with open(path, "rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def sql_fingerprint(database):
    result = {}
    for table in TABLES:
        query = ("SELECT count(*), md5(coalesce(string_agg(md5(to_jsonb(t)::text), '' "
                 "ORDER BY md5(to_jsonb(t)::text)),'')) FROM notes_workspace." + table + " t")
        result[table] = run("runuser", "-u", "postgres", "--", "psql", "-XqAt", "-d", database, "-c", query, output=True).strip()
    return result


def exact_directory(path):
    p = Path(path)
    if not p.is_absolute() or str(p) != path or not p.is_dir():
        raise ValueError("invalid recovery directory")
    if any(parent.is_symlink() for parent in (p, *p.parents)):
        raise ValueError("recovery path is a symlink")
    return p


def verify_backup_result(value):
    worker = value["data"]["worker"]
    checkpoint = worker["latest_checkpoint"]
    if not value["ok"] or worker["last_run"]["run_status"] != "succeeded" or not checkpoint["checkpoint_json"]["committed"]:
        raise ValueError("operational backup did not commit")
    if checkpoint["updated_by_run_id"] != worker["last_run"]["worker_run_id"]:
        raise ValueError("operational backup returned an older checkpoint")
    return checkpoint["checkpoint_json"]


def main(config_path):
    if os.geteuid() != 0:
        raise ValueError("Notes recovery requires its root service")
    os.umask(0o077)
    cfg = json.loads(Path(config_path).read_text())
    cloud = json.loads(Path(cfg["cloud_config"]).read_text())["snapshots"]
    if cloud["backend"] != "borg" or cloud["borg"]["encryption"] not in ("repokey-blake2", "repokey", "keyfile", "keyfile-blake2"):
        raise ValueError("protected Notes recovery requires the existing encrypted Borg backend")
    runtime_path = Path(cfg["runtime_config"])
    runtime_raw = runtime_path.read_bytes()
    runtime = json.loads(runtime_raw)
    if {s["Collection"] for s in runtime["scopes"]} != set(cfg["collections"]):
        raise ValueError("Notes enrollment changed; update recovery collections")
    state = exact_directory(cfg["state_dir"])
    replica = exact_directory(runtime["replica_dir"])
    if replica.parent != state:
        raise ValueError("replica is outside the recovery fence")
    roots = [exact_directory(p) for p in cfg["collections"].values()]
    roots += [replica, exact_directory(cfg["couch_dir"]), exact_directory(cfg["secrets_dir"])]
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
    lock_path = state / "recovery.lock"
    fd = os.open(lock_path, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, "r+") as lock:
        info = os.fstat(lock.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1:
            raise ValueError("invalid recovery lock")
        import pwd
        owner = pwd.getpwnam("loom")
        os.fchown(lock.fileno(), owner.pw_uid, owner.pw_gid)
        fcntl.flock(lock, fcntl.LOCK_EX)
        run("systemctl", "is-active", "--quiet", "loomd.service")
        run("systemctl", "is-active", "--quiet", "couchdb.service")
        capture_id = "notes-" + dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ-") + uuid.uuid4().hex[:8]
        work = Path(tempfile.mkdtemp(prefix=capture_id + "-", dir=staging))
        started = dt.datetime.now(dt.timezone.utc).isoformat()
        stopped = False
        try:
            run("systemctl", "stop", "couchdb.service")
            stopped = True
            before = sql_fingerprint(cfg["database"])
            tar = work / "main-state.tar.gz"
            relative = [str(p).lstrip("/") for p in roots] + [str(runtime_path).lstrip("/")]
            run("tar", "--acls", "--xattrs", "--numeric-owner", "-czf", str(tar), "-C", "/", "--", *relative)
            backup = json.loads(run("runuser", "-u", "loomadmin", "--", cfg["loom"], "--config", "/etc/loom/loom.env",
                                    "backup", "create", "--production", "--reason", "notes-recovery-cohort",
                                    "--idempotency-key", capture_id, "--json", output=True))
            checkpoint = verify_backup_result(backup)
            package = exact_directory(checkpoint["package_dir"])
            if package.parent != Path(cfg["operational_root"]) or not package.name.startswith("operational-"):
                raise ValueError("unexpected operational package path")
            if before != sql_fingerprint(cfg["database"]) or runtime_path.read_bytes() != runtime_raw:
                raise ValueError("Notes state changed during capture")
            run("tar", "--compare", "--acls", "--xattrs", "--numeric-owner", "-zf", str(tar), "-C", "/")
            run("runuser", "-u", "loomadmin", "--", cfg["loom"], "--config", "/etc/loom/loom.env",
                "backup", "verify", package.name, "--json")
            shutil.copytree(package, work / "operational-package", symlinks=False)
            # A published cohort never relies on the mutable latest-backup link.
            manifest_hash = digest(work / "operational-package" / "manifest.json")
            receipt = {"schema": "loom.notes_recovery.v1", "id": capture_id, "started_at": started,
                       "captured_at": dt.datetime.now(dt.timezone.utc).isoformat(),
                       "package_id": package.name, "manifest_sha256": manifest_hash,
                       "main_state_sha256": digest(tar), "notes_tables": before,
                       "runtime_config_sha256": hashlib.sha256(runtime_raw).hexdigest(),
                       "source_paths": [str(p) for p in roots], "cloud_verified": False,
                       "client_recovery": "separate_device_backup_required"}
            if cfg.get("client"):
                client = cfg["client"]
                client_tar = work / "mac-client.tar.gz"
                try:
                    with client_tar.open("wb") as stream:
                        result = subprocess.run(["runuser", "-u", "agents", "--", "ssh", "-o", "BatchMode=yes",
                            "-o", "ConnectTimeout=10", client["host"], shlex.join([
                                "python3", "-", client["vault"],
                                str(client.get("maxBytes", 16 * 1024 * 1024 * 1024))])],
                            input=Path(cfg["client_script"]).read_bytes(), stdout=stream, stderr=subprocess.PIPE,
                            timeout=client.get("timeoutSeconds", 900))
                    client_ok = result.returncode == 0
                except subprocess.TimeoutExpired:
                    client_ok = False
                if client_ok:
                    receipt["client_recovery"] = "vault_and_intent_exports_not_native_database"
                    receipt["client_sha256"] = digest(client_tar)
                else:
                    client_tar.unlink()  # Only this run's incomplete transfer.
                    receipt["client_recovery"] = "device_unavailable_or_capture_unstable"
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
            os.rename(work, destination / capture_id)
            dfd = os.open(destination, os.O_RDONLY | os.O_DIRECTORY)
            try:
                os.fsync(dfd)
            finally:
                os.close(dfd)
            print(json.dumps({"status": "published", "id": capture_id, "package_id": package.name,
                              "manifest_sha256": manifest_hash, "cloud_verified": False}))
        finally:
            if stopped:
                run("systemctl", "start", "couchdb.service")
                run("systemctl", "is-active", "--quiet", "couchdb.service")
            # Incomplete evidence is retained outside the cloud walker. No user,
            # archive, or previous cohort cleanup belongs to this publisher.


if __name__ == "__main__":
    try:
        main(sys.argv[1])
    except Exception as error:
        # Commands can include private diagnostic output; never print stderr or
        # credential-bearing content in the systemd journal.
        location = next(frame for frame in reversed(traceback.extract_tb(error.__traceback__))
                        if frame.filename == __file__ and frame.name != "run")
        print("Notes recovery failed: " + type(error).__name__ + " at " + Path(location.filename).name + ":" + str(location.lineno) + "; incomplete capture retained", file=sys.stderr)
        sys.exit(1)
