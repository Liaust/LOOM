import { lstat, mkdir, mkdtemp, link, rename, open, readdir } from 'node:fs/promises';
import path from 'node:path';
import { normalize } from './local-files.mjs';

const identity = info => `${info.dev}:${info.ino}`;
const infoAt = target => lstat(target).catch(error => { if (error.code !== 'ENOENT') throw error; return null; });
async function syncDirectory(directory) {
  const handle = await open(directory, 'r');
  try { await handle.sync(); } finally { await handle.close(); }
}
async function parents(local, relative) {
  const parts = normalize(relative).split('/');
  let current = local.root;
  for (const part of parts.slice(0, -1)) {
    current = path.join(current, part);
    await mkdir(current, { mode: 0o700 }).catch(error => { if (error.code !== 'EEXIST') throw error; });
    const info = await lstat(current);
    if (info.isSymbolicLink() || !info.isDirectory()) throw Error('Unsafe local destination parent.');
  }
  return path.join(current, parts.at(-1));
}

async function retireSource(from, recoveryRoot, operation, checkpoint) {
  const placement=operation.placement;
  if(!placement.retained){
    await mkdir(recoveryRoot,{mode:0o700,recursive:true});
    const info=await lstat(recoveryRoot);
    if(info.isSymbolicLink()||!info.isDirectory())throw Error('Unsafe local recovery directory.');
    placement.retained=path.join(await mkdtemp(path.join(recoveryRoot,'path-')),'original');
    await checkpoint();
  }
  const relative=path.relative(recoveryRoot,placement.retained);
  if(relative.startsWith('..')||path.isAbsolute(relative)||path.basename(placement.retained)!=='original')throw Error('Invalid local recovery path.');
  const retained=await infoAt(placement.retained),source=await infoAt(from);
  if(retained){
    if(retained.isSymbolicLink()||identity(retained)!==placement.identity||source)throw Error('Local recovery placement is ambiguous; all entries were preserved.');
  }else{
    if(!source)throw Error('The local source is missing and its recovery placement is unverified.');
    // Capture the actual inode atomically instead of stat-then-unlink, which
    // could erase a replacement saved by an external editor between checks.
    await rename(from,placement.retained);
    const captured=await lstat(placement.retained);
    if(captured.isSymbolicLink()||identity(captured)!==placement.identity){
      if(captured.isFile())await link(placement.retained,from).catch(error=>{if(error.code!=='EEXIST')throw error;});
      throw Error('A concurrent local replacement was retained. No competing file was deleted.');
    }
  }
  await syncDirectory(path.dirname(placement.retained));
  await syncDirectory(path.dirname(from));
}

// Only reconcile an already accepted Main operation. Checkpointed inode identity
// distinguishes a lost local acknowledgement from an unrelated destination.
export async function placeAcceptedPath(local, recoveryRoot, operation, checkpoint) {
  const placement = operation.placement;
  if (!operation.mainFirst || operation.state !== 'accepted' || !placement) throw Error('Main acceptance is required before local placement.');
  if (placement.stage === 'done') return;
  const from = await local.resolve(placement.from).catch(error => { if (error.code !== 'ENOENT') throw error; return path.join(local.root, normalize(placement.from)); });
  const source = await infoAt(from);
  if(placement.external){
    if(source)throw Error('A new local file occupies the previous path. It was preserved.');
    if(operation.kind==='rename'){
      const to=await local.resolve(placement.to),destination=await lstat(to);
      if(destination.isSymbolicLink()||!destination.isFile()||identity(destination)!==placement.identity||(await local.read(placement.to)).base!==operation.base?.hash?.replace(/^sha256:/,''))throw Error('The externally moved file changed. Main accepted; local recovery needs review.');
    }
    placement.stage='done';await checkpoint();return;
  }
  if (!placement.identity) {
    if (!source || source.isSymbolicLink() || (operation.directory ? !source.isDirectory() : !source.isFile())) throw Error('The local source changed. Main accepted; local files need review.');
    placement.identity = identity(source);
    await checkpoint();
  }
  if (source && (source.isSymbolicLink() || identity(source) !== placement.identity)) throw Error('A different local file now occupies the old path. It was preserved.');

  if (operation.kind === 'rename' && !operation.directory) {
    const to = await parents(local, placement.to);
    const destination = await infoAt(to);
    if (destination) {
      if (destination.isSymbolicLink() || identity(destination) !== placement.identity) throw Error('Main accepted the move, but its local destination is occupied. Both files are preserved.');
    } else {
      if (!source) throw Error('The local move source and destination are missing; retained operation content is available.');
      await link(from, to); // Atomic no-replace; never overwrite a destination.
    }
    const published=await lstat(to);
    if(published.isSymbolicLink()||identity(published)!==placement.identity)throw Error('The local destination changed during placement. All versions remain preserved.');
    await syncDirectory(path.dirname(to));
    placement.stage = 'published';
    await checkpoint();
    await retireSource(from,recoveryRoot,operation,checkpoint);
  } else {
    if (operation.kind === 'rename') {
      const to = await parents(local, placement.to);
      let destination = await infoAt(to);
      if (!destination) {
        placement.stage = 'creating_directory';
        await checkpoint();
        await mkdir(to, { mode: 0o700 });
        destination = await lstat(to);
        placement.destinationIdentity = identity(destination);
        await checkpoint();
      }
      if (!placement.destinationIdentity && placement.stage === 'creating_directory' && !destination.isSymbolicLink() && destination.isDirectory() && !(await readdir(to)).length) {
        // Main already owns the canonical target. An interrupted creation may
        // reuse an empty local placeholder, never replace an occupied folder.
        placement.destinationIdentity = identity(destination);
        await checkpoint();
      }
      if (destination.isSymbolicLink() || !destination.isDirectory() || identity(destination) !== placement.destinationIdentity) throw Error('The local destination folder needs review; no folder was overwritten.');
      await syncDirectory(path.dirname(to));
    }
    await retireSource(from,recoveryRoot,operation,checkpoint);
  }
  placement.stage = 'done';
  delete placement.error;
  await checkpoint();
}
