#!/usr/bin/env python3
"""Apply the shipped patch to installed native sources and test disposable homes.

Run with the existing Hermes Python; no model, network, package install or live home.
"""
from contextlib import ExitStack, contextmanager
import hashlib
import importlib
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
from unittest import mock


ROOT = Path(__file__).resolve().parents[1]
PATCHES = [ROOT / "nix/packages/hermes-nix-plugin-selection.patch",
           ROOT / "nix/packages/hermes-nix-plugin-update.patch"]
SCRATCH = ROOT / ".loom-acceptance/hermes-nix-plugin-selection"
MODULES = ("pm.install", "hermes_cli.plugins_admission", "hermes_cli.plugins_cmd")


def load_patched_sources(directory):
    import pm

    source = Path(pm.__file__).resolve().parent.parent
    for name in MODULES:
        relative = Path(*name.split(".")).with_suffix(".py")
        target = directory / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(source / relative, target)
    for patch in PATCHES:
        command = ["patch", "--batch", "-p1", "-d", str(directory), "-i", str(patch)]
        forward = subprocess.run([*command, "--forward", "--dry-run"], capture_output=True, text=True)
        if forward.returncode == 0:
            subprocess.run([*command, "--forward"], check=True, capture_output=True, text=True)
        else:
            reverse = subprocess.run([*command, "--reverse", "--dry-run"], capture_output=True, text=True)
            if reverse.returncode != 0:
                raise RuntimeError("Installed Hermes source neither accepts nor already contains the complete patch:\n"
                                   + forward.stdout + forward.stderr + reverse.stdout + reverse.stderr)
    for name in MODULES:
        module = importlib.import_module(name)
        path = directory / Path(*name.split(".")).with_suffix(".py")
        exec(compile(path.read_bytes(), str(path), "exec"), module.__dict__)


