#!/usr/bin/env python3
"""Owned real-service prerequisites and fail-closed native cold-entry preflight.

No result from this file is a model trial unless its native raw events qualify.
Seeds/oracles live outside participant roots. Python uses only its standard library.
"""
import argparse
import contextlib
import hashlib
import json
import os
from pathlib import Path
import queue
import shutil
import signal
import socket
import socketserver
import threading
import subprocess
import sys
import tempfile
import time
import tomllib

REPO = Path(__file__).resolve().parents[3]
CORPUS_PATH = Path(__file__).with_name('cold_cases.json')
CORPUS = json.loads(CORPUS_PATH.read_text())
PG_DEFAULT = '/nix/store/ixbp523z913y6dbqzhz7di3x039w4f54-postgresql-and-plugins-17.9/bin'
NATIVE = Path('/opt/homebrew/lib/node_modules/@openai/codex/node_modules/@openai/codex-darwin-arm64/vendor/aarch64-apple-darwin/bin/codex')
GLOBAL_PROBE_SHA = 'a08b7ef65be06e94c39eaf1aec4d14fa89e94f58a98678268da89830aa308228'
SELECTORS = {'B': ('httpapi', 'TestProjectDXProjectsFixture'),
             'C': ('knowledge', 'TestProjectDXNotesFixture'),
             'D': ('provenance', 'TestProjectDXProvenanceFixture')}


