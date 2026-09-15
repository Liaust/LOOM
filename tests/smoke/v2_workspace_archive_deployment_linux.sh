#!/usr/bin/env bash
set -euo pipefail

# Only rendered activation functions and tiny self-owned fixtures are executed.
[[ $(uname -s) == Linux && $(id -u) == 0 && $# == 1 ]] || {
  echo 'Requires Linux/root and one rendered loomOrcaAccess text file' >&2
  exit 1
}
for tool in python3 bash find setfacl getfacl readlink stat setpriv; do
  command -v "$tool" >/dev/null
done
fixture=$(mktemp -d /tmp/loom-workspace-acl.XXXXXX)
identity=$(stat -c '%d:%i' "$fixture")
cleanup() {
  [[ -d $fixture && ! -L $fixture && $(stat -c '%d:%i' "$fixture") == "$identity" ]] || return 1
  rm -rf --one-file-system -- "$fixture"
  [[ ! -e $fixture && ! -L $fixture ]]
}
trap cleanup EXIT
python3 - "$1" "$fixture" <<'PY'
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys

rendered = Path(sys.argv[1]).read_text()
root = Path(sys.argv[2])
os.chmod(root, 0o755)
box = root / 'box'
storage = root / 'storage'
archive = storage / 'archive'
names = ('grant_agent_read_write', 'grant_agent_read_only', 'protect_workspace_archive_categories')
functions = []
for name in names:
    matches = list(re.finditer(r'(?m)^( *)' + name + r'\(\) \{\n', rendered))
    assert len(matches) == 1, name
    start = matches[0]
    end = re.search(r'(?m)^' + start.group(1) + r'\}\s*$', rendered[start.end():])
    assert end, name
    functions.append(rendered[start.start():start.end() + end.end()])
script = '\n'.join(functions)

# Substitute only executable locations, configured roots and fixture identities.
def executable(match):
    name = match.group(1)
    assert name in ('find', 'setfacl', 'readlink', 'stat'), name
    return shutil.which(name)
script = re.sub(r'/nix/store/[^/\s]+/bin/([a-z]+)', executable, script)
script = script.replace('/srv/loom/box', str(box)).replace('/srv/loom/storage', str(storage))
script = script.replace('/var/lib/loom', str(root / 'state'))
script = script.replace('u:agents:', 'u:65534:').replace('u:loom:', 'u:root:')
script = script.replace("'loom'", "'root'").replace('!= loom ]', '!= root ]')
assert '/srv/loom' not in script and '/var/lib/loom' not in script and '/nix/store/' not in script
assert 'u:agents:' not in script and 'u:loom:' not in script

def command(*args, check=True):
    return subprocess.run(args, check=check, capture_output=True, text=True)

def activate(check=True):
    invocation = script + '\n' + f'grant_agent_read_write {box}\ngrant_agent_read_only {storage}\nprotect_workspace_archive_categories\n'
    return command('bash', '-euo', 'pipefail', '-c', invocation, check=check)

def snapshot(path):
    result = {}
    for entry in [path] + sorted(path.rglob('*')):
        s = entry.lstat()
        result[str(entry.relative_to(path))] = {
            'identity': (s.st_dev, s.st_ino, s.st_uid, s.st_gid, s.st_mode, s.st_mtime_ns, s.st_nlink),
            'sha256': hashlib.sha256(entry.read_bytes()).hexdigest() if entry.is_file() else None,
            'xattrs': {key: os.getxattr(entry, key).hex() for key in sorted(os.listxattr(entry))},
        }
    return result

for kind in ('Topics', 'Projects', 'Library'):
    (box / kind).mkdir(parents=True)
    category = archive / kind.lower()
    category.mkdir(parents=True)
    os.chmod(category, 0o2750)
    command('setfacl', '-m', 'u:65534:r-x,d:u:65534:r-x', str(category))

source = box / 'Topics/pilot'
(source / '.loom-acceptance').mkdir(parents=True)
payload = source / '.loom-acceptance/payload.md'
payload.write_bytes(b'only a disposable deployment fidelity fixture\n')
payload_bytes = payload.read_bytes()
os.chmod(payload, 0o640)
for directory in (source, source / '.loom-acceptance'):
    os.chmod(directory, 0o2750)
    command('setfacl', '-m', 'u:65534:rwx,m::r-x,d:u:65534:rwx,d:m::r-x', str(directory))
command('setfacl', '-m', 'u:65534:rw-,m::r--', str(payload))
container = archive / 'topics/pilot'
container.mkdir()
intent = container / '.archive-intent.json'
intent.write_bytes(b'{"fixture":"pending"}\n')
os.chmod(intent, 0o600)
before = snapshot(source)
intent_before = snapshot(intent)
category_before = (archive / 'topics').stat()

for _ in range(2):
    activate()
    assert snapshot(source) == before, 'pending source changed'
    assert snapshot(intent) == intent_before, 'authenticated intent changed'
after = (archive / 'topics').stat()
assert (after.st_dev, after.st_ino, after.st_mode, after.st_mtime_ns) == (
    category_before.st_dev, category_before.st_ino, category_before.st_mode, category_before.st_mtime_ns)
print('PASS pending activation x2: exact source/intent and category binding')

destination = container / 'content'
source.rename(destination)
for _ in range(2):
    activate()
    assert snapshot(destination) == before, 'archived payload changed'
    assert snapshot(intent) == intent_before, 'archived intent changed'
test = 'import os,sys; os.open(sys.argv[1], int(sys.argv[2]))'
for flags in (os.O_RDONLY, os.O_WRONLY):
    attempt = command('setpriv', '--reuid=65534', '--regid=65534', '--clear-groups',
                      'python3', '-c', test, str(destination / '.loom-acceptance/payload.md'), str(flags), check=False)
    assert attempt.returncode != 0 and 'PermissionError' in attempt.stderr, 'nonowner reached archived payload'
assert (destination / '.loom-acceptance/payload.md').read_bytes() == payload_bytes
print('PASS archived activation x2: exact fidelity, nonowner read/write denied, owner read works')

destination.rename(source)
for _ in range(2):
    activate()
    assert snapshot(source) == before, 'restored payload changed'
print('PASS restored activation x2: exact inode/mode/mtime/ACL/hash round trip')

category = archive / 'library'
external = root / 'external'
external.mkdir()
external_before = snapshot(external)
category.rename(archive / 'library.saved')
category.symlink_to(external, target_is_directory=True)
assert activate(check=False).returncode != 0
assert snapshot(external) == external_before, 'symlink target changed'
category.unlink()
(archive / 'library.saved').rename(category)
os.chmod(category, 0o770)
assert activate(check=False).returncode != 0
os.chmod(category, 0o2750)
os.chown(category, 65534, 65534)
assert activate(check=False).returncode != 0
print('PASS invalid category symlink/mode/owner fails closed')
print(json.dumps({'checks': 4, 'payload_bytes': len(payload.read_bytes()), 'production_actions': 0}))
PY
