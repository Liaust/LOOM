#!/usr/bin/env bash
# Slice 4 independent disposable Hermes runtime/recovery acceptance.
# Real scaffold, native gateway/TUI, supported queries, interruption/replay,
# native ZIP completeness and disposable Borg fetch/import/readback.
# Every mutable path is a newly created fixture. No host service is installed.
set -euo pipefail
# Native import attempts automatic service installation, even when managed.
# This acceptance currently requires macOS kernel sandbox containment. Refuse
# unsupported hosts rather than running import without an equivalent boundary.
[[ "$(uname -s)" == Darwin && -x /usr/bin/sandbox-exec ]] || {
  printf '%s\n' 'This smoke requires macOS sandbox-exec containment.' >&2; exit 1;
}
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
NIX="${LOOM_TEST_NIX_BINARY:-/nix/var/nix/profiles/default/bin/nix}"
HERMES="${LOOM_TEST_HERMES_BINARY:-}"
if [[ -z "$HERMES" ]]; then
  package="$(cd "$ROOT" && "$NIX" --extra-experimental-features 'nix-command flakes' build --offline .#hermes-agent --builders '' --no-link --print-out-paths)"
  HERMES="$package/bin/hermes"
fi
BORG="${LOOM_TEST_BORG_BINARY:-}"
if [[ -z "$BORG" ]]; then
  package="$(cd "$ROOT" && "$NIX" --extra-experimental-features 'nix-command flakes' build --offline --builders '' --no-link --print-out-paths --impure --expr '
    let flake = (import ./tests/nix/source-flake.nix {});
        pkgs = flake.inputs.nixpkgs.legacyPackages.${builtins.currentSystem};
    in pkgs.borgbackup.overrideAttrs (_: {
      version = "1.4.3";
      src = pkgs.fetchPypi { pname = "borgbackup"; version = "1.4.3";
        hash = "sha256-ebv6dF0ZAdaFlzWEvS0Wo1Bobd0Xb2oiREkPsBmWRB8="; };
    })')"
  # Nix may print both the program and man-page outputs. Resolve exactly one
  # executable output; never concatenate those paths into a command.
  BORG="$(python3 - "$package" <<'PYBORG'
import pathlib,sys
matches=[str(pathlib.Path(p)/'bin/borg') for p in sys.argv[1].splitlines() if (pathlib.Path(p)/'bin/borg').is_file()]
assert len(matches)==1,matches
print(matches[0])
PYBORG
)"
fi

PYTHON="$(python3 - "$HERMES" <<'PY'
import pathlib,re,sys
wrapper=pathlib.Path(sys.argv[1]).with_name('.hermes-wrapped').read_text()
m=re.search(r"export HERMES_PYTHON='([^']+)'",wrapper)
assert m and m[1].startswith('/nix/store/')
print(m[1])
PY
)"
PARENT="$(mktemp -d "/private/tmp/loom-h4.XXXXXXXX")"
PARENT="$(cd "$PARENT" && pwd -P)"
RECEIPTS="$PARENT/.loom-acceptance"
WORKSPACE="$RECEIPTS/morathustra"
INSTALLED_ROOT="$WORKSPACE/skills"
mkdir -p "$RECEIPTS/home" "$RECEIPTS/tmp" "$WORKSPACE/recovery"
# Receipts and interrupted fixtures intentionally survive for review.
finish() {
  local result=$?
  trap - EXIT
  if [[ -n "${WRITER_PID:-}" ]]; then touch "$RECEIPTS/stop"; wait "$WRITER_PID" || result=1; fi
  if [[ -s "$RECEIPTS/host-before.json" ]]; then
    RECEIPTS="$RECEIPTS" python3 "$RECEIPTS/check-host.py" exit >"$RECEIPTS/host-exit.json" || result=1
  fi
  printf 'Receipts: %s\n' "$RECEIPTS" >&2
  exit "$result"
}
trap finish EXIT
cd "$ROOT"
go build -o "$RECEIPTS/loom" ./cmd/loom
go build -o "$RECEIPTS/loom-hermes-recovery" ./internal/hermesprofile/cmd/loom-hermes-recovery
go test -c -o "$RECEIPTS/cloudstorage.test" ./internal/cloudstorage
# The strict policy is inherited through exec and every allowed child. Native
# version/profile probes and import cannot fork. Recovery, native seed/WAL and
# Borg inherit the common boundary, which denies host-service access.
COMMON_POLICY='(version 1)
(allow default)
(deny network*)
(deny mach-lookup)
(deny file-read-data)
(allow file-read-data (literal "/") (subpath "/private/preboot")
  (subpath "/Library/Apple") (subpath "/private/var/db/dyld") (subpath "/nix/store") (subpath "/System") (subpath "/usr")
  (subpath "/bin") (subpath "/private/etc") (subpath "/dev")
  (subpath (param "FIXTURE")))
(deny file-write*)
(allow file-write* (subpath (param "FIXTURE")) (literal "/dev/null"))
(deny file-write* (subpath (param "INSTALLED")))
(deny process-exec (literal "/bin/launchctl") (literal "/usr/bin/launchctl")
  (literal "/usr/bin/osascript") (literal "/usr/bin/open")
  (literal "/usr/bin/systemctl") (literal "/bin/systemctl"))'
# The recovery adapter opens held directory descriptors from / down to the
# fixture and Nix binary. Permit only those ancestor directories, not siblings.
ANCESTORS="$(python3 - "$RECEIPTS" <<'PYPARENTS'
import pathlib,json,sys
paths=set(str(p) for p in pathlib.Path(sys.argv[1]).parents)|{'/nix'}
print('(allow file-read-data '+ ' '.join('(literal '+json.dumps(p)+')' for p in sorted(paths)) + ')')
PYPARENTS
)"
COMMON_POLICY="$COMMON_POLICY $ANCESTORS"
printf '%s\n' "$COMMON_POLICY" >"$RECEIPTS/containment.sb"
isolated() (
    cd "$WORKSPACE"
    /usr/bin/sandbox-exec -D "FIXTURE=$RECEIPTS" -D "INSTALLED=$INSTALLED_ROOT" -p "$COMMON_POLICY" \
      /usr/bin/env -i PATH=/usr/bin:/bin USER=fixture LOGNAME=fixture HOME="$RECEIPTS/home" TMPDIR="$RECEIPTS/tmp" \
      HERMES_HOME="$WORKSPACE/.hermes" HERMES_MANAGED=true HERMES_DISABLE_LAZY_INSTALLS=1 \
      HERMES_SKIP_NODE_BOOTSTRAP=1 RECEIPTS="$RECEIPTS" WORKSPACE="$WORKSPACE" "$@"
)
contained_import() (
    cd "$WORKSPACE"
    /usr/bin/sandbox-exec -D "FIXTURE=$RECEIPTS" -D "INSTALLED=$INSTALLED_ROOT" \
      -p "$COMMON_POLICY (deny process-fork)" \
      /usr/bin/env -i PATH=/usr/bin:/bin USER=fixture LOGNAME=fixture HOME="$RECEIPTS/home" TMPDIR="$RECEIPTS/tmp" \
      HERMES_HOME="$WORKSPACE/.hermes" HERMES_MANAGED=true HERMES_DISABLE_LAZY_INSTALLS=1 \
      HERMES_SKIP_NODE_BOOTSTRAP=1 RECEIPTS="$RECEIPTS" WORKSPACE="$WORKSPACE" "$@"
)
# Inspect only host metadata needed by the Slice 3 regression. Never read a
# live profile or LaunchAgent contents, and never remove host artifacts.
cat >"$RECEIPTS/check-host.py" <<'PYHOST'
import os,pathlib,pwd,stat,subprocess,json,sys
receipt=pathlib.Path(os.environ['RECEIPTS'])
agent_dir=pathlib.Path(pwd.getpwuid(os.getuid()).pw_dir)/'Library/LaunchAgents'
agent=agent_dir/'ai.hermes.gateway.plist'
assert not agent.exists() and not agent.is_symlink(), 'host Hermes LaunchAgent exists'
for domain in ['gui','user']:
    p=subprocess.run(['/bin/launchctl','print',f'{domain}/{os.getuid()}/ai.hermes.gateway'],capture_output=True,text=True)
    assert p.returncode==113 and 'Could not find service' in p.stderr, 'host job absence not proven'
