"""Focused checks against the packaged Hermes interpreter; no live agent or GUI input."""
import asyncio
import builtins
import os
import subprocess
import sys
import time
import types
import unittest
from unittest.mock import Mock, patch

from opentelemetry import context
from opentelemetry.context.contextvars_context import ContextVarsRuntimeContext
from opentelemetry.util import _importlib_metadata as metadata
from tools.computer_use.cua_backend_session import _AsyncBridge, _CuaDriverSession


class ContextInitializationTests(unittest.TestCase):
    def test_first_mcp_import_with_missing_metadata_uses_packaged_default(self):
        code = """
from opentelemetry.util import _importlib_metadata as metadata
metadata._original_entry_points_cached = lambda: metadata.EntryPoints(())
metadata._original_entry_points_cached.cache_clear = lambda: None
import mcp
from opentelemetry import context
print(type(context._RUNTIME_CONTEXT).__name__)
"""
        result = subprocess.run([sys.executable, "-c", code], capture_output=True,
                                text=True, timeout=15, check=True)
        self.assertIn("ContextVarsRuntimeContext", result.stdout)
        self.assertNotIn("StopIteration", result.stderr)

    def test_valid_custom_provider_remains_authoritative(self):
        selected = object()
        provider = Mock()
        provider.load.return_value = lambda: selected
        with patch.dict(os.environ, {"OTEL_PYTHON_CONTEXT": "owned-custom-context"}), \
                patch.object(context, "entry_points", return_value=[provider]) as discover:
            self.assertIs(context._load_runtime_context(), selected)
            discover.assert_called_once_with(group="opentelemetry_context", name="owned-custom-context")

    def test_unavailable_custom_provider_keeps_native_default_fallback(self):
        with patch.dict(os.environ, {"OTEL_PYTHON_CONTEXT": "owned-missing-context"}), \
                patch.object(metadata, "_original_entry_points_cached", return_value=metadata.EntryPoints(())), \
                self.assertLogs("opentelemetry.context", level="ERROR"):
            self.assertIsInstance(context._load_runtime_context(), ContextVarsRuntimeContext)

    def test_broken_default_code_is_not_silently_ready(self):
        provider = Mock()
        provider.load.side_effect = ImportError("owned broken context module")
        with patch.dict(os.environ, {"OTEL_PYTHON_CONTEXT": "contextvars_context"}), \
                patch.object(context, "entry_points", return_value=[provider]), \
                self.assertLogs("opentelemetry.context", level="ERROR"):
            with self.assertRaisesRegex(ImportError, "owned broken context module"):
                context._load_runtime_context()

    def test_missing_registration_is_not_fabricated(self):
        with patch.object(metadata, "_original_entry_points_cached", return_value=metadata.EntryPoints(())):
            self.assertEqual(list(metadata.entry_points(group="owned-missing-group", name="none")), [])


class SessionStartupTests(unittest.TestCase):
    def test_sdk_failure_is_immediate_and_retires_bridge(self):
        for failure in (ImportError("owned missing SDK"), StopIteration("owned context registration")):
            with self.subTest(error=type(failure).__name__):
                bridge = _AsyncBridge()
                session = _CuaDriverSession(bridge)
                original = builtins.__import__

                def fail_sdk(name, *args, **kwargs):
                    if name == "mcp":
                        raise failure
                    return original(name, *args, **kwargs)

                begin = time.monotonic()
                try:
                    with patch("builtins.__import__", side_effect=fail_sdk):
                        with self.assertRaisesRegex(RuntimeError, "sdk-import.*" + type(failure).__name__):
                            session.start()
                    self.assertLess(time.monotonic() - begin, 2)
                    self.assertTrue(session._closed_event.is_set())
                    self.assertFalse(session._started)
                    self.assertIsNone(session._lifecycle_future)
                    self.assertIsNone(bridge._thread)
                finally:
                    session.stop()
                    bridge.stop()

    def test_timed_out_start_unwinds_before_reusing_bridge(self):
        bridge = _AsyncBridge()
        session = _CuaDriverSession(bridge)
        session._STARTUP_TIMEOUT_SECONDS = 0.05

        async def stalled(self):
            self._startup_phase = "owned-test-initialize"
            self._shutdown_event = asyncio.Event()
            try:
                await asyncio.Event().wait()
            finally:
                self._closed_event.set()

        async def ready(self):
            self._startup_phase = "ready"
            self._shutdown_event = asyncio.Event()
            self._ready_event.set()
            try:
                await self._shutdown_event.wait()
            finally:
                self._session, self._started = None, False
                self._closed_event.set()

        try:
            session._lifecycle_coro = types.MethodType(stalled, session)
            with self.assertRaisesRegex(RuntimeError, "owned-test-initialize"):
                session.start()
            self.assertTrue(session._closed_event.is_set())
            self.assertIsNone(bridge._thread)
            self.assertIsNone(session._lifecycle_future)
            session._lifecycle_coro = types.MethodType(ready, session)
            session.start()
            self.assertTrue(session._started)
            self.assertEqual(session._transport_generation, 1)
        finally:
            session.stop()
            bridge.stop()
        self.assertTrue(session._closed_event.is_set())


if __name__ == "__main__":
    unittest.main()
