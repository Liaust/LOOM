"""Required package patch/import gates; optional REAL realized-output probe.

Default discovery runs the exact postInstall patch program against the pinned
installed wheel in a disposable output. That is not Nix realization proof.
The separate --installed HERMES CONNECTOR command verifies realized outputs
using their own import path/interpreter and synthetic native calls only.
"""
from __future__ import annotations
import ast
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import unittest

REPO = Path(__file__).resolve().parents[2]
LOCK = json.loads((REPO / 'nix/locks/mac-computer-use.json').read_text())
if not os.environ.get('HERMES_NATIVE_ENV'):
    raise RuntimeError('Set HERMES_NATIVE_ENV to the pinned Hermes Python environment before running packaging tests')
ENV = Path(os.environ['HERMES_NATIVE_ENV'])
HERMES_PACKAGE = REPO / 'nix/packages/hermes-agent.nix'
BRIDGE_PACKAGE = REPO / 'nix/packages/mac-computer-use-bridge.nix'
PATCH = REPO / 'nix/patches/hermes-mac-computer-use.patch'


def patch_program():
    text = HERMES_PACKAGE.read_text()
    program = text.split("<<'PYMACPATCH'\n", 1)[1].split('\nPYMACPATCH', 1)[0]
    for name, value in {
        '${../locks/mac-computer-use.json}': str(REPO / 'nix/locks/mac-computer-use.json'),
        '${upstream.hermesVenv}': str(ENV), '${../patches/hermes-mac-computer-use.patch}': str(PATCH),
        '${patchTool}/bin/patch': '/usr/bin/patch',
    }.items():
        program = program.replace(name, value)
    assert '${' not in program
    return program


def native_probe(overlay, connector=None, gateway=False):
    # All imports originate in the supplied OUTPUT overlay. Unlike the prior
    # source-copy tests this probe never patches or prepends a source checkout.
    # The GUI/SSH observer is the existing disposable subprocess fixture.
    return r'''
import hashlib, json, os, pathlib, socket, subprocess, sys, tempfile
from unittest import mock
from types import SimpleNamespace
socket.socket.connect = lambda *a, **kw: (_ for _ in ()).throw(AssertionError('network forbidden'))
output = pathlib.Path(sys.argv[1])
fixture_tests = sys.argv[2]
with tempfile.TemporaryDirectory(prefix='loom-installed-native-') as tmp:
    os.environ.update(HERMES_HOME=tmp, HERMES_DISABLE_LAZY_INSTALLS='1')
    if GATEWAY_BOOTSTRAP:
        import hermes_cli.main
        import gateway.run
    from tools.computer_use import cua_backend as cua, tool as native, backend
    for module in (cua, native, backend):
        assert pathlib.Path(module.__file__).resolve().is_relative_to(output.resolve()), module.__file__
    receipt = json.loads((output / 'patch-identity.json').read_text())
    for name, expected in receipt['files'].items():
        assert hashlib.sha256((output / name).read_bytes()).hexdigest() == expected
    assert not cua._remote_mac_enabled()
    assert native.get_computer_use_schema()['name'] == 'computer_use'
    sys.path.insert(0, fixture_tests)
    from test_mac_endpoint import Harness
    h = Harness(tmp)
    os.environ.update(HERMES_MAC_BINDING=str(h.config), HERMES_CUA_DRIVER_CMD=str(h.launcher))
    # Use the REAL packaged bridge module while keeping SSH and Darwin probes
    # confined to the synthetic Harness. No real endpoint or driver runs.
    if len(sys.argv) > 3:
        connector = pathlib.Path(sys.argv[3])
        h.launcher.write_text('#!' + sys.executable + ' -I\nimport sys\nsys.path.insert(0, ' + repr(str(connector / 'share/loom-mac-computer-use')) + ')\nimport bridge\nbridge.main()\n')
    popen = subprocess.Popen
    def only_fixture(command, *args, **kwargs):
        assert isinstance(command, (tuple, list)) and command[0] == str(h.launcher), 'non-fixture process forbidden'
        return popen(command, *args, **kwargs)
    native._should_route_through_aux_vision = lambda: False
    native._route_capture_through_aux_vision = lambda *a, **kw: (_ for _ in ()).throw(AssertionError('model forbidden'))
    # Only module-local caller simulation; this is not Linux-kernel evidence.
    cua.sys = native.sys = SimpleNamespace(platform='linux')
    b = cua.CuaDriverBackend(permission_mode='standard')
    try:
        with mock.patch.object(subprocess, 'Popen', side_effect=only_fixture), mock.patch.object(b._session, '_call_tool_via_cli', side_effect=AssertionError('local fallback forbidden')):
            b.start()
            with mock.patch.object(native, '_get_backend', return_value=b):
                result = native.handle_computer_use({'action': 'capture', 'app': 'Safari', 'pid': 4242, 'window_id': 701, 'mode': 'som'})
            assert result['_multimodal'] and result['target_node'] == 'macbook'
            assert result['delivery'] == 'observed'
            with mock.patch.object(native, '_get_backend', return_value=b), mock.patch.object(native, '_approval_callback', return_value='approve_once'):
                result = json.loads(native.handle_computer_use({'action':'set_value', 'value':'fixture', 'element':1}))
            assert result['code'] == 'mac_policy_refused' and not result['ok']
            assert not any(c['name'] == 'set_value' for c in h.calls())
    finally:
        b.stop()
    print(json.dumps({'imported': [cua.__file__, native.__file__, backend.__file__], 'native_capture': 'observed', 'set_value':'refused', 'real_gui':False}))
'''.replace('GATEWAY_BOOTSTRAP', repr(gateway))


