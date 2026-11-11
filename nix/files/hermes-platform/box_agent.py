"""Fresh native Box agent bootstrap; never sync templates over live state."""
import argparse
from contextlib import contextmanager
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import stat

import yaml

MARKER = "loom-box-owner.json"
SCHEMA = "loom.box_agent.v1"
CONTEXT_SCHEMA = "loom.hermes.external-contexts.v1"
CONTEXT_PATHS = {".loom/box.yaml", "AGENTS.md", "SOUL.md", "USER.md", "MEMORY.md", "protocols", "context", ".agents/skills", "skills/installed"}


def digest(raw):
    return "sha256:" + hashlib.sha256(raw).hexdigest()


def read(path):
    if not path.is_file() or path.resolve() != path or path.stat().st_size > 1 << 20:
        raise ValueError("unsafe or unavailable Box source")
    return path.read_bytes()


def resolve_box(catalog_file, key):
    catalog = json.loads(catalog_file.read_bytes())
    if catalog.get("schema_version") != "loom.box_catalog.v1":
        raise ValueError("unsupported Box catalog")
    selected = [b for b in catalog["boxes"] if b["key"] == key]
    if len(selected) != 1:
        raise ValueError("Box binding unavailable or ambiguous")
    box = selected[0]
    root = Path(box["root_path"])
    if not root.is_absolute() or not root.is_dir() or root.resolve() != root:
        raise ValueError("Box root unavailable")
    owner = yaml.safe_load(read(root / ".loom/box.yaml"))
    if owner.get("schema_version") not in ("loom.box.v0.4.1", "loom.box.v0.5") or not re.fullmatch(r"box_[A-Za-z0-9_-]+", owner.get("box_id", "")) or owner.get("owner_node") != box["node"] or owner.get("root_path") != str(root):
        raise ValueError("Box descriptor disagrees with catalog")
    return dict(box, box_id=owner["box_id"], catalog_roots=[b["root_path"] for b in catalog["boxes"]])


