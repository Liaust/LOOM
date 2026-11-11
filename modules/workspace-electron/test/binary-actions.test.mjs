import test from 'node:test';
import assert from 'node:assert/strict';
import {mkdtemp,rm} from 'node:fs/promises';
import path from 'node:path';
import os from 'node:os';
import {Readable} from 'node:stream';
import {NotesSync} from '../src/notes-sync.mjs';
import {FileCache} from '../src/cache.mjs';
import {BinaryActions} from '../src/binary-actions.mjs';

test('remote-only binary action never downloads bytes and lost responses replay the immutable request',async t=>{
  const root=await mkdtemp(path.join(os.tmpdir(),'loom-asset-'));let lost=true;const requests=[],receipts=new Map();
  const remote={async json(endpoint,q,o={}){
    assert.equal(endpoint,'actions');if(o.method!=='POST')return {data:{path:q.path,base_id:'meta',generation:'g',file:{file_id:'file'}}};
    const r=o.body;requests.push(structuredClone(r));if(receipts.has(r.operation_id))return {data:receipts.get(r.operation_id)};
    const value={operation_id:r.operation_id,state:'accepted',base_id:'next',file:{file_id:'file',deleted:r.kind==='delete'},path:r.target||r.path};receipts.set(r.operation_id,value);if(lost){lost=false;throw Error('Connection lost');}return {data:value};
  },async content(){throw Error('A rename must not download the asset');}};
  const notes=new NotesSync(path.join(root,'native'),remote);await notes.init();const cache=new FileCache(path.join(root,'cache'));await cache.init();notes.binary=new BinaryActions(notes,cache);
  t.after(async()=>{await notes.close();await rm(root,{recursive:true,force:true});});
  const moved=await notes.binary.mutate({root:'box',path:'Notes/large.pdf',target:'Notes/moved.pdf',operation:'rename'});await notes.running;await notes.pump();
  assert.equal(notes.inspect(moved.operation_id).summary.includes('State: accepted'),true);assert.equal(cache.summary().files,0);assert.deepEqual(requests[0],requests[1]);assert.equal('text' in requests[0],false);
});

test('accepted asset moves retarget cached pins without copying bytes; removal retains the offline copy',async t=>{
  const root=await mkdtemp(path.join(os.tmpdir(),'loom-cache-move-'));t.after(()=>rm(root,{recursive:true,force:true}));
  const cache=new FileCache(root);await cache.init();await cache.fetch('box','a.pdf',{size:3,revision:'r'},()=>Readable.from(['pdf']));await cache.pin('box','a.pdf',true);
  const original=cache.get('box','a.pdf');await cache.retarget('box','a.pdf','b.pdf','op1');
  assert.equal(cache.get('box','a.pdf'),null);assert.equal(cache.get('box','b.pdf').file,original.file);assert.equal(cache.get('box','b.pdf').pinned,true);
  await cache.retarget('box','b.pdf',undefined,'op2');assert.equal(cache.get('box','b.pdf'),null);assert.equal(cache.summary().pinned,1);assert.equal(cache.get('retained','op2').file,original.file);
});
