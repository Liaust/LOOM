"""Native script-only Hermes timing -> existing authorized LOOM job -> Codex."""
import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile

LOOM = "/run/current-system/sw/bin/loom"
CODEX = "/run/current-system/sw/bin/loom-codex-exec"
MAX_BYTES = 2 << 20
BOXES = "/etc/loom-boxes.json"
SELECTORS = {"PATH", "HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME",
             "GH_CONFIG_DIR", "GH_HOST", "GIT_CONFIG_GLOBAL", "GIT_SSH_COMMAND", "GWS_CONFIG_DIR",
             "GOOGLE_WORKSPACE_CLI_CONFIG_DIR", "LOOM_CONFIG_FILE", "LOOM_BOX_ID", "LOOM_BOX_KEY"}


def validate(spec):
    required = {"project_id", "resource", "job_id", "profile_home", "cwd", "prompt", "model", "timeout_seconds", "sandbox"}
    if not isinstance(spec, dict) or not required <= set(spec) or set(spec) - required - {"box_context"}:
        raise ValueError("invalid Codex dispatch declaration")
    if (not isinstance(spec["project_id"], str) or not (re.fullmatch(r"project_[A-Za-z0-9_-]+", spec["project_id"]) or (spec["project_id"] == "" and "box_context" in spec))
            or not re.fullmatch(r"[a-z][a-z0-9_-]{0,62}", spec["resource"]) or not re.fullmatch(r"[a-zA-Z0-9_-]{1,128}", spec["job_id"])):
        raise ValueError("invalid dispatch identity")
    if not re.fullmatch(r"gpt-[a-z0-9.-]{1,100}", spec["model"]) or type(spec["timeout_seconds"]) is not int or not 1 <= spec["timeout_seconds"] <= 1800 or spec["sandbox"] not in ("read-only", "workspace-write", "danger-full-access"):
        raise ValueError("explicit bounded Codex settings required")
    if not isinstance(spec["prompt"], str) or not spec["prompt"].strip() or len(spec["prompt"].encode()) > 32768:
        raise ValueError("bounded prompt required")
    if not Path(spec["cwd"]).is_absolute() or not Path(spec["profile_home"]).is_absolute():
        raise ValueError("fixed absolute runtime paths required")
    if "box_context" in spec:
        context = spec["box_context"]
        if (not isinstance(context, dict) or set(context) != {"box_id", "profile", "workdir", "context_revision"}
                or not isinstance(context["box_id"], str) or not re.fullmatch(r"box_[A-Za-z0-9_-]+", context["box_id"])
                or not isinstance(context["profile"], str) or not re.fullmatch(r"[a-z][a-z0-9_-]{0,62}", context["profile"])
                or not isinstance(context["context_revision"], str) or not re.fullmatch(r"sha256:[0-9a-f]{64}", context["context_revision"])
                or not isinstance(context["workdir"], str) or not Path(context["workdir"]).is_absolute()
                or context["workdir"] == "/" or not Path(spec["cwd"]).is_relative_to(context["workdir"])
                or Path(spec["profile_home"]).name != context["profile"] or Path(spec["profile_home"]).parent.name != "profiles"):
            raise ValueError("invalid saved native Box context")
    return spec


def read_source(path, managed=True):
    path = Path(path)
    resolved = path.resolve(strict=True)
    if resolved != path and not (str(path) == BOXES and str(resolved).startswith("/nix/store/")):
        raise ValueError("aliased dispatch context")
    info = resolved.stat()
    if not resolved.is_file() or info.st_size > 1 << 20 or (managed and (info.st_uid not in (0, os.geteuid()) or info.st_mode & 0o022)):
        raise ValueError("unsafe dispatch context")
    with resolved.open("rb") as stream:
        raw = stream.read((1 << 20) + 1)
    if len(raw) > 1 << 20:
        raise ValueError("dispatch context exceeds bound")
    return raw


def read_yaml(path, managed=True):
    import yaml
    try:
        return yaml.safe_load(read_source(path, managed))
    except yaml.YAMLError:
        raise ValueError("native Box YAML configuration unavailable") from None


def catalog_entry(context, boxes_file):
    catalog = json.loads(read_source(boxes_file))
    if not isinstance(catalog, dict) or catalog.get("schema_version") != "loom.box_catalog.v1" or not isinstance(catalog.get("boxes"), list):
        raise ValueError("trusted Box catalog unavailable")
    entries = [b for b in catalog["boxes"] if b.get("root_path") == context["workdir"]]
    if len(entries) != 1:
        raise ValueError("saved Box root is no longer registered")
    root = Path(context["workdir"])
    owner = read_yaml(root / ".loom/box.yaml", managed=False)
    if (not isinstance(owner, dict) or owner.get("box_id") != context["box_id"] or owner.get("root_path") != str(root)
            or owner.get("owner_node") != entries[0].get("node") or owner.get("schema_version") not in ("loom.box.v0.4.1", "loom.box.v0.5")):
        raise ValueError("saved Box descriptor changed")
    return entries[0]