# Metadata inventory detects additions/removals/edits without reading other jobs.
entries={p.name:[p.lstat().st_ino,p.lstat().st_mode,p.lstat().st_mtime_ns,p.lstat().st_size]
         for p in agent_dir.iterdir()} if agent_dir.exists() else {}
processes=[]
for line in subprocess.check_output(['/bin/ps','-axo','pid=,ppid=,command='],text=True).splitlines():
    fields=line.strip().split(None,2)
    if len(fields)!=3: continue
    pid,ppid,command=fields
    if int(pid) in [os.getpid(),os.getppid()]: continue
    # The fixture must have no remaining processes, and no native gateway may
    # already be running. Do not retain unrelated host command lines.
    if str(receipt) in command or ('hermes' in command and ('gateway/run.py' in command or ' -m gateway.run' in command or '/bin/hermes gateway' in command or ' -m tui_gateway.entry' in command or '/ui-tui/dist/entry.js' in command or '/bin/hermes --tui' in command)):
        processes.append(int(pid))
assert not processes, f'fixture or native gateway process exists: {processes}'
sockets=[]
for root,dirs,files in os.walk(receipt,followlinks=False):
    for name in dirs+files:
        p=pathlib.Path(root)/name
        if stat.S_ISSOCK(p.lstat().st_mode): sockets.append(str(p.relative_to(receipt)))
assert not sockets, 'fixture socket exists'
result={'launchagent_absent':True,'jobs_absent':True,'processes_absent':True,'sockets_absent':True,'launchagents_metadata':entries}
if sys.argv[1]!='before':
    before=json.loads((receipt/'host-before.json').read_text())
    assert entries==before['launchagents_metadata'], 'host LaunchAgents changed; stop immediately'
print(json.dumps(result,sort_keys=True))
PYHOST
RECEIPTS="$RECEIPTS" python3 "$RECEIPTS/check-host.py" before >"$RECEIPTS/host-before.json"
# Only the deliberately constructed sibling sentinel is probed for denied reads.
printf '%s\n' 'outside-fixture-sentinel' >"$PARENT/read-sentinel"
contained_import "$PYTHON" - >"$RECEIPTS/containment.json" <<'PYPROBE'
import os,pathlib,json,subprocess,socket,ctypes
outside=pathlib.Path(os.environ['RECEIPTS']).parent
try: (outside/'denied-write-probe').write_text('must be denied')
except PermissionError: pass
else: raise AssertionError('outside writes permitted')
try: (outside/'read-sentinel').read_text()
except PermissionError: pass
else: raise AssertionError('outside reads permitted')
try: subprocess.run(['/bin/echo','must be denied'],check=True)
except PermissionError: pass
else: raise AssertionError('subprocess permitted')
try:
    sock=socket.socket(socket.AF_INET,socket.SOCK_STREAM)
    try: sock.bind(('127.0.0.1',0))
    finally: sock.close()
except PermissionError: pass
else: raise AssertionError('network bind permitted')
# Query the effective kernel policy; do not send any request to a host service.
lib=ctypes.CDLL('/usr/lib/libsandbox.dylib')
assert lib.sandbox_check(os.getpid(), b'mach-lookup', 1, b'com.apple.cfprefsd.agent')!=0
assert not (outside/'denied-write-probe').exists()
print(json.dumps({'outside_reads_denied':True,'outside_writes_denied':True,'subprocess_denied':True,'network_bind_denied':True,'mach_lookup_denied':True}))
PYPROBE
RECEIPTS="$RECEIPTS" python3 "$RECEIPTS/check-host.py" probes >"$RECEIPTS/host-after-probes.json"
[[ "$(isolated "$BORG" --version)" == 'borg 1.4.3' ]]
contained_import "$HERMES" --version >"$RECEIPTS/hermes-version.txt"
# Evaluate the actual accepted package and defaults offline. Only fixture paths
# and boolean production-contract assertions are written to receipts.
LOOM_ACCEPTANCE_WORKSPACE="$WORKSPACE" "$NIX" --extra-experimental-features 'nix-command flakes' \
  eval --offline --impure --json --expr '
  let f = (import ./tests/nix/source-flake.nix {});
      c = f.nixosConfigurations.hardware-main.config;
      pkg = f.packages.${builtins.currentSystem}.hermes-agent;
      workspace = builtins.getEnv "LOOM_ACCEPTANCE_WORKSPACE";
  in {
    package = toString pkg;
    revision = pkg.upstreamRevision;
    version = pkg.version;
    sourceHash = pkg.upstreamSourceHash;
    serviceDisabled = !c.loom.morathustra.enable && !(c.systemd.services ? loom-morathustra);
    sameProfile = c.loom.morathustra.hermesHome == c.loom.morathustra.workspaceRoot + "/.hermes";
    baseline = c.loom.morathustra.baselineSettings // {
      skills = c.loom.morathustra.baselineSettings.skills // { external_dirs = [ (workspace + "/skills/installed") ]; };
    };
  }' >"$RECEIPTS/nix-contract.json"
# This is disposable provisioning, before native access: real accepted scaffold,
# real LOOM-distributed skill, then immutable installed boundaries for runtimes.
SETUP_READ="$(python3 - "$ROOT/ai-loom-pack" <<'PYSETUP'
import pathlib,json,sys
root=pathlib.Path(sys.argv[1])
print('(allow file-read-data (subpath '+json.dumps(str(root))+') '+ ' '.join('(literal '+json.dumps(str(p))+')' for p in root.parents) + ')')
PYSETUP
)"
/usr/bin/sandbox-exec -D "FIXTURE=$RECEIPTS" -D "INSTALLED=$WORKSPACE/skills" \
  -p "$COMMON_POLICY $SETUP_READ (allow file-write* (subpath \"$WORKSPACE/skills\"))" \
  /usr/bin/env -i PATH=/usr/bin:/bin HOME="$RECEIPTS/home" TMPDIR="$RECEIPTS/tmp" \
  "$RECEIPTS/loom" --json agent pack scaffold-workspace --pack-dir "$ROOT/ai-loom-pack" --path "$WORKSPACE" --yes >"$RECEIPTS/scaffold.json" 2>"$RECEIPTS/scaffold.stderr"
chmod u+w "$WORKSPACE/skills" "$WORKSPACE/skills/installed"
cp -R "$ROOT/ai-loom-pack/skills/search-loom-docs" "$WORKSPACE/skills/installed/"
find "$WORKSPACE/skills" -type f -exec chmod 0444 {} +
find "$WORKSPACE/skills" -type d -exec chmod 0555 {} +
contained_import "$PYTHON" - "$HERMES" >"$RECEIPTS/profile-contract.json" <<'PYCONTRACT'
import json,os,pathlib,sys
root=pathlib.Path(os.environ['WORKSPACE']); receipt=pathlib.Path(os.environ['RECEIPTS'])
contract=json.loads((receipt/'nix-contract.json').read_text())
assert contract['package']+'/bin/hermes'==sys.argv[1]
assert contract['revision']=='29112bef099274229cadff79cdff7bf7b99c4b77' and contract['version']=='0.21.0'
assert contract['serviceDisabled'] and contract['sameProfile']
cfg=contract['baseline']
assert cfg['memory']['write_approval'] and cfg['skills']['write_approval']
assert cfg['model']=={'default':'','provider':'auto'}
assert not cfg['curator']['enabled'] and not cfg['sessions']['auto_prune']
assert cfg['platforms'] and all(v=={'enabled':False} for v in cfg['platforms'].values())
assert 'create_dir' not in cfg['skills']
assert list(root.rglob('SOUL.md'))==[root/'.hermes/SOUL.md']
assert not (root/'MORA.md').exists() and not (root/'memory').exists()
(root/'.hermes/config.yaml').write_text(json.dumps(cfg))
installed=root/'skills/installed/search-loom-docs/SKILL.md'
assert 'Search LOOM Docs' in installed.read_text()
for mutation in [lambda:installed.write_text('denied'),lambda:installed.chmod(0o644),lambda:(root/'skills').rename(root/'moved-skills')]:
    try: mutation()
    except PermissionError: pass
    else: raise AssertionError('installed skills mutation permitted')
