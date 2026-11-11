"""Publish a signed native Hermes capture into the existing encrypted archive root."""

import argparse
from datetime import datetime, timezone
import fcntl
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import stat
import subprocess
import tempfile
import zipfile

from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey, Ed25519PublicKey

REVISION = "d923a5eb487a17c0a68801bba9ef0f56c81040a8"
CONTEXT_DIRECTORIES = ("protocols", "context", ".agents/skills", "skills/installed")
BOX_CONTEXT_PATHS = {".loom/box.yaml", "AGENTS.md", "SOUL.md", "USER.md", "MEMORY.md", *CONTEXT_DIRECTORIES}
PROFILE_INVENTORY_SCHEMA = "loom.hermes.profile-critical-inventory.v1"
PROFILE_CRITICAL_TREES = {"memories", "sessions", "skills", "home", "cron", "context", "protocols", ".agents"}


class CaptureValidationError(ValueError):
    def __init__(self, reason, path, phase):
        super().__init__("capture source validation failed")
        self.reason, self.path, self.phase = reason, path, phase


def failure_summary(error):
    result = {"status": "failed", "error": type(error).__name__}
    if isinstance(error, CaptureValidationError):
        result.update(code="capture.source_invalid", reason=error.reason,
                      path=error.path, phase=error.phase)
    return result


