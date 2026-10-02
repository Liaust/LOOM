"""Run project Codex calls under the existing agents login, not loom's HOME."""
import argparse
import json
import os
from pathlib import Path
import pwd
import select
import signal
import socket
import struct
import subprocess
import sys
import tempfile
import time

SOCKET = "/run/loom-codex-jobs/exec.sock"
HOME = "/home/agents"
BINARY = HOME + "/.local/bin/codex"
MAX_REQUEST = 128 * 1024
MAX_ANSWER = 1024 * 1024
MAX_RESPONSE = 8 * MAX_ANSWER
MAX_TIMEOUT = 3600
SANDBOXES = ("read-only", "workspace-write")


def receive(conn, limit):
    data = bytearray()
    while len(data) <= limit:
        block = conn.recv(min(65536, limit + 1 - len(data)))
        if not block:
            raise ValueError("connection closed before result")
        data.extend(block)
        if b"\n" in block:
            if not data.endswith(b"\n") or data.count(b"\n") != 1:
                raise ValueError("expected one JSON message")
            break
    if len(data) > limit:
        raise ValueError("message too large")
    return json.loads(data)


def send(conn, value):
    conn.sendall(json.dumps(value).encode() + b"\n")


def write_job_result(result):
    result = dict(result)
    answer = result.pop("answer", "")
    artifacts = []
    if result.get("ok"):
        (Path(os.environ["LOOM_ARTIFACT_DIR"]) / "answer.txt").write_text(answer)
        artifacts.append({"key": "answer", "path": "answer.txt", "type": "file"})
    Path(os.environ["LOOM_RESULT_FILE"]).write_text(json.dumps({
        "status": "ok" if result.get("ok") else "error", "outputs": result, "artifacts": artifacts}) + "\n")


def validate(request, projects_root):
    required = {"project_root", "prompt", "timeout_seconds"}
    if not isinstance(request, dict) or not required <= set(request) or set(request) - required - {"sandbox"}:
        raise ValueError("expected project_root, prompt, timeout_seconds and optional sandbox")
    if request.get("sandbox", "read-only") not in SANDBOXES:
        raise ValueError("sandbox must be read-only or workspace-write")
    root = Path(request["project_root"])
    if not root.is_absolute() or not root.is_dir():
        raise ValueError("existing absolute project_root required")
    root = root.resolve(strict=True)
    parent = Path(projects_root).resolve(strict=True)
    if root == parent or not root.is_relative_to(parent):
        raise ValueError("project_root must be inside the configured Projects folder")
    if not isinstance(request["prompt"], str) or not request["prompt"].strip():
        raise ValueError("nonempty prompt required")
    timeout = request["timeout_seconds"]
    if type(timeout) is not int or not 1 <= timeout <= MAX_TIMEOUT:
        raise ValueError(f"timeout_seconds must be between 1 and {MAX_TIMEOUT}")
    return root, timeout


