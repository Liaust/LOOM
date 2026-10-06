#!/usr/bin/env python3
"""Build in an owned pinned checkout; never copy to a vault or download code."""
from pathlib import Path
import subprocess,sys,os,json,hashlib,argparse,re
parser=argparse.ArgumentParser(description=__doc__)
parser.add_argument('build',type=Path)
parser.add_argument('--bundle-version',default='0.0.0-dev')
parser.add_argument('--sequence',type=int,default=0)
parser.add_argument('--source-commit')
args=parser.parse_args()
root=args.build.resolve()
here=Path(__file__).resolve().parent
if args.sequence < 0 or args.sequence > 2**53-1 or (args.sequence and not re.fullmatch(r'(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)',args.bundle_version)):
    parser.error('stable bundles need a semantic version and positive sequence')
head=subprocess.check_output(['git','rev-parse','HEAD'],cwd=here,text=True).strip()
source=args.source_commit or head
if not re.fullmatch(r'[a-f0-9]{40}',source): parser.error('exact source commit required')
if source != head: parser.error('build from the intended source checkout; do not relabel another source commit')
if args.sequence:
    repo=subprocess.check_output(['git','rev-parse','--show-toplevel'],cwd=here,text=True).strip()
    owners=['modules/notes-workspace-client','modules/client-updates']
    changed=subprocess.run(['git','diff','--quiet',head,'--',*owners],cwd=repo).returncode
    untracked=subprocess.check_output(['git','ls-files','--others','--exclude-standard','--',*owners],cwd=repo,text=True).strip()
    if changed or untracked: parser.error('commit client/package source before creating a stable identity; unrelated dirty files are allowed')
if os.environ.get('PATHS_TEST_INSTALL') or any(root.glob('.env*')):
    sys.exit('Refusing install environment or dotenv files')
subprocess.run([sys.executable,str(here/'apply.py'),str(root)],check=True)
release={'schema':'loom.client-bundle.v1','component':'notes-workspace','adapter':'obsidian-plugin','pluginId':'obsidian-livesync','releaseId':'client-notes-'+args.bundle_version,'sequence':args.sequence,'version':args.bundle_version,'channel':'stable' if args.sequence else 'development','sourceCommit':source,'upstream':{'revision':'7b3b6ff854f2ff10452be3eaf8e0cd76ac65ac2b','version':'1.0.32'},'compatibility':{'minAppVersion':'1.7.2','platforms':['desktop','mobile'],'notesProtocol':1,'handoffAPI':1,'journalSchema':1,'activation':'reload','rollback':'same-schema'}}
release_json=json.dumps(release,indent=2)+'\n'
(root/'loom-release.json').write_text(release_json)
(root/'src/loomClient/release.json').write_text(release_json)
subprocess.run(['npm','run','tsc-check'],cwd=root,check=True)
subprocess.run(['npm','run','test:unit','--','src/loomClient/control.unit.spec.ts','src/loomClient/client.unit.spec.ts','src/loomClient/publisher.unit.spec.ts','src/loomClient/work.unit.spec.ts','src/loomClient/journal.unit.spec.ts','src/loomClient/namespace.unit.spec.ts','src/loomClient/update.unit.spec.ts'],cwd=root,check=True)
subprocess.run(['npx','--no-install','vite','build','--mode','original'],cwd=root,check=True)
files=['main.js','manifest.json','styles.css','loom-release.json']
receipt={'upstream':'7b3b6ff854f2ff10452be3eaf8e0cd76ac65ac2b','patchManifestSha256':hashlib.sha256((here/'patch-manifest.json').read_bytes()).hexdigest(),'node':subprocess.check_output(['node','--version'],text=True).strip(),'files':{f:hashlib.sha256((root/f).read_bytes()).hexdigest() for f in files},'release':release,'installed':False}
(root/'loom-client-build.json').write_text(json.dumps(receipt,indent=2)+'\n')
print('Local bundle and receipt ready. No installation performed.')
