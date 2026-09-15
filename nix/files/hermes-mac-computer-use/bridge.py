"""Fixed SSH MCP transport. No installation, daemon ownership, or GUI fallback.

HERMES_MAC_BINDING names trusted operator configuration, never tool arguments.
Packaging and the restricted server-side key are separate operator gates.
"""
from __future__ import annotations
import asyncio
import contextlib
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import stat
import sys
import time
import uuid

MAX_FRAME = 8 * 1024 * 1024
MAX_STDERR = 4096
CONNECT = 5.0
HANDSHAKE = 10.0
ACTION = 30.0
TEARDOWN = 5.0
ENDPOINT = '/usr/local/libexec/loom-mac-endpoint'
# Set only by the immutable installed launcher, never an inherited environment.
PINNED_BINDING_SHA256 = None
READS = frozenset({'list_windows', 'list_apps', 'get_window_state', 'get_displays', 'get_screen_size', 'get_cursor_position'})
INPUTS = frozenset({'click', 'double_click', 'right_click', 'middle_click', 'drag', 'scroll', 'type_text', 'press_key', 'hotkey', 'bring_to_front', 'set_value'})
RUNTIME_FIELDS = frozenset({'host', 'user', 'uid', 'peer_uid', 'driver', 'driver_sha256',
    'socket', 'permission_mode', 'user_policy_sha256', 'managed_policy_sha256'})
RUNTIME_OBSERVATIONS = frozenset({'runtime_pid', 'socket_inode'})
MESSAGES = {
    'mac_unreachable': 'Mac is unreachable, possibly asleep or offline. No desktop action was sent.',
    'mac_auth_failed': 'Mac authentication was refused.',
    'mac_host_identity_mismatch': 'The reviewed Mac host, user or runtime identity did not match.',
    'mac_desktop_unavailable': 'The approved Mac desktop is unavailable.',
    'mac_permission_denied': 'The Mac runtime refused permission.',
    'mac_driver_unavailable': 'The approved Mac driver is unavailable or incompatible.',
    'mac_target_stale': 'Take fresh Mac state and reconcile the selected target before another input.',
    'mac_busy': 'Another Mac transaction is active. No desktop action was sent.',
    'mac_connection_lost': 'The Mac connection closed before dispatch.',
    'mac_action_outcome_unknown': 'The Mac action outcome is unknown. It was not replayed. Observe and reconcile before another input.',
    'mac_action_failed': 'The Mac driver reported a failure; partial effects cannot be excluded.',
    'mac_policy_refused': 'This operation is unsupported by the reviewed Mac endpoint policy.',
    'mac_output_limit': 'The Mac response exceeded the reviewed size limit.',
    'mac_protocol_error': 'The Mac endpoint returned an invalid protocol frame.',
}


def metadata(code, phase='admission', delivery='not_sent', generation=None):
    return {'target_node': 'macbook', 'code': code, 'phase': phase, 'delivery': delivery,
            'retryable_after_recheck': code in {'mac_unreachable', 'mac_busy', 'mac_connection_lost'},
            'next_action': ('continue' if code == 'mac_ok' else 'observe_and_reconcile'
                            if delivery == 'unknown' or code in {'mac_target_stale', 'mac_action_failed'}
                            else 'recheck_binding_and_availability'),
            'generation': generation}


class Fault(Exception):
    def __init__(self, code, phase='admission', delivery='not_sent'):
        self.meta = metadata(code, phase, delivery)
        super().__init__(code + ": " + MESSAGES[code])


def failure(code, phase='admission', delivery='not_sent', generation=None):
    meta = metadata(code, phase, delivery, generation)
    return {'content': [{'type': 'text', 'text': MESSAGES[code]}], 'isError': True,
            'structuredContent': dict(meta, ok=False, message=MESSAGES[code])}


