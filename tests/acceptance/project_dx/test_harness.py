"""Deterministic cleanup regression fixtures; no PostgreSQL/model is launched."""
import contextlib
import concurrent.futures
import errno
import os
import io
import json
from pathlib import Path
import tempfile
import sys
import unittest
from unittest.mock import patch

import harness


class ClusterCleanupTest(unittest.TestCase):
    def setUp(self):
        self.scratch = tempfile.TemporaryDirectory(prefix='cleanup-test-', dir=harness.REPO / '.loom-acceptance')
        self.addCleanup(self.scratch.cleanup)
        self.base = Path(self.scratch.name)
        self.root = self.base / 'owned'
        self.root.mkdir()
        self.out = self.base / 'evidence'
        self.out.mkdir()
        self.runtime = self.out / 'pg-runtime'
        self.pg = self.base / 'fake-pg' / 'bin'
        self.failures = set()
        self.calls = []
        self.stderr = io.StringIO()

    def fake_run(self, args, log, **kwargs):
        stage = Path(log).stem
        self.calls.append(stage)
        if stage == 'pg-root':
            # A dangling GC-root link must still count as retained residue.
            self.runtime.symlink_to(self.base / 'fake-runtime')
        if stage == 'initdb':
            (self.root / 'data').mkdir()
            (self.root / 'data' / 'sentinel').write_bytes(b'owned database bytes')
            (self.root / 'server.log').write_text('original server log\n')
        if stage in self.failures:
            raise RuntimeError('injected ' + stage)

    @contextlib.contextmanager
    def owned_cluster(self):
        with patch.object(harness.tempfile, 'mkdtemp', return_value=str(self.root)), \
             patch.object(harness, 'run', side_effect=self.fake_run), \
             contextlib.redirect_stderr(self.stderr):
            with harness.cluster(self.out, self.pg) as value:
                yield value

    def receipt(self):
        return json.loads((self.out / 'cleanup.json').read_text())

    def test_inventory_failure_still_stops_and_removes_owned_roots(self):
        self.failures.add('db-cleanup-inventory')
        with self.owned_cluster():
            pass
        receipt = self.receipt()
        self.assertLess(self.calls.index('db-cleanup-inventory'), self.calls.index('pg-stop'))
        self.assertTrue(receipt['postgres_stopped'])
        self.assertTrue(receipt['root_removed'])
        self.assertTrue(receipt['runtime_root_removed'])
        self.assertEqual(receipt['cleanup_errors'], [])
        self.assertEqual(receipt['diagnostic_errors'][0]['stage'], 'inventory')
        self.assertEqual((self.out / 'postgres.log').read_text(), 'original server log\n')
        self.assertIn('injected db-cleanup-inventory', self.stderr.getvalue())

    def test_inventory_failure_does_not_replace_primary_failure(self):
        self.failures.add('db-cleanup-inventory')
        primary = ValueError('fixture assertion failed')
        with self.assertRaises(ValueError) as caught:
            with self.owned_cluster():
                raise primary
        self.assertIs(caught.exception, primary)
        receipt = self.receipt()
        self.assertEqual(receipt['primary_error']['error'], str(primary))
        self.assertTrue(receipt['postgres_stopped'])
        self.assertTrue(receipt['root_removed'])

    def test_stop_failure_retains_data_and_runtime_alongside_primary(self):
        self.failures.update(('db-cleanup-inventory', 'pg-stop'))
        primary = ValueError('fixture assertion failed')
        with self.assertRaises(ValueError) as caught:
            with self.owned_cluster():
                raise primary
        self.assertIs(caught.exception, primary)
        receipt = self.receipt()
        self.assertFalse(receipt['postgres_stopped'])
        self.assertFalse(receipt['root_removed'])
        self.assertFalse(receipt['runtime_root_removed'])
        self.assertTrue(self.runtime.is_symlink())
        self.assertEqual((self.root / 'data' / 'sentinel').read_bytes(), b'owned database bytes')
        self.assertEqual(receipt['cleanup_errors'][0]['stage'], 'postgres_stop')
        for message in ('fixture assertion failed', 'injected db-cleanup-inventory', 'injected pg-stop'):
            self.assertIn(message, self.stderr.getvalue())

    def test_stop_failure_is_reported_when_body_succeeds(self):
        self.failures.add('pg-stop')
        with self.assertRaisesRegex(RuntimeError, 'injected pg-stop'):
            with self.owned_cluster():
                pass
        self.assertIsNone(self.receipt()['primary_error'])
        self.assertTrue(self.root.exists())
        self.assertTrue(self.runtime.is_symlink())

    def test_failed_start_still_attempts_stop_and_preserves_unknown_residue(self):
        self.failures.update(('pg-start', 'pg-stop'))
        with self.assertRaisesRegex(RuntimeError, 'injected pg-start'):
            with self.owned_cluster():
                self.fail('failed startup must not yield')
        self.assertIn('pg-stop', self.calls)
        self.assertTrue(self.receipt()['postgres_start_attempted'])
        self.assertFalse(self.receipt()['postgres_stopped'])
        self.assertTrue(self.root.exists())
        self.assertTrue(self.runtime.is_symlink())

    def test_unconfirmed_native_process_preserves_root_after_pg_stop(self):
        with self.assertRaisesRegex(RuntimeError, 'unconfirmed native process cleanup'):
            with self.owned_cluster():
                (self.root / '.native-process-123.json').write_text('{"pid":123}')
        self.assertTrue(self.receipt()['postgres_stopped'])
        self.assertFalse(self.receipt()['root_removed'])
        self.assertTrue(self.runtime.is_symlink())

    def test_failed_start_can_clean_after_confirmed_stop(self):
        self.failures.add('pg-start')
        with self.assertRaisesRegex(RuntimeError, 'injected pg-start'):
            with self.owned_cluster():
                self.fail('failed startup must not yield')
        self.assertTrue(self.receipt()['postgres_stopped'])
        self.assertFalse(self.root.exists())
        self.assertFalse(self.runtime.is_symlink())



