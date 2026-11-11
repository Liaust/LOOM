"""Focused adoption checks using the selected native runtime, no replica service."""

import importlib.util
import json
import os
from pathlib import Path
import stat
import tempfile
from types import SimpleNamespace
import unittest
from unittest import mock


spec = importlib.util.spec_from_file_location("bootstrap", Path(__file__).with_name("bootstrap.py"))
bootstrap = importlib.util.module_from_spec(spec)
spec.loader.exec_module(bootstrap)


class AdoptionTest(unittest.TestCase):
    def test_adoption_preserves_history_and_later_settings(self):
        import yaml

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            workspace = root / "mina"
            home = workspace / ".hermes"
            state = root / "state"
            home.mkdir(parents=True)
            state.mkdir()
            (workspace / "SOUL.md").write_text("Current MINA personality\n")
            (home / "SOUL.md").write_text("Historical personality\n")
            (home / "config.yaml").write_text("model:\n  default: user-choice\nplatforms:\n  discord:\n    enabled: true\ncustom: preserve\n")
            (home / "history-marker").write_bytes(b"unchanged history")
            first = bootstrap.prepare(workspace, state)
            self.assertEqual(first["status"], "adopted")
            config = yaml.safe_load((home / "config.yaml").read_text())
            self.assertEqual(config["model"]["default"], "user-choice")
            self.assertEqual(config["custom"], "preserve")
            self.assertFalse(config["platforms"]["discord"]["enabled"])
            self.assertEqual((home / "SOUL.md").resolve(), workspace / "SOUL.md")
            self.assertEqual((state / "before-adoption/SOUL.md").read_text(), "Historical personality\n")
            self.assertEqual((home / "history-marker").read_bytes(), b"unchanged history")
            auth = (state / "client-auth.json").read_bytes()
            config["model"]["default"] = "later-user-choice"
            (home / "config.yaml").write_text(yaml.safe_dump(config))
            learned = {
                workspace / "SOUL.md": b"Later agent-owned personality\n",
                home / "memories/USER.md": b"Later user preferences\n",
                home / "memories/MEMORY.md": b"Later agent memory\n",
                home / "skills/created/local-skill/SKILL.md": b"Later native skill\n",
            }
            for path, content in learned.items():
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(content)
            self.assertEqual(bootstrap.prepare(workspace, state)["status"], "already_adopted")
            for path, content in learned.items():
                self.assertEqual(path.read_bytes(), content)
            self.assertEqual((home / "SOUL.md").resolve(), workspace / "SOUL.md")
            self.assertEqual((state / "client-auth.json").read_bytes(), auth)
            self.assertEqual(yaml.safe_load((home / "config.yaml").read_text())["model"]["default"], "later-user-choice")
            self.assertEqual((state / "client-auth.json").stat().st_mode & 0o777, 0o600)

    def test_malformed_settings_are_not_rewritten(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            workspace = root / "mina"
            home = workspace / ".hermes"
            state = root / "state"
            home.mkdir(parents=True)
            state.mkdir()
            (workspace / "SOUL.md").write_text("MINA\n")
            original = "skills:\n  external_dirs: invalid\n"
            (home / "config.yaml").write_text(original)
            with self.assertRaises(ValueError):
                bootstrap.prepare(workspace, state)
            self.assertEqual((home / "config.yaml").read_text(), original)
            self.assertFalse((state / "adoption.json").exists())


class ExternalContextProvisioningTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve()
        self.workspace = self.root / "mina"
        self.home = self.workspace / ".hermes"
        self.state = self.root / "state"
        self.home.mkdir(parents=True)
        self.state.mkdir(mode=0o750)
        self.state.chmod(0o750)
        self.registry = self.state / "external-contexts.json"
        self.preserved = {
            self.workspace / "SOUL.md": b"Current learned personality\n",
            self.home / "config.yaml": b"model: preserve-current-choice\n",
            self.home / "memories/USER.md": b"Current user preferences\n",
            self.home / "memories/MEMORY.md": b"Current learned memory\n",
            self.home / "skills/created/example/SKILL.md": b"Current created skill\n",
            self.state / "adoption.json": json.dumps({"revision": bootstrap.REVISION, "home": str(self.home)}).encode(),
            self.state / "dashboard.env": b"retained environment fixture\n",
            self.state / "client-auth.json": b"retained credential fixture\n",
        }
        for path, raw in self.preserved.items():
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(raw)

    def prepare(self):
        return bootstrap.prepare(self.workspace, self.state)

    def assert_preserved(self):
        for path, raw in self.preserved.items():
            self.assertEqual(path.read_bytes(), raw, str(path))

    def registry_value(self):
        return {"schema": bootstrap.CONTEXT_SCHEMA, "contexts": [{
            "profile": "tyler", "box_id": "box_company", "root": str(self.root / "company"),
            "paths": [".loom/box.yaml", "AGENTS.md", ".agents/skills"],
        }]}

    def write_registry(self, value=None):
        self.registry.write_text(json.dumps(self.registry_value() if value is None else value, indent=2) + "\n")
        self.registry.chmod(0o600)

    def test_adopted_state_repairs_only_managed_parent_and_initializes_empty_registry(self):
        before_modes = {path: stat.S_IMODE(path.stat().st_mode) for path in (self.root, self.workspace, self.home, self.home / "memories")}
        self.assertEqual(self.prepare()["status"], "already_adopted")
        self.assertEqual(stat.S_IMODE(self.state.stat().st_mode), 0o700)
        self.assertEqual(stat.S_IMODE(self.registry.stat().st_mode), 0o600)
        self.assertEqual(json.loads(self.registry.read_bytes()), {"schema": bootstrap.CONTEXT_SCHEMA, "contexts": []})
        self.assertEqual({path: stat.S_IMODE(path.stat().st_mode) for path in before_modes}, before_modes)
        self.assert_preserved()
        self.assertFalse((self.state / "before-adoption").exists())

    def test_adopted_existing_registry_retains_bytes_inode_and_permissions(self):
        self.write_registry()
        before, info = self.registry.read_bytes(), self.registry.stat()
        for _ in range(2):
            self.assertEqual(self.prepare()["status"], "already_adopted")
            self.assertEqual(self.registry.read_bytes(), before)
            self.assertEqual(self.registry.stat().st_ino, info.st_ino)
            self.assertEqual(self.registry.stat().st_mtime_ns, info.st_mtime_ns)
            self.assertEqual(stat.S_IMODE(self.registry.stat().st_mode), 0o600)
        self.assert_preserved()

    def test_existing_safe_root_owned_registry_is_preserved(self):
        self.write_registry()
        self.registry.chmod(0o644)
        before, inode = self.registry.read_bytes(), self.registry.stat().st_ino
        actual_stat, actual_fstat = os.stat, os.fstat
        def root_owned(info):
            if info.st_ino != inode:
                return info
            fields = {name: getattr(info, name) for name in ("st_dev", "st_ino", "st_uid", "st_mode", "st_nlink", "st_size", "st_mtime_ns", "st_ctime_ns")}
            fields["st_uid"] = 0
            return SimpleNamespace(**fields)
        with mock.patch.object(bootstrap.os, "stat", side_effect=lambda *a, **kw: root_owned(actual_stat(*a, **kw))), mock.patch.object(bootstrap.os, "fstat", side_effect=lambda fd: root_owned(actual_fstat(fd))):
            self.assertEqual(self.prepare()["status"], "already_adopted")
        self.assertEqual(self.registry.read_bytes(), before)
        self.assertEqual(self.registry.stat().st_ino, inode)
        self.assertEqual(stat.S_IMODE(self.registry.stat().st_mode), 0o644)
        self.assert_preserved()

    def test_registry_symlink_directory_hardlink_and_unsafe_mode_are_rejected(self):
        cases = ("symlink", "directory", "hardlink", "writable")
        for kind in cases:
            with self.subTest(kind=kind):
                target = self.root / (kind + "-target")
                if kind == "directory":
                    self.registry.mkdir()
                elif kind == "symlink":
                    target.write_bytes(b"untouched fixture")
                    self.registry.symlink_to(target)
                elif kind == "hardlink":
                    target.write_bytes(b"untouched fixture")
                    os.link(target, self.registry)
                else:
                    self.write_registry()
                    self.registry.chmod(0o620)
                with self.assertRaises((ValueError, OSError)):
                    self.prepare()
                if kind == "directory":
                    self.registry.rmdir()
                else:
                    self.registry.unlink()
                if kind in ("symlink", "hardlink"):
                    self.assertEqual(target.read_bytes(), b"untouched fixture")
                self.assert_preserved()

    def test_registry_bound_and_invalid_schema_or_entries_are_rejected_without_rewrite(self):
        for raw in (b"x" * (bootstrap.MAX_CONTEXT_BYTES + 1), b"not-json", b"[]",
                    b'{"schema":"wrong","contexts":[]}',
                    json.dumps({"schema": bootstrap.CONTEXT_SCHEMA, "contexts": [None]}).encode(),
                    json.dumps({"schema": bootstrap.CONTEXT_SCHEMA, "contexts": [{"profile": "tyler"}]}).encode(),
                    json.dumps({"schema": bootstrap.CONTEXT_SCHEMA, "contexts": [self.registry_value()["contexts"][0]] * 129}).encode()):
            with self.subTest(size=len(raw)):
                self.registry.write_bytes(raw)
                self.registry.chmod(0o600)
                with self.assertRaises((ValueError, UnicodeError)):
                    self.prepare()
                self.assertEqual(self.registry.read_bytes(), raw)
                self.assert_preserved()

    def test_wrong_registry_and_state_owners_are_rejected_without_chown(self):
        self.write_registry()
        actual_fstat = os.fstat
        for path in (self.registry, self.state):
            inode = path.stat().st_ino
            def foreign(fd):
                info = actual_fstat(fd)
                if info.st_ino != inode:
                    return info
                fields = {name: getattr(info, name) for name in ("st_dev", "st_ino", "st_uid", "st_mode", "st_nlink", "st_size", "st_mtime_ns", "st_ctime_ns")}
                fields["st_uid"] = os.getuid() + 1
                return SimpleNamespace(**fields)
            with self.subTest(path=path), mock.patch.object(bootstrap.os, "fstat", side_effect=foreign):
                with self.assertRaises(ValueError):
                    self.prepare()
            self.assertEqual(path.stat().st_uid, os.getuid())
        self.assert_preserved()

    def test_state_symlink_and_alias_ancestors_are_rejected(self):
        alias = self.root / "state-alias"
        alias.symlink_to(self.state, target_is_directory=True)
        with self.assertRaises(ValueError):
            bootstrap.prepare(self.workspace, alias)
        parent_alias = self.root / "root-alias"
        parent_alias.symlink_to(self.root, target_is_directory=True)
        with self.assertRaises(ValueError):
            bootstrap.prepare(self.workspace, parent_alias / "state")
        self.assertEqual(stat.S_IMODE(self.state.stat().st_mode), 0o750)
        self.assertFalse(self.registry.exists())

    def test_concurrent_creation_is_not_overwritten(self):
        winner = json.dumps(self.registry_value()).encode()
        actual_link = os.link
        def race(source, destination, **options):
            self.registry.write_bytes(winner)
            self.registry.chmod(0o600)
            return actual_link(source, destination, **options)
        with mock.patch.object(bootstrap.os, "link", side_effect=race):
            with self.assertRaisesRegex(ValueError, "appeared during provisioning"):
                self.prepare()
        self.assertEqual(self.registry.read_bytes(), winner)
        self.assertEqual(list(self.state.glob(".loom-contexts-*")), [])
        self.assert_preserved()

    def test_concurrent_replacement_during_validation_is_rejected_and_preserved(self):
        self.write_registry()
        winner = b'{"schema":"loom.hermes.external-contexts.v1","contexts":[]}\n'
        actual_loads = bootstrap.json.loads
        def race(raw):
            parsed = actual_loads(raw)
            temporary = self.state / "concurrent-registry"
            temporary.write_bytes(winner)
            temporary.chmod(0o600)
            os.replace(temporary, self.registry)
            return parsed
        with mock.patch.object(bootstrap.json, "loads", side_effect=race):
            with self.assertRaisesRegex(ValueError, "changed during validation"):
                self.prepare()
        self.assertEqual(self.registry.read_bytes(), winner)
        self.assert_preserved()

    def test_busy_registry_lock_blocks_provisioning(self):
        lock = self.state / "external-contexts.json.lock"
        lock.touch(mode=0o600)
        with lock.open("a") as stream:
            bootstrap.fcntl.flock(stream, bootstrap.fcntl.LOCK_EX | bootstrap.fcntl.LOCK_NB)
            with self.assertRaises(BlockingIOError):
                self.prepare()
        self.assertFalse(self.registry.exists())


if __name__ == "__main__":
    unittest.main()
