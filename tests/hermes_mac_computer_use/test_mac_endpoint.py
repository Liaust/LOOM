"""Subprocess-only endpoint/SSH fixtures. No Cua/SSH/GUI or live identity probe."""
from __future__ import annotations
import asyncio
import base64
import copy
import hashlib
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time
import unittest

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[1]
ADAPTER = REPO / 'nix/files/hermes-mac-computer-use'
sys.path.insert(0, str(ADAPTER))
import bridge
import mac_endpoint
FIXTURE = json.loads((HERE / 'fixtures/native_driver_manifest.json').read_text())


def digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def fixture(mode, root, args):
    root = Path(root)
    with (root / 'pids.jsonl').open('a') as out:
        out.write(json.dumps({'pid': os.getpid(), 'mode': mode}) + '\n')
    def control():
        return json.loads((root / 'control.json').read_text())
    if mode == '--ssh':
        (root / 'ssh-args.json').write_text(json.dumps(args))
        fault = control().get('ssh')
        if fault:
            print({'offline': 'connect failed', 'auth': 'Permission denied',
                   'host': 'Host key verification failed'}[fault] + ' SYNTHETIC_SECRET_SENTINEL', file=sys.stderr)
            return
        os.execv(sys.executable, [sys.executable, '-B', str(HERE / 'test_mac_endpoint.py'), '--endpoint', str(root)])
    if mode == '--endpoint':
        mac_endpoint.ACTION = control().get('budget', bridge.ACTION)
        mac_endpoint.observe_runtime = lambda binding: json.loads((root / 'observed.json').read_text())
        asyncio.run(bridge.serve(mac_endpoint.Endpoint(bridge.trusted_json(root / 'endpoint.json'))))
        return
    if mode != '--driver' or args != ['mcp', '--socket', str(root / 'synthetic.sock')]:
        raise RuntimeError('Forbidden fixture process selector')
    for line in sys.stdin:
        request = json.loads(line)
        method, params = request['method'], request.get('params', {})
        if 'id' not in request:
            continue
        conf = control()
        if method == 'initialize':
            result = {'protocolVersion': '2025-06-18', 'capabilities': {'tools': {}},
                      'serverInfo': {'name': 'synthetic', 'version': conf.get('version', '0.26.1')}}
        elif method == 'tools/list':
            result = copy.deepcopy(FIXTURE['tools_list'])
            if conf.get('stateless_window_listing'):
                for tool in result['tools']:
                    if tool['name'] == 'list_windows':
                        tool['inputSchema']['properties'].pop('session', None)
                        tool['inputSchema']['additionalProperties'] = False
        elif method == 'ping':
            result = {}
        elif method == 'tools/call':
            name = params['name']
            if conf.get('stateless_window_listing') and name == 'list_windows':
                assert set(params.get('arguments', {})) <= {'pid', 'on_screen_only'}
            with (root / 'calls.jsonl').open('a') as out:
                out.write(json.dumps(params) + '\n')
            if conf.get('turnover_during') == name:
                observed = json.loads((root / 'observed.json').read_text())
                observed['runtime_pid'] += 1
                observed['socket_inode'] += 1
                (root / 'observed.json').write_text(json.dumps(observed))
            if name == 'click':
                if conf.get('delay'):
                    time.sleep(conf['delay'])
                if conf.get('disconnect'):
                    return
            if conf.get('malformed') == name:
                print('{broken SYNTHETIC_SECRET_SENTINEL', flush=True)
                continue
            if conf.get('oversize') == name:
                print('x' * (bridge.MAX_FRAME + 2), flush=True)
                continue
            result = copy.deepcopy(FIXTURE['windows_result'] if name == 'list_windows' else
                                   FIXTURE['capture_result'] if name == 'get_window_state' else FIXTURE['action_result'])
            if conf.get('empty') and name == 'list_windows':
                result['structuredContent']['windows'] = []
            if name == 'get_window_state' and conf.get('without_image'):
                result['content'] = [part for part in result['content'] if part['type'] != 'image']
            if name == 'get_window_state' and conf.get('invalid_image'):
                for part in result['content']:
                    if part['type'] == 'image':
                        part['data'] = base64.b64encode(b'not an image').decode('ascii')
            if name == 'get_window_state' and conf.get('empty_capture'):
                result = {'content': [{'type': 'text', 'text': 'No exact-window observation.'}],
                          'structuredContent': {'elements': [], 'tree_markdown': '',
                                                'screenshot_frame_valid': False}}
                if conf.get('degraded_capture'):
                    result['structuredContent'].update(degraded=True, elements_complete=False,
                        screenshot_error={'code': 'px_capture_unavailable',
                                          'reason': 'Synthetic window capture unavailable'})
            if conf.get('deny') == name:
                result = bridge.failure(conf.get('refusal_code', 'mac_permission_denied'))
            if conf.get('stderr'):
                print('SYNTHETIC_SECRET_SENTINEL' * 2000, file=sys.stderr, flush=True)
        else:
            raise RuntimeError('Forbidden driver method')
        print(json.dumps({'jsonrpc': '2.0', 'id': request['id'], 'result': result}), flush=True)


