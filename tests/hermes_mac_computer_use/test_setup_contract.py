"""Offline setup acceptance: disposable trees and fixed host-command stubs only."""
from __future__ import annotations
import base64
import builtins
import copy
import hashlib
import importlib.machinery
import importlib.util
import io
import json
import os
from pathlib import Path
import plistlib
import shutil
import stat
import subprocess
import sys
import tarfile
import tempfile
from types import SimpleNamespace
import unittest
from unittest import mock

REPO = Path(__file__).resolve().parents[2]
SCRIPT = REPO / 'scripts/loom-mac-computer-use-setup'
loader = importlib.machinery.SourceFileLoader('loom_setup_contract', str(SCRIPT))
spec = importlib.util.spec_from_loader(loader.name, loader)
setup = importlib.util.module_from_spec(spec)
loader.exec_module(setup)
SENTINEL = 'SYNTHETIC_SECRET_SENTINEL'


class SetupTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='loom-setup-fixture-')
        self.addCleanup(self.temp.cleanup)
        self.base = Path(self.temp.name).resolve()
        self.consumer = {'user': 'agents', 'uid': os.getuid(), 'gid': os.getgid(), 'groups': [os.getgid()]}
        self.root = self.base / 'root'
        self.package = self.base / 'package'
        self.package.mkdir()
        for parent in ('usr/local/libexec', 'usr/local/etc', 'usr/local/var',
                       'etc/loom-mac-computer-use', 'Users/fixture/.ssh',
                       'Applications/CuaDriver.app/Contents/MacOS', 'private-key-locators'):
            (self.root / parent).mkdir(parents=True, exist_ok=True)
        for name in ('bridge.py', 'mac_endpoint.py'):
            shutil.copyfile(REPO / 'nix/files/hermes-mac-computer-use' / name, self.package / name)
        for name in ('endpoint-binding.schema.json', 'ssh_config.template'):
            shutil.copyfile(REPO / 'nix/files/mac-computer-use' / name, self.package / name)
        self.driver = setup.mapped(self.root, setup.DRIVER)
        self.driver.write_bytes(b'inert synthetic driver; NEVER EXECUTE\n')
        self.driver.chmod(0o555)
        self.info = setup.mapped(self.root, setup.APP) / 'Contents/Info.plist'
        self.info.write_bytes(plistlib.dumps({'CFBundleIdentifier': 'com.trycua.driver',
                                             'CFBundleShortVersionString': '0.26.1',
                                             'CFBundleExecutable': 'cua-driver'}))
        self.archive = self.base / 'cua-fixture.tar.gz'
        with tarfile.open(self.archive, 'w:gz') as tar:
            tar.add(self.driver, arcname='release/CuaDriver.app/Contents/MacOS/cua-driver')
        # Only fixture-owned copies of the lock are altered. Product lock and
        # artifact/source fixtures retain their accepted exact hashes.
        lock = json.loads((REPO / 'nix/locks/mac-computer-use.json').read_text())
        lock['cua']['artifact'].update(size_bytes=self.archive.stat().st_size,
            observed_sha256=setup.sha(self.archive.read_bytes()), published_sha256=setup.sha(self.archive.read_bytes()))
        (self.package / 'dependency_lock.json').write_bytes(setup.canonical(lock))
        raw = b'\0\0\0\x0bssh-ed25519\0\0\0\x20' + bytes(range(32))
        self.public = self.base / 'key.pub'
        self.public.write_text('ssh-ed25519 ' + base64.b64encode(raw).decode() + '\n')
        self.known = self.base / 'known_hosts'
        self.known.write_text('synthetic.invalid ' + self.public.read_text())
        self.private = self.root / 'private-key-locators/key'
        self.private.write_text(SENTINEL)
        self.private.chmod(0o600)
        self.ssh = self.base / 'ssh-stub-inert'
        self.ssh.write_text('inert package OpenSSH identity, never run')
        self.ssh.chmod(0o555)
        self.originals = {}
        for rel in ('Users/fixture/.ssh/authorized_keys', 'Users/fixture/.ssh/config', 'etc/sshd_config'):
            p = self.root / rel
            p.write_text('unrelated ' + SENTINEL + '\n')
            self.originals[p] = (p.stat().st_ino, p.read_bytes())
        self.host = {'host': 'synthetic-mac', 'uid': os.getuid(), 'user': 'fixture', 'platform': 'darwin'}
        self.runtime = {'host': 'synthetic-mac', 'user': 'fixture', 'uid': 501, 'peer_uid': 501,
                        'driver': setup.DRIVER, 'driver_sha256': setup.sha(self.driver.read_bytes()),
                        'runtime_pid': 12345, 'socket_inode': 789, 'socket': '/Users/fixture/cua.sock',
                        'permission_mode': 'standard', 'user_policy_sha256': None, 'managed_policy_sha256': None}
        self.request = {'schema_version': 1, 'role': 'endpoint', 'root': str(self.root),
                        'host_identity': self.host, 'runtime': self.runtime,
                        'inputs': {'archive': str(self.archive), 'gui_home': '/Users/fixture',
                                   'main_source': '192.0.2.1/32', 'public_key': str(self.public)}}
        package_patchers = [] if getattr(self, 'use_packaged_bindings', False) else [
            mock.patch.object(setup, 'PACKAGE_DIR', str(self.package)),
            mock.patch.object(setup, 'PACKAGE_SSH', str(self.ssh))]
        for patcher in (*package_patchers, mock.patch.object(setup, 'host_identity', return_value=self.host),
                        mock.patch.object(setup.pwd, 'getpwnam', side_effect=self.account),
                        mock.patch.object(setup.os, 'getgrouplist', side_effect=self.groups),
                        mock.patch.object(setup.subprocess, 'run', side_effect=self.command)):
            patcher.start()
            self.addCleanup(patcher.stop)
        self.commands = []

    def account(self, name):
        if name == 'agents':
            return SimpleNamespace(pw_name='agents', pw_uid=self.consumer['uid'],
                                   pw_gid=self.consumer['gid'], pw_dir='/home/agents')
        self.assertEqual(name, 'fixture')
        return SimpleNamespace(pw_name='fixture', pw_uid=501, pw_gid=501, pw_dir='/Users/fixture')

    def groups(self, name, gid):
        self.assertEqual((name, gid), ('agents', self.consumer['gid']))
        return list(self.consumer['groups'])

    def command(self, argv, **kwargs):
        self.assertEqual(argv[0], '/usr/bin/codesign')
        self.assertIn(argv[1:-1], [['--verify', '--deep', '--strict'], ['-dvvv']])
        self.assertEqual(argv[-1], str(setup.mapped(self.root, setup.APP)))
        self.assertEqual(kwargs['stdin'], subprocess.DEVNULL)
        self.assertNotIn('shell', kwargs)
        self.commands.append(argv)
        signature = ('Identifier=com.trycua.driver\nTeamIdentifier=YCK386LBJ7\n'
                     'Authority=Developer ID Application: Cua AI, Inc. (YCK386LBJ7)\n'
                     'flags=0x10000(runtime)\nCDHash=8084d02233ae6121138ce3986a6ad3320c4c20ba\n')
        return SimpleNamespace(returncode=0, stdout=b'', stderr=signature.encode())

    def snapshot(self):
        return {str(p.relative_to(self.base)): (p.lstat().st_ino, p.lstat().st_mode,
                    p.read_bytes() if p.is_file() else None) for p in self.base.rglob('*')}

    def main_request(self):
        request = copy.deepcopy(self.request)
        request['role'] = 'main'
        request['inputs'] = {'host': 'synthetic.invalid', 'known_hosts': str(self.known),
                             'identity_file': '/private-key-locators/key'}
        return request

    def apply(self, plan, action='apply'):
        return setup.mutate(plan, plan['digest'], action)

    def assert_preserved(self):
        for p, old in self.originals.items():
            self.assertEqual((p.stat().st_ino, p.read_bytes()), old)
        self.assertEqual(self.driver.read_bytes(), b'inert synthetic driver; NEVER EXECUTE\n')

    def test_dry_run_zero_writes_network_gui_and_secret_reads(self):
        before = self.snapshot()
        with mock.patch.object(setup.socket, 'socket', side_effect=AssertionError('network/GUI forbidden')):
            endpoint = setup.plan(self.request)
            main = setup.plan(self.main_request())
        self.assertEqual(before, self.snapshot())
        self.assertNotIn(SENTINEL, json.dumps(endpoint) + json.dumps(main))
        self.assertNotIn('sha256', main['plan']['evidence']['identity_locator'])
        self.assertEqual(main['plan']['evidence']['identity_locator']['consumer'], self.consumer)
        self.assertEqual(len(self.commands), 2)
        self.assertEqual(endpoint['posture'], 'offline_staging')
        self.assertIn('ephemeral', endpoint['notice'])

    def test_signed_app_parent_policy_is_exact_and_read_only(self):
        chain = setup.SignedAppInput.__new__(setup.SignedAppInput)
        chain.names = ['/', 'Applications']
        allowed = SimpleNamespace(st_uid=0, st_gid=80, st_mode=stat.S_IFDIR | 0o775)
        chain.validate_directory(allowed)
        with self.assertRaisesRegex(setup.Refusal, 'ancestor_permissions'):
            setup.Chain.validate_directory(chain, allowed)
        for uid, gid, mode in ((0, 0, 0o775), (0, 80, 0o777), (0, 80, 0o770),
                               (501, 80, 0o775), (0, 80, 0o2775)):
            with self.subTest(uid=uid, gid=gid, mode=mode), self.assertRaises(setup.Refusal):
                chain.validate_directory(SimpleNamespace(st_uid=uid, st_gid=gid,
                                                         st_mode=stat.S_IFDIR | mode))
        for names in (['/', 'usr', 'local'], ['/', 'Applications', 'CuaDriver.app'],
                      ['/', 'fixture', 'Applications']):
            chain.names = names
            with self.subTest(names=names), self.assertRaises(setup.Refusal):
                chain.validate_directory(allowed)

    def test_signed_app_input_accepts_only_fixed_darwin_files(self):
        with mock.patch.object(setup.sys, 'platform', 'darwin'), \
                mock.patch.object(setup.Chain, '__init__') as traverse:
            for filename in (setup.DRIVER, setup.APP + '/Contents/Info.plist'):
                setup.SignedAppInput(filename)
                traverse.assert_called_with(Path(filename).parent)
        with mock.patch.object(setup.os, 'open', side_effect=AssertionError('unexpected traversal')):
            for platform, filename in (('linux', setup.DRIVER), ('darwin', str(self.driver)),
                                       ('darwin', '/Applications/Other.app/Contents/Info.plist'),
                                       ('darwin', setup.CONFIG)):
                with self.subTest(platform=platform, filename=filename), \
                        mock.patch.object(setup.sys, 'platform', platform), \
                        self.assertRaisesRegex(setup.Refusal, 'signed_app_input'):
                    setup.inspect_file(filename, signed_app=True)

    def test_signed_app_input_revalidates_held_and_named_ancestors(self):
        # Map only the filesystem root into this disposable tree. The real
        # no-follow descriptor walk and pathname revalidation still execute.
        real_open, real_stat, real_fstat = os.open, os.stat, os.fstat
        applications = self.root / 'Applications'
        applications.chmod(0o775)
        app_inode = applications.stat().st_ino

        def metadata(st):
            values = {k: getattr(st, k) for k in dir(st) if k.startswith('st_')}
            values.update(st_uid=0, st_gid=80 if st.st_ino == app_inode else 0)
            return SimpleNamespace(**values)

        def opened(name, flags, *args, **kwargs):
            return real_open(str(self.root) if name == '/' else name, flags, *args, **kwargs)

        def stated(name, *args, **kwargs):
            return metadata(real_stat(str(self.root) if name == '/' else name, *args, **kwargs))

        with mock.patch.object(setup.sys, 'platform', 'darwin'), \
                mock.patch.object(setup.os, 'open', side_effect=opened), \
                mock.patch.object(setup.os, 'stat', side_effect=stated), \
                mock.patch.object(setup.os, 'fstat', side_effect=lambda fd: metadata(real_fstat(fd))):
            info, _ = setup.inspect_file(setup.DRIVER, signed_app=True)
            self.assertEqual(info['sha256'], self.runtime['driver_sha256'])
            with setup.SignedAppInput(setup.DRIVER) as chain:
                chain.check()
                saved = applications.with_name('Applications-original')
                applications.rename(saved)
                applications.mkdir()
                with self.assertRaisesRegex(setup.Refusal, 'ancestor_changed'):
                    chain.check()
                applications.rmdir()
                saved.rename(applications)
                chain.check()
                applications.chmod(0o777)
                with self.assertRaisesRegex(setup.Refusal, 'ancestor_changed'):
                    chain.check()

    def test_exact_apply_replay_and_preserve_existing_app_ssh(self):
        plan = setup.plan(self.request)
        first = self.apply(plan)
        self.assertTrue(first['ok'], first)
        before = self.snapshot()
        again = self.apply(plan)
        self.assertTrue(again['ok'], again)
        self.assertEqual(before, self.snapshot())
        installed = setup.mapped(self.root, setup.CONFIG)
        config = json.loads(installed.read_text())
        self.assertEqual(config['runtime'], self.runtime)
        self.assertEqual(setup.mapped(self.root, config['state_dir']).stat().st_mode & 0o777, 0o700)
        launcher = setup.mapped(self.root, setup.ENDPOINT).read_text()
        compile(launcher, 'installed endpoint', 'exec')
        self.assertIn(' -I\n', launcher)
        restricted = self.root / 'usr/local/etc/loom-mac-authorized-key'
        self.assertEqual(restricted.read_text(), 'restrict,from="192.0.2.1/32",command="/usr/local/libexec/loom-mac-endpoint" ' + self.public.read_text())
        self.assert_preserved()

    def test_main_binding_exact_contract_and_key_is_locator_only(self):
        plan = setup.plan(self.main_request())
        self.assertTrue(self.apply(plan)['ok'])
        binding = json.loads((self.root / 'etc/loom-mac-computer-use/binding.json').read_text())
        self.assertEqual(binding['runtime'], self.runtime)
        self.assertEqual(binding['ssh'], str(self.ssh))
        self.assertEqual(binding['known_hosts_sha256'], setup.sha(self.known.read_bytes()))
        self.assertEqual(binding['identity_file'], '/private-key-locators/key')
        self.assertNotIn(SENTINEL, json.dumps(plan))
        self.assert_preserved()

    def test_changed_plan_digest_and_inputs_refused(self):
        plan = setup.plan(self.request)
        tampered = copy.deepcopy(plan)
        tampered['plan']['request']['runtime']['runtime_pid'] += 1
        with self.assertRaises(setup.Refusal):
            self.apply(tampered)
        with self.assertRaises(setup.Refusal):
            setup.mutate(plan, '0' * 64, 'apply')
        self.archive.write_bytes(b'changed ' + SENTINEL.encode())
        result = self.apply(plan)
        self.assertFalse(result['ok'])
        self.assertEqual(result['published'], [])
        self.assertNotIn(SENTINEL, json.dumps(result))

    def test_destination_and_ancestor_substitution_refused(self):
        plan = setup.plan(self.request)
        target = setup.mapped(self.root, setup.ENDPOINT)
        target.write_text('unrelated ' + SENTINEL)
        self.assertFalse(self.apply(plan)['ok'])
        self.assertEqual(target.read_text(), 'unrelated ' + SENTINEL)
        target.unlink()
        parent = target.parent
        moved = parent.with_name('libexec-original')
        parent.rename(moved)
        parent.mkdir()
        self.assertFalse(self.apply(plan)['ok'])
        self.assertEqual(list(parent.iterdir()), [])
        self.assertEqual(list(moved.iterdir()), [])

    def test_input_permission_symlink_and_private_key_substitution(self):
        plan = setup.plan(self.main_request())
        self.private.chmod(0o644)
        self.assertFalse(self.apply(plan)['ok'])
        self.private.chmod(0o600)
        saved = self.private.with_name('saved')
        self.private.rename(saved)
        self.private.symlink_to(saved)
        self.assertFalse(self.apply(plan)['ok'])
        self.assertEqual(saved.read_text(), SENTINEL)

    def test_private_key_cannot_be_read_as_known_hosts_alias(self):
        # Also protect the unprivileged offline case where publisher/consumer
        # happen to coincide. Hardlinks must not disguise the private inode.
        alias = self.base / 'public-input-alias'
        os.link(self.private, alias)
        request = self.main_request()
        original_open = os.open
        private_inode = self.private.stat().st_ino
        def no_private_open(name, flags, *args, **kwargs):
            st = os.stat(name, dir_fd=kwargs.get('dir_fd'), follow_symlinks=False)
            self.assertNotEqual(st.st_ino, private_inode, 'private key opened as public input')
            return original_open(name, flags, *args, **kwargs)
        for source in (self.private, alias):
            request['inputs']['known_hosts'] = str(source)
            with mock.patch.object(setup.os, 'open', side_effect=no_private_open), self.assertRaisesRegex(setup.Refusal, 'private_key_input_alias'):
                setup.plan(request)

    def test_atomic_partial_publication_and_exact_replay(self):
        plan = setup.plan(self.request)
        link = os.link
        count = 0
        def fail_second(*args, **kwargs):
            nonlocal count
            count += 1
            if count == 2:
                raise OSError(SENTINEL)
            return link(*args, **kwargs)
        with mock.patch.object(setup.os, 'link', side_effect=fail_second):
            partial = self.apply(plan)
        self.assertFalse(partial['ok'])
        self.assertEqual(partial['published'], ['/usr/local/etc/loom-mac-authorized-key'])
        self.assertTrue(partial['reconciliation_required'])
        self.assertNotIn(SENTINEL, json.dumps(partial))
        self.assertTrue((self.root / 'usr/local/etc/loom-mac-authorized-key').read_text().endswith(self.public.read_text()))
        self.assertFalse(setup.mapped(self.root, setup.CONFIG).exists())
        self.assertTrue(self.apply(plan)['ok'])
        self.assert_preserved()

    def test_publication_ancestor_race_has_no_substitute_write(self):
        plan = setup.plan(self.request)
        original_link = os.link
        parent = setup.mapped(self.root, setup.ENDPOINT).parent
        moved = parent.with_name('libexec-old')
        outside = self.base / 'outside'
        outside.mkdir()
        def substitute(*args, **kwargs):
            if args[1] == 'loom-mac-endpoint':
                parent.rename(moved)
                parent.symlink_to(outside, target_is_directory=True)
            return original_link(*args, **kwargs)
        with mock.patch.object(setup.os, 'link', side_effect=substitute):
            result = self.apply(plan)
        self.assertFalse(result['ok'])
        self.assertEqual(result['published'], sorted(plan['plan']['payload']))
        self.assertTrue((moved / 'loom-mac-endpoint').is_file())
        self.assertEqual(list(outside.iterdir()), [])

    def test_signature_hash_bundle_and_runtime_identity_checks(self):
        with mock.patch.object(setup.subprocess, 'run', return_value=SimpleNamespace(returncode=0, stdout=b'', stderr=SENTINEL.encode())):
            with self.assertRaises(setup.Refusal):
                setup.plan(self.request)
        info = plistlib.loads(self.info.read_bytes())
        info['CFBundleIdentifier'] = 'unapproved'
        self.info.write_bytes(plistlib.dumps(info))
        with self.assertRaises(setup.Refusal):
            setup.plan(self.request)
        for changes in ({'uid': 0, 'peer_uid': 0}, {'permission_mode': 'unrestricted'},
                        {'driver': '/arbitrary/shell'}, {'runtime_pid': True}, {'extra': 'launcher'}):
            with self.subTest(changes=changes), self.assertRaises(setup.Refusal):
                setup.validate_runtime(dict(self.runtime, **changes))

    def test_restart_invalidates_review_and_requires_explicit_new_plan(self):
        # Republishing changed request bytes is still an exact setup operation.
        # Runtime reconnect no longer needs this republication in the first place.
        plan = setup.plan(self.request)
        self.assertTrue(self.apply(plan)['ok'])
        changed = copy.deepcopy(self.request)
        changed['runtime']['runtime_pid'] += 1
        changed['runtime']['socket_inode'] += 1
        with self.assertRaises(setup.Refusal):
            setup.plan(changed)  # never overwrite the previous binding
        self.assertTrue(self.apply(plan, 'revoke')['ok'])
        new = setup.plan(changed)
        self.assertNotEqual(new['digest'], plan['digest'])
        self.assertNotEqual(new['plan']['state'], plan['plan']['state'])
        self.assertTrue(self.apply(new)['ok'])
        self.assertFalse(self.apply(plan)['ok'])

    def test_revoke_exact_identities_and_unrelated_content_preserved(self):
        plan = setup.plan(self.request)
        self.assertTrue(self.apply(plan)['ok'])
        target = setup.mapped(self.root, setup.CONFIG)
        old = target.read_bytes()
        target.unlink()
        target.write_bytes(old)  # even matching bytes are not our publication
        self.assertFalse(self.apply(plan, 'revoke')['ok'])
        self.assertTrue(setup.mapped(self.root, setup.ENDPOINT).exists())
        target.unlink()
        journal = setup.mapped(self.root, plan['plan']['journal_parent']) / ('.loom-mac-setup-' + plan['digest'])
        receipt = json.loads((journal / 'receipt.json').read_text())
        stage = receipt['stages'][setup.CONFIG]['name']
        os.link(journal / stage, target)
        # Revocation works after source artifacts disappear. It cannot require
        # a live driver/key/SSH connection to prevent future endpoint admission.
        self.archive.unlink()
        result = self.apply(plan, 'revoke')
        self.assertTrue(result['ok'], result)
        self.assertEqual(result['published'], [])
        before = self.snapshot()
        self.assertTrue(self.apply(plan, 'revoke')['ok'])
        self.assertEqual(before, self.snapshot())
        self.assert_preserved()

    def test_stage_state_and_unknown_directory_refused(self):
        plan = setup.plan(self.request)
        journal = setup.mapped(self.root, plan['plan']['journal_parent']) / ('.loom-mac-setup-' + plan['digest'])
        journal.mkdir(mode=0o700)
        (journal / 'unrelated').write_text(SENTINEL)
        result = self.apply(plan)
        self.assertFalse(result['ok'])
        self.assertEqual((journal / 'unrelated').read_text(), SENTINEL)
        self.assertEqual(result['published'], [])

    def test_same_bytes_stage_substitution_refused(self):
        plan = setup.plan(self.request)
        self.assertTrue(self.apply(plan)['ok'])
        journal = setup.mapped(self.root, plan['plan']['journal_parent']) / ('.loom-mac-setup-' + plan['digest'])
        stage = journal / 'stage-0'
        before = stage.read_bytes()
        mode = stage.stat().st_mode & 0o777
        stage.unlink()
        stage.write_bytes(before)
        stage.chmod(mode)
        result = self.apply(plan)
        self.assertFalse(result['ok'])
        self.assertEqual(result['publication_inventory'], 'incomplete_until_success')
        self.assert_preserved()

    def test_state_parent_substitution_refuses_replay_but_allows_revoke(self):
        plan = setup.plan(self.request)
        self.assertTrue(self.apply(plan)['ok'])
        parent = self.root / 'usr/local/var'
        state_name = Path(plan['plan']['state']).name
        moved = parent.with_name('var-old')
        parent.rename(moved)
        parent.mkdir()
        (moved / state_name).rename(parent / state_name)
        self.assertFalse(self.apply(plan)['ok'])
        # Do not require this runtime directory to be restored or deleted just
        # to remove exact protected connector executables/configuration.
        self.assertTrue(self.apply(plan, 'revoke')['ok'])
        self.assertTrue((parent / state_name).is_dir())

    def test_cli_redaction_and_no_default_apply(self):
        request_file = self.base / 'request.json'
        malformed = dict(self.request, extra=SENTINEL)
        request_file.write_bytes(setup.canonical(malformed))
        out = io.StringIO()
        with contextlib_redirect(out):
            result = setup.main(['plan', '--request', str(request_file)])
        self.assertEqual(result, 1)
        self.assertNotIn(SENTINEL, out.getvalue())
        self.assertEqual(json.loads(out.getvalue())['code'], 'setup_refused')
        out = io.StringIO()
        with mock.patch('sys.stderr', out), self.assertRaises(SystemExit):
            setup.main(['apply', '--plan', str(request_file), '--digest', '0' * 64])
        self.assertIn('--confirm', out.getvalue())


