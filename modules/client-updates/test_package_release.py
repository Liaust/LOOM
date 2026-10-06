import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from package_release import package, COMPATIBILITY


class PackageTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.build = self.root / "build"
        self.build.mkdir()
        self.identity = {"schema": "loom.client-bundle.v1", "component": "notes-workspace", "adapter": "obsidian-plugin", "pluginId": "obsidian-livesync", "releaseId": "client-notes-0.1.0", "sequence": 1, "version": "0.1.0", "channel": "stable", "sourceCommit": "a" * 40, "upstream": {"revision": "7b3b6ff854f2ff10452be3eaf8e0cd76ac65ac2b", "version": "1.0.32"}, "compatibility": COMPATIBILITY}
        (self.build / "main.js").write_text("compiled LOOM code")
        (self.build / "styles.css").write_text("body {}")
        (self.build / "manifest.json").write_text(json.dumps({"id": "obsidian-livesync", "version": "1.0.32", "minAppVersion": "1.7.2", "isDesktopOnly": False}))
        self.receipt()

    def receipt(self):
        (self.build / "loom-release.json").write_text(json.dumps(self.identity))
        files = {name: hashlib.sha256((self.build / name).read_bytes()).hexdigest() for name in ("main.js", "manifest.json", "styles.css", "loom-release.json")}
        (self.build / "loom-client-build.json").write_text(json.dumps({"files": files, "release": self.identity, "installed": False,
            "upstream": self.identity["upstream"]["revision"], "patchManifestSha256": "b" * 64, "node": "v26.7.0"}))

    def test_exact_code_and_no_state_or_network(self):
        release = package(self.build, self.root / "out")
        self.assertEqual(release["sequence"], 1)
        self.assertEqual(set(p.name for p in (self.root / "out").iterdir()), {"main.js", "manifest.json", "styles.css", "loom-client-build.json", "loom-release.json", "release.json", "LICENSE.upstream"})
        for item in release["files"]:
            self.assertEqual((self.root / "out" / item["name"]).read_bytes(), (self.build / item["name"]).read_bytes())
        with self.assertRaises(ValueError):
            package(self.build, self.root / "out")

    def test_hash_drift_refuses_before_output(self):
        (self.build / "main.js").write_text("altered")
        with self.assertRaises(ValueError):
            package(self.build, self.root / "out")
        self.assertFalse((self.root / "out").exists())

    def test_private_input_refused(self):
        for name in ("data.json", "loom-intent-backup-0.json", ".env.local"):
            with self.subTest(name=name):
                path = self.build / name
                path.write_text("private")
                with self.assertRaises(ValueError):
                    package(self.build, self.root / "out")
                path.unlink()
        receipt_path = self.build / "loom-client-build.json"
        receipt = json.loads(receipt_path.read_text())
        receipt["privateHost"] = "operator-only"
        receipt_path.write_text(json.dumps(receipt))
        with self.assertRaises(ValueError):
            package(self.build, self.root / "out")

    def test_symlink_code_refused(self):
        code = self.build / "main.js"
        code.rename(self.root / "outside")
        code.symlink_to(self.root / "outside")
        with self.assertRaises(OSError):
            package(self.build, self.root / "out")

    def test_unknown_compatibility_and_development_refused(self):
        self.identity["compatibility"] = {**COMPATIBILITY, "journalSchema": 2}
        self.receipt()
        with self.assertRaises(ValueError):
            package(self.build, self.root / "out")
        self.identity["compatibility"] = COMPATIBILITY
        self.identity["sequence"] = 0
        self.receipt()
        with self.assertRaises(ValueError):
            package(self.build, self.root / "out")


if __name__ == "__main__":
    unittest.main()
