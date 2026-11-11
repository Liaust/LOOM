import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("notes_recovery", ROOT / "scripts/loom-notes-recovery.py")
recovery = importlib.util.module_from_spec(spec)
spec.loader.exec_module(recovery)


class EnrollmentRecoveryTest(unittest.TestCase):
    def test_fresh_recovery_membership_is_independent_of_stale_delivery(self):
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory).resolve()
            live = state / "live"
            live.mkdir()
            cfg, runtime, legacy = self.inventory_fixture(state, [{
                "Collection": "live", "SourceCollection": "live-source", "Root": "Notes",
                "status": "held", "source_generation": ""}])
            raw_before = (state / "enrollment.json").read_bytes()
            prepared = dict(legacy, schema_version="notes_workspace.recovery_enrollment.v1", collections=[{
                "Collection": "live", "SourceCollection": "live-source", "Root": "Notes",
                "status": "current_snapshot", "source_generation": "current-owner-generation",
                "source_path": str(live), "source_intake_stage": "current_filesystem_snapshot",
                "recovery_missing_deferred": 6898}])
            collections, raw, actual = recovery.recovery_membership(cfg, runtime, state, prepared)
            self.assertEqual(collections, {"live": str(live)})
            self.assertEqual(actual, json.loads(raw))
            self.assertEqual(actual["collections"][0]["recovery_missing_deferred"], 6898)
            self.assertEqual((state / "enrollment.json").read_bytes(), raw_before)
            for field, value in [("status", "active"), ("source_intake_stage", "admitted"), ("recovery_missing_deferred", -1)]:
                bad = json.loads(json.dumps(prepared))
                bad["collections"][0][field] = value
                with self.assertRaises(ValueError):
                    recovery.recovery_membership(cfg, runtime, state, bad)

    def inventory_fixture(self, state, entries):
        runtime = {"schema_version": "notes_workspace.runtime.v1", "command": "/bin/sync",
                   "replica": "one", "replica_dir": str(state / "replica"),
                   "settings": "/private/settings.json", "scopes": [], "layout_publication": True}
        inventory = {"schema_version": "notes_workspace.enrollment.v1", "replica": "one",
                     "config_sha256": recovery.runtime_digest(runtime), "collections": entries,
                     "holds": [{"source_collection": "unverified", "reason": "archive_reference_unverified"}]}
        path = state / "enrollment.json"
        path.write_text(json.dumps(inventory))
        path.chmod(0o600)
        return {"enrollment_inventory": str(path), "collections": {"old": "/not-used"}}, runtime, inventory

    def archive_reference(self):
        return {"Collection": "archived", "SourceCollection": "archive-source", "Root": "Projects/_Archived/Old/Notes",
                "archived": True, "status": "reference", "source_generation": "exact-archive-generation",
                "archive_operation": "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAV",
                "archive_manifest": "sha256:" + "a" * 64, "reference_files": 2, "reference_bytes": 1234}

    def test_archive_reference_identity_is_preserved_without_payload_copy(self):
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory).resolve()
            live = state / "live"
            live.mkdir()
            entry = {"Collection": "live", "SourceCollection": "live-source", "Root": "Projects/_Passive/Live/Notes",
                     "status": "active", "work_state": "passive", "source_path": str(live), "source_generation": "live-generation"}
            reference = self.archive_reference()
            cfg, runtime, inventory = self.inventory_fixture(state, [entry, reference])
            with patch.object(recovery, "exact_directory", wraps=recovery.exact_directory) as exact:
                collections, raw, captured = recovery.recovery_membership(cfg, runtime, state)
                self.assertEqual(collections, {"live": str(live)})
                exact.assert_called_once_with(str(live))
            self.assertEqual(captured, inventory)
            self.assertEqual(json.loads(raw), inventory)
            self.assertNotIn("cloud_verified", captured)
            # A stale path in reference metadata never becomes a live archive walk.
            reference["source_path"] = "/must-not-copy-retained-payload"
            cfg, runtime, inventory = self.inventory_fixture(state, [reference])
            with patch.object(recovery, "exact_directory", side_effect=AssertionError("archive payload opened")):
                collections, raw, captured = recovery.recovery_membership(cfg, runtime, state)
            self.assertEqual(collections, {})
            self.assertEqual(json.loads(raw), inventory)

    def test_unadmitted_archive_candidates_do_not_block_live_recovery(self):
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory).resolve()
            live = state / "live"
            live.mkdir()
            entry = {"Collection": "live", "SourceCollection": "live-source", "Root": "Projects/Live/Notes",
                     "status": "active", "source_path": str(live), "source_generation": "live-generation"}
            held = {"Collection": "old", "SourceCollection": "unverified", "Root": "Projects/_Archived/Old/Notes",
                    "archived": True, "status": "reference_held", "source_generation": ""}
            for entries in ([entry, held], [held]):
                cfg, runtime, inventory = self.inventory_fixture(state, entries)
                collections, raw, captured = recovery.recovery_membership(cfg, runtime, state)
                self.assertEqual(collections, {"live": str(live)} if len(entries) > 1 else {})
                self.assertEqual(json.loads(raw), inventory)
                self.assertEqual(captured["holds"], inventory["holds"])
            for status in ("held", "active"):
                bad_live = dict(entry, status=status, source_generation="")
                cfg, runtime, _ = self.inventory_fixture(state, [bad_live, held])
                with self.assertRaisesRegex(ValueError, "Notes source admission is held"):
                    recovery.recovery_membership(cfg, runtime, state)
            held["archived"] = False
            cfg, runtime, _ = self.inventory_fixture(state, [entry, held])
            with self.assertRaisesRegex(ValueError, "archive reference admission is held"):
                recovery.recovery_membership(cfg, runtime, state)

    def test_reference_requires_exact_identity_and_generation(self):
        changes = [("archive_operation", None), ("archive_operation", ""), ("archive_operation", "unverified"),
                   ("archive_operation", "workspace_archive_operation_81ARZ3NDEKTSV4RRFFQ69G5FAV"),
                   ("archive_manifest", None), ("archive_manifest", "sha256:wrong"),
                   ("source_generation", None), ("source_generation", ""), ("source_generation", 1),
                   ("reference_files", -1), ("reference_bytes", True)]
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory).resolve()
            for field, value in changes:
                with self.subTest(field=field, value=value):
                    reference = self.archive_reference()
                    if value is None:
                        reference.pop(field)
                    else:
                        reference[field] = value
                    cfg, runtime, _ = self.inventory_fixture(state, [reference])
                    with self.assertRaisesRegex(ValueError, "Notes archive reference"):
                        recovery.recovery_membership(cfg, runtime, state)

    def test_inventory_is_the_recovery_membership_not_legacy_list(self):
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory).resolve()
            source = state / "new-source"
            source.mkdir()
            runtime = {"schema_version": "notes_workspace.runtime.v1", "command": "/bin/sync",
                       "replica": "one", "replica_dir": str(state / "replica"),
                       "settings": "/private/settings.json", "scopes": []}
            entry = {"Collection": "new", "SourceCollection": "source", "Root": "Projects/New/Notes",
                     "source_path": str(source), "source_generation": "exact", "status": "active"}
            inventory = {"schema_version": "notes_workspace.enrollment.v1", "replica": "one",
                         "config_sha256": recovery.runtime_digest(runtime), "collections": [entry]}
            path = state / "enrollment.json"
            path.write_text(json.dumps(inventory))
            path.chmod(0o600)
            cfg = {"enrollment_inventory": str(path), "collections": {"old": "/not-used"}}
            collections, raw, current = recovery.recovery_membership(cfg, runtime, state)
            self.assertEqual(collections, {"new": str(source)})
            self.assertEqual(current, inventory)
            self.assertEqual(json.loads(raw), inventory)
            for mode in (0o600, 0o640, 0o650, 0o644):
                path.chmod(mode)
                self.assertEqual(recovery.recovery_membership(cfg, runtime, state)[0], collections)
            for mode in (0o620, 0o602, 0o660, 0o666):
                path.chmod(mode)
                with self.assertRaises(ValueError, msg=f"non-owner write {mode:o}"):
                    recovery.recovery_membership(cfg, runtime, state)
            path.chmod(0o650)
            for change in ("configuration", "held", "duplicate", "withdrawn"):
                bad = json.loads(json.dumps(inventory))
                if change == "configuration":
                    bad["config_sha256"] = "wrong"
                elif change == "held":
                    bad["collections"][0]["status"] = "held"
                elif change == "duplicate":
                    bad["collections"].append(entry)
                else:
                    bad["collections"][0]["status"] = "withdrawn"
                path.write_text(json.dumps(bad))
                with self.assertRaises(ValueError, msg=change):
                    recovery.recovery_membership(cfg, runtime, state)
            path.unlink()
            with self.assertRaises(FileNotFoundError):
                recovery.recovery_membership(cfg, runtime, state)

    def test_legacy_reader_only_when_inventory_not_configured(self):
        runtime = {"scopes": [{"Collection": "old"}]}
        cfg = {"collections": {"old": "/legacy"}}
        self.assertEqual(recovery.recovery_membership(cfg, runtime, Path("/state"))[0], cfg["collections"])
        runtime["scopes"].append({"Collection": "new"})
        with self.assertRaises(ValueError):
            recovery.recovery_membership(cfg, runtime, Path("/state"))

    def test_config_hash_matches_go_canonical_fixture(self):
        runtime = {"schema_version": "notes_workspace.runtime.v1", "command": "/package/bin/notes-sync",
                   "replica": "pilot", "replica_dir": "/state/replica", "settings": "/private/settings.json",
                   "scopes": [{"Workspace": "pilot", "Collection": "personal", "SourceCollection": "source-1",
                               "Generation": "enrollment-1", "Root": "Personal", "admission_path": "welcome.md"}]}
        # Assert canonical bytes independently; optional zero values don't alter identity.
        import hashlib
        canonical = json.dumps(runtime, sort_keys=True, separators=(",", ":")).encode()
        self.assertEqual(recovery.runtime_digest(runtime), "sha256:" + hashlib.sha256(canonical).hexdigest())
        runtime["watch_seconds"] = 0
        runtime["scopes"][0]["path_prefix"] = ""
        runtime["layout_publication"] = False
        runtime["scopes"][0].update({"read_only": False, "archived": False, "work_state": "", "working_root": "", "desired_root": "", "layout_from": "",
                                     "layout_generation": "", "layout_read_only": False})
        self.assertEqual(recovery.runtime_digest(runtime), "sha256:" + hashlib.sha256(canonical).hexdigest())
        runtime["layout_publication"] = True
        self.assertNotEqual(recovery.runtime_digest(runtime), "sha256:" + hashlib.sha256(canonical).hexdigest())

    def test_config_hash_preserves_nonzero_layout_fields(self):
        import hashlib
        runtime = {"schema_version": "notes_workspace.runtime.v1", "command": "/bin/sync", "replica": "one",
                   "replica_dir": "/state/replica", "settings": "/private/settings.json", "layout_publication": True,
                   "scopes": [{"Workspace": "one", "Collection": "notes", "SourceCollection": "source",
                               "Generation": "current", "Root": "Projects/_Archived/Old/Notes", "admission_path": "",
                               "layout_generation": "previous-exact-generation", "layout_read_only": True}]}
        for zero_field in (None, "layout_generation", "layout_read_only"):
            with self.subTest(zero_field=zero_field):
                current = json.loads(json.dumps(runtime))
                if zero_field:
                    current["scopes"][0][zero_field] = "" if zero_field == "layout_generation" else False
                expected = json.loads(json.dumps(current))
                if zero_field:
                    expected["scopes"][0].pop(zero_field)
                canonical = json.dumps(expected, sort_keys=True, separators=(",", ":")).encode()
                self.assertEqual(recovery.runtime_digest(current), "sha256:" + hashlib.sha256(canonical).hexdigest())


if __name__ == "__main__":
    unittest.main()