print(json.dumps({'pin':True,'service_disabled':True,'one_soul':True,'baseline':True,'installed_readable':True,'installed_write_chmod_rename_denied':True}))
PYCONTRACT
isolated "$PYTHON" - <<'PY'
import os,json,pathlib
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
from cryptography.hazmat.primitives.serialization import Encoding,PrivateFormat,PublicFormat,NoEncryption
root=pathlib.Path(os.environ['WORKSPACE']); profile=root/'.hermes'; receipt=pathlib.Path(os.environ['RECEIPTS'])
for name in ['sessions','memories','skills','cron','logs']:(profile/name).mkdir(parents=True,exist_ok=True)
assert (profile/'SOUL.md').is_file() and (root/'AGENTS.md').is_file()
from hermes_state import SessionDB
from tools.memory_tool import MemoryStore,memory_tool,apply_memory_pending
from tools.skill_manager_tool import skill_manage,apply_skill_pending
from tools import write_approval as wa
from tools.skills_tool import skill_view
store=MemoryStore();store.load_from_disk()
memory_result=json.loads(memory_tool(action='add',target='memory',content='slice3-memory-cobalt',store=store))
assert memory_result.get('staged') and 'slice3-memory-cobalt' not in str(store.memory_entries)
memory_pending=wa.get_pending(wa.MEMORY,memory_result['pending_id']); assert memory_pending
assert apply_memory_pending(memory_pending['payload'],store)['success']
assert wa.discard_pending(wa.MEMORY,memory_result['pending_id'])
payload={'action':'create','name':'slice3-created-skill','content':'---\nname: slice3-created-skill\ndescription: Disposable recovery proof\n---\n\nslice3-skill-amber\n'}
created=json.loads(skill_manage(**payload)); assert created.get('staged'),created
assert not (profile/'skills/slice3-created-skill').exists()
pending=wa.get_pending(wa.SKILLS,created['pending_id']); assert pending and all(pending['payload'].get(k)==v for k,v in payload.items()),pending
approved=json.loads(apply_skill_pending(pending['payload'])); assert approved.get('success'),approved
assert wa.discard_pending(wa.SKILLS,created['pending_id'])
rejected=json.loads(skill_manage(action='create',name='unapproved-skill',content=payload['content']))
assert rejected.get('staged') and not (profile/'skills/unapproved-skill').exists()
assert wa.discard_pending(wa.SKILLS,rejected['pending_id'])
assert 'Search LOOM Docs' in skill_view('search-loom-docs')
(receipt/'approval.json').write_text(json.dumps({'memory_staged_then_approved':True,'skill_staged_then_approved':True,'rejected_skill_absent':True,'external_skill_readback':True}))
with SessionDB() as db:
    db.create_session('slice3-original','cli')
    db.append_message('slice3-original','user','slice3searchcobalt is the recovery search marker.')
# Preserve native source modes; no synthetic chmod rule masks runtime behavior.
key=Ed25519PrivateKey.generate(); seed=key.private_bytes(Encoding.Raw,PrivateFormat.Raw,NoEncryption());pub=key.public_key().public_bytes(Encoding.Raw,PublicFormat.Raw)
(receipt/'signing.key').write_bytes(seed+pub);(receipt/'signing.key').chmod(0o600)
(receipt/'public-key.txt').write_text(pub.hex())
PY
# Foreground native runtime diagnostics. Gateway/session probes cannot fork.
# The real TUI needs Node plus its native Python backend. Its descendants inherit
# the file/network/Mach boundary and may execute only the reviewed pinned chain;
# signals are restricted to the same sandbox. The external fixture supervisor
# bounds and reaps only its own process group, then checks host state immediately.
cat >"$RECEIPTS/runtime-sessions.py" <<'PYSESSIONS'
import os,pathlib,json,time
r=pathlib.Path(os.environ['RECEIPTS']);w=pathlib.Path(os.environ['WORKSPACE'])
from gateway.session import SessionSource,SessionStore
from gateway.config import load_gateway_config,Platform
from hermes_state import SessionDB
from tools.session_search_tool import session_search
cfg=load_gateway_config()
assert not [p for p,v in cfg.platforms.items() if v.enabled]
assert not [k for k in os.environ if k.endswith(('_API_KEY','_TOKEN','_PASSWORD','_SECRET'))], 'unexpected credential environment'
s=SessionStore(w/'.hermes/sessions',cfg)
source=SessionSource(platform=Platform.LOCAL,chat_id='slice4-disposable-gateway',user_id='fixture')
g=s.get_or_create_session(source)
s.append_to_transcript(g.session_id,{'role':'user','content':'slice4gatewaycobalt is a synthetic gateway transcript fixture.'})
s.close_all_db_handles()
from tui_gateway import server
class Capture:
 def __init__(self):self.events=[]
 def write(self,obj):self.events.append(obj);return True
 def close(self):pass
capture=Capture()
def rpc(method,params):
 token=server.bind_transport(capture)
 try:result=server.handle_request({'jsonrpc':'2.0','id':method,'method':method,'params':params})
 finally:server.reset_transport(token)
 assert result and 'error' not in result,result
 return result['result']
t=rpc('session.create',{'cwd':str(w),'source':'cli'})
u=t['session_id'];key=t['stored_session_id']
assert key!=g.session_id and t['info']['cwd']==str(w)
assert rpc('session.title',{'session_id':u,'title':'Slice 4 disposable TUI fixture'})['pending'] is False
with SessionDB() as db:
 assert db.get_session(key) and db.get_session(g.session_id)
 db.append_message(key,'user','slice4tuicobalt is a synthetic TUI transcript fixture; no model response is simulated.')
for current,query,target in [(key,'slice4gatewaycobalt',g.session_id),(g.session_id,'slice4tuicobalt',key)]:
 result=json.loads(session_search(query=query,detail='full',current_session_id=current))
 assert result.get('success') and target in json.dumps(result) and query in json.dumps(result),result
 # Native search excludes the requesting session from the returned histories.
 (r/(query+'-search.json')).write_text(json.dumps(result))