def digest(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def regular(path, limit, *, label=None, phase="source_validation", check_owner=True):
    info = path.lstat()
    reason = ("not_regular" if not stat.S_ISREG(info.st_mode) else
              "owner_mismatch" if check_owner and info.st_uid != os.getuid() else
              "multiple_links" if info.st_nlink != 1 else
              "size_limit" if info.st_size > limit else None)
    if reason:
        raise CaptureValidationError(reason, label or path.name, phase)
    return info


def platform_files(state):
    selected = []
    for root, dirs, files in os.walk(state, followlinks=False):
        for name in dirs:
            path = Path(root) / name
            if path.is_symlink():
                raise CaptureValidationError("symlink_directory", str(path.relative_to(state)), "platform_preflight")
        for name in sorted(files):
            path = Path(root) / name
            regular(path, 4 << 20, label=str(path.relative_to(state)), phase="platform_preflight")
            selected.append(path)
    return selected


def synced_directory(path):
    fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def named_profiles(home):
    from hermes_constants import PROFILE_ID_RE, named_profile_has_identity
    root = Path(home) / "profiles"
    if not root.exists():
        return []
    if root.is_symlink():
        raise ValueError("native profile root is symlinked")
    result = []
    for path in root.iterdir():
        if not PROFILE_ID_RE.fullmatch(path.name):
            continue
        if path.is_symlink():
            raise ValueError("native profile directory is symlinked")
        if path.is_dir() and named_profile_has_identity(path):
            result.append(path.name)
    return sorted(result)


def critical_profile_path(name):
    if not isinstance(name, str) or not name or "\\" in name or any(ord(c) < 32 for c in name):
        return False
    path = PurePosixPath(name)
    return (bool(path.parts) and not path.is_absolute() and path.as_posix() == name
            and not any(p in (".", "..") for p in path.parts)
            and (len(path.parts) == 1 or path.parts[0] in PROFILE_CRITICAL_TREES))


def selected_profile_inventory(home, output, profiles):
    from hermes_cli.backup import _iter_backup_files
    selected = {profile: [] for profile in profiles}
    # Reuse the pinned native exclusions. This is metadata only: learned files
    # are mutable, and native SQLite snapshot sizes need not match live files.
    for _, relative in _iter_backup_files(home, output):
        parts = relative.parts
        if len(parts) >= 3 and parts[0] == "profiles" and parts[1] in selected:
            name = PurePosixPath(*parts[2:]).as_posix()
            if critical_profile_path(name):
                selected[parts[1]].append(name)
    if any(not paths for paths in selected.values()):
        raise ValueError("native profile has no selected critical inventory")
    return {"schema": PROFILE_INVENTORY_SCHEMA,
            "profiles": {profile: sorted(paths) for profile, paths in selected.items()}}


def verify_profile_inventory(manifest, archive):
    if "profile_inventory" not in manifest:
        return "legacy_unrecorded"
    inventory = manifest["profile_inventory"]
    profiles = manifest["profiles"]
    if (not isinstance(inventory, dict) or set(inventory) != {"schema", "profiles"}
            or inventory["schema"] != PROFILE_INVENTORY_SCHEMA
            or not isinstance(inventory["profiles"], dict)
            or not isinstance(profiles, list)
            or any(not isinstance(p, str) or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_-]{0,63}", p) for p in profiles)
            or len(set(profiles)) != len(profiles) or set(inventory["profiles"]) != set(profiles)):
        raise ValueError("invalid native profile inventory")
    expected = set()
    for profile, paths in inventory["profiles"].items():
        if (not isinstance(paths, list) or not paths
                or any(not critical_profile_path(name) for name in paths)
                or paths != sorted(set(paths))):
            raise ValueError("invalid native profile critical paths")
        expected.update("profiles/" + profile + "/" + name for name in paths)
    actual, required = set(), {"config.yaml", "state.db"}
    for entry in archive.infolist():
        parts = entry.filename.split("/")
        critical = (len(parts) >= 3 and parts[0] == "profiles" and parts[1] in inventory["profiles"]
                    and critical_profile_path("/".join(parts[2:])))
        if not critical and entry.filename not in required:
            continue
        if (entry.filename in actual or entry.is_dir()
                or stat.S_IFMT(entry.external_attr >> 16) not in (0, stat.S_IFREG)):
            raise ValueError("invalid native profile ZIP member")
        actual.add(entry.filename)
    if actual != expected | required:
        raise ValueError("native profile critical inventory differs")
    return "verified_native_selected_critical_inventory"


def context_files(workspace, paths=None):
    """Select bootstrap context only, never the recursive Box payload."""
    selected = list(workspace.glob("*.md")) if paths is None else []
    for relative in (CONTEXT_DIRECTORIES if paths is None else paths):
        base = workspace / relative
        if base.is_symlink():
            raise ValueError("external context root must not be a symlink")
        if base.is_dir():
            for root, dirs, files in os.walk(base, followlinks=False):
                if any((Path(root) / d).is_symlink() for d in dirs):
                    raise ValueError("external context directory is symlinked")
                dirs[:] = [d for d in dirs if d not in (".git", "node_modules", "__pycache__", ".venv")]
                selected.extend(Path(root) / file for file in files)
                if paths is not None and len(selected) > 10000:
                    raise ValueError("external context exceeds entry bound")
        elif base.exists():
            selected.append(base)
        if paths is not None and len(selected) > 10000:
            raise ValueError("external context exceeds entry bound")
    return sorted(set(selected))


def external_context_files(registry_path, home, profiles):
    if not registry_path:
        return [], []
    import yaml
    registry = Path(registry_path)
    resolved = registry.resolve()
    if resolved != registry and not str(resolved).startswith("/nix/store/"):
        raise ValueError("external context registry is aliased")
    info = resolved.lstat()
    if (not stat.S_ISREG(info.st_mode) or info.st_size > 1 << 20
            or info.st_mode & 0o022 or info.st_uid not in (0, os.getuid())):
        raise ValueError("external context registry is not bounded trusted configuration")
    with resolved.open("rb") as stream:
        raw = stream.read((1 << 20) + 1)
    if len(raw) > 1 << 20:
        raise ValueError("external context registry exceeds bound")
    registry = json.loads(raw)
    if (set(registry) != {"schema", "contexts"} or registry["schema"] != "loom.hermes.external-contexts.v1"
            or not isinstance(registry["contexts"], list) or len(registry["contexts"]) > 128):
        raise ValueError("invalid external context registry")
    files, inventory, identities, roots = [], [], set(), []
    for entry in registry["contexts"]:
        if set(entry) != {"profile", "box_id", "root", "paths"}:
            raise ValueError("invalid external context registration")
        profile, box_id, root, paths = entry["profile"], entry["box_id"], Path(entry["root"]), entry["paths"]
        if (not re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}", profile)
                or not re.fullmatch(r"box_[A-Za-z0-9][A-Za-z0-9_-]{1,100}", box_id)
                or not root.is_absolute() or root == Path("/") or root.resolve() != root
                or not isinstance(paths, list) or len(paths) != len(set(paths))
                or not paths or any(p not in BOX_CONTEXT_PATHS for p in paths)
                or ".loom/box.yaml" not in paths or profile in identities
                or any(root == other or root in other.parents or other in root.parents for other in [home.parent, *roots])):
            raise ValueError("invalid or overlapping external context binding")
        identities.add(profile)
        roots.append(root)
        prefix = "boxes/" + box_id + "/profiles/" + profile + "/"
        record = dict(entry, archive_prefix=prefix, status="unavailable", files=[])
        inventory.append(record)
        if not root.exists() or profile not in profiles:
            continue
        if root.is_symlink() or not root.is_dir():
            raise ValueError("external Box context must be a canonical directory")
        descriptor = root / ".loom/box.yaml"
        if not descriptor.exists():
            continue
        # Registered Box context is shared source owned by the Box/service
        # writer, not necessarily the native agents user. Native state keeps
        # its strict ownership check; external source still forbids aliases.
        regular(descriptor, 1 << 20, check_owner=False)
        if descriptor.resolve() != descriptor:
            raise ValueError("external Box descriptor is aliased")
        value = yaml.safe_load(descriptor.read_bytes())
        if (not isinstance(value, dict) or value.get("box_id") != box_id
                or value.get("root_path") != str(root)
                or value.get("schema_version") not in ("loom.box.v0.4.1", "loom.box.v0.5")):
            raise ValueError("external Box context identity differs")
        selected = context_files(root, paths)
        record["missing_paths"] = [p for p in paths if not (root / p).exists()]
        for path in selected:
            if path.resolve() != path:
                raise ValueError("external Box context path is aliased")
            regular(path, 32 << 20, check_owner=False)
            name = prefix + str(path.relative_to(root))
            files.append((path, name))
            record["files"].append(name)
        record["status"] = "selected"
    return files, inventory


