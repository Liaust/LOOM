"""Fault injection into actual pinned native session code, with no SSH/GUI.

Baseline tests preserve upstream behavior. The same fault assertions are
required passes in remote mode; subprocess recovery tests supplement them.
"""
import asyncio
import concurrent.futures
import copy
import json
import os
import threading
from types import SimpleNamespace
import unittest
from unittest import mock

from test_native_contract import NativeCase, FIXTURE, HERE, cua, native, unpack, CallToolResult, REMOTE_MODE, remote_requirement


class FaultPeer:
    """Single synthetic endpoint with observable dispatch counts."""
    def __init__(self, fault=None):
        self.fault = fault
        self.calls = []

    async def call_tool(self, name, arguments):
        self.calls.append((name, copy.deepcopy(arguments)))
        if self.fault is not None:
            failure, self.fault = self.fault, None
            if isinstance(failure, BaseException):
                raise failure
            return CallToolResult.model_validate(failure)
        return CallToolResult.model_validate(FIXTURE["action_result"])


class TransportCase(NativeCase):
    def connected(self, fault=None):
        backend = self.backend()
        peer = FaultPeer(fault)
        session = backend._session
        session._session = peer
        session._started = True
        self.enterContext(mock.patch.object(session._bridge, "run", side_effect=lambda coro, timeout=None: asyncio.run(coro)))

        def reconnect():
            session._transport_generation += 1
            session._notify_transport_reset()
            session._started = True

        self.enterContext(mock.patch.object(session, "_restart_session_locked", side_effect=reconnect))
        # Cleanup is test-owned; don't issue an extra lifecycle action into counts.
        self.addCleanup(setattr, session, "_started", False)
        return backend, peer


class TransportBaseline(TransportCase):
    def test_before_start_has_zero_dispatch_and_no_fallback(self):
        backend, peer = self.connected()
        backend._session._started = False
        with mock.patch.object(backend._session, "start", side_effect=ConnectionError("synthetic offline")), \
             mock.patch.object(backend._session, "_call_tool_via_cli") as fallback:
            result = backend._action("click", {"pid": 4242, "window_id": 701})
        self.assertFalse(result.ok)
        self.assertEqual(peer.calls, [])
        fallback.assert_not_called()

    def test_after_dispatch_broken_pipe_does_not_replay_input(self):
        backend, peer = self.connected(BrokenPipeError("synthetic lost response"))
        result = backend._action("click", {"pid": 4242, "window_id": 701})
        self.assertFalse(result.ok)
        self.assertEqual(result.code, "transport_outcome_unknown")
        self.assertEqual([n for n, _ in peer.calls], ["click"])

    def test_transient_daemon_failure_does_not_replay_input(self):
        backend, peer = self.connected(RuntimeError("daemon transport error"))
        with mock.patch.object(backend._session, "_call_tool_via_cli") as fallback:
            result = backend._action("type_text", {"text": "fixture"})
        self.assertEqual(result.code, "transport_outcome_unknown")
        self.assertEqual([n for n, _ in peer.calls], ["type_text"])
        fallback.assert_not_called()

    def test_timeout_has_unknown_outcome_without_input_replay(self):
        backend, peer = self.connected(concurrent.futures.TimeoutError("synthetic deadline"))
        result = backend._action("click", {"pid": 4242, "window_id": 701})
        self.assertEqual(result.code, "timeout_outcome_unknown")
        self.assertTrue(backend._session._timeout_suspect)
        self.assertEqual([n for n, _ in peer.calls], ["click"])

    def test_closed_read_reconnects_once_and_invalidates_target(self):
        backend, peer = self.connected(BrokenPipeError("synthetic read disconnect"))
        backend._active_pid, backend._active_window_id = 4242, 701
        backend._last_target = {"pid": 4242, "window_id": 701}
        backend._snapshot_tokens = {1: "sfixture:1"}
        result = backend._session.call_tool("list_windows", {})
        self.assertFalse(result["isError"])
        self.assertEqual([n for n, _ in peer.calls], ["list_windows", "list_windows"])
        self.assertIsNone(backend._active_pid)
        self.assertEqual(backend._snapshot_tokens, {})
        self.assertEqual(backend._session._transport_generation, 1)

    def test_permission_denial_is_conclusive_and_never_replayed(self):
        refusal = {"content": [{"type": "text", "text": "synthetic permission refusal"}],
                   "isError": True, "structuredContent": {"code": "mac_permission_denied",
                   "target_node": "macbook", "phase": "admission", "delivery": "not_sent"}}
        backend, peer = self.connected(refusal)
        result = backend._action("click", {"pid": 4242, "window_id": 701})
        self.assertEqual(result.code, "mac_permission_denied")
        self.assertEqual(result.meta["delivery"], "not_sent")
        self.assertEqual(len(peer.calls), 1)

    def test_reset_requires_capture_before_backend_click(self):
        backend, peer = self.connected()
        backend._active_pid, backend._active_window_id = 4242, 701
        backend._snapshot_tokens = {1: "sfixture:1"}
        backend._session._notify_transport_reset()
        result = backend.click(element=1)
        self.assertFalse(result.ok)
        self.assertEqual(peer.calls, [])

    def test_unknown_result_survives_native_text_response(self):
        backend, _ = self.connected(BrokenPipeError("synthetic response loss"))
        result = json.loads(native._text_response(backend._action("click", {})))
        self.assertEqual(result["code"], "transport_outcome_unknown")
        self.assertEqual(result["meta"]["next_step"], "fresh_state")
        self.assertIn("unknown", result["message"])

    def test_delivery_case_collection_distinguishes_before_and_after_send(self):
        cases = json.loads((HERE / "fixtures/routing_cases.json").read_text())["delivery_cases"]
        self.assertEqual(len({x["id"] for x in cases}), 5)
        self.assertEqual({x["delivery"] for x in cases}, {"not_sent", "unknown"})
        for case in cases:
            self.assertEqual(case["max_input_calls"], int(case["delivery"] == "unknown"))


