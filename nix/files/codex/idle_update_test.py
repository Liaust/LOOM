import fcntl
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("idle_update", Path(__file__).with_name("idle_update.py"))
updater = importlib.util.module_from_spec(spec)
spec.loader.exec_module(updater)


class FakeRuntime(updater.Runtime):
    def __init__(self, home):
        super().__init__(home)
        self.codex_home.mkdir()
        self.binary.parent.mkdir(parents=True)
        self.binary.write_text("new CLI")
        self.old = self.codex_home / "old-codex"
        self.old.write_text("retained CLI")
        self.cli_version = self.server_version = self.managed_version = (0, 162, 0)
        self.selected = self.old
        self.calls = []
        self.idle_calls = 0
        self.busy_after = None
        self.fail_new = False

    def installed(self):
        return self.cli_version

    def daemon(self):
        return dict(backend="pid", status="running", managedCodexPath=str(self.selected),
                    managedCodexVersion=".".join(map(str, self.managed_version)),
                    appServerVersion=".".join(map(str, self.server_version)),
                    socketPath=str(self.codex_home / "app-server-control/app-server-control.sock"))

    def owner(self):
        return 42

    def idle(self):
        self.idle_calls += 1
        if self.busy_after is not None and self.idle_calls > self.busy_after:
            raise updater.Deferred("native_turn_active")
        return dict(server_pid=42, running_version=".".join(map(str, self.server_version)))

    def executing_binary(self, pid):
        return self.old

    def run(self, argv, timeout=45):
        self.calls.append(tuple(map(str, argv)))
        self.cli_version = (0, 162, 1)
        return ""

    def systemctl(self, *args):
        self.calls.append(args)
        if args in (("restart", updater.UNIT), ("start", updater.UNIT)):
            self.server_version = self.managed_version
        return ""

    def prepare(self, target):
        self.calls.append(("prepare", target))
        self.selected, self.managed_version = self.binary, target

    def select(self, binary):
        self.calls.append(("select", str(binary)))
        self.selected = Path(binary)
        self.managed_version = (0, 162, 0) if self.selected == self.old else (0, 162, 1)

    def verify(self, target):
        self.calls.append(("verify", target))
        if self.fail_new and target == (0, 162, 1):
            raise RuntimeError("unready")
        self.assert_version = self.server_version == target
        if not self.assert_version:
            raise RuntimeError("wrong version")


