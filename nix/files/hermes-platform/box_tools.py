"""Select a native business CLI account by trusted Box context, not agent name."""
import argparse
import json
import os
from pathlib import Path
import stat
import subprocess
import sys

from company_tools import prepare
from fixed_basecamp import prepare as prepare_personal_basecamp


def read_binding(path):
    path = Path(path)
    info = path.stat()
    if (not path.is_absolute() or not stat.S_ISREG(info.st_mode)
            or info.st_uid not in (0, os.geteuid()) or info.st_mode & 0o022
            or info.st_size > 65536):
        raise ValueError("unsafe account context binding")
    return json.loads(path.read_bytes())


def resolve(loom, box=None, run=subprocess.run):
    argv = [loom, "box", "context", "--json"]
    if box:
        argv.extend(["--box", box])
    result = run(argv, capture_output=True, text=True, timeout=10, check=True)
    envelope = json.loads(result.stdout)
    if envelope.get("ok") is not True:
        raise ValueError("Box context unavailable")
    context = envelope["data"]
    if not context.get("box_id") or not context.get("box_key"):
        raise ValueError("Box context unavailable")
    return context


def dispatch(context, bindings, native, tool, args, *, load=read_binding, run=subprocess.run):
    if context.get("policy_status") != "available":
        raise ValueError("Box operating policy unavailable")
    selected = bindings.get(context["box_key"])
    if not isinstance(selected, dict) or not selected.get("HOME"):
        raise ValueError("Box account binding unavailable")
    home = Path(selected["HOME"])
    if not home.is_absolute() or home.resolve() != home:
        raise ValueError("Box account home unavailable")
    tools = load(home.parent / "loom-tool-context.json")
    env = tools.get("environment", {})
    if (env.get("HOME") != str(home) or env.get("LOOM_BOX_ID") != context["box_id"]
            or env.get("LOOM_BOX_KEY") != context["box_key"]):
        raise ValueError("Box account binding mismatch")
    # Account context may be shared with Tyler; never load his personality,
    # transcript or inference state into an ordinary CLI/coding invocation.
    tools = dict(tools, commands=dict(tools.get("commands", {}), **native))
    tools["environment"] = {k: v for k, v in env.items() if k != "HERMES_HOME"}
    if context["box_key"] == "personal" and tool == "basecamp":
        binding = tools.get("basecamp")
        if not isinstance(binding, dict) or set(binding) != {"profile", "identity_id", "account_id"}:
            raise ValueError("Personal account binding unavailable")
        selected = dict(binding, binary=native[tool], home=str(home),
                        config_home=env.get("XDG_CONFIG_HOME"), cache_home=env.get("XDG_CACHE_HOME"))
        return prepare_personal_basecamp(selected, list(args), run)
    return prepare(tools, tool, args, run)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--loom", required=True)
    parser.add_argument("--basecamp", required=True)
    parser.add_argument("--gh", required=True)
    parser.add_argument("--git", required=True)
    parser.add_argument("--contexts", required=True)
    parser.add_argument("--box")
    parser.add_argument("tool", choices=("basecamp", "gh", "git"))
    parser.add_argument("args", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    context = resolve(args.loom, args.box)
    bindings = read_binding(args.contexts)
    argv, env = dispatch(context, bindings, dict(basecamp=args.basecamp, gh=args.gh, git=args.git), args.tool, args.args)
    os.execve(argv[0], argv, env)


if __name__ == "__main__":
    try:
        main()
    except Exception:
        print("Box tool unavailable or account binding refused; no fallback was used.", file=sys.stderr)
        raise SystemExit(69)
