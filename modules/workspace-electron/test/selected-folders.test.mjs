import test from 'node:test';
import assert from 'node:assert/strict';
import {mkdtemp,mkdir,rm,writeFile,readFile,rename,lstat,unlink} from 'node:fs/promises';
import path from 'node:path';
import os from 'node:os';
import {NotesSync} from '../src/notes-sync.mjs';
import {SelectedFolders} from '../src/selected-folders.mjs';
import {digest} from '../src/local-files.mjs';

async function fixture(t){
  const directory=await mkdtemp(path.join(os.tmpdir(),'loom-selected-'));
  const files=new Map([['Notes',{directory:true,id:'root',version:1}],['Notes/a.md',{text:'first',id:'a',version:1}]]),receipts=new Map();
  let offline=false,reads=0;const submitted=[];
  const snapshot=(p,f)=>({root:'box',path:p,file:{file_id:f.id||`id_${p}`},base_id:`base_${f.version||0}`,generation:'g',exists:!f.deleted,text:f.text||'',hash:'sha256:'+digest(Buffer.from(f.text||''))});
  const remote={files,submitted,get reads(){return reads;},set offline(v){offline=v;},async json(endpoint,q,o={}){
    if(offline)throw Error('Network unavailable');reads++;
    if(endpoint==='entries'){
      if(!files.has(q.path)){const e=Error('Missing folder');e.status=404;throw e;}
      return {data:{entries:[...files].filter(([p,f])=>p!==q.path&&!f.deleted&&path.posix.dirname(p)===q.path).map(([p,f])=>({name:path.posix.basename(p),relative_path:p,kind:f.directory?'directory':'file',size:Buffer.byteLength(f.text||''),revision:String(f.version)})),has_more:false}};
    }
    assert.equal(endpoint,'note');
    if(o.method!=='POST')return {data:snapshot(q.path,files.get(q.path)||{deleted:true})};
    const r=o.body;submitted.push(structuredClone(r));if(receipts.has(r.operation_id))return {data:receipts.get(r.operation_id)};
    const f=files.get(r.path)||{deleted:true,id:r.file_id,version:0};
    if(r.base_id!==`base_${f.version}`)return {data:{operation_id:r.operation_id,state:'conflict'}};
    if(r.target&&files.has(r.target))return {data:{operation_id:r.operation_id,state:'conflict'}};
    const p=r.target||r.path,next={...f,directory:r.directory,text:r.kind==='edit'?r.text:f.text,version:f.version+1,deleted:r.kind==='delete'};
    if(r.target)files.delete(r.path);files.set(p,next);
    const receipt={operation_id:r.operation_id,state:'accepted',note:snapshot(p,next)};receipts.set(r.operation_id,receipt);return {data:receipt};
  }};
  const notes=new NotesSync(directory,remote);await notes.init();const selections=new SelectedFolders(notes,{},remote);
  t.after(async()=>{await selections.close();await notes.close();await rm(directory,{recursive:true,force:true});});
  return {directory,remote,notes,selections,files};
}

test('explicit selection projects later notes and empty folders, with no idle polling or implicit saves',async t=>{
  const {selections,notes,files,remote}=await fixture(t);selections.start({root:'box',path:'Notes'});await selections.running;
  assert.equal(notes.get('box','Notes/a.md').text,'first');const reads=remote.reads;await new Promise(r=>setTimeout(r,250));assert.equal(remote.reads,reads);assert.equal(remote.submitted.length,0);
  files.set('Notes/empty',{directory:true,id:'empty',version:1});files.set('Notes/new.md',{text:'new',id:'new',version:1});
  selections.sourceChanged(['Notes']);await selections.running;
  assert.equal((await lstat(path.join(notes.root,'box/Notes/empty'))).isDirectory(),true);assert.equal(notes.get('box','Notes/new.md').text,'new');
});
test('source deletion retires unchanged bytes and recreation does not collide with historical row',async t=>{
  const {selections,notes,files}=await fixture(t);selections.start({root:'box',path:'Notes'});await selections.running;
  const old=notes.get('box','Notes/a.md');files.delete(old.path);selections.sourceChanged(['Notes']);await selections.running;
  assert.equal(notes.get('box',old.path),undefined);await assert.rejects(()=>lstat(path.join(notes.root,old.localPath)),/ENOENT/);
  assert.equal(await readFile(old.observation.placement.retained,'utf8'),'first');
  files.set(old.path,{text:'fresh',id:'new_a',version:1});selections.sourceChanged(['Notes']);await selections.running;
  assert.notEqual(notes.get('box',old.path).id,old.id);assert.equal(notes.byID.has(old.id),true);
});
test('offline local addition obtains an absent base only on reconnect and never replaces occupied Main content',async t=>{
  const {selections,notes,remote,files}=await fixture(t);selections.start({root:'box',path:'Notes'});await selections.running;
  remote.offline=true;await writeFile(path.join(notes.root,'box/Notes/local.md'),'offline');await selections.scanLocal('Notes');await notes.running;
  const r=notes.get('box','Notes/local.md');assert.equal(r.pending,true);assert.equal(remote.submitted.length,0);
  remote.offline=false;await notes.pump();assert.equal(files.get(r.path).text,'offline');assert.equal(r.pending,false);
  remote.offline=true;await writeFile(path.join(notes.root,'box/Notes/collision.md'),'local');await selections.scanLocal('Notes');await notes.running;
  files.set('Notes/collision.md',{text:'remote',id:'occupied',version:1});remote.offline=false;await notes.pump();
  assert.equal(files.get('Notes/collision.md').text,'remote');assert.equal(notes.get('box','Notes/collision.md').held,true);
});
test('proven external file rename waits for Main and does not recreate its old local path',async t=>{
  const {selections,notes,files}=await fixture(t);selections.start({root:'box',path:'Notes'});await selections.running;
  const r=notes.get('box','Notes/a.md');await rename(path.join(notes.root,r.localPath),path.join(notes.root,'box/Notes/renamed.md'));
  await selections.scanLocal('Notes');await notes.running;
  assert.equal(files.has('Notes/a.md'),false);assert.equal(notes.get('box','Notes/renamed.md').id,r.id);assert.equal(r.held,false);
  await assert.rejects(()=>lstat(path.join(notes.root,'box/Notes/a.md')),/ENOENT/);
});
test('pausing a selection preserves its files and prevents later source membership downloads',async t=>{
  const {selections,notes,files}=await fixture(t);const s=selections.start({root:'box',path:'Notes'});await selections.running;
  selections.state(s.id,'paused');files.set('Notes/no.md',{text:'not downloaded',id:'no',version:1});selections.sourceChanged(['Notes']);await selections.running;
  assert.equal(notes.get('box','Notes/no.md'),undefined);assert.equal(notes.get('box','Notes/a.md').text,'first');
});

