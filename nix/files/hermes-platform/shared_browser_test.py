import importlib.util
from pathlib import Path
import sys
import tempfile
import types
import unittest
from unittest.mock import MagicMock, patch


def load_helper():
    package = types.ModuleType("tools.bot_desktop")
    package.browser, package.runtime = MagicMock(), MagicMock()
    spec = importlib.util.spec_from_file_location(
        "shared_browser_under_test", Path(__file__).with_name("shared_browser.py"))
    module = importlib.util.module_from_spec(spec)
    with patch.dict(sys.modules, {"tools": types.ModuleType("tools"), "tools.bot_desktop": package}):
        spec.loader.exec_module(module)
    return module


class SharedBrowserTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.helper = load_helper()
        self.browser, self.runtime = self.helper.browser, self.helper.runtime
        self.root = Path(self.tmp.name)
        self.screen = {"DISPLAY": ":20", "XAUTHORITY": str(self.root / "Xauthority")}
        self.runtime.state_dir.return_value = self.root
        self.runtime.published_env.return_value = self.screen
        self.runtime.desktop_env.side_effect = lambda env: {**env, **self.screen}
        self.browser.profile_dir.return_value = self.root / "browser-profile"
        self.browser.dock_launch.return_value = ("/native/chromium", str(self.root / "browser-profile"))
        self.browser.dock_argv.return_value = ["/native/chromium", "--user-data-dir=private", "--remote-debugging-port=0"]

    def start(self, **kwargs):
        return self.helper.ensure_started(exclude_session="owned-session", env={"PATH": "/native/bin"}, **kwargs)

    def test_existing_shared_browser_attaches_without_spawning(self):
        self.browser.running_instance_cdp_port.return_value = 49100
        with patch.object(self.helper.subprocess, "Popen") as spawn:
            self.assertEqual(self.start(), 49100)
            spawn.assert_not_called()

    def test_existing_daemon_owned_browser_is_not_reconfigured(self):
        self.browser.running_instance_cdp_port.side_effect = [None, 49100]
        with patch.object(self.helper.subprocess, "Popen") as spawn:
            self.assertIsNone(self.start())
            spawn.assert_not_called()

    def test_cold_start_is_desktop_owned_and_uses_selected_private_environment(self):
        self.browser.running_instance_cdp_port.side_effect = [None, None, 49100]
        with patch.object(self.helper.subprocess, "Popen") as spawn:
            self.assertEqual(self.start(), 49100)
            self.assertEqual(spawn.call_args.args[0], self.browser.dock_argv.return_value)
            self.assertEqual(spawn.call_args.kwargs["env"], {"PATH": "/native/bin", **self.screen})
            self.assertTrue(spawn.call_args.kwargs["start_new_session"])
        lock = self.root / "browser-launch.lock"
        self.assertEqual(lock.stat().st_mode & 0o777, 0o600)

    def test_missing_desktop_does_not_fall_back(self):
        self.runtime.published_env.return_value = {}
        with patch.object(self.helper.subprocess, "Popen") as spawn:
            with self.assertRaisesRegex(RuntimeError, "no physical browser fallback"):
                self.start()
            spawn.assert_not_called()

    def test_changed_screen_refuses_before_spawn(self):
        self.browser.running_instance_cdp_port.return_value = None
        self.runtime.desktop_env.side_effect = lambda env: {**env, "DISPLAY": ":0"}
        with patch.object(self.helper.subprocess, "Popen") as spawn:
            with self.assertRaisesRegex(RuntimeError, "changed before browser launch"):
                self.start()
            spawn.assert_not_called()

    def test_timeout_is_bounded_and_only_stops_its_own_child(self):
        self.browser.running_instance_cdp_port.return_value = None
        child = MagicMock()
        child.poll.return_value = None
        with patch.object(self.helper.subprocess, "Popen", return_value=child):
            with self.assertRaisesRegex(RuntimeError, "did not publish"):
                self.start(timeout=0)
        child.terminate.assert_called_once()
        child.wait.assert_called_once_with(timeout=3)

    def test_bad_lock_permissions_refuse_without_spawn(self):
        (self.root / "browser-launch.lock").touch(mode=0o644)
        with patch.object(self.helper.subprocess, "Popen") as spawn:
            with self.assertRaisesRegex(RuntimeError, "Invalid private"):
                self.start()
            spawn.assert_not_called()


if __name__ == "__main__":
    unittest.main()
