import test from 'node:test';
import assert from 'node:assert/strict';
import {mkdtemp,rm} from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import {Readable} from 'node:stream';
import {NotesSync} from '../src/notes-sync.mjs';
import {OfflineFolders} from '../src/offline-folders.mjs';
import {FileCache} from '../src/cache.mjs';

async function fixture(t){
  const directory=await mkdtemp(path.join(os.tmpdir(),'loom-offline-'));
  const entries=[{name:'one.md',relative_path:'Notes/one.md',kind:'file',size:3,revision:'one'},
    {name:'empty',relative_path:'Notes/empty',kind:'directory'},
    {name:'picture.png',relative_path:'Notes/picture.png',kind:'file',size:3,revision:'asset'}];
  const writes=[];
  const remote={async json(endpoint,query,options){if(options?.method==='POST')writes.push(options);
    if(endpoint==='entries')return {data:{entries:query.path==='Notes'?entries:[],has_more:false}};
    if(endpoint==='stat')return {data:entries.find(e=>e.relative_path===query.path)};
    return {data:{root:'box',path:query.path,text:'one',base_id:'base',generation:'g',file:{file_id:'f'},exists:true}};
  },async content(){const response=Readable.from(['png']);return response;}};
  const notes=new NotesSync(path.join(directory,'native'),remote);await notes.init();
  const cache=new FileCache(path.join(directory,'cache'));await cache.init();
  const offline=new OfflineFolders(notes,cache,remote);
  t.after(async()=>{await offline.close();await notes.close();await rm(directory,{recursive:true,force:true});});
  return {directory,entries,remote,notes,offline,cache,writes};
}
test('folder download persists shallow pages and editable notes without submitting any saves',async t=>{
  const {offline,notes,writes}=await fixture(t);const preview=await offline.preview({root:'box',path:'Notes'});assert.equal(preview.files,1);
  const job=offline.start({root:'box',path:'Notes'});await offline.running;
  assert.equal(offline.jobs()[0].state,'complete');assert.equal(offline.jobs()[0].skipped,1);assert.equal(notes.get('box','Notes/one.md').text,'one');assert.equal(writes.length,0);
  assert.equal(offline.page('box','Notes').entries.length,3);assert.equal(offline.page('box','Notes/empty').entries.length,0);
  assert.equal(offline.jobs()[0].id,job.id);
});
test('pause during a listing cannot be overwritten; a retained page resumes after restart',async t=>{
  const {offline,remote,notes,cache}=await fixture(t);let release,started;
  const wait=new Promise(r=>release=r),start=new Promise(r=>started=r),json=remote.json.bind(remote);
  remote.json=async(...args)=>{if(args[0]==='entries'){started();await wait;}return json(...args);};
  const job=offline.start({root:'box',path:'Notes'});await start;offline.setState(job.id,'paused');release();await offline.running;
  assert.equal(offline.jobs()[0].state,'paused');assert.equal(offline.jobs()[0].files,0);
  const restarted=new OfflineFolders(notes,cache,remote);restarted.setState(job.id,'running');await restarted.running;await restarted.close();
  assert.equal(restarted.jobs()[0].state,'complete');assert.equal(restarted.jobs()[0].files,1);
});
test('optional attachments are pinned copies and unsafe server paths stop without another walk',async t=>{
  const {offline,cache,entries}=await fixture(t);offline.start({root:'box',path:'Notes',assets:true});await offline.running;
  assert.equal(cache.get('box','Notes/picture.png').pinned,true);
  entries.unshift({name:'escape.md',relative_path:'Other/escape.md',kind:'file',size:0});
  const second=offline.start({root:'box',path:'Notes',assets:true});await offline.running;
  assert.equal(offline.jobs().find(j=>j.id===second.id).state,'paused');assert.match(offline.jobs()[0].reason,/outside/);
});
test('simultaneous adoption shares one binding instead of replacing a pending local edit',async t=>{
  const {notes}=await fixture(t);const [a,b]=await Promise.all([notes.adopt('box','Notes/one.md'),notes.adopt('box','Notes/one.md')]);
  assert.equal(a.id,b.id);assert.equal(notes.records.size,1);assert.equal(notes.workingBytes,3);
});