s=SessionStore(w/'.hermes/sessions',cfg)
assert s.get_or_create_session(source).session_id==g.session_id
s.close_all_db_handles()
assert server._sessions[u]['agent_ready'].wait(20),'native TUI provider discovery did not finish'
error=server._sessions[u]['agent_error']
assert error and server._sessions[u]['agent'] is None,{'error':error,'agent_present':server._sessions[u]['agent'] is not None}
print('PROVIDER_ABSENT_ERROR',str(error),flush=True)
rpc('session.close',{'session_id':u})
(r/'runtime-sessions.json').write_text(json.dumps({'gateway_session_id':g.session_id,'tui_stored_session_id':key,'tui_runtime_session_id':u,'same_profile':True,'same_workspace':True,'provider_environment_empty':True,'platforms_disabled':True,'cross_session_search':True,'gateway_session_survives_store_restart':True,'tui_provider_absent':str(error),'synthetic_transcripts_native_apis':True,'rpc_events':capture.events},default=str))
print('NATIVE_SESSION_PROBE_PASSED',flush=True)
PYSESSIONS
cat >"$RECEIPTS/runtime-driver.py" <<'PYRUNTIME'
import os,pathlib,subprocess,time,signal,json,pty,select,re,fcntl,termios,struct,sys
r=pathlib.Path(sys.argv[1]);w=pathlib.Path(sys.argv[2]);h=pathlib.Path(sys.argv[3]);py=sys.argv[4]
assert r.name=='.loom-acceptance' and w.parent==r and h.is_absolute()
common=(r/'containment.sb').read_text()
strict=common+'\n(deny process-fork)\n(deny signal)\n'
wrap=h.with_name('.hermes-wrapped');source=wrap.read_text()
node=re.search(r"export HERMES_NODE='([^']+)'",source)[1]
# The Nix Python environment uses a binary wrapper around this immutable
# interpreter. Resolve it from the wrapper bytes without executing another tool.
interpreters=set(re.findall(rb'/nix/store/[a-z0-9]{32}-python3-[0-9.]+/bin/python3[.0-9]*',pathlib.Path(py).read_bytes()))
assert len(interpreters)==1,interpreters
paths=[str(h),str(wrap),source.splitlines()[0].split()[1],py,str(pathlib.Path(py).with_name('python3.12')),str(pathlib.Path(py).with_name('hermes')),node,next(iter(interpreters)).decode()]
paths=sorted(set(paths+[str(pathlib.Path(p).resolve()) for p in paths]))
assert all(p.startswith('/nix/store/') and pathlib.Path(p).is_file() for p in paths)
tui=common+'\n(deny signal)\n(allow signal (target same-sandbox))\n(deny process-exec)\n(allow process-exec '+' '.join('(literal '+json.dumps(p)+')' for p in paths)+')\n'
(r/'runtime-containment.sb').write_text(strict);(r/'tui-containment.sb').write_text(tui)
e={'PATH':'/usr/bin:/bin','USER':'fixture','LOGNAME':'fixture','HOME':str(r/'home'),'TMPDIR':str(r/'tmp'),'HERMES_HOME':str(w/'.hermes'),'HERMES_MANAGED':'true','HERMES_DISABLE_LAZY_INSTALLS':'1','HERMES_SKIP_NODE_BOOTSTRAP':'1','RECEIPTS':str(r),'WORKSPACE':str(w),'TERM':'xterm-256color','COLORTERM':'truecolor'}
def command(policy,args):
 return ['/usr/bin/sandbox-exec','-D',f'FIXTURE={r}','-D',f'INSTALLED={w}/skills','-p',policy]+args
def host(stage):
 with (r/f'host-after-{stage}.json').open('w') as out:
  subprocess.run(['/usr/bin/python3',str(r/'check-host.py'),stage],env={'PATH':'/usr/bin:/bin','RECEIPTS':str(r)},stdout=out,check=True)
def reap(p):
 if p is not None and p.poll() is None:
  os.killpg(p.pid,signal.SIGKILL);p.wait()
# This probe runs in an allowed child, proving it inherits containment. No
# service manager is invoked; exec denial is tested with an inert echo only.
probe="""import os,pathlib,subprocess,json,socket,ctypes
outside=pathlib.Path(os.environ['RECEIPTS']).parent
for action in [lambda:(outside/'read-sentinel').read_text(),lambda:(outside/'denied-tui-write').write_text('denied'),lambda:subprocess.run(['/bin/echo','denied'],check=True)]:
 try:action()
 except PermissionError:pass
 else:raise AssertionError('TUI descendant escaped containment')
s=socket.socket()
try:
 try:s.bind(('127.0.0.1',0))
 except PermissionError:pass
 else:raise AssertionError('TUI descendant may bind network')
finally:s.close()
lib=ctypes.CDLL('/usr/lib/libsandbox.dylib')
assert lib.sandbox_check(os.getpid(),b'mach-lookup',1,b'com.apple.cfprefsd.agent')!=0
try:os.kill(os.getppid(),0)
except PermissionError:pass
else:raise AssertionError('outside-sandbox signal permitted')
print(json.dumps({'descendant_outside_reads_writes_denied':True,'unlisted_exec_denied':True,'network_bind_denied':True,'mach_lookup_denied':True,'outside_signal_denied':True}))
"""
with (r/'tui-containment.json').open('w') as out:
 subprocess.run(command(tui,[py,'-c',probe]),env=e,cwd=w,stdout=out,stderr=subprocess.STDOUT,check=True,timeout=15)
host('tui-containment')
# A foreground gateway with every platform disabled must remain idle until the
# supervisor's planned SIGINT. Never use install/start/replace/force commands.
gateway_runs=[]
for stage in ['gateway-start','gateway-restart']:
 p=None;log_path=r/f'{stage}.log'
 try:
  with log_path.open('wb') as log:
   p=subprocess.Popen(command(strict,[str(h),'gateway','run','-v']),env=e,cwd=w,stdin=subprocess.DEVNULL,stdout=log,stderr=subprocess.STDOUT,start_new_session=True)
   until=time.monotonic()+20
   while p.poll() is None and time.monotonic()<until:
    text=log_path.read_text(errors='replace')
    if 'Press Ctrl+C to stop' in text:break
    time.sleep(.1)
   assert p.poll() is None and 'Press Ctrl+C to stop' in log_path.read_text(errors='replace'),'foreground gateway failed to become ready'
   assert 'No messaging platforms enabled.' in log_path.read_text()
   os.killpg(p.pid,signal.SIGINT);p.wait(timeout=12)
   assert p.returncode==0,p.returncode
   gateway_runs.append({'stage':stage,'pid':p.pid,'returncode':p.returncode,'platforms_disabled':True})
 finally:
  reap(p);host(stage)
(r/'gateway-runtime.json').write_text(json.dumps(gateway_runs))
with (r/'runtime-sessions.log').open('wb') as log:
 subprocess.run(command(strict,[py,str(r/'runtime-sessions.py')]),env=e,cwd=w,stdout=log,stderr=subprocess.STDOUT,check=True,timeout=35)
host('runtime-sessions')
# Launch the actual packaged TUI in a disposable PTY, submit one synthetic
# prompt, and require its native provider-absence error without authorizing a
# model. No setup/login command or external adapter is invoked.
master,slave=pty.openpty();fcntl.ioctl(slave,termios.TIOCSWINSZ,struct.pack('HHHH',32,120,0,0));p=None;data=bytearray()
try:
 p=subprocess.Popen(command(tui,[str(h),'--tui','chat','-q','slice4tuiactualcobalt disposable provider absence probe']),cwd=w,env=e,stdin=slave,stdout=slave,stderr=slave,start_new_session=True)
 os.close(slave);slave=None
 until=time.monotonic()+20
 while p.poll() is None and time.monotonic()<until:
  if select.select([master],[],[],.1)[0]:
   try:data.extend(os.read(master,65536))
   except OSError:break
  if b'No inference provider configured' in data:break
 assert p.poll() is None and b'No inference provider configured' in data,'native TUI did not report provider absence'
 assert b'Setup Required' in data and b'slice4tuiactualcobalt' in data
 os.killpg(p.pid,signal.SIGINT);p.wait(timeout=12)
 assert p.returncode in [0,130],p.returncode
 while select.select([master],[],[],.1)[0]:
  try:
   chunk=os.read(master,65536)
   if not chunk:break
   data.extend(chunk)
  except OSError:break
 (r/'tui-pty.json').write_text(json.dumps({'pid':p.pid,'returncode':p.returncode,'bytes':len(data),'provider_absence':True,'packaged_frontend':True,'exec_allowlist':paths}))