class FixtureOwnerCleanupTest(unittest.TestCase):
    def setUp(self):
        self.scratch = tempfile.TemporaryDirectory(dir=harness.REPO / '.loom-acceptance')
        self.addCleanup(self.scratch.cleanup)
        self.root = Path(self.scratch.name)
        self.out = self.root / 'evidence'
        (self.out / 'B').mkdir(parents=True)
        (self.out / 'httpapi.test').touch()
        (self.out / 'B/ready.json').write_text('{}')
        self.stderr = io.StringIO()
        self.stops = []

        class Owner:
            pid, returncode = 123456, None

            def wait(owner, timeout):
                self.stops.append(timeout)
                owner.returncode = self.exit_code

        self.owner = Owner()
        self.exit_code = 0

    @contextlib.contextmanager
    def launch(self):
        with patch.object(harness.subprocess, 'Popen', return_value=self.owner), \
             contextlib.redirect_stderr(self.stderr):
            with harness.fixture_owner(self.root, {}, self.out, 'B'):
                yield

    def receipt(self):
        return json.loads((self.out / 'B-owner-cleanup.json').read_text())

    def test_failed_pid_receipt_still_stops_launched_owner(self):
        original = harness.write

        def write(path, value):
            if path.name.startswith('.native-process-owner-') and value.get('pid'):
                raise OSError('injected pid receipt failure')
            original(path, value)

        with patch.object(harness, 'write', side_effect=write):
            with self.assertRaisesRegex(OSError, 'injected pid receipt failure'):
                with self.launch():
                    self.fail('launch failure must not yield')
        self.assertEqual(self.stops, [5])
        self.assertTrue(self.receipt()['stopped'])
        self.assertEqual(list(self.root.glob('.native-process-*')), [])

    def test_stop_file_failure_preserves_primary_and_still_waits(self):
        primary = ValueError('primary canary failure')
        with patch.object(Path, 'touch', side_effect=OSError('injected stop file failure')):
            with self.assertRaises(ValueError) as caught:
                with self.launch():
                    raise primary
        self.assertIs(caught.exception, primary)
        self.assertEqual(self.stops, [5])
        self.assertTrue(self.receipt()['stopped'])
        self.assertEqual(self.receipt()['cleanup_errors'][0]['stage'], 'stop_file')
        self.assertIn('injected stop file failure', '\n'.join(primary.__notes__))

    def test_unconfirmed_shutdown_preserves_marker(self):
        with patch.object(self.owner, 'wait', side_effect=OSError('injected wait failure')), \
             patch.object(harness.os, 'killpg') as kill:
            with self.assertRaises(harness.NativeBlocked):
                with self.launch():
                    pass
        self.assertEqual(kill.call_count, 2)
        self.assertFalse(self.receipt()['stopped'])
        self.assertEqual(len(list(self.root.glob('.native-process-*'))), 1)

    def test_nonzero_owner_exit_fails_successful_body(self):
        self.exit_code = 1
        with self.assertRaisesRegex(harness.NativeBlocked, 'fixture owner exit 1'):
            with self.launch():
                pass
        self.assertTrue(self.receipt()['stopped'])
        self.assertEqual(self.receipt()['exit_code'], 1)


