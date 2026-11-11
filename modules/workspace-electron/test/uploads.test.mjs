import test from 'node:test';
import assert from 'node:assert/strict';
import {DatabaseSync} from 'node:sqlite';
import {mkdtemp,writeFile,mkdir,readFile,rm,realpath} from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import {Readable} from 'node:stream';
import {UploadQueue} from '../src/uploads.mjs';

function server(){const records=new Map();return {records,async request(endpoint,q,options={}){
  assert.equal(endpoint,'uploads');let v;
  if(q.action==='begin'){const b=options.body;v=records.get(b.id);if(v)assert.deepEqual(v.intent,b);else records.set(b.id,v={intent:b,path:b.path,offset:0,state:'receiving',parts:[]});}
  else {v=records.get(q.id);assert.ok(v);if(q.action==='chunk'){assert.equal(Number(q.offset),v.offset);v.parts.push(Buffer.from(options.body));v.offset+=options.body.length;}if(q.action==='complete'){assert.equal(v.offset,v.intent.size);v.state='accepted';}if(q.action==='cancel')v.state='cancelled';}
  return Readable.from([JSON.stringify({data:{path:v.path,offset:v.offset,state:v.state}})]);
}};}
async function fixture(t){const directory=await realpath(await mkdtemp(path.join(os.tmpdir(),'loom-upload-')));const db=new DatabaseSync(':memory:');t.after(async()=>{db.close();await rm(directory,{recursive:true,force:true});});return {directory,db,remote:server()};}

test('uploads bounded chunks, preserves originals, excludes rebuildables and retains empty folders',async t=>{
  const {directory,db,remote}=await fixture(t);const source=path.join(directory,'folder');await mkdir(path.join(source,'empty'),{recursive:true});await mkdir(path.join(source,'node_modules'));await writeFile(path.join(source,'node_modules','skip.js'),'rebuild');const payload=Buffer.alloc(1024*1024+37,42);await writeFile(path.join(source,'data.bin'),payload);
  const ensured=[];const q=new UploadQueue(db,remote,{ensureDirectory:async p=>{ensured.push(p);return {path:p,state:'accepted'};}});
  await q.enqueue([source],{root:'box',path:'Documents'});await q.running;
  assert.deepEqual(ensured,['Documents/folder','Documents/folder/empty']);
  assert.equal(q.list().filter(v=>v.state==='accepted').length,3);
  assert.equal(q.list().find(v=>v.state==='notice').exclusionCount,1);
  const v=[...remote.records.values()][0];assert.deepEqual(v.parts.map(p=>p.length),[1024*1024,37]);assert.deepEqual(Buffer.concat(v.parts),payload);assert.deepEqual(await readFile(path.join(source,'data.bin')),payload);
});

test('restart pauses saved transfers and changed sources are held',async t=>{
  const {directory,db,remote}=await fixture(t);const source=path.join(directory,'note.md');await writeFile(source,'original');const first=new UploadQueue(db,remote);first.suspended=true;await first.enqueue([source],{root:'box',path:'Documents'});
  const second=new UploadQueue(db,remote);assert.equal(second.list()[0].state,'paused');await writeFile(source,'new content');await second.control({id:second.list()[0].id,action:'resume'});await second.running;assert.equal(second.list()[0].state,'held');assert.match(second.list()[0].error,/Source changed/);assert.equal(remote.records.size,0);
});

test('folder keep-both destination propagates to children',async t=>{
  const {directory,db,remote}=await fixture(t);const source=path.join(directory,'folder');await mkdir(source);await writeFile(path.join(source,'note.md'),'note');const q=new UploadQueue(db,remote,{ensureDirectory:async p=>({path:p+' (2)',state:'accepted'})});await q.enqueue([source],{root:'box',path:'Documents'});await q.running;assert.equal([...remote.records.values()][0].intent.path,'Documents/folder (2)/note.md');
});