finally:
 (r/'tui-pty.log').write_bytes(data)
 reap(p)
 if slave is not None:os.close(slave)
 os.close(master);host('tui-pty')
PYRUNTIME
python3 "$RECEIPTS/runtime-driver.py" "$RECEIPTS" "$WORKSPACE" "$HERMES" "$PYTHON"
# Exercise the supported compiled LOOM CLI against a typed disposable UNIX
# fixture. Only this CLI policy permits outbound access to its exact socket;
# native Hermes retains the no-network boundary throughout.
cat >"$RECEIPTS/query-fixture.py" <<'PYQUERY'
import pathlib,os,json,sys,subprocess,socketserver,http.server,threading,urllib.parse,stat,socket
r=pathlib.Path(sys.argv[1]);w=r/'morathustra';sock=r/'loom-query.sock';py=sys.argv[2]
assert r.name=='.loom-acceptance' and not sock.exists()
policy=(r/'containment.sb').read_text()+'\n(deny process-fork)\n(deny signal)\n(allow network-outbound (remote unix-socket (literal '+json.dumps(str(sock))+')))\n'
(r/'query-containment.sb').write_text(policy)
e={'PATH':'/usr/bin:/bin','USER':'fixture','LOGNAME':'fixture','HOME':str(r/'home'),'TMPDIR':str(r/'tmp'),'RECEIPTS':str(r),'WORKSPACE':str(w)}
def cmd(args):return ['/usr/bin/sandbox-exec','-D',f'FIXTURE={r}','-D',f'INSTALLED={w}/skills','-p',policy]+args
record='11111111-1111-4111-8111-111111111114';candidate='22222222-2222-4222-8222-222222222224';case='33333333-3333-4333-8333-333333333334'
object_id='object_slice4_fixture';note_id='knowledge_object_slice4_fixture';now='2026-09-06T00:00:00Z'
obj={'object_id':object_id,'object_type':'file','name':'slice4-object-cobalt','state_class':'observed','status':'active','created_at':now,'updated_at':now,'metadata':{}}
note={'knowledge_object_id':note_id,'notes_source_root_id':'notes_root_fixture','title':'slice4-note-amber','relative_path':'Notes/fixture.md','file_class':'markdown','processing_state':'ready','recency_at':now,'last_seen_at':now,'absolute_time_metadata':{}}
def match(kind,posture,summary):return {'summary':{'text':summary,'truncated':False},'source':{'kind':kind,'source_reference_ids':[],'truncated':False},'freshness':{'recorded_at':now,'currentness':'pending_unaccepted' if kind=='pending_candidate' else 'current'},'assertion_posture':posture,'project_ids':['project_fixture'],'repository_ids':['repo_fixture'],'compact_truncated':False}
def search(pending):
 result={'schema_version':'loom.provenance.search.v1','query':'slice4 decision','ordering':'stable','accepted_records':{'items':[{'record_id':record,'match':match('accepted_record','accepted','slice4-accepted-cobalt'),'exact_get':{'resource':'record','id':record,'path':'/v1/provenance/records/'+record}}]},'pending_candidates':{'items':[]},'unresolved_cases':{'items':[{'case_id':case,'match':match('resolution_case','unresolved','slice4-unresolved-question'),'exact_get':{'resource':'case','id':case,'path':'/v1/provenance/cases/'+case}}]},'repo_state':{'items':[]},'returned':2,'truncated':False}
 if pending:result['pending_candidates']['items']=[{'candidate_id':candidate,'match':match('pending_candidate','unaccepted_candidate','slice4-unaccepted-amber'),'exact_get':{'resource':'candidate','id':candidate,'path':'/v1/provenance/candidates/'+candidate}}];result['returned']=3
 return result
requests=[];failures=[]
class Handler(http.server.BaseHTTPRequestHandler):
 def log_message(self,*args):pass
 def do_GET(self):self.respond()
 def do_POST(self):self.respond()
 def respond(self):
  try:
   u=urllib.parse.urlparse(self.path);q=urllib.parse.parse_qs(u.query)
   raw=self.rfile.read(int(self.headers.get('Content-Length','0')));body=json.loads(raw) if raw else None
   requests.append({'method':self.command,'path':u.path,'query':q,'body':body})
   if self.command=='GET' and u.path=='/v1/objects':
    assert q=={'limit':['3'],'project':['project_fixture'],'type':['file']},q;data=[obj]
   elif self.command=='GET' and u.path=='/v1/objects/'+object_id:data={'object':obj,'scope_links':[],'locations':[]}
   elif self.command=='POST' and u.path=='/v1/knowledge/notes/search':
    assert body['query']=='slice4 source' and body['project_ref']=='project_fixture' and body['mode']=='lexical' and body['limit']==3,body
    data={'query':'slice4 source','mode':'lexical','result_count':1,'results':[{'search_document_id':'search_document_fixture','knowledge_object_id':note_id,'title':'slice4-note-amber','relative_path':'Notes/fixture.md','citation':{'label':'Notes/fixture.md','source_ref':note_id}}]}
   elif self.command=='GET' and u.path=='/v1/knowledge/notes/objects/'+note_id:data=note
   elif self.command=='POST' and u.path=='/v1/provenance/search':
    assert body['query']=='slice4 decision' and body['project']=='project_fixture' and body['repository']=='repo_fixture' and body['limit']==4,body
    data=search(body.get('include_pending',False))
   elif self.command=='GET' and u.path=='/v1/provenance/records/'+record:data={'schema_version':'loom.provenance.record.lifecycle.v1','record':{'record_id':record,'assertion_posture':'accepted','claim':'slice4-accepted-cobalt'},'source_reference_ids':[],'representation_events':[]}
   elif self.command=='GET' and u.path=='/v1/provenance/candidates/'+candidate:data={'candidate':{'candidate_id':candidate,'assertion_posture':'unaccepted_candidate','claim':'slice4-unaccepted-amber'},'effective_state':'pending','source_reference_ids':[],'events':[]}
   elif self.command=='GET' and u.path=='/v1/provenance/cases/'+case:data={'case':{'case_id':case,'issue':'slice4-unresolved-question'},'effective_state':'open','members':[],'events':[]}
   else:raise AssertionError('undeclared request '+self.command+' '+self.path)
   payload=json.dumps({'ok':True,'data':data,'meta':{'correlation_id':'corr_slice4_fixture','source':'disposable-query-fixture','freshness':'fixture','generated_at':now}}).encode()
   self.send_response(200);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(payload)));self.end_headers();self.wfile.write(payload)
  except Exception as error:
   failures.append(str(error));self.send_error(500)
unrelated=socket.socket(socket.AF_UNIX);unrelated_path=pathlib.Path(str(sock)+'.unrelated');unrelated.bind(str(unrelated_path));unrelated.listen(1);unrelated_owned=unrelated_path.lstat()
server=socketserver.UnixStreamServer(str(sock),Handler);owned=sock.lstat();thread=threading.Thread(target=server.serve_forever,kwargs={'poll_interval':.05},daemon=True);thread.start()
results={}
def cli(name,args):
 p=subprocess.run(cmd([str(r/'loom'),'--json','--socket',str(sock)]+args),cwd=w,env=e,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True,timeout=15)
 (r/(name+'.stdout.json')).write_text(p.stdout);(r/(name+'.stderr')).write_text(p.stderr)
 assert p.returncode==0 and not failures,(name,p.returncode,p.stderr,failures)
 value=json.loads(p.stdout);results[name]=value
 return value.get('data',value)
