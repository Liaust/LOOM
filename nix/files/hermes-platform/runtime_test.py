import unittest
from unittest.mock import patch
from urllib.error import HTTPError, URLError
import runtime


class Native:
    def __init__(self, busy=False):
        self.busy, self.draining, self.calls = busy, False, []

    def status(self):
        return {"idle": not self.busy, "gateway_running": True,
                "gateway_state": "draining" if self.draining else "running", "gateway_busy": self.busy}

    def call(self, path, body=None):
        self.calls.append((path, body))
        if path == "/api/gateway/drain":
            self.draining = body["action"] == "drain"
        return {"ok": True, "idle": True, "token": "native-lease"}


class ActivationTest(unittest.TestCase):
    def test_current_package_does_not_authenticate_or_mutate(self):
        outcome = runtime.activate(None, self.fail, "current", lambda: ["python", "current"], client_factory=self.fail)
        self.assertEqual(outcome, {"status": "current"})

    def test_stopped_scheduler_is_not_enabled(self):
        client, commands = Native(), []
        status = client.status
        client.status = lambda: dict(status(), gateway_running=False)
        outcome = runtime.activate(client, commands.append, "new", lambda: ["old"], scheduler_running=False)
        self.assertEqual(outcome["status"], "activated")
        self.assertEqual(commands, [["systemctl", "restart", runtime.BACKEND]])

    def test_replacement_uses_new_authenticated_client(self):
        before, after, commands = Native(), Native(), []
        factory = unittest.mock.Mock(side_effect=[before, after])
        outcome = runtime.activate(None, commands.append, "new", lambda: ["old"], client_factory=factory)
        self.assertEqual(outcome["status"], "activated")
        self.assertEqual(factory.call_count, 2)
        self.assertIn(("/api/health", None), after.calls)

    def test_failed_restart_reports_committed_state(self):
        def run(argv):
            if argv[1] == "restart":
                raise OSError("private command details")
        with self.assertRaises(runtime.RuntimeFailure) as raised:
            runtime.activate(Native(), run, "new", lambda: ["old"])
        self.assertTrue(raised.exception.committed)
        self.assertEqual(raised.exception.phase, "backend_restart")
        self.assertNotIn("private", str(raised.exception))

    def test_runtime_change_during_readiness_does_not_restart(self):
        command = unittest.mock.Mock(side_effect=[["old"], ["other"]])
        result = runtime.activate(Native(), self.fail, "new", command)
        self.assertEqual(result["reason"], "native_runtime_changed_during_readiness")

    def test_plugin_reload_uses_native_admission_even_for_current_binary(self):
        client, commands = Native(), []
        outcome = runtime.activate(client, commands.append, "current", lambda: ["current"], reload_plugins=True)
        self.assertEqual(outcome["status"], "activated")
        self.assertEqual([x[1] for x in commands], ["stop", "restart", "start"])

    def test_plugin_reload_does_not_interrupt_work(self):
        client, commands = Native(busy=True), []
        outcome = runtime.activate(client, commands.append, "current", lambda: ["current"], reload_plugins=True)
        self.assertEqual(outcome["status"], "deferred")
        self.assertEqual(commands, [])

    def test_plugin_reload_rejects_unexpected_runtime(self):
        client, commands = Native(), []
        outcome = runtime.activate(client, commands.append, "expected", lambda: ["another"], reload_plugins=True)
        self.assertEqual(outcome["reason"], "native_runtime_changed_before_plugin_reload")
        self.assertEqual(commands, [])

    def test_active_work_is_not_interrupted(self):
        client, commands = Native(busy=True), []
        outcome = runtime.activate(client, commands.append, "new", lambda: ["old"])
        self.assertEqual(outcome["status"], "deferred")
        self.assertEqual(commands, [])
        self.assertEqual(client.calls, [])

    def test_idle_clients_allow_fenced_activation(self):
        client, commands = Native(), []
        outcome = runtime.activate(client, commands.append, "new", lambda: ["old"])
        self.assertEqual(outcome["status"], "activated")
        self.assertEqual([x[1] for x in commands], ["stop", "restart", "start"])
        self.assertFalse(client.draining)
        self.assertIn(("/api/health/retirement", {"action": "commit", "token": "native-lease"}), client.calls)

    def test_admission_race_cancels_scheduler_drain(self):
        client = Native()
        call = client.call
        def dispatch(path, body=None):
            if path == "/api/health/retirement":
                return {"ok": False, "idle": False}
            return call(path, body)
        with patch.object(client, "call", side_effect=dispatch):
            self.assertEqual(runtime.activate(client, self.fail, "new", lambda: ["old"])["status"], "deferred")
        self.assertFalse(client.draining)


class ReadinessTest(unittest.TestCase):
    def test_connection_refusal_waits_for_listener(self):
        client = Native()
        factory = unittest.mock.Mock(side_effect=[URLError(ConnectionRefusedError()), client])
        with patch.object(runtime.time, "sleep"):
            self.assertIs(runtime.wait_for_client(factory, lambda: True, "startup"), client)
        self.assertEqual(factory.call_count, 2)

    def test_invalid_credentials_are_not_retried(self):
        factory = unittest.mock.Mock(side_effect=HTTPError("private", 401, "private", {}, None))
        with self.assertRaises(runtime.RuntimeFailure) as raised:
            runtime.wait_for_client(factory, lambda: True, "startup")
        self.assertEqual(raised.exception.reason, "authentication_refused")
        self.assertEqual(factory.call_count, 1)

    def test_transient_timeout_is_bounded(self):
        factory = unittest.mock.Mock(side_effect=TimeoutError())
        with patch.object(runtime.time, "monotonic", side_effect=[0, 60]):
            with self.assertRaises(runtime.RuntimeFailure) as raised:
                runtime.wait_for_client(factory, lambda: True, "startup")
        self.assertEqual(raised.exception.reason, "backend_not_ready")
        self.assertEqual(factory.call_count, 1)

    def test_failed_owner_does_not_wait(self):
        with self.assertRaises(runtime.RuntimeFailure) as raised:
            runtime.wait_for_client(self.fail, lambda: False, "startup")
        self.assertEqual(raised.exception.reason, "backend_owner_not_running")

    def test_invalid_health_is_not_retried(self):
        for health in ([], {}, {"status": "invented"}):
            with self.subTest(health=health):
                client = Native()
                client.call = lambda path: health
                factory = unittest.mock.Mock(return_value=client)
                with self.assertRaises(runtime.RuntimeFailure):
                    runtime.wait_for_client(factory, lambda: True, "startup")
                self.assertEqual(factory.call_count, 1)

    def test_unready_backend_defers_without_mutation(self):
        failure = runtime.RuntimeFailure("pre_activation", "backend_not_ready")
        with patch.object(runtime, "wait_for_client", side_effect=failure):
            outcome = runtime.activate(None, self.fail, "new", lambda: ["old"], client_factory=self.fail)
        self.assertEqual(outcome["status"], "deferred")
        self.assertEqual(outcome["phase"], "pre_activation")


if __name__ == "__main__":
    unittest.main()
