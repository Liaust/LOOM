import {lstat,realpath,readFile,writeFile,rename,mkdir,open} from 'node:fs/promises';
import path from 'node:path';

export const locationFile='native-client/working-set-location.json';
const absent=error=>{if(error.code!=='ENOENT')throw error;return null;};
const identity=info=>`${info.dev}:${info.ino}`;
async function exactDirectory(root){
  const info=await lstat(root);
  if(!info.isDirectory()||info.isSymbolicLink()||await realpath(root)!==root)throw Error('Replica root must be a real directory, not a link.');
  return info;
}
async function persist(file,value){
  const temporary=`${file}.tmp`,handle=await open(temporary,'w',0o600);
  try{await handle.writeFile(JSON.stringify(value)+'\n');await handle.sync();}finally{await handle.close();}
  await rename(temporary,file);const parent=await open(path.dirname(file),'r');try{await parent.sync();}finally{await parent.close();}
}

// Called only while the existing native lifecycle lock is held, before SQLite
// materialization or outbox replay. Relative record paths never change.
export async function relocateWorkingRoot(userData,destination,{home,move}){
  const previous=path.join(userData,'native-client/working-set'),locator=path.join(userData,locationFile);
  if(!path.isAbsolute(destination)||path.dirname(destination)!==home||destination===userData)throw Error('Choose the dedicated Main replica root directly under Home.');
  await exactDirectory(home);await mkdir(path.dirname(locator),{recursive:true,mode:0o700});
  const locatorInfo=await lstat(locator).catch(absent);if(locatorInfo&&(locatorInfo.isSymbolicLink()||!locatorInfo.isFile()||locatorInfo.size>8192))throw Error('Unsafe replica root locator.');
  let receipt=await readFile(locator,'utf8').then(JSON.parse).catch(absent);
  if(receipt&&(receipt.schema!=='loom.working_root.v1'||receipt.source_root!==userData||receipt.previous!==previous||receipt.path!==destination||!['prepared','ready'].includes(receipt.state)))throw Error('Replica root handover needs review. No files were moved.');
  let source=await lstat(previous).catch(absent),target=await lstat(destination).catch(absent);
  if(!receipt){
    if(target)throw Error('The Main replica destination already exists. No folders were merged.');
    if(!source){await mkdir(previous,{mode:0o700});source=await lstat(previous);}
    await exactDirectory(previous);
    receipt={schema:'loom.working_root.v1',source_root:userData,previous,path:destination,identity:identity(source),state:'prepared'};
    await persist(locator,receipt);
  }
  if(receipt.state==='prepared'){
    if(source){
      if(identity(source)!==receipt.identity||target)throw Error('Replica root changed during handover. Existing files are preserved.');
      await move(home,path.relative(home,previous),path.relative(home,destination),receipt.identity);
    }
    target=await exactDirectory(destination);
    if(identity(target)!==receipt.identity)throw Error('Moved replica root identity differs. No replay was started.');
    receipt={...receipt,state:'ready'};await persist(locator,receipt);
  }
  target=await exactDirectory(destination);
  if(identity(target)!==receipt.identity)throw Error('The Main replica root was replaced. Pending edits remain in private state.');
  if((await lstat(previous).catch(absent))?.isDirectory())throw Error('A second replica tree exists at the old root. No trees were merged.');
  // An old app must not silently recreate a second writable working-set tree.
  if(!await lstat(previous).catch(absent))await writeFile(previous,'Main replicas moved; see working-set-location.json.\n',{flag:'wx',mode:0o600});
  return destination;
}

export async function recoveryWorkingRoot(root,{mapped=true}={}){
  if(!mapped)return path.join(root,'native-client/working-set');
  const info=await lstat(path.join(root,locationFile)).catch(absent);if(info&&(info.isSymbolicLink()||!info.isFile()||info.size>8192))throw Error('Unsafe recovery replica locator.');
  const receipt=await readFile(path.join(root,locationFile),'utf8').then(JSON.parse).catch(absent);
  if(!receipt||!mapped)return path.join(root,'native-client/working-set');
  if(receipt.schema!=='loom.working_root.v1'||receipt.source_root!==root||receipt.state!=='ready'||!path.isAbsolute(receipt.path))throw Error('Replica root recovery handover is incomplete.');
  const actual=await exactDirectory(receipt.path);
  if(identity(actual)!==receipt.identity)throw Error('Replica root recovery identity differs.');
  return receipt.path;
}
