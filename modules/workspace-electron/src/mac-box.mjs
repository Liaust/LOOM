import {spawn} from 'node:child_process';
import {watch} from 'node:fs';
import {mkdir,lstat,realpath} from 'node:fs/promises';
import path from 'node:path';
import {randomUUID} from 'node:crypto';
import {LocalFiles,normalize,TEXT_LIMIT} from './local-files.mjs';
import {isEditableText} from './file-formats.mjs';

export function localFileAction(helper,root,request){
 return new Promise((resolve,reject)=>{
  const child=spawn(helper,['local',root],{stdio:['pipe','pipe','pipe']});let output='',error='';
  const timer=setTimeout(()=>child.kill(),15000);
  child.on('error',reject);child.stdin.on('error',()=>{});
  child.stdout.on('data',data=>{output+=data;if(output.length>8192)child.kill();});child.stderr.on('data',data=>error=(error+data).slice(-1000));
  child.on('close',code=>{clearTimeout(timer);try{const result=JSON.parse(output);if(code||!result.ok)throw Error(result.error||error||'Local file action did not finish. Your draft is preserved.');resolve(result);}catch(e){reject(e);}});
  child.stdin.end(JSON.stringify(request));
 });
}

export class MacBox extends LocalFiles{
 constructor(root,helper,{db,emit=()=>{}}){super(root);Object.assign(this,{helper,db,emit});this.records=new Map();this.serial=Promise.resolve();this.watchers=new Map();this.timers=new Map();}
 async init(){
  await mkdir(this.root,{mode:0o755}).catch(error=>{if(error.code!=='EEXIST')throw error;});
  const info=await lstat(this.root);if(info.isSymbolicLink()||!info.isDirectory()||await realpath(this.root)!==this.root)throw Error('Mac Box must be a real directory.');
  this.identity=`${info.dev}:${info.ino}`;
  this.db.exec('CREATE TABLE IF NOT EXISTS mac_files(path TEXT PRIMARY KEY,record TEXT NOT NULL);CREATE TABLE IF NOT EXISTS mac_operations(id TEXT PRIMARY KEY,record TEXT NOT NULL);CREATE TABLE IF NOT EXISTS mac_root(identity TEXT PRIMARY KEY);');
  const previous=this.db.prepare('SELECT identity FROM mac_root').get();if(previous&&previous.identity!==this.identity)throw Error('Mac Box identity changed. Local drafts were preserved.');
  this.db.prepare('INSERT OR IGNORE INTO mac_root VALUES(?)').run(this.identity);
  for(const {record} of this.db.prepare('SELECT record FROM mac_files').iterate()){const v=JSON.parse(record);this.records.set(v.path,v);}
  // Only exact inode-proven moves are replayed. Unknown outcomes stay visible
  // as a start-up error instead of reissuing a filesystem mutation.
  for(const {record} of this.db.prepare('SELECT record FROM mac_operations').iterate()){
   const op=JSON.parse(record);if(op.state!=='prepared')continue;
   const source=await this.stat(op.path).catch(e=>{if(e.code!=='ENOENT')throw e;return null;});
   const target=op.target&&await this.stat(op.target).catch(e=>{if(e.code!=='ENOENT')throw e;return null;});
   if(!source&&target&&target.revision.startsWith(op.identity+':'))await this.followMove(op);
   else {op.state='held';op.error='Interrupted Mac operation needs review; current files and drafts preserved.';this.db.prepare('UPDATE mac_operations SET record=? WHERE id=?').run(JSON.stringify(op),op.id);}
  }
  return this;
 }
 remember(v){this.records.set(v.path,v);this.db.prepare('INSERT INTO mac_files VALUES(?,?) ON CONFLICT(path) DO UPDATE SET record=excluded.record').run(v.path,JSON.stringify(v));return v;}
 blocked(relative,except){return this.db.prepare('SELECT record FROM mac_operations').all().some(({record})=>{const op=JSON.parse(record);return op.id!==except&&['prepared','held'].includes(op.state)&&[op.path,op.target].filter(Boolean).some(p=>relative===p||relative.startsWith(p+'/')||p.startsWith(relative+'/'));});}
 async resolve(relative){const info=await lstat(this.root);if(this.identity&&`${info.dev}:${info.ino}`!==this.identity||info.isSymbolicLink())throw Error('Mac Box identity changed.');return super.resolve(relative);}
 async observe(relative){
  if(this.watchers.has(relative))return;
  const file=await this.resolve(relative);
  try{const watcher=watch(file,{persistent:false},(_kind,name)=>{
   clearTimeout(this.timers.get(relative));this.timers.set(relative,setTimeout(()=>{this.timers.delete(relative);void Promise.resolve(this.emit('directory-changed',{root:'mac',path:relative})).catch(()=>{});if(name&&!String(name).startsWith('.'))void Promise.resolve(this.emit('mac-content',{root:'mac',path:path.posix.join(relative,String(name))})).catch(()=>{});},150));
  });watcher.on('error',()=>{watcher.close();this.watchers.delete(relative);});this.watchers.set(relative,watcher);
   if(this.watchers.size>64){const first=this.watchers.keys().next().value;this.watchers.get(first).close();this.watchers.delete(first);}
  }catch{ /* Manual refresh remains available for unsupported native watches. */ }
 }
 async read(relative){const value=await super.read(relative),prior=this.records.get(relative);const record=this.remember({root:'mac',path:relative,id:prior?.id||randomUUID(),text:value.text});await this.observe(path.posix.dirname(relative)==='.'?'':path.posix.dirname(relative));return {...value,binding_id:record.id};}
 async close(){for(const watcher of this.watchers.values())watcher.close();for(const timer of this.timers.values())clearTimeout(timer);this.watchers.clear();this.timers.clear();await this.serial;}
 async save(relative,text,base,binding){return this.exclusive(async()=>{
  if(this.records.get(relative)?.id!==binding)throw Error('Mac file binding changed. Your draft is preserved.');
  if(this.blocked(relative))throw Error('This local path has a retained operation needing review. Your draft is preserved.');
  if(typeof text!=='string'||Buffer.byteLength(text)>TEXT_LIMIT||!isEditableText(relative))throw Error('Unsupported local text save.');
  await localFileAction(this.helper,this.root,{root_identity:this.identity,id:randomUUID(),kind:'save',path:relative,text,base});
  return {...await this.read(relative),pending:false,held:null};
 });}
 exclusive(fn){const task=this.serial.catch(()=>{}).then(fn);this.serial=task.catch(()=>{});return task;}
 async followMove(op){
  for(const [p,watcher]of this.watchers)if(p===op.path||p.startsWith(op.path+'/')){watcher.close();this.watchers.delete(p);clearTimeout(this.timers.get(p));this.timers.delete(p);}
  const affected=[...this.records].filter(([p])=>p===op.path||p.startsWith(op.path+'/'));
  this.db.exec('BEGIN IMMEDIATE');
  try{
   for(const [p,v]of affected){this.db.prepare('DELETE FROM mac_files WHERE path=?').run(p);if(op.target){const next={...v,path:op.target+p.slice(op.path.length)};this.db.prepare('INSERT INTO mac_files VALUES(?,?) ON CONFLICT(path) DO UPDATE SET record=excluded.record').run(next.path,JSON.stringify(next));}}
   this.db.prepare('UPDATE mac_operations SET record=? WHERE id=?').run(JSON.stringify({...op,state:'complete'}),op.id);this.db.exec('COMMIT');
  }catch(error){this.db.exec('ROLLBACK');throw error;}
  for(const [p,v]of affected){this.records.delete(p);if(op.target){const next={...v,path:op.target+p.slice(op.path.length)};this.records.set(next.path,next);}}
  op.state='complete';
  await this.emit('mac-path',{root:'mac',previousPath:op.path,path:op.target||op.path,deleted:!op.target,directory:op.directory});
 }
 async mutate(input){return this.exclusive(async()=>{
  const relative=normalize(input.path),kind=input.operation;
  if(!['create_note','create_folder','rename','trash'].includes(kind))throw Error('Unsupported Mac file action.');
  if(kind.startsWith('create_')){
   if(typeof input.name!=='string'||normalize(input.name)!==input.name||input.name.includes('/')||!input.name)throw Error('Enter one file or folder name.');
   const target=path.posix.join(relative,input.name);
   if(this.blocked(target))throw Error('This path has an unfinished local operation. Its files and drafts are preserved.');
   await this.resolve(relative);await localFileAction(this.helper,this.root,{root_identity:this.identity,id:randomUUID(),kind,path:target,text:input.text||''});
   await this.emit('directory-changed',{root:'mac',path:relative});return {root:'mac',path:target,status:'local'};
  }
  if(!relative)throw Error('Mac Box itself cannot be moved or removed.');
  const source=await this.stat(relative),target=kind==='rename'?normalize(input.target||path.posix.join(path.posix.dirname(relative),input.name||'')):null;
  if(this.blocked(relative)||target&&this.blocked(target))throw Error('This path has an unfinished local operation. Inspect the saved operation first.');
  if(target===relative||target?.startsWith(relative+'/'))throw Error('Choose a different destination outside the source.');
  if(target){const occupied=await this.stat(target).catch(e=>{if(e.code!=='ENOENT')throw e;return null;});if(occupied)throw Error('Destination already exists. No files were moved.');await this.resolve(path.posix.dirname(target)==='.'?'':path.posix.dirname(target));}
  const op={id:randomUUID(),path:relative,target,kind,directory:source.kind==='directory',identity:source.revision.split(':').slice(0,2).join(':'),state:'prepared'};
  this.db.prepare('INSERT INTO mac_operations VALUES(?,?)').run(op.id,JSON.stringify(op));
  try{await localFileAction(this.helper,this.root,{root_identity:this.identity,id:op.id,kind,path:relative,target:target||'',identity:op.identity});}
  catch(error){op.state='held';op.error=error.message;this.db.prepare('UPDATE mac_operations SET record=? WHERE id=?').run(JSON.stringify(op),op.id);throw error;}
  await this.followMove(op);
  for(const p of new Set([path.posix.dirname(relative),target&&path.posix.dirname(target)]))if(p)await this.emit('directory-changed',{root:'mac',path:p==='.'?'':p});
  return {root:'mac',from:relative,path:target||relative,deleted:!target,status:'local'};
 });}
}
