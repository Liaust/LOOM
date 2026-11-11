import test from 'node:test';
import assert from 'node:assert/strict';
import {mkdtemp,rm,readFile} from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import {NotesSync} from '../src/notes-sync.mjs';

async function fixture(t){
  const directory=await mkdtemp(path.join(os.tmpdir(),'loom-project-'));
  const calls=[];let lose=false,kind='archive';
  const plan=()=>({project_id:'one',plan_digest:'project-digest',workspace:{operation_kind:kind,operation_id:'workspace-operation',source:{root:kind==='archive'?'box':'storage',relative_path:'Projects/one'},destination:{root:kind==='archive'?'storage':'box',relative_path:'Projects/one'}}});
  const remote={async api(route,q,options){calls.push({route,body:options.body});kind=route.includes('/restore/')?'restore':'archive';if(route.endsWith('/plan'))return {data:plan()};if(lose){lose=false;throw Object.assign(Error('Lost response'),{receipt:{operation_id:'workspace-operation',phase:'payload_moved'}});}return {data:{project_id:'one',operation_id:'workspace-operation',plan_digest:'project-digest',phase:'complete',mutation_blocked:kind==='restore',activation_state:'inactive'}};},async json(_endpoint,q){return {data:{root:'box',path:q.path,exists:true,text:'kept',file:{file_id:'file'},hash:'sha256:x',base_id:'base',generation:'g'}};}};
  const notes=new NotesSync(directory,remote);await notes.init();t.after(async()=>{await notes.close();await rm(directory,{recursive:true,force:true});});
  return {notes,remote,directory,calls,lose:()=>{lose=true;}};
}
test('project review is durable, exact and does not submit until confirmed',async t=>{
  const {notes,calls}=await fixture(t),l=notes.lifecycle;
  const op=await l.preview('one','archive');assert.equal(calls.length,1);assert.equal(l.get(op.id).state,'preview');
  const result=await l.apply(op.id);assert.equal(result.state,'complete');assert.deepEqual(calls[1].body,{plan:op.plan,plan_digest:op.plan.plan_digest,confirm:true});
  await l.apply(op.id);assert.equal(calls.length,2);
});
test('pending edits prevent archive without discarding local bytes',async t=>{
  const {notes,calls}=await fixture(t);const r=await notes.adopt('box','Projects/one/a.md');r.pending=true;notes.persist(r);
  const op=await notes.lifecycle.preview('one','archive');await assert.rejects(notes.lifecycle.apply(op.id),/pending local changes/);
  assert.equal(calls.length,1);assert.equal((await notes.local.read(r.localPath)).text,'kept');
});
test('lost response fences new writes and uses exact saved recovery rather than a new archive',async t=>{
  const {notes,calls,lose}=await fixture(t);const r=await notes.adopt('box','Projects/one/a.md');
  const op=await notes.lifecycle.preview('one','archive');lose();const held=await notes.lifecycle.apply(op.id);assert.equal(held.state,'needs_review');assert.equal(held.receipt.phase,'payload_moved');
  assert.throws(()=>notes.queue(r,'edit','newer'),/needs review/);await assert.rejects(notes.lifecycle.apply(op.id),/explicitly recover/);
  const result=await notes.lifecycle.apply(op.id,{recover:true});assert.equal(result.state,'complete');assert.equal(r.retainedProject,op.id);assert.throws(()=>notes.queue(r,'edit','overwrite'),/archived project/);
  assert.ok(calls.at(-1).route.endsWith('/recover'));assert.deepEqual(calls.at(-1).body,{operation_id:op.plan.workspace.operation_id,plan_digest:op.plan.plan_digest,confirm:true});
  assert.equal((await notes.local.read(r.localPath)).text,'kept');
});
test('inactive restore is complete despite mutation_blocked and does not reactivate runtime',async t=>{
  const {notes,calls}=await fixture(t);const r=await notes.adopt('box','Projects/one/a.md');r.retainedProject='prior';notes.persist(r);
  const op=await notes.lifecycle.preview('one','restore');assert.equal((await notes.lifecycle.apply(op.id)).state,'complete');
  assert.equal(r.restoreReady,op.id);await notes.refresh(r.path);assert.equal(r.retainedProject,undefined);assert.equal(r.source.base_id,'base');
  assert.equal(calls.some(c=>c.route.endsWith('/reactivate')),false);
});
test('a typed stale pre-mutation review is rejected without permanently fencing local files',async t=>{
  const {notes,remote}=await fixture(t),api=remote.api.bind(remote);remote.api=async(route,...args)=>{if(route.endsWith('/apply'))throw Object.assign(Error('Review is stale'),{code:'project_archive.review_stale'});return api(route,...args);};
  const op=await notes.lifecycle.preview('one','archive');assert.equal((await notes.lifecycle.apply(op.id)).state,'rejected');assert.equal(notes.lifecycle.blocked('Projects/one/a.md'),undefined);
  await assert.rejects(notes.lifecycle.apply(op.id,{recover:true}),/fresh review/);
});
test('a refresh already in flight cannot retire the local note after an uncertain project operation',async t=>{
  const {notes,remote,lose}=await fixture(t);const r=await notes.adopt('box','Projects/one/a.md');
  let release;remote.json=(_endpoint,q)=>new Promise(resolve=>{release=()=>resolve({data:{root:q.root,path:q.path,exists:false,base_id:'absent'}});});
  const refresh=notes.refresh(r.path);await new Promise(resolve=>setImmediate(resolve));const op=await notes.lifecycle.preview('one','archive');lose();await notes.lifecycle.apply(op.id);release();await refresh;
  assert.equal(r.deleted,undefined);assert.equal((await notes.local.read(r.localPath)).text,'kept');
});
test('an unrelated refresh source cannot retire a local project note',async t=>{
  const {notes,remote}=await fixture(t);const r=await notes.adopt('box','Projects/one/a.md');
  remote.json=async()=>({data:{root:'box',path:'Projects/other/a.md',exists:false,base_id:'absent'}});
  await assert.rejects(notes.refresh(r.path),/unrelated refresh source/);
  assert.equal(r.deleted,undefined);assert.equal((await notes.local.read(r.localPath)).text,'kept');
});
test('exact recovery can finish while preserving a draft created after a lost response',async t=>{
  const {notes,lose}=await fixture(t);const op=await notes.lifecycle.preview('one','archive');lose();await notes.lifecycle.apply(op.id);
  notes.lifecycle.checkDrafts=()=>{throw Error('new draft retained')};assert.equal((await notes.lifecycle.apply(op.id,{recover:true})).state,'complete');
});
