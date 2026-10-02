#!/usr/bin/env python3
"""Apply the pinned overlay only to an owned upstream build directory."""
from pathlib import Path
import hashlib
import json
import subprocess
import sys

here = Path(__file__).resolve().parent
root = Path(sys.argv[1]).resolve()
manifest = json.loads((here / 'patch-manifest.json').read_text())
sha = lambda p: hashlib.sha256(p.read_bytes()).hexdigest()
if (root / '.env').exists(): sys.exit('Refusing build directory with .env')
if sha(root / 'package-lock.json') != '70c3f4c80bb6bcc525df84b282fa9e1191058a174a056de6e5ec8038d1377850': sys.exit('Wrong upstream lock')
if json.loads((root / 'manifest.json').read_text())['version'] != '1.0.32': sys.exit('Wrong plugin version')
if sha(here/'patches/native-hooks.patch') != manifest['patchSha256']: sys.exit('Patch integrity mismatch')
receipt = root / '.loom-client-overlay.json'
previous = json.loads(receipt.read_text()) if receipt.exists() else {'files':{}}
originals = root / '.loom-client-originals'
# Preflight every target before changing any source; refuse unknown edits.
for target in manifest['targets']:
    p = root / target['path']; actual = sha(p)
    if actual not in [target['before'], previous['files'].get(target['path'])]: sys.exit('Unexpected edit: '+str(p))
    backup = originals / target['path']
    if backup.exists() and sha(backup) != target['before']: sys.exit('Original integrity mismatch')
for name, expected in manifest['overlay'].items():
    if sha(here/'src'/name) != expected: sys.exit('Overlay integrity mismatch')
    dest = root/'src/loomClient'/name
    if dest.exists() and sha(dest) not in [expected, previous['files'].get('src/loomClient/'+name)]: sys.exit('Unexpected overlay edit')
for target in manifest['targets']:
    p = root / target['path']; backup = originals / target['path']
    if not backup.exists(): backup.parent.mkdir(parents=True,exist_ok=True); backup.write_bytes(p.read_bytes())
    p.write_bytes(backup.read_bytes())
subprocess.run(['git','apply','--check',str(here/'patches/native-hooks.patch')],cwd=root,check=True)
subprocess.run(['git','apply',str(here/'patches/native-hooks.patch')],cwd=root,check=True)
files={}
for target in manifest['targets']:
    if sha(root/target['path']) != target['after']: sys.exit('Applied patch mismatch')
    files[target['path']]=target['after']
for name,expected in manifest['overlay'].items():
    dest=root/'src/loomClient'/name;dest.parent.mkdir(parents=True,exist_ok=True);dest.write_bytes((here/'src'/name).read_bytes());files['src/loomClient/'+name]=expected
receipt.write_text(json.dumps({'files':files,'patchSha256':manifest['patchSha256']},indent=2)+'\n')
print('Verified overlay applied; no build or installation performed.')