class TransportExpectedRemoteFailures(TransportCase):
    @remote_requirement
    def test_real_initialize_refusal_reaches_native_without_ssh_or_fallback(self):
        self.harness.config.chmod(0o666)
        backend = self.backend()
        with mock.patch.object(native, '_get_backend', return_value=backend), \
             mock.patch.object(backend._session, '_call_tool_via_cli', side_effect=AssertionError('fallback')):
            result = json.loads(native.handle_computer_use(
                {'action': 'list_windows', 'app': 'TextEdit'}, session_id='admission-fixture'))
        self.assertFalse(result['ok'])
        self.assertEqual(result['code'], 'mac_host_identity_mismatch')
        self.assertEqual(result['phase'], 'admission')
        self.assertEqual(result['delivery'], 'not_sent')
        self.assertFalse((self.harness.root / 'ssh-args.json').exists())
        self.assertEqual(self.calls(), [])

    @remote_requirement
    def test_before_dispatch_error_is_typed_not_sent(self):
        backend, peer = self.connected()
        backend._session._started = False
        with mock.patch.object(backend._session, "start", side_effect=ConnectionError("synthetic before send")):
            result = backend._action("click", {})
        self.assertEqual(peer.calls, [])
        self.assertEqual(result.meta.get("delivery"), "not_sent", "F05: delivery phase is lost")

    @remote_requirement
    def test_unknown_error_has_full_remote_delivery_contract(self):
        backend, _ = self.connected(BrokenPipeError("synthetic after send"))
        result = backend._action("click", {})
        self.assertEqual(result.meta.get("target_node"), "macbook", "F06: missing target")
        self.assertEqual(result.meta.get("delivery"), "unknown")
        self.assertEqual(result.code, "mac_action_outcome_unknown")

    @remote_requirement
    def test_read_failure_never_enters_cli_fallback(self):
        backend, _ = self.connected(RuntimeError("daemon transport error"))
        with mock.patch.object(backend._session, "_call_tool_via_cli", return_value=unpack(FIXTURE["windows_result"])) as fallback:
            backend._session.call_tool("list_windows", {})
        fallback.assert_not_called()

    @remote_requirement
    def test_empty_discovery_never_enters_cli_fallback(self):
        backend, _ = self.connected()
        empty = {"data": [], "images": [], "structuredContent": {"windows": []}, "isError": False}
        with mock.patch.object(backend._session, "call_tool", return_value=empty), \
             mock.patch.object(backend._session, "_call_tool_via_cli", return_value=empty) as fallback:
            backend._load_windows()
        fallback.assert_not_called()

    @remote_requirement
    def test_cancel_after_dispatch_preserves_unknown_delivery(self):
        backend, peer = self.connected(concurrent.futures.CancelledError("synthetic cancel"))
        result = backend._action("click", {})
        self.assertEqual(len(peer.calls), 1)
        self.assertEqual(result.meta.get("delivery"), "unknown", "F07: cancellation loses delivery status")

    @remote_requirement
    def test_timeout_immediately_discards_snapshot_references(self):
        backend, _ = self.connected(concurrent.futures.TimeoutError("synthetic deadline"))
        backend._active_pid, backend._active_window_id = 4242, 701
        backend._snapshot_tokens = {1: "sfixture:1"}
        backend._action("click", {})
        self.assertEqual(backend._snapshot_tokens, {}, "F07/F08: timeout leaves refs until next restart")

    @remote_requirement
    def test_timeout_does_not_leave_coroutine_running(self):
        bridge = cua._AsyncBridge()
        bridge.start()
        finished = threading.Event()
        cancelled = threading.Event()

        async def request():
            try:
                await asyncio.sleep(0.03)
                finished.set()
            except asyncio.CancelledError:
                cancelled.set()
                raise

        try:
            with self.assertRaises(concurrent.futures.TimeoutError):
                bridge.run(request(), timeout=0.001)
            # Let this harmless task finish so teardown never strands it.
            finished.wait(0.15)
            self.assertTrue(cancelled.is_set(), "F07: future.result timeout does not cancel the coroutine")
        finally:
            bridge.stop()

    @remote_requirement
    def test_second_input_requires_reconciliation_after_unknown(self):
        backend, peer = self.connected(RuntimeError("daemon transport error"))
        backend._session.call_tool("click", {"pid": 4242, "window_id": 701})
        backend._session.call_tool("click", {"pid": 4242, "window_id": 701})
        self.assertEqual(len(peer.calls), 1, "F08: session admission does not enforce reconciliation")

    @remote_requirement
    def test_routine_native_unknown_metadata_redacts_stderr_sentinel(self):
        backend, _ = self.connected(BrokenPipeError("SYNTHETIC_SECRET_SENTINEL"))
        response = native._text_response(backend._action("click", {}))
        self.assertNotIn("SYNTHETIC_SECRET_SENTINEL", response, "F12: raw exception detail reaches result metadata")


def load_tests(loader, tests, pattern):
    suite = unittest.TestSuite()
    if os.environ.get("LOOM_NATIVE_STRESS_ONLY"):
        for _ in range(20):
            for name in ["test_timeout_does_not_leave_coroutine_running", "test_cancel_after_dispatch_preserves_unknown_delivery",
                         "test_timeout_immediately_discards_snapshot_references"]:
                suite.addTest(TransportExpectedRemoteFailures(name))
        return suite
    if not REMOTE_MODE:
        suite.addTests(loader.loadTestsFromTestCase(TransportBaseline))
    for name in loader.getTestCaseNames(TransportExpectedRemoteFailures):
        if name == 'test_real_initialize_refusal_reaches_native_without_ssh_or_fallback' and not REMOTE_MODE:
            continue
        suite.addTest(TransportExpectedRemoteFailures(name))
    return suite

if __name__ == "__main__":
    unittest.main()
