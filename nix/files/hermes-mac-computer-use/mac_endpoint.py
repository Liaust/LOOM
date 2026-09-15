"""Restricted Mac MCP admission. The app-owned daemon is never started/stopped.

The configuration and private state directory are provisioned by the operator.
Tests replace observe_runtime with synthetic observations in a separate process;
there is deliberately no runtime environment switch bypassing identity checks.
"""
from __future__ import annotations
import asyncio
import contextlib
import ctypes
import fcntl
import hashlib
import json
import os
from pathlib import Path
import pwd
import socket
import stat
import sys
import time
import uuid
from bridge import (ACTION, HANDSHAKE, INPUTS, READS, Fault, Peer,
                    failure, metadata, runtime_identity, serve, trusted_json, verified_bytes)

CONFIG = '/usr/local/etc/loom-mac-endpoint.json'


def checked_driver(path, digest):
    # The installer admits the pinned 61 MiB app executable up to 128 MiB.
    # Keep transport/config limits separate and retain ordinary owner checks.
    data = verified_bytes(path, limit=128 * 1024 * 1024)
    if hashlib.sha256(data).hexdigest() != digest:
        raise Fault('mac_host_identity_mismatch')
    return str(path)


def observe_runtime(binding):
    """Observe peer credentials and executable of the existing Darwin socket."""
    if sys.platform != 'darwin':
        raise Fault('mac_host_identity_mismatch')
    peer_path = Path(binding['socket'])
    st = peer_path.lstat()
    if not stat.S_ISSOCK(st.st_mode) or st.st_uid != os.getuid() or st.st_mode & 0o022:
        raise Fault('mac_permission_denied')
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as peer:
        peer.settimeout(0.5)
        peer.connect(str(peer_path))
        lib = ctypes.CDLL('/usr/lib/libSystem.B.dylib', use_errno=True)
        uid, gid = ctypes.c_uint(), ctypes.c_uint()
        if lib.getpeereid(peer.fileno(), ctypes.byref(uid), ctypes.byref(gid)) != 0:
            raise Fault('mac_host_identity_mismatch')
        # Darwin sys/un.h: SOL_LOCAL=0, LOCAL_PEERPID=2.
        pid = peer.getsockopt(0, 2)
        path = ctypes.create_string_buffer(4096)
        if lib.proc_pidpath(pid, path, len(path)) <= 0:
            raise Fault('mac_host_identity_mismatch')
        executable = os.fsdecode(path.value)
        # Fixed read-only daemon status on the identity-checked socket. It cannot
        # grant permissions or select another runtime. No arbitrary socket RPC.
        peer.sendall(b'{"method":"authorization_status"}\n')
        response = bytearray()
        deadline = time.monotonic() + 0.5
        while b'\n' not in response:
            if time.monotonic() >= deadline or len(response) >= 65536:
                raise Fault('mac_driver_unavailable')
            chunk = peer.recv(min(4096, 65536 - len(response)))
            if not chunk:
                raise Fault('mac_driver_unavailable')
            response.extend(chunk)
        status = json.loads(response.split(b'\n', 1)[0])
        if status.get('ok') is not True:
            raise Fault('mac_permission_denied')
        policy = status.get('result', {})
        mode = policy.get('permission_mode')
        if mode not in {'standard', 'bounded'}:
            raise Fault('mac_permission_denied')
        for prefix in ('user_policy', 'managed_policy'):
            if policy.get(prefix + '_configured') and not (policy.get(prefix + '_active') and policy.get(prefix + '_valid')):
                raise Fault('mac_permission_denied')
    driver = checked_driver(binding['driver'], binding['driver_sha256'])
    current = peer_path.lstat()
    if executable != driver or (current.st_dev, current.st_ino, current.st_uid, current.st_mode) != (st.st_dev, st.st_ino, st.st_uid, st.st_mode):
        raise Fault('mac_host_identity_mismatch')
    return {'host': socket.gethostname(), 'user': pwd.getpwuid(os.getuid()).pw_name,
            'uid': os.getuid(), 'peer_uid': uid.value, 'driver': driver,
            'driver_sha256': hashlib.sha256(Path(driver).read_bytes()).hexdigest(),
            'runtime_pid': pid, 'socket_inode': st.st_ino, 'socket': str(peer_path),
            'permission_mode': mode, 'user_policy_sha256': policy.get('user_policy_sha256'),
            'managed_policy_sha256': policy.get('managed_policy_sha256')}


