#!/usr/bin/env python3
"""Build in an owned pinned checkout; never copy to a vault or download code."""
from pathlib import Path
import subprocess,sys,os,json,hashlib
root=Path(sys.argv[1]).resolve()
here=Path(__file__).resolve().parent
if os.environ.get('PATHS_TEST_INSTALL') or any(root.glob('.env*')):
    sys.exit('Refusing install environment or dotenv files')
subprocess.run([sys.executable,str(here/'apply.py'),str(root)],check=True)
subprocess.run(['npm','run','tsc-check'],cwd=root,check=True)
subprocess.run(['npm','run','test:unit','--','src/loomClient/control.unit.spec.ts','src/loomClient/client.unit.spec.ts','src/loomClient/publisher.unit.spec.ts','src/loomClient/work.unit.spec.ts','src/loomClient/journal.unit.spec.ts','src/loomClient/namespace.unit.spec.ts'],cwd=root,check=True)
subprocess.run(['npx','--no-install','vite','build','--mode','original'],cwd=root,check=True)
files=['main.js','manifest.json','styles.css']
receipt={'upstream':'7b3b6ff854f2ff10452be3eaf8e0cd76ac65ac2b','patchManifestSha256':hashlib.sha256((here/'patch-manifest.json').read_bytes()).hexdigest(),'node':subprocess.check_output(['node','--version'],text=True).strip(),'files':{f:hashlib.sha256((root/f).read_bytes()).hexdigest() for f in files},'installed':False}
(root/'loom-client-build.json').write_text(json.dumps(receipt,indent=2)+'\n')
print('Local bundle and receipt ready. No installation performed.')