def sha(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def write(path, value):
    Path(path).write_text(json.dumps(value, indent=2) + '\n')


def clean_env():
    return {'HOME': str(Path.home()), 'PATH': '/opt/homebrew/bin:/usr/bin:/bin',
            'TMPDIR': '/tmp', 'GOPROXY': 'off', 'GOSUMDB': 'off', 'GOWORK': 'off'}


def run(args, log, env=None, cwd=REPO, timeout=900):
    started = time.monotonic()
    with open(log, 'wb') as out:
        process = subprocess.Popen([str(x) for x in args], stdout=out, stderr=subprocess.STDOUT,
                                   env=env or clean_env(), cwd=cwd, start_new_session=True)
        try:
            code = process.wait(timeout=timeout)
        except BaseException:
            os.killpg(process.pid, signal.SIGTERM)
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait()
            raise
    write(str(log) + '.command.json', {'argv': [str(x) for x in args], 'exit_code': code,
                                      'elapsed_seconds': time.monotonic() - started})
    if code:
        raise RuntimeError(f'command failed ({code}); original output retained: {log}')


def count_tests(path):
    counts = {'pass': 0, 'fail': 0, 'skip': 0}
    for line in Path(path).read_text().splitlines():
        try:
            event = json.loads(line)
        except ValueError:
            continue
        if event.get('Test') and event.get('Action') in counts:
            counts[event['Action']] += 1
    return counts


def native_observations(events, bridge_calls):
    """Facts available from native events; never participant-reported metrics."""
    commands = [e['item'] for e in events if e.get('type') == 'item.completed'
                and e.get('item', {}).get('type') == 'command_execution']
    usage = [e['usage'] for e in events if e.get('type') == 'turn.completed' and 'usage' in e]
    return {
        'thread_ids': [e['thread_id'] for e in events if e.get('type') == 'thread.started'],
        'observed_command_count': len(commands),
        'commands': commands,
        'bridge_request_count': bridge_calls,
        'fixture_transport': 'observed' if bridge_calls else 'not established; inspect command output',
        'provider_usage_events': usage,
        'provider_input_tokens': sum(u['input_tokens'] for u in usage) if usage else None,
        'provider_output_tokens': sum(u['output_tokens'] for u in usage) if usage else None,
        'cached_input_tokens_subset': sum(u.get('cached_input_tokens', 0) for u in usage) if usage else None,
        'resolved_model': None,
        'resolved_model_evidence': 'not exposed in the captured exec events',
        'trial_usage': 'not applicable; transport preflight only',
        'instruction_inventory': 'full injected instructions/tools not exposed in captured exec events',
    }


def pin_sources(out):
    # Frozen proposal history includes planning files subsequently changed by the
    # accepted integrator dispatch. Entrypoints/references must match current bytes.
    pins = CORPUS['source_pins']
    sources = [str(p.relative_to(REPO)) for p in CORPUS_PATH.parent.iterdir() if p.is_file()]
    sources += ['internal/' + pkg + '/project_dx_acceptance_test.go' for pkg in ('httpapi','knowledge','provenance')]
    sources += ['tests/smoke/v2_project_developer_experience_local.sh']
    for relative in sources:
        target = out / 'fixture-source' / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(REPO / relative, target)
    write(out / 'fixture-source-hashes.json', {p: sha(REPO / p) for p in sources})
    selected = {p for paths in CORPUS['entry'].values() for p in paths}
    selected.update(CORPUS['references'].values())
    actual = {p: sha(REPO / p) for p in selected}
    if any(actual[p] != pins[p] for p in selected):
        raise RuntimeError('source entry/reference drift from frozen F4a manifest')
    entries = {}
    for case, paths in CORPUS['entry'].items():
        body = b'\n'.join((REPO / p).read_bytes() for p in paths)
        entries[case] = {'sha256': hashlib.sha256(body).hexdigest(), 'bytes': len(body),
                         'words': len(body.split()), 'tokens': 'unmeasured; actual tokenizer unavailable'}
        (out / f'{case}-entry.md').write_bytes(body)
    write(out / 'pins.json', {'corpus_sha256': sha(CORPUS_PATH), 'entry_sources': actual,
                             'entries': entries, 'proposal_sha256': CORPUS['proposal_sha256'],
                             'proposal_source_manifest_sha256': CORPUS['proposal_source_manifest_sha256']})


class NativeBlocked(RuntimeError):
    """An actual native qualification gate failed; never proceed to a model."""


def config_summary(value):
    """Allowlist control metadata; never persist arbitrary config or secrets."""
    if not isinstance(value, dict):
        return None
    result = {'keys': sorted(value)}
    for key in ('approval_policy', 'sandbox_mode', 'web_search', 'allow_login_shell'):
        if isinstance(value.get(key), (str, bool)):
            result[key] = value[key]
    result['instruction_overrides_present'] = [k for k in
        ('instructions', 'developer_instructions', 'model_instructions_file', 'compact_prompt')
        if value.get(k)]
    result['features'] = {k: v for k, v in value.get('features', {}).items() if isinstance(v, bool)}
    for key in ('mcp_servers', 'plugins'):
        result[key] = {k: {'enabled': v.get('enabled')} for k, v in value.get(key, {}).items()
                       if isinstance(v, dict)}
    result['notify_present'] = bool(value.get('notify'))
    if type(value.get('project_doc_max_bytes')) is int:
        result['project_doc_max_bytes'] = value['project_doc_max_bytes']
    if isinstance(value.get('permissions'), dict) and 'g2b-fixture' in value['permissions']:
        result['fixture_permissions'] = value['permissions']['g2b-fixture']
    proxy = value.get('features', {}).get('network_proxy')
    if isinstance(proxy, dict):
        result['network_proxy'] = {k: proxy.get(k) for k in ('enabled', 'domains', 'unix_sockets',
            'allow_local_binding', 'allow_upstream_proxy', 'dangerously_allow_all_unix_sockets',
            'dangerously_allow_non_loopback_proxy')}
    return result


def rpc_evidence(message, method):
    """Model streams are exact; config and integration metadata are sanitized."""
    if method == 'config/read' and 'result' in message:
        data = message['result']
        return {'id': message['id'], 'result': {
            'config': config_summary(data.get('config')),
            'layers': [{'name': x.get('name'), 'version': x.get('version'),
                        'disabled': bool(x.get('disabledReason')),
                        'config': config_summary(x.get('config'))} for x in data.get('layers') or []],
            'origin_keys': sorted(k for k in data.get('origins', {}) if k.split('.')[0] in
                ('features', 'permissions', 'mcp_servers', 'plugins', 'approval_policy',
                 'allow_login_shell', 'web_search', 'notify'))}, 'sanitized': True}
    if method in ('skills/list', 'hooks/list', 'mcpServerStatus/list') and 'result' in message:
        result = message['result']
        if method == 'mcpServerStatus/list':
            entries = [{'name': x.get('name'), 'authStatus': x.get('authStatus'),
                        'runtimeStatus': x.get('runtimeStatus'),
                        'tool_names': sorted(x.get('tools', {}))} for x in result.get('data', [])]
        else:
            key = 'skills' if method == 'skills/list' else 'hooks'
            fields = (('path', 'name', 'scope', 'enabled', 'pluginId') if key == 'skills' else
                ('enabled', 'eventName', 'source', 'sourcePath', 'isManaged', 'trustStatus'))
            entries = [{'cwd': x.get('cwd'), 'errors_count': len(x.get('errors', [])),
                        key: [{k: y.get(k) for k in fields} for y in x.get(key, [])]}
                       for x in result.get('data', [])]
        return {'id': message['id'], 'result': {'data': entries,
                'nextCursor': result.get('nextCursor')}, 'sanitized': True}
    if method == 'configRequirements/read' and 'result' in message:
        requirements = message['result'].get('requirements') or {}
        return {'id': message['id'], 'result': {'requirement_keys': sorted(requirements),
                'network': requirements.get('network')}, 'sanitized': True}
    if method and (method.startswith('account/') or 'auth' in method.lower()):
        return {'id': message.get('id'), 'method': method, 'redacted': True}
    return message


class NativeRPC:
    """Small stdio adapter; one owned process, continuously drained pipes."""
    def __init__(self, argv, cwd, env, out, owned_root=None):
        self.out = out
        self.pending = {}
        self.methods = {}
        self.events = queue.Queue()
        self.lock = threading.RLock()
        self.next_id = 1
        self.failure = None
        self.readers = []
        self.records = open(out / 'native-rpc.jsonl', 'w')
        self.stderr = open(out / 'native-app-server.stderr', 'wb')
        self.marker = (owned_root / f'.native-process-{time.monotonic_ns()}.json') if owned_root else None
        try:
            if self.marker:
                write(self.marker, {'pid': None, 'evidence': str(out), 'state': 'launch pending'})
            self.process = subprocess.Popen(argv, cwd=cwd, env=env, stdin=subprocess.PIPE,
                stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
        except BaseException:
            self.records.close()
            self.stderr.close()
            if self.marker:
                self.marker.unlink(missing_ok=True)
            raise
        try:
            if self.marker:
                write(self.marker, {'pid': self.process.pid, 'evidence': str(out)})
        except BaseException as primary:
            try:
                self.close()
            except BaseException as cleanup:
                if hasattr(primary, 'add_note'):
                    primary.add_note('Native launch teardown also failed: ' + str(cleanup))
            raise
        self.readers = [threading.Thread(target=self._read, daemon=True),
                        threading.Thread(target=self._stderr, daemon=True)]
        for reader in self.readers:
            reader.start()

    def record(self, direction, message, method=None):
        with self.lock:
            self.records.write(json.dumps({'direction': direction, 'monotonic': time.monotonic(),
                'message': rpc_evidence(message, method)}) + '\n')
            self.records.flush()

    def send(self, message):
        with self.lock:
            self.record('client', message, message.get('method'))
            self.process.stdin.write((json.dumps(message) + '\n').encode())
            self.process.stdin.flush()

    def _read(self):
        try:
            while True:
                raw = self.process.stdout.readline(4 * 1024 * 1024 + 1)
                if not raw:
                    raise EOFError('native app-server stdout closed')
                if len(raw) > 4 * 1024 * 1024 or not raw.endswith(b'\n'):
                    raise NativeBlocked('native record exceeds bounded JSONL framing')
                message = json.loads(raw)
                if not isinstance(message, dict):
                    raise NativeBlocked('native message is not an object')
                self.deliver(message)
        except BaseException as exc:
            self.failure = exc
            with self.lock:
                for waiter in self.pending.values():
                    waiter.put(exc)
            self.events.put(exc)

    def deliver(self, message):
        with self.lock:
            method = message.get('method') or self.methods.get(message.get('id'))
            self.record('server', message, method)
            if 'method' not in message and 'id' in message:
                waiter = self.pending.get(message['id'])
                if waiter is None:
                    raise NativeBlocked('unmatched native response ID')
                waiter.put(message)
                return
            if 'id' in message:
                # Never approve a network/permission escalation or user action.
                if method in ('item/commandExecution/requestApproval', 'item/fileChange/requestApproval'):
                    response = {'decision': 'cancel'}
                    self.send({'id': message['id'], 'result': response})
                else:
                    self.send({'id': message['id'], 'error': {'code': -32601,
                              'message': 'No external action is authorized by the fixture'}})
            self.events.put(message)

    def _stderr(self):
        used = 0
        while True:
            chunk = self.process.stderr.read(65536)
            if not chunk:
                return
            used += len(chunk)
            if used <= 1024 * 1024:
                self.stderr.write(chunk)
                self.stderr.flush()
            else:
                self.failure = NativeBlocked('native stderr exceeded 1 MiB; remaining bytes drained')

    def request(self, method, params=None, timeout=30):
        with self.lock:
            if self.failure:
                raise self.failure
            ident = self.next_id
            self.next_id += 1
            waiter = queue.Queue()
            self.pending[ident] = waiter
            self.methods[ident] = method
            self.send({'id': ident, 'method': method, 'params': params})
        try:
            response = waiter.get(timeout=timeout)
        except queue.Empty:
            raise TimeoutError(f'native request deadline: {method}') from None
        finally:
            with self.lock:
                self.pending.pop(ident, None)
        if isinstance(response, BaseException):
            raise response
        if 'error' in response:
            raise NativeBlocked(f'native {method} rejected: {response["error"]}')
        return response['result']

    def close(self):
        self.process.stdin.close()
        try:
            self.process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            os.killpg(self.process.pid, signal.SIGTERM)
            try:
                self.process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                os.killpg(self.process.pid, signal.SIGKILL)
                self.process.wait(timeout=5)
        for reader in self.readers:
            reader.join(timeout=5)
        self.process.stdout.close()
        self.process.stderr.close()
        self.records.close()
        self.stderr.close()
        readers_stopped = all(not reader.is_alive() for reader in self.readers)
        write(self.out / 'native-process-cleanup.json', {'pid': self.process.pid,
              'exit_code': self.process.returncode, 'reader_threads_stopped':
              readers_stopped})
        if self.process.returncode is None or not readers_stopped:
            raise NativeBlocked('owned native process teardown remains unconfirmed')
        if self.marker:
            self.marker.unlink()


def native_usage(events):
    totals, phases = {}, {}
    for message in events:
        if message.get('method') != 'thread/tokenUsage/updated':
            continue
        data = message['params']
        totals[data['threadId']] = data['tokenUsage']['total']
        phases[(data['threadId'], data['turnId'])] = data['tokenUsage']['total']
    return {'final_thread_totals': totals,
            'phase_cumulative': [{'thread_id': t, 'turn_id': u, 'total': v}
                                 for (t, u), v in phases.items()],
            'input_output_tokens': sum(v['inputTokens'] + v['outputTokens']
                                      for v in totals.values()) if totals else None,
            'exhaustive_executable_count': 'unqualified', 'hard_provider_cap': False}


DISABLED_NATIVE_FEATURES = ('hooks', 'memories', 'chronicle', 'apps', 'plugins',
    'remote_plugin', 'recommended_plugins', 'skill_mcp_dependency_install',
    'multi_agent', 'multi_agent_v2', 'browser_use', 'browser_use_external',
    'browser_use_full_cdp_access', 'computer_use', 'image_generation', 'goals',
    'in_app_browser', 'in_app_chat', 'in_app_local_automation', 'shell_snapshot',
    'code_mode_host', 'workspace_dependencies', 'skill_search', 'tool_suggest')


def toml_value(value):
    if isinstance(value, dict):
        return '{' + ','.join(json.dumps(k) + '=' + toml_value(v) for k, v in value.items()) + '}'
    if isinstance(value, list):
        return '[' + ','.join(toml_value(v) for v in value) + ']'
    if isinstance(value, (str, bool, int)):
        return json.dumps(value)
    raise TypeError('unsupported session override value')


def native_session_controls(participant):
    # Read only this known user configuration; neither values nor credentials are
    # copied. Session flags disable every configured direct MCP before startup.
    source = Path.home() / '.codex' / 'config.toml'
    user = tomllib.loads(source.read_text()) if source.exists() else {}
    controls = {'features.' + name: False for name in DISABLED_NATIVE_FEATURES}
    controls.update({'features.skip_host_skill_discovery': True,
        'web_search': 'disabled', 'allow_login_shell': False, 'notify': [],
        'approval_policy': 'never', 'analytics.enabled': False, 'project_doc_max_bytes': 0,
        'shell_environment_policy.inherit': 'none',
        'shell_environment_policy.set': {'HOME': str(participant),
            'TMPDIR': str(participant / '.tmp'),
            'PATH': str(participant / 'bin') + ':/opt/homebrew/bin:/usr/bin:/bin',
            'PYTHONDONTWRITEBYTECODE': '1'}})
    for name in user.get('mcp_servers', {}):
        if '.' in name:
            raise NativeBlocked('MCP name requires unsupported dotted CLI override escaping')
        controls['mcp_servers.' + name + '.enabled'] = False
    for name in user.get('plugins', {}):
        if '.' in name:
            raise NativeBlocked('plugin name requires unsupported dotted CLI override escaping')
        controls['plugins.' + name + '.enabled'] = False
    # These standard discovery roots were observed in native skills/list.
    # Enumerate paths only; no private skill body is opened or injected.
    discovered = []
    for home_name in ('.codex', '.agents', '.claude'):
        directory = Path.home() / home_name / 'skills'
        for pattern in ('*/SKILL.md', '.system/*/SKILL.md'):
            discovered.extend(directory.glob(pattern))
    controls['skills.config'] = [{'path': str(p), 'enabled': False}
                                 for p in sorted(set(discovered))]
    if config_summary(user)['instruction_overrides_present']:
        raise NativeBlocked('user configuration has an unapproved instruction override')
    return controls


def permission_controls(participant, bridge, writable, allow_bridge=True):
    participant, bridge = participant.resolve(), bridge.resolve()
    network = {'enabled': allow_bridge, 'allow_local_binding': False, 'allow_upstream_proxy': False,
        'dangerously_allow_all_unix_sockets': False,
        'dangerously_allow_non_loopback_proxy': False,
        'enable_socks5_udp': False, 'domains': {},
        'unix_sockets': {str(bridge): 'allow'} if allow_bridge else {}}
    filesystem = {':root': 'deny', ':minimal': 'read', ':tmpdir': 'deny', ':slash_tmp': 'deny',
        str(participant): 'write' if writable else 'read',
        str(Path(sys.base_prefix)): 'read', str(Path(sys.base_prefix).resolve()): 'read'}
    return {'features.network_proxy': network,
        'permissions.g2b-fixture.filesystem': filesystem,
        'permissions.g2b-fixture.network': network}


def verify_sources(thread, approved):
    paths = thread.get('instructionSources')
    if paths is None:
        raise NativeBlocked('thread/start omitted loaded instruction-source paths')
    unexpected = [p for p in paths if p not in approved]
    if unexpected:
        raise NativeBlocked('unapproved instruction source paths loaded: ' + json.dumps(unexpected))
    if not thread.get('thread', {}).get('ephemeral') or thread['thread'].get('path'):
        raise NativeBlocked('native thread did not establish ephemeral, non-file-backed state')
    if (thread.get('activePermissionProfile') or {}).get('id') != 'g2b-fixture':
        raise NativeBlocked('native thread did not select the explicit fixture permissions')


def verify_mcp_disabled(config, inventory):
    if inventory.get('nextCursor'):
        raise NativeBlocked('MCP inventory incomplete; remaining metadata must be inspected')
    for server in inventory.get('data', []):
        configured = config.get('mcp_servers', {}).get(server['name'], {})
        if (configured.get('enabled') is not False or server.get('tools') or
                server.get('resources') or server.get('resourceTemplates') or
                server.get('runtimeStatus') not in (None, 'disabled', 'notStarted')):
            raise NativeBlocked('unrelated MCP server is not disabled: ' + server['name'])


def verify_discovery_disabled(config):
    value = config.get('project_doc_max_bytes')
    if type(value) is not int or value != 0:
        raise NativeBlocked('automatic project instruction discovery was not disabled')


@contextlib.contextmanager
def native_participant(root, target, out, bridge, writable, fixture_instructions,
                       allow_known_global=False, allow_bridge=True):
    """Fresh ephemeral participant, yielded only after sources/tools qualify."""
    target.mkdir(parents=True, exist_ok=True)
    (target / '.tmp').mkdir(exist_ok=True)
    controls = native_session_controls(target)
    permitted_sources = set()
    if allow_known_global:
        global_path = Path.home() / '.codex' / 'AGENTS.md'
        # Opaque hashing only; never open this source into the worker's context.
        if global_path.stat().st_size != 6805 or sha(global_path) != GLOBAL_PROBE_SHA:
            raise NativeBlocked('integrator-pinned global instruction source drifted')
        permitted_sources.add(str(global_path))
        write(out / 'context-deviation.json', {'source': str(global_path), 'bytes': 6805,
            'sha256': GLOBAL_PROBE_SHA, 'strict_context_gate': 'failed/unqualified',
            'scope': 'bd379213 qualified correctness probe only',
            'possible_contamination': 'identity-neutral Basecamp/Provenance policy',
            'cold_benchmark_acceptance': False})
    controls.update(permission_controls(target, bridge, writable, allow_bridge))
    flags = [x for key, value in controls.items() for x in ('-c', key + '=' + toml_value(value))]
    native_env = {'HOME': str(Path.home()), 'PATH': '/opt/homebrew/bin:/usr/bin:/bin',
                  'TMPDIR': str(target / '.tmp')}
    run([NATIVE, '--version'], out / 'native-version.log', native_env, target, 30)
    if (out / 'native-version.log').read_text().strip() != 'codex-cli 0.151.0':
        raise NativeBlocked('native version differs from the inspected dispatch schema')
    run([NATIVE, 'app-server', 'generate-json-schema', '--experimental', '--out', out / 'native-schema'],
        out / 'native-schema-generation.log', native_env, target, 30)
    write(out / 'native-interface.json', {'binary_sha256': sha(NATIVE),
        'schema_sha256': {str(p.relative_to(out / 'native-schema')): sha(p)
                          for p in sorted((out / 'native-schema').rglob('*.json'))}})
    write(out / 'native-session-controls.json', controls)
    run([NATIVE, *flags, 'features', 'list'], out / 'native-configured-features.log', native_env, target, 30)
    enabled = {}
    for line in (out / 'native-configured-features.log').read_text().splitlines():
        words = line.split()
        if words and words[-1] in ('true', 'false'):
            enabled[words[0]] = words[-1] == 'true'
    if any(enabled.get(key) is not False for key in DISABLED_NATIVE_FEATURES):
        raise NativeBlocked('launch-time feature controls were not confirmed disabled')
    if enabled.get('skip_host_skill_discovery') is not True:
        raise NativeBlocked('host skill discovery is not disabled')
    argv = [str(NATIVE), 'app-server', '--stdio', '--strict-config', *flags]
    write(out / 'native-launch.json', {'argv': argv, 'cwd': str(target),
          'binary_sha256': sha(NATIVE), 'kind': 'model-free context qualification'})
    rpc = NativeRPC(argv, target, native_env, out, owned_root=root)
    thread_id = None
    primary_error = None
    try:
        rpc.request('initialize', {'clientInfo': {'name': 'loom_fixture', 'version': 'g2b'},
                    'capabilities': {'experimentalApi': True}})
        rpc.send({'method': 'initialized'})
        config = rpc.request('config/read', {'cwd': str(target), 'includeLayers': True})
        verify_discovery_disabled(config['config'])
        write(out / 'native-discovery-control.json', {'project_doc_max_bytes':
              config['config']['project_doc_max_bytes'], 'scope': 'session only',
              'global_source_absence': 'must still be proved by instructionSources'})
        if config_summary(config['config'])['instruction_overrides_present']:
            raise NativeBlocked('effective configuration contains unapproved instruction overrides')
        for key in DISABLED_NATIVE_FEATURES:
            value = config['config'].get('features', {}).get(key)
            if isinstance(value, dict):
                value = value.get('enabled')
            if value is not False:
                raise NativeBlocked('effective native feature was not disabled: ' + key)
        rpc.request('configRequirements/read')
        profiles = rpc.request('permissionProfile/list', {'cwd': str(target)})
        if not any(x.get('id') == 'g2b-fixture' and x.get('allowed') for x in profiles['data']):
            raise NativeBlocked('fixture permission profile is not allowed')
        mcp = rpc.request('mcpServerStatus/list', {})
        verify_mcp_disabled(config['config'], mcp)
        hooks = rpc.request('hooks/list', {'cwds': [str(target)]})
        # Per-hook enabled metadata is not execution when the global hooks
        # feature is off. Both the launch flags and effective feature were checked.
        if any(entry.get('errors') for entry in hooks['data']):
            raise NativeBlocked('native hook inventory contains errors')
        skills = rpc.request('skills/list', {'cwds': [str(target)]})
        if any(x.get('enabled') for entry in skills['data'] for x in entry.get('skills', [])):
            raise NativeBlocked('native metadata still reports ambient enabled skills')
        schema = json.loads((out / 'native-schema/v2/ThreadStartParams.json').read_text())
        if 'developerInstructions' not in schema.get('properties', {}):
            raise NativeBlocked('installed schema omits developerInstructions')
        (out / 'injected-fixture-AGENTS.md').write_bytes(fixture_instructions)
        write(out / 'fixture-instruction-injection.json', {
            'sha256': hashlib.sha256(fixture_instructions).hexdigest(),
            'bytes': len(fixture_instructions), 'words': len(fixture_instructions.split()),
            'tokens': 'unmeasured', 'injections': 1 if fixture_instructions else 0,
            'transport': 'developerInstructions', 'automatic_discovery': False})
        thread = rpc.request('thread/start', {'cwd': str(target),
            'runtimeWorkspaceRoots': [str(target.resolve())], 'ephemeral': True,
            'permissions': 'g2b-fixture', 'approvalPolicy': 'never',
            'developerInstructions': fixture_instructions.decode('utf-8') if fixture_instructions else None})
        thread_id = thread['thread']['id']
        write(out / 'native-thread-context.json', thread)
        verify_sources(thread, permitted_sources)
        if allow_known_global and (global_path.stat().st_size != 6805 or sha(global_path) != GLOBAL_PROBE_SHA):
            raise NativeBlocked('global instruction source drifted during native initialization')
        verify_mcp_disabled(config['config'], rpc.request('mcpServerStatus/list', {'threadId': thread_id}))
        # The caller must still prove socket/filesystem isolation before models.
        write(out / 'native-context-qualified.json', {'thread_id': thread_id,
              'model': thread['model'], 'instruction_sources': thread['instructionSources'],
              'hidden_builtin_context': 'opaque; control measurement unqualified',
              'socket_canaries': 'not-run', 'model_turns': 0})
        yield rpc, thread
    except BaseException as exc:
        primary_error = exc
        raise
    finally:
        if thread_id is not None:
            try:
                rpc.request('thread/unsubscribe', {'threadId': thread_id}, timeout=5)
            except Exception as exc:
                write(out / 'native-unsubscribe-error.json', {'error': str(exc)})
        try:
            rpc.close()
        except BaseException as exc:
            if primary_error is None:
                raise
            detail = 'Native teardown also failed: ' + str(exc)
            print(detail, file=sys.stderr, flush=True)
            if hasattr(primary_error, 'add_note'):
                primary_error.add_note(detail)



@contextlib.contextmanager
def fixture_owner(root, env, out, case):
    package, selector = SELECTORS[case]
    binary = out / (package + '.test')
    if not binary.exists():
        run(['go', 'test', '-c', '-o', binary, './internal/' + package], out / (case + '-build-helper.log'))
    owner_env = dict(env, LOOM_DX_SERVE='1', LOOM_DX_EVIDENCE=str(out))
    owner, log, primary_error = None, None, None
    errors = []
    marker = root / f'.native-process-owner-{time.monotonic_ns()}.json'

    def attempt(stage, action):
        try:
            action()
        except BaseException as exc:
            errors.append({'stage': stage, 'type': type(exc).__name__, 'error': str(exc)})

    try:
        write(marker, {'pid': None, 'kind': 'fixture owner', 'case': case, 'state': 'launch pending'})
        log = open(out / (case + '-owner.log'), 'wb')
        owner = subprocess.Popen([binary, '-test.run=^' + selector + '$', '-test.v', '-test.timeout=15m'],
            cwd=REPO / 'internal' / package, env=owner_env, stdout=log, stderr=subprocess.STDOUT,
            start_new_session=True)
        write(marker, {'pid': owner.pid, 'kind': 'fixture owner', 'case': case})
        ready = out / case / ('seed-ready.json' if case == 'C' else 'ready.json')
        deadline = time.monotonic() + 45
        while not ready.exists():
            if owner.poll() is not None or time.monotonic() > deadline:
                raise NativeBlocked('fixture owner was not ready: ' + case)
            time.sleep(.025)
        yield owner
    except BaseException as exc:
        primary_error = exc
        raise
    finally:
        if owner is not None:
            # A diagnostic/stop-file failure must never skip process shutdown.
            attempt('stop_file', lambda: (root / (case + '.stop')).touch())
            for sig in (None, signal.SIGTERM, signal.SIGKILL):
                if owner.returncode is not None:
                    break
                if sig is not None:
                    attempt('signal_' + str(sig), lambda: os.killpg(owner.pid, sig))
                try:
                    owner.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    if sig == signal.SIGKILL:
                        errors.append({'stage': 'wait', 'error': 'fixture owner shutdown unconfirmed'})
                except BaseException as exc:
                    errors.append({'stage': 'wait', 'type': type(exc).__name__, 'error': str(exc)})
            if owner.returncode not in (None, 0):
                errors.append({'stage': 'exit', 'error': 'fixture owner exit ' + str(owner.returncode)})
        if log is not None:
            attempt('close_log', log.close)
        stopped = owner is None or owner.returncode is not None
        if stopped:
            attempt('remove_marker', lambda: marker.unlink(missing_ok=True))
        receipt = {'pid': owner.pid if owner else None,
            'exit_code': owner.returncode if owner else None, 'stopped': stopped,
            'primary_error': str(primary_error) if primary_error else None, 'cleanup_errors': errors}
        attempt('write_receipt', lambda: write(out / (case + '-owner-cleanup.json'), receipt))
        if errors:
            detail = 'Fixture owner cleanup: ' + json.dumps(errors)
            print(detail, file=sys.stderr)
            if primary_error is not None:
                primary_error.add_note(detail)
            else:
                raise NativeBlocked(detail)


@contextlib.contextmanager
def listening(server_class, address, handler):
    server = server_class(address, handler)
    server.calls = 0
    worker = threading.Thread(target=lambda: server.serve_forever(poll_interval=.025), daemon=True)
    worker.start()
    try:
        yield server
    finally:
        server.shutdown()
        server.server_close()
        worker.join(timeout=5)
        if worker.is_alive():
            raise NativeBlocked('owned canary listener did not stop')
        if isinstance(address, str):
            Path(address).unlink(missing_ok=True)


CANARY_PROGRAM = r"""
import errno, json, os, socket, sys
from urllib.parse import urlsplit
p = json.loads(sys.argv[1])
checks = {}
def connect(name, family, address, expect):
    s = socket.socket(family); s.settimeout(2)
    try:
        s.connect(address)
        checks[name] = {'connected': True, 'pass': expect}
    except OSError as e:
        checks[name] = {'connected': False, 'errno': e.errno,
                        'pass': not expect and e.errno in (errno.EPERM, errno.EACCES)}
    finally:
        s.close()
connect('bridge_socket', socket.AF_UNIX, p['bridge'], p['allow_bridge'])
connect('sibling_socket', socket.AF_UNIX, p['sibling'], False)
connect('sibling_alias', socket.AF_UNIX, p['sibling_alias'], False)
connect('postgres_socket', socket.AF_UNIX, p['postgres'], False)
connect('raw_tcp', socket.AF_INET, ('127.0.0.1', p['port']), False)
proxy = os.environ.get('HTTP_PROXY') or os.environ.get('http_proxy')
if p['allow_bridge']:
    try:
        u = urlsplit(proxy or '')
        if u.hostname not in ('127.0.0.1', 'localhost') or not u.port:
            raise ValueError('missing owned loopback HTTP proxy')
        with socket.create_connection((u.hostname, u.port), timeout=2) as s:
            s.sendall(('GET http://127.0.0.1:%d/ HTTP/1.1\r\nHost: 127.0.0.1:%d\r\nConnection: close\r\n\r\n' % (p['port'],p['port'])).encode())
            status = s.recv(256).split(b'\r\n')[0].decode()
        checks['proxied_tcp'] = {'status':status,'pass':len(status.split())>1 and status.split()[1]=='403'}
    except Exception as e:
        checks['proxied_tcp'] = {'pass':False,'error':str(e)}
else:
    checks['proxied_tcp'] = {'pass':not proxy,'network_disabled':True}
for key in list(os.environ):
    if key.lower().endswith('_proxy'): os.environ.pop(key)
connect('raw_tcp_proxy_bypass', socket.AF_INET, ('127.0.0.1',p['port']), False)
for name,path in [('evaluator_read',p['secret']),('evidence_read',p['evidence_secret'])]:
    try:
        with open(path) as f: f.read()
        checks[name]={'pass':False,'readable':True}
    except OSError as e:
        checks[name]={'pass':e.errno in (errno.EPERM,errno.EACCES),'errno':e.errno}
try:
    with open(p['readable']) as f: value=f.read()
    checks['fixture_read']={'pass':value==p['nonce']}
except OSError as e:
    checks['fixture_read']={'pass':False,'errno':e.errno}
for name,path,expect in [('fixture_write',p['writable_file'],p['writable']),('outside_write',p['outside_write'],False)]:
    try:
        with open(path,'w') as f: f.write('owned canary')
        checks[name]={'pass':expect,'wrote':True}
    except OSError as e:
        checks[name]={'pass':not expect and e.errno in (errno.EPERM,errno.EACCES),'errno':e.errno}
print(json.dumps(checks))
sys.exit(0 if all(x['pass'] for x in checks.values()) else 2)
"""


def qualify_native_context(root, env, out, allow_known_global=False):
    """Real model-free context and deny-all-except-owned-socket canaries."""
    target = root / 'participants' / 'qualification'
    target.mkdir(parents=True)
    fixture_instructions = CORPUS['cases']['A']['files']['AGENTS.md'].encode()
    (target / 'AGENTS.md').write_bytes(fixture_instructions)
    bridge_path = root / 'qualification-bridge.sock'
    sibling = root / 'denied-sibling.sock'
    nonce = os.urandom(16).hex()
    secret = root / 'evaluator-secret.txt'
    secret.write_text(nonce)
    evidence_secret = out / 'evaluator-secret.txt'
    evidence_secret.write_text(nonce)
    (target / 'readable.txt').write_text(nonce)
    expected = ['project', 'status', str(root / 'participants/B/field-notebook'), '--node', 'main']

    class Bridge(socketserver.StreamRequestHandler):
        def handle(self):
            self.connection.settimeout(3)
            raw = self.rfile.readline(65537)
            if not raw: return
            self.server.calls += 1
            try:
                argv = json.loads(raw)
                if argv != expected:
                    raise ValueError('not the fixed model-free public CLI probe')
                p = subprocess.run([str(out / 'loom'), '--config', str(root / 'B.conf'),
                    '--socket', str(root / 'B.sock'), '--json', *argv], cwd=target,
                    env={'HOME': str(target), 'PATH':'/usr/bin:/bin'}, capture_output=True,
                    text=True, timeout=15)
                result = {'argv': argv, 'stdout': p.stdout, 'stderr': p.stderr, 'exit_code': p.returncode}
            except Exception as exc:
                result = {'stdout':'','stderr':str(exc),'exit_code':126}
            with open(out / 'qualification-public-cli.jsonl','a') as log:
                log.write(json.dumps(result)+'\n')
            self.wfile.write((json.dumps(result)+'\n').encode())

    class Trap(socketserver.BaseRequestHandler):
        def handle(self):
            self.server.calls += 1
            self.request.settimeout(.1)
            try:
                self.request.recv(2048)
                self.request.sendall(b'HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n')
            except OSError:
                pass

    with fixture_owner(root, env, out, 'B'), \
         listening(socketserver.UnixStreamServer, str(bridge_path), Bridge) as bridge, \
         listening(socketserver.UnixStreamServer, str(sibling), Trap) as denied, \
         listening(socketserver.TCPServer, ('127.0.0.1',0), Trap) as trap:
        # Listener constructors have bound/listened; PG identity was checked by cluster().
        assert (root / 'socket/.s.PGSQL.5432').exists()
        for label, writable, allow_bridge in [('readonly',False,True),('workspace',True,True),('no-bridge',True,False)]:
            evidence = out / label
            evidence.mkdir()
            with native_participant(root, target, evidence, bridge_path, writable,
                                    fixture_instructions, allow_known_global, allow_bridge) as (rpc, thread):
                parameters = {'bridge': str(bridge_path.resolve()), 'sibling':str(sibling.resolve()),
                    'sibling_alias':str(sibling),'postgres':str((root/'socket/.s.PGSQL.5432').resolve()),
                    'port':trap.server_address[1], 'secret':str(secret), 'evidence_secret':str(evidence_secret),
                    'readable':str(target/'readable.txt'), 'nonce':nonce, 'writable':writable,
                    'writable_file':str(target/'canary-write.txt'), 'outside_write':str(root/'outside-write.txt'),
                    'allow_bridge':allow_bridge}
                result = rpc.request('command/exec', {'command':[str(Path(sys.executable).resolve()), '-B',
                    '-c', CANARY_PROGRAM, json.dumps(parameters)], 'cwd':str(target),
                    'permissionProfile':'g2b-fixture','timeoutMs':15000,'outputBytesCap':65536},timeout=20)
                write(evidence/'canary-result.json',result)
                try: checks=json.loads(result['stdout'])
                except ValueError: checks={}
                if result['exitCode'] or not checks or not all(v['pass'] for v in checks.values()):
                    raise NativeBlocked('native filesystem/socket/proxy canaries failed: '+label)
                if denied.calls or trap.calls:
                    raise NativeBlocked('a denied owned listener received a connection')
                if allow_bridge:
                    api_log = out / 'B/api.jsonl'
                    api_before = len(api_log.read_text().splitlines()) if api_log.exists() else 0
                    script = ('import socket,json,sys;s=socket.socket(socket.AF_UNIX);s.settimeout(10);'
                              's.connect(sys.argv[1]);s.sendall((sys.argv[2]+"\\n").encode());'
                              'print(s.makefile().readline())')
                    public = rpc.request('command/exec', {'command':[str(Path(sys.executable).resolve()),
                        '-B','-c',script,str(bridge_path.resolve()),json.dumps(expected)], 'cwd':str(target),
                        'permissionProfile':'g2b-fixture','timeoutMs':15000,'outputBytesCap':65536},timeout=20)
                    write(evidence/'public-cli-canary.json',public)
                    if public['exitCode'] or not bridge.calls:
                        raise NativeBlocked('public CLI bridge did not complete')
                    # Parse the real bridge result; service/CLI failures are retained.
                    response=json.loads(public['stdout'])
                    api_after = len(api_log.read_text().splitlines()) if api_log.exists() else 0
                    if response.get('exit_code') == 126 or api_after <= api_before:
                        raise NativeBlocked('public CLI probe did not reach the real fixture HTTP owner')
            (target/'canary-write.txt').unlink(missing_ok=True)
    write(out/'native-isolation-qualified.json',{'profiles':['readonly','workspace','no-bridge'],
        'denied_unix_connections':denied.calls,'denied_tcp_connections':trap.calls,
        'public_cli_bridge_calls':bridge.calls,'model_turns':0,
        'strict_context_gate':'failed/unqualified' if allow_known_global else 'passed'})


def cleanup_cluster(root, root_link, out, pg, start_attempted, primary_error):
    diagnostics, errors = [], []

    def attempt(stage, action, failures=errors):
        try:
            action()
            return True
        except BaseException as exc:
            failures.append({'stage': stage, 'type': type(exc).__name__, 'error': str(exc)})
            return False

    stopped = False
    if start_attempted:
        # Inventory is evidence, never a prerequisite for shutting down our DB.
        attempt('inventory', lambda: run(
            [pg / 'psql', '-h', root / 'socket', '-U', 'postgres', '-d', 'postgres', '-Atc',
             'select datname from pg_database order by datname;'],
            out / 'db-cleanup-inventory.log'), diagnostics)
        # A failed/timed-out start may still have created a running postmaster.
        stopped = attempt('postgres_stop', lambda: run(
            [pg / 'pg_ctl', '-D', root / 'data', '-m', 'fast', '-w', 'stop'], out / 'pg-stop.log'))
    log_saved = True
    if (root / 'server.log').exists():
        log_saved = attempt('save_postgres_log', lambda: shutil.copy2(root / 'server.log', out / 'postgres.log'))
    native_residue = sorted(str(p) for p in root.glob('.native-process-*.json'))
    if native_residue:
        errors.append({'stage': 'native_teardown', 'error': 'unconfirmed native process cleanup',
                       'residue_markers': native_residue})
    if (stopped or not start_attempted) and log_saved and not native_residue:
        attempt('remove_root', lambda: shutil.rmtree(root))
    # Keep the runtime rooted while any owned data/process state is unresolved.
    if not root.exists():
        attempt('remove_runtime_root', lambda: root_link.unlink(missing_ok=True))
    receipt = {'root': str(root), 'runtime_root': str(root_link),
               'root_removed': not root.exists(),
               'runtime_root_removed': not (root_link.exists() or root_link.is_symlink()),
               'postgres_start_attempted': start_attempted, 'postgres_stopped': stopped,
               'primary_error': None if primary_error is None else
                   {'type': type(primary_error).__name__, 'error': str(primary_error)},
               'diagnostic_errors': diagnostics, 'cleanup_errors': errors}
    attempt('write_cleanup_receipt', lambda: write(out / 'cleanup.json', receipt))
    return receipt


@contextlib.contextmanager
def cluster(out, pg):
    root = Path(tempfile.mkdtemp(prefix='loom-dx-', dir='/tmp'))
    start_attempted = False
    primary_error = None
    root_link = out / 'pg-runtime'
    try:
        (root / 'socket').mkdir()
        (root / 'temp').mkdir()
        run(['/nix/var/nix/profiles/default/bin/nix-store', '--add-root', root_link,
             '--indirect', '--realise', pg.parent], out / 'pg-root.log')
        run([pg / 'initdb', '-D', root / 'data', '-U', 'postgres', '--auth=trust',
             '--no-locale', '-E', 'UTF8'], out / 'initdb.log')
        start_attempted = True
        run([pg / 'pg_ctl', '-D', root / 'data', '-l', root / 'server.log', '-o',
             f"-F -k {root / 'socket'} -c listen_addresses=''", '-w', 'start'], out / 'pg-start.log')
        run([pg / 'psql', '-h', root / 'socket', '-U', 'postgres', '-d', 'postgres', '-Atc',
             "select version(); show listen_addresses; select name,default_version from pg_available_extensions where name='vector';"], out / 'pg-identity.log')
        env = clean_env()
        dsn = f'postgresql://postgres@/postgres?host={root}/socket&sslmode=disable'
        env.update(TMPDIR=str(root / 'temp'), LOOM_DX_ROOT=str(root), LOOM_DX_EVIDENCE=str(out), LOOM_DX_CLI=str(out / 'loom'),
                   LOOM_DX_HTTP_TEST=str(out / 'httpapi.test'), LOOM_TEST_DB_URL=dsn,
                   LOOM_PROVENANCE_TEST_DB_URL=dsn, LOOM_DX_PG_BIN=str(pg))
        yield root, env
    except BaseException as exc:
        primary_error = exc
        raise
    finally:
        cleanup = cleanup_cluster(root, root_link, out, pg, start_attempted, primary_error)
        if cleanup['diagnostic_errors'] or cleanup['cleanup_errors']:
            detail = 'Owned cluster cleanup: ' + json.dumps(cleanup)
            print(detail, file=sys.stderr, flush=True)
            if primary_error is not None:
                if hasattr(primary_error, 'add_note'):
                    primary_error.add_note(detail)
            elif cleanup['cleanup_errors']:
                raise RuntimeError(detail)


def seed_a(root, out):
    target = root / 'participants' / 'A'
    target.mkdir(parents=True)
    for name, body in CORPUS['cases']['A']['files'].items():
        (target / name).write_text(body)
    run(['python3', '-m', 'unittest', '-v'], out / 'A-fixture-tests.log', cwd=target)
    write(out / 'A-fixture.json', {'files': {p.name: sha(p) for p in target.iterdir() if p.is_file()},
                                 'plain_fixture': True, 'model_trial': 'not-run'})


def prerequisites(root, env, out, cases, gates):
    counts = {}
    for case in cases:
        if case == 'A':
            seed_a(root, out)
            continue
        package, test = SELECTORS[case]
        log = out / f'{case}-fixture.jsonl'
        run(['go', 'test', '-count=1', '-json', '-run', '^' + test + '$', './internal/' + package], log, env)
        counts[case] = count_tests(log)
        if counts[case] != {'pass': 1, 'fail': 0, 'skip': 0}:
            raise RuntimeError(f'required fixture must pass without skips: {case}: {counts[case]}')
    if gates:
        owners = ['./internal/httpapi', './internal/knowledge', './internal/provenance']
        # Broad owner gates use their normal clean environment. Dedicated
        # fixture PG URLs are never injected into unrelated conditional suites.
        focused_env = clean_env()
        if sys.platform == 'darwin':
            focused_env['TMPDIR'] = subprocess.check_output(['/usr/bin/getconf', 'DARWIN_USER_TEMP_DIR']).decode().strip()
        run(['go', 'test', '-count=1', '-json'] + owners, out / 'focused.jsonl', focused_env)
        counts['focused'] = count_tests(out / 'focused.jsonl')
        # Fresh case roots for repeated fixtures. Owned DBs have already dropped.
        for case in 'BCD':
            shutil.rmtree(root / 'participants' / case, ignore_errors=True)
        for control in ('C.stop', 'C.query', 'C.barrier.done'):
            (root / control).unlink(missing_ok=True)
        run(['go', 'test', '-race', '-count=1', '-json', '-run',
             '^TestProjectDX(Projects|Notes|Provenance)Fixture$'] + owners, out / 'race.jsonl', env)
        counts['race'] = count_tests(out / 'race.jsonl')
        if counts['race'] != {'pass': 3, 'fail': 0, 'skip': 0}:
            raise RuntimeError('required race fixture selectors did not pass without skips')
        run(['go', 'vet'] + owners, out / 'vet.log', env)
    write(out / 'test-counts.json', counts)
    return counts


def native_preflight(root, env, out):
    # This entry is deliberately fail-closed. exec JSON's observed events must
    # establish context/tools, persistence, transport and command telemetry;
    # --help and a participant's statements cannot establish those properties.
    dirty = subprocess.check_output(['git', 'status', '--porcelain', '--untracked-files=no'], cwd=REPO).decode()
    committed = subprocess.check_output(['git', 'show', 'HEAD:tests/acceptance/project_dx/cold_cases.json'], cwd=REPO)
    if dirty or hashlib.sha256(committed).hexdigest() != sha(CORPUS_PATH):
        raise RuntimeError('checkpoint the fixture/corpus before native execution')
    for item in (root / 'participants' / 'B').glob('*'):
        if item.is_dir():
            shutil.rmtree(item)
        else:
            item.unlink()
    serve_env = dict(env, LOOM_DX_SERVE='1')
    with open(out / 'preflight-owner.log', 'wb') as log:
        owner = subprocess.Popen([out / 'httpapi.test', '-test.run=^TestProjectDXProjectsFixture$',
                                  '-test.v', '-test.timeout=3m'], cwd=REPO / 'internal/httpapi',
                                 env=serve_env, stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
        bridge = None
        try:
            deadline = time.monotonic() + 45
            while not (root / 'B.sock').exists():
                if owner.poll() is not None or time.monotonic() > deadline:
                    raise RuntimeError('preflight public fixture owner not ready')
                time.sleep(.025)
            participant = root / 'participants' / 'preflight'
            participant.mkdir()
            bin_dir = participant / 'bin'
            bin_dir.mkdir()
            shim = bin_dir / 'loom'
            # Execute the guarded CLI in the operator process. The participant
            # shim cannot write evaluator logs under a read-only native sandbox.
            bridge_path = root / 'preflight-bridge.sock'
            class Bridge(socketserver.StreamRequestHandler):
                def handle(self):
                    start = time.monotonic()
                    raw = self.rfile.readline(65537)
                    try:
                        argv = json.loads(raw)
                    except ValueError:
                        argv = None
                    expected = ['project', 'status', '/missing-dx-preflight', '--node', 'main']
                    allowed = argv == expected and self.server.calls == 0
                    self.server.calls += 1
                    if allowed:
                        result = subprocess.run([str(out / 'loom'), '--config', str(root / 'B.conf'),
                            '--socket', str(root / 'B.sock'), '--json'] + argv,
                            capture_output=True, text=True, timeout=30,
                            env={'HOME': str(participant), 'PATH': '/usr/bin:/bin'})
                        record = {'stdout': result.stdout, 'stderr': result.stderr, 'exit_code': result.returncode}
                    else:
                        record = {'stdout': '', 'stderr': 'Forbidden preflight command', 'exit_code': 126}
                    record.update(argv=argv, denied=not allowed, elapsed_seconds=time.monotonic()-start)
                    with open(out / 'preflight-shim.jsonl', 'a') as bridge_log:
                        bridge_log.write(json.dumps(record) + '\n')
                    self.wfile.write((json.dumps(record) + '\n').encode())
            bridge = socketserver.UnixStreamServer(str(bridge_path), Bridge)
            bridge.calls = 0
            bridge_thread = threading.Thread(target=bridge.serve_forever, daemon=True)
            bridge_thread.start()
            shim.write_text('#!/usr/bin/env python3\nimport json,socket,sys\n'
                + 's=socket.socket(socket.AF_UNIX);s.connect(' + repr(str(bridge_path)) + ')\n'
                + 's.sendall((json.dumps(sys.argv[1:])+"\\n").encode());r=json.loads(s.makefile().readline())\n'
                + 'sys.stdout.write(r["stdout"]);sys.stderr.write(r["stderr"]);sys.exit(r["exit_code"])\n')
            shim.chmod(0o700)
            prompt = ('This is a disposable native harness transport preflight. Run exactly this shell command once: '
                      + str(shim) + ' project status /missing-dx-preflight --node main. '
                      'A missing-project error is expected. Then finish. Do not read other files, use other tools, '
                      'or contact any service besides this fixture through that command.\n')
            (out / 'preflight-prompt.txt').write_text(prompt)
            native_env = {'HOME': str(Path.home()), 'PATH': str(bin_dir) + ':/opt/homebrew/bin:/usr/bin:/bin', 'TMPDIR': '/tmp'}
            args = [str(NATIVE), 'exec', '--ephemeral', '--ignore-user-config', '--skip-git-repo-check',
                    '--sandbox', 'read-only', '--cd', str(participant), '--json',
                    '--output-last-message', str(out / 'preflight-final.txt'), '-']
            run([NATIVE, '--version'], out / 'native-version.log', native_env)
            run([NATIVE, 'exec', '--help'], out / 'native-exec-help.log', native_env)
            start = time.monotonic()
            with open(out / 'preflight-native.jsonl', 'wb') as stdout, open(out / 'preflight-native.stderr', 'wb') as stderr:
                proc = subprocess.Popen(args, stdin=subprocess.PIPE, stdout=stdout, stderr=stderr,
                                        env=native_env, cwd=participant, start_new_session=True)
                try:
                    proc.communicate(prompt.encode(), timeout=90)
                except BaseException:
                    os.killpg(proc.pid, signal.SIGTERM)
                    try:
                        proc.wait(timeout=5)
                    except subprocess.TimeoutExpired:
                        os.killpg(proc.pid, signal.SIGKILL)
                        proc.wait()
                    raise
            events = []
            for line in (out / 'preflight-native.jsonl').read_text().splitlines():
                try:
                    events.append(json.loads(line))
                except ValueError:
                    pass
            # Record actual available fields, not inferred self-reported loads.
            write(out / 'native-preflight.json', {
                'binary': str(NATIVE), 'binary_sha256': sha(NATIVE), 'argv': args,
                'exit_code': proc.returncode, 'elapsed_seconds': time.monotonic()-start,
                'event_types': sorted({e.get('type', '') for e in events}),
                'raw_events': 'preflight-native.jsonl', 'raw_stderr': 'preflight-native.stderr',
                'observations': native_observations(events, bridge.calls),
                'qualified_cold': False, 'instruction_inventory': 'not established by exec event inventory',
                'sidebar_persistence': 'not established by ephemeral flag or thread.started alone',
                'B_two_phase_same_ephemeral_participant': 'exec is one-shot; resume/fork forbidden by frozen contract',
                'model_tool_caps': 'not independently enforceable for tool surfaces absent from native event inventory',
                'model_token_compliance': 'unmeasured unless complete provider usage is present in raw events',
                'trials': {c: 'not-run; native harness qualification blocked' for c in 'ABCD'}})
        finally:
            if bridge is not None:
                bridge.shutdown()
                bridge.server_close()
                bridge_thread.join(timeout=2)
                bridge_path.unlink(missing_ok=True)
            (root / 'B.stop').touch()
            try:
                owner.wait(timeout=10)
            except subprocess.TimeoutExpired:
                os.killpg(owner.pid, signal.SIGTERM)
                owner.wait(timeout=5)


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument('--fixture-only', action='store_true')
    ap.add_argument('--case', choices=list('ABCD'))
    ap.add_argument('--gates', action='store_true')
    ap.add_argument('--native-preflight', action='store_true')
    ap.add_argument('--native-qualify', action='store_true')
    ap.add_argument('--context-deviated', action='store_true')
    ap.add_argument('--pg-bin', default=PG_DEFAULT)
    args = ap.parse_args()
    if args.native_preflight and (args.case or args.gates):
        ap.error('native preflight requires all four fixture prerequisites without repeated gates')
    if args.native_qualify and (args.case or args.gates or args.native_preflight):
        ap.error('native qualification requires all four fixtures and no other native mode')
    if args.context_deviated and not args.native_qualify:
        ap.error('context-deviated applies only to the scoped native qualification mode')
    stamp = time.strftime('%Y%m%d-%H%M%S')
    out = REPO / '.loom-acceptance' / (('g2b-' if args.native_qualify else 'g2a-') + stamp)
    out.mkdir(parents=True, exist_ok=False)
    print('Evidence: ' + str(out), flush=True)
    write(out / 'run.json', {'kind': ('model-free native qualification' if args.native_qualify else
                           'real-service fixtures and native preflight' if args.native_preflight else 'fixture-only'),
                           'head': subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=REPO).decode().strip()})
    try:
        pin_sources(out)
        run(['go', 'build', '-mod=readonly', '-buildvcs=false', '-o', out / 'loom', './cmd/loom'], out / 'build.log')
        run(['go', 'test', '-c', '-o', out / 'httpapi.test', './internal/httpapi'], out / 'build-http-helper.log')
        write(out / 'binary.json', {'loom_sha256': sha(out / 'loom')})
        with cluster(out, Path(args.pg_bin)) as (root, env):
            counts = prerequisites(root, env, out, [args.case] if args.case else 'ABCD', args.gates)
            if args.native_preflight:
                native_preflight(root, env, out)
            elif args.native_qualify:
                qualify_native_context(root, env, out, args.context_deviated)
        write(out / 'receipt.json', {'fixture_result': 'passed', 'counts': counts,
                                   'corpus_sha256': sha(CORPUS_PATH),
                                   'model_trials': 'blocked by native qualification' if args.native_preflight else 'not-run'})
    except BaseException as exc:
        write(out / 'failure.json', {'error': str(exc), 'type': type(exc).__name__, 'original_logs_retained': True})
        raise
    print('Owned fixture complete; receipt: ' + str(out / 'receipt.json'), flush=True)


if __name__ == '__main__':
    def interrupted(signum, frame):
        raise KeyboardInterrupt(f'signal {signum}')
    signal.signal(signal.SIGTERM, interrupted)
    signal.signal(signal.SIGINT, interrupted)
    main()
