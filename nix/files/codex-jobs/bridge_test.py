import importlib.util
import fcntl
import hashlib
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("bridge", Path(__file__).with_name("bridge.py"))
bridge = importlib.util.module_from_spec(spec)
spec.loader.exec_module(bridge)


class BridgeTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.projects = self.root / "Projects"
        self.project = self.projects / "owned"
        self.project.mkdir(parents=True)
        self.home = self.root / "agents"
        (self.home / ".codex").mkdir(parents=True)
        (self.home / ".codex/auth.json").write_text("{}")
        self.binary = self.root / "fake-codex"
        self.request = dict(project_root=str(self.project), prompt="read context", timeout_seconds=1)
        self.mina = self.root / "mina"
        self.mina.mkdir()
        self.custom = self.home / "tasks"
        (self.custom / "one").mkdir(parents=True)
        self.server, self.client = socket.socketpair()
        self.addCleanup(self.server.close)
        self.addCleanup(self.client.close)

    def fake(self, body):
        self.binary.write_text("#!" + sys.executable + "\n" + body)
        self.binary.chmod(0o700)

    def execute(self):
        return bridge.execute(self.request, self.server, self.projects, str(self.binary), str(self.home))

    def test_existing_home_fixed_flags_and_answer(self):
        self.fake('''import os, pathlib, sys
assert "--ignore-user-config" in sys.argv and "--ephemeral" in sys.argv
assert sys.argv[sys.argv.index("--sandbox") + 1] == "read-only"
assert pathlib.Path(os.environ["CODEX_HOME"]) == pathlib.Path(os.environ["HOME"]) / ".codex"
assert os.getcwd() == sys.argv[sys.argv.index("--cd") + 1]
assert sys.stdin.read() == "read context"
pathlib.Path(sys.argv[sys.argv.index("--output-last-message") + 1]).write_text("answer")
''')
        result = self.execute()
        self.assertTrue(result["ok"])
        self.assertEqual(result["executor_uid"], os.getuid())
        self.assertEqual(result["answer"], "answer")

    def test_timeout_and_disconnect_reap_child(self):
        self.fake('''import os, pathlib, time
pathlib.Path("pid").write_text(str(os.getpid()))
time.sleep(10)
''')
        result = self.execute()
        self.assertEqual(result["exit_code"], 124)
        pid = int((self.project / "pid").read_text())
        with self.assertRaises(ProcessLookupError):
            os.kill(pid, 0)
        timer = threading.Timer(0.2, self.client.close)
        timer.start()
        try:
            with self.assertRaisesRegex(ValueError, "disconnected"):
                self.execute()
        finally:
            timer.join()
        pid = int((self.project / "pid").read_text())
        with self.assertRaises(ProcessLookupError):
            os.kill(pid, 0)

    def test_bad_request_does_not_run(self):
        for field, value in (("project_root", str(self.root)), ("timeout_seconds", 3601),
                             ("sandbox", "danger-full-access"), ("sandbox", None),
                             ("timeout_seconds", True), ("prompt", ""), ("codex_home", "/other")):
            request = dict(self.request)
            request[field] = value
            with self.subTest(field=field, value=value), self.assertRaises(ValueError):
                bridge.validate(request, self.projects)
        link = self.projects / "escape"
        link.symlink_to(self.home)
        with self.assertRaises(ValueError):
            bridge.validate(dict(self.request, project_root=str(link)), self.projects)

    def test_workspace_write_and_longer_timeout_are_explicit(self):
        self.request.update(sandbox="workspace-write", timeout_seconds=1800)
        self.fake('''import pathlib, sys
assert sys.argv[sys.argv.index("--sandbox") + 1] == "workspace-write"
pathlib.Path("owned-output.txt").write_text("edited")
pathlib.Path(sys.argv[sys.argv.index("--output-last-message") + 1]).write_text("done")
''')
        result = self.execute()
        self.assertTrue(result["ok"])
        self.assertEqual(result["sandbox"], "workspace-write")
        self.assertEqual(result["timeout_seconds"], 1800)
        self.assertEqual((self.project / "owned-output.txt").read_text(), "edited")
        bridge.validate(dict(self.request, timeout_seconds=3600), self.projects)

    def test_no_login_and_failed_codex_are_not_success(self):
        self.fake("import sys\nsys.exit(7)\n")
        result = self.execute()
        self.assertFalse(result["ok"])
        self.assertEqual(result["exit_code"], 7)
        self.assertNotIn("answer", result)
        (self.home / ".codex/auth.json").unlink()
        with self.assertRaisesRegex(ValueError, "login unavailable"):
            self.execute()

    def test_message_bound(self):
        self.client.sendall(json.dumps(self.request).encode() + b"\n")
        self.assertEqual(bridge.receive(self.server, bridge.MAX_REQUEST), self.request)
        self.client.sendall(b"x" * 11)
        with self.assertRaisesRegex(ValueError, "too large"):
            bridge.receive(self.server, 10)

    def test_runtime_update_lease_waits_without_starting_or_replaying(self):
        self.fake('''import pathlib, sys
pathlib.Path("started").write_text("one run")
pathlib.Path(sys.argv[sys.argv.index("--output-last-message") + 1]).write_text("done")
''')
        results = []
        with (self.home / ".codex/loom-runtime-update.lock").open("a") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX)
            worker = threading.Thread(target=lambda: results.append(self.execute()))
            worker.start()
            time.sleep(0.1)
            self.assertFalse((self.project / "started").exists())
        worker.join(timeout=5)
        self.assertFalse(worker.is_alive())
        self.assertEqual(len(results), 1)
        self.assertTrue(results[0]["ok"])

    def test_cancelled_lease_waiter_does_not_launch(self):
        with (self.home / ".codex/loom-runtime-update.lock").open("a") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX)
            self.client.close()
        with self.assertRaisesRegex(ValueError, "disconnected"):
            self.execute()

    def test_job_artifact_is_published_in_artifact_directory(self):
        artifacts = self.root / "artifacts"
        output = self.root / "output"
        artifacts.mkdir()
        output.mkdir()
        result_file = self.root / "result.json"
        with patch.dict(os.environ, LOOM_ARTIFACT_DIR=str(artifacts), LOOM_OUTPUT_DIR=str(output), LOOM_RESULT_FILE=str(result_file)):
            bridge.write_job_result(dict(ok=True, executor="agents", answer="marker"))
        result = json.loads(result_file.read_text())
        self.assertEqual(result["status"], "ok")
        self.assertEqual((artifacts / result["artifacts"][0]["path"]).read_text(), "marker")
        self.assertEqual(list(output.iterdir()), [])
        self.assertNotIn("answer", result["outputs"])

    def saved(self, mode="project", **changes):
        request = dict(protocol_version=2, mode=mode, cwd=str({"project": self.project, "mina": self.mina, "custom": self.custom / "one"}[mode]),
                       prompt="read context", prompt_ref="owned/prompt.md", model="gpt-6-astra",
                       sandbox="danger-full-access", timeout_seconds=2, job_id="job-owned", attempt=1)
        request.update(changes)
        return request

    def test_saved_question_is_rendered_in_answer_artifact(self):
        artifacts = self.root / "artifacts"
        artifacts.mkdir()
        result_file = self.root / "result.json"
        for answer in ("", "I read the CSV but need its report scope."):
            with self.subTest(answer=answer), patch.dict(os.environ, LOOM_ARTIFACT_DIR=str(artifacts), LOOM_RESULT_FILE=str(result_file)):
                bridge.write_job_result(dict(protocol_version=2, ok=False, outcome="needs_input",
                                             question="All valid rows or flagged rows only?", answer=answer))
            rendered = (artifacts / "answer.txt").read_text()
            self.assertIn("Clarification required:\nAll valid rows or flagged rows only?\n", rendered)
            if answer:
                self.assertTrue(rendered.startswith(answer + "\n\n"))
            result = json.loads(result_file.read_text())
            self.assertEqual(result["status"], "needs_input")
            self.assertEqual(result["outputs"]["question"], "All valid rows or flagged rows only?")
            self.assertFalse(result["outputs"]["ok"])
            self.assertNotIn("answer", result["outputs"])

    def test_saved_empty_failure_answer_names_the_outcome(self):
        artifacts = self.root / "artifacts"
        artifacts.mkdir()
        with patch.dict(os.environ, LOOM_ARTIFACT_DIR=str(artifacts), LOOM_RESULT_FILE=str(self.root / "result.json")):
            bridge.write_job_result(dict(protocol_version=2, ok=False, outcome="interrupted",
                                         error="codex_timed_out", answer="", question=""))
        self.assertEqual((artifacts / "answer.txt").read_text(), "Agent outcome: interrupted\nReason: codex_timed_out\n")

    def test_saved_execution_help_explains_clarification_and_continuation(self):
        help_text = " ".join(subprocess.check_output([sys.executable, bridge.__file__, "exec", "--help"], text=True).split())
        self.assertIn("Process exit 0 is not task completion", help_text)
        self.assertIn("explicit choice, not automatic retry", help_text)
        self.assertIn("same mode, cwd, model and sandbox", help_text)

    def execute_saved(self, request):
        return bridge.execute_saved(request, self.server, self.projects, str(self.binary), str(self.home), str(self.mina), str(self.custom))

    def test_saved_modes_and_exact_continuation(self):
        thread = "00000000-0000-4000-8000-000000000002"
        self.fake('''import json, pathlib, sys
assert "--ephemeral" not in sys.argv and "--ignore-user-config" not in sys.argv
assert "--json" in sys.argv and "--output-schema" in sys.argv
print(json.dumps({"type":"thread.started", "thread_id":"00000000-0000-4000-8000-000000000002"}), flush=True)
pathlib.Path(sys.argv[sys.argv.index("-o") + 1]).write_text(json.dumps({"outcome":"needs_input","answer":"", "question":"Which folder?"}))
''')
        for mode in ("mina", "project", "custom"):
            request = self.saved(mode)
            result = self.execute_saved(request)
            self.assertEqual(result["thread_id"], thread)
            self.assertEqual(result["outcome"], "needs_input")
            self.assertEqual(bridge.receive(self.client, bridge.MAX_RESPONSE)["event"], "thread_started")
            result = self.execute_saved(dict(request, thread_id=thread))
            self.assertEqual(result["thread_id"], thread)
            bridge.receive(self.client, bridge.MAX_RESPONSE)
            with self.assertRaisesRegex(ValueError, "settings mismatch"):
                self.execute_saved(dict(request, thread_id=thread, sandbox="read-only"))

    def test_saved_admission_and_partial_receipt(self):
        for mode, cwd in (("mina", self.project), ("project", self.mina), ("custom", self.project)):
            with self.subTest(mode=mode), self.assertRaises(ValueError):
                bridge.validate_saved(self.saved(mode, cwd=str(cwd)), self.projects, str(self.mina), str(self.custom))
        thread = "00000000-0000-4000-8000-000000000002"
        self.fake('''import json, time
print(json.dumps({"type":"thread.started", "thread_id":"00000000-0000-4000-8000-000000000002"}), flush=True)
time.sleep(10)
''')
        result = self.execute_saved(self.saved(timeout_seconds=1))
        self.assertTrue(result["timed_out"])
        self.assertEqual(result["outcome"], "interrupted")
        partial = bridge.receive(self.client, bridge.MAX_RESPONSE)["result"]
        self.assertEqual(partial["thread_id"], thread)
        durable = json.loads((self.home / ".codex/loom-jobs" / (thread + ".json")).read_text())
        self.assertEqual(durable["outcome"], "interrupted")
        artifacts = self.root / "artifacts"
        artifacts.mkdir()
        with patch.dict(os.environ, LOOM_ARTIFACT_DIR=str(artifacts), LOOM_RESULT_FILE=str(self.root / "result.json")):
            bridge.write_job_result(partial)
        self.assertEqual(json.loads((artifacts / "codex-run.json").read_text())["thread_id"], thread)

    def box_roots(self):
        self.company = self.root / "company/Projects/owned"
        self.company.mkdir(parents=True)
        self.company_env = {key: str(self.root / "company-home" / key.lower()) for key in bridge.REQUIRED_CONTEXT_ENV}
        self.company_env.update(PATH=os.environ["PATH"], HOME=str(self.root / "company-home"))
        return bridge.ProjectRoots(self.projects, [dict(box_id="box_company", projects_root=str(self.company.parent), environment=self.company_env)])

    def test_explicit_box_admission_never_falls_back(self):
        roots = self.box_roots()
        request = dict(self.request, project_root=str(self.company), box_id="box_company")
        self.assertEqual(bridge.validate(request, roots)[0], self.company)
        self.assertEqual(bridge.validate(self.request, roots)[0], self.project)
        for changes in ({"box_id": "box_unknown"}, {"box_id": None}, {"project_root": str(self.project)}, {"project_root": str(self.company.parent)}):
            with self.subTest(changes=changes), self.assertRaises(ValueError):
                bridge.validate(dict(request, **changes), roots)
        with self.assertRaises(ValueError):
            bridge.validate({k: v for k, v in request.items() if k != "box_id"}, roots)
        with self.assertRaises(ValueError):
            bridge.validate_saved(self.saved("mina", box_id="box_company"), roots, str(self.mina), str(self.custom))

    def test_box_environment_is_clean_and_not_process_global(self):
        roots = self.native_box_roots()
        with patch.dict(os.environ, GH_TOKEN="personal", BASECAMP_PROFILE="mina", GOOGLE_APPLICATION_CREDENTIALS="personal"):
            before = dict(os.environ)
            env, context = bridge.execution_context(dict(box_id="box_company"), roots, str(self.home))
            self.assertEqual(os.environ, before)
        self.assertNotIn("GH_TOKEN", env)
        self.assertNotIn("BASECAMP_PROFILE", env)
        self.assertNotIn("GOOGLE_APPLICATION_CREDENTIALS", env)
        self.assertEqual(env["HOME"], str(self.profile / "home"))
        self.assertEqual(env["CODEX_HOME"], str(self.home / ".codex"))
        self.assertEqual(context["box_id"], "box_company")
        self.assertEqual(env["LOOM_CONFIG_FILE"], self.company_env["LOOM_CONFIG_FILE"])

    def test_saved_box_binding_is_immutable(self):
        roots = self.native_box_roots()
        thread = "00000000-0000-4000-8000-000000000002"
        request = self.saved(cwd=str(self.company), box_id="box_company")
        self.fake('''import json, pathlib, sys
print(json.dumps({"type":"thread.started", "thread_id":"00000000-0000-4000-8000-000000000002"}), flush=True)
pathlib.Path(sys.argv[sys.argv.index("-o") + 1]).write_text(json.dumps({"outcome":"completed","answer":"done", "question":""}))
''')
        result = bridge.execute_saved(request, self.server, roots, str(self.binary), str(self.home), str(self.mina), str(self.custom))
        bridge.receive(self.client, bridge.MAX_RESPONSE)
        self.assertEqual(result["box_id"], "box_company")
        self.profile_config["profile_context"]["environment"]["GH_HOST"] = "github.example"
        self.write_profile_config()
        with self.assertRaisesRegex(ValueError, "settings mismatch"):
            bridge.execute_saved(dict(request, thread_id=thread), self.server, roots, str(self.binary), str(self.home), str(self.mina), str(self.custom))

    def test_catalog_derives_roots_and_preserves_uninitialized_legacy(self):
        roots = self.box_roots()
        import yaml
        catalog = self.root / "boxes.json"
        contexts = self.root / "contexts.json"
        source = self.company.parent.parent
        catalog.write_text(json.dumps(dict(schema_version="loom.box_catalog.v1", default_box="personal", boxes=[
            dict(key="personal", label="Personal", node="main", root_path=str(self.projects.parent)),
            dict(key="company", label="Company", node="main", root_path=str(source)),
        ])))
        # Fixture primary must not contain the company Box (catalog forbids nesting).
        personal = self.root / "personal/Projects"
        personal.mkdir(parents=True)
        data = json.loads(catalog.read_text())
        data["boxes"][0]["root_path"] = str(personal.parent)
        catalog.write_text(json.dumps(data))
        contexts.write_text(json.dumps(dict(company=self.company_env)))
        uninitialized = bridge.ProjectRoots.from_catalog(personal, catalog, contexts)
        self.assertEqual(uninitialized.boxes, {})
        (source / ".loom").mkdir()
        (source / ".loom/box.yaml").write_text(yaml.safe_dump(dict(schema_version="loom.box.v0.5", box_id="box_company", owner_node="main", root_path=str(source))))
        selected = bridge.ProjectRoots.from_catalog(personal, catalog, contexts)
        self.assertEqual(bridge.project_parent(selected, "box_company"), self.company.parent)
        self.assertEqual(selected.boxes["box_company"]["environment"]["LOOM_BOX_KEY"], "company")
        with self.assertRaises(ValueError):
            bridge.execution_context(dict(box_id="box_company"), selected, str(self.home))

    def test_static_company_environment_is_not_sufficient_to_launch(self):
        roots = self.box_roots()
        requests = (dict(self.request, project_root=str(self.company), box_id="box_company"),
                    self.saved(cwd=str(self.company), box_id="box_company"))
        for request in requests:
            execute = bridge.execute_saved if request.get("protocol_version") == 2 else bridge.execute
            with self.subTest(request=request), patch.object(bridge.subprocess, "Popen") as launch:
                with self.assertRaises((ValueError, OSError)):
                    execute(request, self.server, roots, str(self.binary), str(self.home))
                launch.assert_not_called()

    def native_box_roots(self):
        roots = self.box_roots()
        self.profile = self.root / "native/.hermes/profiles/tyler"
        (self.profile / "home").mkdir(parents=True)
        root = self.company.parent.parent
        (root / ".loom").mkdir()
        (root / ".loom/box.yaml").write_text("schema_version: loom.box.v0.5\nbox_id: box_company\nowner_node: main\nroot_path: " + str(root) + "\n")
        roots.boxes["box_company"]["environment"].update(HOME=str(self.profile / "home"), LOOM_BOX_KEY="company", LOOM_CONFIG_FILE="/etc/loom-client.env")
        (self.profile / "home/bin").mkdir()
        roots.boxes["box_company"]["environment"]["PATH"] = str(self.profile / "home/bin") + ":/etc/profiles/per-user/agents/bin:/home/agents/.local/bin:/run/current-system/sw/bin"
        self.box_context = dict(box_id="box_company", profile="tyler", workdir=str(root), context_revision="sha256:" + "a" * 64)
        hashes, self.commands = {}, {}
        for tool in ("gh", "basecamp"):
            binary = self.root / ("native-" + tool)
            binary.write_text("#!/bin/sh\nexit 0\n")
            binary.chmod(0o700)
            self.commands[tool] = str(binary)
            shim = self.profile / "home/bin" / tool
            shim.write_text("#!/bin/sh\nexit 69\n")
            shim.chmod(0o700)
            hashes[str(shim)] = "sha256:" + hashlib.sha256(shim.read_bytes()).hexdigest()
        self.profile_marker = dict(self.box_context, schema_version="loom.box_agent.v1", box_key="company", retired=False, bootstrap_hashes=hashes)
        (self.profile / "loom-box-owner.json").write_text(json.dumps(self.profile_marker))
        selectors = dict(roots.boxes["box_company"]["environment"], LOOM_BOX_ID="box_company")
        selectors.pop("HOME")
        self.profile_config = dict(terminal=dict(cwd=str(root)), profile_context=dict(required_cwd=str(root), allow_root_auth_fallback=False, environment=selectors))
        self.write_profile_config()
        return roots

    def write_profile_config(self):
        (self.profile / "config.yaml").write_text(json.dumps(self.profile_config))
        self.tool_context = dict(environment=dict(self.profile_config["profile_context"]["environment"], HOME=str(self.profile / "home"), HERMES_HOME=str(self.profile)), commands=self.commands, basecamp=None, github_host=None)
        (self.profile / "loom-tool-context.json").write_text(json.dumps(self.tool_context))

    def test_direct_company_dispatch_rechecks_all_protected_context_before_launch(self):
        roots = self.native_box_roots()
        paths = [self.profile / name for name in ("home", "home/bin", "home/bin/basecamp", "home/bin/gh", "loom-box-owner.json", "config.yaml", "loom-tool-context.json")]
        paths.append(Path(self.commands["basecamp"]))
        for path in paths:
            held = path.with_name(path.name + ".held")
            path.rename(held)
            try:
                for saved, explicit in ((False, False), (True, False), (True, True)):
                    request = self.saved(cwd=str(self.company), box_id="box_company") if saved else dict(self.request, project_root=str(self.company), box_id="box_company")
                    if explicit:
                        request["box_context"] = self.box_context
                    execute = bridge.execute_saved if saved else bridge.execute
                    with self.subTest(missing=path.name, saved=saved, explicit=explicit), patch.object(bridge.subprocess, "Popen") as launch:
                        with self.assertRaises((ValueError, OSError)):
                            execute(request, self.server, roots, str(self.binary), str(self.home))
                        launch.assert_not_called()
            finally:
                held.rename(path)

    def test_company_replaced_shims_or_tool_selectors_refuse(self):
        roots = self.native_box_roots()
        request = dict(box_id="box_company")
        shim = self.profile / "home/bin/basecamp"
        original = shim.read_text()
        shim.write_text("#!/bin/sh\nexec /etc/profiles/per-user/agents/bin/basecamp \"$@\"\n")
        with self.assertRaisesRegex(ValueError, "shim"):
            bridge.execution_context(request, roots, str(self.home))
        shim.write_text(original)
        shim.chmod(0o600)
        with self.assertRaisesRegex(ValueError, "shim"):
            bridge.execution_context(request, roots, str(self.home))
        shim.chmod(0o700)
        self.tool_context["environment"]["HOME"] = "/home/agents"
        (self.profile / "loom-tool-context.json").write_text(json.dumps(self.tool_context))
        with self.assertRaisesRegex(ValueError, "binding unavailable"):
            bridge.execution_context(request, roots, str(self.home))

    def test_box_id_only_is_generic_codex_with_verified_tool_binding(self):
        roots = self.native_box_roots()
        self.fake('''import os, pathlib, sys
assert "--profile" not in sys.argv
assert os.environ["HOME"].endswith("profiles/tyler/home")
assert "/etc/profiles/per-user/agents/bin" not in os.environ["PATH"].split(":")
assert "/home/agents/.local/bin" not in os.environ["PATH"].split(":")
assert "BASECAMP_PROFILE" not in os.environ and "GH_TOKEN" not in os.environ
assert os.environ["BASECAMP_NO_KEYRING"] == "1"
pathlib.Path(sys.argv[sys.argv.index("--output-last-message") + 1]).write_text("generic coding")
''')
        request = dict(self.request, project_root=str(self.company), box_id="box_company")
        with patch.dict(os.environ, GH_TOKEN="ambient", BASECAMP_PROFILE="mina"):
            result = bridge.execute(request, self.server, roots, str(self.binary), str(self.home))
        self.assertTrue(result["ok"])
        self.assertEqual(result["box_context"], self.box_context)
        self.assertEqual(result["codex_home"], str(self.home / ".codex"))

    def test_native_box_wide_admission_is_not_arbitrary_custom_cwd(self):
        roots = self.native_box_roots()
        request = self.saved(mode="project", cwd=self.box_context["workdir"], box_id="box_company", box_context=self.box_context)
        request["mode"] = "box"
        self.assertEqual(bridge.validate_saved(request, roots)[0], Path(self.box_context["workdir"]))
        # A Box-wide job can work in Notes without requiring a Projects folder.
        self.company.rmdir()
        self.company.parent.rmdir()
        self.assertEqual(bridge.validate_saved(request, roots)[0], Path(self.box_context["workdir"]))
        for change in ({"cwd": str(self.root)}, {"box_id": "box_unknown"}, {"box_context": None}, {"mode": "custom"}, {"mode": "mina"}):
            with self.subTest(change=change), self.assertRaises(ValueError):
                bridge.validate_saved(dict(request, **change), roots)
        without = dict(request)
        without.pop("box_context")
        with self.assertRaises(ValueError): bridge.validate_saved(without, roots)

    def test_native_box_tools_come_from_current_profile_not_stale_nix_selectors(self):
        roots = self.native_box_roots()
        self.profile_config["profile_context"]["environment"]["XDG_CACHE_HOME"] = str(self.profile / "home/current-cache")
        self.write_profile_config()
        request = dict(box_id="box_company", box_context=self.box_context)
        with patch.dict(os.environ, GH_TOKEN="ambient", GOOGLE_APPLICATION_CREDENTIALS="ambient", BASECAMP_PROFILE="mina"):
            before = dict(os.environ)
            env, binding = bridge.execution_context(request, roots, str(self.home))
            self.assertEqual(dict(os.environ), before)
        self.assertEqual(env["XDG_CACHE_HOME"], str(self.profile / "home/current-cache"))
        self.assertEqual(env["HOME"], str(self.profile / "home"))
        self.assertEqual(env["HERMES_HOME"], str(self.profile))
        self.assertEqual(env["CODEX_HOME"], str(self.home / ".codex"))
        self.assertEqual(binding["box_context"], self.box_context)
        self.assertEqual(env["PATH"], str(self.profile / "home/bin") + ":/run/current-system/sw/bin")
        self.assertEqual(env["BASECAMP_NO_KEYRING"], "1")
        for key in ("GH_TOKEN", "GOOGLE_APPLICATION_CREDENTIALS", "BASECAMP_PROFILE"):
            self.assertNotIn(key, env)
        for changes in (dict(retired=True), dict(context_revision="sha256:" + "b" * 64), dict(box_id="box_other")):
            (self.profile / "loom-box-owner.json").write_text(json.dumps(dict(self.profile_marker, **changes)))
            with self.subTest(changes=changes), self.assertRaises(ValueError):
                bridge.execution_context(request, roots, str(self.home))
        (self.profile / "loom-box-owner.json").write_text(json.dumps(self.profile_marker))
        (self.profile / "config.yaml").write_text("invalid: [PRIVATE_FIXTURE\n")
        with self.assertRaises(ValueError) as error:
            bridge.execution_context(request, roots, str(self.home))
        self.assertNotIn("PRIVATE_FIXTURE", str(error.exception))

    def test_native_box_saved_thread_keeps_profile_and_current_tool_digest(self):
        roots = self.native_box_roots()
        request = self.saved(cwd=self.box_context["workdir"], box_id="box_company", box_context=self.box_context)
        request["mode"] = "box"
        thread = "00000000-0000-4000-8000-000000000002"
        self.fake('''import json, os, pathlib, sys
assert os.environ["LOOM_BOX_ID"] == "box_company"
assert "GH_TOKEN" not in os.environ
assert pathlib.Path(os.environ["CODEX_HOME"]).name == ".codex"
assert "--profile" not in sys.argv
print(json.dumps({"type":"thread.started", "thread_id":"00000000-0000-4000-8000-000000000002"}), flush=True)
pathlib.Path(sys.argv[sys.argv.index("-o") + 1]).write_text(json.dumps({"outcome":"completed","answer":"done", "question":""}))
''')
        with patch.dict(os.environ, GH_TOKEN="ambient"):
            result = bridge.execute_saved(request, self.server, roots, str(self.binary), str(self.home), str(self.mina), str(self.custom))
        bridge.receive(self.client, bridge.MAX_RESPONSE)
        self.assertEqual(result["box_context"], self.box_context)
        self.assertEqual(result["mode"], "box")
        self.profile_config["profile_context"]["environment"]["XDG_CACHE_HOME"] = str(self.profile / "home/changed-cache")
        self.write_profile_config()
        with self.assertRaisesRegex(ValueError, "settings mismatch"):
            bridge.execute_saved(dict(request, thread_id=thread), self.server, roots, str(self.binary), str(self.home), str(self.mina), str(self.custom))


if __name__ == "__main__":
    unittest.main()