def contextlib_redirect(stream):
    from contextlib import redirect_stdout
    return redirect_stdout(stream)


class MainConsumerTests(SetupTests):
    """Canonical root publisher vs UID1001 consumer, modeled metadata only.

    Real files/operations stay in one disposable tree owned by the test runner.
    stat/account/UID observations are modeled; no chown, setuid or OS privilege.
    Protected parents report root custody; only the private key subtree reports
    agents custody. Actual no-follow opens, inode changes and writes still run.
    """
    def setUp(self):
        super().setUp()
        self.consumer = {'user': 'agents', 'uid': 1001, 'gid': 1001, 'groups': [1001, 1002]}
        self.host = {'host': 'synthetic-main', 'uid': 0, 'user': 'root', 'platform': 'linux'}
        self.request['host_identity'] = self.host
        self.request['root'] = '/'
        self.overrides = {}
        self.fd_paths = {}
        actual_stat, actual_fstat = os.stat, os.fstat
        actual_open, actual_close = os.open, os.close
        actual_io_open, actual_builtin_open = io.open, builtins.open
        original_mapped = setup.mapped

        def resolved(filename, dir_fd=None):
            if isinstance(filename, int):
                return self.fd_paths.get(filename)
            p = Path(filename)
            return p if p.is_absolute() else self.fd_paths[dir_fd] / p if dir_fd is not None else Path.cwd() / p

        def metadata(st, p):
            values = {name: getattr(st, name) for name in dir(st) if name.startswith('st_')}
            values['st_uid'] = 0
            values['st_gid'] = 0
            if stat_is_dir(st) and not any(part.startswith('.loom-mac-setup-') for part in p.parts):
                values['st_mode'] = (st.st_mode & ~0o7777) | 0o755
            if p == self.private.parent:
                values.update(st_uid=1001, st_gid=1001, st_mode=(st.st_mode & ~0o7777) | 0o700)
            elif p == self.private:
                values.update(st_uid=1001, st_gid=1001)
            for name, value in self.overrides.get(str(p), {}).items():
                if name == 'mode':
                    values['st_mode'] = (st.st_mode & ~0o7777) | value
                else:
                    values['st_' + name] = value
            return SimpleNamespace(**values)

        def no_secret(filename, dir_fd=None):
            p = resolved(filename, dir_fd)
            if p == self.private:
                raise AssertionError('private key must never be opened/read/hashed')
            return p

        def opened(filename, flags, *args, **kwargs):
            p = no_secret(filename, kwargs.get('dir_fd'))
            fd = actual_open(filename, flags, *args, **kwargs)
            self.fd_paths[fd] = p
            return fd

        def closed(fd):
            self.fd_paths.pop(fd, None)
            return actual_close(fd)

        def stated(filename, *args, **kwargs):
            return metadata(actual_stat(filename, *args, **kwargs), resolved(filename, kwargs.get('dir_fd')))

        def fstated(fd):
            return metadata(actual_fstat(fd), self.fd_paths[fd])

        def guarded_open(original):
            def guard(filename, *args, **kwargs):
                no_secret(filename)
                return original(filename, *args, **kwargs)
            return guard

        for patcher in (mock.patch.object(setup, 'mapped', side_effect=lambda root, name: original_mapped(self.root if root == Path('/') else root, name)),
                        mock.patch.object(setup, 'host_identity', return_value=self.host),
                        mock.patch.object(setup.os, 'getuid', return_value=0),
                        mock.patch.object(setup.os, 'open', side_effect=opened),
                        mock.patch.object(setup.os, 'close', side_effect=closed),
                        mock.patch.object(setup.os, 'stat', side_effect=stated),
                        mock.patch.object(setup.os, 'fstat', side_effect=fstated),
                        mock.patch.object(io, 'open', side_effect=guarded_open(actual_io_open)),
                        mock.patch.object(builtins, 'open', side_effect=guarded_open(actual_builtin_open)),
                        mock.patch.object(setup.os, 'chown', side_effect=AssertionError('chown forbidden')),
                        mock.patch.object(setup.os, 'fchown', side_effect=AssertionError('fchown forbidden'))):
            patcher.start()
            self.addCleanup(patcher.stop)

    def test_consumer_root_publisher_agents_key_and_secret_absence(self):
        plan = setup.plan(self.main_request())
        key = plan['plan']['evidence']['identity_locator']
        self.assertEqual(plan['plan']['request']['host_identity']['uid'], 0)
        self.assertEqual(key['consumer'], self.consumer)
        self.assertEqual(key['identity']['uid'], 1001)
        self.assertNotEqual(key['identity']['uid'], self.runtime['uid'])
        self.assertEqual(set(key), {'path', 'consumer', 'identity', 'ancestors'})
        self.assertNotIn(SENTINEL, json.dumps(plan))
        self.assertTrue(self.apply(plan)['ok'])
        files = {p.name: (p.stat().st_ino, p.read_bytes()) for p in (self.root / 'etc/loom-mac-computer-use').iterdir() if p.is_file()}
        self.assertTrue(self.apply(plan)['ok'])
        self.assertEqual(files, {p.name: (p.stat().st_ino, p.read_bytes()) for p in (self.root / 'etc/loom-mac-computer-use').iterdir() if p.is_file()})
        self.assertTrue(self.apply(plan, 'revoke')['ok'])
        self.assert_preserved()

    def test_consumer_root_unrelated_and_unreadable_keys_refused(self):
        for changes in ({'uid': 0}, {'uid': 1003}, {'mode': 0o200}, {'mode': 0o000},
                        {'mode': 0o640}, {'mode': 0o644}, {'mode': 0o700}):
            with self.subTest(changes=changes):
                self.overrides[str(self.private)] = changes
                with self.assertRaises(setup.Refusal):
                    setup.plan(self.main_request())
        self.overrides[str(self.private)] = {'mode': 0o400}
        self.assertEqual(setup.plan(self.main_request())['plan']['evidence']['identity_locator']['identity']['mode'], 0o400)

    def test_consumer_private_ancestors_require_custody_and_posix_traversal(self):
        p = str(self.private.parent)
        for changes in ({'uid': 0, 'mode': 0o700}, {'uid': 1003, 'mode': 0o755},
                        {'uid': 1001, 'mode': 0o600}, {'uid': 1001, 'mode': 0o770},
                        {'uid': 0, 'mode': 0o1777}, {'uid': 0, 'gid': 1002, 'mode': 0o701}):
            with self.subTest(changes=changes):
                self.overrides[p] = changes
                with self.assertRaises(setup.Refusal):
                    setup.plan(self.main_request())
        for changes in ({'uid': 1001, 'mode': 0o700}, {'uid': 0, 'gid': 1002, 'mode': 0o750}):
            self.overrides[p] = changes
            self.assertEqual(setup.plan(self.main_request())['plan']['evidence']['identity_locator']['consumer']['uid'], 1001)

    def test_consumer_account_owner_and_group_changes_refuse_replay(self):
        plan = setup.plan(self.main_request())
        self.assertTrue(self.apply(plan)['ok'])
        for changes in ({'uid': 1003}, {'gid': 1002}, {'groups': [1001]}):
            before = self.consumer.copy()
            self.consumer.update(changes)
            self.assertFalse(self.apply(plan)['ok'])
            self.consumer = before
        self.overrides[str(self.private)] = {'uid': 0}
        self.assertFalse(self.apply(plan)['ok'])
        # Existing exact protected entries remain revocable when consumer/key
        # custody has changed; neither credentials nor account state is repaired.
        self.assertTrue(self.apply(plan, 'revoke')['ok'])

    def test_consumer_path_and_parent_substitution_refuse_replay(self):
        plan = setup.plan(self.main_request())
        self.assertTrue(self.apply(plan)['ok'])
        saved = self.private.with_name('saved')
        self.private.rename(saved)
        self.private.symlink_to(saved)
        self.assertFalse(self.apply(plan)['ok'])
        self.private.unlink()
        saved.rename(self.private)
        parent = self.private.parent
        moved = parent.with_name('moved')
        parent.rename(moved)
        parent.mkdir()
        (moved / 'key').rename(parent / 'key')
        self.assertFalse(self.apply(plan)['ok'])
        self.assertTrue(self.apply(plan, 'revoke')['ok'])

    def test_consumer_changed_during_publication_is_partial_refusal(self):
        plan = setup.plan(self.main_request())
        original_link = os.link
        def changed(*args, **kwargs):
            result = original_link(*args, **kwargs)
            self.overrides[str(self.private)] = {'uid': 1003}
            return result
        with mock.patch.object(setup.os, 'link', side_effect=changed):
            result = self.apply(plan)
        self.assertFalse(result['ok'])
        self.assertEqual(len(result['published']), 1)
        self.assertEqual(result['publication_inventory'], 'incomplete_until_success')
        self.assertNotIn(SENTINEL, json.dumps(result))
        self.assertTrue(self.apply(plan, 'revoke')['ok'])

    def test_consumer_permission_does_not_expand_publication_custody(self):
        self.overrides[str(self.root / 'etc/loom-mac-computer-use')] = {'uid': 1001, 'mode': 0o755}
        with self.assertRaises(setup.Refusal):
            setup.plan(self.main_request())
        self.overrides.clear()
        with mock.patch.object(setup.os, 'getuid', return_value=1001):
            with self.assertRaisesRegex(setup.Refusal, 'protected_install_requires_operator'):
                setup.plan(self.main_request())

    def test_consumer_cli_lifecycle_with_distinct_publisher(self):
        request_file, plan_file = self.base / 'request.json', self.base / 'plan.json'
        request_file.write_bytes(setup.canonical(self.main_request()))
        target = self.root / 'etc/loom-mac-computer-use'

        def invoke(*args):
            out = io.StringIO()
            with contextlib_redirect(out):
                code = setup.main(list(args))
            self.assertEqual(code, 0, out.getvalue())
            self.assertNotIn(SENTINEL, out.getvalue())
            return json.loads(out.getvalue())

        reviewed = invoke('plan', '--request', str(request_file))
        self.assertEqual(list(target.iterdir()), [])
        self.assertEqual(reviewed['plan']['evidence']['identity_locator']['consumer']['uid'], 1001)
        plan_file.write_bytes(setup.canonical(reviewed))
        for action in ('apply', 'apply', 'revoke', 'revoke'):
            result = invoke(action, '--plan', str(plan_file), '--digest', reviewed['digest'], '--confirm', action)
            self.assertTrue(result['ok'])
            self.assertEqual(result['publication_inventory'], 'complete')
        self.assertFalse((target / 'binding.json').exists())


