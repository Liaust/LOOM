"""Pinned public trust and real rootless namespace regression; no live SSH/GUI."""
import asyncio
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

SOURCE = Path(os.environ.get('LOOM_BRIDGE_TEST_SOURCE',
    str(Path(__file__).resolve().parents[2] / 'nix/files/hermes-mac-computer-use/bridge.py')))
spec = importlib.util.spec_from_file_location('bridge', SOURCE)
bridge = importlib.util.module_from_spec(spec)
spec.loader.exec_module(bridge)


def digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


class TrustTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='loom-public-trust-')
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.path = self.root / 'binding.json'
        self.path.write_text('{"target_node":"macbook"}')
        self.pin = digest(self.path)

    def test_pin_verifies_owner_and_unmapped_owner_without_uid_exception(self):
        self.assertEqual(bridge.trusted_json(self.path, self.pin)['target_node'], 'macbook')
        with mock.patch.object(bridge.os, 'getuid', return_value=os.getuid() + 1):
            # This case uses content trust, not a special case for UID65534.
            self.assertEqual(bridge.trusted_json(self.path, self.pin)['target_node'], 'macbook')

    def test_pinned_drift_refuses_even_when_caller_owns_file(self):
        self.path.write_text('{"target_node":"SYNTHETIC_SECRET_SENTINEL"}')
        with self.assertRaises(bridge.Fault) as caught:
            bridge.trusted_json(self.path, self.pin)
        self.assertEqual(caught.exception.meta['code'], 'mac_host_identity_mismatch')
        self.assertNotIn('SYNTHETIC_SECRET_SENTINEL', str(caught.exception))

    def test_pin_does_not_relax_mode_type_link_or_bounds(self):
        self.path.chmod(0o666)
        with self.assertRaises(bridge.Fault):
            bridge.trusted_json(self.path, self.pin)
        self.path.chmod(0o600)
        link = self.root / 'link'
        link.symlink_to(self.path)
        with self.assertRaises(OSError):
            bridge.trusted_json(link, self.pin)
        self.path.write_text('x' * 65537)
        with self.assertRaises(bridge.Fault):
            bridge.trusted_json(self.path, digest(self.path))
        with self.assertRaises(bridge.Fault):
            bridge.trusted_json(self.root, self.pin)

    def test_missing_or_malformed_digest_is_not_a_public_trust_override(self):
        for bad in ('', 'g' * 64, 'a' * 63, 12, []):
            with self.subTest(pin=bad), self.assertRaises(bridge.Fault):
                bridge.trusted_json(self.path, bad)
        with self.assertRaises(bridge.Fault):
            bridge.checked_file(self.path, pinned_public=True)

    def test_private_key_retains_custody_and_does_not_read_credential_bytes(self):
        self.path.chmod(0o600)
        with mock.patch.object(bridge.os, 'fdopen', side_effect=AssertionError('credential read')):
            self.assertEqual(bridge.checked_file(self.path, private=True), str(self.path))
        self.path.chmod(0o644)
        with self.assertRaises(bridge.Fault) as caught:
            bridge.checked_file(self.path, private=True)
        self.assertEqual(caught.exception.meta['code'], 'mac_auth_failed')
        self.path.chmod(0o600)
        with mock.patch.object(bridge.os, 'getuid', return_value=os.getuid() + 1):
            with self.assertRaises(bridge.Fault):
                bridge.checked_file(self.path, private=True)

    def test_unpinned_owner_policy_remains_exact(self):
        real_fstat = bridge.os.fstat
        real_lstat = Path.lstat
        def foreign(st):
            values = list(st)
            values[4] = 65534
            return os.stat_result(values)
        with mock.patch.object(bridge.os, 'fstat', side_effect=lambda fd: foreign(real_fstat(fd))), \
             mock.patch.object(Path, 'lstat', side_effect=lambda: foreign(real_lstat(self.path))):
            with self.assertRaises(bridge.Fault):
                bridge.trusted_json(self.path)


class AdmissionTests(unittest.IsolatedAsyncioTestCase):
    async def test_initialize_preserves_pre_ssh_fault_as_typed_mcp_error(self):
        with tempfile.TemporaryDirectory(prefix='loom-admission-') as root:
            source = Path(root) / 'bridge.py'
            source.write_bytes(SOURCE.read_bytes().replace(b'PINNED_BINDING_SHA256 = None',
                b"PINNED_BINDING_SHA256 = '" + b'a' * 64 + b"'"))
            binding = Path(root) / 'binding.json'
            binding.write_text('{"SYNTHETIC_SECRET_SENTINEL": true}')
            peer = bridge.Peer()
            try:
                await peer.start([sys.executable, '-B', str(source), 'mcp'],
                    env=dict(os.environ, HERMES_MAC_BINDING=str(binding),
                             PINNED_BINDING_SHA256=digest(binding),
                             HERMES_MAC_BINDING_SHA256=digest(binding)))
                result = await peer.exchange({'jsonrpc':'2.0', 'id':1, 'method':'initialize',
                    'params':{'protocolVersion':'2025-06-18','capabilities':{},
                              'clientInfo':{'name':'fixture','version':'1'}}},
                    asyncio.get_running_loop().time() + 5)
                self.assertEqual(result['error']['code'], -32000)
                self.assertEqual(result['error']['data']['code'], 'mac_host_identity_mismatch')
                self.assertEqual(result['error']['data']['phase'], 'admission')
                self.assertEqual(result['error']['data']['delivery'], 'not_sent')
                self.assertNotIn('SYNTHETIC_SECRET_SENTINEL', json.dumps(result))
                self.assertEqual(bytes(peer.stderr), b'')
            finally:
                await peer.close()


