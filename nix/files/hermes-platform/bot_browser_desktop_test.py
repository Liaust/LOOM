import importlib.util
import os
from pathlib import Path
import shlex
import subprocess
import sys
import tempfile
import types
import unittest
from unittest.mock import MagicMock, patch


class BotBrowserDesktopTests(unittest.TestCase):
    def setUp(self):
        package = types.ModuleType("tools.bot_desktop")
        package.runtime = MagicMock()
        package.runtime.state_dir.return_value = Path("/persistent/profile/bot-desktop")
        constants = types.ModuleType("hermes_constants")
        constants.SOCKET_TMPDIR_MAX_LEN = 50
        constants.socket_safe_tmpdir = MagicMock(return_value="/tmp")
        modules = {"tools": types.ModuleType("tools"), "tools.bot_desktop": package,
                   "hermes_constants": constants}
        self.addCleanup(patch.stopall)
        patch.dict(sys.modules, modules).start()
        spec = importlib.util.spec_from_file_location("browser_under_test", os.environ["HERMES_BROWSER_SOURCE"])
        self.browser = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.browser)
        self.constants = constants

    def test_deep_profile_shrinks_only_browser_temp(self):
        home = "/srv/loom/agents/mina/.hermes/profiles/" + "p" * 40
        env = {"TMPDIR": home + "/cache/scratch", "HERMES_HOME": home,
               "HERMES_SCRATCH_DIR": home + "/cache/scratch"}
        with patch.object(self.browser, "executable", return_value="/native/chromium"):
            self.browser.env_for_agent(env)
        self.assertEqual([env[k] for k in ("TMPDIR", "TMP", "TEMP")], ["/tmp"] * 3)
        self.assertEqual(env["HERMES_HOME"], home)
        self.assertEqual(env["HERMES_SCRATCH_DIR"], home + "/cache/scratch")
        self.assertEqual(env["AGENT_BROWSER_PROFILE"], "/persistent/profile/bot-desktop/browser-profile")

    def test_short_override_is_unchanged(self):
        self.assertEqual(self.browser.socket_temp_env({"TMPDIR": "/private/short"}), {})
        self.constants.socket_safe_tmpdir.assert_not_called()

    def test_socket_budget_is_bytes_not_characters(self):
        path = "/" + "\u00e9" * 26
        self.constants.socket_safe_tmpdir.return_value = path
        self.assertEqual(self.browser.socket_temp_env({"TMPDIR": path})["TMPDIR"], "/tmp")

    def test_dock_preserves_profile_and_uses_short_temp(self):
        argv = ["/native/chromium", "--user-data-dir=/persistent/housing profile", "--remote-debugging-port=0"]
        with patch.dict(os.environ, {"TMPDIR": "/deep/" + "p" * 90}), \
                patch.object(self.browser, "dock_argv", return_value=argv):
            line = self.browser.dock_exec_line(argv[0], "/persistent/housing profile")
        tokens = shlex.split(line.removeprefix("Exec="))
        self.assertEqual(tokens[-3:], argv)
        self.assertEqual(tokens[1:4], ["TMPDIR=/tmp", "TMP=/tmp", "TEMP=/tmp"])

    def test_application_menu_reuses_dock_identity_and_preserves_other_entries(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            data = root / "data"
            apps = data / "applications"
            apps.mkdir(parents=True)
            user_entry = apps / "user.desktop"
            user_entry.write_text("preserve-user-launcher")
            line = 'Exec="/native/chromium" "TMPDIR=/tmp" "--user-data-dir=/persistent/housing profile"'
            env = {**os.environ, "HOME": str(root), "HERMES_BD_PROFILE": "housing",
                   "HERMES_BD_DISPLAY_NUM": "88", "HERMES_BD_SOCKET": str(root / "rfb.sock"),
                   "HERMES_BD_XAUTH": str(root / "Xauthority"), "HERMES_BD_ENV_FILE": str(root / "env"),
                   "HERMES_BD_CONFIG_HOME": str(root / "config"), "HERMES_BD_DATA_HOME": str(data),
                   "HERMES_BD_BROWSER_EXEC": "/native/chromium", "HERMES_BD_BROWSER_EXEC_LINE": line,
                   "HERMES_BD_SEED_ONLY": "1"}
            subprocess.run(["bash", os.environ["HERMES_DESKTOP_LAUNCHER_SOURCE"]],
                           env=env, capture_output=True, text=True, check=True, timeout=10)
            entry = (apps / "hermes-bot-browser.desktop").read_text()
            self.assertIn("Name=Chromium (Bot Browser)", entry)
            self.assertIn(line, entry)
            self.assertEqual(user_entry.read_text(), "preserve-user-launcher")


if __name__ == "__main__":
    unittest.main()
