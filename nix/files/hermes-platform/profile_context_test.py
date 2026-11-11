"""Focused pinned-source tests. HERMES_PINNED_SOURCE selects a read-only source tree."""
from concurrent.futures import ThreadPoolExecutor
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import threading
import unittest
from unittest.mock import patch


class ProfileContextTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        source = os.environ.get("HERMES_PINNED_SOURCE")
        if not source:
            raise unittest.SkipTest("set HERMES_PINNED_SOURCE to the pinned d923a5eb source")
        source = Path(source)
        if hashlib.sha256((source / "hermes_constants.py").read_bytes()).hexdigest() != "8d55a41452b625061f1f2e2f507a7e7ffb2a396e711afcc7aee28b03a6e8fa9d":
            raise ValueError("not the pinned Hermes source")
        cls.tree = tempfile.TemporaryDirectory(prefix="loom-profile-source-")
        cls.addClassCleanup(cls.tree.cleanup)
        target = Path(cls.tree.name)
        repo = Path(__file__).resolve().parents[3]
        cls.repo = repo
        patch_file = repo / "nix/packages/hermes-profile-context.patch"
        paths = {line[6:] for line in patch_file.read_text().splitlines() if line.startswith("+++ b/")}
        def overlay(src, dest, prefix=""):
            dest.mkdir(exist_ok=True)
            for path in src.iterdir():
                relative = prefix + path.name
                out = dest / path.name
                if relative in paths:
                    shutil.copyfile(path, out)
                elif any(p.startswith(relative + "/") for p in paths):
                    overlay(path, out, relative + "/")
                else:
                    out.symlink_to(path, target_is_directory=path.is_dir())
        overlay(source, target)
        subprocess.run(["patch", "-s", "-p1", "-i", str(patch_file)], cwd=target, check=True)
        sys.path[:0] = [str(target), str(repo / "internal/hermesschedules")]
        cls.addClassCleanup(lambda: sys.path.__delitem__(slice(0, 2)))

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="loom-profile-context-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.native = self.root / "agent/.hermes"
        self.native.mkdir(parents=True)
        (self.native / "config.yaml").write_text("{}\n")
        self.env = patch.dict(os.environ, HOME=str(self.root), HERMES_HOME=str(self.native))
        self.env.start()
        self.addCleanup(self.env.stop)

    def config(self, home, box):
        import hermes_yaml as yaml
        home.mkdir(parents=True)
        (home / "home").mkdir()
        box.mkdir()
        config = dict(terminal=dict(cwd=str(box), home_mode="profile"), profile_context=dict(allow_root_auth_fallback=False, required_cwd=str(box),
                      environment=dict(LOOM_BOX_ID="box_" + home.name, XDG_CONFIG_HOME=str(home / "home/.config")), unset_environment_prefixes=["GH_", "XDG_"]))
        (home / "config.yaml").write_text(yaml.safe_dump(config))

    def test_concurrent_terminal_file_code_and_mcp_context(self):
        from hermes_constants import set_hermes_home_override, reset_hermes_home_override, get_subprocess_home, apply_subprocess_home_env
        from tools.terminal_scope import install_and_reset_profile_terminal_scope
        from tools.mcp_tool_config import _build_safe_env
        from tools.environments.local import _apply_profile_home
        from tools.code_execution_env import _build_child_env
        from tools.file_tools_paths import _expand_tilde
        barrier = threading.Barrier(2)
        homes = [self.native / "profiles" / name for name in ("tyler", "another")]
        for home in homes:
            self.config(home, self.root / home.name)
        before = dict(os.environ)
        def selected(home):
            token = set_hermes_home_override(home)
            try:
                with install_and_reset_profile_terminal_scope(home):
                    barrier.wait(timeout=10)
                    # Shared helper is used by terminal, ~ file paths and code children.
                    self.assertEqual(get_subprocess_home(), str(home / "home"))
                    env = dict(os.environ, GH_TOKEN="ambient-personal", XDG_CONFIG_HOME="personal")
                    apply_subprocess_home_env(env)
                    terminal = dict(os.environ)
                    _apply_profile_home(terminal)
                    code = _build_child_env(rpc_endpoint="fixture", rpc_token="fixture", tmpdir=str(self.root), child_python=sys.executable)
                    self.assertEqual(_expand_tilde("~/file"), str(home / "home/file"))
                    mcp = _build_safe_env({})
                    for child in (env, terminal, code, mcp):
                        self.assertEqual(child["HOME"], str(home / "home"))
                        self.assertEqual(child["HERMES_HOME"], str(home))
                        self.assertEqual(child["XDG_CONFIG_HOME"], str(home / "home/.config"))
                        self.assertNotIn("GH_TOKEN", child)
            finally:
                reset_hermes_home_override(token)
        with ThreadPoolExecutor(max_workers=2) as pool:
            list(pool.map(selected, homes))
        self.assertEqual(dict(os.environ), before)

    def test_root_provider_state_and_pools_are_not_borrowed(self):
        from hermes_constants import set_hermes_home_override, reset_hermes_home_override
        from hermes_cli import auth
        profile = self.native / "profiles/tyler"
        self.config(profile, self.root / "box")
        # Dummy fixture only, never inspect the machine's actual auth stores.
        (self.native / "auth.json").write_text(json.dumps(dict(providers={"openai-codex": {"fixture": True}}, credential_pool={"openai-codex": [{"fixture": True}]})))
        token = set_hermes_home_override(profile)
        try:
            self.assertIsNone(auth._global_auth_file_path())
            self.assertEqual(auth._load_provider_state_with_source({}, "openai-codex"), (None, None))
            self.assertEqual(auth.read_credential_pool("openai-codex"), [])
            from hermes_cli.auth_nous import _nous_shared_auth_dir
            self.assertEqual(_nous_shared_auth_dir(), profile / "shared")
        finally:
            reset_hermes_home_override(token)

    def test_selected_codex_sharing_preserves_root_refresh_owner(self):
        from hermes_constants import set_hermes_home_override, reset_hermes_home_override
        from hermes_cli import auth
        from hermes_cli.auth_codex import _save_codex_tokens
        from agent.credential_pool import persist_pool_entries, _profile_owns_pool_provider
        import hermes_yaml as yaml
        profile = self.native / "profiles/tyler"
        self.config(profile, self.root / "box")
        config = yaml.safe_load((profile / "config.yaml").read_text())
        config["model"] = dict(default="gpt-6.1-sol", provider="openai-codex")
        config["profile_context"]["share_inference_provider"] = "openai-codex"
        (profile / "config.yaml").write_text(yaml.safe_dump(config))
        root_auth = self.native / "auth.json"
        root_auth.write_text(json.dumps(dict(providers={"openai-codex": {"tokens": {"access_token": "fixture-old", "refresh_token": "fixture-refresh"}}, "nous": {"fixture": "unrelated"}}, credential_pool={"openai-codex": [{"id": "fixture-row", "access_token": "fixture-old"}], "nous": [{"id": "unrelated-row"}]})))
        token = set_hermes_home_override(profile)
        try:
            self.assertIn("openai-codex", auth.PROVIDER_REGISTRY)
            self.assertIsNone(auth._global_auth_file_path())
            self.assertIsNone(auth._global_auth_file_path("nous"))
            self.assertEqual(auth._global_auth_file_path("openai-codex"), root_auth)
            self.assertEqual(auth._load_provider_state_with_source({}, "openai-codex")[1], root_auth)
            self.assertEqual(auth._load_provider_state_with_source({}, "nous"), (None, None))
            self.assertEqual(auth.read_credential_pool("openai-codex")[0]["id"], "fixture-row")
            self.assertEqual(auth.read_credential_pool("nous"), [])
            self.assertEqual(auth.read_credential_pool(), {})
            self.assertFalse(_profile_owns_pool_provider("openai-codex"))
            persist_pool_entries("openai-codex", [{"id": "fixture-row", "access_token": "fixture-pool-rotated"}], token_bases={"fixture-row": ("fixture-old", None)})
            self.assertEqual(json.loads(root_auth.read_text())["credential_pool"]["openai-codex"][0]["access_token"], "fixture-pool-rotated")
            _save_codex_tokens(dict(access_token="fixture-singleton-rotated", refresh_token="fixture-refresh-rotated"), write_through=True)
            saved = json.loads(root_auth.read_text())
            self.assertEqual(saved["providers"]["openai-codex"]["tokens"]["access_token"], "fixture-singleton-rotated")
            self.assertEqual(saved["providers"]["nous"], dict(fixture="unrelated"))
            self.assertEqual(saved["credential_pool"]["nous"], [dict(id="unrelated-row")])
            self.assertFalse((profile / "auth.json").exists())
            config["model"]["provider"] = "nous"
            (profile / "config.yaml").write_text(yaml.safe_dump(config))
            with self.assertRaisesRegex(ValueError, "differs"):
                auth._global_auth_file_path("openai-codex")
        finally:
            reset_hermes_home_override(token)

    def test_missing_profile_home_and_cwd_refuse(self):
        from hermes_constants import set_hermes_home_override, reset_hermes_home_override, get_subprocess_home
        from tools.terminal_scope import install_and_reset_profile_terminal_scope, TerminalPolicyUnavailable
        profile = self.native / "profiles/tyler"
        box = self.root / "box"
        self.config(profile, box)
        token = set_hermes_home_override(profile)
        try:
            with install_and_reset_profile_terminal_scope(profile):
                (profile / "home").rmdir()
                with self.assertRaisesRegex(ValueError, "HOME"):
                    get_subprocess_home()
                from tools.file_tools_paths import _expand_tilde
                with self.assertRaisesRegex(ValueError, "HOME"):
                    _expand_tilde("~/file")
            box.rmdir()
            with install_and_reset_profile_terminal_scope(profile):
                with self.assertRaises(TerminalPolicyUnavailable):
                    get_subprocess_home()
        finally:
            reset_hermes_home_override(token)

    def test_cwd_loss_mid_turn_never_uses_process_launch_directory(self):
        from hermes_constants import set_hermes_home_override, reset_hermes_home_override
        from tools.terminal_scope import install_and_reset_profile_terminal_scope
        from agent.runtime_cwd import resolve_agent_cwd
        profile, box = self.native / "profiles/tyler", self.root / "box"
        self.config(profile, box)
        token = set_hermes_home_override(profile)
        try:
            with install_and_reset_profile_terminal_scope(profile):
                self.assertEqual(resolve_agent_cwd(), box)
                box.rmdir()
                with self.assertRaises(ValueError):
                    resolve_agent_cwd()
        finally:
            reset_hermes_home_override(token)

    def test_prompt_context_uses_bound_box_and_requires_explicit_cwd(self):
        from hermes_constants import set_hermes_home_override, reset_hermes_home_override
        from agent.prompt_builder import build_context_files_prompt
        profile, box = self.native / "profiles/tyler", self.root / "box"
        self.config(profile, box)
        (box / "AGENTS.md").write_text("Fixture company workspace instructions.\n")
        (profile / "SOUL.md").write_text("Fixture Tyler personality.\n")
        token = set_hermes_home_override(profile)
        try:
            text = build_context_files_prompt(cwd=str(box), home_override=profile)
            self.assertIn("Fixture company workspace instructions", text)
            self.assertIn("Fixture Tyler personality", text)
            with self.assertRaises(ValueError):
                build_context_files_prompt(home_override=profile)
        finally:
            reset_hermes_home_override(token)

    def test_legacy_project_profile_adapter(self):
        from native_profiles_test import NativeProfilesTest
        NativeProfilesTest("test_profile_replay_drift_schedule_and_archive").debug()

    def test_native_box_bootstrap_schedule_events_and_preservation(self):
        import box_agent
        import native_project as owner
        from cron import jobs
        from hermes_cli import profiles
        root = self.root / "company"
        (root / ".loom").mkdir(parents=True)
        import hermes_yaml as yaml
        (root / ".loom/box.yaml").write_text(yaml.safe_dump(dict(schema_version="loom.box.v0.5", box_id="box_company", owner_node="main", root_path=str(root))))
        catalog = self.root / "boxes.json"
        catalog.write_text(json.dumps(dict(schema_version="loom.box_catalog.v1", default_box="personal", boxes=[dict(key="company", label="Company", node="main", root_path=str(root))])))
        args = (self.native, catalog, "company", "tyler", self.repo / "ai-loom-pack/templates/box-agent", "gpt-6.1-sol", "openai-codex", sys.executable, Path(box_agent.__file__).with_name("company_tools.py"), "/run/current-system/sw/bin:/etc/profiles/per-user/agents/bin:/home/agents/.local/bin", {"gh": sys.executable, "basecamp": sys.executable})
        state = self.root / "state"
        state.mkdir(mode=0o700)
        registry = state / "external-contexts.json"
        other = dict(profile="other", box_id="box_other", root=str(self.root / "other"), paths=[".loom/box.yaml"])
        registry.write_text(json.dumps(dict(schema=box_agent.CONTEXT_SCHEMA, contexts=[other])))
        registry.chmod(0o600)
        options = dict(external_contexts=registry, share_inference_provider="openai-codex")
        private_skill = self.native / "skills/fixture-mina-private"
        private_skill.mkdir(parents=True)
        (private_skill / "SKILL.md").write_text("---\nname: fixture-mina-private\ndescription: Private root learned skill fixture\n---\nRoot-only fixture instructions.\n")
        (self.native / "memories").mkdir(exist_ok=True)
        for name in ("USER.md", "MEMORY.md"):
            (self.native / "memories" / name).write_text("Fixture root private history, must not be copied.\n")
        with patch.object(profiles, "_notify_multiplexer"), patch.object(profiles, "_maybe_register_gateway_service"):
            created = box_agent.prepare(*args, **options)
        self.assertEqual(created["status"], "created")
        directory = self.native / "profiles/tyler"
        self.assertFalse((directory / "auth.json").exists())
        templates = self.repo / "ai-loom-pack/templates/box-agent"
        expected_agents = (templates / "AGENTS.md").read_text()
        for key, value in {"{{BOX_KEY}}": "company", "{{BOX_ID}}": "box_company", "{{BOX_ROOT}}": str(root), "{{AGENT_NAME}}": "Tyler"}.items():
            expected_agents = expected_agents.replace(key, value)
        self.assertEqual((root / "AGENTS.md").read_text(), expected_agents)
        self.assertEqual((root / "AGENTS.md").stat().st_mode & 0o777, 0o640)
        self.assertEqual((directory / "SOUL.md").stat().st_mode & 0o777, 0o600)
        self.assertEqual((directory / "loom-tool-context.json").stat().st_mode & 0o777, 0o600)
        self.assertEqual((directory / "SOUL.md").read_text(), (templates / "SOUL.md").read_text())
        bootstrap = json.loads((directory / "loom-box-owner.json").read_text())
        self.assertEqual(bootstrap["bootstrap_hashes"][str(root / "AGENTS.md")], box_agent.digest(expected_agents.encode()))
        self.assertEqual(bootstrap["bootstrap_hashes"][str(directory / "SOUL.md")], box_agent.digest((templates / "SOUL.md").read_bytes()))
        # The actual bootstrap contract, not a second hand-written profile fixture.
        import importlib.util
        bridge_spec = importlib.util.spec_from_file_location("box_bridge", self.repo / "nix/files/codex-jobs/bridge.py")
        bridge = importlib.util.module_from_spec(bridge_spec)
        bridge_spec.loader.exec_module(bridge)
        tool_config = json.loads((directory / "loom-tool-context.json").read_text())
        static = {k: v for k, v in tool_config["environment"].items() if k in bridge.CONTEXT_ENV}
        static["PATH"] += ":/etc/profiles/per-user/agents/bin:/home/agents/.local/bin"
        roots = bridge.ProjectRoots(self.root / "personal/Projects", [dict(box_id="box_company", projects_root=str(root / "Projects"), environment=static)])
        env, binding = bridge.execution_context(dict(box_id="box_company"), roots, str(self.root / "codex-home"))
        self.assertEqual(env["PATH"], str(directory / "home/bin") + ":/run/current-system/sw/bin")
        self.assertEqual(binding["box_context"], {k: bootstrap[k] for k in ("box_id", "profile", "workdir", "context_revision")})
        self.assertEqual(env["CODEX_HOME"], str(self.root / "codex-home/.codex"))
        from native_boxes import resolve
        self.assertEqual(resolve(self.native, "box_company", "tyler", catalog)[0], directory)
        descriptor = root / ".loom/box.yaml"
        original_descriptor = descriptor.read_bytes()
        value = yaml.safe_load(original_descriptor)
        for schema in ("loom.box.v0.4.1", "loom.box.v0.5"):
            descriptor.write_text(yaml.safe_dump(dict(value, schema_version=schema)))
            self.assertEqual(resolve(self.native, "box_company", "tyler", catalog)[0], directory)
        for invalid in (yaml.safe_dump(dict(value, schema_version="loom.box.future")), "[]", "invalid: [PRIVATE_FIXTURE"):
            descriptor.write_text(invalid)
            with self.assertRaises((ValueError, owner.Conflict)):
                resolve(self.native, "box_company", "tyler", catalog)
        descriptor.write_bytes(original_descriptor)
        marker_path = directory / "loom-box-owner.json"
        original_marker = marker_path.read_bytes()
        for invalid in ("{}", "[", "null"):
            marker_path.write_text(invalid)
            with self.assertRaises((ValueError, owner.Conflict)):
                resolve(self.native, "box_company", "tyler", catalog)
        marker_path.unlink()
        with self.assertRaises(owner.Conflict):
            resolve(self.native, "box_company", "tyler", catalog)
        marker_path.write_bytes(original_marker)
        config_path = directory / "config.yaml"
        original_config = config_path.read_bytes()
        for invalid in ("[]", "terminal: []", "invalid: [PRIVATE_FIXTURE"):
            config_path.write_text(invalid)
            with self.assertRaises((ValueError, owner.Conflict)):
                resolve(self.native, "box_company", "tyler", catalog)
        config_path.unlink()
        with self.assertRaises(ValueError):
            resolve(self.native, "box_company", "tyler", catalog)
        config_path.write_bytes(original_config)
        self.assertEqual(box_agent.prepare(*args, **options)["status"], "preserved")
        config = yaml.safe_load((directory / "config.yaml").read_text())
        self.assertEqual(config["model"], dict(default="gpt-6.1-sol", provider="openai-codex"))
        self.assertEqual(config["profile_context"]["share_inference_provider"], "openai-codex")
        self.assertEqual(config["profile_context"]["environment"]["LOOM_CONFIG_FILE"], "/etc/loom-client.env")
        tools = json.loads((directory / "loom-tool-context.json").read_text())
        self.assertEqual(tools["environment"]["LOOM_CONFIG_FILE"], "/etc/loom-client.env")
        self.assertEqual(tools["environment"]["LOOM_BOX_ID"], "box_company")
        self.assertEqual(tools["environment"]["LOOM_BOX_KEY"], "company")
        self.assertFalse((directory / "skills/fixture-mina-private").exists())
        from agent.skill_utils import get_skill_search_roots
        from agent.prompt_builder import build_skills_system_prompt
        from tools.skills_tool import _skill_catalog
        from tools.skill_manager_tool import _find_skill_in_other_profiles
        from tools.memory_tool import load_on_disk_store
        own_skill = directory / "skills/fixture-tyler-owned"
        own_skill.mkdir()
        (own_skill / "SKILL.md").write_text("---\nname: fixture-tyler-owned\ndescription: Tyler local skill fixture\n---\nLocal fixture instructions.\n")
        with owner.profile_environment(directory):
            search_roots = [p for _, p in get_skill_search_roots()]
            self.assertIn(directory / "skills", search_roots)
            self.assertNotIn(self.native / "skills", search_roots)
            names = [s["name"] for s in _skill_catalog()]
            self.assertIn("fixture-tyler-owned", names)
            self.assertNotIn("fixture-mina-private", names)
            skill_prompt = build_skills_system_prompt(skills_dir_override=directory / "skills")
            self.assertIn("fixture-tyler-owned", skill_prompt)
            self.assertNotIn("fixture-mina-private", skill_prompt)
            self.assertEqual(_find_skill_in_other_profiles("fixture-mina-private"), [])
            memory = load_on_disk_store()
            for target in ("memory", "user"):
                self.assertNotIn("Fixture root private history", memory.format_for_system_prompt(target) or "")
        # The opt-out is neutral-profile only; the root retains its native hint.
        self.assertIn("tyler", [name for name, _ in _find_skill_in_other_profiles("fixture-tyler-owned")])
        registered = json.loads(registry.read_text())
        self.assertEqual(registered["contexts"][0], other)
        self.assertEqual(registered["contexts"][1], dict(profile="tyler", box_id="box_company", root=str(root), paths=[".loom/box.yaml", "AGENTS.md", ".agents/skills"]))
        import capture
        protected_files, inventory = capture.external_context_files(str(registry), self.native, ["tyler"])
        self.assertEqual(inventory[1]["status"], "selected")
        self.assertEqual(inventory[1]["missing_paths"], [".agents/skills"])
        self.assertEqual(len(protected_files), 2)
        (directory / "memories/MEMORY.md").write_text("Live learned state.\n")
        (directory / "SOUL.md").write_text("Live personality edit.\n")
        self.assertIn(str(directory / "SOUL.md"), box_agent.prepare(*args, **options)["drift"])
        self.assertEqual((directory / "memories/MEMORY.md").read_text(), "Live learned state.\n")
        source = dict(host="main", profile="mina", revision=owner.PLATFORM_REVISION)
        request = dict(operation="box_reconcile", source=source, box_id="box_company", profile="tyler", resource="review", expected_revision="absent", expected_id="", token="first", input_hash=owner.digest("first"), spec=dict(schedule="every 60m", prompt="Review company notes", skills=[], status="paused", workdir=str(root)))
        before = dict(os.environ)
        first = owner.execute(jobs, self.native, source, request, boxes_file=catalog)[0]
        self.assertEqual(first["project_id"], "")
        self.assertEqual(first["box"]["box_id"], "box_company")
        self.assertEqual(owner.execute(jobs, self.native, source, request, boxes_file=catalog), [first])
        project_request = dict(request, project_id="project_company", token="project", input_hash=owner.digest("project"))
        project_job = owner.execute(jobs, self.native, source, project_request, boxes_file=catalog)[0]
        self.assertEqual(project_job["project_id"], "project_company")
        paused = owner.execute(jobs, self.native, source, dict(operation="pause_project", source=source, project_id="project_company"), boxes_file=catalog)
        self.assertEqual(paused[0]["id"], project_job["id"])
        self.assertTrue(paused[0]["retired"])
        from native_codex import script_name
        marker = json.loads((directory / "loom-box-owner.json").read_text())
        context = {k: marker[k] for k in ("box_id", "profile", "workdir", "context_revision")}
        for project in ("", "project_coding"):
            coding = dict(request, project_id=project, resource="coding", token="coding" + project, input_hash=owner.digest("coding" + project))
            workdir = root / "Projects/owned" if project else root
            workdir.mkdir(parents=True, exist_ok=True)
            coding["spec"] = dict(request["spec"], workdir=str(workdir), codex=dict(model="gpt-6.1-sol", timeout_seconds=90, sandbox="read-only"))
            created = owner.execute(jobs, self.native, source, coding, boxes_file=catalog)[0]
            self.assertEqual(owner.execute(jobs, self.native, source, coding, boxes_file=catalog), [created])
            native_job = next(j for j in owner.strict_jobs(directory) if j["id"] == created["id"])
            self.assertTrue(native_job["no_agent"])
            self.assertEqual(native_job["workdir"], str(workdir))
            script = directory / "scripts" / native_job["script"]
            sealed = json.loads(script.with_suffix(".json").read_text())
            self.assertEqual(sealed["box_context"], context)
            self.assertEqual(sealed["project_id"], project)
            self.assertNotIn("environment", sealed)
            self.assertNotEqual(script.name, script_name(project, "coding", coding["spec"], dict(context, context_revision="sha256:" + "b" * 64)))
        event = dict(operation="dispatch_event", source=source, project_id="", box_id="box_company", profile="tyler", event=dict(event_id="event_one", job_id="job_one", event_type="job.failed", project_id="", created_at="2026-11-08T09:00:00Z"))
        receipt = owner.execute(jobs, self.native, source, event, boxes_file=catalog)
        self.assertEqual(receipt["box"]["box_id"], "box_company")
        self.assertEqual(owner.execute(jobs, self.native, source, event, boxes_file=catalog)["delivery"], "duplicate")
        from hermes_cli.profile_context import validate_job_context
        with owner.profile_environment(directory):
            saved = owner.strict_jobs(directory)[0]
            validate_job_context(saved)
            saved["origin"]["loom_box_schedule"]["context_revision"] = "stale"
            with self.assertRaises(ValueError):
                validate_job_context(saved)
        self.assertEqual(dict(os.environ), before)

    def test_external_registry_refuses_rebinding_permissions_and_concurrent_edits(self):
        import box_agent
        state = self.root / "state"
        state.mkdir(mode=0o700)
        registry = state / "external-contexts.json"
        box = dict(box_id="box_company", root_path=str(self.root / "company"))
        with box_agent.external_context_registry(registry, self.native, box, "tyler") as publish:
            publish()
        self.assertEqual(registry.stat().st_mode & 0o777, 0o600)
        before = registry.read_bytes()
        with self.assertRaisesRegex(ValueError, "binding differs"):
            with box_agent.external_context_registry(registry, self.native, dict(box, box_id="box_changed"), "tyler"):
                pass
        self.assertEqual(registry.read_bytes(), before)
        registry.chmod(0o666)
        with self.assertRaisesRegex(ValueError, "unsafe"):
            with box_agent.external_context_registry(registry, self.native, box, "tyler"):
                pass
        registry.chmod(0o600)
        state.chmod(0o755)
        with self.assertRaisesRegex(ValueError, "0700"):
            with box_agent.external_context_registry(registry, self.native, box, "tyler"):
                pass
        state.chmod(0o700)
        with box_agent.external_context_registry(registry, self.native, box, "tyler") as publish:
            changed = json.loads(before)
            changed["contexts"][0]["paths"].append("protocols")
            registry.write_text(json.dumps(changed))
            with self.assertRaisesRegex(ValueError, "changed"):
                publish()
        self.assertEqual(json.loads(registry.read_text()), changed)
        root = self.root / "company"
        root.mkdir(mode=0o700)
        with self.assertRaisesRegex(ValueError, "outside"):
            with box_agent.external_context_registry(root / "registry.json", self.native, box, "tyler"):
                pass

    def test_bound_profile_missing_or_broad_auth_policy_fails_closed(self):
        from hermes_constants import set_hermes_home_override, reset_hermes_home_override
        from hermes_cli.profile_context import root_auth_fallback_allowed
        import hermes_yaml as yaml
        profile = self.native / "profiles/tyler"
        self.config(profile, self.root / "box")
        (profile / "loom-box-owner.json").write_text("{}")
        token = set_hermes_home_override(profile)
        try:
            config = yaml.safe_load((profile / "config.yaml").read_text())
            config["profile_context"]["allow_root_auth_fallback"] = True
            (profile / "config.yaml").write_text(yaml.safe_dump(config))
            with self.assertRaises(ValueError):
                root_auth_fallback_allowed("openai-codex")
            (profile / "config.yaml").unlink()
            with self.assertRaises(ValueError):
                root_auth_fallback_allowed("openai-codex")
        finally:
            reset_hermes_home_override(token)


if __name__ == "__main__":
    unittest.main()