try:
 # A local UNIX exception must not grant any TCP, unrelated UNIX, or host-file access.
 probe="""import socket,sys,pathlib
for address,family in [(('127.0.0.1',9),socket.AF_INET),(sys.argv[1]+'.unrelated',socket.AF_UNIX)]:
 s=socket.socket(family)
 try:
  try:s.connect(address)
  except PermissionError:pass
  else:raise AssertionError('query socket exception escaped')
 finally:s.close()
try:pathlib.Path(sys.argv[2]).read_text()
except PermissionError:pass
else:raise AssertionError('private sibling read permitted')
print('query-boundary-passed')
"""
 p=subprocess.run(cmd([py,'-c',probe,str(sock),str(r.parent/'read-sentinel')]),cwd=w,env=e,capture_output=True,text=True,timeout=8)
 assert p.returncode==0,(p.stdout,p.stderr)
 assert cli('objects-list',['object','list','--project','project_fixture','--type','file','--limit','3'])[0]['object_id']==object_id
 assert cli('object-inspect',['object','inspect',object_id])['object']['object_id']==object_id
 assert cli('notes-search',['notes','search','slice4 source','--project','project_fixture','--mode','lexical','--limit','3'])['results'][0]['knowledge_object_id']==note_id
 assert cli('notes-exact',['notes','objects','show',note_id])['knowledge_object_id']==note_id
 args=['provenance','search','slice4 decision','--project','project_fixture','--repo','repo_fixture','--limit','4']
 a=cli('provenance-default',args);assert not a['pending_candidates']['items'] and candidate not in json.dumps(a)
 b=cli('provenance-with-pending',args+['--include-pending']);assert b['accepted_records']['items'][0]['record_id']==record and b['pending_candidates']['items'][0]['candidate_id']==candidate and b['pending_candidates']['items'][0]['match']['assertion_posture']=='unaccepted_candidate'
 assert cli('provenance-record',['provenance','record','get',record])['record']['record_id']==record
 assert cli('provenance-candidate',['provenance','candidate','get',candidate])['effective_state']=='pending'
 assert cli('provenance-case',['provenance','case','get',case])['case']['case_id']==case
 assert len(requests)==9 and not failures
 # Once the fixture closes, the CLI must fail on that exact socket; it must not
 # fall back to another configured node or invent a result.
finally:
 server.shutdown();server.server_close();thread.join(timeout=2);assert not thread.is_alive()
 unrelated.close();assert unrelated_path.lstat().st_ino==unrelated_owned.st_ino;unrelated_path.unlink()
 current=sock.lstat();assert (current.st_ino,current.st_dev)==(owned.st_ino,owned.st_dev) and stat.S_ISSOCK(current.st_mode);sock.unlink()
p=subprocess.run(cmd([str(r/'loom'),'--json','--socket',str(sock),'object','inspect',object_id]),cwd=w,env=e,capture_output=True,text=True,timeout=12)
assert p.returncode!=0 and json.loads(p.stdout)['ok'] is False,(p.returncode,p.stdout,p.stderr)
(r/'query-offline-refusal.json').write_text(p.stdout)
(r/'loom-query-contract.json').write_text(json.dumps({'supported_cli_routes':True,'fixture_responses_not_live_backend':True,'requests':requests,'pending_explicit_and_separate':True,'exact_gets':True,'offline_refusal':True,'tcp_unrelated_unix_private_read_denied':True,'socket_removed':not sock.exists()},indent=2))
print('LOOM_QUERY_FIXTURE_PASSED')
PYQUERY
python3 "$RECEIPTS/query-fixture.py" "$RECEIPTS" "$PYTHON"
RECEIPTS="$RECEIPTS" python3 "$RECEIPTS/check-host.py" queries >"$RECEIPTS/host-after-queries.json"
# Read-only before/after metadata makes native source-namespace changes visible
# without inspecting or copying raw SQLite bytes into recovery evidence.
cat >"$RECEIPTS/inventory.py" <<'PYINVENTORY'
import pathlib,sys,json,stat
root=pathlib.Path(sys.argv[1]);rows=[]
for p in [root]+sorted(root.rglob('*')):
 s=p.lstat();rows.append({'path':str(p.relative_to(root)),'mode':oct(stat.S_IMODE(s.st_mode)),'type':stat.S_IFMT(s.st_mode),'inode':s.st_ino,'mtime_ns':s.st_mtime_ns,'ctime_ns':s.st_ctime_ns,'size':s.st_size})
print(json.dumps(rows,indent=2))
PYINVENTORY
# The supported CLI initializes native logging before the bounded inventory.
# The adapter refuses capture-time namespace creation/rotation, including logs.
isolated "$HERMES" backup --help >"$RECEIPTS/backup-help.txt" 2>&1
CREATED="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
# Interrupt the actual native backup child while holding its fixture-only lock.
# Preserve the empty staging attempt, then retry this exact logical request
# below while native WAL writes are active; no recovery attempt is removed.
cat >"$RECEIPTS/interrupt-fixture.py" <<'PYINTERRUPT'
import pathlib,os,sys,json,subprocess,fcntl,signal,time,stat
r=pathlib.Path(sys.argv[1]);w=r/'morathustra';h=sys.argv[2];ident=sys.argv[3];created=sys.argv[4]
assert r.name=='.loom-acceptance' and not (w/'recovery'/ident).exists()
base=w/'recovery/.staging';before=set(base.iterdir()) if base.exists() else set()
lock=w/'.hermes/.backup.lock';fd=os.open(lock,os.O_RDWR|os.O_CREAT|os.O_NOFOLLOW,0o600);held=os.fstat(fd)
assert stat.S_ISREG(held.st_mode) and held.st_nlink==1 and held.st_size==0
fcntl.flock(fd,fcntl.LOCK_EX|fcntl.LOCK_NB)
policy=(r/'containment.sb').read_text()+'\n(deny signal)\n(allow signal (target same-sandbox))\n'
(r/'interrupt-containment.sb').write_text(policy)
e={'PATH':'/usr/bin:/bin','USER':'fixture','LOGNAME':'fixture','HOME':str(r/'home'),'TMPDIR':str(r/'tmp'),'HERMES_HOME':str(w/'.hermes'),'HERMES_MANAGED':'true','HERMES_DISABLE_LAZY_INSTALLS':'1','HERMES_SKIP_NODE_BOOTSTRAP':'1'}
argv=[str(r/'loom-hermes-recovery'),'--mode','publish','--fixture','--workspace',str(w),'--hermes',h,'--id',ident,'--created-at',created,'--signing-key-file',str(r/'signing.key')]
command=['/usr/bin/sandbox-exec','-D',f'FIXTURE={r}','-D',f'INSTALLED={w}/skills','-p',policy]+argv
p=None;observed=[];added=[]
try:
 with (r/'interrupted-publish.log').open('wb') as out:
  p=subprocess.Popen(command,cwd=w,env=e,stdin=subprocess.DEVNULL,stdout=out,stderr=subprocess.STDOUT,start_new_session=True)
  deadline=time.monotonic()+12
  while p.poll() is None and time.monotonic()<deadline:
   added=sorted(set(base.iterdir())-before) if base.exists() else []
   if added:
    # Inspect only the supervisor-owned process group. Retain no unrelated
    # host process arguments. The held native lock prevents backup completion.
    for line in subprocess.check_output(['/bin/ps','-axo','pid=,pgid=,command='],text=True).splitlines():
     fields=line.strip().split(None,2)
     if len(fields)==3 and int(fields[1])==p.pid and ' backup --output ' in fields[2] and str(base) in fields[2]:
      observed.append({'pid':int(fields[0]),'pgid':int(fields[1]),'command':fields[2]})
    if observed:break
   time.sleep(.001)
  assert p.poll() is None and len(added)==1 and observed,'native backup did not enter its bounded interruption window'
  assert not (w/'recovery'/ident).exists() and not (added[0]/'manifest.json').exists()
  os.killpg(p.pid,signal.SIGKILL);p.wait(timeout=5)
  assert p.returncode==-signal.SIGKILL,p.returncode
