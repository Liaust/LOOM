"""Inspect native Hermes or activate a reviewed Nix runtime only when work is idle."""

import argparse
import errno
import fcntl
import http.cookiejar
import json
import os
from pathlib import Path
import subprocess
import time
from urllib.error import HTTPError, URLError
from urllib.request import HTTPCookieProcessor, ProxyHandler, Request, build_opener

BACKEND = "loom-hermes-platform.service"
SCHEDULER = "loom-hermes-scheduler.service"


class RuntimeFailure(Exception):
    def __init__(self, phase, reason, *, committed=False):
        super().__init__(reason)
        self.phase, self.reason, self.committed = phase, reason, committed


class Starting(Exception):
    pass


def transient(error):
    if isinstance(error, HTTPError):
        if error.code != 503:
            return False
        try:
            body = json.loads(error.read(4096))
            return isinstance(body, dict) and body.get("status") == "starting"
        except (ValueError, OSError):
            return False
    if isinstance(error, URLError):
        error = error.reason
    return isinstance(error, (Starting, TimeoutError, ConnectionError)) or (
        isinstance(error, OSError) and error.errno in
        (errno.ECONNREFUSED, errno.ECONNRESET, errno.ETIMEDOUT,
         errno.ENETUNREACH, errno.EHOSTUNREACH))


def wait_for_client(factory, owner_alive, phase, *, timeout=60):
    deadline = time.monotonic() + timeout
    while True:
        if not owner_alive():
            raise RuntimeFailure(phase, "backend_owner_not_running")
        try:
            client = factory(deadline)
            health = client.call("/api/health")
            if not isinstance(health, dict):
                raise ValueError("invalid health response")
            if health.get("status") == "starting":
                raise Starting()
            if health.get("ok") is False or health.get("status") in ("failed", "unhealthy", "error"):
                raise RuntimeFailure(phase, "backend_unhealthy")
            if health.get("ok") is not True and health.get("status") not in ("ok", "healthy", "ready"):
                raise ValueError("unrecognized health response")
            return client
        except Exception as error:
            if not transient(error):
                if isinstance(error, RuntimeFailure):
                    raise
                reason = "authentication_refused" if isinstance(error, HTTPError) and error.code in (401, 403) else "invalid_backend_response"
                raise RuntimeFailure(phase, reason) from error
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise RuntimeFailure(phase, "backend_not_ready") from error
            time.sleep(min(1, remaining))


class Client:
    def __init__(self, url, auth_file, *, deadline=None):
        self.url = url.rstrip("/")
        self.deadline = deadline
        self.opener = build_opener(ProxyHandler({}), HTTPCookieProcessor(http.cookiejar.CookieJar()))
        auth = json.loads(Path(auth_file).read_text())
        self.call("/auth/password-login", {"provider": "basic", "username": auth["username"],
                                          "password": auth["password"], "next": "/"})

    def call(self, path, body=None):
        request = Request(self.url + path,
                          data=None if body is None else json.dumps(body).encode(),
                          headers={"Content-Type": "application/json", "Origin": self.url})
        timeout = 20
        if self.deadline is not None:
            timeout = min(2, self.deadline - time.monotonic())
            if timeout <= 0:
                raise TimeoutError("readiness deadline expired")
        with self.opener.open(request, timeout=timeout) as response:
            raw = response.read((2 << 20) + 1)
        if len(raw) > 2 << 20:
            raise ValueError("native response exceeds bound")
        return json.loads(raw)

    def status(self):
        idle = self.call("/api/health/idle")
        state = self.call("/api/status")
        return {"idle": idle.get("idle"), "reason": idle.get("reason"),
                "gateway_running": state.get("gateway_running"),
                "gateway_state": state.get("gateway_state"),
                "gateway_busy": state.get("gateway_busy"),
                "active_agents": state.get("active_agents"), "version": state.get("version")}


