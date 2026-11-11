import unittest
from contextlib import redirect_stdout
import copy
import io
import json
import os
from pathlib import Path
import tempfile
import types
from unittest.mock import patch
import codex_dispatch as dispatch


class DispatchTest(unittest.TestCase):
    def test_default_catalog_does_not_require_private_daemon_directory(self):
        self.assertEqual(dispatch.BOXES, "/etc/loom-boxes.json")

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="loom-box-dispatch-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.box = self.root / "company"
        (self.box / ".loom").mkdir(parents=True)
        self.project = self.box / "Projects/owned"
        self.project.mkdir(parents=True)
        self.home = self.root / "agent/.hermes/profiles/tyler"
        (self.home / "home").mkdir(parents=True)
        (self.home / "scripts").mkdir()
        self.catalog = self.root / "boxes.json"
        self.catalog.write_text(json.dumps(dict(schema_version="loom.box_catalog.v1", default_box="personal", boxes=[dict(key="company", label="Company", node="main", root_path=str(self.box))])))
        (self.box / ".loom/box.yaml").write_text("schema_version: loom.box.v0.5\nbox_id: box_company\nowner_node: main\nroot_path: " + str(self.box) + "\n")
        self.context = dict(box_id="box_company", profile="tyler", workdir=str(self.box), context_revision="sha256:" + "a" * 64)
        self.marker = dict(self.context, schema_version="loom.box_agent.v1", box_key="company", retired=False)
        self.write_json(self.home / "loom-box-owner.json", self.marker)
        self.config = dict(terminal=dict(cwd=str(self.box)), profile_context=dict(required_cwd=str(self.box), allow_root_auth_fallback=False, environment=dict(PATH="/native/bin", XDG_CONFIG_HOME=str(self.home / "home/.config"), LOOM_CONFIG_FILE="/etc/loom-client.env", LOOM_BOX_ID="box_company", LOOM_BOX_KEY="company")))
        # JSON is valid YAML, keeping these tests independent of emitter style.
        self.write_json(self.home / "config.yaml", self.config)
        self.spec = dict(project_id="", resource="review", job_id="native123", profile_home=str(self.home), cwd=str(self.box), prompt="Read company notes", model="gpt-6.1-sol", timeout_seconds=90, sandbox="read-only", box_context=self.context)

    def write_json(self, path, value):
        path.write_text(json.dumps(value))
        path.chmod(0o600)

    def envelope(self):
        return dict(ok=True, data=dict(status="completed", job_id="job_test", capability_call=dict(capability_call_id="call_test"), result=dict(job=dict(job=dict(status="completed"), outputs=[dict(output_key="thread_id", value_json="thread_test"), dict(output_key="outcome", value_json="completed")]))))

    def test_actual_script_runner_input_envelope(self):
        payload = dict(spec={}, hermes_execution_id="native_execution")
        self.assertEqual(dispatch.job_input(json.dumps(dict(input=payload, script=dict(id="hermes_codex_dispatch"), routing={}))), payload)
        self.assertEqual(dispatch.job_input(json.dumps(payload)), payload)
        with self.assertRaises(ValueError): dispatch.job_input('{}')

    def test_real_outcome_not_transport_acceptance(self):
        spec = dict(project_id="project_test", resource="review", job_id="native123", profile_home="/tmp/profile", cwd="/tmp/project", prompt="Read README", model="gpt-6-astra", timeout_seconds=90, sandbox="read-only")
        self.assertEqual(dispatch.validate(spec), spec)
        envelope = dict(ok=True, data=dict(status="completed", job_id="job_test", capability_call=dict(capability_call_id="call_test"), result=dict(job=dict(job=dict(status="completed"), outputs=[dict(output_key="thread_id", value_json="thread_test"), dict(output_key="outcome", value_json="needs_input")]))))
        saved = dispatch.receipt(envelope, spec, "execution_test")
        self.assertEqual(saved["outcome"], "needs_input")
        self.assertEqual(saved["thread_id"], "thread_test")
        self.assertNotIn("prompt", saved)
        self.assertNotIn("answer", saved)
        for changed in (dict(timeout_seconds=0), dict(sandbox="inherit"), dict(model="ambient")):
            with self.assertRaises(ValueError): dispatch.validate(dict(spec, **changed))

    def test_box_wide_spec_requires_bound_context_not_a_fake_project(self):
        self.assertEqual(dispatch.validate(self.spec), self.spec)
        bare = dict(self.spec)
        bare.pop("box_context")
        with self.assertRaises(ValueError): dispatch.validate(bare)
        for change in (dict(profile="mina"), dict(box_id="unknown"), dict(workdir="/"), dict(context_revision="ambient")):
            invalid = dict(self.spec, box_context=dict(self.context, **change))
            with self.subTest(change=change), self.assertRaises(ValueError): dispatch.validate(invalid)

    def test_invocation_uses_authorized_owner_and_exact_occurrence_idempotency(self):
        path = self.home / "scripts/loom-codex-fixture.json"
        self.write_json(path, self.spec)
        calls, scopes = [], []
        constants = types.ModuleType("hermes_constants")
        constants.set_hermes_home_override = lambda home: scopes.append(home) or "token"
        constants.reset_hermes_home_override = lambda token: scopes.append(token)
        executions = types.ModuleType("cron.executions")
        executions.list_executions = lambda **kwargs: [dict(status="running", id="execution_same")]
        def run(argv, **kwargs):
            calls.append((argv, kwargs))
            kwargs["stdout"].write(json.dumps(self.envelope()).encode())
            return types.SimpleNamespace(returncode=0)
        modules = {"hermes_constants": constants, "cron": types.ModuleType("cron"), "cron.executions": executions}
        with patch.dict("sys.modules", modules), patch.object(dispatch.subprocess, "run", run), patch.dict(os.environ, GH_TOKEN="ambient", GITHUB_TOKEN="ambient", BASECAMP_PROFILE="mina"):
            before = dict(os.environ)
            with redirect_stdout(io.StringIO()) as out:
                self.assertEqual(dispatch.invoke(path, self.catalog), 0)
                self.assertEqual(dispatch.invoke(path, self.catalog), 0)
            self.assertEqual(dict(os.environ), before)
        self.assertEqual(scopes, [str(self.home), "token", str(self.home), "token"])
        for argv, kwargs in calls:
            self.assertEqual(argv[:4], [dispatch.LOOM, "capability", "call", "main@script-runner.script.run"])
            request = json.loads(argv[argv.index("--input") + 1])
            self.assertEqual(request["script_ref"], "hermes_codex_dispatch")
            self.assertNotIn("project_ref", request)
            self.assertEqual(request["input"]["spec"]["box_context"], self.context)
            self.assertEqual(request["input"]["hermes_execution_id"], "execution_same")
            self.assertEqual(argv[argv.index("--idempotency-key") + 1], "hermes-codex:execution_same")
            self.assertEqual(kwargs["env"]["HOME"], str(self.home / "home"))
            self.assertEqual(kwargs["env"]["LOOM_CONFIG_FILE"], "/etc/loom-client.env")
            self.assertNotIn("GH_TOKEN", kwargs["env"])
            self.assertNotIn("BASECAMP_PROFILE", kwargs["env"])
        self.assertEqual(json.loads(out.getvalue().splitlines()[0])["box_context"], self.context)

    def test_changed_profile_descriptor_or_token_override_refuses(self):
        for changed in (dict(context_revision="sha256:" + "b" * 64), dict(retired=True), dict(box_id="box_changed")):
            self.write_json(self.home / "loom-box-owner.json", dict(self.marker, **changed))
            with self.subTest(changed=changed), self.assertRaises(ValueError):
                dispatch.invocation_environment(self.spec, self.catalog)
        self.write_json(self.home / "loom-box-owner.json", self.marker)
        config = copy.deepcopy(self.config)
        config["profile_context"]["environment"]["GH_TOKEN"] = "ambient"
        self.write_json(self.home / "config.yaml", config)
        with self.assertRaises(ValueError): dispatch.invocation_environment(self.spec, self.catalog)
        self.write_json(self.home / "config.yaml", self.config)
        (self.home / "config.yaml").write_text("invalid: [PRIVATE_FIXTURE\n")
        with self.assertRaises(ValueError) as error:
            dispatch.invocation_environment(self.spec, self.catalog)
        self.assertNotIn("PRIVATE_FIXTURE", str(error.exception))
        self.write_json(self.home / "config.yaml", self.config)
        (self.box / ".loom/box.yaml").write_text("schema_version: loom.box.v0.5\nbox_id: box_changed\nroot_path: " + str(self.box) + "\n")
        with self.assertRaises(ValueError): dispatch.invocation_environment(self.spec, self.catalog)

    def test_project_and_box_jobs_forward_context_without_private_profile_reads(self):
        artifacts = self.root / "artifacts"
        artifacts.mkdir()
        result_path = self.root / "result.json"
        for project in ("", "project_owned"):
            spec = dict(self.spec, project_id=project, cwd=str(self.project if project else self.box))
            request = dict(spec=spec, hermes_execution_id="execution_same")
            calls = []
            def run(argv, **kwargs):
                calls.append((argv, kwargs))
                self.write_json(result_path, dict(status="completed", outputs=dict(outcome="completed", thread_id="thread_test", box_context=self.context), artifacts=[]))
                return types.SimpleNamespace(returncode=0)
            with patch.dict(os.environ, LOOM_INPUT_JSON=json.dumps(request), LOOM_ARTIFACT_DIR=str(artifacts), LOOM_RESULT_FILE=str(result_path), LOOM_JOB_ID="job_test", LOOM_ATTEMPT="1", GH_TOKEN="ambient", OPENAI_API_KEY="ambient"), patch.object(dispatch.subprocess, "run", run):
                with self.subTest(project=project):
                    # The loom job need not traverse agents' private profile.
                    (self.home / "config.yaml").unlink(missing_ok=True)
                    self.assertEqual(dispatch.run_job(self.catalog), 0)
            argv, kwargs = calls[0]
            self.assertEqual(argv[0], dispatch.CODEX)
            self.assertEqual(argv[argv.index("--mode") + 1], "project" if project else "box")
            self.assertEqual(argv[argv.index("--box-id") + 1], "box_company")
            self.assertEqual(json.loads(argv[argv.index("--box-context") + 1]), self.context)
            self.assertEqual(kwargs["env"]["LOOM_JOB_ID"], "job_test")
            self.assertNotIn("GH_TOKEN", kwargs["env"])
            self.assertNotIn("OPENAI_API_KEY", kwargs["env"])
            outputs = json.loads(result_path.read_text())["outputs"]
            self.assertEqual(outputs["hermes_execution_id"], "execution_same")
            self.assertEqual(outputs["box_context"], self.context)
            self.assertEqual(outputs["project_id"], project)


if __name__ == "__main__":
    unittest.main()
