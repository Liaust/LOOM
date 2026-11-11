"""Run project Codex calls under the existing agents login, not loom's HOME."""
import argparse
from contextlib import contextmanager
import fcntl
import hashlib
import json
import os
from pathlib import Path
import pwd
import re
import select
import signal
import socket
import struct
import subprocess
import sys
import tempfile
import time
import uuid

SOCKET = "/run/loom-codex-jobs/exec.sock"
HOME = "/home/agents"
BINARY = HOME + "/.local/bin/codex"
MAX_REQUEST = 128 * 1024
MAX_ANSWER = 1024 * 1024
MAX_RESPONSE = 8 * MAX_ANSWER
MAX_TIMEOUT = 3600
SANDBOXES = ("read-only", "workspace-write")
MODES = ("mina", "project", "custom", "box")
MINA = "/srv/loom/agents/mina"
CUSTOM = HOME + "/tasks"
OUTCOME_SCHEMA = {
    "type": "object", "additionalProperties": False,
    "properties": {"outcome": {"type": "string", "enum": ["completed", "needs_input", "failed"]},
                   "answer": {"type": "string"}, "question": {"type": "string"}},
    "required": ["outcome", "answer", "question"],
}

BOX_ID = re.compile(r"^box_[A-Za-z0-9_-]+$")
CONTEXT_ENV = {"HOME", "PATH", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME",
               "XDG_STATE_HOME", "GH_CONFIG_DIR", "GH_HOST", "GIT_CONFIG_GLOBAL",
               "GIT_SSH_COMMAND", "GWS_CONFIG_DIR", "GOOGLE_WORKSPACE_CLI_CONFIG_DIR", "LOOM_CONFIG_FILE", "LOOM_BOX_KEY"}
REQUIRED_CONTEXT_ENV = {"HOME", "PATH", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "GH_CONFIG_DIR", "GIT_CONFIG_GLOBAL", "GIT_SSH_COMMAND", "LOOM_CONFIG_FILE"}