class IdleUpdateTest(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name).resolve()
        self.runtime = FakeRuntime(self.root)

    def update(self):
        with patch.object(updater, "process_blockers", return_value=[]):
            return updater.update(self.runtime, release=lambda: (0, 162, 1), quiet=lambda _: None)

    def test_current_version_no_activity_reads_restart_or_retry(self):
        self.runtime.cli_version = self.runtime.server_version = self.runtime.managed_version = (0, 162, 1)
        self.assertEqual(self.update()["status"], "current")
        self.assertEqual(self.runtime.idle_calls, 0)
        self.assertEqual(self.runtime.calls, [("stop", updater.RETRY)])

    def test_busy_does_not_install_or_stop_server_and_schedules_retry(self):
        self.runtime.busy_after = 0
        result = self.update()
        self.assertEqual(result["status"], "deferred")
        self.assertEqual(result["reason"], "native_turn_active")
        self.assertEqual(self.runtime.calls, [("restart", updater.RETRY)])

    def test_late_activity_preserves_running_server(self):
        self.runtime.busy_after = 2
        result = self.update()
        self.assertEqual(result["status"], "deferred")
        self.assertEqual(self.runtime.cli_version, (0, 162, 1))
        self.assertEqual(self.runtime.server_version, (0, 162, 0))
        self.assertEqual(self.runtime.managed_version, (0, 162, 1))
        self.assertNotIn(("stop", updater.UNIT), self.runtime.calls)
        self.assertNotIn(("restart", updater.UNIT), self.runtime.calls)

    def test_update_uses_owner_native_selection_and_retains_old(self):
        result = self.update()
        self.assertEqual(result["status"], "updated")
        self.assertTrue(self.runtime.old.exists())
        self.assertIn(("prepare", (0, 162, 1)), self.runtime.calls)
        self.assertNotIn(("select", str(self.runtime.binary)), self.runtime.calls)
        self.assertIn(("restart", updater.UNIT), self.runtime.calls)
        self.assertEqual(self.runtime.idle_calls, 3)
        self.assertEqual(self.runtime.calls[-1], ("stop", updater.RETRY))

    def test_failed_readiness_restores_old_and_holds_target(self):
        self.runtime.fail_new = True
        result = self.update()
        self.assertEqual(result["status"], "failed")
        self.assertEqual(result["reason"], "update_failed_previous_daemon_restored")
        self.assertEqual(self.runtime.server_version, (0, 162, 0))
        self.runtime.calls.clear()
        self.assertEqual(self.update()["reason"], "failed_release_held_for_review")
        self.assertEqual(self.runtime.calls, [("stop", updater.RETRY)])

    def test_shared_job_lease_defers_replacement(self):
        self.runtime.cli_version = (0, 162, 1)
        with (self.runtime.codex_home / updater.LEASE).open("a") as lock:
            fcntl.flock(lock, fcntl.LOCK_SH | fcntl.LOCK_NB)
            self.assertEqual(self.update()["reason"], "scheduled_codex_run_active")
        self.assertNotIn(("stop", updater.UNIT), self.runtime.calls)

    def test_prerelease_and_downgrade_are_refused(self):
        for value in ("0.163.0-alpha.1", "rust-v0.163.0-alpha.1", "latest", "0.162"):
            with self.subTest(value=value), self.assertRaises(ValueError):
                updater.version(value)
        self.runtime.cli_version = (0, 163, 0)
        self.assertEqual(self.update()["reason"], "automatic_downgrade_refused")
        self.assertNotIn(("stop", updater.UNIT), self.runtime.calls)

    def test_uncertain_native_check_is_deferred_not_failed(self):
        with patch.object(self.runtime, "_idle", side_effect=TimeoutError):
            with self.assertRaisesRegex(updater.Deferred, "idle_unverified:TimeoutError"):
                updater.Runtime.idle(self.runtime)

    def test_loaded_pages_and_active_or_unknown_background_states(self):
        calls = []

        def rpc(method, params):
            calls.append((method, params))
            if method == "thread/loaded/list":
                return dict(data=["two"] if params["cursor"] else ["one"], nextCursor=None if params["cursor"] else "next")
            if method == "thread/read":
                return dict(thread={"status": {"type": "idle"}})
            return dict(data=[], nextCursor=None)

        self.assertEqual(updater.inspect_threads(rpc), 2)
        self.assertTrue(all(p.get("includeTurns") is False for m, p in calls if m == "thread/read"))
        for state in ("active", "systemError", "future-status"):
            def bad(method, params):
                return dict(data=["one"]) if method == "thread/loaded/list" else dict(thread={"status": {"type": state}})
            with self.subTest(state=state), self.assertRaises(updater.Deferred):
                updater.inspect_threads(bad)
        for response in ({}, {"data": [dict(processId="42")]}, {"data": [], "nextCursor": "more"}):
            with patch.object(updater, "inspect_threads", wraps=updater.inspect_threads):
                def background(method, params):
                    if method == "thread/queue/list":
                        return dict(data=[], nextCursor=None)
                    return response if method == "thread/backgroundTerminals/list" else (dict(data=["one"]) if method == "thread/loaded/list" else dict(thread={"status": {"type": "idle"}}))
                with self.assertRaises(updater.Deferred):
                    updater.inspect_threads(background)

        def queued(method, params):
            if method == "thread/loaded/list":
                return dict(data=["one"])
            if method == "thread/read":
                return dict(thread={"status": {"type": "idle"}})
            return dict(data=[dict(id="pending")], nextCursor=None)
        with self.assertRaisesRegex(updater.Deferred, "native_work_queued"):
            updater.inspect_threads(queued)

    def test_staged_selection_after_deferral_resumes_without_redownload(self):
        self.runtime.cli_version = self.runtime.managed_version = (0, 162, 1)
        self.runtime.selected = self.runtime.binary
        self.assertEqual(self.update()["status"], "updated")
        self.assertNotIn(("prepare", (0, 162, 1)), self.runtime.calls)
        self.assertIn(("restart", updater.UNIT), self.runtime.calls)

    def test_rollback_does_not_stop_new_work(self):
        self.runtime.fail_new = True
        self.runtime.busy_after = 3
        result = self.update()
        self.assertEqual(result["status"], "failed")
        self.assertEqual(result["reason"], "rollback_deferred_for_new_activity")
        self.assertNotIn(("stop", updater.UNIT), self.runtime.calls)
        self.assertEqual(self.runtime.calls.count(("restart", updater.UNIT)), 1)

    def test_idle_clients_are_not_work_but_exec_and_competing_servers_block(self):
        proc = self.root / "proc"
        proc.mkdir()
        for pid, argv, name in ((42, ["codex", "app-server"], "codex"),
                                (43, ["codex", "app-server", "proxy"], "codex"),
                                (44, ["codex"], "codex"),
                                (45, ["python3", "/nix/store/hash-bridge.py", "serve"], "python3"),
                                (46, ["codex-code-mode-host"], "codex-code-mode"),
                                (47, ["codex", "exec", "--json"], "codex"),
                                (48, ["codex", "-c", "model=\"gpt-6.1-sol\"", "review"], "codex"),
                                (49, ["codex", "app-server", "--listen", "unix://"], "codex"),
                                (50, ["codex", "-c", "features.code_mode_host=true", "app-server", "proxy"], "codex")):
            entry = proc / str(pid)
            entry.mkdir()
            (entry / "comm").write_text(name)
            (entry / "cmdline").write_bytes(b"\0".join(x.encode() for x in argv) + b"\0")
            (entry / "status").write_text("PPid:\t42\n")
        self.assertEqual(updater.process_blockers(42, proc), [45, 47, 48, 49])
        with patch.object(Path, "read_bytes", side_effect=PermissionError):
            with self.assertRaisesRegex(updater.Deferred, "inventory_unverified"):
                updater.process_blockers(42, proc)

    def test_daemon_only_preparation_keeps_native_settings_and_exact_stable_target(self):
        with patch.object(self.runtime, "run", return_value="installer source") as run, patch.object(self.runtime, "daemon", return_value={"managedCodexVersion": "0.162.1"}):
            updater.Runtime.prepare(self.runtime, (0, 162, 1))
        commands = run.call_args_list
        self.assertEqual(commands[0].args[0][0], "curl")
        self.assertEqual(commands[1].args[0], ["sh", "-s", "--", "--release", "0.162.1"])
        self.assertEqual(commands[1].kwargs["extra_env"], {"CODEX_INSTALL_DAEMON_ONLY": "1", "CODEX_NON_INTERACTIVE": "1"})

    def test_external_owner_after_reconnect_is_never_stopped(self):
        self.runtime.cli_version = self.runtime.managed_version = (0, 162, 1)
        self.runtime.fail_new = True
        with patch.object(self.runtime, "daemon", return_value={"status": "running"}):
            with self.assertRaisesRegex(RuntimeError, "external_owner_preserved"):
                self.runtime.replace((0, 162, 1), self.runtime.old, (0, 162, 0))
        self.assertNotIn(("stop", updater.UNIT), self.runtime.calls)

    def test_idle_recheck_accepts_staged_package_without_confusing_running_binary(self):
        executing = self.runtime.codex_home / "packages/app-server-daemon/releases/old/bin/codex"
        executing.parent.mkdir(parents=True)
        executing.write_text("retained executable")
        with patch.object(self.runtime, "executing_binary", return_value=executing), patch.object(self.runtime, "run", return_value="codex-cli 0.162.0"), patch.object(updater, "native_idle", return_value=2), patch.object(updater, "process_blockers", return_value=[]):
            self.runtime.managed_version = (0, 162, 1)
            result = updater.Runtime._idle(self.runtime)
        self.assertEqual(result["running_version"], "0.162.0")
        self.assertEqual(result["loaded_threads"], 2)


if __name__ == "__main__":
    unittest.main()