if __name__ == '__main__' and sys.argv[1:2] and sys.argv[1].startswith('--'):
    fixture(sys.argv[1], sys.argv[2], sys.argv[3:])
    raise SystemExit()


class Harness:
    def __init__(self, root):
        self.root = Path(root)
        self.audit = self.root / 'calls.jsonl'
        self.control = self.root / 'control.json'
        self.control.write_text('{}')
        self.state = self.root / 'state'
        self.state.mkdir(mode=0o700)
        self.driver = self.script('driver', '--driver')
        self.ssh = self.script('ssh', '--ssh')
        self.launcher = self.root / 'launcher'
        self.launcher.write_text(f'#!{sys.executable} -B\nimport runpy\nrunpy.run_path({str(ADAPTER / "bridge.py")!r}, run_name="__main__")\n')
        self.launcher.chmod(0o700)
        self.runtime = {'host': 'synthetic-mac', 'user': 'fixture', 'uid': os.getuid(), 'peer_uid': os.getuid(),
            'driver': str(self.driver), 'driver_sha256': digest(self.driver), 'runtime_pid': 12345,
            'permission_mode': 'standard', 'user_policy_sha256': None, 'managed_policy_sha256': None,
            'socket_inode': 123, 'socket': str(self.root / 'synthetic.sock')}
        self.observed = self.root / 'observed.json'
        self.observed.write_text(json.dumps(self.runtime))
        self.endpoint = self.root / 'endpoint.json'
        self.endpoint.write_text(json.dumps({'runtime': self.runtime, 'state_dir': str(self.state)}))
        known, key = self.root / 'known_hosts', self.root / 'key-ref'
        known.write_text('synthetic host key entry, no credential')
        key.write_text('synthetic reference, no private key')
        key.chmod(0o600)
        self.binding = {'target_node': 'macbook', 'host': 'synthetic.invalid', 'user': 'fixture',
            'ssh': str(self.ssh), 'ssh_sha256': digest(self.ssh), 'known_hosts': str(known),
            'known_hosts_sha256': digest(known), 'identity_file': str(key), 'runtime': self.runtime}
        self.config = self.root / 'binding.json'
        self.config.write_text(json.dumps(self.binding))

    def script(self, name, mode):
        path = self.root / name
        path.write_text(f'#!{sys.executable} -B\nimport runpy,sys\nsys.argv=[{str(HERE / "test_mac_endpoint.py")!r}, {mode!r}, {str(self.root)!r}, *sys.argv[1:]]\nrunpy.run_path({str(HERE / "test_mac_endpoint.py")!r}, run_name="__main__")\n')
        path.chmod(0o700)
        return path

    def calls(self):
        return [json.loads(s) for s in self.audit.read_text().splitlines()] if self.audit.exists() else []

    def set(self, **config):
        self.control.write_text(json.dumps(config))

    def env(self):
        return dict(os.environ, HERMES_MAC_BINDING=str(self.config), HERMES_CUA_DRIVER_CMD=str(self.launcher))