def verify_context_inventory(manifest, names):
    expected = set()
    for context in manifest.get("external_contexts", []):
        if context["status"] not in ("selected", "unavailable"):
            raise ValueError("invalid external context status")
        if context["status"] == "unavailable":
            if context["files"]:
                raise ValueError("unavailable external context claims files")
            continue
        if context["profile"] not in manifest["profiles"]:
            raise ValueError("external context profile missing")
        prefix = "boxes/" + context["box_id"] + "/profiles/" + context["profile"] + "/"
        if (context["archive_prefix"] != prefix or prefix + ".loom/box.yaml" not in context["files"]
                or any(not name.startswith(prefix) or ".." in Path(name).parts for name in context["files"])
                or len(set(context["files"])) != len(context["files"])):
            raise ValueError("external context namespace differs")
        if expected.intersection(context["files"]):
            raise ValueError("duplicate external context namespace")
        expected.update(context["files"])
    if {name for name in names if name.startswith("boxes/")} != expected:
        raise ValueError("external context inventory differs")


def verify(package, public_key):
    package = Path(package)
    raw = (package / "manifest.json").read_bytes()
    if len(raw) > 1 << 20:
        raise ValueError("native manifest exceeds bound")
    envelope = json.loads(raw)
    manifest = envelope["manifest"]
    canonical = json.dumps(manifest, sort_keys=True, separators=(",", ":")).encode()
    Ed25519PublicKey.from_public_bytes(bytes.fromhex(public_key)).verify(
        bytes.fromhex(envelope["signature"]), canonical)
    if (manifest["schema"] != "loom.hermes.native-capture.v1" or manifest["revision"] != REVISION
            or manifest["id"] != package.name
            or {entry["path"] for entry in manifest["files"]} != {"profile.zip", "context.zip"}
            or len(manifest["files"]) != 2):
        raise ValueError("native manifest identity differs")
    for entry in manifest["files"]:
        payload = package / entry["path"]
        info = payload.lstat()
        if (not stat.S_ISREG(info.st_mode) or info.st_nlink != 1
                or info.st_size != entry["size"] or digest(payload) != entry["sha256"]):
            raise ValueError("native payload differs")
        with zipfile.ZipFile(payload) as archive:
            if archive.testzip():
                raise ValueError("native payload CRC failed")
            names = set(archive.namelist())
            if entry["path"] == "context.zip":
                verify_context_inventory(manifest, names)
            else:
                profile_assurance = verify_profile_inventory(manifest, archive)
    return {"status": "verified" if profile_assurance != "legacy_unrecorded" else "verified_legacy",
            "profile_inventory_assurance": profile_assurance,
            "qualification": ("Signed critical-path inventory matches ZIP metadata; not a global application transaction"
                              if profile_assurance != "legacy_unrecorded" else
                              "Legacy signature/payload integrity only; native-selected profile inventory was not recorded"),
            "id": manifest["id"], "created_at": manifest["created_at"],
            "profiles": manifest["profiles"], "files": manifest["files"],
            "external_contexts": manifest.get("external_contexts", []),
            "cloud_protection": "requires_exact_normal_cloud_manifest_inclusion"}


