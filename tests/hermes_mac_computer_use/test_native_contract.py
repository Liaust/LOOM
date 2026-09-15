"""Pinned Hermes baseline plus required patched remote contracts; synthetic peers only.

Run with the pinned Hermes Python environment and HERMES_NATIVE_SOURCE. Missing/wrong dependencies
are errors, never skips. Expected failures describe upstream/remote-absent
behavior only. Every corresponding remote-mode assertion is a required pass. The --fixture-driver process
below is a tiny synthetic MCP peer, not a Cua implementation.
"""
from __future__ import annotations

import asyncio
import atexit
import base64
import copy
import hashlib
import importlib.metadata
import json
import io
import logging
import os
from pathlib import Path
import struct
import shutil
import subprocess
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest import mock

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[1]
FIXTURE_PATH = HERE / "fixtures/native_driver_manifest.json"
FIXTURE = json.loads(FIXTURE_PATH.read_text())
FROZEN_FIXTURES = {
    "native_driver_manifest.json": "b19f5c9a286d880a44bf03fe4cd1cd39035e58114ea146d6b46e3594e4f9c292",
    "routing_cases.json": "d1b3c39991cebfd598b5795ccf7f3c0d7bdf5fd4224226b24d09b9c4382d7246",
}
for filename, expected_digest in FROZEN_FIXTURES.items():
    if hashlib.sha256((HERE / "fixtures" / filename).read_bytes()).hexdigest() != expected_digest:
        raise RuntimeError("Frozen fixture mismatch: " + filename)


def fixture_driver(argv):
    """Only JSON on stdout; all effects are writes to the test-owned audit file."""
    launcher, audit, *args = argv
    if args == ["manifest"]:
        manifest = copy.deepcopy(FIXTURE["manifest"])
        manifest["mcp_invocation"]["command"] = launcher
        print(json.dumps(manifest), flush=True)
        return
    if args == ["--help"]:
        print("Synthetic peer: mcp manifest --no-overlay")
        return
    if not args or args[0] != "mcp":
        raise SystemExit("fixture refuses non-MCP invocation")
    for line in sys.stdin:
        request = json.loads(line)
        if "id" not in request:
            continue
        method, params = request["method"], request.get("params", {})
        if method == "initialize":
            result = {"protocolVersion": "2025-06-18", "capabilities": {"tools": {}},
                      "serverInfo": {"name": "loom-synthetic-cua", "version": "0.26.1"}}
        elif method == "tools/list":
            result = FIXTURE["tools_list"]
        elif method == "tools/call":
            with open(audit, "a") as stream:
                stream.write(json.dumps(params) + "\n")
            name = params["name"]
            if name == "list_windows":
                result = FIXTURE["windows_result"]
            elif name == "get_window_state":
                result = FIXTURE["capture_result"]
            elif name in {"click", "type_text", "start_session", "end_session",
                           "set_config", "set_agent_cursor_enabled"}:
                result = FIXTURE["action_result"]
            else:
                result = {"content": [{"type": "text", "text": "unsupported fixture tool"}],
                          "isError": True}
        else:
            print(json.dumps({"jsonrpc": "2.0", "id": request["id"],
                              "error": {"code": -32601, "message": "unknown fixture method"}}), flush=True)
            continue
        print(json.dumps({"jsonrpc": "2.0", "id": request["id"], "result": result}), flush=True)


if __name__ == "__main__" and sys.argv[1:2] == ["--fixture-driver"]:
    fixture_driver(sys.argv[2:])
    raise SystemExit(0)

LOCK = json.loads((REPO / "nix/locks/mac-computer-use.json").read_text())
if not os.environ.get("HERMES_NATIVE_SOURCE"):
    raise RuntimeError("Set HERMES_NATIVE_SOURCE to the pinned Hermes source before running native tests")
SOURCE = Path(os.environ["HERMES_NATIVE_SOURCE"])
REMOTE_MODE = os.environ.get("LOOM_NATIVE_TEST_MODE") == "remote"
IMMUTABLE_SOURCE = SOURCE
for relative, digest in LOCK["hermes"]["file_sha256"].items():
    if hashlib.sha256((SOURCE / relative).read_bytes()).hexdigest() != digest:
        raise RuntimeError(f"Pinned source mismatch: {relative}")
for name, version in LOCK["hermes"]["dependencies"].items():
    try:
        observed = importlib.metadata.version(name)
    except importlib.metadata.PackageNotFoundError as exc:
        raise RuntimeError(f"Required {name} missing; use the pinned Hermes Python environment") from exc
    if observed != version:
        raise RuntimeError(f"Pinned environment mismatch: {name}={observed}, expected {version}")

if os.environ.get("LOOM_NATIVE_TEST_MODE") in {"remote", "patched-local"}:
    PATCHED_SOURCE = tempfile.TemporaryDirectory(prefix="loom-patched-hermes-")
    atexit.register(PATCHED_SOURCE.cleanup)
    patched = Path(PATCHED_SOURCE.name)
    # Keep immutable dependencies linked, but all patched owners are real copies.
    for child in SOURCE.iterdir():
        if child.name != "tools":
            (patched / child.name).symlink_to(child, target_is_directory=child.is_dir())
    (patched / "tools").mkdir()
    for child in (SOURCE / "tools").iterdir():
        if child.name != "computer_use":
            (patched / "tools" / child.name).symlink_to(child, target_is_directory=child.is_dir())
    shutil.copytree(SOURCE / "tools/computer_use", patched / "tools/computer_use", ignore=shutil.ignore_patterns("__pycache__", "*.pyc"))
    (patched / "tools/computer_use").chmod(0o700)
    for f in (patched / "tools/computer_use").rglob("*.py"):
        f.chmod(0o600)
    patch = REPO / "nix/patches/hermes-mac-computer-use.patch"
    headers = [line.split(" ", 1)[1][2:] for line in patch.read_text().splitlines() if line.startswith("+++ ")]
    old_headers = [line.split(" ", 1)[1][2:] for line in patch.read_text().splitlines() if line.startswith("--- ")]
    if old_headers != headers:
        raise RuntimeError("Patch source/target mismatch")
    expected = {"tools/computer_use/cua_backend.py", "tools/computer_use/tool.py", "tools/computer_use/backend.py"}
    if set(headers) != expected or len(headers) != 3:
        raise RuntimeError("Patch owner boundary mismatch")
    subprocess.run(["/usr/bin/patch", "-p1", "--batch", "--fuzz=0", "-i", str(patch)], cwd=patched, check=True, capture_output=True)
    for name in expected:
        if (patched / name).read_bytes() == (IMMUTABLE_SOURCE / name).read_bytes():
            raise RuntimeError("Patch did not change expected source: " + name)
    SOURCE = patched.resolve()