class NativeRPCTest(unittest.TestCase):
    SCRIPT = r"""
import json, sys
waiting = []
for raw in sys.stdin:
    m = json.loads(raw)
    method = m.get('method')
    if method == 'crash':
        sys.exit(3)
    if method == 'hang':
        continue
    if method == 'approval':
        print(json.dumps({'id': 900, 'method': 'item/commandExecution/requestApproval',
              'params': {'threadId': 't', 'turnId': 'u', 'itemId': 'i'}}), flush=True)
        response = json.loads(sys.stdin.readline())
        print(json.dumps({'id': m['id'], 'result': response}), flush=True)
        continue
    if method == 'pair':
        waiting.append(m)
        if len(waiting) < 2:
            continue
        for x in reversed(waiting):
            print(json.dumps({'id': x['id'], 'result': x['params']}), flush=True)
        waiting.clear()
        continue
    print(json.dumps({'method': 'unknown/testNotification', 'params': {'value': 1}}), flush=True)
    reply = json.dumps({'id': m['id'], 'result': m.get('params')}) + '\n'
    sys.stdout.write(reply[:7]); sys.stdout.flush()
    sys.stdout.write(reply[7:]); sys.stdout.flush()
"""

    def setUp(self):
        self.scratch = tempfile.TemporaryDirectory(prefix='rpc-test-', dir=harness.REPO / '.loom-acceptance')
        self.addCleanup(self.scratch.cleanup)
        self.out = Path(self.scratch.name)
        self.rpc = harness.NativeRPC([sys.executable, '-B', '-u', '-c', self.SCRIPT],
                                     self.out, harness.clean_env(), self.out, owned_root=self.out)
        self.addCleanup(self.rpc.close)

    def test_fragmented_records_and_unknown_notifications_are_retained(self):
        self.assertEqual(self.rpc.request('echo', {'value': '✓'}), {'value': '✓'})
        notification = self.rpc.events.get(timeout=1)
        self.assertEqual(notification['method'], 'unknown/testNotification')
        records = [json.loads(x) for x in (self.out / 'native-rpc.jsonl').read_text().splitlines()]
        self.assertEqual(len(records), 3)

    def test_interleaved_responses_are_correlated_by_id(self):
        with concurrent.futures.ThreadPoolExecutor(2) as workers:
            futures = [workers.submit(self.rpc.request, 'pair', {'index': i}) for i in range(2)]
            self.assertEqual([f.result(timeout=2) for f in futures], [{'index': 0}, {'index': 1}])

    def test_approval_is_cancelled_without_grant(self):
        response = self.rpc.request('approval')
        self.assertEqual(response, {'id': 900, 'result': {'decision': 'cancel'}})

    def test_eof_wakes_pending_request(self):
        with self.assertRaises(EOFError):
            self.rpc.request('crash', timeout=2)

    def test_request_deadline(self):
        with self.assertRaisesRegex(TimeoutError, 'native request deadline'):
            self.rpc.request('hang', timeout=.02)