def runtime_identity(runtime):
    """Complete v1 bindings remain valid; only PID/inode are observations."""
    if not isinstance(runtime, dict) or set(runtime) != RUNTIME_FIELDS | RUNTIME_OBSERVATIONS:
        raise Fault('mac_host_identity_mismatch')
    for key in ('uid', 'peer_uid', 'runtime_pid', 'socket_inode'):
        if type(runtime[key]) is not int or runtime[key] <= 0:
            raise Fault('mac_host_identity_mismatch')
    if runtime['uid'] != runtime['peer_uid'] or runtime['permission_mode'] not in {'standard', 'bounded'}:
        raise Fault('mac_host_identity_mismatch')
    for key in ('host', 'user', 'driver', 'socket'):
        if not isinstance(runtime[key], str) or not runtime[key] or '\x00' in runtime[key] or '\n' in runtime[key]:
            raise Fault('mac_host_identity_mismatch')
    for key in ('driver', 'socket'):
        if not Path(runtime[key]).is_absolute():
            raise Fault('mac_host_identity_mismatch')
    for key in ('driver_sha256', 'user_policy_sha256', 'managed_policy_sha256'):
        value = runtime[key]
        if key != 'driver_sha256' and value is None:
            continue
        if not isinstance(value, str) or re.fullmatch('[0-9a-f]{64}', value) is None:
            raise Fault('mac_host_identity_mismatch')
    return {key: runtime[key] for key in RUNTIME_FIELDS}


def encode(frame):
    data = (json.dumps(frame, separators=(',', ':'), allow_nan=False) + '\n').encode()
    if len(data) > MAX_FRAME:
        raise Fault('mac_output_limit')
    return data


def decode(data):
    if len(data) > MAX_FRAME:
        raise Fault('mac_output_limit')
    try:
        def unique(pairs):
            out = {}
            for k, v in pairs:
                if k in out:
                    raise ValueError('duplicate key')
                out[k] = v
            return out
        obj = json.loads(data, object_pairs_hook=unique, parse_constant=lambda x: (_ for _ in ()).throw(ValueError()))
        if not isinstance(obj, dict) or obj.get('jsonrpc') != '2.0':
            raise ValueError()
        if 'method' in obj:
            if not isinstance(obj['method'], str) or not isinstance(obj.get('params', {}), dict):
                raise ValueError()
        if 'id' in obj and type(obj['id']) not in {int, str}:
            raise ValueError()
        return obj
    except (ValueError, TypeError, RecursionError):
        raise Fault('mac_protocol_error') from None


