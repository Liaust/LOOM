"""Stable native Codex updates only in an observed quiet window."""

import argparse
from contextlib import contextmanager
from datetime import datetime, timezone
import fcntl
import json
import os
from pathlib import Path
import pwd
import re
import subprocess
import tempfile
import time
from urllib.request import Request, urlopen

UNIT = "loom-codex-app-server.service"
RETRY = "loom-codex-update-retry.timer"
LATEST = "https://api.github.com/repos/openai/codex/releases/latest"
INSTALLER = "https://chatgpt.com/codex/install.sh"
LEASE = "loom-runtime-update.lock"


class Deferred(Exception):
    pass


def version(value):
    match = re.fullmatch(r"(?:codex-cli |rust-v)?(\d+\.\d+\.\d+)", value.strip())
    if not match:
        raise ValueError("stable_version_unverified")
    return tuple(map(int, match[1].split(".")))


def latest_release():
    request = Request(LATEST, headers={"User-Agent": "loom-codex-idle-updater", "Accept": "application/vnd.github+json"})
    with urlopen(request, timeout=15) as response:
        payload = response.read(2 * 1024 * 1024 + 1)
    if len(payload) > 2 * 1024 * 1024:
        raise ValueError("release_metadata_too_large")
    release = json.loads(payload)
    if release.get("draft") is not False or release.get("prerelease") is not False:
        raise ValueError("release_not_stable")
    return version(release["tag_name"])


def inspect_threads(call):
    cursor, seen, threads = None, set(), []
    while True:
        page = call("thread/loaded/list", {"limit": 100, "cursor": cursor})
        if not isinstance(page.get("data"), list) or not all(isinstance(x, str) for x in page["data"]):
            raise Deferred("thread_inventory_unverified")
        threads.extend(page["data"])
        cursor = page.get("nextCursor")
        if not cursor:
            break
        if cursor in seen or len(threads) > 10000:
            raise Deferred("thread_inventory_incomplete")
        seen.add(cursor)
    for thread_id in set(threads):
        thread = call("thread/read", {"threadId": thread_id, "includeTurns": False})["thread"]
        state = thread.get("status", {}).get("type")
        if state == "active":
            raise Deferred("native_turn_active")
        if state not in ("idle", "notLoaded"):
            raise Deferred("thread_status_unverified")
        if state == "idle":
            for method, reason in (("thread/queue/list", "native_work_queued"),
                                   ("thread/backgroundTerminals/list", "background_terminal_active")):
                pending = call(method, {"threadId": thread_id, "limit": 1})
                if not isinstance(pending.get("data"), list):
                    raise Deferred("pending_activity_unverified")
                if pending["data"] or pending.get("nextCursor"):
                    raise Deferred(reason)
    return len(set(threads))


def native_idle(socket_path):
    from websockets.sync.client import unix_connect

    deadline = time.monotonic() + 30
    with unix_connect(str(socket_path), open_timeout=5, close_timeout=1, max_size=2 * 1024 * 1024) as connection:
        request_id = 0

        def call(method, params):
            nonlocal request_id
            request_id += 1
            connection.send(json.dumps({"id": request_id, "method": method, "params": params}))
            while True:
                remaining = min(5, deadline - time.monotonic())
                if remaining <= 0:
                    raise Deferred("native_idle_check_timeout")
                message = json.loads(connection.recv(timeout=remaining))
                if message.get("id") == request_id:
                    if "error" in message or not isinstance(message.get("result"), dict):
                        raise Deferred("native_idle_check_unavailable")
                    return message["result"]

        call("initialize", {"clientInfo": {"name": "loom-codex-idle-updater", "version": "1.0"},
                            "capabilities": {"experimentalApi": True}})
        connection.send(json.dumps({"method": "initialized"}))
        return inspect_threads(call)


def process_blockers(server_pid, proc=Path("/proc"), uid=None):
    uid = os.getuid() if uid is None else uid
    blockers = []
    for entry in proc.iterdir():
        if not entry.name.isdigit():
            continue
        try:
            if entry.stat().st_uid != uid:
                continue
            argv = [x.decode(errors="replace") for x in (entry / "cmdline").read_bytes().split(b"\0") if x]
            name = (entry / "comm").read_text().strip()
            if not argv or int(entry.name) == server_pid:
                continue
            # Clients aren't work. Native RPC covers shared-server activity;
            # independent interactive sessions keep their immutable executable.
            command = codex_command(argv) if name == "codex" else ""
            if command in ("exec", "e", "review") or ("serve" in argv and any(x.endswith("bridge.py") for x in argv)):
                blockers.append(int(entry.name))
            elif command == "app-server":
                offset = argv.index("app-server")
                if argv[offset + 1:offset + 2] != ["proxy"]:
                    blockers.append(int(entry.name))
        except FileNotFoundError:
            continue
        except (OSError, ValueError, StopIteration) as error:
            raise Deferred("process_inventory_unverified") from error
    return sorted(blockers)