def verify_runtime(binding):
    expected = binding['runtime']
    identity = runtime_identity(expected)
    try:
        observed = observe_runtime(expected)
    except PermissionError:
        raise Fault('mac_permission_denied') from None
    except (OSError, ValueError):
        raise Fault('mac_driver_unavailable') from None
    if expected.get('permission_mode') not in {'standard', 'bounded'}:
        raise Fault('mac_permission_denied')
    if identity != runtime_identity(observed):
        raise Fault('mac_host_identity_mismatch')
    return observed


class Endpoint:
    def __init__(self, binding):
        self.binding = binding
        self.peer = Peer()
        self.deadline = time.monotonic() + HANDSHAKE
        self.generation = uuid.uuid4().hex
        self.session = 'loom-' + uuid.uuid4().hex
        self.target = None
        self.tokens = {}
        self.epoch = None
        self.ready = False
        self.declared = False
        self.schemas = {}
        self.runtime = None
        self.initialize_request = None
        self.state = Path(binding['state_dir'])
        st = self.state.lstat()
        if not stat.S_ISDIR(st.st_mode) or st.st_uid != os.getuid() or stat.S_IMODE(st.st_mode) != 0o700:
            raise Fault('mac_permission_denied')
        flags = os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW
        self.lock = os.open(self.state / 'input.lock', flags, 0o600)
        if os.fstat(self.lock).st_uid != os.getuid() or not stat.S_ISREG(os.fstat(self.lock).st_mode):
            raise Fault('mac_permission_denied')

    def current_epoch(self):
        os.lseek(self.lock, 0, os.SEEK_SET)
        return os.read(self.lock, 128).decode('ascii')

    def invalidate(self):
        epoch = uuid.uuid4().hex
        os.lseek(self.lock, 0, os.SEEK_SET)
        os.ftruncate(self.lock, 0)
        os.write(self.lock, epoch.encode())
        os.fsync(self.lock)
        self.target, self.tokens, self.epoch = None, {}, None

    def clear_target(self):
        self.target, self.tokens, self.epoch = None, {}, None

    async def initialize_driver(self, request, observed, deadline):
        await self.peer.close()
        self.peer = Peer()
        self.generation = uuid.uuid4().hex
        self.session = 'loom-' + uuid.uuid4().hex
        self.clear_target()
        self.declared, self.ready = False, False
        self.schemas = {}
        driver = checked_driver(observed['driver'], observed['driver_sha256'])
        await self.peer.start([driver, 'mcp', '--socket', observed['socket']],
            {'PATH': '/usr/bin:/bin', 'LANG': 'C.UTF-8', 'CUA_DRIVER_RS_TELEMETRY_ENABLED': 'false',
             'CUA_DRIVER_RS_UPDATE_CHECK': 'false', 'CUA_DRIVER_EMBEDDED': '1'})
        result = await self.peer.exchange(request, deadline)
        if 'result' not in result or result['result'].get('serverInfo', {}).get('version') != '0.26.1':
            raise Fault('mac_driver_unavailable')
        if verify_runtime(self.binding) != observed:
            raise Fault('mac_driver_unavailable')
        self.runtime, self.ready = observed, True
        result['result'].setdefault('_meta', {})['loom'] = {'target_node': 'macbook',
            'generation': self.generation, 'binding': observed}
        return result

    async def list_tools(self, request, deadline):
        result = await self.peer.exchange(request, deadline)
        listing = result.get('result', {})
        if listing.get('capability_version') != '1' or listing.get('nextCursor'):
            raise Fault('mac_driver_unavailable')
        tools = listing.get('tools', [])
        self.schemas = {t['name']: t['inputSchema'] for t in tools if t['name'] in READS | INPUTS | {'start_session', 'end_session'}}
        if not {'list_windows', 'get_window_state', 'click', 'start_session', 'end_session'} <= self.schemas.keys():
            raise Fault('mac_driver_unavailable')
        listing['tools'] = [t for t in tools if t['name'] in self.schemas and t['name'] != 'set_value']
        return result

    def require_same_runtime(self):
        try:
            if verify_runtime(self.binding) != self.runtime:
                raise Fault('mac_target_stale')
        except Fault:
            self.clear_target()
            raise

    def args(self, name, args):
        if not isinstance(args, dict):
            raise Fault('mac_policy_refused')
        properties = self.schemas.get(name, {}).get('properties', {})
        # Hermes attaches a public label even to Cua's stateless window list.
        # It is not authority and must not leak into that tool's strict schema.
        stateless_listing = name == 'list_windows' and 'session' not in properties
        if stateless_listing and 'session' in args:
            if not isinstance(args['session'], str) or len(args['session']) > 65536:
                raise Fault('mac_policy_refused')
            args = {key: value for key, value in args.items() if key != 'session'}
        if any(k not in properties for k in args):
            raise Fault('mac_policy_refused')
        # No paths, launch selectors or runtime policy can arrive in a tool call.
        if set(args) & {'command', 'socket', 'screenshot_out_file', 'permission_mode', 'grant', 'capability_manifest'}:
            raise Fault('mac_policy_refused')
        for key, value in args.items():
            schema = properties[key]
            typ = schema.get('type')
            if typ == 'integer' and (type(value) is not int or value <= 0):
                raise Fault('mac_policy_refused')
            if typ == 'boolean' and type(value) is not bool:
                raise Fault('mac_policy_refused')
            if typ == 'string' and (not isinstance(value, str) or len(value) > 65536):
                raise Fault('mac_policy_refused')
            if typ == 'number' and (type(value) not in {float, int}):
                raise Fault('mac_policy_refused')
            if 'enum' in schema and value not in schema['enum']:
                raise Fault('mac_policy_refused')
        # Never accept a caller's session label as endpoint/agent authority.
        out = dict(args)
        if not stateless_listing:
            out['session'] = self.session
        return out

    async def handle(self, request):
        self.peer.sent = False
        deadline = self.deadline if self.initialize_request is None else time.monotonic() + ACTION
        method, params = request.get('method'), request.get('params') or {}
        if not isinstance(params, dict):
            raise Fault('mac_policy_refused')
        if method not in {'initialize', 'notifications/initialized', 'tools/list', 'tools/call', 'ping'}:
            raise Fault('mac_policy_refused')
        if method == 'initialize':
            if self.ready or self.peer.process is not None:
                raise Fault('mac_policy_refused')
            expected = params.get('_meta', {}).get('loom_binding')
            if runtime_identity(expected) != runtime_identity(self.binding['runtime']):
                raise Fault('mac_host_identity_mismatch')
            clean = dict(request, params={k: v for k, v in params.items() if k != '_meta'})
            self.initialize_request = clean
            return await self.initialize_driver(clean, verify_runtime(self.binding), deadline)
        if self.initialize_request is None:
            raise Fault('mac_driver_unavailable')
        try:
            observed = verify_runtime(self.binding)
        except Fault:
            self.clear_target()
            raise
        if not self.ready or observed != self.runtime or self.peer.process.returncode is not None:
            await self.initialize_driver(self.initialize_request, observed, deadline)
            await self.peer.exchange({'jsonrpc': '2.0', 'method': 'notifications/initialized'}, deadline)
            await self.list_tools({'jsonrpc': '2.0', 'id': 'loom-tools-' + uuid.uuid4().hex,
                                  'method': 'tools/list'}, deadline)
            if method == 'tools/call' and params.get('name') in INPUTS:
                raise Fault('mac_target_stale')
        if method == 'tools/list':
            return await self.list_tools(request, deadline)
        if method in {'notifications/initialized', 'ping'}:
            return await self.peer.exchange(request, deadline)
        name, arguments = params.get('name'), params.get('arguments', {})
        if name == 'set_value' or name not in self.schemas or name not in READS | INPUTS | {'start_session', 'end_session'}:
            raise Fault('mac_policy_refused')
        args = self.args(name, arguments)
        if name in {'start_session', 'end_session'}:
            result = await self.peer.exchange(dict(request, params={'name': name, 'arguments': {'session': self.session}}), deadline)
            if not isinstance(result.get('result'), dict) or result['result'].get('isError') is True:
                raise Fault('mac_driver_unavailable')
            self.declared = name == 'start_session'
            return result
        if not self.declared:
            declared = await self.peer.exchange({'jsonrpc': '2.0', 'id': 'loom-session-' + uuid.uuid4().hex,
                'method': 'tools/call', 'params': {'name': 'start_session', 'arguments': {'session': self.session}}}, deadline)
            if not isinstance(declared.get('result'), dict) or declared['result'].get('isError') is True:
                raise Fault('mac_driver_unavailable')
            self.declared = True
        locked = False
        mutation = name in INPUTS
        try:
            # Captures also hold the lock so an observation cannot cross another input.
            if mutation or name == 'get_window_state':
                try:
                    fcntl.flock(self.lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
                    locked = True
                except BlockingIOError:
                    raise Fault('mac_busy') from None
            target = (args.get('pid'), args.get('window_id'))
            self.require_same_runtime()
            if mutation:
                if self.target != target or None in target or self.epoch != self.current_epoch():
                    raise Fault('mac_target_stale')
                index = args.get('element_index')
                if index is not None and (index not in self.tokens or args.get('element_token') != self.tokens[index]):
                    raise Fault('mac_target_stale')
                saved_tokens = self.tokens.copy()
                # Write before dispatch: cancellation/process death cannot preserve old refs.
                self.invalidate()
            if name == 'get_window_state' and (None in target or any(type(x) is not int or x <= 0 for x in target)):
                raise Fault('mac_target_stale')
            if name == 'get_window_state':
                self.target, self.tokens, self.epoch = None, {}, None
            clean = dict(request, params={'name': name, 'arguments': args})
            result = await self.peer.exchange(clean, deadline)
            try:
                self.require_same_runtime()
            except Fault:
                if mutation:
                    raise Fault('mac_action_outcome_unknown', 'response', 'unknown') from None
                raise Fault('mac_target_stale', 'response', 'observed') from None
            value = result.get('result')
            if not isinstance(value, dict):
                raise Fault('mac_protocol_error')
            structured = value.get('structuredContent') or {}
            if not isinstance(structured, dict):
                raise Fault('mac_protocol_error')
            value['structuredContent'] = structured
            error = value.get('isError') is True
            native_code = structured.get('code')
            if native_code is not None:
                structured['driver_code'] = native_code
            code = 'mac_ok'
            if error:
                code = {'permission_denied': 'mac_permission_denied', 'mac_permission_denied': 'mac_permission_denied',
                    'desktop_unavailable': 'mac_desktop_unavailable', 'mac_desktop_unavailable': 'mac_desktop_unavailable', 'stale_element': 'mac_target_stale', 'window_id_not_found': 'mac_target_stale', 'window_owner_pid_mismatch': 'mac_target_stale'}.get(native_code, 'mac_action_failed')
            delivery = 'observed' if not mutation else 'confirmed'
            if error and code in {'mac_permission_denied', 'mac_target_stale', 'mac_desktop_unavailable'}:
                delivery = 'not_sent'
            structured.update(metadata(code, 'driver', delivery, self.generation))
            if mutation and not error:
                # Confirmed inputs retain native sticky targeting. The driver still
                # validates element tokens; all other processes' epochs are stale.
                self.target, self.tokens, self.epoch = target, saved_tokens, self.current_epoch()
            # Preserve native error/verdict fields and task content; never put them in logs.
            if not error and name == 'get_window_state':
                if not structured.get('elements') and not any(c.get('type') == 'image' for c in value.get('content', [])):
                    # Cua can return a successful MCP envelope for a degraded
                    # desktop capture with neither exact-window AX nor an image.
                    capture_error = structured.get('screenshot_error')
                    if (structured.get('degraded') is True
                            and isinstance(capture_error, dict)
                            and capture_error.get('code') == 'px_capture_unavailable'):
                        value['isError'] = True
                        structured.update(metadata('mac_desktop_unavailable', 'driver', 'observed', self.generation))
                        return result
                    raise Fault('mac_driver_unavailable', 'response', 'observed')
                self.target = target
                self.epoch = self.current_epoch()
                self.tokens = {e['element_index']: e['element_token'] for e in structured.get('elements', [])
                               if type(e.get('element_index')) is int and isinstance(e.get('element_token'), str)}
            return result
        except (asyncio.CancelledError, asyncio.TimeoutError, BrokenPipeError, ConnectionError):
            await self.peer.close()
            self.ready = False
            if mutation and self.peer.sent:
                raise Fault('mac_action_outcome_unknown', 'response', 'unknown') from None
            raise Fault('mac_connection_lost', 'response') from None
        except Fault as exc:
            if mutation and self.peer.sent and exc.meta['code'] in {'mac_connection_lost', 'mac_output_limit', 'mac_protocol_error'}:
                raise Fault('mac_action_outcome_unknown', 'response', 'unknown') from None
            raise
        finally:
            if locked:
                fcntl.flock(self.lock, fcntl.LOCK_UN)

    async def close(self):
        await self.peer.close()
        os.close(self.lock)


def main():
    if len(sys.argv) != 1 or os.environ.get('SSH_ORIGINAL_COMMAND', '') not in {'', '/usr/local/libexec/loom-mac-endpoint'}:
        raise SystemExit(2)
    try:
        asyncio.run(serve(Endpoint(trusted_json(CONFIG))))
    except Exception:
        raise SystemExit(1) from None


if __name__ == '__main__':
    main()
