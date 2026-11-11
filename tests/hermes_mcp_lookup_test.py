#!/usr/bin/env python3
"""Exercise the shipped MCP patch with installed Hermes Python and owned scratch."""

from contextlib import ExitStack
import importlib
import logging
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
from unittest import mock


ROOT = Path(__file__).resolve().parents[1]
PATCH = ROOT / "nix/packages/hermes-mcp-lookup-discovery.patch"
SCRATCH = ROOT / ".loom-acceptance/hermes-mcp-lookup"
MODULES = ("hermes_cli.mcp_startup", "model_tools")


def load_patched_sources(directory):
    import hermes_cli

    source = Path(hermes_cli.__file__).resolve().parent.parent
    for name in MODULES:
        relative = Path(*name.split(".")).with_suffix(".py")
        target = directory / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(source / relative, target)
    command = ["patch", "--batch", "-p1", "-d", str(directory), "-i", str(PATCH)]
    forward = subprocess.run([*command, "--forward", "--dry-run"], capture_output=True, text=True)
    if forward.returncode == 0:
        subprocess.run([*command, "--forward"], check=True, capture_output=True, text=True)
    else:
        reverse = subprocess.run([*command, "--reverse", "--dry-run"], capture_output=True, text=True)
        if reverse.returncode != 0:
            raise RuntimeError("Native source no longer matches the reviewed MCP patch")
    for name in MODULES:
        module = importlib.import_module(name)
        path = directory / Path(*name.split(".")).with_suffix(".py")
        exec(compile(path.read_bytes(), str(path), "exec"), module.__dict__)


