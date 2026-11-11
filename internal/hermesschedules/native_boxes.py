"""Box-owned native profiles; a Box is not a synthetic project."""
import json
from pathlib import Path
import re

from native_project import Conflict, DIGEST, KEY, PROJECT
from native_profiles import configuration, read_json

BOX = re.compile(r"^box_[A-Za-z0-9_-]+$")
MARKER = "loom-box-owner.json"


def profiles(home, box_id=None):
    root = Path(home) / "profiles"
    if root.is_symlink() or (box_id is not None and not BOX.fullmatch(box_id)):
        raise ValueError("invalid Box profile catalog")
    if not root.exists():
        return []
    entries = sorted(root.iterdir())
    if len(entries) > 256:
        raise ValueError("profile catalog too large")
    found = []
    for directory in entries:
        if not KEY.fullmatch(directory.name):
            continue
        if directory.is_symlink():
            raise ValueError("unsafe profile path")
        marker = directory / MARKER
        if not marker.exists():
            continue
        value = read_json(marker)
        if (not isinstance(value, dict) or value.get("schema_version") != "loom.box_agent.v1"
                or not BOX.fullmatch(value.get("box_id", "")) or value.get("profile") != directory.name
                or not DIGEST.fullmatch(value.get("context_revision", "")) or type(value.get("retired")) is not bool
                or not isinstance(value.get("workdir"), str) or not Path(value["workdir"]).is_absolute()
                or (directory / "loom-project-owner.json").exists()):
            raise ValueError("invalid Box profile binding")
        if box_id is None or value["box_id"] == box_id:
            found.append((directory, value))
    return found


def resolve(home, box_id, profile, boxes_file):
    import hermes_yaml as yaml
    if not boxes_file:
        raise ValueError("trusted Box catalog required")
    selected = [(d, m) for d, m in profiles(home, box_id) if d.name == profile]
    if len(selected) != 1 or selected[0][1]["retired"]:
        raise Conflict()
    directory, marker = selected[0]
    catalog = json.loads(Path(boxes_file).read_bytes())
    if catalog.get("schema_version") != "loom.box_catalog.v1":
        raise ValueError("unsupported Box catalog")
    entries = [b for b in catalog["boxes"] if b["key"] == marker.get("box_key") and b["root_path"] == marker["workdir"]]
    if len(entries) != 1:
        raise Conflict()
    root = Path(marker["workdir"])
    descriptor = root / ".loom/box.yaml"
    if root.resolve() != root or not root.is_dir() or descriptor.resolve() != descriptor or not descriptor.is_file() or descriptor.stat().st_size > 65536:
        raise Conflict()
    try:
        owner = yaml.safe_load(descriptor.read_text())
    except (OSError, yaml.YAMLError):
        raise ValueError("Box descriptor unavailable") from None
    if (not isinstance(owner, dict) or owner.get("schema_version") not in ("loom.box.v0.4.1", "loom.box.v0.5")
            or owner.get("box_id") != box_id or owner.get("root_path") != str(root) or owner.get("owner_node") != entries[0]["node"]):
        raise Conflict()
    try:
        config = configuration(directory)["config"]
    except (OSError, ValueError, yaml.YAMLError):
        raise ValueError("Box profile configuration unavailable") from None
    terminal = config.get("terminal") if isinstance(config, dict) else None
    policy = config.get("profile_context") if isinstance(config, dict) else None
    if (not isinstance(terminal, dict) or terminal.get("cwd") != str(root)
            or not isinstance(policy, dict) or policy.get("required_cwd") != str(root)
            or policy.get("allow_root_auth_fallback") is not False):
        raise Conflict()
    return directory, marker


def execute(native, home, source, request, alive, boxes_file):
    from native_profiles import locked
    from native_project import execute_schedule, profile_environment
    allowed = {"operation", "source", "box_id", "profile", "project_id", "resource", "expected_revision", "expected_id", "token", "input_hash", "spec"}
    if set(request) - allowed or request.get("operation") not in ("box_list", "box_reconcile", "box_retire") or not BOX.fullmatch(request.get("box_id", "")) or not KEY.fullmatch(request.get("profile", "")):
        raise ValueError("invalid Box schedule request")
    project = request.get("project_id", "")
    if project and not PROJECT.fullmatch(project):
        raise ValueError("invalid project identity")
    with locked(home):
        directory, marker = resolve(home, request["box_id"], request["profile"], boxes_file)
        forwarded = {k: v for k, v in request.items() if k not in ("box_id", "profile")}
        forwarded.update(operation=request["operation"].removeprefix("box_"), source=dict(source, profile=directory.name))
        spec = forwarded.get("spec")
        if isinstance(spec, dict) and "profile" in spec:
            spec = dict(spec)
            if spec.pop("profile") != directory.name:
                raise ValueError("conflicting profile")
            forwarded["spec"] = spec
        with profile_environment(directory):
            return execute_schedule(native, directory, forwarded["source"], forwarded, alive, box=marker)
