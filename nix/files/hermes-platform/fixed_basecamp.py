"""Explicit native Basecamp route using an operator-owned account binding."""
import argparse
import json
import os
from pathlib import Path
import stat
import subprocess
import sys


def prepare(binding, args, run=subprocess.run):
    if set(binding) != {"binary", "home", "config_home", "cache_home", "profile", "identity_id", "account_id"}:
        raise ValueError("incomplete fixed account binding")
    for key in ("binary", "home", "config_home", "cache_home"):
        if not isinstance(binding[key], str) or not Path(binding[key]).is_absolute() or "\0" in binding[key]:
            raise ValueError("absolute native account paths required")
    if (binding["profile"] != "codex" or type(binding["identity_id"]) is not int
            or type(binding["account_id"]) is not int
            or binding["identity_id"] <= 0 or binding["account_id"] <= 0):
        raise ValueError("unapproved Personal account binding")
    if args[:2] != ["--profile", "codex"] or len(args) < 3:
        raise ValueError("explicit --profile codex prefix required")
    args = args[2:]
    forbidden = ("--profile", "--account", "--config-dir", "--cache-dir", "--base-url", "--token")
    if any(arg in forbidden or any(arg.startswith(flag + "=") for flag in forbidden)
           or arg.startswith(("-a", "-P")) or
           (arg.startswith("-") and not arg.startswith("--") and any(c in arg[1:] for c in "aP"))
           for arg in args):
        raise ValueError("account override refused")
    if args[0].startswith("-") and args not in (["--help"], ["--version"]):
        raise ValueError("native command must follow the profile prefix")
    if args[0] in {"auth", "login", "logout", "config", "profile", "setup", "upgrade", "update", "migrate"}:
        raise ValueError("authentication/configuration remain operator-owned")
    env = dict(HOME=binding["home"], XDG_CONFIG_HOME=binding["config_home"],
               XDG_CACHE_HOME=binding["cache_home"], PATH="/run/current-system/sw/bin",
               LANG="C.UTF-8", BASECAMP_ACCOUNT_ID=str(binding["account_id"]), BASECAMP_NO_KEYRING="1",
               BASECAMP_NO_UPDATE_CHECK="1", DBUS_SESSION_BUS_ADDRESS="unix:path=/dev/null")
    argv = [binding["binary"], "--profile", "codex"]
    result = run(argv + ["me", "--agent"], env=env, cwd=binding["home"],
                 capture_output=True, text=True, timeout=20, check=True)
    actor = json.loads(result.stdout)
    selected = [account for account in actor.get("accounts", []) if account.get("current") is True]
    if (actor.get("identity", {}).get("id") != binding["identity_id"]
            or len(selected) != 1 or selected[0].get("id") != binding["account_id"]):
        raise ValueError("Personal identity/account mismatch")
    return argv + args, env


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binding", type=Path, required=True)
    parser.add_argument("args", nargs=argparse.REMAINDER)
    options = parser.parse_args()
    info = options.binding.lstat()
    if (not options.binding.is_absolute() or not stat.S_ISREG(info.st_mode)
            or info.st_uid not in (0, os.geteuid()) or info.st_mode & 0o077 or info.st_size > 65536):
        raise ValueError("unsafe account binding")
    binding = json.loads(options.binding.read_bytes())
    args = options.args[1:] if options.args[:1] == ["--"] else options.args
    argv, env = prepare(binding, args)
    os.chdir(binding["home"])
    os.execve(argv[0], argv, env)


if __name__ == "__main__":
    try:
        main()
    except Exception:
        print("Personal Basecamp route unavailable or account binding refused; no fallback was used.", file=sys.stderr)
        raise SystemExit(69)