# Imports and cache writes are bound to a disposable profile before loading Hermes.
PROFILE = tempfile.TemporaryDirectory(prefix="loom-native-contract-")
_previous_profile = os.environ.get("HERMES_HOME")
os.environ["HERMES_HOME"] = PROFILE.name
os.environ["HERMES_DISABLE_LAZY_INSTALLS"] = "1"
sys.path.insert(0, str(SOURCE))
from tools.computer_use import cua_backend as cua, tool as native
from mcp.types import CallToolResult, ListToolsResult


def _cleanup_profile():
    native._shutdown_backend_atexit()
    PROFILE.cleanup()
    if _previous_profile is None:
        os.environ.pop("HERMES_HOME", None)
    else:
        os.environ["HERMES_HOME"] = _previous_profile


# Avoid restoring the profile until tests and Hermes atexit cleanup have finished.
atexit.register(_cleanup_profile)


def unpack(result):
    return cua._extract_tool_result(CallToolResult.model_validate(result))


class NativeCase(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="loom-native-case-")
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.audit = self.root / "calls.jsonl"
        self.launcher = self.root / "fixture-launcher"
        self.launcher.write_text(
            f"#!{sys.executable}\nimport runpy,sys\n"
            f"sys.argv=[{str(Path(__file__).resolve())!r}, '--fixture-driver', "
            f"{str(self.launcher)!r}, {str(self.audit)!r}, *sys.argv[1:]]\n"
            f"runpy.run_path({str(Path(__file__).resolve())!r}, run_name='__main__')\n")
        self.launcher.chmod(0o700)
        if REMOTE_MODE:
            from test_mac_endpoint import Harness
            remote_root = self.root / "remote"
            remote_root.mkdir()
            self.harness = Harness(remote_root)
            self.launcher = self.harness.launcher
            self.audit = self.harness.audit
            self.enterContext(mock.patch.dict(os.environ, self.harness.env()))
        self.enterContext(mock.patch.dict(os.environ, {
            "HERMES_CUA_DRIVER_CMD": str(self.launcher),
            "HERMES_HOME": PROFILE.name, "HERMES_DISABLE_LAZY_INSTALLS": "1",
        }))
        # Patch each owner's module reference, never global sys.platform.
        self.enterContext(mock.patch.object(cua, "sys", SimpleNamespace(platform="linux")))
        self.enterContext(mock.patch.object(native, "sys", SimpleNamespace(platform="linux")))
        self.enterContext(mock.patch.object(cua, "_computer_use_cfg", return_value={}))
        self.enterContext(mock.patch.object(cua, "_maybe_nudge_update"))
        self.enterContext(mock.patch.object(cua.logger, "disabled", True))
        self.enterContext(mock.patch.object(native.logger, "disabled", True))
        self.enterContext(mock.patch.object(native, "_should_route_through_aux_vision", return_value=False))
        popen = subprocess.Popen

        def only_fixture(command, *args, **kwargs):
            if not isinstance(command, (tuple, list)) or command[0] != str(self.launcher):
                raise AssertionError(f"Non-fixture process forbidden: {command!r}")
            return popen(command, *args, **kwargs)

        self.enterContext(mock.patch.object(subprocess, "Popen", side_effect=only_fixture))

    def calls(self):
        return [json.loads(x) for x in self.audit.read_text().splitlines()] if self.audit.exists() else []

    def backend(self):
        backend = cua.CuaDriverBackend()
        self.addCleanup(backend.stop)
        return backend

    def manifest_result(self, value=None):
        return SimpleNamespace(returncode=0, stdout=json.dumps(value or FIXTURE["manifest"]), stderr="")