def execute(request, conn, projects_root, binary=BINARY, home=HOME):
    root, timeout = validate(request, projects_root)
    if not (Path(home) / ".codex/auth.json").is_file():
        raise ValueError("agents Codex login unavailable; operator login required")
    env = os.environ.copy()
    env.update(HOME=home, CODEX_HOME=str(Path(home) / ".codex"))
    # Do not import the caller's scratch HOME, credentials or arbitrary flags.
    with tempfile.TemporaryDirectory(prefix="loom-codex-job-") as temporary:
        answer = Path(temporary) / "answer.txt"
        sandbox = request.get("sandbox", "read-only")
        argv = [binary, "exec", "--ignore-user-config", "--ephemeral", "--sandbox", sandbox,
                "-c", 'approval_policy="never"', "--skip-git-repo-check", "--cd", str(root),
                "--output-last-message", str(answer), "-"]
        # A file avoids blocking on a child that never reads its prompt pipe.
        with tempfile.TemporaryFile() as prompt:
            prompt.write(request["prompt"].encode())
            prompt.seek(0)
            child = subprocess.Popen(argv, cwd=root, env=env, stdin=prompt,
                                     stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                                     start_new_session=True)
            timed_out = False
            try:
                deadline = time.monotonic() + timeout
                while child.poll() is None:
                    if time.monotonic() >= deadline:
                        timed_out = True
                        break
                    if select.select([conn], [], [], 0.1)[0]:
                        # EOF means LOOM cancelled or lost its job client. Extra
                        # requests on the same connection are not a new job.
                        raise ValueError("caller disconnected or sent extra input")
                code = 124 if timed_out else child.returncode
            finally:
                try:
                    os.killpg(child.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                child.wait()
        result = {"ok": code == 0, "exit_code": code, "timed_out": timed_out,
                  "executor": "agents", "executor_uid": os.getuid(),
                  "codex_home": env["CODEX_HOME"], "project_root": str(root),
                  "sandbox": sandbox, "timeout_seconds": timeout}
        if code == 0:
            with answer.open("rb") as source:
                data = source.read(MAX_ANSWER + 1)
            if len(data) > MAX_ANSWER:
                raise ValueError("Codex answer exceeded 1 MiB")
            result["answer"] = data.decode("utf-8")
        else:
            result["error"] = "codex_timed_out" if timed_out else "codex_execution_failed"
        return result


def serve(projects_root, caller):
    conn = socket.socket(fileno=os.dup(0))
    conn.settimeout(5)
    try:
        _, uid, _ = struct.unpack("3i", conn.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, 12))
        if uid != pwd.getpwnam(caller).pw_uid:
            raise ValueError("only the LOOM job runner may call this service")
        if os.getuid() != pwd.getpwnam("agents").pw_uid:
            raise ValueError("Codex executor identity is not agents")
        request = receive(conn, MAX_REQUEST)
        result = execute(request, conn, projects_root)
    except ValueError as error:
        result = {"ok": False, "error": "codex_job_invalid_request", "message": str(error)}
    except (OSError, TypeError, KeyError):
        # No provider stderr, prompt or credential material in service logs.
        result = {"ok": False, "error": "codex_job_unavailable_or_invalid_request"}
    try:
        send(conn, result)
    except OSError:
        pass
    finally:
        conn.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="mode", required=True)
    server = commands.add_parser("serve")
    server.add_argument("--projects-root", required=True)
    server.add_argument("--caller", required=True)
    client = commands.add_parser("exec")
    client.add_argument("--project-root", required=True)
    client.add_argument("--prompt-file", required=True)
    client.add_argument("--timeout-seconds", type=int, default=90)
    client.add_argument("--sandbox", choices=SANDBOXES, default="read-only")
    client.add_argument("--loom-job-result", action="store_true", help="write normal LOOM result and answer artifact")
    client.add_argument("--require-text", help="optional acceptance string required in the final answer")
    args = parser.parse_args()
    if args.mode == "serve":
        signal.signal(signal.SIGTERM, lambda *_: sys.exit(143))
        serve(args.projects_root, args.caller)
        return 0
    try:
        with open(args.prompt_file, "rb") as source:
            prompt = source.read(MAX_REQUEST + 1).decode("utf-8")
        request = {"project_root": args.project_root, "prompt": prompt,
                   "timeout_seconds": args.timeout_seconds, "sandbox": args.sandbox}
        if not 1 <= args.timeout_seconds <= MAX_TIMEOUT:
            raise ValueError("timeout outside supported range")
        if len(json.dumps(request).encode()) + 1 > MAX_REQUEST:
            raise ValueError("prompt exceeds request limit")
        with socket.socket(socket.AF_UNIX) as conn:
            conn.settimeout(args.timeout_seconds + 10)
            conn.connect(SOCKET)
            send(conn, request)
            result = receive(conn, MAX_RESPONSE)
    except (ValueError, OSError):
        result = {"ok": False, "error": "codex_job_bridge_unavailable"}
    if args.require_text and result.get("ok"):
        result["acceptance_marker"] = args.require_text in result.get("answer", "")
        if not result["acceptance_marker"]:
            result.update(ok=False, error="codex_answer_missing_required_text")
    if args.loom_job_result:
        write_job_result(result)
    else:
        print(json.dumps(result))
    return 0 if result.get("ok") else 1


if __name__ == "__main__":
    sys.exit(main())