test('an unresolved local absence cannot delete Main without an explicit remove action',async t=>{
  const {selections,notes,files}=await fixture(t);selections.start({root:'box',path:'Notes'});await selections.running;
  const r=notes.get('box','Notes/a.md');await unlink(path.join(notes.root,r.localPath));await selections.scanLocal('Notes');await notes.running;
  assert.equal(files.get(r.path).text,'first');assert.equal(r.localAbsence,true);
  await notes.mutate({root:'box',path:r.path,operation:'trash'});await notes.running;assert.equal(files.get(r.path).deleted,true);assert.equal(r.deleted,true);
});

test('pause during source adoption is not overwritten by a stale progress update',async t=>{
  const {selections,notes,remote,files}=await fixture(t);files.set('Notes/b.md',{text:'second',id:'b',version:1});
  const json=remote.json.bind(remote);let begin,release;const begun=new Promise(r=>begin=r),wait=new Promise(r=>release=r);
  remote.json=async(e,q,o)=>{if(e==='note'&&q.path==='Notes/a.md'&&!o){begin();await wait;}return json(e,q,o);};
  const s=selections.start({root:'box',path:'Notes'});await begun;selections.state(s.id,'paused');release();await selections.running;
  assert.equal(selections.list()[0].state,'paused');assert.equal(notes.get('box','Notes/b.md'),undefined);
});

test('capture suspension refuses an earlier uncommitted adoption after its remote lookup',async t=>{
  const {notes,remote}=await fixture(t);const json=remote.json.bind(remote);let release,begin;const wait=new Promise(r=>release=r),begun=new Promise(r=>begin=r);
  remote.json=async(...args)=>{begin();await wait;return json(...args);};
  const adoption=notes.adopt('box','Notes/a.md');await begun;notes.suspended=true;await notes.serial;release();
  await assert.rejects(()=>adoption,/capture/);assert.equal(notes.get('box','Notes/a.md'),undefined);notes.suspended=false;
});

test('restart never rematerializes an interrupted source-absence retirement',async t=>{
  const {directory,selections,notes,files,remote}=await fixture(t);selections.start({root:'box',path:'Notes'});await selections.running;
  const r=notes.get('box','Notes/a.md');files.delete(r.path);await notes.refresh(r.path);
  r.deleted=false;notes.persist(r);await selections.close();await notes.close();
  const restored=new NotesSync(directory,remote);await restored.init();t.after(()=>restored.close());
  await assert.rejects(()=>lstat(path.join(restored.root,r.localPath)),/ENOENT/);await restored.refresh(r.path);
  assert.equal(restored.get('box',r.path),undefined);assert.equal(await readFile(r.observation.placement.retained,'utf8'),'first');
});

test('missing selected root watches its parent rather than retrying a nonexistent directory forever',async t=>{
  const {selections,notes,files}=await fixture(t);selections.start({root:'box',path:'Notes'});await selections.running;
  files.clear();selections.sourceChanged(['.']);await selections.running;
  assert.equal(selections.list()[0].sourceMissing,true);assert.deepEqual(selections.watchPaths(),['.']);assert.equal(notes.get('box','Notes/a.md'),undefined);
});