class NativeEvidenceTest(unittest.TestCase):
    def test_config_and_hook_evidence_excludes_secret_values(self):
        config = {'id': 1, 'result': {'config': {'developer_instructions': 'PRIVATE_TEXT',
            'mcp_servers': {'x': {'enabled': False, 'env': {'TOKEN': 'SECRET_VALUE'}}}},
            'layers': [{'name': {'type': 'user', 'file': '/user/config.toml'},
                        'config': {'opaque_secret': 'SECRET_VALUE'}}], 'origins': {}}}
        rendered = json.dumps(harness.rpc_evidence(config, 'config/read'))
        self.assertNotIn('PRIVATE_TEXT', rendered)
        self.assertNotIn('SECRET_VALUE', rendered)
        hook = {'id': 2, 'result': {'data': [{'hooks': [{'enabled': False, 'command': 'SECRET_VALUE'}]}]}}
        self.assertNotIn('SECRET_VALUE', json.dumps(harness.rpc_evidence(hook, 'hooks/list')))

    def test_cumulative_usage_is_not_summed_and_missing_is_not_zero(self):
        self.assertIsNone(harness.native_usage([])['input_output_tokens'])
        events = []
        for turn, input_tokens, output_tokens in [('u1', 100, 5), ('u1', 100, 5), ('u2', 150, 9)]:
            events.append({'method': 'thread/tokenUsage/updated', 'params': {'threadId': 't',
                'turnId': turn, 'tokenUsage': {'total': {'inputTokens': input_tokens,
                'outputTokens': output_tokens, 'cachedInputTokens': 80, 'reasoningOutputTokens': 2}}}})
        usage = harness.native_usage(events)
        self.assertEqual(usage['input_output_tokens'], 159)
        self.assertEqual(len(usage['phase_cumulative']), 2)

    def test_unapproved_instruction_source_blocks_before_content_read(self):
        value = {'instructionSources': ['/unapproved/AGENTS.md'],
                 'thread': {'ephemeral': True, 'path': None},
                 'activePermissionProfile': {'id': 'g2b-fixture'}}
        with patch.object(Path, 'read_text', side_effect=AssertionError('must not read contents')):
            with self.assertRaisesRegex(harness.NativeBlocked, 'unapproved instruction source'):
                harness.verify_sources(value, set())

    def test_discovery_zero_requires_actual_numeric_control_and_source_absence(self):
        harness.verify_discovery_disabled({'project_doc_max_bytes': 0})
        self.assertEqual(harness.config_summary({'project_doc_max_bytes': 0})['project_doc_max_bytes'], 0)
        for value in (None, False, "0", 1):
            with self.assertRaises(harness.NativeBlocked):
                harness.verify_discovery_disabled({'project_doc_max_bytes': value})
        thread = {'instructionSources': ['/global/AGENTS.md'], 'thread': {'ephemeral': True},
                  'activePermissionProfile': {'id': 'g2b-fixture'}}
        with self.assertRaises(harness.NativeBlocked):
            harness.verify_sources(thread, set())

    def test_approved_source_requires_exact_path_without_alias_normalization(self):
        thread = {'instructionSources': ['/global/AGENTS.md'], 'thread': {'ephemeral': True},
                  'activePermissionProfile': {'id': 'g2b-fixture'}}
        harness.verify_sources(thread, {'/global/AGENTS.md'})
        thread['instructionSources'] = ['/global/../global/AGENTS.md']
        with self.assertRaises(harness.NativeBlocked):
            harness.verify_sources(thread, {'/global/AGENTS.md'})

    def test_disabled_mcp_inventory_is_distinct_from_active_tools(self):
        config = {'mcp_servers': {'x': {'enabled': False}}}
        inventory = {'data': [{'name': 'x', 'tools': {}, 'runtimeStatus': None}]}
        harness.verify_mcp_disabled(config, inventory)
        inventory['data'][0]['tools'] = {'unsafe_tool': {}}
        with self.assertRaises(harness.NativeBlocked):
            harness.verify_mcp_disabled(config, inventory)

    def test_session_overrides_target_literal_names_and_skill_files(self):
        with tempfile.TemporaryDirectory(dir=harness.REPO / '.loom-acceptance') as root:
            home = Path(root)
            skill = home / '.codex' / 'skills' / 'example' / 'SKILL.md'
            skill.parent.mkdir(parents=True)
            skill.write_text('Private body must not be loaded')
            (home / '.codex' / 'config.toml').write_text('[mcp_servers.example]\ncommand="unused"\n')
            with patch.object(Path, 'home', return_value=home):
                controls = harness.native_session_controls(home / 'participant')
            self.assertIs(controls['mcp_servers.example.enabled'], False)
            self.assertEqual(controls['skills.config'], [{'path': str(skill), 'enabled': False}])

    def test_session_toml_round_trip_and_exact_socket_policy(self):
        policy = harness.permission_controls(Path('/tmp/fixture/participant'), Path('/tmp/fixture/bridge.sock'), False)
        encoded = '\n'.join(k + '=' + harness.toml_value(v) for k, v in policy.items())
        parsed = harness.tomllib.loads(encoded)
        net = parsed['permissions']['g2b-fixture']['network']
        self.assertEqual(net['unix_sockets'], {str(Path('/tmp/fixture/bridge.sock').resolve()): 'allow'})
        self.assertFalse(net['dangerously_allow_all_unix_sockets'])
        self.assertEqual(parsed['permissions']['g2b-fixture']['filesystem'][str(Path('/tmp/fixture/participant').resolve())], 'read')