class DriverIdentityTests(unittest.TestCase):
    def test_driver_accepts_pinned_artifact_size_without_widening_frame_limit(self):
        with tempfile.TemporaryDirectory(prefix='loom-driver-size-') as root:
            driver = Path(root) / 'driver'
            with driver.open('wb') as stream:
                stream.truncate(63962080)
            driver.chmod(0o700)
            self.assertEqual(mac_endpoint.checked_driver(str(driver), digest(driver)), str(driver))
            self.assertEqual(bridge.MAX_FRAME, 8 * 1024 * 1024)
            with self.assertRaises(bridge.Fault):
                bridge.checked_file(str(driver), digest(driver))

    def test_driver_size_exception_keeps_identity_and_custody_checks(self):
        with tempfile.TemporaryDirectory(prefix='loom-driver-size-') as root:
            driver = Path(root) / 'driver'
            driver.write_bytes(b'synthetic executable')
            driver.chmod(0o700)
            expected = digest(driver)
            with self.assertRaises(bridge.Fault):
                mac_endpoint.checked_driver(str(driver), '0' * 64)
            link = Path(root) / 'link'
            link.symlink_to(driver)
            with self.assertRaises(OSError):
                mac_endpoint.checked_driver(str(link), expected)
            driver.chmod(0o722)
            with self.assertRaises(bridge.Fault):
                mac_endpoint.checked_driver(str(driver), expected)
            driver.chmod(0o700)
            with driver.open('wb') as stream:
                stream.truncate(128 * 1024 * 1024 + 1)
            with self.assertRaises(bridge.Fault):
                mac_endpoint.checked_driver(str(driver), expected)