class NativeBaseline(NativeCase):
    def test_pinned_schema_and_source_identity(self):
        self.assertEqual(Path(cua.__file__).resolve(), SOURCE / "tools/computer_use/cua_backend.py")
        schema = native.get_computer_use_schema()
        self.assertEqual(schema["name"], "computer_use")
        self.assertEqual(schema["parameters"]["properties"]["action"]["enum"], FIXTURE["native_actions"])
        self.assertNotIn("browser", schema["parameters"]["properties"]["action"]["enum"])
        self.assertNotIn("page", schema["parameters"]["properties"]["action"]["enum"])

    def test_offline_discovery_uses_executable_without_display_or_probe(self):
        with mock.patch.dict(os.environ, {}, clear=True), \
             mock.patch.dict(os.environ, {"HERMES_CUA_DRIVER_CMD": str(self.launcher)}), \
             mock.patch.object(cua.subprocess, "run", side_effect=AssertionError("probe at discovery")):
            self.assertTrue(native.check_computer_use_requirements())
        self.assertEqual(self.calls(), [])

    def test_invalid_override_never_searches_other_driver(self):
        with mock.patch.dict(os.environ, {"HERMES_CUA_DRIVER_CMD": str(self.root / "missing")}):
            self.assertIsNone(cua.resolve_cua_driver_cmd())
            self.assertFalse(native.check_computer_use_requirements())

    def test_runtime_manifest_contract_and_negative_versions(self):
        for version, expected in [("0.26.1", True), ("0.19.9", False), ("garbage", False)]:
            manifest = copy.deepcopy(FIXTURE["manifest"])
            manifest["binary_version"] = version
            with self.subTest(version=version), mock.patch.object(cua.subprocess, "run", return_value=self.manifest_result(manifest)):
                self.assertEqual(cua.cua_driver_runtime_contract_status(str(self.launcher))["ready"], expected)
        manifest["binary_version"] = "0.26.1"
        manifest["subcommands"] = []
        with mock.patch.object(cua.subprocess, "run", return_value=self.manifest_result(manifest)):
            self.assertFalse(cua.cua_driver_runtime_contract_status(str(self.launcher))["ready"])

    def test_broken_managed_override_does_not_auto_install(self):
        contract = {"ready": False, "binary": str(self.launcher), "reason": "offline"}
        self.assertIs(cua._maybe_repair_runtime_contract(contract), contract)
        self.assertEqual(self.calls(), [])

    def test_real_mcp_image_model_and_native_multimodal_envelope(self):
        out = unpack(FIXTURE["capture_result"])
        self.assertEqual(out["image_mime_types"], ["image/png"])
        raw = base64.b64decode(out["images"][0], validate=True)
        self.assertEqual(raw[:8], b"\x89PNG\r\n\x1a\n")
        self.assertEqual(struct.unpack(">II", raw[16:24]), (64, 64))
        cap = cua.CaptureResult(mode="som", width=64, height=64, png_b64=out["images"][0],
                                image_mime_type="image/png", png_bytes_len=len(raw))
        response = native._capture_response(cap)
        self.assertTrue(response["_multimodal"])
        self.assertEqual(response["content"][1]["type"], "image_url")
        self.assertEqual(response["content"][1]["image_url"]["url"], "data:image/png;base64," + out["images"][0])
        path = Path(response["meta"]["screenshot_path"])
        self.assertTrue(path.is_relative_to(Path(PROFILE.name)))
        self.assertEqual(path.read_bytes(), raw)

    def test_mcp2_error_and_structured_fields_are_not_lost(self):
        frame = {"content": [{"type": "text", "text": "permission denied"}], "isError": True,
                 "structuredContent": {"code": "mac_permission_denied", "target_node": "macbook",
                                       "phase": "admission", "delivery": "not_sent"}}
        out = unpack(frame)
        self.assertTrue(out["isError"])
        backend = self.backend()
        with mock.patch.object(backend._session, "call_tool", return_value=out):
            response = json.loads(native._text_response(backend._action("click", {})))
        self.assertFalse(response["ok"])
        self.assertEqual(response["code"], "mac_permission_denied")
        self.assertEqual(response["meta"]["delivery"], "not_sent")


    def exercise_safari_stdio(self):
        backend = self.backend()
        # Ensure remains managed and cannot install anything; mcp already imported.
        with mock.patch("tools.lazy_deps.ensure"):
            backend.start()
        with mock.patch.object(native, "_get_backend", return_value=backend):
            capture = native.handle_computer_use({"action": "capture", "app": "Safari", "pid": 4242,
                                                  "window_id": 701, "mode": "som"}, session_id="fixture-tui")
            self.assertTrue(capture["_multimodal"])
            with mock.patch.object(native, "_approval_callback", return_value="approve_once"):
                action = json.loads(native.handle_computer_use({"action": "click", "element": 1}, session_id="fixture-tui"))
        self.assertTrue(action["ok"])
        self.assertEqual(backend._last_target, {"pid": 4242, "window_id": 701})
        clicks = [x for x in self.calls() if x["name"] == "click"]
        self.assertEqual(len(clicks), 1)
        self.assertEqual(clicks[0]["arguments"]["element_index"], 1)
        self.assertEqual(clicks[0]["arguments"]["window_id"], 701)
        self.assertFalse(any(x["name"] in {"page", "browser", "serve", "stop"} for x in self.calls()))
        return clicks

    def test_safari_capture_and_action_over_real_stdio(self):
        self.exercise_safari_stdio()

    def test_fixture_only_discovery_model_prototype_preserves_token(self):
        # Compatibility prototype only. The actual pinned native path remains
        # unchanged and its matching token assertion below stays XFAIL.
        from mcp import ClientSession
        from mcp.types import Tool, ListToolsRequest

        class DriverTool(Tool):
            capabilities: list[str] = []

        class DriverListing(ListToolsResult):
            tools: list[DriverTool]
            capability_version: str = ""

        class FixturePreservingClient(ClientSession):
            async def list_tools(self, *, params=None):
                result = await self.send_request(ListToolsRequest(params=params), DriverListing)
                complete = (params is None or params.cursor is None) and result.next_cursor is None
                return self._absorb_tool_listing(result, complete=complete)

        with mock.patch("mcp.ClientSession", FixturePreservingClient):
            clicks = self.exercise_safari_stdio()
        self.assertEqual(clicks[0]["arguments"].get("element_token"), "sfixture:1")


    def test_standard_mcp_schema_properties_survive(self):
        backend = self.backend()
        session = SimpleNamespace(list_tools=mock.AsyncMock(return_value=ListToolsResult.model_validate(FIXTURE["tools_list"])))
        asyncio.run(backend._session._populate_capabilities(session))
        self.assertTrue(backend._session.supports_input_property("click", "element_token"))
        self.assertTrue(backend._session.supports_input_property("click", "delivery_mode"))

    def test_native_denial_precedes_backend_start(self):
        with mock.patch.object(native, "_approval_callback", return_value="deny"), \
             mock.patch.object(native, "_get_backend") as get_backend:
            response = json.loads(native.handle_computer_use({"action": "click", "element": 1}, session_id="denied"))
        self.assertEqual(response["error"], "denied by user")
        get_backend.assert_not_called()
        self.assertEqual(self.calls(), [])

    def test_mac_blocked_shortcut_works_on_linux_caller(self):
        with mock.patch.object(native, "_get_backend") as get_backend:
            result = json.loads(native.handle_computer_use({"action": "key", "keys": "cmd+ctrl+q"}))
        self.assertIn("blocked key combo", result["error"])
        get_backend.assert_not_called()

    def test_wrong_app_cannot_reuse_sticky_safari_target(self):
        backend = self.backend()
        backend._last_app = "Safari"
        with mock.patch.object(backend, "click") as click:
            result = json.loads(native._dispatch(backend, "click", {"app": "Finder", "element": 1}))
        self.assertIn("error", result)
        click.assert_not_called()

    def test_session_ids_and_owned_cleanup_are_independent(self):
        one, two = self.backend(), self.backend()
        self.assertNotEqual(one._session_id, two._session_id)
        self.assertIsNone(one._embedded_daemon)
        self.assertIsNone(two._embedded_daemon)
        with mock.patch.object(one._session, "stop") as stop_one, mock.patch.object(two._session, "stop") as stop_two:
            one.stop()
            stop_one.assert_called_once()
            stop_two.assert_not_called()

    def test_routing_fixture_collection_and_native_safari_shape(self):
        cases = json.loads((HERE / "fixtures/routing_cases.json").read_text())["cases"]
        self.assertEqual(len({x["id"] for x in cases}), 10)
        safari = next(x for x in cases if x["id"] == "R03")
        self.assertEqual(safari["expected_route"], "native_computer_use")
        self.assertIn("main_browser", safari["forbidden"])
        # Routing is a frozen scenario set; no model/agent route choice was run.
        backend = mock.Mock()
        backend.capture.return_value = cua.CaptureResult(mode="ax", width=64, height=64)
        native._dispatch(backend, "capture", {"app": "Safari", "pid": 4242, "window_id": 701, "mode": "ax"})
        backend.capture.assert_called_once_with(mode="ax", app="Safari", pid=4242, window_id=701)