class CanaryLogicTest(unittest.TestCase):
    def evaluate(self, denied_errno=errno.EPERM, allow_writes=False):
        parameters = {'bridge':'/fixture/bridge', 'sibling':'/fixture/sibling',
            'sibling_alias':'/fixture/sibling-alias', 'postgres':'/fixture/pg',
            'port':4321, 'secret':'/fixture/secret', 'evidence_secret':'/evidence/secret',
            'readable':'/case/readable', 'nonce':'canary', 'writable':False,
            'writable_file':'/case/write', 'outside_write':'/fixture/write', 'allow_bridge':True}
        test = self
        class FakeSocket:
            def settimeout(self, value): pass
            def connect(self, address):
                if address != parameters['bridge']:
                    raise OSError(denied_errno, 'injected denial')
            def sendall(self, body):
                test.assertIn(b'\r\nHost:', body)
            def recv(self, count): return b'HTTP/1.1 403 Forbidden\r\n\r\n'
            def close(self): pass
            def __enter__(self): return self
            def __exit__(self, *args): pass
        def fake_open(path, mode='r'):
            if path == parameters['readable']:
                return io.StringIO('canary')
            if 'w' in mode and allow_writes:
                return io.StringIO()
            raise PermissionError(errno.EPERM, 'injected file denial')
        output = io.StringIO()
        with patch.object(harness.socket, 'socket', side_effect=lambda *a:FakeSocket()), \
             patch.object(harness.socket, 'create_connection', return_value=FakeSocket()), \
             patch('builtins.open', side_effect=fake_open), \
             patch.object(sys, 'argv', ['canary',json.dumps(parameters)]), \
             patch.dict(os.environ, {'HTTP_PROXY':'http://127.0.0.1:1234'}, clear=True), \
             contextlib.redirect_stdout(output):
            with self.assertRaises(SystemExit) as exit_result:
                exec(compile(harness.CANARY_PROGRAM, '<canary>', 'exec'), {})
        return exit_result.exception.code, json.loads(output.getvalue())

    def test_only_actual_permission_denial_qualifies(self):
        code, checks = self.evaluate()
        self.assertEqual(code, 0)
        self.assertTrue(all(x['pass'] for x in checks.values()))

    def test_missing_endpoint_is_not_misreported_as_permission_denial(self):
        code, checks = self.evaluate(denied_errno=errno.ENOENT)
        self.assertEqual(code, 2)
        self.assertFalse(checks['sibling_socket']['pass'])

    def test_working_network_guard_does_not_hide_filesystem_escape(self):
        code, checks = self.evaluate(allow_writes=True)
        self.assertEqual(code, 2)
        self.assertTrue(checks['proxied_tcp']['pass'])
        self.assertFalse(checks['fixture_write']['pass'])
        self.assertFalse(checks['outside_write']['pass'])


if __name__ == '__main__':
    unittest.main()
