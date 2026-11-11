"""Select a validated native Hermes screen or physical login; no input engine."""
from __future__ import annotations

import json
import os
from pathlib import Path
import pwd
import re
import stat
import subprocess
import sys

GUI_KEYS = ("DISPLAY", "XAUTHORITY", "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS",
            "XDG_CURRENT_DESKTOP", "XDG_SESSION_TYPE", "XDG_SESSION_ID")
TARGET_SESSION = "CUA_DRIVER_RS_TARGET_LOGIND_SESSION"


def selected_session():
    if pwd.getpwuid(os.getuid()).pw_name != "agents":
        raise RuntimeError("Main computer use belongs to the agents account.")
    rows = json.loads(subprocess.check_output(
        ["loginctl", "list-sessions", "--json=short"], timeout=10))
    active = []
    for row in rows:
        if row.get("uid") != os.getuid() or row.get("class") != "user" or not row.get("seat"):
            continue
        sid = str(row["session"])
        if not re.fullmatch(r"[A-Za-z0-9_-]{1,32}", sid):
            raise RuntimeError("Invalid graphical session identifier.")
        properties = ("Id", "User", "Type", "Class", "Active", "LockedHint", "Display")
        raw = subprocess.check_output(["loginctl", "show-session", sid,
            *("--property=" + key for key in properties)], text=True, timeout=10)
        value = dict(line.split("=", 1) for line in raw.splitlines() if "=" in line)
        if value.get("Active") == "yes" and value.get("Type") in {"x11", "wayland"}:
            active.append(value)
    if len(active) != 1:
        raise RuntimeError("Log in to exactly one active agents graphical desktop on Main.")
    value = active[0]
    if value.get("User") != str(os.getuid()) or value.get("Class") != "user":
        raise RuntimeError("Main graphical session ownership changed.")
    if value.get("LockedHint") != "no":
        raise RuntimeError("Main's agents desktop is locked; ask the operator to unlock it.")
    if value.get("Type") != "x11":
        raise RuntimeError("Select Xfce Session at the agents login. GNOME 50 Wayland is unsupported by upstream Cua.")
    return value


def binding_path():
    root = Path(f"/run/user/{os.getuid()}/loom-agent-desktop")
    if root.exists():
        st = root.lstat()
        if not stat.S_ISDIR(st.st_mode) or st.st_uid != os.getuid() or stat.S_IMODE(st.st_mode) != 0o700:
            raise RuntimeError("Invalid private Main desktop binding directory.")
    return root / "x11-session.json"


def validate_environment(env, session):
    runtime = f"/run/user/{os.getuid()}"
    if (env.get("XDG_RUNTIME_DIR") != runtime or
            env.get("DBUS_SESSION_BUS_ADDRESS") != f"unix:path={runtime}/bus" or
            env.get("XDG_SESSION_TYPE") != "x11" or
            env.get("XDG_SESSION_ID") != session["Id"]):
        raise RuntimeError("Desktop environment does not belong to the current agents login.")
    display = env.get("DISPLAY", "")
    if not re.fullmatch(r":[0-9]+(?:\.[0-9]+)?", display):
        raise RuntimeError("Only the local agents X11 display is permitted.")
    if session.get("Display") and session["Display"] != display:
        raise RuntimeError("The recorded X11 display no longer matches the login.")
    if os.environ.get("DISPLAY") not in (None, "", display):
        raise RuntimeError("The caller selected another display, possibly Hermes Bot Desktop; refusing to substitute the physical Main desktop.")
    authority = Path(env.get("XAUTHORITY", ""))
    if not authority.is_absolute():
        raise RuntimeError("The graphical login did not supply its Xauthority path.")
    st = authority.lstat()
    if not stat.S_ISREG(st.st_mode) or st.st_uid != os.getuid() or stat.S_IMODE(st.st_mode) & 0o077:
        raise RuntimeError("The Xauthority file is not private to agents.")


def bind_session():
    if pwd.getpwuid(os.getuid()).pw_name != "agents":
        return
    session = selected_session()
    env = {key: os.environ[key] for key in GUI_KEYS if key in os.environ}
    env["XDG_SESSION_ID"] = session["Id"]
    env.setdefault("XAUTHORITY", str(Path.home() / ".Xauthority"))
    validate_environment(env, session)
    path = binding_path()
    path.parent.mkdir(mode=0o700, exist_ok=True)
    temporary = path.parent / (".binding-" + str(os.getpid()))
    with open(temporary, "x", opener=lambda p, flags: os.open(p, flags, 0o600)) as stream:
        json.dump({"session": session["Id"], "environment": env}, stream)
    os.replace(temporary, path)