def remote_requirement(test):
    return test if REMOTE_MODE else unittest.expectedFailure(test)


class NativeExpectedRemoteFailures(NativeCase):
    @remote_requirement
    def test_expected_mcp2_preserves_cua_discovery_extensions(self):
        backend = self.backend()
        if REMOTE_MODE:
            backend.start()
        else:
            session = SimpleNamespace(list_tools=mock.AsyncMock(return_value=ListToolsResult.model_validate(FIXTURE["tools_list"])))
            asyncio.run(backend._session._populate_capabilities(session))
        self.assertEqual(backend._session.capability_version, "1")
        self.assertTrue(backend._session.supports_input_property("click", "delivery_mode"))
        self.assertTrue(backend._session.supports_capability("accessibility.element_tokens", "click"))
        self.assertFalse(backend._session._has_tool("browser"))


    @remote_requirement
    def test_expected_snapshot_token_survives_real_mcp2_discovery(self):
        clicks = NativeBaseline.exercise_safari_stdio(self)
        self.assertEqual(clicks[0]["arguments"].get("element_token"), "sfixture:1",
                         "A06: MCP 2 drops Cua capability extensions before token injection")


    @remote_requirement
    def test_remote_manifest_cannot_relocate_main_executable(self):
        with mock.patch.object(cua.subprocess, "run", return_value=self.manifest_result()), \
             mock.patch.object(cua, "_cua_driver_supports_no_overlay", return_value=False):
            command, _ = cua._resolve_mcp_invocation(str(self.launcher))
        self.assertEqual(command, str(self.launcher), "C03: remote command must remain data")

    @remote_requirement
    def test_bounded_remote_backend_does_not_own_private_local_daemon(self):
        policy = self.root / "policy.json"
        policy.write_text('{"version":3}')
        with mock.patch.object(cua, "_cua_capability_manifest", return_value=str(policy)):
            backend = cua.CuaDriverBackend(permission_mode="bounded")
        self.addCleanup(backend.stop)
        self.assertIsNone(backend._embedded_daemon, "C04: runtime policy belongs to fixed Mac app")

    @remote_requirement
    def test_yolo_cannot_widen_remote_runtime_policy(self):
        with mock.patch("tools.approval.is_approval_bypass_active_for_session", return_value=True):
            mode = native._cua_permission_mode("fixture-yolo")
        self.assertEqual(mode, "standard", "C04: local approval bypass must not change endpoint policy")

    @remote_requirement
    def test_offline_native_error_has_target_and_delivery(self):
        # Keep the upstream-only frozen XFAIL; patched mode requires actual
        # typed evidence rather than a fabricated transport-looking message.
        error = (cua._RemoteMacError(cua._remote_metadata("mac_unreachable", "connect"))
                 if REMOTE_MODE else ConnectionError("fixture offline"))
        with mock.patch.object(native, "_get_backend", side_effect=error):
            result = json.loads(native.handle_computer_use({"action": "list_windows"}))
        self.assertEqual(result.get("target_node"), "macbook", "F01: missing typed offline envelope")
        self.assertEqual(result.get("delivery"), "not_sent")

    @remote_requirement
    def test_empty_remote_windows_do_not_diagnose_main_display(self):
        with mock.patch.dict(os.environ, {"HERMES_MAC_BINDING": "test"} if REMOTE_MODE else {}, clear=True):
            reason = cua._empty_discovery_reason()
        self.assertNotIn("DISPLAY", reason, "C02: diagnostic uses caller OS instead of remote target")