def run_probe(overlay, connector=None, gateway=False):
    args = [sys.executable, '-B', '-c', native_probe(overlay, connector, gateway), str(overlay), str(Path(__file__).parent)]
    if connector:
        args.append(str(connector))
    with tempfile.TemporaryDirectory(prefix='loom-package-cwd-') as cwd:
        env = dict(PATH='/usr/bin:/bin', LANG='C.UTF-8', HOME=cwd, PYTHONPATH=str(overlay), HERMES_DISABLE_LAZY_INSTALLS='1', HERMES_SKIP_NODE_BOOTSTRAP='1')
        return subprocess.run(args, cwd=cwd, env=env, capture_output=True, text=True, timeout=40)


def tui_child_probe(overlay, source_root):
    # Do not import tools in sitecustomize: the defect appears AFTER the real
    # TUI entrypoint has hardened sys.path and loaded its server.
    with tempfile.TemporaryDirectory(prefix='loom-tui-child-') as tmp:
        root = Path(tmp)
        for name in ('tools', 'utils', 'proxy'):
            (root / name).mkdir()
            (root / name / '__init__.py').write_text("raise AssertionError('workspace package imported')\n")
        hook = root / 'hook'
        hook.mkdir()
        (hook / 'sitecustomize.py').write_text(
            'import os, pathlib, socket, sys\n'
            'socket.socket.connect = lambda *a, **k: (_ for _ in ()).throw(AssertionError("network forbidden"))\n'
            'def inspect(frame, event, arg):\n'
            '    if event == "line" and frame.f_globals.get("__name__") == "__main__" and frame.f_code.co_filename.endswith("tui_gateway/entry.py") and "server" in frame.f_globals:\n'
            '        sys.settrace(None)\n'
            '        from tools.computer_use import cua_backend, tool, backend\n'
            '        owners = [pathlib.Path(m.__file__) for m in (cua_backend, tool, backend)]\n'
            '        ok = all(p.is_relative_to(' + repr(str(overlay)) + ') for p in owners)\n'
            '        os.write(1, ("tui-child-after-bootstrap=" + str(ok) + "\\n").encode())\n'
            '        os._exit(0 if ok else 1)\n'
            '    return inspect\n'
            'sys.settrace(inspect)\n')
        # Exact pinned gatewayClient.ts ordering: source root precedes the
        # inherited PYTHONPATH; Hermes bootstrap promotes that root again.
        env = dict(PATH='/usr/bin:/bin', HOME=tmp, HERMES_HOME=tmp,
                   HERMES_PYTHON_SRC_ROOT=str(source_root),
                   PYTHONPATH=os.pathsep.join(map(str, (source_root, overlay, hook, root))),
                   HERMES_DISABLE_LAZY_INSTALLS='1', HERMES_SKIP_NODE_BOOTSTRAP='1')
        return subprocess.run([sys.executable, '-B', '-m', 'tui_gateway.entry'],
                              cwd=tmp, env=env, capture_output=True, text=True, timeout=30)


