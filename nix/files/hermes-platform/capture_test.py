"""Focused metadata/preflight checks; no live capture or private file reads."""

import importlib.util
import io
import json
import os
from pathlib import Path
import stat
import sys
import tempfile
import types
import unittest
import warnings
from unittest.mock import Mock, patch
import zipfile

from cryptography.hazmat.primitives import serialization

spec = importlib.util.spec_from_file_location("capture", Path(__file__).with_name("capture.py"))
capture = importlib.util.module_from_spec(spec)
spec.loader.exec_module(capture)


class CaptureTest(unittest.TestCase):
    def profile_inventory(self, paths):
        return {"profiles": ["tyler"], "profile_inventory": {
            "schema": capture.PROFILE_INVENTORY_SCHEMA, "profiles": {"tyler": sorted(paths)}}}

    def profile_zip(self, paths):
        stream = io.BytesIO()
        with zipfile.ZipFile(stream, "w") as archive:
            for name in ["config.yaml", "state.db", *paths]:
                archive.writestr(name, "fixture bytes")
        return zipfile.ZipFile(stream)

    def signed_package(self, root, manifest, members):
        package = root / "hermes-native-fixture"
        package.mkdir(exist_ok=True)
        for name, paths in (("profile.zip", members), ("context.zip", [])):
            with zipfile.ZipFile(package / name, "w") as archive:
                for path in paths:
                    archive.writestr(path, "private fixture contents must not enter the receipt")
        manifest = dict(manifest, schema="loom.hermes.native-capture.v1", revision=capture.REVISION,
                        id=package.name, created_at="2026-11-08T00:00:00+00:00", files=[
                            {"path": name, "size": (package / name).stat().st_size,
                             "sha256": capture.digest(package / name)} for name in ("profile.zip", "context.zip")])
        key = capture.Ed25519PrivateKey.generate()
        canonical = json.dumps(manifest, sort_keys=True, separators=(",", ":")).encode()
        (package / "manifest.json").write_text(json.dumps({"manifest": manifest, "signature": key.sign(canonical).hex()}))
        public = key.public_key().public_bytes(serialization.Encoding.Raw, serialization.PublicFormat.Raw).hex()
        return package, public

    def test_critical_inventory_uses_native_selection_without_reading_payloads(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d).resolve()
            home, output = root / ".hermes", root / "profile.zip"
            paths = ["config.yaml", "profile.yaml", "SOUL.md", "state.db", "memories/MEMORY.md",
                     "memories/USER.md", "sessions/conversation.json", "skills/learned/SKILL.md",
                     "home/.config/gh/hosts.yml", "home/bin/gh", "context/private.md",
                     "cron/jobs.json", "loom-box-owner.json", "loom-tool-context.json"]
            # A fixture native selector is the authority: do not infer names
            # from templates, file contents, or everything under a ZIP prefix.
            relative = [Path("profiles/tyler") / name for name in paths]
            relative += [Path("profiles/tyler/logs/runtime.log"), Path("config.yaml"),
                         Path("profiles/other/memories/MEMORY.md")]
            native = types.ModuleType("hermes_cli.backup")
            native._iter_backup_files = Mock(return_value=[(home / p, p) for p in relative])
            with patch.dict(sys.modules, {"hermes_cli.backup": native}), patch.object(Path, "read_bytes", side_effect=AssertionError("payload read")):
                inventory = capture.selected_profile_inventory(home, output, ["tyler"])
            native._iter_backup_files.assert_called_once_with(home, output)
            self.assertEqual(inventory, self.profile_inventory(paths)["profile_inventory"])
            self.assertNotIn("other", inventory["profiles"])
            # Changing learned file bytes is not a membership change.
            members = ["profiles/tyler/" + name for name in paths]
            with self.profile_zip(members) as archive:
                with patch.object(archive, "open", side_effect=AssertionError("extra payload walk")):
                    self.assertEqual(capture.verify_profile_inventory({"profiles": ["tyler"], "profile_inventory": inventory}, archive),
                                     "verified_native_selected_critical_inventory")

    def test_crc_valid_incomplete_native_profile_is_refused(self):
        paths = ["config.yaml", "SOUL.md", "state.db", "memories/MEMORY.md", "skills/learned/SKILL.md",
                 "sessions/conversation.json", "home/.config/gh/hosts.yml", "loom-tool-context.json"]
        manifest = self.profile_inventory(paths)
        for omitted in paths:
            with self.subTest(omitted=omitted), self.profile_zip(["profiles/tyler/" + p for p in paths if p != omitted]) as archive:
                self.assertIsNone(archive.testzip())
                with self.assertRaisesRegex(ValueError, "critical inventory differs"):
                    capture.verify_profile_inventory(manifest, archive)

    def test_signed_inventory_is_rechecked_and_legacy_is_qualified(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            manifest = self.profile_inventory(["config.yaml", "memories/MEMORY.md"])
            members = ["config.yaml", "state.db", "profiles/tyler/config.yaml", "profiles/tyler/memories/MEMORY.md"]
            package, public = self.signed_package(root, manifest, members)
            with patch.object(capture, "digest", wraps=capture.digest) as digest:
                result = capture.verify(package, public)
            self.assertEqual(digest.call_count, 2)
            self.assertEqual(result["status"], "verified")
            self.assertEqual(result["profile_inventory_assurance"], "verified_native_selected_critical_inventory")
            self.assertNotIn("private fixture contents", (package / "manifest.json").read_text())
            self.assertNotIn("private fixture contents", json.dumps(result))
            # Re-sign hashes for an internally intact but incomplete ZIP: byte
            # integrity alone must not turn this into a complete profile.
            package, public = self.signed_package(root, manifest, members[:-1])
            with self.assertRaisesRegex(ValueError, "critical inventory differs"):
                capture.verify(package, public)
            package, public = self.signed_package(root, {"profiles": ["tyler"]}, members[:-1])
            result = capture.verify(package, public)
            self.assertEqual(result["status"], "verified_legacy")
            self.assertEqual(result["profile_inventory_assurance"], "legacy_unrecorded")
            self.assertIn("not recorded", result["qualification"])
            # A malformed new inventory cannot downgrade itself to legacy.
            package, public = self.signed_package(root, {"profiles": ["tyler"], "profile_inventory": None}, members)
            with self.assertRaisesRegex(ValueError, "invalid native profile inventory"):
                capture.verify(package, public)

    def test_critical_inventory_rejects_duplicates_aliases_and_nonregular_members(self):
        manifest = self.profile_inventory(["config.yaml"])
        for name in (".", "../config.yaml", "/config.yaml", "home/../config.yaml", "home//file", "home\\file"):
            with self.subTest(name=name), self.profile_zip([]) as archive:
                with self.assertRaises(ValueError):
                    capture.verify_profile_inventory(self.profile_inventory([name]), archive)
        for paths in ([], ["config.yaml", "config.yaml"]):
            with self.profile_zip([]) as archive, self.assertRaises(ValueError):
                capture.verify_profile_inventory(self.profile_inventory(paths), archive)
        for mode in (stat.S_IFLNK | 0o777, stat.S_IFDIR | 0o700):
            with self.profile_zip([]) as archive:
                stream = archive.fp
                archive.close()
                with zipfile.ZipFile(stream, "a") as writer:
                    member = zipfile.ZipInfo("profiles/tyler/config.yaml")
                    member.external_attr = mode << 16
                    writer.writestr(member, "not a regular file")
                with zipfile.ZipFile(stream) as checked, self.assertRaises(ValueError):
                    capture.verify_profile_inventory(manifest, checked)
        with self.profile_zip(["profiles/tyler/config.yaml"]) as archive:
            stream = archive.fp
            archive.close()
            with warnings.catch_warnings(), zipfile.ZipFile(stream, "a") as writer:
                warnings.simplefilter("ignore", UserWarning)
                writer.writestr("profiles/tyler/config.yaml", "duplicate")
            with zipfile.ZipFile(stream) as checked, self.assertRaises(ValueError):
                capture.verify_profile_inventory(manifest, checked)

    def test_inventory_profile_membership_must_match_and_cannot_be_empty(self):
        with self.profile_zip(["profiles/tyler/config.yaml"]) as archive:
            manifest = self.profile_inventory(["config.yaml"])
            for profiles in ([], ["tyler", "tyler"], ["other"]):
                with self.subTest(profiles=profiles), self.assertRaises(ValueError):
                    capture.verify_profile_inventory(dict(manifest, profiles=profiles), archive)
            with self.assertRaises(ValueError):
                capture.verify_profile_inventory(self.profile_inventory(["config.yaml", "home/absent"]), archive)
        native = types.ModuleType("hermes_cli.backup")
        native._iter_backup_files = lambda *_: iter(())
        with patch.dict(sys.modules, {"hermes_cli.backup": native}), self.assertRaisesRegex(ValueError, "no selected critical inventory"):
            capture.selected_profile_inventory(Path("/fixture"), Path("/fixture.zip"), ["tyler"])

    def test_critical_membership_is_exact_but_noncritical_native_files_are_allowed(self):
        manifest = self.profile_inventory(["config.yaml"])
        with self.profile_zip(["profiles/tyler/config.yaml", "profiles/tyler/logs/runtime.log"]) as archive:
            self.assertEqual(capture.verify_profile_inventory(manifest, archive), "verified_native_selected_critical_inventory")
        with self.profile_zip(["profiles/tyler/config.yaml", "profiles/tyler/home/new-context.md"]) as archive:
            with self.assertRaisesRegex(ValueError, "critical inventory differs"):
                capture.verify_profile_inventory(manifest, archive)
        with self.profile_zip([]) as archive:
            self.assertEqual(capture.verify_profile_inventory({"profiles": [], "profile_inventory": {
                "schema": capture.PROFILE_INVENTORY_SCHEMA, "profiles": {}}}, archive),
                "verified_native_selected_critical_inventory")

    def register_context(self, root, box, **changes):
        entry = {"profile": "tyler", "box_id": "box_company", "root": str(box),
                 "paths": [".loom/box.yaml", "AGENTS.md", ".agents/skills"]}
        entry.update(changes)
        registry = root / "external-contexts.json"
        registry.write_text(json.dumps({"schema": "loom.hermes.external-contexts.v1", "contexts": [entry]}))
        registry.chmod(0o600)
        return registry

    def prepare_box(self, root):
        box = root / "company"
        (box / ".loom").mkdir(parents=True)
        (box / ".loom/box.yaml").write_text("schema_version: loom.box.v0.5\nbox_id: box_company\nroot_path: " + str(box) + "\n")
        (box / "AGENTS.md").write_text("Company bootstrap only")
        (box / ".agents/skills/sample").mkdir(parents=True)
        (box / ".agents/skills/sample/SKILL.md").write_text("Neutral tool")
        (box / "Documents").mkdir()
        (box / "Documents/private.txt").write_text("User payload is not bootstrap context")
        return box

    def test_registered_external_box_context_is_bounded_and_namespaced(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d).resolve()
            box = self.prepare_box(root)
            registry = self.register_context(root, box)
            files, inventory = capture.external_context_files(registry, root / "mina/.hermes", ["tyler"])
            self.assertEqual(len(files), 3)
            self.assertEqual(inventory[0]["status"], "selected")
            self.assertEqual(inventory[0]["missing_paths"], [])
            self.assertTrue(all(name.startswith("boxes/box_company/profiles/tyler/") for _, name in files))
            self.assertFalse(any("Documents" in name or "mina" in name for _, name in files))
            with self.assertRaises(ValueError):
                capture.external_context_files(self.register_context(root, box, paths=[".loom/box.yaml", "Documents"]), root / "mina/.hermes", ["tyler"])

    def test_unactivated_external_context_is_honestly_unavailable(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d).resolve()
            box = root / "not-created"
            registry = self.register_context(root, box)
            files, inventory = capture.external_context_files(registry, root / "mina/.hermes", [])
            self.assertEqual(files, [])
            self.assertEqual(inventory[0]["status"], "unavailable")
            self.assertFalse(box.exists())
            box = self.prepare_box(root)
            registry = self.register_context(root, box)
            files, inventory = capture.external_context_files(registry, root / "mina/.hermes", [])
            self.assertEqual(files, [])
            self.assertEqual(inventory[0]["status"], "unavailable")

    def test_external_identity_alias_and_duplicate_are_refused(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d).resolve()
            box = self.prepare_box(root)
            registry = self.register_context(root, box, box_id="box_different")
            with self.assertRaises(ValueError):
                capture.external_context_files(registry, root / "mina/.hermes", ["tyler"])
            registry = self.register_context(root, box)
            value = json.loads(registry.read_text())
            value["contexts"] *= 2
            registry.write_text(json.dumps(value))
            with self.assertRaises(ValueError):
                capture.external_context_files(registry, root / "mina/.hermes", ["tyler"])
            registry = self.register_context(root, box)
            (box / "AGENTS.md").unlink()
            (box / "AGENTS.md").symlink_to(box / "Documents/private.txt")
            with self.assertRaises(ValueError):
                capture.external_context_files(registry, root / "mina/.hermes", ["tyler"])

    def test_native_named_profile_inventory_includes_tyler(self):
        import sys
        import types
        with tempfile.TemporaryDirectory() as d:
            home = Path(d).resolve()
            for name in ("tyler", "loom-project", ".staging"):
                (home / "profiles" / name).mkdir(parents=True)
                (home / "profiles" / name / "config.yaml").write_text("{}")
            constants = types.SimpleNamespace(PROFILE_ID_RE=capture.re.compile(r"[a-z][a-z0-9-]*"),
                                            named_profile_has_identity=lambda p: (p / "config.yaml").is_file())
            with patch.dict(sys.modules, hermes_constants=constants):
                self.assertEqual(capture.named_profiles(home), ["loom-project", "tyler"])

    def test_external_manifest_inventory_requires_exact_profile_namespace(self):
        prefix = "boxes/box_company/profiles/tyler/"
        context = {"profile": "tyler", "box_id": "box_company", "archive_prefix": prefix,
                   "status": "selected", "files": [prefix + ".loom/box.yaml", prefix + "AGENTS.md"]}
        manifest = {"profiles": ["tyler"], "external_contexts": [context]}
        capture.verify_context_inventory(manifest, set(context["files"]) | {"workspace/AGENTS.md"})
        with self.assertRaises(ValueError):
            capture.verify_context_inventory(manifest, {prefix + ".loom/box.yaml"})
        with self.assertRaises(ValueError):
            capture.verify_context_inventory(dict(manifest, profiles=[]), set(context["files"]))
        with self.assertRaises(ValueError):
            capture.verify_context_inventory(manifest, set(context["files"]) | {prefix + "Documents/private.txt"})
        capture.verify_context_inventory({"profiles": []}, {"workspace/AGENTS.md"})

    def test_owner_failure_has_safe_reason_and_relative_path(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            p = root / "operator-before.md"
            p.write_text("private content must not be logged")
            with patch.object(capture.os, "getuid", return_value=os.getuid() + 1):
                with self.assertRaises(capture.CaptureValidationError) as failure:
                    capture.platform_files(root)
                capture.regular(p, 1 << 20, check_owner=False)
            result = capture.failure_summary(failure.exception)
            self.assertEqual(result["reason"], "owner_mismatch")
            self.assertEqual(result["path"], p.name)
            self.assertEqual(result["phase"], "platform_preflight")
            self.assertNotIn("private content", str(result))
            self.assertNotIn(d, str(result))

    def test_preflight_precedes_native_backup_and_staging(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d).resolve()
            workspace, state, destination = [root / n for n in ("workspace", "state", "destination")]
            for p in (workspace, state, destination):
                p.mkdir()
            key = root / "signing.key"
            key.write_bytes(bytes(64))
            key.chmod(0o600)
            with patch.object(capture, "platform_files", side_effect=capture.CaptureValidationError("owner_mismatch", "receipt.md", "platform_preflight")), patch.object(capture.subprocess, "run") as run:
                with self.assertRaises(capture.CaptureValidationError):
                    capture.capture(workspace, state, destination, "/unused/hermes", key)
                run.assert_not_called()
            self.assertFalse((destination / ".staging").exists())

    def test_valid_inventory_and_symlink_or_hardlink_refusal(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            p = root / "state.json"
            p.write_text("{}")
            self.assertEqual(capture.platform_files(root), [p])
            link = root / "link"
            link.symlink_to(p)
            with self.assertRaises(capture.CaptureValidationError):
                capture.platform_files(root)
            link.unlink()
            os.link(p, link)
            with self.assertRaises(capture.CaptureValidationError):
                capture.platform_files(root)


if __name__ == "__main__":
    unittest.main()
