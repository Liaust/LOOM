"""Focused upstream driver/session adapter checks; no replacement GUI engine."""
import importlib.util
import io
import json
import os
from contextlib import redirect_stdout
from pathlib import Path
import socket
from types import SimpleNamespace
import tempfile
import unittest
from unittest.mock import patch

root = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("session_launcher", root / "nix/files/agent-desktop/session_launcher.py")
launcher = importlib.util.module_from_spec(spec)
spec.loader.exec_module(launcher)


class SessionTests(unittest.TestCase):
    def session(self, **extra):
        return dict(Id="42", User="1002", Type="x11", Class="user",
                    Active="yes", LockedHint="no", Display=":2") | extra

    def select(self, **extra):
        rows = json.dumps([dict(session="42", uid=1002, **{"class": "user"}, seat="seat0")]).encode()
        text = "\n".join(key + "=" + value for key, value in self.session(**extra).items())
        with patch.object(launcher.os, "getuid", return_value=1002), patch.object(
                launcher.pwd, "getpwuid", return_value=SimpleNamespace(pw_name="agents")), patch.object(
                launcher.subprocess, "check_output", side_effect=[rows, text]) as run:
            result = launcher.selected_session()
            # loginctl accepts repeated properties, not systemctl's comma list.
            self.assertIn("--property=Active", run.call_args_list[-1].args[0])
            self.assertFalse(any("," in arg for arg in run.call_args_list[-1].args[0]))
            return result

    def test_only_actual_unlocked_x11_login(self):
        self.assertEqual(self.select()["Id"], "42")
        for values in [dict(LockedHint="yes"), dict(Type="wayland"), dict(User="1000"), dict(Active="no")]:
            with self.assertRaises(RuntimeError):
                self.select(**values)

    def test_environment_ownership_and_local_display(self):
        env = dict(DISPLAY=":2", XAUTHORITY="/run/user/1002/gdm/Xauthority",
                   XDG_RUNTIME_DIR="/run/user/1002", XDG_SESSION_TYPE="x11",
                   XDG_SESSION_ID="42", DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/1002/bus")
        with patch.object(launcher.os, "getuid", return_value=1002), patch.object(
                launcher.Path, "lstat", return_value=SimpleNamespace(st_uid=1002, st_mode=0o100600)):
            launcher.validate_environment(env, self.session())
            with patch.dict(launcher.os.environ, {"DISPLAY": ":20"}), self.assertRaises(RuntimeError):
                launcher.validate_environment(env, self.session())
            for values in [dict(DISPLAY="remote:2"), dict(XDG_SESSION_ID="41"),
                           dict(XDG_SESSION_TYPE="wayland"), dict(XAUTHORITY="relative")]:
                with self.assertRaises(RuntimeError):
                    launcher.validate_environment(env | values, self.session())

    def test_upstream_manifest_keeps_native_contract(self):
        manifest = dict(binary_version="0.38.0", subcommands=[dict(name="serve")],
                        mcp_invocation=dict(command="/upstream/bin/cua-driver", args=["mcp"]))
        output = io.StringIO()
        with patch.object(launcher.sys, "argv", ["launcher", "manifest"]), patch.dict(
                launcher.os.environ, dict(LOOM_CUA_BINARY="/upstream/bin/cua-driver",
                                          LOOM_CUA_LAUNCHER="/managed/launcher")), patch.object(
                launcher.subprocess, "check_output", return_value=json.dumps(manifest).encode()), redirect_stdout(output):
            launcher.main()
        result = json.loads(output.getvalue())
        self.assertEqual(result | {"mcp_invocation": manifest["mcp_invocation"]}, manifest)
        self.assertEqual(result["mcp_invocation"], dict(command="/managed/launcher", args=["mcp"]))

    def test_no_custom_engine_or_gnome_helper_is_installed(self):
        module = (root / "nix/modules/loom-agent-desktop.nix").read_text()
        self.assertIn("desktopManager.xfce.enable = true", module)
        self.assertIn("OnlyShowIn=XFCE", module)
        self.assertNotIn("systemd.user.services.loom-agent-desktop", module)
        self.assertNotIn("desktop.py", module)
        self.assertNotIn("X-GNOME-Autostart-Phase=", module)
        self.assertFalse((root / "nix/files/agent-desktop/desktop.py").exists())

    def test_driver_marker_comes_only_from_validated_login(self):
        env = dict(DISPLAY=":2", XAUTHORITY="/private/auth", XDG_SESSION_ID="42")
        binding = io.StringIO(json.dumps(dict(session="42", environment=env)))
        binding.fileno = lambda: 10
        with patch.object(launcher, "selected_session", return_value=self.session()), patch.object(
                launcher, "binding_path", return_value=Path("/private/binding")), patch.object(
                launcher.os, "open", return_value=10), patch.object(launcher.os, "fdopen", return_value=binding), patch.object(
                launcher.os, "fstat", return_value=SimpleNamespace(st_uid=1002, st_mode=0o100600, st_size=300)), patch.object(
                launcher.os, "getuid", return_value=1002), patch.object(launcher, "validate_environment") as validate, patch.dict(
                launcher.os.environ, {launcher.TARGET_SESSION: "stale", "WAYLAND_DISPLAY": "wayland-9"}):
            result = launcher.driver_environment()
        validate.assert_called_once_with(env, self.session())
        self.assertEqual(result[launcher.TARGET_SESSION], "42")
        self.assertNotIn("WAYLAND_DISPLAY", result)
        self.assertEqual(result["CUA_DRIVER_RS_TELEMETRY_ENABLED"], "0")


class BotDesktopTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory(prefix="loom-bot-", dir="/tmp")
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name) / "bot-desktop"
        self.root.mkdir(mode=0o700)
        for name in ("env", "Xauthority"):
            path = self.root / name
            path.touch(mode=0o600)
        self.socket = socket.socket(socket.AF_UNIX)
        self.addCleanup(self.socket.close)
        self.socket.bind(str(self.root / "rfb.sock"))
        os.chmod(self.root / "rfb.sock", 0o600)
        self.published = dict(DISPLAY=":20", XAUTHORITY=str(self.root / "Xauthority"),
                              DBUS_SESSION_BUS_ADDRESS="unix:path=/tmp/native-private-bus",
                              XDG_CONFIG_HOME=str(self.root / "xdg"),
                              XDG_CACHE_HOME=str(self.root / "xdg/.cache"),
                              XDG_DATA_HOME=str(self.root / "xdg/.local-share"))
        self.runtime = SimpleNamespace(state_dir=lambda: self.root,
                                       published_env=lambda: self.published,
                                       rfb_socket_path=lambda: self.root / "rfb.sock")

    def env(self):
        with patch.object(launcher.pwd, "getpwuid", return_value=SimpleNamespace(pw_name="agents")), patch.object(
                launcher, "selected_session", side_effect=AssertionError("must not select physical seat")):
            return launcher.bot_driver_environment(self.runtime)

    def test_native_bot_has_no_logind_or_physical_lock_dependency(self):
        with patch.dict(os.environ, {"DISPLAY": ":20", "WAYLAND_DISPLAY": "wayland-0",
                                     "XDG_SESSION_ID": "8208", launcher.TARGET_SESSION: "8208",
                                     "XDG_CONFIG_HOME": "/home/agents/.config"}):
            result = self.env()
        for key in ("WAYLAND_DISPLAY", "XDG_SESSION_ID", launcher.TARGET_SESSION):
            self.assertNotIn(key, result)
        self.assertEqual(result["DISPLAY"], ":20")
        self.assertEqual(result["XDG_CONFIG_HOME"], self.published["XDG_CONFIG_HOME"])
        self.assertEqual(result["CUA_DRIVER_RS_TELEMETRY_ENABLED"], "0")

    def test_down_bot_never_falls_back_to_physical(self):
        self.published = {}
        with self.assertRaisesRegex(RuntimeError, "physical desktop is not a fallback"):
            self.env()

    def test_mismatched_display_authority_bus_and_profile_are_refused(self):
        with patch.dict(os.environ, {"DISPLAY": ":0"}), self.assertRaises(RuntimeError):
            self.env()
        original = self.published.copy()
        for values in [dict(DISPLAY=":0"), dict(XAUTHORITY="/other/Xauthority"),
                       dict(DBUS_SESSION_BUS_ADDRESS=f"unix:path=/run/user/{os.getuid()}/bus"),
                       dict(DBUS_SESSION_BUS_ADDRESS="tcp:host=localhost,port=1234"),
                       dict(XDG_CONFIG_HOME="/home/agents/.config")]:
            self.published = original | values
            with self.assertRaises(RuntimeError):
                self.env()

    def test_private_runtime_permissions_are_required(self):
        for path in (self.root, self.root / "env", self.root / "Xauthority", self.root / "rfb.sock"):
            old = path.stat().st_mode & 0o777
            path.chmod(0o755)
            with self.assertRaises(RuntimeError):
                self.env()
            path.chmod(old)


if __name__ == "__main__":
    unittest.main()
