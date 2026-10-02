#!/usr/bin/env python3
"""Refresh only this adapter in an owned, already pinned upstream checkout."""
from pathlib import Path
import hashlib,json,shutil,sys
here=Path(__file__).resolve().parent
root=Path(sys.argv[1]).resolve()
pin=json.loads((here/'upstream-lock.json').read_text())
if hashlib.sha256((root/'package-lock.json').read_bytes()).hexdigest()!=pin['packageLockSha256']:sys.exit('Wrong upstream lock')
module=root/'src/apps/cli/loom-adapter';module.mkdir(exist_ok=True)
for name in ['adapter.mjs','entrypoint.ts','upstream-lock.json']:shutil.copyfile(here/name,module/name)
# Share the strict client codec and native control transport. Never maintain a
# second JS wire schema or put sensitive fields outside native note encryption.
for name in ['protocol.ts','control.ts']:shutil.copyfile(here.parent/'notes-workspace-client/src'/name,module/name)
(root/'src/apps/cli/entrypoint.ts').write_text('import "./loom-adapter/entrypoint";\n')
print('Pinned adapter refreshed; no install, execution or replication performed.')