def stat_is_dir(st):
    import stat
    return stat.S_ISDIR(st.st_mode)


def load_tests(loader, tests, pattern):
    if os.environ.get('LOOM_MAIN_CONSUMER_STRESS_ONLY'):
        names = ['test_consumer_account_owner_and_group_changes_refuse_replay',
                 'test_consumer_path_and_parent_substitution_refuse_replay',
                 'test_consumer_changed_during_publication_is_partial_refusal']
        return unittest.TestSuite(MainConsumerTests(name) for _ in range(20) for name in names)
    if os.environ.get('LOOM_SETUP_STRESS_ONLY'):
        names = ['test_exact_apply_replay_and_preserve_existing_app_ssh',
                 'test_destination_and_ancestor_substitution_refused',
                 'test_atomic_partial_publication_and_exact_replay',
                 'test_publication_ancestor_race_has_no_substitute_write',
                 'test_restart_invalidates_review_and_requires_explicit_new_plan',
                 'test_revoke_exact_identities_and_unrelated_content_preserved',
                 'test_same_bytes_stage_substitution_refused',
                 'test_state_parent_substitution_refuses_replay_but_allows_revoke']
        return unittest.TestSuite(SetupTests(name) for _ in range(20) for name in names)
    suite = loader.loadTestsFromTestCase(SetupTests)
    suite.addTests(MainConsumerTests(name) for name in sorted(MainConsumerTests.__dict__) if name.startswith('test_consumer_'))
    return suite


if __name__ == '__main__':
    unittest.main()
