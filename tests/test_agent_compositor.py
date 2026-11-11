"""Focused compositor recovery and palette ownership checks."""
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import Mock, patch

root = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(root / "nix/files/agent-desktop"))
import compositor
import dark_palette


class CompositorTests(unittest.TestCase):
    def test_child_failure_restores_xfwm(self):
        child = Mock()
        child.wait.return_value = 1
        child.poll.return_value = 1
        with tempfile.TemporaryDirectory() as temporary, patch.object(
                compositor, "driver_environment", return_value=dict(XDG_CURRENT_DESKTOP="XFCE", XDG_SESSION_ID="42")), patch.object(
                compositor, "binding_path", return_value=Path(temporary) / "binding"), patch.object(
                compositor, "state_directory", return_value=Path(temporary)), patch.object(
                compositor, "setting", return_value="true"), patch.object(
                compositor, "recover") as recover, patch.object(
                compositor.signal, "signal"), patch.object(
                compositor.subprocess, "Popen", return_value=child), patch.dict(
                os.environ, LOOM_PICOM_BINARY="/picom", LOOM_PICOM_CONFIG="/config"):
            self.assertEqual(compositor.run_compositor(), 1)
            recover.assert_called_once()

    def test_recovery_preserves_user_choice_and_marks_inactive(self):
        for current in ["false", "true"]:
            with tempfile.TemporaryDirectory() as temporary, patch.object(
                    compositor, "state_directory", return_value=Path(temporary)), patch.object(
                    compositor, "setting", return_value=current) as setting:
                path = Path(temporary) / "picom-v1.json"
                path.write_text('{"before":"true","active":true}')
                compositor.recover()
                self.assertFalse(json.loads(path.read_text())["active"])
                self.assertEqual(setting.call_count, 2 if current == "false" else 1)

    def test_refuses_other_desktop(self):
        with patch.object(compositor, "driver_environment", return_value=dict(XDG_CURRENT_DESKTOP="GNOME")):
            with self.assertRaisesRegex(RuntimeError, "Xfce"):
                compositor.run_compositor()

    def test_palette_preserves_later_user_edits(self):
        class Store:
            def __init__(self):
                self.values = {}

            def get(self, channel, key):
                return self.values.get((channel, key))

            def put(self, channel, key, value):
                if value is None:
                    self.values.pop((channel, key), None)
                else:
                    self.values[channel, key] = value

        with tempfile.TemporaryDirectory() as temporary, patch.object(
                Path, "is_file", return_value=True), patch.dict(os.environ,
                LOOM_DARK_THEME="Colloid-Teal-Dark", LOOM_DARK_THEME_ROOT="/themes",
                LOOM_DARK_PALETTE_CSS=str(root / "nix/files/agent-desktop/near-black.css")):
            base = Path(temporary)
            css = base / "gtk.css"
            css.write_text("/* original user styles */\n")
            store = Store()
            receipt = base / "palette.json"
            dark_palette.palette(store, receipt, css)
            self.assertTrue(css.read_text().startswith("/* original user styles */"))
            css.write_text(css.read_text() + "/* later user edit */\n")
            store.put("xsettings", "/Net/ThemeName", dark_palette.GLib.Variant("s", "UserChoice"))
            dark_palette.palette(store, receipt, css)
            dark_palette.palette(store, receipt, css, restore=True)
            self.assertEqual(store.get("xsettings", "/Net/ThemeName").unpack(), "UserChoice")
            self.assertTrue(css.read_text().endswith("/* later user edit */\n"))
            self.assertEqual(len(json.loads(receipt.read_text())["held"]), 2)

    def test_config_has_only_conservative_effects(self):
        config = (root / "nix/files/agent-desktop/picom.conf").read_text()
        for text in ['corner-radius = 10', 'blur-method = "none"', 'fading = false',
                     'use-damage = true', 'fullscreen', 'MAXIMIZED_VERT', "Xfce4-screensaver"]:
            self.assertIn(text, config)


if __name__ == "__main__":
    unittest.main()
