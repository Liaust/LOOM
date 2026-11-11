"""Optional project profiles. Native Hermes owns their layout and runtime state."""
import contextlib
import fcntl
import json
import os
from pathlib import Path
import re

from native_project import Conflict, DIGEST, KEY, PROJECT, digest

PROFILE = re.compile(r"^loom-[0-9a-f]{32}$")
MARKER_FILE = "loom-project-owner.json"


def profile_id(project, resource):
    import hashlib
    return "loom-" + hashlib.sha256((project + "/" + resource).encode()).hexdigest()[:32]


def read_json(path):
    if path.is_symlink() or path.stat().st_size > 65536:
        raise ValueError("unsafe profile owner")
    return json.loads(path.read_text())


def profiles(home, project):
    root = Path(home) / "profiles"
    found = []
    if not root.exists():
        return found
    if root.is_symlink():
        raise ValueError("unsafe profile root")
    for directory in sorted(root.iterdir()):
        if not PROFILE.fullmatch(directory.name):
            continue
        if directory.is_symlink():
            raise ValueError("unsafe profile path")
        marker = directory / MARKER_FILE
        if not marker.exists():
            continue
        value = read_json(marker)
        if not PROJECT.fullmatch(value.get("project_id", "")) or not KEY.fullmatch(value.get("resource", "")) or directory.name != profile_id(value["project_id"], value["resource"]):
            raise ValueError("invalid profile binding")
        if value["project_id"] == project:
            found.append((directory, value))
    return found


def configuration(directory):
    import hermes_yaml as yaml
    for name in ("config.yaml", "SOUL.md"):
        path = directory / name
        if path.is_symlink() or path.stat().st_size > 262144:
            raise ValueError("unsafe managed profile source")
    return dict(config=yaml.safe_load((directory / "config.yaml").read_text()),
                instructions=(directory / "SOUL.md").read_text())


def revision(directory, marker):
    return digest(dict(id=directory.name, config=configuration(directory),
                       owner={k: marker[k] for k in ("project_id", "resource", "desired_hash", "retired")}))


def public(directory, marker):
    receipt = marker["receipt"]
    return dict(kind="agent", id=directory.name, profile=directory.name,
                project_id=marker["project_id"], resource=marker["resource"],
                revision=revision(directory, marker), desired_hash=marker["desired_hash"],
                paused=True, retired=marker["retired"],
                drift=digest(configuration(directory)) != marker["config_hash"], **receipt)


def atomic_json(path, value):
    from utils import atomic_json_write
    atomic_json_write(path, value)


def execute(home, request, alive, before_retire=lambda: None):
    from hermes_cli.profiles import create_profile, write_profile_meta
    from utils import atomic_yaml_write, atomic_write_text
    project, key = request["project_id"], request.get("resource", "")
    if not KEY.fullmatch(key) or key == "mina" or not request.get("token") or not DIGEST.fullmatch(request.get("input_hash", "")):
        raise ValueError("invalid agent action")
    directory = Path(home) / "profiles" / profile_id(project, key)
    selected = {m["resource"]: (d, m) for d, m in profiles(home, project)}
    existing = selected.get(key)
    marker = existing[1] if existing else None
    if marker and marker["receipt"]["token"] == request["token"]:
        if marker["receipt"]["input_hash"] != request["input_hash"]:
            raise Conflict()
        return [public(directory, marker)]
    before = revision(directory, marker) if marker else "absent"
    before_id = directory.name if marker else ""
    if before != request.get("expected_revision") or before_id != request.get("expected_id", ""):
        raise Conflict()
    retiring = request["operation"] == "retire_agent"
    spec = request.get("agent")
    if retiring:
        if marker is None:
            raise Conflict()
    else:
        if not isinstance(spec, dict) or set(spec) != {"instructions", "model", "provider", "workdir"}:
            raise ValueError("invalid agent spec")
        if not isinstance(spec["instructions"], str) or not 0 < len(spec["instructions"].encode()) <= 32768 or "\0" in spec["instructions"]:
            raise ValueError("invalid instructions")
        if not isinstance(spec["model"], str) or not spec["model"].strip() or len(spec["model"]) > 256 or any(c in spec["model"] for c in "\0\r\n") or not KEY.fullmatch(spec["provider"]):
            raise ValueError("explicit inference selection required")
        workdir = Path(spec["workdir"])
        if not workdir.is_absolute() or not workdir.is_dir() or workdir.resolve() != workdir:
            raise ValueError("workdir unavailable")
        if marker and digest(configuration(directory)) != marker["config_hash"]:
            raise Conflict()
    alive()
    if retiring:
        before_retire()
    creating = marker is None
    if creating:
        # No clone flags: native fresh state, no copied MINA credentials/memory.
        if directory.exists():
            raise Conflict()
        directory = create_profile(directory.name, no_alias=True)
        marker = dict(project_id=project, resource=key, retired=False)
    if not retiring:
        # A native fresh profile may intentionally have no config until its
        # first explicit model selection. Do not inherit the default profile.
        config = {} if creating else configuration(directory)["config"] or {}
        if creating:
            from hermes_cli.config_defaults import DEFAULT_CONFIG
            config["_config_version"] = DEFAULT_CONFIG["_config_version"]
        config["model"] = dict(default=spec["model"], provider=spec["provider"])
        config.setdefault("terminal", {})["cwd"] = spec["workdir"]
        config.setdefault("cron", {}).update(allow_agent_scheduling=True, catch_up_missed=False, script_timeout_seconds=1865)
        config.setdefault("curator", {})["enabled"] = False
        atomic_yaml_write(directory / "config.yaml", config, sort_keys=False)
        atomic_write_text(directory / "SOUL.md", spec["instructions"])
        if creating:
            write_profile_meta(directory, display_name=key, description="LOOM project " + project + ": " + key)
        marker["desired_hash"] = digest(spec)
    marker["retired"] = retiring
    marker["config_hash"] = digest(configuration(directory))
    marker["receipt"] = dict(token=request["token"], input_hash=request["input_hash"],
                             before_revision=before, before_id=before_id)
    marker["receipt"]["committed_revision"] = revision(directory, marker)
    alive()
    atomic_json(directory / MARKER_FILE, marker)
    return [public(directory, marker)]


@contextlib.contextmanager
def locked(home):
    # Serializes declaration requests, not native editing or inference.
    with open(Path(home) / ".loom-project-profiles.lock", "a+") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        yield