def invocation_environment(spec, boxes_file):
    home = Path(spec["profile_home"])
    if "box_context" not in spec:
        return dict(os.environ, HERMES_HOME=str(home))
    context = spec["box_context"]
    box = catalog_entry(context, boxes_file)
    marker = json.loads(read_source(home / "loom-box-owner.json"))
    if (not isinstance(marker, dict) or marker.get("schema_version") != "loom.box_agent.v1" or marker.get("retired") is not False
            or marker.get("box_key") != box.get("key") or any(marker.get(k) != v for k, v in context.items())):
        raise ValueError("saved native Box profile changed")
    config = read_yaml(home / "config.yaml")
    policy = config.get("profile_context") if isinstance(config, dict) else None
    if (not isinstance(policy, dict) or policy.get("required_cwd") != context["workdir"] or policy.get("allow_root_auth_fallback") is not False
            or (config.get("terminal") or {}).get("cwd") != context["workdir"]):
        raise ValueError("native Box policy changed")
    selectors = policy.get("environment")
    if (not isinstance(selectors, dict) or set(selectors) - SELECTORS
            or any(not isinstance(v, str) or not v or any(c in v for c in "\0\r\n") for v in selectors.values())
            or selectors.get("LOOM_BOX_ID") != context["box_id"] or selectors.get("LOOM_BOX_KEY") != box["key"]
            or selectors.get("LOOM_CONFIG_FILE") != "/etc/loom-client.env" or not selectors.get("PATH")):
        raise ValueError("native Box tool environment unavailable")
    if not (home / "home").is_dir() or (home / "home").resolve() != home / "home":
        raise ValueError("native Box HOME unavailable")
    env = {k: os.environ[k] for k in ("USER", "LOGNAME", "LANG", "LC_ALL", "TZ") if k in os.environ}
    env.update(selectors, HOME=str(home / "home"), HERMES_HOME=str(home))
    return env


def receipt(envelope, spec, occurrence):
    data = envelope.get("data") or {}
    call = data.get("capability_call") or {}
    result = data.get("result") or call.get("result_json") or {}
    detail = result.get("job") or {}
    job = detail.get("job") or {}
    outputs = {item["output_key"]: item.get("value_json") for item in detail.get("outputs", [])}
    context = dict(box_context=dict(spec["box_context"])) if "box_context" in spec else {}
    return dict(hermes_job_id=spec["job_id"], hermes_execution_id=occurrence,
                call_id=call.get("capability_call_id"), call_status=data.get("status") or call.get("status"),
                job_id=data.get("job_id") or job.get("job_id"), job_status=job.get("status"),
                thread_id=outputs.get("thread_id"), outcome=outputs.get("outcome"),
                question=outputs.get("question"), error_code=data.get("error_code") or call.get("error_code"), **context)


def invoke(spec_file, boxes_file=BOXES):
    path = Path(spec_file)
    if path.resolve(strict=True) != path:
        raise ValueError("aliased dispatch spec")
    if path.stat().st_size > 65536:
        raise ValueError("dispatch spec exceeds limit")
    spec = validate(json.loads(read_source(path)))
    home = Path(spec["profile_home"])
    if path.parent != home / "scripts" or home.resolve() != home:
        raise ValueError("dispatch does not belong to its profile")
    env = invocation_environment(spec, boxes_file)
    from hermes_constants import set_hermes_home_override, reset_hermes_home_override
    token = set_hermes_home_override(str(home))
    try:
        from cron.executions import list_executions
        running = [r for r in list_executions(job_id=spec["job_id"], limit=10) if r["status"] == "running"]
    finally:
        reset_hermes_home_override(token)
    if len(running) != 1:
        raise ValueError("one actual native running occurrence is required")
    occurrence = running[0]["id"]
    if not isinstance(occurrence, str) or not re.fullmatch(r"[A-Za-z0-9_.:-]{1,128}", occurrence):
        raise ValueError("invalid native occurrence identity")
    request = dict(script_ref="hermes_codex_dispatch",
                   input=dict(spec=spec, hermes_execution_id=occurrence),
                   execution_mode="wait_for_completion", wait_timeout_seconds=spec["timeout_seconds"] + 60)
    if spec["project_id"]:
        request["project_ref"] = spec["project_id"]
    args = [LOOM, "capability", "call", "main@script-runner.script.run", "--input", json.dumps(request),
            "--idempotency-key", "hermes-codex:" + occurrence, "--wait", "--timeout-seconds", str(spec["timeout_seconds"] + 65), "--json"]
    with tempfile.TemporaryFile() as output:
        result = subprocess.run(args, stdout=output, stderr=subprocess.DEVNULL, timeout=spec["timeout_seconds"] + 70, env=env)
        output.seek(0)
        raw = output.read(MAX_BYTES + 1)
    if len(raw) > MAX_BYTES:
        raise ValueError("LOOM result exceeds bound")
    envelope = json.loads(raw)
    # Keep the real LOOM call/job and Codex artifacts navigable; acceptance is
    # not success. Unknown/needs_input is not retried by this adapter.
    saved = receipt(envelope, spec, occurrence)
    print(json.dumps(saved))
    return 0 if result.returncode == 0 and envelope.get("ok") is True and saved["call_status"] == "completed" and saved["job_status"] == "completed" and saved["outcome"] == "completed" else 1


