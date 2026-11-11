"""Focused one-time appearance and non-destructive undo checks."""
import importlib.util
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

root = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(root / "nix/files/agent-desktop"))
spec = importlib.util.spec_from_file_location("xfce_preferences", root / "nix/files/agent-desktop/xfce_preferences.py")
appearance = importlib.util.module_from_spec(spec)
spec.loader.exec_module(appearance)


class Store:
    def __init__(self):
        self.values = {("xsettings", "/Net/ThemeName"): appearance.GLib.Variant("s", "Adwaita")}

    def get(self, channel, key):
        return self.values.get((channel, key))

    def put(self, channel, key, value):
        if value is None:
            self.values.pop((channel, key), None)
        else:
            self.values[channel, key] = value

    def snapshot(self, channel):
        return {key: appearance.encode(value) for (name, key), value in self.values.items()
                if name == channel}


class AppearanceTests(unittest.TestCase):
    def test_official_writer_uses_typed_array_arguments(self):
        store = appearance.Xfconf.__new__(appearance.Xfconf)
        with patch.object(appearance.subprocess, "run") as run:
            store.put("panel", "/ids", appearance.GLib.Variant("av", [appearance.GLib.Variant("i", 1)]))
            self.assertIn("--force-array", run.call_args.args[0])
            self.assertEqual(run.call_args.args[0][-4:], ["--type", "int", "--set", "1"])
        rgba = appearance.GLib.Variant("ad", [0.1, 1.0])
        exposed = appearance.GLib.Variant("av", [appearance.GLib.Variant("d", n) for n in [0.1, 1.0]])
        self.assertEqual(appearance.encode(rgba), appearance.encode(exposed))

    def test_snapshot_preserves_native_types(self):
        store = appearance.Xfconf.__new__(appearance.Xfconf)
        reply = appearance.GLib.Variant("(a{sv})", ({"/size": appearance.GLib.Variant("u", 26)},))
        with patch.object(store, "call", return_value=reply):
            self.assertEqual(store.snapshot("panel"), {"/size": "uint32 26"})

    def test_once_only_and_guarded_restore(self):
        store = Store()
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "receipt.json"
            with patch.object(appearance.Path, "is_file", return_value=True), patch.dict(
                    os.environ, LOOM_XFCE_THEME_ROOT="/themes"):
                appearance.apply(store, path)
                self.assertEqual(path.stat().st_mode & 0o777, 0o600)
                self.assertEqual(json.loads(path.read_text())["state"], "applied")
                store.put("xsettings", "/Gtk/FontName", appearance.GLib.Variant("s", "User Font 14"))
                appearance.apply(store, path)
                appearance.restore(store, path)
            self.assertEqual(store.get("xsettings", "/Net/ThemeName").unpack(), "Adwaita")
            self.assertEqual(store.get("xsettings", "/Gtk/FontName").unpack(), "User Font 14")
            self.assertIsNone(store.get("xfce4-panel", "/plugins/plugin-19"))
            self.assertEqual(json.loads(path.read_text())["state"], "restore-held")

    def test_refuses_incomplete_apply(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "receipt.json"
            path.write_text('{"state":"applying"}')
            with self.assertRaisesRegex(RuntimeError, "Incomplete"):
                appearance.apply(Store(), path)

    def test_typed_roundtrip_and_scope(self):
        for channel, entries in appearance.settings(19).items():
            for key, (signature, value) in entries.items():
                variant = appearance.GLib.Variant(signature, value)
                self.assertEqual(appearance.encode(appearance.decode(appearance.encode(variant))),
                                 appearance.encode(variant))
                self.assertNotIn("cursor", key.lower())
                self.assertNotIn("mouse", key.lower())
                self.assertNotIn("last-image", key)
        panels = appearance.settings(19)["xfce4-panel"]
        self.assertNotIn("/panels/panel-2/plugin-ids", panels)


if __name__ == "__main__":
    unittest.main()