def verified_bytes(path, *, digest=None, limit=32 * 1024 * 1024, private=False):
    p = Path(path)
    if not p.is_absolute() or '\n' in str(p) or '\x00' in str(p):
        raise Fault('mac_host_identity_mismatch')
    if digest is not None and (type(digest) is not str or re.fullmatch('[0-9a-f]{64}', digest) is None):
        raise Fault('mac_host_identity_mismatch')
    fd = os.open(p, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    try:
        st = os.fstat(fd)
        if not stat.S_ISREG(st.st_mode) or st.st_mode & 0o022 or st.st_size > limit:
            raise Fault('mac_host_identity_mismatch')
        if private:
            if st.st_uid != os.getuid() or stat.S_IMODE(st.st_mode) != 0o600:
                raise Fault('mac_auth_failed')
            # SSH alone consumes credential bytes; the bridge checks custody.
            current = p.lstat()
            if (st.st_dev, st.st_ino, st.st_mode, st.st_uid, st.st_gid) != (current.st_dev, current.st_ino, current.st_mode, current.st_uid, current.st_gid):
                raise Fault('mac_auth_failed')
            return b''
        elif digest is None and st.st_uid not in {0, os.getuid()}:
            raise Fault('mac_host_identity_mismatch')
        # Exact content identity is independent of user-namespace UID mapping.
        with os.fdopen(fd, 'rb', closefd=False) as stream:
            data = stream.read(limit + 1)
        after = os.fstat(fd)
        current = p.lstat()
        identity = lambda s: (s.st_dev, s.st_ino, s.st_mode, s.st_uid, s.st_gid, s.st_size, s.st_mtime_ns, s.st_ctime_ns)
        if len(data) > limit or identity(st) != identity(after) or identity(st) != identity(current):
            raise Fault('mac_host_identity_mismatch')
        if digest is not None and hashlib.sha256(data).hexdigest() != digest:
            raise Fault('mac_host_identity_mismatch')
        return data
    finally:
        os.close(fd)


def trusted_json(path, digest=None):
    return json.loads(verified_bytes(path, digest=digest, limit=65536))


def checked_file(path, digest=None, *, pinned_public=False, private=False):
    p = Path(path)
    if pinned_public and digest is None:
        raise Fault('mac_host_identity_mismatch')
    data = verified_bytes(p, digest=digest if pinned_public else None, private=private)
    if digest is not None and hashlib.sha256(data).hexdigest() != digest:
        raise Fault('mac_host_identity_mismatch')
    return str(p)


def ssh_vector(binding, *, pinned_public=False):
    if binding.get('target_node') != 'macbook' or not re.fullmatch(r'[a-zA-Z0-9][a-zA-Z0-9.:-]*', binding['host']) or not re.fullmatch(r'[a-zA-Z_][a-zA-Z0-9_-]*', binding['user']):
        raise Fault('mac_host_identity_mismatch')
    ssh = checked_file(binding['ssh'], binding['ssh_sha256'], pinned_public=pinned_public)
    try:
        known = checked_file(binding['known_hosts'], binding['known_hosts_sha256'], pinned_public=pinned_public)
    except OSError:
        raise Fault('mac_host_identity_mismatch') from None
    try:
        key = checked_file(binding['identity_file'], private=True)
    except OSError:
        raise Fault('mac_auth_failed') from None
    return [ssh, '-F', '/dev/null', '-T', '-oBatchMode=yes', '-oStrictHostKeyChecking=yes',
            '-oUserKnownHostsFile=' + known, '-oGlobalKnownHostsFile=/dev/null',
            '-oIdentitiesOnly=yes', '-oIdentityAgent=none', '-oForwardAgent=no', '-oClearAllForwardings=yes',
            '-oPermitLocalCommand=no', '-oProxyCommand=none', '-oProxyJump=none', '-oControlMaster=no',
            '-oControlPath=none', '-oPasswordAuthentication=no', '-oKbdInteractiveAuthentication=no',
            '-oPreferredAuthentications=publickey', '-oUpdateHostKeys=no', '-oVerifyHostKeyDNS=no',
            '-oHostbasedAuthentication=no', '-oGSSAPIAuthentication=no', '-oNumberOfPasswordPrompts=0', '-oConnectionAttempts=1', '-oConnectTimeout=5',
            '-oServerAliveInterval=5', '-oServerAliveCountMax=1', '-i', key, '-l', binding['user'],
            '--', binding['host'], ENDPOINT]


class Peer:
    """One owned stdio child, one outstanding MCP exchange, bounded all the way."""
    def __init__(self):
        self.process = None
        self.stderr = bytearray()
        self.stderr_task = None
        self.sent = False

    async def start(self, command, env=None):
        self.process = await asyncio.create_subprocess_exec(*command, stdin=asyncio.subprocess.PIPE,
            stdout=asyncio.subprocess.PIPE, stderr=asyncio.subprocess.PIPE, limit=MAX_FRAME + 1,
            env=env or {'PATH': '/usr/bin:/bin', 'LANG': 'C.UTF-8'}, start_new_session=True)
        async def drain():
            while chunk := await self.process.stderr.read(1024):
                remaining = MAX_STDERR - len(self.stderr)
                self.stderr.extend(chunk[:max(0, remaining)])
        self.stderr_task = asyncio.create_task(drain())

    async def exchange(self, request, deadline):
        self.sent = False
        if self.process is None or self.process.returncode is not None:
            raise Fault('mac_connection_lost')
        data = encode(request)
        try:
            async with asyncio.timeout_at(deadline):
                self.process.stdin.write(data)
                self.sent = True  # any subsequent failure can be after delivery
                await self.process.stdin.drain()
                if 'id' not in request:
                    return None
                while True:
                    line = await self.process.stdout.readline()
                    if not line:
                        raise Fault('mac_connection_lost')
                    result = decode(line)
                    if result.get('id') != request['id'] or ('result' in result) == ('error' in result):
                        raise Fault('mac_protocol_error')
                    return result
        except (ValueError, asyncio.LimitOverrunError):
            raise Fault('mac_output_limit') from None

    async def close(self):
        if self.process:
            if self.process.returncode is None:
                with contextlib.suppress(ProcessLookupError):
                    os.killpg(self.process.pid, signal.SIGTERM)
                try:
                    await asyncio.wait_for(self.process.wait(), TEARDOWN / 2)
                except asyncio.TimeoutError:
                    with contextlib.suppress(ProcessLookupError):
                        os.killpg(self.process.pid, signal.SIGKILL)
                    await asyncio.wait_for(self.process.wait(), TEARDOWN / 2)
            if self.stderr_task:
                with contextlib.suppress(asyncio.CancelledError, asyncio.TimeoutError):
                    await asyncio.wait_for(self.stderr_task, TEARDOWN / 2)


def classify_ssh(stderr):
    # Never return stderr, a pathname, or an exception detail to Hermes/logs.
    if b'HOST IDENTIFICATION HAS CHANGED' in stderr or b'Host key verification failed' in stderr:
        return 'mac_host_identity_mismatch'
    if b'Permission denied' in stderr or b'Load key' in stderr or b'no such identity' in stderr:
        return 'mac_auth_failed'
    return 'mac_unreachable'


class Bridge:
    def __init__(self, binding, *, pinned_public=False):
        self.binding = binding
        self.pinned_public = pinned_public
        self.peer = Peer()
        self.generation = None
        self.ready = False
        self.uncertain = False
        self.observed = False
        self.deadline = time.monotonic() + HANDSHAKE

    async def handle(self, request):
        method = request.get('method')
        params = request.get('params') or {}
        name = params.get('name') if method == 'tools/call' else None
        mutating = name in INPUTS
        if method not in {'initialize', 'notifications/initialized', 'tools/list', 'tools/call', 'ping'}:
            raise Fault('mac_policy_refused')
        if name and name not in INPUTS | READS | {'start_session', 'end_session'}:
            raise Fault('mac_policy_refused')
        if name == 'set_value':
            raise Fault('mac_policy_refused')
        if mutating and (self.uncertain or not self.observed):
            raise Fault('mac_target_stale')
        try:
            if self.peer.process is None:
                await self.peer.start(ssh_vector(self.binding, pinned_public=self.pinned_public))
            outgoing = dict(request)
            if method == 'initialize':
                runtime_identity(self.binding['runtime'])
                outgoing['params'] = dict(params, _meta={'loom_binding': self.binding['runtime']})
            deadline = self.deadline if not self.ready else time.monotonic() + ACTION
            result = await self.peer.exchange(outgoing, deadline)
            if result is None:
                return None
            if method == 'initialize':
                if 'error' in result:
                    code = result['error'].get('data', {}).get('code', 'mac_driver_unavailable')
                    raise Fault(code if code in MESSAGES else 'mac_driver_unavailable')
                attestation = result['result'].get('_meta', {}).get('loom', {})
                if runtime_identity(attestation.get('binding')) != runtime_identity(self.binding['runtime']) or attestation.get('target_node') != 'macbook' or re.fullmatch('[0-9a-f]{32}', str(attestation.get('generation'))) is None:
                    raise Fault('mac_host_identity_mismatch')
                self.generation = attestation['generation']
                self.ready = True
            if method == 'tools/call':
                value = result.get('result', {})
                current = value.get('structuredContent', {}).get('generation')
                if current is not None:
                    if not isinstance(current, str) or re.fullmatch('[0-9a-f]{32}', current) is None:
                        raise Fault('mac_protocol_error')
                    if current != self.generation:
                        self.observed = False
                        self.generation = current
                if value.get('isError') is True and value.get('structuredContent', {}).get('delivery') == 'unknown':
                    self.uncertain = True
                    self.observed = False
                if value.get('isError') is True:
                    self.observed = False
                if name == 'get_window_state' and not value.get('isError') and 'result' in result:
                    self.observed = True
                    self.uncertain = False
            return result
        except (Exception, asyncio.CancelledError) as exc:
            sent = mutating and self.peer.sent
            self.observed = False
            if sent:
                self.uncertain = True
                raise Fault('mac_action_outcome_unknown', 'response', 'unknown') from None
            if isinstance(exc, Fault) and exc.meta['code'] not in {'mac_connection_lost'}:
                raise
            code = classify_ssh(self.peer.stderr) if not self.ready else 'mac_connection_lost'
            raise Fault(code, 'connect' if not self.ready else 'response') from None

    async def close(self):
        await self.peer.close()


def error_frame(request, fault, generation=None):
    meta = dict(fault.meta, generation=generation)
    if request.get('method') == 'tools/call':
        return {'jsonrpc': '2.0', 'id': request['id'], 'result': failure(meta['code'], meta['phase'], meta['delivery'], generation)}
    return {'jsonrpc': '2.0', 'id': request['id'], 'error': {'code': -32000, 'message': str(fault), 'data': meta}}


async def serve(handler):
    reader = asyncio.StreamReader(limit=MAX_FRAME + 1)
    transport, _ = await asyncio.get_running_loop().connect_read_pipe(lambda: asyncio.StreamReaderProtocol(reader), sys.stdin.buffer)
    active = None
    loop = asyncio.get_running_loop()
    closing = asyncio.Event()
    def shutdown():
        closing.set()
        reader.feed_eof()
    loop.add_signal_handler(signal.SIGTERM, shutdown)
    output_transport, output_protocol = await loop.connect_write_pipe(asyncio.streams.FlowControlMixin, sys.stdout.buffer)
    writer = asyncio.StreamWriter(output_transport, output_protocol, None, loop)
    async def emit(frame):
        try:
            writer.write(encode(frame))
            await asyncio.wait_for(writer.drain(), TEARDOWN)
        except (BrokenPipeError, ConnectionError, asyncio.TimeoutError, Fault):
            shutdown()
    async def respond(request):
        try:
            result = await handler.handle(request)
        except Fault as exc:
            result = error_frame(request, exc, getattr(handler, 'generation', None)) if 'id' in request else None
        except asyncio.CancelledError:
            result = error_frame(request, Fault('mac_action_outcome_unknown', 'cancel', 'unknown')) if 'id' in request else None
        except Exception:
            result = error_frame(request, Fault('mac_protocol_error')) if 'id' in request else None
        if result is not None:
            await emit(result)
    try:
        while not closing.is_set():
            try:
                line = await reader.readline()
                if not line:
                    break
                request = decode(line)
            except (Fault, ValueError):
                break
            if request.get('method') == 'notifications/cancelled':
                if active and not active.done() and request.get('params', {}).get('requestId') == active.request_id:
                    active.cancel()
                continue
            if active and not active.done():
                if 'id' in request:
                    await emit(error_frame(request, Fault('mac_busy')))
                continue
            if 'id' not in request:
                await respond(request)
                continue
            active = asyncio.create_task(respond(request))
            active.request_id = request.get('id')
    finally:
        loop.remove_signal_handler(signal.SIGTERM)
        transport.close()
        if active and not active.done():
            active.cancel()
        if active:
            with contextlib.suppress(asyncio.CancelledError):
                await active
        await handler.close()
        output_transport.close()


class RefusedBridge:
    def __init__(self, fault):
        self.fault = fault

    async def handle(self, request):
        raise self.fault

    async def close(self):
        pass


def main():
    if sys.argv[1:] != ['mcp']:
        raise SystemExit(2)
    try:
        binding = trusted_json(os.environ['HERMES_MAC_BINDING'], PINNED_BINDING_SHA256)
        handler = Bridge(binding, pinned_public=PINNED_BINDING_SHA256 is not None)
    except Fault as fault:
        handler = RefusedBridge(fault)
    except (OSError, KeyError, ValueError, TypeError, RecursionError):
        handler = RefusedBridge(Fault('mac_host_identity_mismatch'))
    try:
        asyncio.run(serve(handler))
    except Exception:
        # Pipe/protocol failure must not expose filesystem or exception prose.
        raise SystemExit(1) from None


if __name__ == '__main__':
    main()