def capture(workspace, state, destination, binary, signing_key, external_contexts=None):
    workspace, state, destination = map(Path, (workspace, state, destination))
    for root in (workspace, state, destination):
        if root.is_symlink() or not root.is_dir() or root.resolve() != root:
            raise ValueError("capture requires existing canonical directories")
    key_info = regular(Path(signing_key), 64)
    if key_info.st_mode & 0o777 != 0o600 or key_info.st_size != 64:
        raise ValueError("capture requires the existing owner-private signing key")
    key = Ed25519PrivateKey.from_private_bytes(Path(signing_key).read_bytes()[:32])
    # Reject misplaced operator artifacts before native SQLite/ZIP work starts.
    # Revalidate below too: preflight is not authority to read a changed file.
    platform_files(state)
    profiles = named_profiles(workspace / ".hermes")
    extra_files, extra_inventory = external_context_files(external_contexts, workspace / ".hermes", profiles)
    staging = destination / ".staging"
    staging.mkdir(mode=0o700, exist_ok=True)
    with (staging / "capture.lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        started = datetime.now(timezone.utc)
        name = "hermes-native-" + started.strftime("%Y%m%d-%H%M%S-%f")
        # Interrupted captures stay in .staging; only a complete fsynced package publishes.
        temporary = Path(tempfile.mkdtemp(prefix=name + "-", dir=staging))
        package = temporary / "package"
        package.mkdir(mode=0o750)
        payload = package / "profile.zip"
        env = dict(os.environ, HERMES_HOME=str(workspace / ".hermes"),
                   PYTHONDONTWRITEBYTECODE="1")
        selected_profiles = named_profiles(workspace / ".hermes")
        profile_inventory = selected_profile_inventory(workspace / ".hermes", payload, selected_profiles)
        result = subprocess.run([binary, "backup", "--keep", "0", "--output", str(payload)],
                                cwd=workspace, env=env, stdout=subprocess.DEVNULL,
                                stderr=subprocess.DEVNULL, timeout=240)
        if result.returncode:
            raise ValueError("native Hermes backup did not complete")
        with zipfile.ZipFile(payload) as archive:
            names = set(archive.namelist())
            if not {"config.yaml", "state.db"}.issubset(names) or archive.testzip():
                raise ValueError("native capture inventory or CRC failed")
            verify_profile_inventory({"profiles": selected_profiles, "profile_inventory": profile_inventory}, archive)
            if selected_profiles != named_profiles(workspace / ".hermes"):
                raise ValueError("native profile inventory changed or is missing from capture")
        context = package / "context.zip"
        with zipfile.ZipFile(context, "w", compression=zipfile.ZIP_DEFLATED,
                             strict_timestamps=False) as archive:
            # Native backup deliberately skips file symlinks, including .hermes/SOUL.md.
            # Capture its canonical source and the tools/context outside HERMES_HOME.
            selected = [(p, "workspace/" + str(p.relative_to(workspace)), True) for p in context_files(workspace)]
            selected += [(p, name, False) for p, name in extra_files]
            total = 0
            for path, archive_name, check_owner in selected:
                info = regular(path, 32 << 20, check_owner=check_owner)
                total += info.st_size
                if total > 256 << 20:
                    raise ValueError("external context exceeds capture bound")
                archive.write(path, archive_name)
            for path in platform_files(state):
                archive.write(path, "platform/" + str(path.relative_to(state)))
        if (selected_profiles != profiles
                or external_context_files(external_contexts, workspace / ".hermes", selected_profiles)[1] != extra_inventory):
            raise ValueError("external context registration changed during capture")
        inventory = []
        for path in (payload, context):
            regular(path, 2 << 30)
            inventory.append({"path": path.name, "size": path.stat().st_size, "sha256": digest(path)})
        manifest = {"schema": "loom.hermes.native-capture.v1", "id": name,
                    "version": "0.22.2", "revision": REVISION, "workspace": str(workspace),
                    "started_at": started.isoformat(), "created_at": datetime.now(timezone.utc).isoformat(),
                    "capture_policy": "native_sqlite_backup_plus_canonical_external_context",
                    "qualification": "Online per-database snapshots, not a single global application transaction",
                    "profiles": selected_profiles,
                    "profile_inventory": profile_inventory,
                    "external_contexts": extra_inventory,
                    "files": inventory}
        canonical = json.dumps(manifest, sort_keys=True, separators=(",", ":")).encode()
        envelope = (json.dumps({"manifest": manifest, "signature": key.sign(canonical).hex()}, sort_keys=True) + "\n").encode()
        if len(envelope) > 1 << 20:
            raise ValueError("native manifest exceeds bound")
        (package / "manifest.json").write_bytes(envelope)
        for path in package.iterdir():
            with path.open("rb") as stream:
                os.fsync(stream.fileno())
            path.chmod(0o440)
        synced_directory(package)
        final = destination / name
        os.rename(package, final)
        synced_directory(destination)
        temporary.rmdir()
        return {"id": name, "path": str(final), "profiles": manifest["profiles"],
                "profile_inventory_assurance": "verified_native_selected_critical_inventory",
                "external_contexts": extra_inventory,
                "members": len(names), "files": inventory, "cloud_protection": "pending_normal_upload"}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--workspace", default="/srv/loom/agents/mina")
    parser.add_argument("--state", default="/var/lib/loom/mina-hermes")
    parser.add_argument("--destination", default="/srv/loom/agents/mina/recovery-hermes-native")
    parser.add_argument("--binary")
    parser.add_argument("--signing-key", default="/var/lib/loom/mina-recovery/signing.key")
    parser.add_argument("--verify-package")
    parser.add_argument("--public-key")
    parser.add_argument("--external-contexts", help="Trusted bounded Box bootstrap-context registry")
    args = parser.parse_args()
    try:
        if args.verify_package:
            print(json.dumps(verify(args.verify_package, args.public_key)))
        elif args.binary:
            print(json.dumps(capture(args.workspace, args.state, args.destination, args.binary, args.signing_key, args.external_contexts)))
        else:
            parser.error("--binary is required for capture")
    except Exception as error:
        print(json.dumps(failure_summary(error)))
        raise SystemExit(1)
