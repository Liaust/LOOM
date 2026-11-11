"""Focused checks against the packaged native routing function; run on Main."""
import unittest
from unittest.mock import patch

from tools import browser_tool as bt, browser_tool_session as session
from tools.bot_desktop import browser, shared_browser


class SharedBrowserRoutingTests(unittest.TestCase):
    def route(self, *, shared=True, start=True, headed=True, lightpanda=False, camofox=False):
        info = {"session_name": "owned", "features": {"local": True, "lightpanda": lightpanda}}
        with (patch.object(session, "_shares_bot_desktop_browser", return_value=shared),
              patch.object(session._cloud, "_is_headed_mode", return_value=headed),
              patch.object(bt, "_is_camofox_mode", return_value=camofox),
              patch.object(bt, "_build_browser_env", return_value={"SAFE": "yes"}),
              patch.object(browser, "running_instance_cdp_port", return_value=49101),
              patch.object(session, "_ensure_screen_for_headed_chromium") as screen,
              patch.object(shared_browser, "ensure_started", return_value=49102) as ensure):
            result = session._bot_desktop_attach_port(info, start=start)
            return result, screen.called, ensure.called

    def test_cleanup_never_starts_a_screen_or_browser(self):
        self.assertEqual(self.route(start=False), (49101, False, False))

    def test_headless_does_not_start_a_screen_or_browser(self):
        self.assertEqual(self.route(headed=False), (49101, False, False))

    def test_lightpanda_is_unchanged(self):
        self.assertEqual(self.route(lightpanda=True), (49101, False, False))

    def test_camofox_is_unchanged(self):
        self.assertEqual(self.route(camofox=True), (49101, False, False))

    def test_non_shared_route_does_not_start_a_browser(self):
        self.assertEqual(self.route(shared=False), (None, True, False))

    def test_running_shared_screen_starts_desktop_owned_browser(self):
        self.assertEqual(self.route(), (49102, True, True))

    def test_cold_screen_is_started_before_shared_target_is_checked(self):
        ready = []
        info = {"session_name": "cold", "features": {"local": True}}
        with (patch.object(session._cloud, "_is_headed_mode", return_value=True),
              patch.object(bt, "_is_camofox_mode", return_value=False),
              patch.object(bt, "_build_browser_env", return_value={"SAFE": "yes"}),
              patch.object(session, "_ensure_screen_for_headed_chromium", side_effect=lambda: ready.append(True)),
              patch.object(session, "_shares_bot_desktop_browser", side_effect=lambda _: bool(ready)),
              patch.object(shared_browser, "ensure_started", return_value=49102) as ensure):
            self.assertEqual(session._bot_desktop_attach_port(info, start=True), 49102)
            ensure.assert_called_once()


if __name__ == "__main__":
    unittest.main()
