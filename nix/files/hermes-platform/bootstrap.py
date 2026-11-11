"""One-time native Hermes adoption; ordinary restarts preserve agent settings."""

import argparse
import base64
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import stat
import tempfile


REVISION = "d923a5eb487a17c0a68801bba9ef0f56c81040a8"
CONTEXT_SCHEMA = "loom.hermes.external-contexts.v1"
CONTEXT_PATHS = {".loom/box.yaml", "AGENTS.md", "SOUL.md", "USER.md", "MEMORY.md", "protocols", "context", ".agents/skills", "skills/installed"}
MAX_CONTEXT_BYTES = 1 << 20


def validate_external_contexts(raw, workspace, state):
    value = json.loads(raw)
    if not isinstance(value, dict) or set(value) != {"schema", "contexts"} or value["schema"] != CONTEXT_SCHEMA or not isinstance(value["contexts"], list) or len(value["contexts"]) > 128:
        raise ValueError("invalid external context registry")
    profiles, box_ids, roots = set(), set(), []
    for entry in value["contexts"]:
        if not isinstance(entry, dict) or set(entry) != {"profile", "box_id", "root", "paths"}:
            raise ValueError("invalid external context entry")
        profile, box_id, location, paths = (entry[key] for key in ("profile", "box_id", "root", "paths"))
        if not all(isinstance(item, str) for item in (profile, box_id, location)):
            raise ValueError("invalid external context binding")
        root = Path(location)
        if (not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_-]{0,63}", profile)
                or not re.fullmatch(r"box_[A-Za-z0-9][A-Za-z0-9_-]{1,100}", box_id)
                or not root.is_absolute() or root == Path("/") or root.resolve() != root
                or not isinstance(paths, list) or not paths or any(not isinstance(p, str) or p not in CONTEXT_PATHS for p in paths)
                or len(set(paths)) != len(paths) or ".loom/box.yaml" not in paths
                or profile in profiles or box_id in box_ids
                or any(root.is_relative_to(other) or other.is_relative_to(root) for other in [workspace, *roots])
                or state.is_relative_to(root)):
            raise ValueError("invalid or overlapping external context binding")
        profiles.add(profile)
        box_ids.add(box_id)
        roots.append(root)


def registry_identity(info):
    return (info.st_dev, info.st_ino, info.st_uid, info.st_mode, info.st_nlink,
            info.st_size, info.st_mtime_ns, info.st_ctime_ns)


