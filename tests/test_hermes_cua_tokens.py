"""Run against the packaged Main Hermes environment, without a desktop."""
from types import SimpleNamespace
import unittest
from unittest.mock import patch

try:
    from tools.computer_use.cua_backend import CuaDriverBackend
except ModuleNotFoundError:
    CuaDriverBackend = None


@unittest.skipUnless(CuaDriverBackend, "Requires the packaged Main Hermes environment")
class ElementTokens(unittest.TestCase):
    def test_native_menu_uses_exact_captured_target(self):
        calls = []
        backend = SimpleNamespace(
            _target_args=lambda *args, **kwargs: (None, dict(pid=123, window_id=456)),
            _session=SimpleNamespace(_has_tool=lambda name: name == "invoke_menu"),
            _action=lambda name, args: calls.append((name, args)))
        CuaDriverBackend.invoke_menu(backend, ["File", "Quit"])
        self.assertEqual(calls, [("invoke_menu", dict(pid=123, window_id=456, path=["File", "Quit"]))])
        for invalid in (None, "File/Quit", [], [""], ["x"] * 17):
            result = CuaDriverBackend.invoke_menu(backend, invalid)
            self.assertFalse(result.ok)
        self.assertEqual(len(calls), 1)

    def test_menu_shares_native_input_approval_and_target_guards(self):
        from tools.computer_use.schema import _PROPERTIES
        from tools.computer_use.tool import _ACTIONS
        self.assertIn("invoke_menu", _PROPERTIES["action"]["enum"])
        self.assertTrue(_ACTIONS["invoke_menu"].input)
        self.assertTrue(_ACTIONS["invoke_menu"].destructive)

    def test_capture_requests_tokens_without_duplicate_markdown(self):
        for supported in (True, False):
            backend = SimpleNamespace(_active_pid=123, _active_window_id=456, _session_id="check",
                                      _session=SimpleNamespace(supports_input_property=lambda *args: supported))
            with patch("tools.computer_use.cua_backend._cua_configured_ax_max_elements", return_value=25):
                args = CuaDriverBackend._gws_args(backend)
            self.assertEqual(args.get("tree_format"), "elements" if supported else None)
            self.assertEqual(args["max_elements"], 25)

    def invoke(self, *, properties, tokens):
        calls = []
        def call(name, args):
            calls.append((name, dict(args)))
            return dict(data={}, structuredContent={}, isError=False)
        session = SimpleNamespace(supports_input_property=lambda name, key: key in properties,
                                  supports_capability=lambda *args, **kwargs: False,
                                  call_tool=call)
        backend = SimpleNamespace(_session=session, _snapshot_tokens=tokens, _session_id="check")
        result = CuaDriverBackend._action(backend, "click", dict(pid=123, window_id=456, element_index=3))
        return calls, result

    def test_strict_schema_gets_only_current_token(self):
        calls, _ = self.invoke(properties={"element_token"}, tokens={3: "snapshot:3"})
        self.assertEqual(calls, [("click", dict(pid=123, window_id=456,
                                               element_token="snapshot:3", session="check"))])

    def test_missing_token_refuses_without_input(self):
        calls, result = self.invoke(properties={"element_token"}, tokens={})
        self.assertEqual(calls, [])
        self.assertFalse(result.ok)

    def test_legacy_schema_keeps_supported_index(self):
        calls, _ = self.invoke(properties={"element_index"}, tokens={})
        self.assertEqual(calls[0][1]["element_index"], 3)
        self.assertNotIn("element_token", calls[0][1])


if __name__ == "__main__":
    unittest.main()