class EndpointTests(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='loom-endpoint-')
        self.addCleanup(self.tmp.cleanup)
        self.h = Harness(self.tmp.name)
        self.peers = []
        self.counter = 0

    async def asyncTearDown(self):
        for peer in self.peers:
            await peer.close()
        pids = self.h.root / 'pids.jsonl'
        if pids.exists():
            for line in pids.read_text().splitlines():
                pid = json.loads(line)['pid']
                for _ in range(100):
                    try:
                        os.kill(pid, 0)
                    except ProcessLookupError:
                        break
                    await asyncio.sleep(0.005)
                else:
                    self.fail('Fixture child remained after bounded cleanup: ' + str(pid))

    async def peer(self, direct=False):
        p = bridge.Peer()
        self.peers.append(p)
        if direct:
            await p.start([sys.executable, '-B', str(HERE / 'test_mac_endpoint.py'), '--endpoint', str(self.h.root)])
        else:
            await p.start([str(self.h.launcher), 'mcp'], self.h.env())
        return p

    async def call(self, peer, method, params=None, timeout=4):
        self.counter += 1
        return await peer.exchange({'jsonrpc': '2.0', 'id': self.counter, 'method': method,
                                    'params': params or {}}, time.monotonic() + timeout)

    async def ready(self, direct=False):
        p = await self.peer(direct)
        init = await self.call(p, 'initialize', {'protocolVersion': '2025-06-18', 'capabilities': {},
                 'clientInfo': {'name': 'test', 'version': '1'}, '_meta': {'loom_binding': self.h.runtime}})
        self.assertIn('result', init)
        listing = await self.call(p, 'tools/list')
        self.assertEqual(listing['result']['capability_version'], '1')
        return p, init

    async def capture(self, p):
        result = await self.call(p, 'tools/call', {'name': 'get_window_state',
                   'arguments': {'pid': 4242, 'window_id': 701}})
        self.assertFalse(result['result']['isError'])
        return result

    def click_args(self):
        return {'name': 'click', 'arguments': {'pid': 4242, 'window_id': 701,
                'element_index': 1, 'element_token': 'sfixture:1'}}

    async def test_native_session_label_on_actual_stateless_window_schema(self):
        self.h.set(stateless_window_listing=True)
        p, _ = await self.ready()
        result = await self.call(p, 'tools/call', {'name': 'list_windows',
            'arguments': {'on_screen_only': True, 'session': 'caller-label-not-authority'}})
        self.assertFalse(result['result'].get('isError', False), result)
        calls = [c for c in self.h.calls() if c['name'] == 'list_windows']
        self.assertEqual(calls, [{'name': 'list_windows', 'arguments': {'on_screen_only': True}}])
        self.assertEqual(result['result']['structuredContent']['delivery'], 'observed')
        await self.capture(p)
        captures = [c for c in self.h.calls() if c['name'] == 'get_window_state']
        self.assertTrue(captures[0]['arguments']['session'].startswith('loom-'))
        self.assertNotEqual(captures[0]['arguments']['session'], 'caller-label-not-authority')

    def restart_driver(self):
        observed = json.loads(self.h.observed.read_text())
        observed['runtime_pid'] += 1
        observed['socket_inode'] += 1
        self.h.observed.write_text(json.dumps(observed))
        return observed

    async def test_restart_accepts_complete_legacy_binding_without_republication(self):
        before = self.h.config.read_bytes(), self.h.endpoint.read_bytes()
        observed = self.restart_driver()
        p, init = await self.ready()
        self.assertEqual(init['result']['_meta']['loom']['binding'], observed)
        result = await self.capture(p)
        self.assertFalse(result['result']['isError'])
        self.assertEqual(before, (self.h.config.read_bytes(), self.h.endpoint.read_bytes()))

    async def test_restart_same_connection_invalidates_targets_and_renegotiates(self):
        p, init = await self.ready()
        await self.capture(p)
        starts = [c['arguments']['session'] for c in self.h.calls() if c['name'] == 'start_session']
        self.restart_driver()
        refused = await self.call(p, 'tools/call', self.click_args())
        self.assertEqual(refused['result']['structuredContent']['code'], 'mac_target_stale')
        self.assertEqual(refused['result']['structuredContent']['delivery'], 'not_sent')
        self.assertFalse(any(c['name'] == 'click' for c in self.h.calls()))
        fresh = await self.capture(p)
        self.assertNotEqual(fresh['result']['structuredContent']['generation'], init['result']['_meta']['loom']['generation'])
        self.assertNotEqual([c['arguments']['session'] for c in self.h.calls() if c['name'] == 'start_session'][-1], starts[0])
        good = await self.call(p, 'tools/call', self.click_args())
        self.assertEqual(good['result']['structuredContent']['delivery'], 'confirmed')

    async def test_restart_each_connected_session_requires_its_own_fresh_capture(self):
        one, _ = await self.ready()
        two, _ = await self.ready()
        await self.capture(one)
        await self.capture(two)
        self.restart_driver()
        await self.capture(one)
        refused = await self.call(two, 'tools/call', self.click_args())
        self.assertEqual(refused['result']['structuredContent']['code'], 'mac_target_stale')
        self.assertFalse(any(c['name'] == 'click' for c in self.h.calls()))
        await self.capture(two)
        self.assertFalse((await self.call(two, 'tools/call', self.click_args()))['result']['isError'])

    async def test_runtime_turnover_during_input_is_unknown_and_never_replayed(self):
        p, _ = await self.ready()
        await self.capture(p)
        self.h.set(turnover_during='click')
        result = await self.call(p, 'tools/call', self.click_args())
        self.assertEqual(result['result']['structuredContent']['delivery'], 'unknown')
        self.assertEqual(len([c for c in self.h.calls() if c['name'] == 'click']), 1)
        self.assertEqual((await self.call(p, 'tools/call', self.click_args()))['result']['structuredContent']['code'], 'mac_target_stale')
        self.h.set()
        await self.capture(p)
        self.assertFalse((await self.call(p, 'tools/call', self.click_args()))['result']['isError'])
        self.assertEqual(len([c for c in self.h.calls() if c['name'] == 'click']), 2)

    async def test_runtime_turnover_during_capture_does_not_publish_stale_evidence(self):
        p, _ = await self.ready()
        self.h.set(turnover_during='get_window_state')
        result = await self.call(p, 'tools/call', {'name': 'get_window_state', 'arguments': {'pid': 4242, 'window_id': 701}})
        self.assertTrue(result['result']['isError'])
        self.assertEqual(result['result']['structuredContent']['code'], 'mac_target_stale')
        self.assertFalse(any(c.get('type') == 'image' for c in result['result']['content']))
        self.assertEqual((await self.call(p, 'tools/call', self.click_args()))['result']['structuredContent']['delivery'], 'not_sent')

    async def test_runtime_unavailable_then_restored_same_connection_recovers_read_only(self):
        p, _ = await self.ready()
        await self.capture(p)
        self.h.observed.unlink()
        failed = await self.call(p, 'tools/call', {'name': 'list_windows', 'arguments': {}})
        self.assertEqual(failed['result']['structuredContent']['code'], 'mac_driver_unavailable')
        self.h.observed.write_text(json.dumps(self.h.runtime))
        refused = await self.call(p, 'tools/call', self.click_args())
        self.assertEqual(refused['result']['structuredContent']['code'], 'mac_target_stale')
        await self.capture(p)
        self.assertFalse((await self.call(p, 'tools/call', self.click_args()))['result']['isError'])

    async def test_restart_preserves_all_stable_fields_and_rejects_incomplete_binding(self):
        changed = self.restart_driver()
        for key, value in [('host', 'different'), ('user', 'other'), ('uid', os.getuid()+1),
                           ('peer_uid', os.getuid()+1), ('driver_sha256', 'f'*64),
                           ('socket', '/different.sock'), ('permission_mode', 'bounded'),
                           ('user_policy_sha256', 'a'*64), ('managed_policy_sha256', 'b'*64)]:
            self.h.observed.write_text(json.dumps(dict(changed, **{key: value})))
            p = await self.peer()
            result = await self.call(p, 'initialize', {})
            self.assertEqual(result['error']['data']['code'], 'mac_host_identity_mismatch', key)
        self.h.observed.write_text(json.dumps(changed))
        incomplete = copy.deepcopy(self.h.binding)
        del incomplete['runtime']['managed_policy_sha256']
        self.h.config.write_text(json.dumps(incomplete))
        p = await self.peer()
        result = await self.call(p, 'initialize', {})
        self.assertEqual(result['error']['data']['code'], 'mac_host_identity_mismatch')
        self.assertEqual(self.h.calls(), [])

    async def test_stateless_listing_rejects_invalid_labels_and_other_properties(self):
        self.h.set(stateless_window_listing=True)
        p, _ = await self.ready()
        for extra in ({'session': None}, {'session': 1}, {'session': {}},
                      {'session': 'x' * 65537}, {'command': 'id'},
                      {'permission_mode': 'unrestricted'}, {'unknown': 'value'}):
            result = await self.call(p, 'tools/call', {'name': 'list_windows',
                'arguments': dict(on_screen_only=True, **extra)})
            self.assertEqual(result['result']['structuredContent']['code'], 'mac_policy_refused')
            self.assertEqual(result['result']['structuredContent']['delivery'], 'not_sent')
        self.assertEqual(self.h.calls(), [])

    async def test_degraded_empty_capture_preserves_desktop_failure_and_invalidates_target(self):
        p, _ = await self.ready()
        await self.capture(p)
        self.h.set(empty_capture=True, degraded_capture=True)
        failed = await self.call(p, 'tools/call', {'name': 'get_window_state',
            'arguments': {'pid': 4242, 'window_id': 701}})
        value = failed['result']
        self.assertTrue(value['isError'])
        self.assertEqual(value['structuredContent']['code'], 'mac_desktop_unavailable')
        self.assertEqual(value['structuredContent']['phase'], 'driver')
        self.assertEqual(value['structuredContent']['delivery'], 'observed')
        self.assertEqual(value['structuredContent']['screenshot_error']['code'], 'px_capture_unavailable')
        self.assertEqual(value['structuredContent']['elements'], [])
        refused = await self.call(p, 'tools/call', self.click_args())
        self.assertEqual(refused['result']['structuredContent']['code'], 'mac_target_stale')
        self.assertFalse(any(c['name'] == 'click' for c in self.h.calls()))
        self.h.set()
        await self.capture(p)
        restored = await self.call(p, 'tools/call', self.click_args())
        self.assertFalse(restored['result']['isError'])
        self.assertEqual(len([c for c in self.h.calls() if c['name'] == 'click']), 1)

    async def test_unknown_empty_capture_remains_failed_after_observation(self):
        self.h.set(empty_capture=True)
        p, _ = await self.ready()
        failed = await self.call(p, 'tools/call', {'name': 'get_window_state',
            'arguments': {'pid': 4242, 'window_id': 701}})
        self.assertTrue(failed['result']['isError'])
        self.assertEqual(failed['result']['structuredContent']['code'], 'mac_driver_unavailable')
        self.assertEqual(failed['result']['structuredContent']['phase'], 'response')
        self.assertEqual(failed['result']['structuredContent']['delivery'], 'observed')

    async def test_timeout_after_send_and_stale_snapshot_refusal(self):
        self.h.set(budget=0.1, delay=1)
        p, _ = await self.ready(True)
        await self.capture(p)
        wrong = self.click_args()
        wrong['arguments']['element_token'] = 'stale'
        refusal = await self.call(p, 'tools/call', wrong)
        self.assertEqual(refusal['result']['structuredContent']['code'], 'mac_target_stale')
        start = time.monotonic()
        result = await self.call(p, 'tools/call', self.click_args())
        self.assertEqual(result['result']['structuredContent']['delivery'], 'unknown')
        self.assertLess(time.monotonic() - start, 1.5)
        self.assertEqual(len([c for c in self.h.calls() if c['name'] == 'click']), 1)

    async def test_no_policy_widening_or_identity_change_after_handshake(self):
        p, _ = await self.ready(True)
        await self.capture(p)
        observed = dict(self.h.runtime, permission_mode='unrestricted')
        self.h.observed.write_text(json.dumps(observed))
        refused = await self.call(p, 'tools/call', self.click_args())
        self.assertEqual(refused['result']['structuredContent']['delivery'], 'not_sent')
        self.assertEqual(len([c for c in self.h.calls() if c['name'] == 'click']), 0)

    async def test_driver_version_desktop_and_missing_key_errors(self):
        self.h.set(version='0.19.9')
        p = await self.peer()
        incompatible = await self.call(p, 'initialize', {})
        self.assertEqual(incompatible['error']['data']['code'], 'mac_driver_unavailable')
        self.assertEqual(self.h.calls(), [])
        self.h.set(deny='get_window_state', refusal_code='mac_desktop_unavailable')
        p, _ = await self.ready()
        desktop = await self.call(p, 'tools/call', {'name': 'get_window_state', 'arguments': {'pid': 4242, 'window_id': 701}})
        self.assertEqual(desktop['result']['structuredContent']['code'], 'mac_desktop_unavailable')
        self.assertEqual(desktop['result']['structuredContent']['delivery'], 'not_sent')
        self.h.set()
        (self.h.root / 'key-ref').unlink()
        p = await self.peer()
        auth = await self.call(p, 'initialize', {})
        self.assertEqual(auth['error']['data']['code'], 'mac_auth_failed')
        self.assertEqual(len([c for c in self.h.calls() if c['name'] == 'click']), 0)

    async def test_identity_extensions_images_and_fixed_ssh(self):
        p, init = await self.ready()
        self.assertEqual(init['result']['_meta']['loom']['binding'], self.h.runtime)
        result = await self.capture(p)
        image = result['result']['content'][1]
        self.assertEqual(image['type'], 'image')
        self.assertTrue(base64.b64decode(image['data']).startswith(b'\x89PNG'))
        response = await self.call(p, 'tools/call', self.click_args())
        self.assertEqual(response['result']['structuredContent']['delivery'], 'confirmed')
        self.assertEqual(len([c for c in self.h.calls() if c['name'] == 'click']), 1)
        self.assertEqual(json.loads((self.h.root / 'ssh-args.json').read_text()), bridge.ssh_vector(self.h.binding)[1:])
        self.assertNotIn('SYNTHETIC_SECRET_SENTINEL', json.dumps(response))
        vector = json.loads((self.h.root / 'ssh-args.json').read_text())
        for option in ['-oStrictHostKeyChecking=yes', '-oBatchMode=yes', '-oIdentitiesOnly=yes',
                       '-oForwardAgent=no', '-oClearAllForwardings=yes', '-oUpdateHostKeys=no',
                       '-oIdentityAgent=none', '-oPasswordAuthentication=no', '-oProxyCommand=none']:
            self.assertIn(option, vector)
        starts = [c for c in self.h.calls() if c['name'] == 'start_session']
        self.assertEqual(len(starts), 1)
        self.assertTrue(starts[0]['arguments']['session'].startswith('loom-'))
        second = await self.call(p, 'tools/call', self.click_args())
        self.assertFalse(second['result']['isError'])
        self.assertEqual(len([c for c in self.h.calls() if c['name'] == 'click']), 2)

    async def test_wrong_host_user_driver_and_socket_are_refused(self):
        for key in ['host', 'user', 'driver', 'driver_sha256', 'socket_inode', 'peer_uid', 'runtime_pid']:
            observed = dict(self.h.runtime, **{key: 'substituted'})
            self.h.observed.write_text(json.dumps(observed))
            p = await self.peer(True)
            result = await self.call(p, 'initialize', {'_meta': {'loom_binding': self.h.runtime}})
            self.assertEqual(result['error']['data']['code'], 'mac_host_identity_mismatch')
            self.assertEqual(self.h.calls(), [])

    async def test_offline_auth_host_failures_are_distinct_redacted(self):
        for fault, code in [('offline', 'mac_unreachable'), ('auth', 'mac_auth_failed'), ('host', 'mac_host_identity_mismatch')]:
            self.h.set(ssh=fault)
            p = await self.peer()
            result = await self.call(p, 'initialize', {})
            self.assertEqual(result['error']['data']['code'], code)
            self.assertEqual(result['error']['data']['delivery'], 'not_sent')
            self.assertNotIn('SYNTHETIC_SECRET_SENTINEL', json.dumps(result))
        self.assertEqual(self.h.calls(), [])

    async def test_set_value_and_control_refused_at_direct_admission(self):
        p, _ = await self.ready(True)
        for name, args in [('set_value', {}), ('set_config', {}), ('serve', {}), ('click', {'command': 'sentinel'})]:
            result = await self.call(p, 'tools/call', {'name': name, 'arguments': args})
            self.assertEqual(result['result']['structuredContent']['code'], 'mac_policy_refused')
        self.assertEqual(self.h.calls(), [])

    async def test_stale_target_and_cross_process_input_serialization(self):
        p, _ = await self.ready(True)
        q, _ = await self.ready(True)
        await self.capture(p)
        await self.capture(q)
        self.h.set(delay=0.3)
        task = asyncio.create_task(self.call(p, 'tools/call', self.click_args()))
        for _ in range(100):
            if any(c['name'] == 'click' for c in self.h.calls()):
                break
            await asyncio.sleep(0.005)
        busy = await self.call(q, 'tools/call', self.click_args())
        self.assertEqual(busy['result']['structuredContent']['code'], 'mac_busy')
        await task
        stale = await self.call(q, 'tools/call', self.click_args())
        self.assertEqual(stale['result']['structuredContent']['code'], 'mac_target_stale')
        self.assertEqual(len([c for c in self.h.calls() if c['name'] == 'click']), 1)

    async def test_disconnect_unknown_no_replay_and_fresh_generation(self):
        p, init = await self.ready()
        await self.capture(p)
        self.h.set(disconnect=True)
        result = await self.call(p, 'tools/call', self.click_args())
        self.assertEqual(result['result']['structuredContent']['delivery'], 'unknown')
        denied = await self.call(p, 'tools/call', self.click_args())
        self.assertEqual(denied['result']['structuredContent']['code'], 'mac_target_stale')
        self.h.set()
        q, new = await self.ready()
        self.assertNotEqual(init['result']['_meta']['loom']['generation'], new['result']['_meta']['loom']['generation'])
        denied = await self.call(q, 'tools/call', self.click_args())
        self.assertEqual(denied['result']['structuredContent']['code'], 'mac_target_stale')
        await self.capture(q)
        good = await self.call(q, 'tools/call', self.click_args())
        self.assertFalse(good['result']['isError'])
        self.assertEqual(len([c for c in self.h.calls() if c['name'] == 'click']), 2)

    async def test_cancel_after_send_unknown_and_owned_cleanup(self):
        p, _ = await self.ready()
        await self.capture(p)
        self.h.set(delay=2)
        self.counter += 1
        request_id = self.counter
        task = asyncio.create_task(p.exchange({'jsonrpc': '2.0', 'id': request_id, 'method': 'tools/call',
                                  'params': self.click_args()}, time.monotonic() + 4))
        for _ in range(100):
            if any(c['name'] == 'click' for c in self.h.calls()):
                break
            await asyncio.sleep(0.005)
        p.process.stdin.write(bridge.encode({'jsonrpc': '2.0', 'method': 'notifications/cancelled',
                                            'params': {'requestId': request_id}}))
        await p.process.stdin.drain()
        result = await task
        self.assertEqual(result['result']['structuredContent']['delivery'], 'unknown')
        self.assertEqual(len([c for c in self.h.calls() if c['name'] == 'click']), 1)
        await p.close()
        self.assertIsNotNone(p.process.returncode)

    async def test_malformed_oversize_permission_and_stderr(self):
        for config, code in [({'malformed': 'get_window_state'}, 'mac_protocol_error'),
                             ({'oversize': 'get_window_state'}, 'mac_output_limit'),
                             ({'deny': 'get_window_state', 'stderr': True}, 'mac_permission_denied')]:
            self.h.set(**config)
            p, _ = await self.ready()
            result = await self.call(p, 'tools/call', {'name': 'get_window_state', 'arguments': {'pid': 4242, 'window_id': 701}})
            self.assertEqual(result['result']['structuredContent']['code'], code)
            self.assertNotIn('SYNTHETIC_SECRET_SENTINEL', json.dumps(result))


def load_tests(loader, tests, pattern):
    if os.environ.get('LOOM_NATIVE_STRESS_ONLY'):
        suite = unittest.TestSuite()
        for _ in range(20):
            for name in ['test_cancel_after_send_unknown_and_owned_cleanup',
                         'test_disconnect_unknown_no_replay_and_fresh_generation',
                         'test_stale_target_and_cross_process_input_serialization',
                         'test_timeout_after_send_and_stale_snapshot_refusal']:
                suite.addTest(EndpointTests(name))
        return suite
    return unittest.TestSuite([loader.loadTestsFromTestCase(DriverIdentityTests),
                               loader.loadTestsFromTestCase(EndpointTests)])

if __name__ == '__main__':
    unittest.main()