class RemoteNativeAdditional(NativeCase):
    def _capture_failure_scenarios(self, args, *, code="mac_target_stale", **control):
        # Each scenario runs the actual patched native capture through the fake
        # SSH/endpoint/driver subprocesses. Only the public backend lookup is fixed.
        for state in ("clean", "observed", "reconnected"):
            with self.subTest(state=state):
                self.harness.set()
                backend = self.backend()
                backend.start()
                with mock.patch.object(native, "_get_backend", return_value=backend):
                    good = {"action": "capture", "app": "Safari", "pid": 4242,
                            "window_id": 701, "mode": "som"}
                    if state != "clean":
                        prior = native.handle_computer_use(good)
                        self.assertTrue(prior["_multimodal"])
                        self.assertEqual(backend._snapshot_tokens, {1: "sfixture:1"})
                    if state == "reconnected":
                        backend._session.stop()
                        fresh = native.handle_computer_use(good)
                        self.assertTrue(fresh["_multimodal"])
                        self.assertNotEqual(prior["generation"], fresh["generation"])
                    before = len(self.calls())
                    self.harness.set(**control)
                    with mock.patch.object(backend._session, "_call_tool_via_cli", side_effect=AssertionError("forbidden CLI fallback")) as fallback:
                        raw = native.handle_computer_use({"action": "capture", "mode": "som", **args})
                    fallback.assert_not_called()
                    self.assertIsInstance(raw, str, raw)
                    failed = json.loads(raw)
                    self.assertFalse(failed["ok"])
                    self.assertEqual(failed["code"], code)
                    self.assertEqual(failed["error"], code)
                    self.assertEqual(failed["target_node"], "macbook")
                    self.assertEqual(failed["delivery"], "not_sent")
                    self.assertNotEqual(failed["next_action"], "continue")
                    self.assertNotEqual(failed["phase"], "driver")
                    self.assertEqual(backend._remote_meta["code"], code)
                    self.assertIsNone(backend._active_pid)
                    self.assertIsNone(backend._active_window_id)
                    self.assertIsNone(backend._last_target)
                    self.assertEqual(backend._snapshot_tokens, {})
                    calls = self.calls()[before:]
                    self.assertFalse(any(c["name"] in {"click", "type_text", "set_value", "serve", "stop"} for c in calls))
                    if args.get("mode") == "vision":
                        self.assertEqual([c["name"] for c in calls if c["name"] == "get_window_state"], ["get_window_state"])
                backend.stop()

    def test_remote_empty_windows_capture_is_closed(self):
        self._capture_failure_scenarios({"app": "Safari"}, empty=True)

    def test_remote_unmatched_app_capture_is_closed(self):
        self._capture_failure_scenarios({"app": "No matching synthetic application"})

    def test_remote_partial_exact_capture_target_is_closed(self):
        for target in ({"pid": 4242}, {"window_id": 701}):
            with self.subTest(target=target):
                self._capture_failure_scenarios(target)

    def test_remote_invalid_exact_capture_target_is_closed(self):
        for target in ({"pid": True, "window_id": 701}, {"pid": 4242, "window_id": "invalid"}):
            with self.subTest(target=target):
                self._capture_failure_scenarios(target)

    def test_remote_vision_without_image_is_closed(self):
        self._capture_failure_scenarios({"pid": 4242, "window_id": 701, "mode": "vision"},
                                        code="mac_driver_unavailable", without_image=True)

    def test_remote_vision_with_unusable_image_is_closed(self):
        self._capture_failure_scenarios({"pid": 4242, "window_id": 701, "mode": "vision"},
                                        code="mac_driver_unavailable", invalid_image=True)

    def test_remote_ax_only_capture_still_preserves_valid_elements(self):
        self.harness.set(without_image=True)
        backend = self.backend()
        backend.start()
        with mock.patch.object(native, "_get_backend", return_value=backend):
            raw = native.handle_computer_use({"action": "capture", "pid": 4242, "window_id": 701, "mode": "ax"})
        self.assertIsInstance(raw, str)
        result = json.loads(raw)
        self.assertNotIn("error", result)
        self.assertEqual(result["code"], "mac_ok")
        self.assertEqual(result["delivery"], "observed")
        self.assertEqual(len(result["elements"]), 1)
        self.assertEqual(backend._snapshot_tokens, {1: "sfixture:1"})
        self.assertEqual((backend._active_pid, backend._active_window_id), (4242, 701))

    def test_remote_degraded_capture_reaches_native_tool_as_desktop_unavailable(self):
        self.harness.set(empty_capture=True, degraded_capture=True)
        session_id = 'degraded-desktop-fixture'
        self.addCleanup(native.release_computer_use_session, session_id)
        result = json.loads(native.handle_computer_use({'action': 'capture', 'mode': 'som',
            'pid': 4242, 'window_id': 701}, session_id=session_id))
        self.assertFalse(result['ok'])
        self.assertEqual(result['code'], 'mac_desktop_unavailable')
        self.assertEqual(result['phase'], 'driver')
        self.assertEqual(result['delivery'], 'observed')
        self.assertEqual(result['target_node'], 'macbook')
        self.assertNotEqual(result['next_action'], 'continue')
        self.assertEqual([c['name'] for c in self.calls()],
                         ['start_session', 'get_window_state'])

    def test_capture_failure_preserves_current_driver_error_and_unknown(self):
        for code, delivery, next_action in [("mac_permission_denied", "not_sent", "recheck_binding_and_availability"),
                                            ("mac_action_outcome_unknown", "unknown", "observe_and_reconcile")]:
            with self.subTest(code=code):
                backend = self.backend()
                meta = {"target_node": "macbook", "code": code, "phase": "response", "delivery": delivery,
                        "retryable_after_recheck": False, "next_action": next_action, "generation": "a" * 32}
                backend._remote_meta = dict(meta)
                backend._active_pid, backend._active_window_id = 4242, 701
                backend._snapshot_tokens = {1: "sfixture:1"}
                with self.assertRaises(cua._RemoteMacError) as raised:
                    backend._failed_capture("vision", "Synthetic failure")
                self.assertEqual(raised.exception.meta, meta)
                self.assertEqual(cua._remote_error_response(raised.exception)["delivery"], delivery)
                self.assertIsNone(backend._active_pid)
                self.assertEqual(backend._snapshot_tokens, {})

    def test_full_native_entry_owns_backend_and_real_mcp(self):
        session_id = "remote-native-entry"
        self.addCleanup(native.release_computer_use_session, session_id)
        capture = native.handle_computer_use({"action": "capture", "app": "Safari", "pid": 4242,
                                              "window_id": 701, "mode": "som"}, session_id=session_id)
        self.assertIsInstance(capture, dict, capture)
        self.assertTrue(capture["_multimodal"])
        with mock.patch.object(native, "_approval_callback", return_value="approve_once"):
            result = json.loads(native.handle_computer_use({"action": "click", "element": 1}, session_id=session_id))
        self.assertTrue(result["ok"], result)
        self.assertEqual(result["delivery"], "confirmed")
        clicks = [c for c in self.calls() if c["name"] == "click"]
        self.assertEqual(len(clicks), 1)
        self.assertEqual(clicks[0]["arguments"]["element_token"], "sfixture:1")

    def test_native_disconnect_reconnect_requires_capture_without_replay(self):
        backend = self.backend()
        backend.start()
        capture_args = {"action": "capture", "app": "Safari", "pid": 4242, "window_id": 701, "mode": "som"}
        with mock.patch.object(native, "_get_backend", return_value=backend), mock.patch.object(native, "_approval_callback", return_value="approve_once"):
            first = native.handle_computer_use(capture_args)
            self.harness.set(disconnect=True)
            result = json.loads(native.handle_computer_use({"action": "click", "element": 1}))
            self.assertEqual(result["meta"]["delivery"], "unknown")
            self.assertEqual(result["verdict"]["decision"], "reconcile")
            self.assertEqual(result["delivery"], "unknown")
            again = json.loads(native.handle_computer_use({"action": "click", "element": 1}))
            self.assertFalse(again.get("ok", False))
            self.assertEqual(len([c for c in self.calls() if c["name"] == "click"]), 1)
            self.harness.set()
            fresh = native.handle_computer_use(capture_args)
            self.assertTrue(fresh["_multimodal"])
            self.assertNotEqual(first["generation"], fresh["generation"])
            good = json.loads(native.handle_computer_use({"action": "click", "element": 1}))
            self.assertTrue(good["ok"])
            self.assertEqual(len([c for c in self.calls() if c["name"] == "click"]), 2)

    def test_native_driver_restart_recaptures_without_binding_republication(self):
        backend = self.backend()
        backend.start()
        args = {"action": "capture", "app": "Safari", "pid": 4242, "window_id": 701, "mode": "som"}
        before = self.harness.config.read_bytes(), self.harness.endpoint.read_bytes()
        with mock.patch.object(native, "_get_backend", return_value=backend), mock.patch.object(native, "_approval_callback", return_value="approve_once"):
            first = native.handle_computer_use(args)
            observed = dict(self.harness.runtime, runtime_pid=54321, socket_inode=987)
            self.harness.observed.write_text(json.dumps(observed))
            refused = json.loads(native.handle_computer_use({"action": "click", "element": 1}))
            self.assertEqual(refused["code"], "mac_target_stale")
            self.assertEqual(refused["delivery"], "not_sent")
            self.assertFalse(any(c["name"] == "click" for c in self.calls()))
            fresh = native.handle_computer_use(args)
            self.assertTrue(fresh["_multimodal"], fresh)
            self.assertNotEqual(first["generation"], fresh["generation"])
            result = json.loads(native.handle_computer_use({"action": "click", "element": 1}))
            self.assertTrue(result["ok"], result)
        self.assertEqual(before, (self.harness.config.read_bytes(), self.harness.endpoint.read_bytes()))

    def test_native_initial_discovery_accepts_runtime_turnover(self):
        self.harness.observed.write_text(json.dumps(dict(self.harness.runtime, runtime_pid=54321, socket_inode=987)))
        session_id = "runtime-turnover-native-entry"
        self.addCleanup(native.release_computer_use_session, session_id)
        result = native.handle_computer_use({"action": "capture", "app": "Safari", "pid": 4242,
                                            "window_id": 701, "mode": "som"}, session_id=session_id)
        self.assertTrue(result["_multimodal"], result)
        self.assertTrue(all(c["name"] in {"start_session", "list_windows", "get_window_state"} for c in self.calls()))

    def test_remote_stderr_is_absent_from_native_logs_and_errors(self):
        self.harness.set(deny="get_window_state", stderr=True)
        stream = io.StringIO()
        handler = logging.StreamHandler(stream)
        cua.logger.addHandler(handler)
        native.logger.addHandler(handler)
        self.addCleanup(cua.logger.removeHandler, handler)
        self.addCleanup(native.logger.removeHandler, handler)
        backend = self.backend()
        with mock.patch.object(cua.logger, "disabled", False), mock.patch.object(native.logger, "disabled", False):
            backend.start()
            with mock.patch.object(native, "_get_backend", return_value=backend):
                result = native.handle_computer_use({"action": "capture", "app": "Safari", "pid": 4242,
                                                    "window_id": 701, "mode": "som"})
        self.assertEqual(json.loads(result)["code"], "mac_permission_denied")
        self.assertNotIn("SYNTHETIC_SECRET_SENTINEL", result + stream.getvalue())

    def test_remote_long_wait_refused_within_ordinary_budget(self):
        backend = self.backend()
        backend.start()
        with mock.patch.object(native, "_get_backend", return_value=backend):
            result = json.loads(native.handle_computer_use({"action": "wait", "seconds": 31}))
        self.assertEqual(result["code"], "mac_policy_refused")
        self.assertEqual(result["delivery"], "not_sent")
        self.assertEqual(self.calls(), [])

    def test_native_set_value_refused_before_driver(self):
        backend = self.backend()
        backend.start()
        with mock.patch.object(native, "_get_backend", return_value=backend), mock.patch.object(native, "_approval_callback", return_value="approve_once"):
            result = json.loads(native.handle_computer_use({"action": "set_value", "value": "fixture", "element": 1}))
        self.assertEqual(result["code"], "mac_policy_refused")
        self.assertFalse(result["ok"])
        self.assertEqual(self.calls(), [])

    def test_native_capture_metadata_and_image(self):
        backend = self.backend()
        backend.start()
        with mock.patch.object(native, "_get_backend", return_value=backend):
            result = native.handle_computer_use({"action": "capture", "app": "Safari", "pid": 4242, "window_id": 701, "mode": "som"})
        self.assertTrue(result["_multimodal"])
        self.assertEqual(result["target_node"], "macbook")
        self.assertEqual(result["delivery"], "observed")
        self.assertTrue(result["generation"])

    def test_native_offline_startup_and_no_fallback(self):
        self.harness.set(ssh="offline")
        self.assertTrue(native.check_computer_use_requirements())
        backend = self.backend()
        def started_backend(session_id=""):
            backend.start()
            return backend
        with mock.patch.object(native, "_get_backend", side_effect=started_backend), \
             mock.patch.object(backend._session, "_call_tool_via_cli", side_effect=AssertionError("fallback forbidden")):
            result = json.loads(native.handle_computer_use({"action": "list_windows"}))
        self.assertEqual(result["code"], "mac_unreachable")
        self.assertEqual(result["phase"], "connect")
        self.assertEqual(result["delivery"], "not_sent")
        self.assertTrue((self.harness.root / "ssh-args.json").exists(), "did not reach real fake SSH")
        self.assertEqual(len([p for p in (self.harness.root / "pids.jsonl").read_text().splitlines() if json.loads(p)['mode'] == '--ssh']), 1)
        self.assertEqual(self.calls(), [])

    def test_native_auth_host_errors_reach_fake_ssh(self):
        for fault, code in (("auth", "mac_auth_failed"), ("host", "mac_host_identity_mismatch")):
            self.harness.set(ssh=fault)
            backend = self.backend()
            def started_backend(session_id=""):
                backend.start()
                return backend
            with mock.patch.object(native, "_get_backend", side_effect=started_backend), \
                 mock.patch.object(backend._session, "_call_tool_via_cli", side_effect=AssertionError("fallback forbidden")):
                result = json.loads(native.handle_computer_use({"action":"list_windows"}))
            backend.stop()
            self.assertEqual((result["code"], result["phase"], result["delivery"]), (code,"connect","not_sent"))
            self.assertNotIn('SYNTHETIC_SECRET_SENTINEL', json.dumps(result))
            self.assertEqual(self.calls(), [])
        self.assertEqual(len([p for p in (self.harness.root / "pids.jsonl").read_text().splitlines() if json.loads(p)['mode'] == '--ssh']), 2)

    def test_native_lookup_typeerror_is_not_offline_proof(self):
        with mock.patch.object(native, "_get_backend", side_effect=TypeError("mac_unreachable SYNTHETIC_SECRET_SENTINEL")):
            result = json.loads(native.handle_computer_use({"action":"list_windows"}))
        self.assertEqual((result['code'],result['delivery']), ('mac_driver_unavailable','unknown'))
        self.assertFalse((self.harness.root / 'ssh-args.json').exists())
        self.assertEqual(self.calls(), [])
        self.assertNotIn('SYNTHETIC_SECRET_SENTINEL', json.dumps(result))

    def test_local_startup_gate_does_not_downgrade_remote_uncertainty(self):
        backend = self.backend()
        unknown = cua._RemoteMacError(cua._remote_metadata('mac_action_outcome_unknown', 'response', 'unknown'))
        with mock.patch.object(backend._session, 'start', side_effect=unknown), \
             mock.patch.object(backend._session, '_call_tool_async') as dispatch:
            result = backend._action('click', {})
        self.assertEqual((result.code, result.meta['delivery']), ('mac_action_outcome_unknown', 'unknown'))
        dispatch.assert_not_called()
        self.assertEqual(self.calls(), [])


