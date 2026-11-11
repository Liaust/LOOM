import json
import os
from pathlib import Path
import tempfile
import unittest

from daemon_settings import configure


class DaemonSettingsTest(unittest.TestCase):
    def test_preserves_settings_and_disables_only_unattended_update(self):
        with tempfile.TemporaryDirectory() as home:
            configure(home)
            path = Path(home) / "app-server-daemon/settings.json"
            settings = {"remoteControlEnabled": True, "featureOverrides": {"code_mode_host": True},
                        "updater": {"autoUpdateEnabled": True, "updateIntervalMinutes": 120}}
            path.write_text(json.dumps(settings))
            configure(home)
            settings["updater"]["autoUpdateEnabled"] = False
            self.assertEqual(json.loads(path.read_text()), settings)
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
            configure(home)
            self.assertEqual(json.loads(path.read_text()), settings)

    def test_refuses_link_and_invalid_object_without_replacement(self):
        with tempfile.TemporaryDirectory() as home:
            configure(home)
            path = Path(home) / "app-server-daemon/settings.json"
            path.write_text("[]")
            with self.assertRaises(ValueError):
                configure(home)
            self.assertEqual(path.read_text(), "[]")
            path.unlink()
            other = Path(home) / "other"
            other.write_text("{}")
            path.symlink_to(other)
            with self.assertRaises(OSError):
                configure(home)
            self.assertEqual(other.read_text(), "{}")


if __name__ == "__main__":
    unittest.main()