finally:
 if p is not None and p.poll() is None:os.killpg(p.pid,signal.SIGKILL);p.wait()
 fcntl.flock(fd,fcntl.LOCK_UN);os.close(fd)
 # The new staging attempt belongs to the interrupted request and remains
 # untouched. There is no final package and therefore no accepted evidence.
 assert not (w/'recovery'/ident).exists()
assert len(added)==1 and added[0].is_dir() and not list(added[0].iterdir())
s=added[0].stat();attempt={'path':str(added[0]),'inode':s.st_ino,'mode':stat.S_IMODE(s.st_mode),'mtime_ns':s.st_mtime_ns}
(r/'interruption.json').write_text(json.dumps({'id':ident,'created_at':created,'native_backup_observed':observed,'supervised_group':p.pid,'signal':'SIGKILL','exit':p.returncode,'final_absent':True,'staging_preserved':attempt,'native_lock_released':True,'retry_argv':argv},indent=2))
# No native writer runs concurrently with this phase. Allow only a bounded
# reap interval for killed children before the ordinary host-residue assertion.
for attempt in range(20):
 alive=[]
 for line in subprocess.check_output(['/bin/ps','-axo','pid=,pgid=,stat='],text=True).splitlines():
  fields=line.split()
  if len(fields)==3 and int(fields[1])==p.pid and not fields[2].startswith('Z'):alive.append(int(fields[0]))
 if not alive:break
 time.sleep(.05)
assert not alive,alive
with (r/'host-after-interruption.json').open('w') as out:
 subprocess.run(['/usr/bin/python3',str(r/'check-host.py'),'interruption'],env={'PATH':'/usr/bin:/bin','RECEIPTS':str(r)},stdout=out,check=True)
print('NATIVE_BACKUP_INTERRUPTED_WITH_NO_PUBLISHED_EVIDENCE')
PYINTERRUPT
python3 "$RECEIPTS/interrupt-fixture.py" "$RECEIPTS" "$HERMES" slice3-native "$CREATED"
cat > "$RECEIPTS/writer.py" <<'PY'
import os,time,pathlib
from hermes_state import SessionDB
receipt=pathlib.Path(os.environ['RECEIPTS']); count=0
with SessionDB() as db:
    db.create_session('slice3-writer','cli')
    db.append_message('slice3-writer','user','slice3walcobalt is committed while the WAL connection remains open.')
    (receipt/'ready').write_text('ready')
    while not (receipt/'stop').exists() and count<20000:
        db.append_message('slice3-writer','user',f'disposable WAL entry {count}')
        count+=1; time.sleep(0.02)
(receipt/'wal-count.txt').write_text(str(count))
PY
isolated "$PYTHON" "$RECEIPTS/writer.py" >"$RECEIPTS/writer.log" 2>&1 &
WRITER_PID=$!
for ((attempt=0;attempt<200;attempt++)); do
  [[ -f "$RECEIPTS/ready" ]] && break
  kill -0 "$WRITER_PID"
  sleep 0.05
done
[[ -f "$RECEIPTS/ready" ]]
[[ -s "$WORKSPACE/.hermes/state.db-wal" ]]
for pass in first replay; do
  python3 "$RECEIPTS/inventory.py" "$WORKSPACE/.hermes" >"$RECEIPTS/$pass-before-inventory.json"
  if isolated "$RECEIPTS/loom-hermes-recovery" --mode publish --fixture --workspace "$WORKSPACE" \
    --hermes "$HERMES" --id slice3-native --created-at "$CREATED" \
    --signing-key-file "$RECEIPTS/signing.key" >"$RECEIPTS/$pass.json" 2>"$RECEIPTS/$pass.stderr"; then
    python3 "$RECEIPTS/inventory.py" "$WORKSPACE/.hermes" >"$RECEIPTS/$pass-after-inventory.json"
  else
    result=$?
    python3 "$RECEIPTS/inventory.py" "$WORKSPACE/.hermes" >"$RECEIPTS/$pass-after-inventory.json"
    # Diagnostic only: metadata and already-staged ZIP inventory. Preserve
    # native timestamps and sidecars; do not repair inputs to obtain a pass.
    contained_import "$PYTHON" - "$pass" <<'PYFAILURE'
import os,pathlib,json,zipfile,sys
r=pathlib.Path(os.environ['RECEIPTS']);w=pathlib.Path(os.environ['WORKSPACE']);name=sys.argv[1]
a={v['path']:v for v in json.loads((r/f'{name}-before-inventory.json').read_text())}
b={v['path']:v for v in json.loads((r/f'{name}-after-inventory.json').read_text())}
delta={'added':[b[k] for k in sorted(b.keys()-a.keys())],'removed':[a[k] for k in sorted(a.keys()-b.keys())],
       'changed':[{'path':k,'before':a[k],'after':b[k]} for k in sorted(a.keys()&b.keys()) if a[k]!=b[k]]}
(r/f'{name}-inventory-delta.json').write_text(json.dumps(delta,indent=2))
errors=[]
for key,item in a.items():
    if item['type']!=32768 or item['mtime_ns']>=315532800000000000:continue
    try:zipfile.ZipInfo.from_file(w/'.hermes'/key,arcname=key)
    except ValueError as error:errors.append({'path':key,'error':str(error),'mtime_ns':item['mtime_ns']})
staged=[]
for p in sorted((w/'recovery/.staging').glob('*/profile.zip')):
    with zipfile.ZipFile(p) as z:names=z.namelist()
    staged.append({'path':str(p.relative_to(w)),'entries':names,'omitted_epoch_files':[e['path'] for e in errors if e['path'] not in names]})
(r/'zip-timestamp-diagnostic.json').write_text(json.dumps({'read_only_native_zipinfo_probe':True,'probe_errors':errors,'staged':staged},indent=2))
PYFAILURE
    cat "$RECEIPTS/$pass.stderr" >&2
    exit "$result"
  fi
done
cmp "$RECEIPTS/first.json" "$RECEIPTS/replay.json"
touch "$RECEIPTS/stop"
wait "$WRITER_PID"
WRITER_PID=""
cat >"$RECEIPTS/native-audit.py" <<'PYAUDIT'
import os,pathlib,sys,json,hashlib,zipfile,stat,re
r=pathlib.Path(os.environ['RECEIPTS']);w=pathlib.Path(os.environ['WORKSPACE']);e=json.loads((r/'first.json').read_text());package=pathlib.Path(e['path'])
m=json.loads((package/'manifest.json').read_text())['manifest']
sources={v['path']:v for v in m['sources']};inventory={v['path']:v for v in m['inventory']}
assert len(sources)==len(m['sources']) and len(inventory)==len(m['inventory'])
assert m['binary']==sys.argv[1] and m['version']=='0.21.0' and m['revision']=='29112bef099274229cadff79cdff7bf7b99c4b77'
assert {'state.db','projects.db','cron/executions.db'}<=sources.keys()
before={v['path']:v for v in json.loads((r/'first-before-inventory.json').read_text())};after={v['path']:v for v in json.loads((r/'first-after-inventory.json').read_text())}
epoch=[]
with zipfile.ZipFile(package/'profile.zip') as z:
 names=z.namelist();assert len(names)==len(set(names)) and set(names)==sources.keys()==inventory.keys()
 for name,source in sources.items():
  entry=z.getinfo(name);item=inventory[name];raw=z.read(name)
  assert hashlib.sha256(raw).hexdigest()==item['sha256'] and len(raw)==item['size']
  assert (entry.external_attr>>16)&0o777==item['mode']
  assert not pathlib.PurePosixPath(name).is_absolute() and '..' not in pathlib.PurePosixPath(name).parts
  if source['online_database'] or source['operational_log']:continue
  # This audit reads ordinary files only. Online DB bytes are touched only by
  # native Hermes; their consistent snapshots are verified inside the ZIP.
  p=w/'.hermes'/name;s=p.stat();identity=source['identity']
  assert hashlib.sha256(p.read_bytes()).hexdigest()==source['sha256']==item['sha256']
  assert s.st_ino==identity['inode'] and s.st_mtime_ns==identity['modified_ns']
  assert stat.S_IMODE(s.st_mode)==identity['mode']&0o777
  if identity['modified_ns']<315532800000000000:
   assert name.startswith('skills/') and entry.date_time==(1980,1,1,0,0,0)
   assert before[name]['mtime_ns']==after[name]['mtime_ns']==s.st_mtime_ns==1000000000
   epoch.append(name)
