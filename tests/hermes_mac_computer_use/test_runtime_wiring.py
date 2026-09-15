"""Required W4 render readback and actual packaged native dispatch; no live peers.

Run the declared Go config gate first to refresh the actual Nix render receipt.
If invoked standalone without a receipt this file performs that one render gate.
Stress repeats only selectors and discovery/refusal, never Nix evaluation.
"""
from __future__ import annotations
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

REPO = Path(__file__).resolve().parents[2]
RECEIPT = REPO / '.loom-acceptance/main-mac-wiring/render.json'
PYTHON = '/nix/store/5c4bns88zxadxva4cghfsgc4bi719gy8-hermes-agent-env/bin/python3'
HERMES = Path('/nix/store/b9z4vy2wns4zd92ya4lh2bc9lfrkb2jr-hermes-agent-0.21.0')
OVERLAY = HERMES / 'share/loom-hermes-native'
CONNECTOR = Path('/nix/store/nbmnpy28wbrv87zhgsng61kqizx75ssw-loom-mac-computer-use-bridge-0.1.0')

# Executed in a clean subprocess so discovery of the frozen upstream suite does
# not change this test's imports. Every native owner is the real packaged output.
PROBE = r'''
import hashlib, json, os, pathlib, socket, subprocess, sys, tempfile, time
from types import SimpleNamespace
from unittest import mock
socket.socket.connect = lambda *a, **kw: (_ for _ in ()).throw(AssertionError('network forbidden'))
mode, overlay, connector, tests, receipt = sys.argv[1:]
render = json.loads(pathlib.Path(receipt).read_text())
from tools.computer_use import cua_backend as cua, tool as native, backend
for m in (cua, native, backend):
    assert pathlib.Path(m.__file__).is_relative_to(pathlib.Path(overlay)), m.__file__
identity = json.loads((pathlib.Path(overlay) / 'patch-identity.json').read_text())
assert identity['patch_sha256'] == hashlib.sha256((pathlib.Path(tests).parents[1] / 'nix/patches/hermes-mac-computer-use.patch').read_bytes()).hexdigest()
for p, sha in identity['files'].items():
    assert hashlib.sha256((pathlib.Path(overlay) / p).read_bytes()).hexdigest() == sha
for entry in ('hermes', 'hermes-agent', 'hermes-acp'):
    assert overlay in (pathlib.Path(overlay).parents[1] / 'bin' / entry).read_text()
# Native defaults/readback, no model construction or profile mutation.
if mode == 'baseline':
    from hermes_cli.config import load_config
    from hermes_cli.tools_config import _get_platform_tools
    from toolsets import resolve_multiple_toolsets
    from agent.skill_utils import get_disabled_skill_names
    from tools.skills_tool import _is_skill_disabled
    config_path = pathlib.Path(os.environ['HERMES_HOME']) / 'config.yaml'
    for off, on in [('Old', 'OldOn'), ('Mina', 'MinaOn')]:
        resolved = []
        for label in (off, on):
            expected = render[label]['Baseline']
            config_path.write_text(json.dumps(expected))
            config = load_config()
            assert config['skills']['disabled'] == expected['skills']['disabled']
            disabled = get_disabled_skill_names()
            assert ('computer-use' in disabled) == (label == off)
            assert _is_skill_disabled('computer-use') == (label == off)
            names = _get_platform_tools(config, 'cli', include_default_mcp_servers=False)
            assert 'computer_use' in names
            tools = resolve_multiple_toolsets(sorted(names))
            assert tools.count('computer_use') == 1
            assert all(t in tools for t in ('terminal', 'browser_navigate', 'skills_list'))
            for section in ('memory', 'skills'):
                assert config[section]['write_approval'] == label.startswith('Old')
            assert config['skills']['guard_agent_created'] == label.startswith('Old')
            resolved.append(set(tools))
        assert resolved[0] == resolved[1], 'unrelated tool selection changed'
    print('actual pinned baseline/skill/native toolset readback passed')
    raise SystemExit()

sys.path.insert(0, tests)
from test_mac_endpoint import Harness
with tempfile.TemporaryDirectory(prefix='loom-w4-native-') as tmp:
    h = Harness(tmp)
    launcher = str(pathlib.Path(connector) / 'bin/loom-mac-computer-use-bridge')
    # Only the public binding path is relocated to the disposable Harness.
    # The actual installed managed launcher runs unchanged, with fake SSH in
    # that binding. Its fixed SSH vector still goes through normal validation.
    os.environ.update(HERMES_MAC_BINDING=str(h.config), HERMES_CUA_DRIVER_CMD=launcher)
    native._should_route_through_aux_vision = lambda: False
    native._route_capture_through_aux_vision = lambda *a, **kw: (_ for _ in ()).throw(AssertionError('model forbidden'))
    cua.sys = native.sys = SimpleNamespace(platform='linux')
    popen = subprocess.Popen
    launches = []
    def only_connector(command, *args, **kwargs):
        assert isinstance(command, (tuple, list)) and command[0] == launcher, command
        launches.append(command)
        return popen(command, *args, **kwargs)
    with mock.patch.object(subprocess, 'Popen', side_effect=AssertionError('discovery process forbidden')), mock.patch.object(subprocess, 'run', side_effect=AssertionError('discovery probe forbidden')):
        assert native.check_computer_use_requirements()
        assert native.get_computer_use_schema()['name'] == 'computer_use'
    assert not h.calls() and not (h.root / 'ssh-args.json').exists()
    if mode == 'discovery':
        # Absence/different profile is local baseline selection, never a remote
        # fallback test. Do not invoke a local backend or inspect the real Mac.
        for label in ('Old', 'Mina'):
            assert not render[label]['Env'].get('HERMES_MAC_BINDING')
        with mock.patch.dict(os.environ, {'HERMES_MAC_BINDING': '', 'HERMES_CUA_DRIVER_CMD': ''}):
            assert not cua._remote_mac_enabled()
        with mock.patch.object(subprocess, 'Popen', side_effect=only_connector):
            b = cua.CuaDriverBackend(permission_mode='standard')
            with mock.patch.object(native, '_get_backend', return_value=b), mock.patch.object(native, '_approval_callback', return_value='approve_once'):
                refusal = json.loads(native.handle_computer_use({'action': 'set_value', 'value': 'fixture', 'element': 1}))
            assert refusal['code'] == 'mac_policy_refused' and not refusal['ok'], refusal
            assert not launches and not h.calls(), 'refusal launched a process'
        print('offline discovery/no probe/native refusal passed')
        raise SystemExit()
    if mode == 'offline': h.set(ssh='offline')
    if mode == 'missing': os.environ['HERMES_MAC_BINDING'] = str(h.root / 'absent.json')
    b = cua.CuaDriverBackend(permission_mode='standard')
    started = time.monotonic()
    try:
        with mock.patch.object(subprocess, 'Popen', side_effect=only_connector), mock.patch.object(b._session, '_call_tool_via_cli', side_effect=AssertionError('fallback forbidden')):
            def started_backend(*a, **kw):
                b.start()
                return b
            with mock.patch.object(native, '_get_backend', side_effect=started_backend):
                result = native.handle_computer_use({'action': 'capture', 'app': 'Safari', 'pid': 4242, 'window_id': 701, 'mode': 'som'})
            if isinstance(result, str): result=json.loads(result)
            assert result['target_node'] == 'macbook', result
            if mode in ('offline', 'missing'):
                assert not result['ok'] and result['delivery'] == 'not_sent', result
                assert result['phase'] == ('connect' if mode == 'offline' else 'admission'), result
                assert result['code'] == ('mac_unreachable' if mode == 'offline' else 'mac_host_identity_mismatch'), result
                assert not h.calls()
                assert (h.root / 'ssh-args.json').exists() == (mode == 'offline'), 'fixture did not reach actual transport'
                assert time.monotonic() - started < 12
            else:
                assert result['_multimodal'] and result['delivery'] == 'observed', result
                assert any(c.get('type') == 'image_url' and c['image_url']['url'].startswith('data:image/png;base64,') for c in result['content']), result
                argv = json.loads((h.root / 'ssh-args.json').read_text())
                assert argv[-5:] == ['-l', 'fixture', '--', 'synthetic.invalid', '/usr/local/libexec/loom-mac-endpoint'], argv
                assert '-F' in argv and '/dev/null' in argv and '-T' in argv
                h.set(disconnect=True)
                with mock.patch.object(native, '_get_backend', return_value=b), mock.patch.object(native, '_approval_callback', return_value='approve_once'):
                    result=json.loads(native.handle_computer_use({'action':'click', 'element':1}))
                assert result['delivery'] == 'unknown', result
                assert len([c for c in h.calls() if c['name']=='click']) == 1
                with mock.patch.object(native, '_get_backend', return_value=b), mock.patch.object(native, '_approval_callback', return_value='approve_once'):
                    result=json.loads(native.handle_computer_use({'action':'click', 'element':1}))
                assert not result['ok']
                assert len([c for c in h.calls() if c['name']=='click']) == 1, 'unknown input replayed'
                assert not any(c['name']=='set_value' for c in h.calls())
    finally: b.stop()
    print(mode + ': packaged native dispatch passed; no real GUI/SSH/model')
'''


class RuntimeWiringTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        if not RECEIPT.exists():
            p = subprocess.run(['go', 'test', '-p', '2', '-count=1', '-timeout=20m', './internal/config', '-run', '^TestNixMacComputerUseRuntimeRender$'], cwd=REPO, env=dict(os.environ, GOMAXPROCS='2'), capture_output=True, text=True, timeout=1200)
            if p.returncode: raise AssertionError(p.stdout + p.stderr)
        cls.render = json.loads(RECEIPT.read_text())
        assert cls.render['Connector']['aarch64-darwin'] == str(CONNECTOR)
        assert Path(PYTHON).exists() and OVERLAY.is_dir()

    def probe(self, mode):
        with tempfile.TemporaryDirectory(prefix='loom-w4-profile-') as tmp:
            env = dict(PATH='/usr/bin:/bin', HOME=tmp, HERMES_HOME=tmp, PYTHONPATH=str(OVERLAY), HERMES_DISABLE_LAZY_INSTALLS='1', HERMES_SKIP_NODE_BOOTSTRAP='1')
            p = subprocess.run([PYTHON, '-B', '-c', PROBE, mode, str(OVERLAY), str(CONNECTOR), str(Path(__file__).parent), str(RECEIPT)], cwd=tmp, env=env, capture_output=True, text=True, timeout=45)
            self.assertEqual(p.returncode, 0, p.stdout + p.stderr)

    def test_realized_profile_launchers(self):
        # Evaluate only the real runtime options with native Darwin packages.
        # The separate Go gate renders the complete Linux host. No NixOS system
        # is built or activated here; these are small immutable launcher outputs.
        nix = '/nix/var/nix/profiles/default/bin/nix'
        expr = r'''let f = (import ./tests/nix/source-flake.nix {}); pkgs = f.inputs.nixpkgs.legacyPackages.aarch64-darwin;
          make = name: let c = pkgs.lib.evalModules {
            specialArgs = { inherit pkgs; self = f; };
            modules = [ ./nix/modules/loom-morathustra.nix { _module.check = false; loom.${name} = { enable = true; macComputerUseEnabled = true; }; } ];
          }; r = c.config.loom.${name}; in { home = r.hermesHome; out = toString r.interactivePackage; drv = r.interactivePackage.drvPath; };
          in map make [ "morathustra" "mina" ]'''
        p = subprocess.run([nix, '--extra-experimental-features', 'nix-command flakes', 'eval', '--offline', '--impure', '--json', '--expr', expr], cwd=REPO, capture_output=True, text=True, timeout=60)
        self.assertEqual(p.returncode, 0, p.stderr)
        outputs = json.loads(p.stdout)
        for r in outputs:
            if not Path(r['out']).exists():
                p = subprocess.run([nix, '--extra-experimental-features', 'nix-command flakes', 'build', '--offline', '--no-link', r['drv'] + '^*'], cwd=REPO, capture_output=True, text=True, timeout=60)
                self.assertEqual(p.returncode, 0, p.stderr)
            for home in (r['home'], '/tmp/other-profile'):
                for entry in ('hermes', 'hermes-agent', 'hermes-acp'):
                    with self.subTest(home=home, entry=entry), tempfile.TemporaryDirectory(prefix='loom-w4-entry-') as tmp:
                        # Stop at Python startup, before Hermes profile/config
                        # access. Only the launcher sees the canonical selector.
                        hook = Path(tmp) / 'sitecustomize.py'
                        hook.write_text('import os,json,sys\nprint(json.dumps({"env":dict(os.environ), "path":sys.path}),flush=True)\nos._exit(0)\n')
                        env = dict(PATH='/usr/bin:/bin', HOME=tmp, HERMES_HOME=home, PYTHONPATH=tmp, HERMES_MAC_BINDING='inherited-binding', HERMES_CUA_DRIVER_CMD='inherited-command')
                        p = subprocess.run([r['out'] + '/bin/' + entry, '--version'], cwd=tmp, env=env, capture_output=True, text=True, timeout=10)
                        self.assertEqual(p.returncode, 0, p.stderr)
                        actual=json.loads(p.stdout)
                        self.assertIn(str(OVERLAY), actual['path'])
                        self.assertEqual(actual['env']['HERMES_HOME'], home)
                        if home == r['home']:
                            self.assertEqual(actual['env']['HERMES_CUA_DRIVER_CMD'], str(CONNECTOR / 'bin/loom-mac-computer-use-bridge'))
                            self.assertEqual(actual['env']['HERMES_MAC_BINDING'], '/etc/loom-mac-computer-use/binding.json')
                        else:
                            self.assertNotIn('HERMES_CUA_DRIVER_CMD', actual['env'])
                            self.assertNotIn('HERMES_MAC_BINDING', actual['env'])
        (RECEIPT.parent / 'darwin-launchers.json').write_text(json.dumps(outputs, indent=2))

    def test_pinned_baseline_skill_toolset_readback(self):
        self.probe('baseline')

    def test_packaged_native_capture_unknown_input_no_replay(self):
        self.probe('online')

    def test_packaged_native_offline_nonfatal(self):
        self.probe('offline')

    def test_packaged_native_missing_binding_nonfatal(self):
        self.probe('missing')

    def test_discovery_without_probe_and_set_value_refusal(self):
        self.probe('discovery')

    def test_actual_agents_startup_preserves_explicit_profile(self):
        for label in ('OldOn', 'MinaOn'):
            r = self.render[label]
            for home in (None, r['Home'], '/tmp/other-profile'):
                with self.subTest(runtime=label, home=home), tempfile.TemporaryDirectory(prefix='loom-w4-shell-') as tmp:
                    shell = Path(tmp) / 'agents.bashrc'
                    shell.write_text(r['Shell'])
                    env = dict(PATH='/usr/bin:/bin', HOME=tmp, TERM='dumb', LOOM_SHELL_WELCOMED='1', HERMES_INFERENCE_MODEL='other-model', HERMES_TUI_PROVIDER='other-provider')
                    if home is not None: env['HERMES_HOME'] = home
                    command = 'source "$1"; exec "$2" -B -c "import os,json; print(json.dumps(dict(os.environ)))"'
                    p = subprocess.run(['/bin/bash', '--noprofile', '--norc', '-ic', command, 'fixture', str(shell), PYTHON], env=env, capture_output=True, text=True, timeout=5)
                    self.assertEqual(p.returncode, 0, p.stderr)
                    actual = json.loads(p.stdout)
                    self.assertEqual(actual['HERMES_HOME'], home or r['Home'])
                    self.assertEqual(actual['HOME'], tmp)
                    self.assertNotIn('HERMES_MAC_BINDING', actual)
                    self.assertNotIn('HERMES_CUA_DRIVER_CMD', actual)
                    if home == '/tmp/other-profile':
                        self.assertEqual(actual['HERMES_INFERENCE_MODEL'], 'other-model')
                        self.assertEqual(actual['HERMES_TUI_PROVIDER'], 'other-provider')
                    else:
                        self.assertEqual(actual['HERMES_INFERENCE_MODEL'], r['Env']['HERMES_INFERENCE_MODEL'])
                        self.assertEqual(actual['HERMES_TUI_PROVIDER'], r['Env']['HERMES_TUI_PROVIDER'])

    def test_selected_invocation_and_other_profile_refusal(self):
        for label in ('OldOn', 'MinaOn'):
            r = self.render[label]
            for home in (r['Home'], '/tmp/another-hermes-profile', '', None, r['Home'] + '-retired'):
                with self.subTest(runtime=label, home=home), tempfile.TemporaryDirectory(prefix='loom-w4-selector-') as tmp:
                    env = dict(PATH='/usr/bin:/bin', HOME=tmp, HERMES_MAC_BINDING='inherited-binding', HERMES_CUA_DRIVER_CMD='inherited-launcher', HERMES_INFERENCE_MODEL='unrelated-model', HERMES_TUI_PROVIDER='unrelated-provider')
                    if home is not None: env['HERMES_HOME']=home
                    # Exact rendered --run body; no product source or native patch
                    # is replaced. Only its following exec is an env observer.
                    program = r['Selector'] + '\nexec "$1" -B -c "import os,json; print(json.dumps(dict(os.environ)))"'
                    p = subprocess.run(['/bin/bash', '-c', program, 'fixture', PYTHON], env=env, capture_output=True, text=True, timeout=5)
                    self.assertEqual(p.returncode, 0, p.stderr)
                    actual=json.loads(p.stdout)
                    for key in ('HERMES_MAC_BINDING', 'HERMES_CUA_DRIVER_CMD'):
                        if home == r['Home']: self.assertEqual(actual[key], r['Env'][key])
                        else: self.assertNotIn(key, actual)
                    self.assertEqual(actual['HERMES_INFERENCE_MODEL'], 'unrelated-model')
                    self.assertEqual(actual['HERMES_TUI_PROVIDER'], 'unrelated-provider')
                    self.assertEqual(actual.get('HERMES_HOME'), home)


def load_tests(loader, tests, pattern):
    if os.environ.get('LOOM_MAC_WIRING_STRESS_ONLY') == '1':
        suite=unittest.TestSuite()
        for _ in range(20):
            suite.addTest(RuntimeWiringTests('test_selected_invocation_and_other_profile_refusal'))
            suite.addTest(RuntimeWiringTests('test_discovery_without_probe_and_set_value_refusal'))
        return suite
    return tests

if __name__ == '__main__': unittest.main()