class PackagingTests(unittest.TestCase):
    def test_gateway_cli_bootstrap_preserves_patched_native_dispatch(self):
        with tempfile.TemporaryDirectory(prefix='loom-gateway-output-') as tmp:
            result = subprocess.run([sys.executable, '-B', '-c', patch_program(), tmp],
                                    capture_output=True, text=True, timeout=20)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            overlay = Path(tmp) / 'share/loom-hermes-native'
            probe = run_probe(overlay, gateway=True)
            self.assertEqual(probe.returncode, 0, probe.stdout + probe.stderr)

    def test_exact_wheel_patch_program_and_native_imports(self):
        with tempfile.TemporaryDirectory(prefix='loom-package-output-') as tmp:
            result = subprocess.run([sys.executable, '-B', '-c', patch_program(), tmp],
                                    capture_output=True, text=True, timeout=20)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            overlay = Path(tmp) / 'share/loom-hermes-native'
            receipt = json.loads((overlay / 'patch-identity.json').read_text())
            self.assertEqual(receipt['upstream_revision'], LOCK['hermes']['revision'])
            self.assertEqual(receipt['patch_sha256'], hashlib.sha256(PATCH.read_bytes()).hexdigest())
            self.assertEqual(len(receipt['files']), 3)
            for rel in ('tools/computer_use/schema.py', 'tools/computer_use_tool.py', 'tools/browser_tool.py'):
                self.assertEqual(hashlib.sha256((overlay / rel).read_bytes()).hexdigest(), LOCK['hermes']['file_sha256'][rel])
            probe = run_probe(overlay)
            self.assertEqual(probe.returncode, 0, probe.stdout + probe.stderr)

    def test_tui_child_preserves_overlay_after_source_root_hardening(self):
        with tempfile.TemporaryDirectory(prefix='loom-tui-output-') as tmp:
            patch = subprocess.run([sys.executable, '-B', '-c', patch_program(), tmp],
                                   capture_output=True, text=True, timeout=20)
            self.assertEqual(patch.returncode, 0, patch.stdout + patch.stderr)
            overlay = Path(tmp) / 'share/loom-hermes-native'
            original = (ENV / 'lib/python3.12/site-packages/hermes_bootstrap.py').resolve().parent
            before = tui_child_probe(overlay, original)
            self.assertEqual(before.returncode, 1, before.stdout + before.stderr)
            self.assertIn('tui-child-after-bootstrap=False', before.stdout)
            self.assertIn('--set HERMES_PYTHON_SRC_ROOT "$out/share/loom-hermes-native"', HERMES_PACKAGE.read_text())
            after = tui_child_probe(overlay, overlay)
            self.assertEqual(after.returncode, 0, after.stdout + after.stderr)
            self.assertIn('tui-child-after-bootstrap=True', after.stdout)
            for name in ('hermes_bootstrap.py', 'hermes_cli/config.py', 'tui_gateway', 'utils.py'):
                self.assertEqual((overlay / name).resolve(), (original / name).resolve())
            original_cli = (original / 'hermes_cli/main.py').read_text()
            expected_cli = original_cli.replace(
                '_bootstrap_root = os.path.realpath(os.path.join(os.path.dirname(__file__), os.pardir))',
                '_bootstrap_root = ' + repr(str(overlay)), 1)
            self.assertEqual((overlay / 'hermes_cli/main.py').read_text(), expected_cli)

    def test_managed_wrappers_and_backup_shim_preserved(self):
        text = HERMES_PACKAGE.read_text()
        self.assertIn('for executable in hermes hermes-agent hermes-acp', text)
        self.assertIn('--prefix PYTHONPATH : "$out/share/loom-hermes-native"', text)
        self.assertIn('--set HERMES_PYTHON_SRC_ROOT "$out/share/loom-hermes-native"', text)
        self.assertIn('assert hermes-upstream.rev == revision', text)
        self.assertIn('if sys.argv[:2] == ["@LOOM_HERMES_ENTRYPOINT@", "backup"]:', text)
        self.assertIn('pinned Hermes Python entrypoint changed', text)
        self.assertIn('kwargs.setdefault("strict_timestamps", False)', text)
        self.assertIn('for command in ["backup", "--version", "gateway", "--tui", "chat", "import", "other-entry"]:', text)
        # Run the existing substantive backup contract against the same shim,
        # including negative commands and explicit caller timestamp semantics.
        shim = text.split('shim = r"""', 1)[1].split('"""', 1)[0]
        contract = text.split('contract = r"""', 1)[1].split('"""', 1)[0]
        with tempfile.TemporaryDirectory(prefix='loom-package-backup-') as tmp:
            source = Path(tmp) / 'sitecustomize.py'
            entry = str(ENV / 'bin/hermes')
            source.write_text(shim.replace('@LOOM_HERMES_ENTRYPOINT@', entry))
            for command in ('backup', '--version', 'gateway', '--tui', 'chat', 'import', 'other-entry'):
                result = subprocess.run([sys.executable, '-B', '-c', contract, str(source), entry, command], capture_output=True, text=True, timeout=10)
                self.assertEqual(result.returncode, 0, result.stderr)

    def test_package_launchers_schema_and_absence_of_activation(self):
        source = BRIDGE_PACKAGE.read_text()
        for token in ('hermes.hermesVenv', 'pkgs.openssh', 'bridgeRelative', 'endpointRelative', 'setupRelative', 'schemaRelative'):
            self.assertIn(token, source)
        for forbidden in ('launchctl ', 'systemctl ', 'activationScripts', 'fetchurl', 'curl ', 'wget ', 'enable = true'):
            self.assertNotIn(forbidden, source)
        for name in ('bridge.py', 'mac_endpoint.py'):
            ast.parse((REPO / 'nix/files/hermes-mac-computer-use' / name).read_text())
        schema = json.loads((REPO / 'nix/files/mac-computer-use/endpoint-binding.schema.json').read_text())
        runtime = schema['$defs']['runtime']
        self.assertIn('runtime_pid', runtime['required'])
        self.assertIn('socket_inode', runtime['required'])
        self.assertFalse(runtime['additionalProperties'])
        self.assertEqual(runtime['properties']['permission_mode']['enum'], ['standard', 'bounded'])
        self.assertEqual(runtime['properties']['driver']['const'], '/Applications/CuaDriver.app/Contents/MacOS/cua-driver')
        self.assertEqual(set(schema['$defs']['endpoint']['required']), {'runtime', 'state_dir'})