class LinuxNamespaceTests(unittest.TestCase):
    @unittest.skipUnless(os.environ.get('LOOM_RUN_USERNS_FIXTURE') == '1', 'explicit isolated Linux namespace fixture')
    def test_rootless_namespace_public_pins_and_private_custody(self):
        self.assertEqual(sys.platform, 'linux')
        self.assertEqual(os.getuid(), 0, 'operator creates only these disposable root-owned fixtures')
        uid, gid = 65533, 65533
        with tempfile.TemporaryDirectory(prefix='loom-mac-userns-', dir='/tmp') as directory:
            root = Path(directory)
            root.chmod(0o755)
            shutil.copyfile(SOURCE, root / 'bridge.py')
            (root / 'bridge.py').chmod(0o444)
            public = root / 'public.json'
            public.write_text('{"target_node":"macbook"}')
            public.chmod(0o444)
            ssh = root / 'ssh-fixture'
            ssh.write_text('not executable: vector validation must not run it')
            ssh.chmod(0o444)
            known = root / 'known_hosts'
            known.write_text('synthetic host pin, no credential')
            known.chmod(0o444)
            key = root / 'key'
            key.write_text('synthetic key reference, no secret')
            key.chmod(0o600)
            os.chown(key, uid, gid)
            arguments = {'target_node':'macbook','host':'fixture.invalid','user':'fixture',
                'ssh':str(ssh),'ssh_sha256':digest(ssh),'known_hosts':str(known),
                'known_hosts_sha256':digest(known),'identity_file':str(key),'runtime':{}}
            code = r'''
import copy,json,os,pathlib,sys
sys.path.insert(0,sys.argv[1])
import bridge
public=pathlib.Path(sys.argv[1])/'public.json'
assert os.stat(public).st_uid == 65534
assert pathlib.Path('/proc/self/uid_map').read_text().split() == ['65533','65533','1']
try: bridge.trusted_json(public)
except bridge.Fault as e: assert e.meta['code']=='mac_host_identity_mismatch'
else: raise AssertionError('unpinned unmapped owner accepted')
assert bridge.trusted_json(public,sys.argv[2]) == {'target_node':'macbook'}
binding=json.loads(sys.argv[3])
assert bridge.ssh_vector(binding,pinned_public=True)[-1] == bridge.ENDPOINT
for field in ('ssh_sha256','known_hosts_sha256'):
    changed=copy.deepcopy(binding); changed[field]='0'*64
    try: bridge.ssh_vector(changed,pinned_public=True)
    except bridge.Fault as e: assert e.meta['code']=='mac_host_identity_mismatch'
    else: raise AssertionError('public byte drift accepted')
key=pathlib.Path(binding['identity_file']); key.chmod(0o644)
try: bridge.ssh_vector(binding,pinned_public=True)
except bridge.Fault as e: assert e.meta['code']=='mac_auth_failed'
else: raise AssertionError('key permissions relaxed')
key.chmod(0o600)
changed=copy.deepcopy(binding); changed['identity_file']=str(public)
try: bridge.ssh_vector(changed,pinned_public=True)
except bridge.Fault: pass
else: raise AssertionError('unmapped private owner accepted')
print('PASS: real rootless namespace, public pin, drift rejection, private custody; no SSH')
'''
            result = subprocess.run(['setpriv', f'--reuid={uid}', f'--regid={gid}', '--clear-groups',
                'unshare', '--user', '--map-current-user', '--', sys.executable, '-B', '-c', code,
                directory, digest(public), json.dumps(arguments)], capture_output=True, text=True, timeout=15)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn('PASS: real rootless namespace', result.stdout)
        self.assertFalse(root.exists())


def load_tests(loader, tests, pattern):
    suite = unittest.TestSuite()
    suite.addTests(loader.loadTestsFromTestCase(TrustTests))
    suite.addTests(loader.loadTestsFromTestCase(AdmissionTests))
    if os.environ.get('LOOM_RUN_USERNS_FIXTURE') == '1':
        suite.addTests(loader.loadTestsFromTestCase(LinuxNamespaceTests))
    return suite


if __name__ == '__main__':
    unittest.main()
