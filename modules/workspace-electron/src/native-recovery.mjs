import { spawn } from 'node:child_process';
import { backup, DatabaseSync } from 'node:sqlite';
import { createServer } from 'node:http';
import { mkdir, mkdtemp, lstat, readdir, open, copyFile, chmod, writeFile, rm, unlink } from 'node:fs/promises';
import { constants, createReadStream } from 'node:fs';
import { createHash } from 'node:crypto';
import { pipeline } from 'node:stream/promises';
import path from 'node:path';
import os from 'node:os';
import {recoveryWorkingRoot,locationFile} from './working-root.mjs';

const paths=['native-client/working-set','native-client/local-recovery','drafts.json','roots.json','boxes.json',locationFile];
const MAX_BYTES=16*1024**3;
const absent=e=>{if(e.code!=='ENOENT')throw e;return null;};

export async function nativeInventory(root,limit=MAX_BYTES,deadline=Infinity,{mapped=true}={}){
  const files=[],directories=[];let bytes=0,entries=0;
  const workingRoot=await recoveryWorkingRoot(root,{mapped});
  const physical=relative=>relative==='native-client/working-set'||relative.startsWith('native-client/working-set/')?path.join(workingRoot,relative.slice('native-client/working-set'.length)):path.join(root,relative);
  async function visit(relative){
    if(Date.now()>deadline)throw Error('Native recovery timed out.');
    const target=physical(relative),info=await lstat(target).catch(absent);if(!info)return;
    if(++entries>200000||info.isSymbolicLink()||!info.isDirectory()&&!info.isFile())throw Error('Unsafe native recovery entry.');
    if(info.isDirectory()){
      directories.push({path:relative,mode:info.mode&0o777});
      for(const name of (await readdir(target)).sort())await visit(`${relative}/${name}`);
    }else{
      bytes+=info.size;if(bytes>limit)throw Error('Native recovery exceeds its byte budget.');
      const hash=createHash('sha256'),handle=await open(target,constants.O_RDONLY|constants.O_NOFOLLOW);
      try{const before=await handle.stat();for await(const chunk of handle.createReadStream({autoClose:false})){hash.update(chunk);if(Date.now()>deadline)throw Error('Native recovery timed out.');}
        const after=await handle.stat();if(before.ino!==info.ino||before.dev!==info.dev||after.size!==info.size||after.mtimeMs!==info.mtimeMs)throw Error('A native file changed during capture.');
      }finally{await handle.close();}
      files.push({path:relative,size:info.size,sha256:hash.digest('hex'),mode:info.mode&0o777});
    }
  }
  for(const name of paths)await visit(name);
  return {files,directories,bytes};
}

async function execute(file,args,options={}){
  const child=spawn(file,args,{stdio:['ignore','ignore','pipe'],...options});let detail='';child.stderr.on('data',chunk=>{detail=(detail+chunk).slice(-500);});
  await new Promise((resolve,reject)=>{child.on('error',reject);child.on('exit',code=>code===0?resolve():reject(Error(`Recovery archiver failed (${code}): ${detail}`)));});
}

export async function acquireNativeOwner(helper,root){
  const child=spawn(helper,['hold',root],{stdio:['pipe','pipe','pipe']});
  await new Promise((resolve,reject)=>{
    const timer=setTimeout(()=>{child.kill();reject(Error('Native state lock did not respond.'));},5000);let data='';
    const fail=error=>{clearTimeout(timer);reject(error);};child.once('error',fail);child.once('exit',code=>fail(Error(`Native state lock exited (${code}).`)));
    child.stdout.on('data',chunk=>{data+=chunk;if(data.length>4096)return fail(Error('Invalid native lock response.'));if(data.includes('\n')){try{if(!JSON.parse(data.split('\n')[0]).ok)throw Error('Native state is already owned.');clearTimeout(timer);resolve();}catch(error){fail(error);}}});
  });
  return {child,async close(){child.stdin.end();await new Promise(resolve=>{if(child.exitCode!==null)return resolve();child.once('exit',resolve);});}};
}