def prepare_external_contexts(workspace, state):
    if not state.is_absolute() or state.resolve() != state:
        raise ValueError("managed native state must be a canonical directory")
    directory = os.open(state, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        owner = os.fstat(directory)
        if not stat.S_ISDIR(owner.st_mode) or owner.st_uid != os.getuid():
            raise ValueError("managed native state must be agent-owned")

        def check_parent(private=True):
            current = state.lstat()
            if (state.resolve() != state or not stat.S_ISDIR(current.st_mode)
                    or not os.path.samestat(owner, current) or current.st_uid != os.getuid()
                    or private and stat.S_IMODE(current.st_mode) != 0o700):
                raise ValueError("managed native state changed during provisioning")

        check_parent(private=False)
        # Only this exact managed state directory needs repair. Never chmod the
        # workspace, native home, registry, or their mixed-owner ancestors.
        if stat.S_IMODE(owner.st_mode) != 0o700:
            os.fchmod(directory, 0o700)
        check_parent()
        name = "external-contexts.json"

        def load_existing():
            try:
                before = os.stat(name, dir_fd=directory, follow_symlinks=False)
            except FileNotFoundError:
                return False
            if (not stat.S_ISREG(before.st_mode) or before.st_uid not in (0, os.getuid())
                    or before.st_mode & 0o022 or before.st_size > MAX_CONTEXT_BYTES or before.st_nlink != 1):
                raise ValueError("unsafe external context registry")
            fd = os.open(name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=directory)
            with os.fdopen(fd, "rb") as stream:
                if registry_identity(os.fstat(stream.fileno())) != registry_identity(before):
                    raise ValueError("external context registry changed before validation")
                raw = stream.read(MAX_CONTEXT_BYTES + 1)
                if len(raw) > MAX_CONTEXT_BYTES:
                    raise ValueError("external context registry exceeds bound")
                validate_external_contexts(raw, workspace, state)
                after = os.stat(name, dir_fd=directory, follow_symlinks=False)
                if (registry_identity(os.fstat(stream.fileno())) != registry_identity(before)
                        or registry_identity(after) != registry_identity(before)):
                    raise ValueError("external context registry changed during validation")
            check_parent()
            return True

        fd = os.open(name + ".lock", os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW | os.O_NONBLOCK, 0o600, dir_fd=directory)
        with os.fdopen(fd, "a") as lock:
            info = os.fstat(lock.fileno())
            if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or info.st_nlink != 1 or info.st_mode & 0o077:
                raise ValueError("unsafe external context registry lock")
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            if load_existing():
                return
            temporary = ".loom-contexts-" + secrets.token_hex(16)
            fd = os.open(temporary, os.O_CREAT | os.O_EXCL | os.O_WRONLY | os.O_NOFOLLOW, 0o600, dir_fd=directory)
            try:
                with os.fdopen(fd, "wb") as stream:
                    stream.write(json.dumps({"schema": CONTEXT_SCHEMA, "contexts": []}).encode() + b"\n")
                    stream.flush()
                    os.fsync(stream.fileno())
                    published = os.fstat(stream.fileno())
                check_parent()
                try:
                    os.link(temporary, name, src_dir_fd=directory, dst_dir_fd=directory, follow_symlinks=False)
                except FileExistsError as error:
                    raise ValueError("external context registry appeared during provisioning; existing bytes preserved") from error
            finally:
                os.unlink(temporary, dir_fd=directory)
            os.fsync(directory)
            if not os.path.samestat(published, os.stat(name, dir_fd=directory, follow_symlinks=False)):
                raise ValueError("external context registry was replaced during provisioning")
            if not load_existing():
                raise ValueError("external context registry disappeared during provisioning")
    finally:
        os.close(directory)


def atomic_write(path, content):
    fd, name = tempfile.mkstemp(prefix=".loom-", dir=path.parent)
    try:
        with os.fdopen(fd, "w") as stream:
            stream.write(content)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(name, path)
    finally:
        if os.path.exists(name):
            os.unlink(name)


def mapping(value, name):
    if not isinstance(value, dict):
        raise ValueError(f"{name} must be a mapping; existing state left unchanged")
    return value


def password_hash(password):
    salt = secrets.token_bytes(16)
    digest = hashlib.scrypt(password.encode(), salt=salt, n=16384, r=8, p=1, dklen=32)
    return "scrypt$16384$8$1$" + base64.b64encode(salt).decode() + "$" + base64.b64encode(digest).decode()


def prepare(workspace, state):
    home = workspace / ".hermes"
    for path in (workspace, home, state):
        if path.is_symlink() or not path.is_dir() or path.stat().st_uid != os.getuid():
            raise ValueError("native workspace/state must be real, existing agent-owned directories")
    soul = workspace / "SOUL.md"
    if not soul.is_file() or not soul.read_text().strip():
        raise ValueError("current canonical personality is missing or empty")
    prepare_external_contexts(workspace, state)
    marker = state / "adoption.json"
    if marker.exists():
        adopted = json.loads(marker.read_text())
        if adopted.get("revision") != REVISION or adopted.get("home") != str(home):
            raise ValueError("native adoption belongs to another release/home; review before migration")
        if not (state / "dashboard.env").is_file() or not (state / "client-auth.json").is_file():
            raise ValueError("native dashboard credentials missing; do not reset them automatically")
        return {"status": "already_adopted", "home": str(home)}

    import yaml

    config_path = home / "config.yaml"
    config = mapping(yaml.safe_load(config_path.read_text()) or {}, "config")
    # Preflight shape before changing native jobs or any existing configuration.
    for key in ("platforms", "gateway", "cron", "plugins", "skills", "mcp_servers", "dashboard", "terminal", "curator"):
        mapping(config.setdefault(key, {}), key)
    for value in config["platforms"].values():
        mapping(value, "platform configuration")
    for section, key in (("skills", "external_dirs"), ("plugins", "disabled")):
        if not isinstance(config[section].get(key, []), list):
            raise ValueError(f"{section}.{key} must be a list")

    previous = state / "before-adoption"
    previous.mkdir(mode=0o700, exist_ok=True)
    for name in ("config.yaml", "SOUL.md", "profile.yaml"):
        source = home / name
        target = previous / name
        if source.is_file() and not target.exists():
            shutil.copyfile(source, target)
            target.chmod(0o600)

    # Native APIs own the cron store. Merely loading a retained job never fires it.
    from cron import jobs

    paused = []
    with jobs.use_cron_store(home):
        for job in jobs.list_jobs(include_disabled=True):
            if job.get("enabled") is not False and job.get("state") != "completed":
                jobs.pause_job(job["id"], reason="LOOM native Hermes adoption; awaiting explicit handover")
                paused.append(job["id"])

    for value in config["platforms"].values():
        value["enabled"] = False
    # False settings defeat credential-based channel auto-detection, including old Discord.
    from gateway.config import Platform

    for platform in Platform:
        config["platforms"].setdefault(platform.value, {})["enabled"] = False
    config["gateway"]["multiplex_profiles"] = True
    config["cron"]["catch_up_missed"] = False
    config["curator"]["enabled"] = False
    config.setdefault("terminal", {})["cwd"] = str(workspace)
    external = config["skills"].setdefault("external_dirs", [])
    if not isinstance(external, list):
        raise ValueError("skills.external_dirs must be a list")
    for path in (workspace / "skills/installed", workspace / ".agents/skills"):
        if path.is_dir() and str(path) not in external:
            external.append(str(path))
    disabled = config["plugins"].get("disabled", [])
    if not isinstance(disabled, list):
        raise ValueError("plugins.disabled must be a list")
    config["plugins"]["disabled"] = [x for x in disabled if x not in ("basic", "dashboard_auth/basic")]
    config["mcp_servers"].setdefault("loom-mac", {
        "command": "/etc/profiles/per-user/agents/bin/loom-mac-computer-use-bridge",
        "args": ["mcp"],
        "env": {"HERMES_MAC_BINDING": "/etc/loom-mac-computer-use/binding.json"},
    })

    auth_file = state / "client-auth.json"
    if auth_file.exists():
        auth = mapping(json.loads(auth_file.read_text()), "client auth")
    else:
        auth = {"username": "operator", "password": secrets.token_urlsafe(32),
                "secret": secrets.token_urlsafe(48)}
        atomic_write(auth_file, json.dumps(auth, indent=2) + "\n")
    environment = {
        "HERMES_DASHBOARD_BASIC_AUTH_USERNAME": auth["username"],
        "HERMES_DASHBOARD_BASIC_AUTH_PASSWORD_HASH": password_hash(auth["password"]),
        "HERMES_DASHBOARD_BASIC_AUTH_SECRET": auth["secret"],
    }
    atomic_write(state / "dashboard.env", "".join(f"{k}={v}\n" for k, v in environment.items()))
    atomic_write(config_path, yaml.safe_dump(config, sort_keys=False, allow_unicode=True))
    from hermes_cli.profiles import write_profile_meta

    write_profile_meta(home, display_name="MINA")
    profile_soul = home / "SOUL.md"
    if profile_soul.resolve() != soul.resolve():
        link = home / (".loom-soul-" + secrets.token_hex(8))
        link.symlink_to("../SOUL.md")
        os.replace(link, profile_soul)
    atomic_write(marker, json.dumps({"schema_version": 1, "revision": REVISION,
                                   "home": str(home), "paused_jobs": paused}, indent=2) + "\n")
    return {"status": "adopted", "home": str(home), "paused_jobs": paused}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--workspace", type=Path, required=True)
    parser.add_argument("--state", type=Path, required=True)
    args = parser.parse_args()
    os.umask(0o077)
    os.environ["HERMES_HOME"] = str(args.workspace / ".hermes")
    print(json.dumps(prepare(args.workspace, args.state)))


if __name__ == "__main__":
    main()