class ProjectRoots:
    """Only immutable server configuration can admit another Box's Projects parent."""
    def __init__(self, legacy, boxes):
        self.legacy = Path(legacy)
        self.boxes = {}
        parents = [self.legacy.resolve()]
        for entry in boxes:
            if not isinstance(entry, dict) or set(entry) != {"box_id", "projects_root", "environment"}:
                raise ValueError("invalid trusted Box project context")
            box_id, root, env = entry["box_id"], Path(entry["projects_root"]), entry["environment"]
            if not isinstance(box_id, str) or not BOX_ID.fullmatch(box_id) or box_id in self.boxes:
                raise ValueError("invalid or duplicate Box identity")
            if not root.is_absolute() or root.resolve() != root or root.name != "Projects":
                raise ValueError("exact canonical Box Projects parent required")
            if any(root != parent and (root.is_relative_to(parent) or parent.is_relative_to(root)) for parent in parents) or any(root == Path(e["projects_root"]) for e in self.boxes.values()):
                raise ValueError("overlapping Box Projects parents")
            if not isinstance(env, dict) or not REQUIRED_CONTEXT_ENV <= set(env) or set(env) - CONTEXT_ENV:
                raise ValueError("explicit company tool environment required")
            if any(not isinstance(v, str) or not v or any(c in v for c in "\0\r\n") for v in env.values()):
                raise ValueError("invalid tool environment")
            self.boxes[box_id] = dict(entry)
            parents.append(root)

    @classmethod
    def from_catalog(cls, legacy, catalog_file, contexts_file):
        import yaml
        def read(path):
            with Path(path).open("rb") as stream:
                raw = stream.read((1 << 20) + 1)
            if len(raw) > 1 << 20:
                raise ValueError("Box configuration exceeds bound")
            return raw
        catalog, contexts = json.loads(read(catalog_file)), json.loads(read(contexts_file))
        if not isinstance(catalog, dict) or set(catalog) != {"schema_version", "default_box", "boxes"} or catalog["schema_version"] != "loom.box_catalog.v1" or not isinstance(catalog["boxes"], list) or not 1 <= len(catalog["boxes"]) <= 128 or not isinstance(contexts, dict):
            raise ValueError("invalid trusted Box catalog/contexts")
        entries, keys, ids, roots = [], set(), set(), []
        primary = Path(legacy).parent
        primary_seen = False
        for entry in catalog["boxes"]:
            if not isinstance(entry, dict) or set(entry) != {"key", "label", "node", "root_path"} or not re.fullmatch(r"[a-z][a-z0-9_-]{0,62}", entry["key"]) or entry["key"] in keys:
                raise ValueError("invalid trusted Box catalog entry")
            keys.add(entry["key"])
            root = Path(entry["root_path"])
            if not root.is_absolute() or root == Path("/") or root.resolve() != root or any(root.is_relative_to(p) or p.is_relative_to(root) for p in roots):
                raise ValueError("invalid or overlapping Box source roots")
            roots.append(root)
            if root == primary:
                if entry["key"] != catalog["default_box"]:
                    raise ValueError("legacy primary Box must remain default")
                primary_seen = True
            descriptor = root / ".loom/box.yaml"
            if not root.exists() or not descriptor.exists():
                continue
            if descriptor.resolve() != descriptor or not descriptor.is_file():
                raise ValueError("unsafe Box descriptor")
            owner = yaml.safe_load(read(descriptor))
            if not isinstance(owner, dict) or owner.get("schema_version") not in ("loom.box.v0.4.1", "loom.box.v0.5") or not BOX_ID.fullmatch(owner.get("box_id", "")) or owner.get("root_path") != str(root) or owner.get("owner_node") != entry["node"] or owner["box_id"] in ids:
                raise ValueError("Box descriptor disagrees with catalog")
            ids.add(owner["box_id"])
            # No tool binding means unavailable, never the host's personal stores.
            if entry["key"] in contexts:
                entries.append(dict(box_id=owner["box_id"], projects_root=str(root / "Projects"), environment=dict(contexts[entry["key"]], LOOM_BOX_KEY=entry["key"])))
        if not primary_seen or set(contexts) - keys:
            raise ValueError("catalog/legacy/context disagreement")
        return cls(legacy, entries)


def box_entry(projects_root, box_id):
    if not isinstance(box_id, str) or not BOX_ID.fullmatch(box_id) or not isinstance(projects_root, ProjectRoots) or box_id not in projects_root.boxes:
        raise ValueError("selected Box project context unavailable")
    return projects_root.boxes[box_id]


def project_parent(projects_root, box_id=None):
    if box_id is not None:
        return Path(box_entry(projects_root, box_id)["projects_root"]).resolve(strict=True)
    return Path(projects_root.legacy if isinstance(projects_root, ProjectRoots) else projects_root).resolve(strict=True)