// The caller fences every native writer before this function touches its state.
// External editors are not fenced: exact before/after inventories detect those
// races and refuse the capture rather than certify mismatched bytes.
export async function stageNativeRecovery(root,db,{limit=MAX_BYTES,timeout=120}={}){
  const deadline=Date.now()+timeout*1000,temporary=await mkdtemp(path.join(os.tmpdir(),'loom-workspace-recovery-')),workspace=path.join(temporary,'workspace');
  try{
    await mkdir(workspace,{mode:0o700});
    const before=await nativeInventory(root,limit,deadline);
    await mkdir(path.join(workspace,'native-client'),{mode:0o700});
    await backup(db,path.join(workspace,'native-client/notes.sqlite'),{rate:128,progress:()=>{if(Date.now()>deadline)throw Error('Native SQLite capture timed out.');}});
    const database=path.join(workspace,'native-client/notes.sqlite');
    await chmod(database,0o600);const checked=new DatabaseSync(database);try{checked.exec('PRAGMA journal_mode=DELETE');if(checked.prepare('PRAGMA integrity_check').get().integrity_check!=='ok')throw Error('Native SQLite capture failed integrity check.');}finally{checked.close();}
    if(before.bytes+(await lstat(database)).size>limit)throw Error('Native database exceeds capture budget.');
    for(const entry of before.directories){await mkdir(path.join(workspace,entry.path),{recursive:true,mode:entry.mode});await chmod(path.join(workspace,entry.path),entry.mode);}
    const workingRoot=await recoveryWorkingRoot(root);
    for(const entry of before.files){await mkdir(path.dirname(path.join(workspace,entry.path)),{recursive:true,mode:0o700});const source=entry.path.startsWith('native-client/working-set/')?path.join(workingRoot,entry.path.slice('native-client/working-set/'.length)):path.join(root,entry.path);await copyFile(source,path.join(workspace,entry.path),constants.COPYFILE_EXCL);await chmod(path.join(workspace,entry.path),entry.mode);}
    const after=await nativeInventory(root,limit,deadline),copied=await nativeInventory(workspace,limit,deadline,{mapped:false});
    if(JSON.stringify(before)!==JSON.stringify(after)||JSON.stringify(before)!==JSON.stringify(copied))throw Error('Native files changed during recovery capture.');
    const hash=createHash('sha256');for await(const chunk of createReadStream(database))hash.update(chunk);
    const receipt={schema:'loom.workspace_client_recovery.v1',mode:'running',captured_at:new Date().toISOString(),cache_included:false,source_root:root,
      directories:['native-client',...before.directories.map(entry=>entry.path)].sort(),files:[...before.files,{path:'native-client/notes.sqlite',size:(await lstat(database)).size,sha256:hash.digest('hex')}]};
    await writeFile(path.join(workspace,'capture.json'),JSON.stringify(receipt)+'\n',{mode:0o600});
    return {temporary,workspace,receipt,async archive(){
      const archive=path.join(temporary,'capture.tar.gz');await execute('/usr/bin/tar',['-czf',archive,'-C',temporary,'workspace'],{env:{...process.env,COPYFILE_DISABLE:'1'},timeout:Math.max(1000,deadline-Date.now())});return archive;
    }};
  }catch(error){await rm(temporary,{recursive:true,force:true});throw error;}
}

export async function startNativeRecovery(root,{capture}){
  const socket=path.join(root,'native-recovery.sock');
  const previous=await lstat(socket).catch(absent);
  if(previous&&!previous.isSocket())throw Error('Unsafe native recovery endpoint.');
  if(previous)await unlink(socket);let busy=false;
  const server=createServer(async(req,res)=>{
    if(req.method!=='POST'||req.url!=='/capture'){res.writeHead(404).end();return;}
    if(busy){res.writeHead(409).end('Native capture already active.');return;}
    busy=true;let stage;
    try{
      let body='';for await(const chunk of req){body+=chunk;if(body.length>4096)throw Error('Invalid capture request.');}
      const input=JSON.parse(body),limit=input.max_bytes,timeout=input.timeout_seconds;
      if(!Number.isSafeInteger(limit)||limit<1||limit>MAX_BYTES||!Number.isInteger(timeout)||timeout<1||timeout>900)throw Error('Invalid capture budget.');
      stage=await capture({limit,timeout});const file=await stage.archive();
      res.writeHead(200,{'Content-Type':'application/gzip','Cache-Control':'no-store','Content-Length':(await lstat(file)).size});
      await pipeline(createReadStream(file),res);
    }catch(error){if(!res.headersSent)res.writeHead(503).end('Native capture unavailable.');else res.destroy(error);}
    finally{if(stage)await rm(stage.temporary,{recursive:true,force:true});busy=false;}
  });
  server.requestTimeout=900000;server.headersTimeout=10000;
  await new Promise((resolve,reject)=>{server.once('error',reject);server.listen(socket,resolve);});await chmod(socket,0o600);
  return {async close(){await new Promise(resolve=>server.close(resolve));await unlink(socket).catch(absent);}};
}