def driver_environment():
    session = selected_session()
    fd = os.open(binding_path(), os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(fd) as stream:
        st = os.fstat(stream.fileno())
        if not stat.S_ISREG(st.st_mode) or st.st_uid != os.getuid() or stat.S_IMODE(st.st_mode) != 0o600 or st.st_size > 8192:
            raise RuntimeError("Invalid Main desktop environment binding.")
        binding = json.load(stream)
    if binding["session"] != session["Id"]:
        raise RuntimeError("Main login changed; normal Xfce autostart must bind the new session.")
    validate_environment(binding["environment"], session)
    env = {key: value for key, value in os.environ.items()
           if key not in (*GUI_KEYS, "WAYLAND_DISPLAY", "CUA_DRIVER_NATIVE_WAYLAND", "CUA_DRIVER_RS_NATIVE_WAYLAND")}
    env.update(binding["environment"])
    # Native Cua revalidates this login and its screensaver for each operation.
    # Do not inherit an ambient marker from a caller or stale service session.
    env[TARGET_SESSION] = session["Id"]
    env["CUA_DRIVER_RS_TELEMETRY_ENABLED"] = "0"
    return env


def bot_driver_environment(runtime=None):
    if pwd.getpwuid(os.getuid()).pw_name != "agents":
        raise RuntimeError("Main computer use belongs to the agents account.")
    if runtime is None:
        from tools.bot_desktop import runtime
    # Native Hermes checks the launcher's PID AND creation time before publishing
    # this profile's environment. Never substitute a physical seat when it is down.
    published = runtime.published_env()
    if not published:
        raise RuntimeError("This profile's native Bot Desktop is not running. Enable its auto-start or use Hermes Screen > Start; the physical desktop is not a fallback.")
    root = runtime.state_dir()
    info = root.lstat()
    if (not stat.S_ISDIR(info.st_mode) or info.st_uid != os.getuid()
            or stat.S_IMODE(info.st_mode) != 0o700):
        raise RuntimeError("Invalid private native Bot Desktop directory.")
    for path in (root / "env", root / "Xauthority"):
        info = path.lstat()
        if (not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid()
                or stat.S_IMODE(info.st_mode) != 0o600):
            raise RuntimeError("Invalid private native Bot Desktop binding.")
    if (root / "env").stat().st_size > 8192:
        raise RuntimeError("Native Bot Desktop environment exceeds its bound.")
    sock = root / "rfb.sock"
    info = sock.lstat()
    if (runtime.rfb_socket_path() != sock or not stat.S_ISSOCK(info.st_mode)
            or info.st_uid != os.getuid() or stat.S_IMODE(info.st_mode) != 0o600):
        raise RuntimeError("Invalid private native Bot Desktop socket.")
    display = published.get("DISPLAY", "")
    match = re.fullmatch(r":([0-9]+)(?:\.0)?", display)
    if not match or not 20 <= int(match[1]) <= 89:
        raise RuntimeError("Native Bot Desktop must own its private display band.")
    if os.environ.get("DISPLAY") not in (None, "", display):
        raise RuntimeError("Caller and native Bot Desktop select different screens.")
    if published.get("XAUTHORITY") != str(root / "Xauthority"):
        raise RuntimeError("Native Bot Desktop selected another Xauthority file.")
    bus = published.get("DBUS_SESSION_BUS_ADDRESS", "")
    if (not bus.startswith(("unix:path=", "unix:abstract="))
            or bus.split(",", 1)[0] == f"unix:path=/run/user/{os.getuid()}/bus"):
        raise RuntimeError("Native Bot Desktop requires its private session bus.")
    for key in ("XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME"):
        path = Path(published.get(key, ""))
        if not path.is_absolute() or not path.resolve().is_relative_to(root.resolve()):
            raise RuntimeError("Native Bot Desktop configuration escaped its profile.")
    keys = (*GUI_KEYS, "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME",
            "WAYLAND_DISPLAY", "SESSION_MANAGER", "CUA_DRIVER_NATIVE_WAYLAND",
            "CUA_DRIVER_RS_NATIVE_WAYLAND", TARGET_SESSION)
    env = {key: value for key, value in os.environ.items() if key not in keys}
    env.update({key: published[key] for key in
                ("DISPLAY", "XAUTHORITY", "DBUS_SESSION_BUS_ADDRESS",
                 "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME")})
    env.update(XDG_RUNTIME_DIR=f"/run/user/{os.getuid()}", XDG_CURRENT_DESKTOP="XFCE",
               XDG_SESSION_TYPE="x11", CUA_DRIVER_RS_TELEMETRY_ENABLED="0")
    return env


def main():
    args = sys.argv[1:] or ["--help"]
    if args == ["session"]:
        bind_session()
        return
    driver = os.environ["LOOM_CUA_BINARY"]
    if args == ["manifest"]:
        manifest = json.loads(subprocess.check_output([driver, "manifest"], timeout=10))
        # Hermes follows this pointer for MCP and private runtimes. Both retain
        # the same session binding without another tool or transport protocol.
        manifest["mcp_invocation"]["command"] = os.environ["LOOM_CUA_LAUNCHER"]
        print(json.dumps(manifest))
        return
    env = os.environ.copy()
    if args[0] in {"mcp", "serve", "call", "doctor"}:
        mode = os.environ.get("LOOM_DESKTOP_MODE", "physical")
        if mode not in ("physical", "bot"):
            raise RuntimeError("Unknown Main desktop target mode.")
        env = bot_driver_environment() if mode == "bot" else driver_environment()
    os.execve(driver, [driver, *args], env)


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, OSError, ValueError, KeyError, subprocess.SubprocessError) as exc:
        print("Main desktop unavailable: " + str(exc), file=sys.stderr)
        sys.exit(1)