class McpLookupTest(unittest.TestCase):
    def setUp(self):
        from hermes_cli import mcp_startup
        from tools import mcp_tool_discovery
        from hermes_constants import set_hermes_home_override, reset_hermes_home_override

        self.stack = ExitStack()
        self.addCleanup(self.stack.close)
        self.home = Path(self.stack.enter_context(tempfile.TemporaryDirectory(dir=SCRATCH)))
        token = set_hermes_home_override(str(self.home))
        self.addCleanup(reset_hermes_home_override, token)
        self.startup = mcp_startup
        self.stack.enter_context(mock.patch.object(mcp_startup, "_mcp_discovery_started", set()))
        self.stack.enter_context(mock.patch.object(mcp_startup, "_mcp_discovery_thread", {}))
        self.stack.enter_context(mock.patch.object(mcp_startup, "_mcp_discovery_deferred", None))
        self.stack.enter_context(mock.patch.object(mcp_startup, "_mcp_server_filter", None))
        self.stack.enter_context(mock.patch.object(mcp_startup, "_has_configured_mcp_servers", return_value=True))
        self.stack.enter_context(mock.patch.object(mcp_startup, "_resolve_discovery_timeout", return_value=2))
        self.status = [{"name": "loom-mac", "status": "configured", "tools": 0, "connected": False}]
        self.stack.enter_context(mock.patch.object(mcp_tool_discovery, "get_mcp_status", side_effect=lambda: self.status))
        self.stack.enter_context(mock.patch.object(mcp_tool_discovery, "mcp_servers_awaiting_connect",
                                                   side_effect=lambda: [s["name"] for s in self.status if not s["connected"]]))

    def lookup(self, enabled=None, disabled=None):
        self.startup.ensure_mcp_discovery_for_tool_lookup(enabled_toolsets=enabled, disabled_toolsets=disabled)

    def connect(self, **kwargs):
        from hermes_constants import hermes_home_key
        self.assertEqual(hermes_home_key(), str(self.home))
        self.status[0].update(status="connected", tools=16, connected=True)

    def test_failed_startup_recovers_in_same_process_and_profile(self):
        log = logging.getLogger("test.mcp")
        with mock.patch.object(self.startup, "_discover_mcp_tools_without_interactive_oauth",
                               side_effect=RuntimeError("owned startup failure")):
            with self.assertLogs("test.mcp", level="WARNING") as recorded:
                self.startup.start_background_mcp_discovery(logger=log, thread_name="test-failed-start")
                self.assertTrue(self.startup.join_mcp_discovery(timeout=2))
            self.assertIn("Background MCP tool discovery failed", " ".join(recorded.output))
        with mock.patch.object(self.startup, "_discover_mcp_tools_without_interactive_oauth",
                               side_effect=self.connect) as discover:
            self.lookup(["loom-mac"])
            discover.assert_called_once_with(allowed_mcp_names=["loom-mac"])
        self.assertEqual(self.status[0]["tools"], 16)

    def test_healthy_and_lazy_catalogues_do_not_start_discovery(self):
        for state, connected in (("connected", True), ("lazy", False)):
            with self.subTest(state=state):
                self.status[0].update(status=state, connected=connected, tools=16)
                with mock.patch.object(self.startup, "start_background_mcp_discovery") as start:
                    self.lookup(["loom-mac"])
                    start.assert_not_called()

    def test_session_restrictions_and_disabled_servers_do_not_connect(self):
        cases = [(["terminal"], []), (["loom-mac"], ["loom-mac"]),
                 (["loom-mac"], ["mcp-loom-mac"]), ([], [])]
        for enabled, disabled in cases:
            with self.subTest(enabled=enabled, disabled=disabled):
                with mock.patch.object(self.startup, "start_background_mcp_discovery") as start:
                    self.lookup(enabled, disabled)
                    start.assert_not_called()
        self.status[0].update(status="disabled", disabled=True)
        with mock.patch.object(self.startup, "start_background_mcp_discovery") as start:
            self.lookup()
            start.assert_not_called()

    def test_process_allowlist_intersects_lookup_selection(self):
        from tools import mcp_tool_discovery

        self.startup.set_mcp_server_filter(["terminal"])
        with mock.patch.object(self.startup, "start_background_mcp_discovery") as start:
            self.lookup(["loom-mac"])
            start.assert_not_called()
        self.startup.set_mcp_server_filter(["loom-mac"])
        with mock.patch.object(mcp_tool_discovery, "discover_mcp_tools") as discover:
            self.startup._discover_mcp_tools_without_interactive_oauth(allowed_mcp_names=["loom-mac", "other"])
            discover.assert_called_once_with(allowed_mcp_names=["loom-mac"])

    def test_bridge_reads_catalogue_after_recovery_without_replacing_prompt(self):
        import model_tools
        from tools import tool_search

        definition = {"type": "function", "function": {"name": "mcp__loom_mac__list_windows",
                      "description": "List Mac windows", "parameters": {"type": "object", "properties": {}}}}
        def catalogue(**kwargs):
            self.assertTrue(kwargs["skip_tool_search_assembly"])
            return [definition] if self.status[0]["connected"] else []
        with mock.patch.object(self.startup, "_discover_mcp_tools_without_interactive_oauth", side_effect=self.connect):
            with mock.patch.object(model_tools, "get_tool_definitions", side_effect=catalogue):
                with mock.patch.object(tool_search, "dispatch_tool_describe", return_value="found") as describe:
                    result, underlying = model_tools._dispatch_bridge_tool(
                        tool_search.TOOL_DESCRIBE_NAME, {"names": [definition["function"]["name"]]},
                        ["loom-mac"], [])
        self.assertEqual(result, "found")
        self.assertIsNone(underlying)
        self.assertEqual(describe.call_args.kwargs["current_tool_defs"], [definition])

    def test_status_exception_does_not_permanently_suppress_retry(self):
        self.startup._mcp_discovery_started.add(str(self.home))
        with mock.patch.object(self.startup, "_any_mcp_connected", side_effect=RuntimeError("status failed")):
            with mock.patch.object(self.startup, "_discover_mcp_tools_without_interactive_oauth", side_effect=self.connect):
                with self.assertLogs("hermes_cli.mcp_startup", level="WARNING"):
                    self.lookup(["loom-mac"])
        self.assertTrue(self.status[0]["connected"])


if __name__ == "__main__":
    SCRATCH.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(dir=SCRATCH) as directory:
        load_patched_sources(Path(directory))
        unittest.main()