def native_box_environment(request, selected):
    import yaml
    home = Path(selected["environment"]["HOME"])
    profile = home.parent
    root = Path(selected["projects_root"]).parent
    if (not home.is_absolute() or profile.parent.name != "profiles" or home.name != "home"
            or not re.fullmatch(r"[a-z][a-z0-9_-]{0,62}", profile.name)):
        raise ValueError("native Box profile differs from trusted tool binding")
    for directory in (profile, home, home / "bin"):
        info = directory.stat()
        if directory.resolve() != directory or not directory.is_dir() or info.st_uid not in (0, os.geteuid()) or info.st_mode & 0o022:
            raise ValueError("protected native Box home unavailable")
    def read(path, managed=True):
        if path.resolve() != path:
            raise ValueError("aliased native Box configuration")
        info = path.stat()
        if not path.is_file() or info.st_size > 1 << 20 or (managed and (info.st_uid not in (0, os.geteuid()) or info.st_mode & 0o022)):
            raise ValueError("unsafe native Box configuration")
        with path.open("rb") as stream:
            raw = stream.read((1 << 20) + 1)
        if len(raw) > 1 << 20:
            raise ValueError("native Box configuration exceeds bound")
        return raw
    def read_yaml(path, managed=True):
        try:
            return yaml.safe_load(read(path, managed))
        except yaml.YAMLError:
            raise ValueError("native Box YAML configuration unavailable") from None
    marker = json.loads(read(profile / "loom-box-owner.json"))
    context = {k: marker.get(k) for k in ("box_id", "profile", "workdir", "context_revision")} if isinstance(marker, dict) else {}
    if (not isinstance(marker, dict) or marker.get("schema_version") != "loom.box_agent.v1"
            or marker.get("retired") is not False or context.get("box_id") != request.get("box_id")
            or context.get("profile") != profile.name or context.get("workdir") != str(root)
            or not isinstance(context.get("context_revision"), str) or not re.fullmatch(r"sha256:[0-9a-f]{64}", context["context_revision"])
            or ("box_context" in request and request["box_context"] != context)
            or marker.get("box_key") != selected["environment"].get("LOOM_BOX_KEY")):
        raise ValueError("saved native Box profile context changed")
    owner = read_yaml(root / ".loom/box.yaml", managed=False)
    if not isinstance(owner, dict) or owner.get("box_id") != context["box_id"] or owner.get("root_path") != str(root) or owner.get("schema_version") not in ("loom.box.v0.4.1", "loom.box.v0.5"):
        raise ValueError("saved Box descriptor changed")
    config = read_yaml(profile / "config.yaml")
    policy = config.get("profile_context") if isinstance(config, dict) else None
    terminal = config.get("terminal") if isinstance(config, dict) else None
    if (not isinstance(policy, dict) or policy.get("required_cwd") != str(root)
            or policy.get("allow_root_auth_fallback") is not False
            or not isinstance(terminal, dict) or terminal.get("cwd") != str(root)):
        raise ValueError("native Box tool policy changed or unavailable")
    overlay = policy.get("environment")
    if (not isinstance(overlay, dict) or set(overlay) - CONTEXT_ENV - {"LOOM_BOX_ID"}
            or not (REQUIRED_CONTEXT_ENV - {"HOME"}) <= set(overlay)
            or any(not isinstance(v, str) or not v or any(c in v for c in "\0\r\n") for v in overlay.values())
            or overlay.get("HOME", str(home)) != str(home)
            or overlay["LOOM_CONFIG_FILE"] != selected["environment"]["LOOM_CONFIG_FILE"]
            or overlay.get("LOOM_BOX_ID") != context["box_id"] or overlay.get("LOOM_BOX_KEY") != marker["box_key"]):
        raise ValueError("native Box tool environment unavailable")
    environment = dict(overlay, HOME=str(home), HERMES_HOME=str(profile))
    tools = json.loads(read(profile / "loom-tool-context.json"))
    if (not isinstance(tools, dict) or tools.get("environment") != environment
            or not isinstance(tools.get("commands"), dict) or not {"basecamp", "gh"} <= set(tools["commands"])
            or not isinstance(marker.get("bootstrap_hashes"), dict)):
        raise ValueError("native Box company tool binding unavailable")
    for tool, command in tools["commands"].items():
        if (not re.fullmatch(r"[a-z][a-z0-9_-]*", tool) or not isinstance(command, str)
                or not Path(command).is_absolute() or not Path(command).is_file() or not os.access(command, os.X_OK)):
            raise ValueError("native Box company command unavailable")
        shim = home / "bin" / tool
        expected = marker["bootstrap_hashes"].get(str(shim))
        if expected != "sha256:" + hashlib.sha256(read(shim)).hexdigest() or not os.access(shim, os.X_OK):
            raise ValueError("native Box company shim unavailable or changed")
    # Never expose the shared user profile's MINA business wrappers as fallback.
    paths = environment["PATH"].split(":")
    if paths[0] != str(home / "bin") or any(not p or not Path(p).is_absolute() for p in paths):
        raise ValueError("native Box company PATH unavailable")
    environment["PATH"] = ":".join(p for p in paths if p not in ("/etc/profiles/per-user/agents/bin", "/home/agents/.local/bin"))
    environment.update(BASECAMP_NO_KEYRING="1", GH_PROMPT_DISABLED="1", DBUS_SESSION_BUS_ADDRESS="unix:path=/dev/null")
    tool_digest = "sha256:" + hashlib.sha256(json.dumps(tools, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
    return environment, context, tool_digest


def execution_context(request, projects_root, home):
    box_id = request.get("box_id")
    if box_id is None:
        env = os.environ.copy()
        env.update(HOME=home, CODEX_HOME=str(Path(home) / ".codex"))
        return env, {}
    selected = box_entry(projects_root, box_id)
    environment = selected["environment"]
    context = {}
    if "box_context" in request or Path(selected["projects_root"]) != projects_root.legacy.resolve():
        environment, saved, tool_digest = native_box_environment(request, selected)
        context["box_context"] = dict(saved)
        context["native_tool_digest"] = tool_digest
    # Keep Codex inference/session state in its existing home. Business CLIs get
    # a clean, explicitly bound environment, never ambient personal tokens.
    env = {k: os.environ[k] for k in ("USER", "LOGNAME", "LANG", "LC_ALL", "TZ") if k in os.environ}
    env.update(environment, CODEX_HOME=str(Path(home) / ".codex"), LOOM_BOX_ID=box_id)
    binding = dict(selected, environment=environment, **context)
    digest = "sha256:" + hashlib.sha256(json.dumps(binding, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
    return env, dict(box_id=box_id, tool_context_digest=digest, **context)


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
    if result.get("ok") or result.get("protocol_version") == 2:
        if result.get("protocol_version") == 2:
            question = result.get("question", "").strip()
            if result.get("outcome") == "needs_input" and question:
                answer = (answer.rstrip() + "\n\n" if answer.strip() else "") + "Clarification required:\n" + question + "\n"
            elif not answer.strip():
                answer = "Agent outcome: " + result.get("outcome", "unknown") + "\n"
                if result.get("error"):
                    answer += "Reason: " + result["error"] + "\n"
        (Path(os.environ["LOOM_ARTIFACT_DIR"]) / "answer.txt").write_text(answer)
        artifacts.append({"key": "answer", "path": "answer.txt", "type": "file"})
    if result.get("protocol_version") == 2:
        receipt = Path(os.environ["LOOM_ARTIFACT_DIR"]) / "codex-run.json"
        atomic_json(receipt, result)
        artifacts.append({"key": "codex_run", "path": receipt.name, "type": "file"})
    atomic_json(Path(os.environ["LOOM_RESULT_FILE"]), {
        "status": result.get("outcome", "ok" if result.get("ok") else "error"),
        "outputs": result, "artifacts": artifacts})


def atomic_json(path, value):
    fd, temporary = tempfile.mkstemp(prefix=".receipt-", dir=path.parent)
    try:
        with os.fdopen(fd, "w") as output:
            json.dump(value, output)
            output.write("\n")
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary, path)
        directory = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def validate_saved(request, projects_root, mina=MINA, custom=CUSTOM):
    required = {"protocol_version", "mode", "cwd", "prompt", "prompt_ref", "timeout_seconds", "sandbox", "model"}
    if not isinstance(request, dict) or not required <= set(request) or set(request) - required - {"thread_id", "job_id", "attempt", "box_id", "box_context"}:
        raise ValueError("expected the version2 persisted execution contract")
    if type(request["protocol_version"]) is not int or request["protocol_version"] != 2 or request["mode"] not in MODES:
        raise ValueError("invalid saved execution mode/version")
    if request["sandbox"] not in (*SANDBOXES, "danger-full-access"):
        raise ValueError("invalid native sandbox selection")
    if not isinstance(request["model"], str) or not request["model"].startswith("gpt-") or any(c not in "abcdefghijklmnopqrstuvwxyz0123456789-." for c in request["model"]):
        raise ValueError("explicit OpenAI Codex model required")
    for field in ("prompt", "prompt_ref"):
        if not isinstance(request[field], str) or not request[field].strip():
            raise ValueError("nonempty prompt and source reference required")
    if type(request["timeout_seconds"]) is not int or not 1 <= request["timeout_seconds"] <= MAX_TIMEOUT:
        raise ValueError("invalid saved execution timeout")
    root = Path(request["cwd"])
    if not root.is_absolute() or not root.is_dir():
        raise ValueError("existing absolute cwd required")
    root = root.resolve(strict=True)
    if "box_id" in request and (request["mode"] not in ("project", "box") or request["box_id"] is None):
        raise ValueError("Box selection requires a project execution")
    if "box_context" in request and (request["mode"] not in ("project", "box") or "box_id" not in request):
        raise ValueError("native context requires explicit Box execution")
    if request["mode"] == "mina":
        if root != Path(mina).resolve(strict=True):
            raise ValueError("MINA requires her fixed workspace")
    elif request["mode"] == "box":
        if "box_context" not in request:
            raise ValueError("Box-wide execution requires a saved native profile context")
        selected = box_entry(projects_root, request.get("box_id"))
        parent = Path(selected["projects_root"]).parent.resolve(strict=True)
        native_box_environment(request, selected)
        if not root.is_relative_to(parent):
            raise ValueError("cwd must stay inside the bound Box")
    else:
        parent = project_parent(projects_root, request.get("box_id")) if request["mode"] == "project" else Path(custom).resolve(strict=True)
        if root == parent or not root.is_relative_to(parent) or root.is_relative_to(Path(mina).resolve()):
            raise ValueError("cwd must be inside the selected task/project root")
    if request.get("thread_id"):
        try:
            if str(uuid.UUID(request["thread_id"])) != request["thread_id"]:
                raise ValueError()
        except (ValueError, TypeError, AttributeError):
            raise ValueError("exact native thread UUID required") from None
    if request.get("job_id") is not None and (not isinstance(request["job_id"], str) or len(request["job_id"]) > 128):
        raise ValueError("invalid LOOM job identity")
    if request.get("attempt") is not None and (type(request["attempt"]) is not int or request["attempt"] < 1):
        raise ValueError("invalid LOOM attempt")
    return root, request["timeout_seconds"]


@contextmanager
def runtime_lease(home, conn):
    fd = os.open(Path(home) / ".codex/loom-runtime-update.lock", os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, "a") as lock:
        fcntl.flock(lock, fcntl.LOCK_SH)
        # A queued caller may have cancelled while the package was replaced.
        if select.select([conn], [], [], 0)[0]:
            raise ValueError("caller disconnected or sent extra input")
        yield


def execute_saved(request, conn, projects_root, binary=BINARY, home=HOME, mina=MINA, custom=CUSTOM):
    with runtime_lease(home, conn):
        return _execute_saved(request, conn, projects_root, binary, home, mina, custom)


def _execute_saved(request, conn, projects_root, binary=BINARY, home=HOME, mina=MINA, custom=CUSTOM):
    root, timeout = validate_saved(request, projects_root, mina, custom)
    codex_home = Path(home) / ".codex"
    if not (codex_home / "auth.json").is_file():
        raise ValueError("agents Codex login unavailable; operator login required")
    receipts = codex_home / "loom-jobs"
    receipts.mkdir(mode=0o700, exist_ok=True)
    binding = {"mode": request["mode"], "cwd": str(root), "model": request["model"], "sandbox": request["sandbox"]}
    env, context = execution_context(request, projects_root, home)
    binding.update(context)
    if request.get("thread_id"):
        previous = json.loads((receipts / (request["thread_id"] + ".json")).read_text())
        if any(previous.get(key) != value for key, value in binding.items()) or any(previous.get(key) != binding.get(key) for key in ("box_id", "tool_context_digest", "box_context")):
            raise ValueError("saved thread mode/cwd/settings mismatch")
    result = dict(binding, protocol_version=2, job_id=request.get("job_id"), attempt=request.get("attempt"),
                  thread_id=request.get("thread_id"), outcome="interrupted", executor="agents",
                  executor_uid=os.getuid(), timeout_seconds=timeout, timed_out=False, question="", answer="")
    with (receipts / "exec.lock").open("a") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise ValueError("another unattended Codex run is active") from None
        with tempfile.TemporaryDirectory(prefix="loom-codex-saved-") as temporary:
            answer, schema = Path(temporary) / "answer.json", Path(temporary) / "schema.json"
            schema.write_text(json.dumps(OUTCOME_SCHEMA))
            argv = [binary, "exec", "--json", "--skip-git-repo-check", "--color", "never",
                    "-c", 'approval_policy="never"', "-c", 'model_provider="openai"',
                    "-c", "sandbox_mode=" + json.dumps(request["sandbox"]), "-m", request["model"],
                    "--cd", str(root), "--output-schema", str(schema), "-o", str(answer)]
            if request["mode"] == "mina":
                argv += ["--profile", "mina"]
            if request.get("thread_id"):
                argv += ["resume", request["thread_id"]]
            argv += ["-"]
            with tempfile.TemporaryFile() as prompt:
                prompt.write(request["prompt"].encode())
                prompt.seek(0)
                child = subprocess.Popen(argv, cwd=root, env=env, stdin=prompt, stdout=subprocess.PIPE,
                                         stderr=subprocess.DEVNULL, start_new_session=True)
                os.set_blocking(child.stdout.fileno(), False)
                pending = bytearray()
                deadline = time.monotonic() + timeout
                try:
                    while True:
                        if time.monotonic() >= deadline:
                            result.update(timed_out=True, error="codex_timed_out")
                            break
                        ready, _, _ = select.select([conn, child.stdout], [], [], 0.1)
                        if conn in ready:
                            result["error"] = "codex_caller_disconnected"
                            break
                        if child.stdout in ready:
                            block = os.read(child.stdout.fileno(), 65536)
                            pending.extend(block)
                            if len(pending) > MAX_RESPONSE:
                                raise ValueError("Codex event exceeded bound")
                            while b"\n" in pending:
                                line, _, pending = pending.partition(b"\n")
                                event = json.loads(line)
                                if event.get("type") == "thread.started":
                                    thread_id = str(uuid.UUID(event["thread_id"]))
                                    if result["thread_id"] and result["thread_id"] != thread_id:
                                        raise ValueError("native continuation returned another thread")
                                    result["thread_id"] = thread_id
                                    atomic_json(receipts / (thread_id + ".json"), result)
                                    send(conn, {"event": "thread_started", "result": result})
                            if not block and child.poll() is not None:
                                result["exit_code"] = child.returncode
                                break
                    if child.poll() is not None and child.returncode == 0 and not result.get("error"):
                        with answer.open("rb") as output:
                            raw = output.read(MAX_ANSWER + 1)
                        if len(raw) > MAX_ANSWER:
                            raise ValueError("Codex answer exceeded 1 MiB")
                        semantic = json.loads(raw)
                        if set(semantic) != set(OUTCOME_SCHEMA["required"]) or semantic["outcome"] not in ("completed", "needs_input", "failed") or not all(isinstance(semantic[k], str) for k in semantic):
                            raise ValueError("invalid terminal Codex response")
                        if semantic["outcome"] == "needs_input" and not semantic["question"].strip():
                            raise ValueError("clarification question missing")
                        result.update(semantic)
                    elif not result.get("error"):
                        result.update(outcome="failed", error="codex_execution_failed")
                except (ValueError, OSError, KeyError):
                    result.update(outcome="failed", error="codex_result_invalid")
                finally:
                    try:
                        os.killpg(child.pid, signal.SIGKILL)
                    except ProcessLookupError:
                        pass
                    child.wait()
                    child.stdout.close()
                    result.setdefault("exit_code", 124 if result["timed_out"] else child.returncode)
                    if result["thread_id"]:
                        atomic_json(receipts / (result["thread_id"] + ".json"), result)
    if not result["thread_id"]:
        result.update(outcome="failed", error="codex_thread_identity_missing")
    result["ok"] = result["outcome"] == "completed"
    return result


def validate(request, projects_root):
    required = {"project_root", "prompt", "timeout_seconds"}
    if not isinstance(request, dict) or not required <= set(request) or set(request) - required - {"sandbox", "box_id", "box_context"}:
        raise ValueError("expected project_root, prompt, timeout_seconds and optional sandbox")
    if request.get("sandbox", "read-only") not in SANDBOXES:
        raise ValueError("sandbox must be read-only or workspace-write")
    root = Path(request["project_root"])
    if not root.is_absolute() or not root.is_dir():
        raise ValueError("existing absolute project_root required")
    root = root.resolve(strict=True)
    if "box_id" in request and request["box_id"] is None:
        raise ValueError("selected Box project context unavailable")
    parent = project_parent(projects_root, request.get("box_id"))
    if "box_context" in request:
        native_box_environment(request, box_entry(projects_root, request.get("box_id")))
    if root == parent or not root.is_relative_to(parent):
        raise ValueError("project_root must be inside the configured Projects folder")
    if not isinstance(request["prompt"], str) or not request["prompt"].strip():
        raise ValueError("nonempty prompt required")
    timeout = request["timeout_seconds"]
    if type(timeout) is not int or not 1 <= timeout <= MAX_TIMEOUT:
        raise ValueError(f"timeout_seconds must be between 1 and {MAX_TIMEOUT}")
    return root, timeout


def execute(request, conn, projects_root, binary=BINARY, home=HOME):
    with runtime_lease(home, conn):
        return _execute(request, conn, projects_root, binary, home)


def _execute(request, conn, projects_root, binary=BINARY, home=HOME):
    root, timeout = validate(request, projects_root)
    if not (Path(home) / ".codex/auth.json").is_file():
        raise ValueError("agents Codex login unavailable; operator login required")
    env, context = execution_context(request, projects_root, home)
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
        result.update(context)
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
        if not isinstance(request, dict):
            raise ValueError("request must be an object")
        result = execute_saved(request, conn, projects_root) if request.get("protocol_version") == 2 else execute(request, conn, projects_root)
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
    server.add_argument("--boxes-file", help="trusted LOOM Box catalog; project parents derive from descriptor-validated roots")
    server.add_argument("--box-contexts-file", help="trusted JSON mapping from Box key to non-secret tool environment")
    server.add_argument("--caller", required=True)
    client = commands.add_parser("exec", epilog=(
        "With --mode, runs retain a native conversation and report completed, needs_input or failed. "
        "Process exit 0 is not task completion: inspect the LOOM job result. "
        "needs_input retains its question in the result and answer artifact; it requires an explicit choice, not automatic retry. "
        "Continue with a new prompt and --thread-id from that result, keeping the same mode, cwd, model and sandbox."))
    client.add_argument("--project-root")
    client.add_argument("--box-id")
    client.add_argument("--box-context", help="saved native Box/profile binding as JSON; no credential values")
    client.add_argument("--mode", choices=MODES, dest="execution_mode")
    client.add_argument("--cwd")
    client.add_argument("--model", default="gpt-6-astra")
    client.add_argument("--thread-id", help="continue the exact saved native conversation; requires --mode and unchanged execution settings")
    client.add_argument("--prompt-file", required=True)
    client.add_argument("--timeout-seconds", type=int, default=90)
    client.add_argument("--sandbox", choices=(*SANDBOXES, "danger-full-access"))
    client.add_argument("--loom-job-result", action="store_true", help="write the semantic LOOM result and readable answer/clarification artifact")
    client.add_argument("--require-text", help="optional acceptance string required in the final answer")
    args = parser.parse_args()
    if args.mode == "serve":
        signal.signal(signal.SIGTERM, lambda *_: sys.exit(143))
        roots = args.projects_root
        if bool(args.boxes_file) != bool(args.box_contexts_file):
            raise ValueError("Box catalog and tool contexts must be supplied together")
        if args.boxes_file:
            roots = ProjectRoots.from_catalog(roots, args.boxes_file, args.box_contexts_file)
        serve(roots, args.caller)
        return 0
    try:
        with open(args.prompt_file, "rb") as source:
            prompt = source.read(MAX_REQUEST + 1).decode("utf-8")
        request = {"project_root": args.project_root, "prompt": prompt,
                   "timeout_seconds": args.timeout_seconds, "sandbox": args.sandbox or "read-only"}
        if args.execution_mode:
            request = {"protocol_version": 2, "mode": args.execution_mode, "cwd": args.cwd or (MINA if args.execution_mode == "mina" else args.project_root),
                       "prompt": prompt, "prompt_ref": str(Path(args.prompt_file).resolve()), "model": args.model,
                       "timeout_seconds": args.timeout_seconds, "sandbox": args.sandbox or "danger-full-access",
                       "job_id": os.environ.get("LOOM_JOB_ID"), "attempt": int(os.environ["LOOM_ATTEMPT"]) if os.environ.get("LOOM_ATTEMPT") else None}
            if args.thread_id:
                request["thread_id"] = args.thread_id
        if args.box_id:
            request["box_id"] = args.box_id
        if args.box_context:
            request["box_context"] = json.loads(args.box_context)
        if not 1 <= args.timeout_seconds <= MAX_TIMEOUT:
            raise ValueError("timeout outside supported range")
        if len(json.dumps(request).encode()) + 1 > MAX_REQUEST:
            raise ValueError("prompt exceeds request limit")
        with socket.socket(socket.AF_UNIX) as conn:
            conn.settimeout(args.timeout_seconds + 10)
            conn.connect(SOCKET)
            send(conn, request)
            with conn.makefile("rb") as stream:
                while True:
                    line = stream.readline(MAX_RESPONSE + 1)
                    if not line.endswith(b"\n") or len(line) > MAX_RESPONSE:
                        raise ValueError("bounded complete response required")
                    result = json.loads(line)
                    if result.get("event") != "thread_started":
                        break
                    if args.loom_job_result:
                        write_job_result(result["result"])
    except (ValueError, OSError):
        if args.execution_mode and args.loom_job_result and Path(os.environ["LOOM_RESULT_FILE"]).exists():
            result = json.loads(Path(os.environ["LOOM_RESULT_FILE"]).read_text())["outputs"]
            result.update(ok=False, outcome="interrupted", error="codex_job_bridge_unavailable")
        else:
            result = {"ok": False, "error": "codex_job_bridge_unavailable"}
    if args.require_text and result.get("ok"):
        result["acceptance_marker"] = args.require_text in result.get("answer", "")
        if not result["acceptance_marker"]:
            result.update(ok=False, error="codex_answer_missing_required_text")
    if args.loom_job_result:
        write_job_result(result)
    else:
        print(json.dumps(result))
    return 0 if result.get("ok") or result.get("protocol_version") == 2 else 1


if __name__ == "__main__":
    sys.exit(main())