@contextmanager
def external_context_registry(path, native_home, box, profile):
    """Hold one metadata-only registry update across bootstrap; never touch other roots."""
    path = Path(path)
    root = Path(box["root_path"])
    if not path.is_absolute() or path.resolve() != path or any(path.is_relative_to(Path(p)) for p in box.get("catalog_roots", [str(root)])):
        raise ValueError("external context registry must be canonical and outside the Box")
    parent = path.parent.stat()
    if not stat.S_ISDIR(parent.st_mode) or parent.st_uid != os.geteuid() or stat.S_IMODE(parent.st_mode) != 0o700:
        raise ValueError("external context registry requires an existing agent-owned 0700 parent")
    def load():
        try:
            info = path.lstat()
        except FileNotFoundError:
            return None, dict(schema=CONTEXT_SCHEMA, contexts=[]), None
        if not stat.S_ISREG(info.st_mode) or info.st_uid not in (0, os.geteuid()) or info.st_mode & 0o022 or info.st_size > 1 << 20 or info.st_nlink != 1:
            raise ValueError("unsafe external context registry")
        raw = path.read_bytes()
        if len(raw) > 1 << 20:
            raise ValueError("external context registry exceeds bound")
        value = json.loads(raw)
        if not isinstance(value, dict) or set(value) != {"schema", "contexts"} or value["schema"] != CONTEXT_SCHEMA or not isinstance(value["contexts"], list) or len(value["contexts"]) > 128:
            raise ValueError("invalid external context registry")
        identities, roots, box_ids = set(), [], set()
        for entry in value["contexts"]:
            if not isinstance(entry, dict) or set(entry) != {"profile", "box_id", "root", "paths"}:
                raise ValueError("invalid external context entry")
            selected = Path(entry["root"])
            paths = entry["paths"]
            if (not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_-]{0,63}", entry["profile"])
                    or not re.fullmatch(r"box_[A-Za-z0-9][A-Za-z0-9_-]{1,100}", entry["box_id"])
                    or not selected.is_absolute() or selected == Path("/") or selected.resolve() != selected
                    or not isinstance(paths, list) or not paths or any(p not in CONTEXT_PATHS for p in paths)
                    or len(set(paths)) != len(paths) or ".loom/box.yaml" not in paths
                    or entry["profile"] in identities or entry["box_id"] in box_ids
                    or any(selected.is_relative_to(p) or p.is_relative_to(selected) for p in [native_home.parent, *roots])
                    or path.is_relative_to(selected)):
                raise ValueError("invalid or overlapping external context binding")
            identities.add(entry["profile"])
            box_ids.add(entry["box_id"])
            roots.append(selected)
        return raw, value, info
    fd = os.open(str(path) + ".lock", os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, "a") as lock:
        info = os.fstat(lock.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid() or info.st_nlink != 1 or info.st_mode & 0o077:
            raise ValueError("unsafe external context registry lock")
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        before, value, info = load()
        contexts = value["contexts"]
        matches = [e for e in contexts if e["profile"] == profile]
        if matches:
            entry = matches[0]
            if entry["box_id"] != box["box_id"] or entry["root"] != str(root):
                raise ValueError("existing profile protection binding differs")
        else:
            if len(contexts) >= 128 or any(e["box_id"] == box["box_id"] or root.is_relative_to(Path(e["root"])) or Path(e["root"]).is_relative_to(root) for e in contexts) or root.is_relative_to(native_home.parent) or native_home.parent.is_relative_to(root):
                raise ValueError("external context protection binding overlaps")
            entry = dict(profile=profile, box_id=box["box_id"], root=str(root), paths=[])
            contexts.append(entry)
        for relative in (".loom/box.yaml", "AGENTS.md", ".agents/skills"):
            if relative not in entry["paths"]:
                entry["paths"].append(relative)
        if before is not None and json.loads(before) != value and info.st_uid != os.geteuid():
            raise ValueError("external context registry update requires its owner")
        def publish():
            from utils import atomic_json_write
            current, _, _ = load()
            if current != before:
                raise ValueError("external context registry changed during bootstrap")
            if before is not None and json.loads(before) == value:
                return
            if info is not None and info.st_uid != os.geteuid():
                raise ValueError("external context registry update requires its owner")
            atomic_json_write(path, value, mode=0o600, fsync_dir=True)
        yield publish


def prepare(native_home, catalog_file, key, profile, templates, model, provider, python, launcher, tool_path, commands, *, external_contexts, share_inference_provider=None, policy_source=None):
    box = resolve_box(catalog_file, key)
    with external_context_registry(external_contexts, native_home, box, profile) as publish:
        result = prepare_profile(native_home, box, key, profile, templates, model, provider, python, launcher, tool_path, commands, share_inference_provider, policy_source)
        current = resolve_box(catalog_file, key)
        if any(current[k] != box[k] for k in ("box_id", "root_path", "node")):
            raise ValueError("Box binding changed during bootstrap")
        publish()
    return dict(result, external_contexts=str(external_contexts))


def prepare_profile(native_home, box, key, profile, templates, model, provider, python, launcher, tool_path, commands, share_inference_provider=None, policy_source=None):
    from hermes_constants import set_hermes_home_override, reset_hermes_home_override
    from hermes_cli.profiles import create_profile, write_profile_meta, get_profile_dir
    from utils import atomic_write_text, atomic_yaml_write, atomic_json_write
    if profile != "tyler" or key != "company" or not model.strip() or provider in ("", "auto"):
        raise ValueError("explicit Tyler/Company and inference selection required")
    if share_inference_provider is not None and (share_inference_provider != "openai-codex" or provider != share_inference_provider):
        raise ValueError("shared inference must be the selected pinned openai-codex provider")
    if native_home.resolve() != native_home or not native_home.is_dir():
        raise ValueError("native catalog must be an existing real directory")
    if not isinstance(commands, dict) or any(not re.fullmatch(r"[a-z][a-z0-9_-]*", k) or not isinstance(v, str) or not Path(v).is_absolute() for k, v in commands.items()):
        raise ValueError("explicit native command paths required")
    if not Path(python).is_absolute() or not launcher.is_absolute() or not all(Path(p).is_absolute() for p in tool_path.split(":")):
        raise ValueError("absolute runtime/launcher/PATH selectors required")
    directory = native_home / "profiles" / profile
    if get_profile_dir(profile) != directory:
        raise ValueError("native profile creation catalog differs; set the explicit native home before bootstrap")
    root = Path(box["root_path"])
    replacements = {"{{AGENT_NAME}}": "Tyler", "{{BOX_KEY}}": key, "{{BOX_ID}}": box["box_id"], "{{BOX_ROOT}}": str(root)}
    def render(name):
        text = (templates / name).read_text()
        for old, value in replacements.items():
            text = text.replace(old, value)
        return text
    home = directory / "home"
    tool_path = ":".join(p for p in tool_path.split(":") if p not in ("/etc/profiles/per-user/agents/bin", "/home/agents/.local/bin"))
    env = dict(PATH=str(home / "bin") + (":" + tool_path if tool_path else ""),
               XDG_CONFIG_HOME=str(home / ".config"), XDG_CACHE_HOME=str(home / ".cache"),
               XDG_DATA_HOME=str(home / ".local/share"), XDG_STATE_HOME=str(home / ".local/state"),
               GH_CONFIG_DIR=str(home / ".config/gh"), GIT_CONFIG_GLOBAL=str(home / ".gitconfig"),
               GIT_SSH_COMMAND="ssh -F " + shlex.quote(str(home / ".ssh/config")),
               LOOM_BOX_ID=box["box_id"], LOOM_BOX_KEY=key, LOOM_CONFIG_FILE="/etc/loom-client.env")
    config = dict(model=dict(default=model, provider=provider), fallback_providers=[],
                  terminal=dict(backend="local", cwd=str(root), home_mode="profile", auto_source_bashrc=False),
                  profile_context=dict(allow_root_auth_fallback=False, discover_other_profiles=False, required_cwd=str(root), environment=env,
                                       unset_environment_prefixes=["GH_", "GITHUB_", "BASECAMP_", "GOOGLE_", "GWS_", "GIT_", "SSH_", "XDG_", "DBUS_", "BROWSER", "CODEX_", "HERMES_MAC_"]),
                  auth=dict(adopt_external_logins=False), curator=dict(enabled=False),
                  cron=dict(allow_agent_scheduling=True, catch_up_missed=False), mcp_servers={},
                  skills=dict(external_dirs=[]), platforms={})
    from gateway.config import Platform
    from hermes_cli.config_defaults import DEFAULT_CONFIG
    config["_config_version"] = DEFAULT_CONFIG["_config_version"]
    config["platforms"] = {p.value: dict(enabled=False) for p in Platform}
    if share_inference_provider:
        config["profile_context"]["share_inference_provider"] = share_inference_provider
    spec_fields = dict(config=config, soul=render("SOUL.md"), user=render("USER.md"), agents=render("AGENTS.md"), commands=commands, python=python, launcher=str(launcher))
    policy = read(policy_source) if policy_source else None
    if policy is not None:
        spec_fields["box_policy"] = digest(policy)
    # Nix's system login profile prepends shared user tools. Restore this native
    # profile's declared PATH after shell startup, before the session snapshot.
    shell_startup = "# Native Box tool selection; never source another user's shell files.\nexport PATH=" + shlex.quote(env["PATH"]) + "\n"
    shell_files = {home / name: shell_startup for name in (".bash_profile", ".bashrc")}
    legacy_config = dict(config, terminal={k:v for k,v in config["terminal"].items() if k != "auto_source_bashrc"})
    legacy_fields = dict(spec_fields, config=legacy_config)
    previous_revisions = {digest(json.dumps(fields, sort_keys=True).encode()) for fields in (legacy_fields,dict(legacy_fields,shell_startup=shell_startup))}
    spec = json.dumps(dict(spec_fields, shell_startup=shell_startup), sort_keys=True).encode()
    context_revision = digest(spec)
    if directory.exists() or directory.is_symlink():
        marker = json.loads(read(directory / MARKER))
        if any(marker.get(k) != v for k, v in dict(schema_version=SCHEMA, box_id=box["box_id"], profile=profile, workdir=str(root)).items()):
            raise ValueError("existing Box agent binding differs; explicit reconciliation required")
        if marker.get("context_revision") in previous_revisions:
            if any(path.is_symlink() or path.exists() and (not path.is_file() or read(path) != text.encode()) for path,text in shell_files.items()):
                raise ValueError("existing shell startup files require explicit merge")
            config_path = directory / "config.yaml"
            if digest(read(config_path)) != marker["bootstrap_hashes"].get(str(config_path)):
                raise ValueError("live native configuration changed; shell-init repair requires explicit merge")
            current_config = yaml.safe_load(read(config_path))
            if current_config != legacy_config:
                raise ValueError("native configuration differs from the reviewed shell-init predecessor")
            # Forward repair of a pristine predecessor: preserve personality and
            # memory; only add shell files and the reviewed shell-init option.
            for path, text in shell_files.items():
                if not path.exists():
                    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
                    with os.fdopen(fd, "w") as stream:
                        stream.write(text)
                        stream.flush()
                        os.fsync(stream.fileno())
                marker["bootstrap_hashes"][str(path)] = digest(read(path))
            atomic_yaml_write(config_path, config, sort_keys=False)
            marker["bootstrap_hashes"][str(config_path)] = digest(read(config_path))
            marker["context_revision"] = context_revision
            atomic_json_write(directory / MARKER, marker)
        if marker.get("context_revision") != context_revision:
            raise ValueError("existing Box agent context differs; explicit reconciliation required")
        drift = [path for path, expected in marker["bootstrap_hashes"].items() if digest(read(Path(path))) != expected]
        return dict(status="preserved", profile=profile, box_id=box["box_id"], drift=drift)
    # Bootstrap must run while the existing native gateway/scheduler is drained;
    # native create publishes and notifies its multiplexer before returning.
    if (root / "AGENTS.md").exists() or (root / "AGENTS.md").is_symlink():
        raise ValueError("existing Box instructions require explicit merge")
    policy_path = root / ".loom/agents/WORKFLOW.md"
    if policy is not None and (policy_path.exists() or policy_path.is_symlink()):
        raise ValueError("existing Box policy requires explicit merge")
    token = set_hermes_home_override(str(native_home))
    try:
        created = create_profile(profile, no_alias=True)
        if created != directory:
            raise ValueError("native profile catalog mismatch")
    finally:
        reset_hermes_home_override(token)
    for path in (home, home / "bin", home / ".config", home / ".cache", home / ".local/share", home / ".local/state", home / ".config/gh", home / ".ssh"):
        path.mkdir(mode=0o700, parents=True, exist_ok=True)
    files = {directory / "SOUL.md": render("SOUL.md"), directory / "memories/USER.md": render("USER.md"), root / "AGENTS.md": render("AGENTS.md"), home / ".gitconfig": "[credential]\n    helper =\n", home / ".ssh/config": "Host *\n    IdentityAgent none\n    IdentitiesOnly yes\n    IdentityFile none\n"}
    if policy is not None:
        policy_path.parent.mkdir(mode=0o750, parents=True, exist_ok=True)
        files[policy_path] = policy.decode("utf-8")
    files.update(shell_files)
    tool_context = dict(environment=dict(env, HOME=str(home), HERMES_HOME=str(directory)), commands=commands, basecamp=None, github_host=None)
    files[directory / "loom-tool-context.json"] = json.dumps(tool_context, sort_keys=True) + "\n"
    for tool in commands:
        if not re.fullmatch(r"[a-z][a-z0-9_-]*", tool):
            raise ValueError("invalid native tool name")
        shim = home / "bin" / tool
        files[shim] = "#!/bin/sh\nexec " + " ".join(shlex.quote(str(v)) for v in (python, launcher, "--context", directory / "loom-tool-context.json", tool)) + ' "$@"\n'
    for path, text in files.items():
        atomic_write_text(path, text, mode=0o640 if path == root / "AGENTS.md" else 0o600)
    for tool in commands:
        (home / "bin" / tool).chmod(0o700)
    atomic_yaml_write(directory / "config.yaml", config, sort_keys=False)
    write_profile_meta(directory, display_name="Tyler", description="Company Box assistant")
    hashes = {str(p): digest(read(p)) for p in (*files, directory / "config.yaml")}
    marker = dict(schema_version=SCHEMA, box_id=box["box_id"], box_key=key, profile=profile, workdir=str(root), context_revision=context_revision, retired=False, bootstrap_hashes=hashes)
    atomic_json_write(directory / MARKER, marker)
    return dict(status="created", profile=profile, box_id=box["box_id"], workdir=str(root))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("native-home", "boxes-file", "templates", "tool-launcher", "external-contexts"):
        parser.add_argument("--" + name, type=Path, required=True)
    parser.add_argument("--box-key", default="company")
    parser.add_argument("--profile", default="tyler")
    parser.add_argument("--policy-source", type=Path)
    parser.add_argument("--share-inference-provider", choices=("openai-codex",), help="explicitly borrow only this existing root inference login; never business credentials")
    for name in ("model", "provider", "python", "tool-path", "commands-file"):
        parser.add_argument("--" + name, required=True)
    args = parser.parse_args()
    os.umask(0o077)
    # This short-lived CLI owns its process environment. Native profile creation
    # resolves the catalog from HERMES_HOME, not the per-turn ContextVar.
    os.environ["HERMES_HOME"] = str(args.native_home)
    commands = json.loads(Path(args.commands_file).read_text())
    print(json.dumps(prepare(args.native_home, args.boxes_file, args.box_key, args.profile, args.templates, args.model, args.provider, args.python, args.tool_launcher, args.tool_path, commands, external_contexts=args.external_contexts, share_inference_provider=args.share_inference_provider, policy_source=args.policy_source)))


if __name__ == "__main__":
    try:
        main()
    except Exception:
        print(json.dumps(dict(status="unavailable", reason="Box agent bootstrap refused; preserve existing state and inspect binding")))
        raise SystemExit(1)
