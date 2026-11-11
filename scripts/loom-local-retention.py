#!/usr/bin/env python3
"""One post-completion local retention pass; no polling or cloud mutation."""
import hashlib
import json
import os
from pathlib import Path
import subprocess
import stat
import sys
import tempfile
import datetime as dt


def atomic_json(path, value, mode=0o600):
    raw = (json.dumps(value, sort_keys=True) + "\n").encode()
    fd, temporary = tempfile.mkstemp(prefix=".retention-", dir=path.parent)
    try:
        with os.fdopen(fd, "wb") as stream:
            os.fchmod(stream.fileno(), mode)
            stream.write(raw)
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


def read_request(path):
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(fd, "rb") as stream:
        info = os.fstat(stream.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or info.st_mode & 0o022:
            raise ValueError("invalid retention request custody")
        raw = stream.read(16 * 1024 + 1)
    if len(raw) > 16 * 1024:
        raise ValueError("retention request exceeds bound")
    request = json.loads(raw)
    if request.get("schema") != "loom.local_retention.request.v1" or not request.get("worker_run_id") or not request.get("archive") or len(request.get("manifest_sha256", "")) != 64:
        raise ValueError("invalid retention request")
    return hashlib.sha256(raw).hexdigest()


def execute(cfg):
    mode = cfg["mode"]
    if mode not in ("disabled", "plan", "apply"):
        raise ValueError("invalid local retention mode")
    if mode == "disabled":
        return {"status": "disabled"}
    revision = cfg.get("policy_revision", "")
    if mode == "apply" and not revision:
        raise ValueError("automatic apply requires an explicitly approved policy revision")
    base = Path(cfg["data_dir"]) / "backups"
    request_path = base / "local-retention-request.json"
    if not request_path.exists():
        return {"status": "no_request"}
    request_hash = read_request(request_path)
    ack_path = base / "local-retention-ack.json"
    try:
        ack = json.loads(ack_path.read_text())
    except (OSError, ValueError):
        ack = {}
    expected_status = "applied" if mode == "apply" else "planned"
    if (ack.get("schema") == "loom.local_retention.ack.v1" and ack.get("request_sha256") == request_hash
            and ack.get("status") == expected_status and ack.get("policy_revision") == revision):
        return {"status": "already_processed"}
    receipts = base / "local-retention-plans"
    receipts.mkdir(mode=0o700, exist_ok=True)
    if receipts.is_symlink() or receipts.stat().st_mode & 0o077:
        raise ValueError("private retention receipt directory required")
    receipt = {"schema": "loom.local_retention.pass.v1", "request_sha256": request_hash, "mode": mode,
               "policy_revision": revision, "observed_at": dt.datetime.now(dt.timezone.utc).isoformat(),
               "status": "started", "scopes": {}}
    receipt_path = receipts / (request_hash + ".json")
    atomic_json(receipt_path, receipt)
    command = [cfg["loom"], "--config", cfg["main_config"], "backup", "retention"]
    for scope in ("notes", "operational"):
        # Plan operational only after Notes apply, so expired reference pins do
        # not require another pass and a pre-Notes digest is never reused.
        process = subprocess.run(command + ["plan", "--scope", scope, "--cloud-config", cfg["cloud_config"], "--json"], capture_output=True)
        if process.returncode:
            receipt.update(status="deferred", reason="retention_plan_unavailable", scope=scope)
            atomic_json(receipt_path, receipt)
            return {"status": "deferred", "reason": receipt["reason"], "scope": scope}
        plan = json.loads(process.stdout)
        receipt["scopes"][scope] = {"plan": plan}
        atomic_json(receipt_path, receipt)
        if mode == "apply":
            process = subprocess.run(command + ["apply", "--scope", scope, "--cloud-config", cfg["cloud_config"],
                                                 "--plan-hash", plan["plan_hash"], "--yes", "--json"], capture_output=True)
            if process.returncode:
                receipt.update(status="deferred", reason="retention_apply_refused_or_partial", scope=scope)
                atomic_json(receipt_path, receipt)
                return {"status": "deferred", "reason": receipt["reason"], "scope": scope}
            receipt["scopes"][scope]["result"] = json.loads(process.stdout)
            atomic_json(receipt_path, receipt)
    receipt["status"] = "applied" if mode == "apply" else "planned"
    atomic_json(receipt_path, receipt)
    # A completion that arrived during this pass is not acknowledged or lost.
    if read_request(request_path) != request_hash:
        return {"status": "newer_request_pending"}
    atomic_json(ack_path, {"schema": "loom.local_retention.ack.v1", "request_sha256": request_hash,
                         "status": receipt["status"], "policy_revision": revision}, 0o644)
    return {"status": receipt["status"], "request_sha256": request_hash}


if __name__ == "__main__":
    if os.geteuid() != 0:
        raise SystemExit("local retention requires its existing root owner")
    try:
        print(json.dumps(execute(json.loads(Path(sys.argv[1]).read_text()))))
    except Exception:
        print(json.dumps({"status": "deferred", "reason": "local_retention_evidence_unavailable"}))
        # Keep the durable request; the next completion/operator can retry.
