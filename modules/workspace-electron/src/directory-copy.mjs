import {spawn} from 'node:child_process';
import {mkdtemp,lstat,realpath,rm} from 'node:fs/promises';
import path from 'node:path';
import {randomUUID} from 'node:crypto';
import {exportDirectorySnapshot} from './project-export.mjs';

export async function importWorkingCopy(helper,root,request){
 return new Promise((resolve,reject)=>{
  const child=spawn(helper,['import',root],{stdio:['pipe','pipe','pipe']});let output='',error='';
  const timer=setTimeout(()=>child.kill(),180000);
  child.on('error',e=>{clearTimeout(timer);reject(e);});child.stdin.on('error',()=>{});
  child.stdout.on('data',chunk=>{output+=chunk;if(output.length>16384)child.kill();});child.stderr.on('data',chunk=>error=(error+chunk).slice(-2000));
  child.on('close',code=>{clearTimeout(timer);try{const value=JSON.parse(output);if(code||!value.ok)throw Error(value.error||error||'Working copy outcome needs review. No original source was changed.');if(value.id!==request.id||value.target!==request.target||value.published!==true||value.uncertain!==false||typeof value.directory_identity!=='string'||!/^\d+:\d+$/.test(value.directory_identity)||!Number.isSafeInteger(value.members)||value.members<0||value.members>10000||!Number.isSafeInteger(value.payload_bytes)||value.payload_bytes<0||value.payload_bytes>512*1024**2||value.symlink_policy!=='reject')throw Error('The native copy receipt does not match this request. Preserve it for review.');resolve(value);}catch(e){reject(e);}});
  child.stdin.end(JSON.stringify(request));
 });
}

// Download and import are explicit foreground actions, never folder selections.
// A private download is disposable; a possibly published destination is not.
export async function makeDirectoryCopy({remote,helper,root,rootIdentity,target,source,planHash,temporaryRoot,verifyDestination,id=randomUUID(),prepared=()=>{},importer=importWorkingCopy}){
 const parent=await realpath(temporaryRoot),stage=await mkdtemp(path.join(parent,'working-copy-')),identity=await lstat(stage);
 let importing=false,complete=false;
 try{
  await verifyDestination();
  const archive=await exportDirectorySnapshot(remote,{source,planHash,output:path.join(stage,'snapshot.tar')});
  await verifyDestination();
  const request={id,root_identity:rootIdentity,target,archive:archive.path,archive_sha256:archive.summary.archive_checksum.slice(7),archive_bytes:archive.summary.archive_bytes};
  await prepared({request,summary:archive.summary});importing=true;
  const result=await importer(helper,root,request);complete=true;
  return {path:path.join(root,target),summary:archive.summary,result};
 }finally{
  const current=await lstat(stage).catch(()=>null);
  if((complete||!importing)&&current?.isDirectory()&&!current.isSymbolicLink()&&current.dev===identity.dev&&current.ino===identity.ino)await rm(stage,{recursive:true});
 }
}