def codex_command(argv):
    value_options = {"-c", "--config", "-m", "--model", "-p", "--profile", "-C", "--cd",
                     "-i", "--image", "-s", "--sandbox", "-a", "--ask-for-approval",
                     "--remote", "--remote-auth-token-env", "--enable", "--disable", "--local-provider"}
    arguments = iter(argv[1:])
    for argument in arguments:
        if argument == "--":
            break
        if argument in value_options:
            next(arguments, None)
        elif not argument.startswith("-"):
            return argument
    return ""


@contextmanager
def exclusive_lease(home):
    fd = os.open(home / LEASE, os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, "a") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as error:
            raise Deferred("scheduled_codex_run_active") from error
        yield


class Runtime:
    def __init__(self, home):
        self.home = Path(home)
        self.codex_home = self.home / ".codex"
        self.binary = self.home / ".local/bin/codex"
        self.status_path = self.codex_home / "loom-updates/status.json"
        self.env = dict(os.environ, HOME=str(self.home), CODEX_HOME=str(self.codex_home),
                        PATH=f"{self.home}/.local/bin:/etc/profiles/per-user/agents/bin:/run/current-system/sw/bin")

    def run(self, argv, timeout=45, input=None, extra_env=None):
        result = subprocess.run(list(map(str, argv)), cwd=self.home, env=dict(self.env, **(extra_env or {})),
                                input=input, capture_output=True, text=True, timeout=timeout)
        if result.returncode:
            raise RuntimeError("command_failed:" + Path(str(argv[0])).name)
        return result.stdout

    def systemctl(self, *args):
        return self.run(["systemctl", "--user", *args])

    def installed(self):
        return version(self.run([self.binary, "--version"]))

    def daemon(self):
        return json.loads(self.run([self.binary, "app-server", "daemon", "version"]))

    def owner(self):
        fields = dict(line.split("=", 1) for line in self.systemctl("show", UNIT, "-p", "ActiveState", "-p", "MainPID").splitlines())
        if fields.get("ActiveState") != "active" or int(fields.get("MainPID", "0")) <= 0:
            raise Deferred("managed_server_not_active")
        return int(fields["MainPID"])

    def idle(self):
        try:
            return self._idle()
        except Deferred:
            raise
        except Exception as error:
            raise Deferred("idle_unverified:" + type(error).__name__) from error

    def _idle(self):
        pid = self.owner()
        daemon = self.daemon()
        if daemon.get("backend") != "pid" or daemon.get("status") != "running":
            raise Deferred("server_owner_unverified")
        executing = self.executing_binary(pid)
        packages = (self.codex_home / "packages/app-server-daemon/releases").resolve(strict=True)
        if (not executing.is_relative_to(packages)
                or version(self.run([executing, "--version"])) != version(daemon["appServerVersion"])):
            raise Deferred("server_binary_unverified")
        loaded = native_idle(daemon["socketPath"])
        if process_blockers(pid):
            raise Deferred("codex_execution_or_competing_server")
        if self.owner() != pid:
            raise Deferred("server_owner_changed")
        return {"server_pid": pid, "loaded_threads": loaded, "running_version": daemon["appServerVersion"]}

    def executing_binary(self, pid):
        return Path(f"/proc/{pid}/exe").resolve(strict=True)

    def prepare(self, target):
        script = self.run(["curl", "-fsSL", "--max-time", "30", INSTALLER], timeout=35)
        self.run(["sh", "-s", "--", "--release", ".".join(map(str, target))], input=script,
                 extra_env={"CODEX_INSTALL_DAEMON_ONLY": "1", "CODEX_NON_INTERACTIVE": "1"}, timeout=180)
        if version(self.daemon()["managedCodexVersion"]) != target:
            raise RuntimeError("prepared_daemon_version_unverified")

    def select(self, binary):
        self.run([binary, "app-server", "daemon", "update", "--from-cli", "--yes"], timeout=60)

    def verify(self, target):
        daemon = self.daemon()
        if (daemon.get("backend") != "pid" or daemon.get("status") != "running"
                or version(daemon["managedCodexVersion"]) != target
                or version(daemon["appServerVersion"]) != target):
            raise RuntimeError("daemon_version_unverified")
        pid = self.owner()
        if Path(f"/proc/{pid}/exe").resolve(strict=True) != Path(daemon["managedCodexPath"]).resolve(strict=True):
            raise RuntimeError("daemon_owner_unverified")
        return daemon

    def replace(self, target, old_binary, old_version):
        try:
            # Package preparation is complete; only the fast owner transition
            # occurs while connected clients need to reconnect.
            self.systemctl("restart", UNIT)
            self.verify(target)
        except Exception as error:
            # Only restore when no external server/turn took over the socket.
            daemon = self.daemon()
            if daemon.get("status") == "running" and daemon.get("backend") != "pid":
                raise RuntimeError("update_failed_external_owner_preserved") from error
            if daemon.get("status") == "running":
                try:
                    self.idle()
                except Deferred as busy:
                    raise RuntimeError("rollback_deferred_for_new_activity") from busy
            self.systemctl("stop", UNIT)
            if process_blockers(0):
                raise RuntimeError("rollback_deferred_for_new_client") from error
            self.select(old_binary)
            self.systemctl("start", UNIT)
            self.verify(old_version)
            raise RuntimeError("update_failed_previous_daemon_restored") from error

    def save(self, result):
        self.status_path.parent.mkdir(mode=0o700, exist_ok=True)
        fd, temporary = tempfile.mkstemp(prefix=".status-", dir=self.status_path.parent)
        try:
            with os.fdopen(fd, "w") as output:
                json.dump(result, output)
                output.write("\n")
                output.flush()
                os.fsync(output.fileno())
            os.replace(temporary, self.status_path)
        finally:
            if os.path.exists(temporary):
                os.unlink(temporary)

    def finish(self, result):
        result["observed_at"] = datetime.now(timezone.utc).isoformat()
        self.save(result)
        self.systemctl("restart" if result["status"] == "deferred" else "stop", RETRY)
        return result


