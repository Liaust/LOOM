#!/usr/bin/env python3
"""Fetch verified source into a NEW build directory; never installs or executes it."""
import hashlib
import io
import json
from pathlib import Path
import subprocess
import sys
import tarfile
import urllib.request

here = Path(__file__).resolve().parent
pin = json.loads((here / 'upstream-lock.json').read_text())
if len(sys.argv) != 2:
    sys.exit('Usage: python3 prepare.py NEW_BUILD_DIRECTORY (Python >=3.12)')
dest = Path(sys.argv[1]).resolve()
if dest.exists():
    sys.exit('Build directory already exists; choose a fresh owned directory.')
with urllib.request.urlopen(pin['archiveUrl'], timeout=60) as response:
    payload = response.read(32 * 1024 * 1024 + 1)
if len(payload) > 32 * 1024 * 1024 or hashlib.sha256(payload).hexdigest() != pin['archiveSha256']:
    sys.exit('Upstream archive integrity mismatch')
dest.mkdir(parents=True)
with tarfile.open(fileobj=io.BytesIO(payload)) as archive:
    archive.extractall(dest, filter='data')
source = dest / ('obsidian-livesync-' + pin['revision'])
lock_bytes = (source / 'package-lock.json').read_bytes()
if hashlib.sha256(lock_bytes).hexdigest() != pin['packageLockSha256']:
    sys.exit('Upstream package lock integrity mismatch')
locked = json.loads(lock_bytes)['packages']['node_modules/@vrtmrz/livesync-commonlib']
if locked['version'] != pin['commonlib'] or locked['integrity'] != pin['commonlibIntegrity']:
    sys.exit('Commonlib pin mismatch')
subprocess.run([sys.executable,str(here/'overlay.py'),str(source)],check=True)
print(source)
