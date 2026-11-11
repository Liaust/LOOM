"""Keep the native Bot Desktop browser independent of automation-session cleanup."""
from __future__ import annotations

import fcntl
import os
import stat
import subprocess
import time

from tools.bot_desktop import browser, runtime


def ensure_started(*, exclude_session: str, env: dict[str, str], timeout: float = 10):
    published = runtime.published_env()
    if not published.get("DISPLAY"):
        raise RuntimeError("The native Bot Desktop is not running; no physical browser fallback.")
    root = runtime.state_dir()
    flags = os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW | os.O_CLOEXEC
    with os.fdopen(os.open(root / "browser-launch.lock", flags, 0o600), "r+") as lock:
        info = os.fstat(lock.fileno())
        if (not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid()
                or stat.S_IMODE(info.st_mode) != 0o600):
            raise RuntimeError("Invalid private Bot Desktop browser launch lock.")
        fcntl.flock(lock.fileno(), fcntl.LOCK_EX)
        profile = str(browser.profile_dir())
        port = browser.running_instance_cdp_port(profile, exclude_session=exclude_session)
        if port is not None:
            return port
        # Do not change a pre-existing daemon-owned browser into a CDP attach:
        # agent-browser would close its own browser during that config change.
        if browser.running_instance_cdp_port(profile) is not None:
            return None
        launch = browser.dock_launch()
        if not launch:
            raise RuntimeError("No native headed browser is installed for this Bot Desktop.")
        executable, user_data_dir = launch
        child_env = runtime.desktop_env(dict(env))
        if any(child_env.get(key) != published.get(key) for key in ("DISPLAY", "XAUTHORITY")):
            raise RuntimeError("Native Bot Desktop changed before browser launch; refusing another screen.")
        proc = subprocess.Popen(
            browser.dock_argv(executable, user_data_dir), env=child_env,
            stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
            start_new_session=True,
        )
        deadline = time.monotonic() + timeout
        while True:
            port = browser.running_instance_cdp_port(profile, exclude_session=exclude_session)
            if port is not None:
                return port
            if (code := proc.poll()) is not None:
                raise RuntimeError(f"The native shared Bot Desktop browser exited ({code}).")
            if time.monotonic() >= deadline:
                proc.terminate()
                try:
                    proc.wait(timeout=3)
                except subprocess.TimeoutExpired:
                    proc.kill()
                    proc.wait(timeout=3)
                raise RuntimeError("The native shared Bot Desktop browser did not publish its local endpoint.")
            time.sleep(0.1)