class RemoteStartupErrorMetadata(unittest.TestCase):
    """Only the patched remote gate runs these mandatory closed-envelope checks."""
    secret = "SYNTHETIC_SECRET_SENTINEL mac_auth_failed mac_unreachable"

    def mcp_error(self, code="mac_unreachable", phase="connect", delivery="not_sent", **changes):
        from mcp.shared.exceptions import MCPError
        data = cua._remote_metadata("mac_unreachable", "connect", "not_sent")
        data.update(code=code, phase=phase, delivery=delivery)
        data.update(changes)
        return MCPError(-32000, self.secret, data=data)

    def response(self, exc):
        result = cua._remote_error_response(exc, "mac_unreachable")
        self.assertFalse(result['ok'])
        self.assertEqual(result['target_node'], 'macbook')
        self.assertNotIn('SYNTHETIC_SECRET_SENTINEL', json.dumps(result))
        return result

    def test_nested_groups_and_causal_wrappers_preserve_typed_metadata(self):
        for code in ('mac_unreachable', 'mac_auth_failed', 'mac_host_identity_mismatch', 'mac_permission_denied', 'mac_driver_unavailable'):
            with self.subTest(code=code):
                error = self.mcp_error(code, generation='a' * 32)
                wrapped = RuntimeError(self.secret)
                wrapped.__cause__ = ExceptionGroup(self.secret, [ExceptionGroup(self.secret, [error])])
                result = self.response(wrapped)
                self.assertEqual((result['code'], result['phase'], result['delivery']), (code, 'connect', 'not_sent'))
                self.assertEqual(result['generation'], 'a' * 32)
                self.assertEqual(result['retryable_after_recheck'], code == 'mac_unreachable')
                self.assertEqual(result['next_action'], 'recheck_binding_and_availability')
                # Native lifecycle wraps the mapping result again, from None.
                typed = cua._RemoteMacError(result)
                typed.__context__ = wrapped
                typed.__suppress_context__ = True
                self.assertEqual(self.response(typed), result)

    def test_unknown_delivery_survives_outer_wrappers_and_conflicts(self):
        unknown = self.mcp_error('mac_action_outcome_unknown', 'response', 'unknown')
        wrapped = RuntimeError(self.secret)
        wrapped.__cause__ = unknown
        result = self.response(wrapped)
        self.assertEqual((result['code'], result['phase'], result['delivery']), ('mac_action_outcome_unknown', 'response', 'unknown'))
        for errors in ([unknown, self.mcp_error()], [self.mcp_error(), unknown]):
            result = self.response(ExceptionGroup(self.secret, errors))
            self.assertEqual(result['code'], 'mac_action_outcome_unknown')
            self.assertEqual(result['delivery'], 'unknown')
            self.assertEqual(result['next_action'], 'observe_and_reconcile')
        typed = cua._RemoteMacError(cua._remote_metadata('mac_driver_unavailable', 'admission', 'unknown'))
        typed.__context__ = TypeError(self.secret)
        self.assertEqual(self.response(typed)['delivery'], 'unknown')

    def test_conflicting_branch_order_cannot_choose_false_not_sent(self):
        errors = [self.mcp_error(), self.mcp_error('mac_auth_failed')]
        forward = self.response(ExceptionGroup(self.secret, errors))
        reverse = self.response(ExceptionGroup(self.secret, list(reversed(errors))))
        self.assertEqual(forward, reverse)
        self.assertEqual((forward['code'], forward['delivery']), ('mac_protocol_error', 'unknown'))
        generation_conflict = self.response(ExceptionGroup(self.secret, [self.mcp_error(generation='a'*32), self.mcp_error(generation='b'*32)]))
        self.assertEqual(generation_conflict['delivery'], 'unknown')

    def test_malformed_or_extra_metadata_never_becomes_transport_certainty(self):
        cases = [
            {'target_node': 'other'}, {'target_node': []}, {'code': self.secret}, {'code': []},
            {'phase': self.secret}, {'phase': {}}, {'delivery': self.secret}, {'delivery': False},
            {'next_action': 'continue'}, {'next_action': {}}, {'retryable_after_recheck': 1},
            {'generation': self.secret}, {'generation': 'a'*33}, {'generation': 7},
            {'unexpected': self.secret}, {'code': 'mac_action_outcome_unknown'},
        ]
        for changes in cases:
            with self.subTest(changes=list(changes)):
                result = self.response(self.mcp_error(**changes))
                self.assertEqual((result['code'], result['delivery']), ('mac_protocol_error', 'unknown'))
        for data in (None, [], {'code':'mac_unreachable'}):
            err = self.mcp_error()
            err.error.data = data
            self.assertEqual(self.response(err)['delivery'], 'unknown')
        err = self.mcp_error()
        err.error.code = -32601
        self.assertEqual(self.response(err)['delivery'], 'unknown')

    def test_untyped_typeerror_and_message_substrings_are_not_offline_proof(self):
        class UnprintableError(TypeError):
            def __str__(self):
                raise AssertionError('exception message must not be inspected')
        for error in (TypeError(self.secret), RuntimeError(self.secret), UnprintableError()):
            error.meta = cua._remote_metadata('mac_auth_failed', 'connect')
            result = self.response(error)
            self.assertEqual((result['code'], result['delivery']), ('mac_driver_unavailable', 'unknown'))
        # Canonical guidance is constructed, not delegated to untrusted flags.
        error = self.mcp_error(retryable_after_recheck=False, next_action='observe_and_reconcile')
        result = self.response(error)
        self.assertTrue(result['retryable_after_recheck'])
        self.assertEqual(result['next_action'], 'recheck_binding_and_availability')
        failed = self.response(self.mcp_error('mac_action_failed', 'driver', 'confirmed'))
        self.assertEqual(failed['next_action'], 'observe_and_reconcile')

    def test_cycles_deep_and_wide_trees_have_bounded_conservative_results(self):
        import time
        cyclic = RuntimeError(self.secret)
        cyclic.__cause__ = cyclic
        cyclic.__context__ = self.mcp_error()
        deep = self.mcp_error()
        for _ in range(100):
            parent = RuntimeError(self.secret)
            parent.__cause__ = deep
            deep = parent
        wide = ExceptionGroup(self.secret, [self.mcp_error() for _ in range(1000)])
        for error in (cyclic, deep, wide):
            start = time.monotonic()
            result = self.response(error)
            self.assertEqual(result['delivery'], 'unknown')
            self.assertLess(time.monotonic() - start, 0.5)

    def test_shared_acyclic_causes_are_not_falsely_conflicting(self):
        error = self.mcp_error()
        a, b = RuntimeError('outer'), RuntimeError('outer')
        a.__cause__ = b.__cause__ = error
        result = self.response(ExceptionGroup('outer', [a,b]))
        self.assertEqual((result['code'], result['phase'], result['delivery']), ('mac_unreachable', 'connect', 'not_sent'))

    def test_explicit_local_predispatch_proof_is_narrow(self):
        error = TypeError(self.secret)
        self.assertEqual(self.response(error)['delivery'], 'unknown')
        result = cua._remote_error_response(error, before_dispatch=True)
        self.assertEqual((result['code'], result['phase'], result['delivery']), ('mac_driver_unavailable', 'admission', 'not_sent'))
        self.assertNotIn('SYNTHETIC_SECRET_SENTINEL', json.dumps(result))
        malformed = self.mcp_error(delivery=False)
        missing = self.mcp_error()
        missing.error.data = None
        unknown = self.mcp_error('mac_action_outcome_unknown', 'response', 'unknown')
        conflict = ExceptionGroup('outer', [self.mcp_error(), self.mcp_error('mac_auth_failed')])
        cycle = RuntimeError('outer')
        cycle.__cause__ = cycle
        for error in (malformed, missing, unknown, conflict, cycle, ExceptionGroup('outer', [TypeError(self.secret)])):
            result = cua._remote_error_response(error, before_dispatch=True)
            self.assertEqual(result['delivery'], 'unknown')
            self.assertNotIn('SYNTHETIC_SECRET_SENTINEL', json.dumps(result))


