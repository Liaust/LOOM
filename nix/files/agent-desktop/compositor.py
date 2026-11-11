"""Session-bound Picom ownership; always give Xfwm its compositor back on exit."""
import argparse
import fcntl
import json
import os
from pathlib import Path
import pwd
import signal
import subprocess
import sys

from session_launcher import bind_session, binding_path, driver_environment


UNIT = "loom-agent-compositor.service"


def state_directory():
    directory = Path.home() / ".local/state/loom-agent-desktop"
    directory.mkdir(parents=True, mode=0o700, exist_ok=True)
    return directory


def write_state(path, value):
    temporary = path.with_suffix(".tmp")
    with open(temporary, "w", opener=lambda p, f: os.open(p, f, 0o600)) as output:
        json.dump(value, output)
        output.flush()
        os.fsync(output.fileno())
    temporary.replace(path)


def setting(value=None):
    command = ["xfconf-query", "-c", "xfwm4", "-p", "/general/use_compositing"]
    if value is not None:
        command += ["--set", value]
    return subprocess.check_output(command, text=True, timeout=5,
                                   env=os.environ | {"DBUS_SESSION_BUS_ADDRESS":
                                       f"unix:path=/run/user/{os.getuid()}/bus"}).strip()


def recover():
    path = state_directory() / "picom-v1.json"
    if not path.exists():
        return
    state = json.loads(path.read_text())
    if state.get("active"):
        if setting() == "false":
            setting(state["before"])
        state["active"] = False
        write_state(path, state)


def run_compositor():
    env = driver_environment()
    if "XFCE" not in env.get("XDG_CURRENT_DESKTOP", "").upper().split(":"):
        raise RuntimeError("Picom belongs only to the agents Xfce session")
    with (binding_path().parent / "compositor.lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        before = setting()
        if before != "true":
            raise RuntimeError("Xfwm compositing is not active; refusing an unknown compositor handover")
        write_state(state_directory() / "picom-v1.json",
                    dict(before=before, active=True, session=env["XDG_SESSION_ID"]))
        child = None
        stopping = False

        def stop(_signal, _frame):
            nonlocal stopping
            stopping = True
            if child is not None and child.poll() is None:
                child.terminate()

        signal.signal(signal.SIGTERM, stop)
        signal.signal(signal.SIGINT, stop)
        try:
            setting("false")
            if stopping:
                return 0
            child = subprocess.Popen([os.environ["LOOM_PICOM_BINARY"], "--config",
                                      os.environ["LOOM_PICOM_CONFIG"]], env=env)
            result = child.wait()
            return 0 if stopping else result
        finally:
            if child is not None and child.poll() is None:
                child.terminate()
                try:
                    child.wait(timeout=3)
                except subprocess.TimeoutExpired:
                    child.kill()
                    child.wait(timeout=3)
            recover()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=["autostart", "start", "restore", "run", "recover"])
    args = parser.parse_args()
    if pwd.getpwuid(os.getuid()).pw_name != "agents":
        if args.action == "autostart":
            return 0
        raise RuntimeError("This compositor belongs only to agents")
    disabled = state_directory() / "picom-disabled"
    if args.action == "recover":
        recover()
    elif args.action == "run":
        return run_compositor()
    elif args.action == "restore":
        disabled.touch(mode=0o600, exist_ok=True)
        subprocess.run(["systemctl", "--user", "stop", UNIT], check=True, timeout=25,
                       env=os.environ | {"XDG_RUNTIME_DIR": f"/run/user/{os.getuid()}"})
        recover()
    else:
        if args.action == "autostart":
            if disabled.exists():
                return 0
            bind_session()
        driver_environment()
        disabled.unlink(missing_ok=True)
        subprocess.run(["systemctl", "--user", "start", UNIT], check=True, timeout=20,
                       env=os.environ | {"XDG_RUNTIME_DIR": f"/run/user/{os.getuid()}"})
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (RuntimeError, OSError, ValueError, KeyError, subprocess.SubprocessError) as error:
        print("Agent compositor: " + str(error), file=sys.stderr)
        sys.exit(1)