def activate(client, run, expected_entry, read_command, *, reload_plugins=False,
             client_factory=None, owner_alive=lambda: True, scheduler_running=None):
    """Use both native admission owners. A diagnostic idle check alone is not a lease."""
    command = read_command()
    if reload_plugins and expected_entry not in command:
        return {"status": "deferred", "reason": "native_runtime_changed_before_plugin_reload"}
    if expected_entry in command and not reload_plugins:
        return {"status": "current"}
    if client_factory is not None:
        try:
            client = wait_for_client(client_factory, owner_alive, "pre_activation")
            client.deadline = None
        except RuntimeFailure as error:
            if error.reason == "backend_not_ready":
                return {"status": "deferred", "reason": error.reason, "phase": error.phase}
            raise
    state = client.status()
    if read_command() != command:
        return {"status": "deferred", "reason": "native_runtime_changed_during_readiness"}
    if state["idle"] is not True or (state["gateway_running"] is True and state["gateway_busy"] is not False):
        return {"status": "deferred", "reason": "native_work_active_or_unverified"}
    token, draining, committed, phase = None, False, False, "pre_activation"
    scheduler_was_running = state["gateway_running"] is True if scheduler_running is None else scheduler_running
    if not scheduler_was_running and state["gateway_running"] is True:
        return {"status": "deferred", "reason": "scheduler_owner_mismatch"}
    try:
        # Drain first closes scheduler admission; existing work is never interrupted.
        if state["gateway_running"] is True:
            if state["gateway_state"] not in ("running", "degraded"):
                return {"status": "deferred", "reason": "scheduler_has_another_lifecycle_owner"}
            draining = True
            client.call("/api/gateway/drain", {"action": "drain", "suppress_notification": True})
            for _ in range(8):
                state = client.status()
                if state["gateway_state"] == "draining" and state["gateway_busy"] is False:
                    break
                time.sleep(1)
            else:
                return {"status": "deferred", "reason": "scheduler_not_drained"}
        elif state["gateway_running"] is not False:
            return {"status": "deferred", "reason": "scheduler_state_unverified"}
        admission = client.call("/api/health/retirement", {"action": "prepare"})
        if admission.get("ok") is not True or admission.get("idle") is not True or not admission.get("token"):
            return {"status": "deferred", "reason": "native_admission_busy"}
        token = admission["token"]
        # Commit within the native 30-second lease; new turns remain fenced until replacement.
        if client.call("/api/health/retirement", {"action": "commit", "token": token}).get("ok") is not True:
            raise ValueError("native retirement commit refused")
        committed = True
        phase = "scheduler_stop"
        if scheduler_was_running:
            run(["systemctl", "stop", SCHEDULER])
        phase = "backend_restart"
        run(["systemctl", "restart", BACKEND])
        phase = "post_activation"
        try:
            client = wait_for_client(client_factory or (lambda deadline: client), owner_alive, "post_activation")
            client.deadline = None
            if draining:
                client.call("/api/gateway/drain", {"action": "cancel"})
        except RuntimeFailure as error:
            error.committed = True
            raise
        if scheduler_was_running:
            phase = "scheduler_resume"
            run(["systemctl", "start", SCHEDULER])
        return {"status": "activated"}
    except Exception as error:
        if committed:
            if isinstance(error, RuntimeFailure):
                error.committed = True
                raise
            raise RuntimeFailure(phase, "replacement_incomplete", committed=True) from error
        raise
    finally:
        if not committed:
            if token:
                client.call("/api/health/retirement", {"action": "cancel", "token": token})
            if draining:
                client.call("/api/gateway/drain", {"action": "cancel"})


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("status", "activate", "reload"))
    parser.add_argument("--url", default="http://10.44.0.2:9119")
    parser.add_argument("--state", default="/var/lib/loom/mina-hermes")
    parser.add_argument("--entry", default="")
    args = parser.parse_args()
    state = Path(args.state)
    if args.action == "status":
        client = Client(args.url, state / "client-auth.json")
        print(json.dumps(client.status()))
        return
    if os.getuid() != 0 or not args.entry.startswith("/nix/store/"):
        raise ValueError("activation requires the reviewed system owner and native entry")
    with Path("/run/loom-hermes-platform-activate.lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        def run(argv):
            subprocess.run(argv, check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=360)
        def command():
            pid = subprocess.check_output(["systemctl", "show", BACKEND, "-p", "MainPID", "--value"], text=True).strip()
            if not pid.isdecimal() or int(pid) <= 0:
                raise ValueError("native backend owner unavailable")
            return Path(f"/proc/{pid}/cmdline").read_bytes().decode().split("\0")
        def owner_alive():
            active = subprocess.check_output(["systemctl", "show", BACKEND, "-p", "ActiveState", "--value"], text=True, timeout=5).strip()
            return active in ("active", "activating", "reloading")
        scheduler_state = subprocess.check_output(["systemctl", "show", SCHEDULER, "-p", "ActiveState", "--value"], text=True, timeout=5).strip()
        if scheduler_state not in ("active", "inactive", "failed"):
            outcome = {"status": "deferred", "reason": "scheduler_has_another_lifecycle_owner"}
        else:
            outcome = activate(None, run, args.entry, command, reload_plugins=args.action == "reload",
                               client_factory=lambda deadline: Client(args.url, state / "client-auth.json", deadline=deadline),
                               owner_alive=owner_alive, scheduler_running=scheduler_state == "active")
        print(json.dumps(outcome))
        if outcome["status"] == "deferred" and args.action == "activate":
            run(["systemctl", "restart", "loom-hermes-platform-activate-retry.timer"])


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        result = {"status": "failed", "error": type(error).__name__}
        if isinstance(error, RuntimeFailure):
            result.update(phase=error.phase, reason=error.reason, replacement_committed=error.committed)
            if error.committed:
                result["next_action"] = "Inspect backend and scheduler before retrying activation."
        print(json.dumps(result))
        raise SystemExit(1)