def update(runtime, release=latest_release, quiet=time.sleep):
    target = None
    try:
        target = release()
        installed, daemon = runtime.installed(), runtime.daemon()
        if daemon.get("backend") != "pid" or daemon.get("status") != "running":
            raise Deferred("managed_server_not_running")
        runtime.owner()
        selected = version(daemon["managedCodexVersion"])
        served = version(daemon.get("appServerVersion", ""))
        previous = json.loads(runtime.status_path.read_text()) if runtime.status_path.exists() else {}
        label = ".".join(map(str, target))
        if previous.get("status") == "failed" and previous.get("target") == label:
            return runtime.finish(dict(previous, reason="failed_release_held_for_review"))
        if max(installed, selected, served) > target:
            raise ValueError("automatic_downgrade_refused")
        if installed == selected == served == target:
            return runtime.finish({"status": "current", "target": label})
        runtime.idle()
        if installed < target:
            runtime.run([runtime.binary, "update"], timeout=180)
        if runtime.installed() != target:
            raise ValueError("installed_stable_release_changed")
        with exclusive_lease(runtime.codex_home):
            current = runtime.daemon()
            if (version(current["managedCodexVersion"]) == target
                    and version(current.get("appServerVersion", "")) == target):
                runtime.verify(target)
                return runtime.finish({"status": "current", "target": label})
            first = runtime.idle()
            old_binary = runtime.executing_binary(first["server_pid"])
            served = version(first["running_version"])
            if version(current["managedCodexVersion"]) != target:
                runtime.prepare(target)
            quiet(5)
            if runtime.idle()["server_pid"] != first["server_pid"]:
                raise Deferred("server_changed_during_quiet_window")
            runtime.replace(target, old_binary, served)
        return runtime.finish({"status": "updated", "target": label, "previous": ".".join(map(str, served))})
    except Deferred as error:
        return runtime.finish({"status": "deferred", "reason": str(error), "target": ".".join(map(str, target)) if target else None})
    except Exception as error:
        # Avoid provider stderr, paths, private thread previews and credentials.
        reason = str(error) if isinstance(error, (RuntimeError, ValueError)) else type(error).__name__
        return runtime.finish({"status": "failed", "reason": reason, "target": ".".join(map(str, target)) if target else None})


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check-idle", action="store_true", help="read-only native/process activity check")
    args = parser.parse_args()
    if os.getuid() != pwd.getpwnam("agents").pw_uid:
        raise SystemExit("run as the existing agents identity")
    runtime = Runtime("/home/agents")
    if args.check_idle:
        try:
            result = dict(runtime.idle(), status="idle")
        except Exception as error:
            result = {"status": "deferred", "reason": str(error) if isinstance(error, Deferred) else type(error).__name__}
    else:
        result = update(runtime)
    print(json.dumps(result))
    return 1 if result["status"] == "failed" else 0


if __name__ == "__main__":
    raise SystemExit(main())