def job_input(raw):
    # Script-runner wraps the user payload alongside its script/routing context.
    request = json.loads(raw)
    if isinstance(request, dict) and "script" in request and "input" in request:
        request = request["input"]
    if not isinstance(request, dict) or set(request) != {"spec", "hermes_execution_id"}:
        raise ValueError("invalid Codex job input")
    if not isinstance(request["hermes_execution_id"], str) or not re.fullmatch(r"[A-Za-z0-9_.:-]{1,128}", request["hermes_execution_id"]):
        raise ValueError("invalid native occurrence identity")
    return request


def run_job(boxes_file=BOXES):
    request = job_input(os.environ["LOOM_INPUT_JSON"])
    spec = validate(request["spec"])
    artifact = Path(os.environ["LOOM_ARTIFACT_DIR"])
    prompt = artifact / "hermes-codex-prompt.md"
    prompt.write_text(spec["prompt"])
    mode = "project" if spec["project_id"] else "box"
    argv = [CODEX, "--mode", mode, "--cwd", spec["cwd"], "--model", spec["model"],
            "--sandbox", spec["sandbox"], "--timeout-seconds", str(spec["timeout_seconds"]),
            "--prompt-file", str(prompt), "--loom-job-result"]
    env = None
    if "box_context" in spec:
        context = spec["box_context"]
        box = catalog_entry(context, boxes_file)
        argv += ["--box-id", context["box_id"], "--box-context", json.dumps(context, sort_keys=True)]
        # The job runs as loom, not agents. The bridge re-reads private native
        # profile selectors as agents; no credential/config vector rides stdin.
        keep = ("HOME", "USER", "LOGNAME", "LANG", "LC_ALL", "TZ", "LOOM_JOB_ID", "LOOM_ATTEMPT", "LOOM_RESULT_FILE", "LOOM_ARTIFACT_DIR", "LOOM_OUTPUT_DIR")
        env = {k: os.environ[k] for k in keep if k in os.environ}
        env.update(PATH="/run/current-system/sw/bin", LOOM_CONFIG_FILE="/etc/loom-client.env", LOOM_BOX_ID=context["box_id"], LOOM_BOX_KEY=box["key"])
    process = subprocess.run(argv, timeout=spec["timeout_seconds"] + 15, env=env)
    path = Path(os.environ["LOOM_RESULT_FILE"])
    result = json.loads(path.read_text())
    result["outputs"].update(hermes_job_id=spec["job_id"], hermes_execution_id=request["hermes_execution_id"],
                             project_id=spec["project_id"], resource=spec["resource"])
    if "box_context" in spec:
        result["outputs"]["box_context"] = dict(spec["box_context"])
    # Existing bridge already published thread, semantic outcome and artifacts.
    # No alternate execution owner: preserve its result document in place.
    with tempfile.NamedTemporaryFile(mode="w", dir=path.parent, delete=False) as stream:
        json.dump(result, stream)
        stream.flush()
        os.fsync(stream.fileno())
        temporary = stream.name
    os.replace(temporary, path)
    return process.returncode


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--spec-file")
    parser.add_argument("--job", action="store_true")
    args = parser.parse_args()
    try:
        if bool(args.spec_file) == bool(args.job):
            raise ValueError("select native timing or LOOM job execution")
        raise SystemExit(run_job() if args.job else invoke(args.spec_file))
    except (ValueError, OSError, KeyError, TypeError, subprocess.SubprocessError):
        print(json.dumps({"ok": False, "code": "hermes.codex_dispatch_failed", "message": "Inspect the native occurrence and linked LOOM job; no automatic retry was attempted."}))
        raise SystemExit(1)