class PatchedNativeGate(unittest.TestCase):
    def test_actual_patched_native_required_contracts(self):
        env = dict(os.environ, LOOM_NATIVE_TEST_MODE="remote")
        result = subprocess.run([sys.executable, "-B", "-m", "unittest", "discover", "-s", str(HERE), "-p", "test_*.py", "-v"],
                                env=env, capture_output=True, text=True, timeout=120)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertNotIn("expected failure", result.stderr)
        self.assertNotIn("skipped", result.stderr)
        print("\nPatched native subprocess: " + result.stderr.split("Ran ")[-1].strip())
        env["LOOM_NATIVE_TEST_MODE"] = "patched-local"
        result = subprocess.run([sys.executable, "-B", "-m", "unittest", "test_native_contract", "test_transport_contract", "-v"],
                                cwd=HERE, env=env, capture_output=True, text=True, timeout=120)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("expected failures=16", result.stderr)
        self.assertIn("Ran 40 tests", result.stderr)
        print("Patched source, remote absent: " + result.stderr.split("Ran ")[-1].strip())


def load_tests(loader, tests, pattern):
    suite = unittest.TestSuite()
    if os.environ.get("LOOM_NATIVE_STRESS_ONLY"):
        for _ in range(20):
            suite.addTest(RemoteNativeAdditional("test_native_disconnect_reconnect_requires_capture_without_replay"))
        return suite
    if REMOTE_MODE and os.environ.get("LOOM_STARTUP_ERROR_STRESS_ONLY"):
        for _ in range(20):
            suite.addTests(loader.loadTestsFromTestCase(RemoteStartupErrorMetadata))
            suite.addTest(RemoteNativeAdditional("test_native_offline_startup_and_no_fallback"))
        return suite
    if REMOTE_MODE:
        suite.addTests(loader.loadTestsFromTestCase(RemoteStartupErrorMetadata))
        suite.addTests(loader.loadTestsFromTestCase(NativeExpectedRemoteFailures))
        suite.addTests(loader.loadTestsFromTestCase(RemoteNativeAdditional))
        for name in ["test_pinned_schema_and_source_identity", "test_real_mcp_image_model_and_native_multimodal_envelope",
                     "test_native_denial_precedes_backend_start", "test_mac_blocked_shortcut_works_on_linux_caller",
                     "test_wrong_app_cannot_reuse_sticky_safari_target", "test_safari_capture_and_action_over_real_stdio"]:
            suite.addTest(NativeBaseline(name))
    else:
        suite.addTests(loader.loadTestsFromTestCase(NativeBaseline))
        suite.addTests(loader.loadTestsFromTestCase(NativeExpectedRemoteFailures))
        if os.environ.get("LOOM_NATIVE_TEST_MODE") != "patched-local":
            suite.addTests(loader.loadTestsFromTestCase(PatchedNativeGate))
    return suite


if __name__ == "__main__":
    unittest.main()
