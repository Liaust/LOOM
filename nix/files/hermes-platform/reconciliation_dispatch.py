"""Hermes timing for the existing conditional MINA Provenance review job."""

import json
from datetime import datetime, timezone
import os
from pathlib import Path
import re
import subprocess
import tempfile

HOME = Path("/srv/loom/agents/mina/.hermes")
LOOM = "/run/current-system/sw/bin/loom"
LIMIT = 2 << 20


def late_receipt(execution, job_id, now):
    scheduled = execution.get("scheduled_instant")
    if not isinstance(scheduled, str):
        raise ValueError("scheduled occurrence time is missing")
    instant = datetime.fromisoformat(scheduled.replace("Z", "+00:00"))
    if instant.tzinfo is None:
        raise ValueError("scheduled occurrence timezone is missing")
    lateness = (now - instant).total_seconds()
    if lateness < 0:
        raise ValueError("scheduled occurrence is in the future")
    if lateness <= 120:
        return None
    return dict(hermes_job_id=job_id, hermes_execution_id=execution["id"],
                review_status="skipped_late", agent_started=False,
                scheduled_for=scheduled, lateness_seconds=round(lateness, 3),
                maximum_lateness_seconds=120)


def bounded_call(args, timeout):
    with tempfile.TemporaryFile() as output:
        process = subprocess.run(args, stdout=output, stderr=subprocess.DEVNULL, timeout=timeout)
        output.seek(0)
        raw = output.read(LIMIT + 1)
    if len(raw) > LIMIT:
        raise ValueError("LOOM result exceeds bound")
    return process.returncode, json.loads(raw)


def source_paused(envelope):
    return (envelope.get("ok") is True
            and envelope.get("data", {}).get("schedule", {}).get("schedule_id") == "schedule_01M441KMNAG13G0PSYCR49X2GH"
            and envelope["data"]["schedule"].get("status") in ("paused", "disabled"))


def result_receipt(envelope, job_id, occurrence):
    data = envelope.get("data") or {}
    call = data.get("capability_call") or {}
    result = data.get("result") or call.get("result_json") or {}
    detail = result.get("job") or {}
    job = detail.get("job") or {}
    outputs = {v["output_key"]: v.get("value_json") for v in detail.get("outputs", [])}
    saved = dict(hermes_job_id=job_id, hermes_execution_id=occurrence,
                 call_id=call.get("capability_call_id"),
                 call_status=data.get("status") or call.get("status"),
                 job_id=data.get("job_id") or job.get("job_id"), job_status=job.get("status"),
                 review_status=outputs.get("review_status"), selected_count=outputs.get("selected_count"),
                 agent_started=outputs.get("agent_started"), thread_id=outputs.get("thread_id"),
                 outcome=outputs.get("outcome"), error_code=data.get("error_code") or call.get("error_code"))
    completed = envelope.get("ok") is True and saved["call_status"] == "completed" and saved["job_status"] == "completed"
    no_work = saved["review_status"] == "no_work" and saved["selected_count"] == 0 and saved["agent_started"] is False
    reviewed = saved["review_status"] == "agent_result" and saved["outcome"] == "completed" and bool(saved["thread_id"])
    return saved, completed and (no_work or reviewed)


def main():
    source = Path(__file__).resolve(strict=True)
    config = source.with_suffix(".json")
    if source.parent != HOME / "scripts" or config.is_symlink() or config.stat().st_size > 4096:
        raise ValueError("unexpected native dispatch source")
    spec = json.loads(config.read_text())
    if set(spec) != {"job_id", "source_schedule_id"} or not re.fullmatch(r"[0-9a-f]{12}", spec["job_id"]) or spec["source_schedule_id"] != "schedule_01M441KMNAG13G0PSYCR49X2GH":
        raise ValueError("unexpected handover identity")
    os.environ["HERMES_HOME"] = str(HOME)
    from hermes_constants import set_hermes_home_override
    set_hermes_home_override(str(HOME))
    from cron.executions import list_executions
    running = [r for r in list_executions(job_id=spec["job_id"], limit=10) if r["status"] == "running"]
    if len(running) != 1:
        raise ValueError("one native running occurrence is required")
    occurrence = running[0]["id"]
    skipped = late_receipt(running[0], spec["job_id"], datetime.now(timezone.utc))
    if skipped is not None:
        print(json.dumps(skipped, sort_keys=True))
        return 0
    code, source_state = bounded_call([LOOM, "schedule", "inspect", spec["source_schedule_id"], "--json"], 20)
    if code != 0 or not source_paused(source_state):
        raise ValueError("source schedule is active or unverified")
    request = dict(script_ref="mina_provenance_reconciliation",
                   execution_mode="wait_for_completion", wait_timeout_seconds=1920)
    args = [LOOM, "capability", "call", "main@script-runner.script.run",
            "--input", json.dumps(request), "--idempotency-key", "hermes-mina-review:" + occurrence,
            "--wait", "--timeout-seconds", "1925", "--json"]
    code, envelope = bounded_call(args, 1930)
    saved, success = result_receipt(envelope, spec["job_id"], occurrence)
    print(json.dumps(saved, sort_keys=True))
    return 0 if code == 0 and success else 1


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (ValueError, OSError, KeyError, TypeError, subprocess.SubprocessError):
        print(json.dumps({"ok": False, "code": "hermes.reconciliation_dispatch_failed",
                          "message": "Inspect this native occurrence and linked LOOM job; no retry was attempted."}))
        raise SystemExit(1)
