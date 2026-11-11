"""Immutable native script inputs, dispatched without a supervisor model."""
import json
import os
from pathlib import Path
import re

from native_project import digest

SCRIPT = re.compile(r"^loom-codex-[0-9a-f]{32}\.py$")


def script_name(project, key, spec, box_context=None):
    identity = [project, key, spec]
    if box_context is not None:
        identity.append(box_context)
    return "loom-codex-" + digest(identity)[7:39] + ".py"


def valid(spec):
    return (isinstance(spec, dict) and set(spec) == {"model", "timeout_seconds", "sandbox"}
            and isinstance(spec["model"], str) and re.fullmatch(r"gpt-[a-z0-9.-]{1,100}", spec["model"])
            and type(spec["timeout_seconds"]) is int and 1 <= spec["timeout_seconds"] <= 1800
            and spec["sandbox"] in ("read-only", "workspace-write", "danger-full-access"))


def publish(path, content):
    if path.exists() or path.is_symlink():
        if path.is_symlink() or path.read_bytes() != content:
            raise ValueError("native dispatch source changed")
        return
    with path.open("xb") as output:
        output.write(content)
        output.flush()
        os.fsync(output.fileno())


def install(home, project, key, job_id, spec, box_context=None):
    root = Path(home) / "scripts"
    if root.is_symlink():
        raise ValueError("script root is symlinked")
    root.mkdir(exist_ok=True)
    path = root / script_name(project, key, spec, box_context)
    source = path.with_suffix(".json")
    value = dict(spec["codex"], project_id=project, resource=key, job_id=job_id,
                 profile_home=str(home), cwd=spec["workdir"], prompt=spec["prompt"])
    if box_context is not None:
        value["box_context"] = dict(box_context)
    publish(source, (json.dumps(value, sort_keys=True) + "\n").encode())
    program = "import os\nos.execv('/run/current-system/sw/bin/loom-hermes-codex-dispatch', ['loom-hermes-codex-dispatch', '--spec-file', " + repr(str(source)) + "])\n"
    publish(path, program.encode())


def source_hash(script):
    if not isinstance(script, str) or not SCRIPT.fullmatch(script):
        return None
    from hermes_constants import get_hermes_home
    path = get_hermes_home() / "scripts" / script
    contents = []
    for source in (path, path.with_suffix(".json")):
        if source.is_symlink() or source.stat().st_size > 65536:
            raise ValueError("unsafe dispatch source")
        contents.append(source.read_text())
    return digest(contents)
