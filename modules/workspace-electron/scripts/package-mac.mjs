import {packager} from '@electron/packager';
import {cp,mkdir,readFile,writeFile,lstat} from 'node:fs/promises';
import {fileURLToPath} from 'node:url';
import path from 'node:path';
import {execFileSync} from 'node:child_process';
import {buildIcons} from './build-icons.mjs';

const root=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'..');
if(process.platform!=='darwin')throw Error('Local macOS packaging must run on the Mac.');
const release=process.argv.includes('--release');
const identity=process.env.WORKSPACE_SIGN_IDENTITY,profile=process.env.WORKSPACE_NOTARY_PROFILE,releaseVersion=process.env.WORKSPACE_RELEASE_VERSION;
if(release){
  if(!identity?.startsWith('Developer ID Application:')||!profile||!/^\d+\.\d+\.\d+$/.test(releaseVersion||''))throw Error('Signed release requires WORKSPACE_SIGN_IDENTITY, WORKSPACE_NOTARY_PROFILE and WORKSPACE_RELEASE_VERSION. No unsigned fallback.');
  if(execFileSync('git',['status','--porcelain','--','.','../../cmd/loom-workspace-fs','../../internal/notesworkspace'],{cwd:root,encoding:'utf8'}).trim())throw Error('Signed releases require clean scoped source.');
}
const output=path.resolve(root,'../../.loom-acceptance/workspace-desktop-v1/packages',new Date().toISOString().replace(/[:.]/g,'-'));
const stage=path.join(output,'source');await mkdir(stage,{recursive:true,mode:0o700});
const icon=await buildIcons(path.join(root,'assets/Loom.icon'),path.join(output,'icons'));
const moveHelper=path.join(output,'loom-workspace-fs');
execFileSync('go',['build','-trimpath','-o',moveHelper,'./cmd/loom-workspace-fs'],{cwd:path.resolve(root,'../..'),stdio:'inherit'});
const manifest=JSON.parse(await readFile(path.join(root,'package.json'),'utf8'));
if(release)manifest.version=releaseVersion;
const electronVersion=manifest.devDependencies.electron;
const copy=async(relative,optional=false)=>{
  const source=path.join(root,relative);let info;
  try{info=await lstat(source);}catch(error){if(optional&&error.code==='ENOENT')return;throw error;}
  if(info.isSymbolicLink())throw Error('Packaging refuses a linked source.');
  await cp(source,path.join(stage,relative),{recursive:true,dereference:false,filter:async file=>{if((await lstat(file)).isSymbolicLink())throw Error('Packaging refuses symbolic links.');return true;}});
};
for(const relative of ['src','dist','assets','README.md'])await copy(relative);
const lock=JSON.parse(await readFile(path.join(root,'package-lock.json'),'utf8'));
for(const [relative,dependency] of Object.entries(lock.packages))if(relative.startsWith('node_modules/')&&!dependency.dev&&!dependency.devOptional)await copy(relative,dependency.optional===true);
// The app includes production code only, never the working set, SSH state,
// journals, developer tooling, private receipts or the repository checkout.
delete manifest.devDependencies;delete manifest.overrides;delete manifest.scripts;
await writeFile(path.join(stage,'package.json'),JSON.stringify(manifest,null,2));
const source=execFileSync('git',['rev-parse','HEAD'],{cwd:root,encoding:'utf8'}).trim();
const dirty=Boolean(execFileSync('git',['status','--porcelain','--','.'],{cwd:root,encoding:'utf8'}).trim());
await writeFile(path.join(stage,'build.json'),JSON.stringify({source,dirty,built_at:new Date().toISOString(),kind:release?'signed-notarized':'local-development',userdata:'LOOM Files'}));
const paths=await packager({dir:stage,out:output,name:'Loom',icon,platform:'darwin',arch:process.arch,electronVersion,extraResource:[moveHelper],
  appBundleId:'com.liaust.loom.workspace',appVersion:manifest.version,asar:true,prune:false,overwrite:false,
  osxSign:release?{identity,continueOnError:false,hardenedRuntime:true,entitlements:path.join(root,'assets/entitlements.mac.plist')}:false,
  osxNotarize:release?{keychainProfile:profile}:false});
if(release)for(const p of paths){const appPath=path.join(p,'Loom.app');execFileSync('/usr/bin/codesign',['--verify','--deep','--strict',appPath],{stdio:'inherit'});execFileSync('/usr/bin/xcrun',['stapler','validate',appPath],{stdio:'inherit'});}
console.log(JSON.stringify({applications:paths.map(p=>path.join(p,'Loom.app')),source,dirty,signing:release?'Developer ID signed and notarized':'local development; not notarized',userdata:'LOOM Files'},null,2));