assert len(epoch)==327,len(epoch)
assert list(w.rglob('SOUL.md'))==[w/'.hermes/SOUL.md']
log=(w/'.hermes/logs/agent.log').read_text();completed=[line for line in log.splitlines() if 'hermes_cli.backup: backup phase=archive status=complete' in line]
assert len(completed)==1,completed
count=re.search(r'files=(\d+) errors=(\d+)',completed[0]);assert count and int(count[1])==len(sources) and count[2]=='0',completed
assert (r/'first.json').read_bytes()==(r/'replay.json').read_bytes()
interruption=json.loads((r/'interruption.json').read_text());assert interruption['id']==e['id'] and interruption['created_at']==e['created_at']
p=pathlib.Path(interruption['staging_preserved']['path']);s=p.stat();saved=interruption['staging_preserved']
assert s.st_ino==saved['inode'] and s.st_mtime_ns==saved['mtime_ns'] and stat.S_IMODE(s.st_mode)==saved['mode'] and not list(p.iterdir())
assert list((w/'recovery/.staging').iterdir())==[p]
added=sorted(after.keys()-before.keys());expected={'projects.db-wal','projects.db-shm','cron/executions.db-wal','cron/executions.db-shm'}
assert expected<=set(added),added
result={'native_binary':m['binary'],'source_count':len(sources),'zip_count':len(inventory),'exact_source_zip_inventory':True,'archive_errors':0,'native_sidecars_added':added,'epoch_skill_count':len(epoch),'epoch_source_mtimes_unchanged':True,'epoch_skill_paths':epoch,'interrupted_request_retried':True,'interrupted_staging_unchanged':True,'exact_replay':True,'one_soul_after_runtime':True}
(r/'native-recovery-audit.json').write_text(json.dumps(result,indent=2))
print('NATIVE_RECOVERY_COMPLETENESS_AND_INTERRUPTION_AUDIT_PASSED')
PYAUDIT
contained_import "$PYTHON" "$RECEIPTS/native-audit.py" "$HERMES"
PUBLIC_KEY="$(cat "$RECEIPTS/public-key.txt")"
isolated /usr/bin/env LOOM_TEST_HERMES_WORKSPACE="$WORKSPACE" LOOM_TEST_HERMES_RECEIPTS="$RECEIPTS" \
  LOOM_TEST_HERMES_PUBLIC_KEY="$PUBLIC_KEY" LOOM_TEST_BORG_BINARY="$BORG" \
  "$RECEIPTS/cloudstorage.test" -test.count=1 -test.run '^TestBorgMorathustraNativeRecoveryRestore$' -test.v >"$RECEIPTS/borg.log" 2>&1
RESTORED="$(python3 - "$RECEIPTS/cloud-restore.json" <<'PY'
import json,sys
print(json.load(open(sys.argv[1]))['restored_package'])
PY
)"
# Import into another empty, explicitly synthetic profile via supported Hermes.
WORKSPACE="$RECEIPTS/restored"
mkdir -p "$WORKSPACE/.hermes"/{sessions,memories,skills,cron,logs}
printf '%s\n' 'Temporary restore fixture personality.' >"$WORKSPACE/.hermes/SOUL.md"
contained_import "$HERMES" import "$RESTORED/profile.zip" >"$RECEIPTS/import.log" 2>&1
RECEIPTS="$RECEIPTS" python3 "$RECEIPTS/check-host.py" import >"$RECEIPTS/host-after-import.json"
isolated "$PYTHON" - >"$RECEIPTS/restored-search.json" <<'PY'
import json,os,pathlib
from tools.session_search_tool import session_search
from tools.memory_tool import MemoryStore
from tools.skills_tool import skill_view
result=json.loads(session_search(query='slice3searchcobalt',detail='full'))
assert result.get('success') and 'slice3searchcobalt' in json.dumps(result),result
wal_result=json.loads(session_search(query='slice3walcobalt',detail='full'))
assert wal_result.get('success') and 'slice3walcobalt' in json.dumps(wal_result),wal_result
runtime=json.loads((pathlib.Path(os.environ['RECEIPTS'])/'runtime-sessions.json').read_text())
for current,query,target in [(runtime['tui_stored_session_id'],'slice4gatewaycobalt',runtime['gateway_session_id']),(runtime['gateway_session_id'],'slice4tuicobalt',runtime['tui_stored_session_id'])]:
    found=json.loads(session_search(query=query,detail='full',current_session_id=current))
    assert found.get('success') and target in json.dumps(found) and query in json.dumps(found),found
# Native import restores all 327 bundled Nix skill files. Paths and bytes must
# match the authenticated inventory. Native import uses ordinary creation modes
# for new files; exact original modes are authenticated on the ZIP, not imposed
# on Hermes' writable native skill store after import.
receipt=pathlib.Path(os.environ['RECEIPTS']);audit=json.loads((receipt/'native-recovery-audit.json').read_text())
cloud=json.loads((receipt/'cloud-restore.json').read_text());manifest=json.loads((pathlib.Path(cloud['restored_package'])/'manifest.json').read_text())['manifest']
items={v['path']:v for v in manifest['inventory']}
import hashlib,stat
restored_modes=set()
for name in audit['epoch_skill_paths']:
    p=pathlib.Path(os.environ['WORKSPACE'])/'.hermes'/name
    assert p.is_file() and hashlib.sha256(p.read_bytes()).hexdigest()==items[name]['sha256']
    mode=stat.S_IMODE(p.stat().st_mode);assert mode&0o7022==0;restored_modes.add(mode)
cfg=json.loads((pathlib.Path(os.environ['WORKSPACE'])/'.hermes/config.yaml').read_text())
assert cfg['skills']['write_approval'] and cfg['memory']['write_approval'] and not cfg['curator']['enabled']
# Switching HERMES_HOME for import must not move the protected LOOM-installed
# boundary. This original external skill stays readable and immutable.
installed=receipt/'morathustra/skills/installed/search-loom-docs/SKILL.md'
assert 'Search LOOM Docs' in installed.read_text()
for mutation in [lambda:installed.chmod(0o644),lambda:installed.write_text('denied')]:
    try:mutation()
    except PermissionError:pass
    else:raise AssertionError('restore lost installed-skill protection')
memory=MemoryStore();memory.load_from_disk();assert 'slice3-memory-cobalt' in str(memory.memory_entries)
skill=skill_view('slice3-created-skill');assert 'slice3-skill-amber' in skill
count=int((pathlib.Path(os.environ['RECEIPTS'])/'wal-count.txt').read_text());assert count>0
print(json.dumps({'session_search':True,'wal_session_search':True,'memory':True,'created_skill':True,'wal_writes':count,'native_restore':True,'gateway_tui_cross_search':True,'bundled_skills':len(audit['epoch_skill_paths']),'native_import_modes':sorted(restored_modes),'restored_approval_gates':True,'installed_still_read_only':True}))
PY
RECEIPTS="$RECEIPTS" python3 "$RECEIPTS/check-host.py" after >"$RECEIPTS/host-post-state.json"
printf '%s\n' 'Morathustra disposable runtime/query/interruption/cloud recovery acceptance passed.'