def installed(hermes, connector):
    hermes, connector = Path(hermes), Path(connector)
    require = lambda test: test or (_ for _ in ()).throw(AssertionError('installed-output contract'))
    require(str(hermes).startswith('/nix/store/') and str(connector).startswith('/nix/store/'))
    overlay = hermes / 'share/loom-hermes-native'
    for name in ('hermes', 'hermes-agent', 'hermes-acp'):
        wrapper = (hermes / 'bin' / name).read_text()
        require(str(overlay) in wrapper)
        require('HERMES_PYTHON_SRC_ROOT' in wrapper)
    # Traverse each emitted makeWrapper exec chain: the patched overlay belongs
    # to the actual managed executable closure, not a test-only PYTHONPATH.
    wrapper = (hermes / 'bin/.hermes-wrapped').read_text()
    require('loom-hermes-backup' in wrapper)
    require('if [[ "${1-}" == backup ]]' in wrapper)
    payload = connector / 'share/loom-mac-computer-use'
    for name in ('bridge.py', 'mac_endpoint.py'):
        require((payload / name).read_bytes() == (REPO / 'nix/files/hermes-mac-computer-use' / name).read_bytes())
    require((connector / 'bin/loom-mac-computer-use-setup').read_text().splitlines()[0].endswith(' -I'))
    # Exercise all real managed wrappers. A disposable Python startup hook
    # imports/verifies the three actual owners, then exits before CLI/agent work.
    with tempfile.TemporaryDirectory(prefix='loom-installed-wrapper-') as tmp:
        hook = Path(tmp) / 'sitecustomize.py'
        hook.write_text('import os, pathlib, sys\n'
                        'from tools.computer_use import cua_backend, tool, backend\n'
                        'owners = [pathlib.Path(m.__file__) for m in (cua_backend, tool, backend)]\n'
                        'ok = all(p.is_relative_to(' + repr(str(overlay)) + ') for p in owners)\n'
                        'print("installed-wrapper-imports=" + str(ok), flush=True)\n'
                        'os._exit(0 if ok else 1)\n')
        env = dict(PATH='/usr/bin:/bin', HOME=tmp, HERMES_HOME=tmp, PYTHONPATH=tmp)
        for name in ('hermes', 'hermes-agent', 'hermes-acp'):
            result = subprocess.run([str(hermes / 'bin' / name), '--version'], env=env, cwd=tmp,
                                    capture_output=True, text=True, timeout=20)
            require(result.returncode == 0 and 'installed-wrapper-imports=True' in result.stdout)
            print(name + ': actual wrapper imports patched output')
    child = tui_child_probe(overlay, overlay)
    require(child.returncode == 0 and 'tui-child-after-bootstrap=True' in child.stdout)
    print('real TUI child: patched owners survive bootstrap; workspace packages cannot shadow them')
    probe = run_probe(overlay, connector)
    if probe.returncode:
        raise AssertionError(probe.stdout + probe.stderr)
    print(probe.stdout.strip())
    # Load the ACTUAL packaged setup code/constants. Model only host account,
    # publisher/consumer metadata and canonical-to-disposable path mapping in
    # the test harness; no local agents account, privilege or secret is needed.
    # This replaces the old test that collapsed publisher and key consumer.
    import importlib.machinery, importlib.util
    import test_setup_contract as setup_tests
    cli = connector / 'bin/loom-mac-computer-use-setup'
    loader = importlib.machinery.SourceFileLoader('loom_installed_setup', str(cli))
    spec = importlib.util.spec_from_loader(loader.name, loader)
    packaged = importlib.util.module_from_spec(spec)
    loader.exec_module(packaged)
    require(packaged.PACKAGE_DIR == str(payload))
    require(str(packaged.PACKAGE_SSH).startswith('/nix/store/'))
    original = setup_tests.setup
    setup_tests.setup = packaged
    setup_tests.MainConsumerTests.use_packaged_bindings = True
    try:
        suite = unittest.TestSuite(setup_tests.MainConsumerTests(name)
            for name in sorted(setup_tests.MainConsumerTests.__dict__) if name.startswith('test_consumer_'))
        result = unittest.TextTestRunner(verbosity=2).run(suite)
        require(result.wasSuccessful() and not result.skipped and not result.expectedFailures)
    finally:
        del setup_tests.MainConsumerTests.use_packaged_bindings
        setup_tests.setup = original
    print('installed setup code/CLI lifecycle: distinct root/agents metadata model, disposable publication, no privileged OS execution')



if __name__ == '__main__':
    if sys.argv[1:2] == ['--installed']:
        installed(*sys.argv[2:])
    else:
        unittest.main()
