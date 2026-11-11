"""Credential-selection launcher, not a replacement for native business CLIs."""
import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import sys


def environment(context):
    # A fresh vector avoids token/config/keyring precedence over selected stores.
    env = dict(context["environment"])
    if not {"HOME", "PATH", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "GH_CONFIG_DIR", "GIT_CONFIG_GLOBAL", "GIT_SSH_COMMAND"} <= set(env):
        raise ValueError("company tool context unavailable")
    if any(not isinstance(k, str) or not isinstance(v, str) or "\0" in k + v for k, v in env.items()):
        raise ValueError("invalid company tool environment")
    paths = env["PATH"].split(":")
    if any(not p or not Path(p).is_absolute() for p in paths):
        raise ValueError("absolute company tool PATH required")
    env["PATH"] = ":".join(p for p in paths if p not in ("/etc/profiles/per-user/agents/bin", "/home/agents/.local/bin"))
    env.update(LANG="C.UTF-8", BASECAMP_NO_KEYRING="1", GH_PROMPT_DISABLED="1",
               DBUS_SESSION_BUS_ADDRESS="unix:path=/dev/null")
    return env


def prepare(context, tool, args, run=subprocess.run):
    if tool not in ("basecamp", "gh", "git"):
        raise ValueError("unsupported native business tool")
    binary = context.get("commands", {}).get(tool)
    if not isinstance(binary, str) or not Path(binary).is_absolute():
        raise ValueError("native company tool unavailable")
    env = environment(context)
    if tool == "basecamp":
        binding = context.get("basecamp")
        if (not isinstance(binding, dict) or set(binding) != {"profile", "identity_id", "account_id"}
                or not re.fullmatch(r"[a-zA-Z0-9_-]+", binding.get("profile", ""))
                or binding["profile"] in ("mina", "codex", "morathustra", "default")
                or type(binding["identity_id"]) is not int or binding["identity_id"] <= 0
                or type(binding["account_id"]) is not int or binding["account_id"] <= 0
                or binding["account_id"] in context.get("forbidden_basecamp_account_ids", [])):
            raise ValueError("company Basecamp human binding unavailable")
        args = list(args)
        if args[:2] == ["--profile", binding["profile"]]:
            args = args[2:]
        # Only selectors are fenced; the native command/flags are otherwise intact.
        if any(a in ("--profile", "--account", "-P", "-a", "--config-dir") or a.startswith(("--profile=", "--account=", "--config-dir=", "-P", "-a")) for a in args):
            raise ValueError("company Basecamp identity override refused")
        env["BASECAMP_ACCOUNT_ID"] = str(binding["account_id"])
        argv = [binary, "--profile", binding["profile"]]
        result = run(argv + ["me", "--agent"], env=env, capture_output=True, text=True, timeout=20, check=True)
        actor = json.loads(result.stdout)
        current = [a for a in actor.get("accounts", []) if a.get("current") is True]
        if actor.get("identity", {}).get("id") != binding["identity_id"] or len(current) != 1 or current[0].get("id") != binding["account_id"]:
            raise ValueError("company Basecamp actor/account mismatch")
        return argv + args, env
    if tool == "gh":
        binding = context.get("github")
        if (not isinstance(binding, dict) or set(binding) != {"host", "login", "id"}
                or not re.fullmatch(r"[A-Za-z0-9.-]+", binding.get("host", ""))
                or not re.fullmatch(r"[A-Za-z0-9_-]+", binding.get("login", ""))
                or type(binding.get("id")) is not int or binding["id"] <= 0):
            raise ValueError("company GitHub host/account binding unavailable")
        host = binding["host"]
        for index, arg in enumerate(args):
            if arg.startswith("--hostname=") and arg != "--hostname=" + host:
                raise ValueError("company GitHub host override refused")
            if arg == "--hostname" and (index + 1 == len(args) or args[index + 1] != host):
                raise ValueError("company GitHub host override refused")
        env["GH_HOST"] = host
        result = run([binary, "api", "user", "--hostname", host], env=env,
                     capture_output=True, text=True, timeout=20, check=True)
        actor = json.loads(result.stdout)
        if actor.get("login") != binding["login"] or actor.get("id") != binding["id"]:
            raise ValueError("company GitHub actor mismatch")
    return [binary, *args], env


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--context", type=Path, required=True)
    parser.add_argument("tool")
    parser.add_argument("args", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    if args.context.is_symlink() or args.context.stat().st_size > 65536:
        raise ValueError("unsafe company tool binding")
    context = json.loads(args.context.read_text())
    argv, env = prepare(context, args.tool, args.args)
    os.execve(argv[0], argv, env)


if __name__ == "__main__":
    try:
        main()
    except Exception:
        # Never print native stderr or auth/preflight bodies.
        print("Company tool unavailable or identity binding refused.", file=sys.stderr)
        raise SystemExit(69)