class NixPluginSelectionTest(unittest.TestCase):
    def setUp(self):
        from pm import install, paths
        from pm.environments import install_state_dir
        from pm.lock import Facts
        from pm.packages import Venv

        self.stack = ExitStack()
        self.addCleanup(self.stack.close)
        self.root = Path(self.stack.enter_context(tempfile.TemporaryDirectory(dir=SCRATCH)))
        self.home, self.project = self.root / "home", self.root / "project"
        self.home.mkdir()
        self.project.mkdir()
        self.stack.enter_context(mock.patch.dict(os.environ, {
            "HOME": str(self.root), "HERMES_HOME": str(self.home),
            "HERMES_INSTALL_ROOT": str(self.project), "HERMES_RUNTIME_DIR": str(self.root / "tools"),
            "HERMES_DISABLE_LAZY_INSTALLS": "1",
        }))
        self.stack.enter_context(mock.patch.object(paths, "repo_root", lambda: self.project))
        self.install = install
        self.stamp = self.stack.enter_context(mock.patch.object(
            Venv, "expected_stamp", side_effect=AssertionError("unexpected environment stamp")))
        self.apply = self.stack.enter_context(mock.patch.object(
            Venv, "apply", side_effect=AssertionError("unexpected environment build")))
        self.record = self.stack.enter_context(mock.patch.object(
            Facts, "record_state", side_effect=AssertionError("unexpected environment selection")))
        self.package_stamp = self.project / "install-stamp.json"
        self.write_stamp()
        self.config = self.home / "config.yaml"
        self.before = b'# retain comment\nmodel: "untouched"\nfeature: yes\nplugins: {enabled: [], disabled: []}\n'
        self.config.write_bytes(self.before)
        self.state = install_state_dir(self.project)
        self.state.mkdir(parents=True)
        self.facts = self.state / "facts.json"
        self.facts.write_bytes(b'{}\n')
        self.addCleanup(self.assert_environment_untouched)
        self.plugin("plain")

    def write_stamp(self, distribution="nix", mechanism="external"):
        self.package_stamp.write_text(json.dumps({
            "commit": "d923a5eb487a17c0a68801bba9ef0f56c81040a8", "baseVersion": "0.22.2",
            "distribution": distribution, "updateMechanism": mechanism,
        }))

    def plugin(self, name, declaration="", *, home=None, pyproject=None):
        directory = (home or self.home) / "plugins" / name
        directory.mkdir(parents=True, exist_ok=True)
        (directory / "plugin.yaml").write_text(f"name: {name}\n" + declaration)
        if pyproject is not None:
            (directory / "pyproject.toml").write_text(pyproject)
        return directory

    def sync(self, enabled=("plain",), disabled=(), extra_dirs=(), **kwargs):
        from pm.plugin_inputs import Selection
        from pm import receipt

        expected = kwargs.pop("expected_config", hashlib.sha256(self.config.read_bytes()).hexdigest())
        with receipt.worker_context("nix-selection-test"):
            try:
                self.install.sync_venv(explicit=True, plugins=Selection({
                    "home": str(self.home), "enabled": list(enabled), "disabled": list(disabled),
                    "extra_dirs": [str(path) for path in extra_dirs], "expected_config": expected,
                }), **kwargs)
            finally:
                self.last_receipt = receipt.last_completed()

    def assert_environment_untouched(self):
        self.apply.assert_not_called()
        self.record.assert_not_called()
        self.assertEqual(self.facts.read_bytes(), b'{}\n')
        self.assertFalse((self.project / "uv.lock").exists())
        self.assertFalse((self.state / "generations").exists())

    def test_enable_disable_preserves_yaml_and_native_receipt(self):
        import hermes_yaml

        self.sync()
        self.assertIn(b'# retain comment\nmodel: "untouched"\n', self.config.read_bytes())
        before, after = hermes_yaml.safe_load(self.before), hermes_yaml.safe_load(self.config.read_bytes())
        self.assertEqual({key: after[key] for key in ("model", "feature")},
                         {key: before[key] for key in ("model", "feature")})
        self.assertEqual(after["plugins"]["enabled"], ["plain"])
        self.assertEqual(self.last_receipt["outcome"], "ok")
        self.assertFalse(self.last_receipt["venv_rebuild"]["ok"])
        self.sync(enabled=(), disabled=("plain",))
        self.assertEqual(hermes_yaml.safe_load(self.config.read_bytes())["plugins"]["disabled"], ["plain"])
        self.assertFalse((self.state / "publication.json").exists())
        self.stamp.assert_not_called()

    def staged_sync(self, declaration=""):
        from pm import receipt
        from pm.plugin_inputs import StagedUpdate
        from pm.store import tree_digest

        target = self.home / 'plugins/plain'
        staged = self.root / 'candidate'
        staged.mkdir()
        (staged / 'plugin.yaml').write_text('name: plain\n' + declaration)
        self.config.write_text('plugins: {enabled: [plain], disabled: []}\n')
        old = {'plain': {'source': 'old'}}
        (target.parent / '.install-metadata.json').write_text(json.dumps(old))
        with receipt.worker_context('nix-update-test'):
            try:
                self.install.sync_venv(explicit=True, plugins=StagedUpdate({
                    'target': str(target), 'staged': str(staged),
                    'target_digest': tree_digest(target), 'old_metadata': old,
                    'new_metadata': {'plain': {'source': 'new'}},
                }))
            finally:
                self.last_receipt = receipt.last_completed()
        return target

    def test_enabled_dependency_free_code_update_uses_native_publication(self):
        target = self.staged_sync('version: 2\n')
        self.assertIn('version: 2', (target / 'plugin.yaml').read_text())
        self.assertEqual(json.loads((target.parent / '.install-metadata.json').read_text())['plain']['source'], 'new')
        self.assertEqual(self.last_receipt['outcome'], 'ok')
        self.stamp.assert_not_called()

    def test_code_update_with_new_dependencies_preserves_old_plugin(self):
        from pm.package import InstallError
        before = (self.home / 'plugins/plain/plugin.yaml').read_bytes()
        with self.assertRaisesRegex(InstallError, 'Nix owns'):
            self.staged_sync('python_dependencies: [example==1]\n')
        self.assertEqual((self.home / 'plugins/plain/plugin.yaml').read_bytes(), before)
        self.assertEqual(self.last_receipt['outcome'], 'failed')

    def test_native_command_delta_preserves_other_selection_and_alias_rules(self):
        from hermes_cli import plugins_cmd
        from pm import client
        import hermes_yaml

        self.config.write_text('plugins: {enabled: [existing], disabled: [legacy, plain]}\nmodel: "untouched"\n')
        with mock.patch.object(client, "sync_venv", self.install.sync_venv):
            plugins_cmd._set_plugin_enabled("plain", enable=True, aliases={"legacy"})
            selected = hermes_yaml.safe_load(self.config.read_bytes())["plugins"]
            self.assertEqual(selected, {"enabled": ["existing", "plain"], "disabled": []})
            plugins_cmd._set_plugin_enabled("plain", enable=False)
        self.assertEqual(hermes_yaml.safe_load(self.config.read_bytes())["plugins"],
                         {"enabled": ["existing"], "disabled": ["plain"]})
        self.assertIn(b'model: "untouched"', self.config.read_bytes())

    def test_dependency_aliases_urls_and_pyprojects_are_refused(self):
        from pm.package import InstallError

        cases = [
            ("pip", "pip_dependencies: [example==1]\n", None),
            ("python", "python_dependencies: [example==1]\n", None),
            ("url", 'python_dependencies: ["example @ https://example.invalid/example.whl"]\n', None),
            ("project", "", '[project]\nname="example"\nversion="1"\ndependencies=[]\n'),
        ]
        for name, declaration, project in cases:
            with self.subTest(name=name):
                self.plugin(name, declaration, pyproject=project)
                with self.assertRaisesRegex(InstallError, "Nix owns"):
                    self.sync(enabled=(name,))
                self.assertEqual(self.config.read_bytes(), self.before)
                self.assertEqual(self.last_receipt["outcome"], "failed")
                self.assertFalse((self.state / "publication.json").exists())

    def test_live_profiles_and_implicit_memory_providers_are_checked(self):
        from pm.package import InstallError

        sibling = self.home / "profiles/sibling"
        self.plugin("dependency", 'pip_dependencies: ["example @ https://example.invalid/a.whl"]\n', home=sibling)
        for config in ("plugins: {enabled: [dependency]}\n", "memory: {provider: dependency}\n"):
            with self.subTest(config=config):
                (sibling / "config.yaml").write_text(config)
                with self.assertRaisesRegex(InstallError, "dependency"):
                    self.sync()
                self.assertEqual(self.config.read_bytes(), self.before)

    def test_extra_dirs_members_and_urls_are_checked(self):
        from pm.package import InstallError

        for declaration in ("python_dependencies: [example==1]\n",
                            'pip_dependencies: ["example @ https://example.invalid/a.whl"]\n'):
            with self.subTest(declaration=declaration):
                directory = self.plugin("extra", declaration, home=self.root / "extra-home")
                with self.assertRaisesRegex(InstallError, "extra"):
                    self.sync(extra_dirs=(directory,))
                self.assertEqual(self.config.read_bytes(), self.before)

    def test_native_external_python_semantics_are_preserved(self):
        self.plugin("external", 'python_runtime: external\npython_dependencies: ["example @ https://example.invalid/a.whl"]\n',
                    pyproject='[project]\nname="external"\nversion="1"\ndependencies=["example==1"]\n')
        self.sync(enabled=("external",))
        self.stamp.assert_not_called()

    def test_only_exact_nix_external_stamp_takes_new_path(self):
        for distribution, mechanism in (("nix", "self"), ("docker", "external"), (None, "external")):
            with self.subTest(distribution=distribution, mechanism=mechanism):
                self.write_stamp(distribution, mechanism)
                with self.assertRaisesRegex(AssertionError, "unexpected environment stamp"):
                    self.sync()
                self.assertEqual(self.config.read_bytes(), self.before)
        self.package_stamp.unlink()
        with self.assertRaisesRegex(AssertionError, "unexpected environment stamp"):
            self.sync()

    def test_stale_digest_and_unreadable_stamp_fail_closed(self):
        from pm.package import InstallError

        with self.assertRaisesRegex(ValueError, "changed since"):
            self.sync(expected_config="stale")
        for content in ("[", "[]"):
            self.package_stamp.write_text(content)
            with self.assertRaisesRegex(InstallError, "stamp"):
                self.sync()
        self.assertEqual(self.config.read_bytes(), self.before)

    def test_concurrent_active_and_sibling_config_edits_survive(self):
        from pm.publication import PluginSelection

        sibling = self.home / "profiles/sibling/config.yaml"
        sibling.parent.mkdir(parents=True)
        sibling.write_text("plugins: {enabled: []}\n")
        publish = PluginSelection.publish
        for edited in (self.config, sibling):
            with self.subTest(path=edited):
                def concurrent(change, project):
                    edited.write_bytes(edited.read_bytes() + b"user_choice: preserve\n")
                    return publish(change, project)
                with mock.patch.object(PluginSelection, "publish", concurrent):
                    with self.assertRaisesRegex(ValueError, "changed"):
                        self.sync()
                self.assertIn(b"user_choice: preserve", edited.read_bytes())
                self.assertFalse((self.state / "publication.json").exists())

    def test_failed_config_write_rolls_back(self):
        from pm import publication

        durable = publication.durable_write_bytes
        def fail(path, data):
            if path == self.config:
                raise OSError("fixture disk full")
            return durable(path, data)
        with mock.patch.object(publication, "durable_write_bytes", fail):
            with self.assertRaisesRegex(OSError, "disk full"):
                self.sync()
        self.assertEqual(self.config.read_bytes(), self.before)
        self.assertFalse((self.state / "publication.json").exists())

    def test_native_journal_recovers_each_durable_boundary(self):
        from hermes_cli import runtime_state
        from pm import publication

        recover, atomic = runtime_state.recover_publication, runtime_state._atomic_bytes
        journal = self.state / "publication.json"
        for phase in ("journal", "payload", "commit"):
            with self.subTest(phase=phase):
                self.config.write_bytes(self.before)
                def interrupt(path, data):
                    atomic(path, data)
                    if ((phase == "journal" and path == journal)
                            or (phase == "payload" and path == self.config)
                            or (phase == "commit" and path == journal and json.loads(data).get("committed"))):
                        raise SystemExit("fixture interrupted")
                def leave_pending(project):
                    if not journal.exists():
                        recover(project)
                with mock.patch.object(publication, "durable_write_bytes", interrupt), \
                        mock.patch.object(runtime_state, "_atomic_bytes", interrupt), \
                        mock.patch.object(runtime_state, "recover_publication", leave_pending):
                    with self.assertRaisesRegex(SystemExit, "interrupted"):
                        self.sync()
                self.assertTrue(journal.exists())
                with runtime_state.runtime_lock(self.project) as held:
                    self.assertTrue(held)
                    recover(self.project)
                    recover(self.project)
                if phase == "commit":
                    self.assertIn(b"plain", self.config.read_bytes())
                else:
                    self.assertEqual(self.config.read_bytes(), self.before)
                self.assertFalse(journal.exists())

    def test_publication_uses_native_install_lock(self):
        from hermes_cli import runtime_state
        from pm.publication import PluginSelection

        lock, publish = runtime_state.runtime_lock, PluginSelection.publish
        holding = []
        @contextmanager
        def tracked(project, **kwargs):
            with lock(project, **kwargs) as held:
                holding.append(held)
                try:
                    yield held
                finally:
                    holding.pop()
        def checked(change, project):
            self.assertEqual(holding, [True])
            return publish(change, project)
        with mock.patch.object(runtime_state, "runtime_lock", tracked), \
                mock.patch.object(PluginSelection, "publish", checked):
            self.sync()

    def test_extras_are_refused_without_environment_changes(self):
        from pm.package import InstallError

        with self.assertRaisesRegex(InstallError, "Nix owns"):
            self.sync(extras=["voice"])
        self.assertEqual(self.config.read_bytes(), self.before)

    def test_cli_nix_remedy_and_ordinary_refusal_hint(self):
        from hermes_cli import plugins_cmd
        from hermes_cli.plugins_admission import AdmissionRefused
        from pm import client
        from rich.console import Console

        self.plugin("dependency", "pip_dependencies: [example==1]\n")
        for failure in (None, ValueError("ordinary refusal")):
            output = io.StringIO()
            console = Console(file=output, force_terminal=False, width=200)
            with mock.patch.object(client, "sync_venv", self.install.sync_venv if failure is None
                                   else mock.Mock(side_effect=failure)):
                with self.assertRaises(AdmissionRefused):
                    plugins_cmd._admit_and_save_plugin_sets({"dependency"}, set(), console=console)
            if failure is None:
                self.assertIn("Nix package", output.getvalue())
                self.assertNotIn("hermes pm install", output.getvalue())
            else:
                self.assertIn("hermes pm install", output.getvalue())
            self.assertEqual(self.config.read_bytes(), self.before)


if __name__ == "__main__":
    SCRATCH.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(dir=SCRATCH) as temporary:
        with mock.patch.dict(os.environ, {"HOME": temporary, "HERMES_HOME": temporary,
                                         "HERMES_RUNTIME_DIR": str(Path(temporary) / "tools")}):
            load_patched_sources(Path(temporary) / "source")
            unittest.main(verbosity=2)
