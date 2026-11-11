import test from 'node:test';
import assert from 'node:assert/strict';
import {DatabaseSync} from 'node:sqlite';
import {mkdtemp,mkdir,writeFile,readFile,rm,realpath,rename} from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import {Readable} from 'node:stream';
import {BoxRoots} from '../src/box-roots.mjs';
import {NotesSync} from '../src/notes-sync.mjs';
import {SelectedFolders} from '../src/selected-folders.mjs';
import {UploadQueue} from '../src/uploads.mjs';
import {digest} from '../src/local-files.mjs';

const company='box:company',roots=()=>new BoxRoots([
  {box_id:'personal',box_key:'personal',root_ref:'box',primary:true,available:true},
  {box_id:'company',box_key:'company',root_ref:company,primary:false,available:true},
]);
const key=(root,relative)=>JSON.stringify([root,relative]);
async function fixture(t){
  const directory=await realpath(await mkdtemp(path.join(os.tmpdir(),'loom-boxes-'))),files=new Map(),submitted=[],receipts=new Map();
  for(const root of ['box',company]){
    files.set(key(root,'Notes'),{id:root+'-folder',directory:true,text:'',version:1});
    files.set(key(root,'Notes/a.md'),{id:root+'-a',text:root,version:1});
  }
  const snapshot=(root,p,f)=>({root,path:p,file:{file_id:f?.id||root+p},base_id:`base_${f?.version||0}`,generation:'g',exists:Boolean(f&&!f.deleted),text:f?.text||'',hash:digest(Buffer.from(f?.text||''))});
  const remote={offline:false,async json(endpoint,q,o={}){
    if(this.offline)throw Error('Network unavailable');
    const b=o.body||q,root=b.root;
    if(endpoint==='tree-move'){
      if(!b.confirm)return {data:{operation_id:b.operation_id,state:'preview',plan_hash:'exact'}};
      for(const [k,f] of [...files]){const [r,p]=JSON.parse(k);if(r===root&&(p===b.from||p.startsWith(b.from+'/'))){files.delete(k);files.set(key(root,b.to+p.slice(b.from.length)),{...f,version:f.version+1});}}
      return {data:{operation_id:b.operation_id,state:'accepted',plan_hash:'exact',root}};
    }
    if(endpoint==='entries')return {data:{entries:[...files].filter(([k,f])=>{const [r,p]=JSON.parse(k);return r===root&&path.posix.dirname(p)===q.path&&!f.deleted;}).map(([k,f])=>{const [,p]=JSON.parse(k);return {relative_path:p,name:path.posix.basename(p),kind:f.directory?'directory':'file',size:Buffer.byteLength(f.text),revision:String(f.version)};}),has_more:false}};
    assert.equal(endpoint,'note');
    if(o.method!=='POST')return {data:snapshot(root,q.path,files.get(key(root,q.path)))};
    submitted.push(structuredClone(b));
    if(receipts.has(b.operation_id))return {data:receipts.get(b.operation_id)};
    const f=files.get(key(root,b.path))||{version:0,id:b.file_id};
    const p=b.target||b.path,next={...f,text:b.kind==='edit'?b.text:f.text,version:f.version+1,directory:b.directory,deleted:b.kind==='delete'};
    if(b.target)files.delete(key(root,b.path));files.set(key(root,p),next);
    const result={operation_id:b.operation_id,state:'accepted',note:snapshot(root,p,next)};receipts.set(b.operation_id,result);return {data:result};
  }};
  let notes=new NotesSync(directory,remote,{boxes:roots()});await notes.init();
  const selections=new SelectedFolders(notes,{},remote);
  t.after(async()=>{await selections.close();await notes.close();await rm(directory,{recursive:true,force:true});});
  return {directory,notes,selections,remote,files,submitted,async restart(boxes=roots()){
    await selections.close();await notes.close();notes=new NotesSync(directory,remote,{boxes});await notes.init();return notes;
  }};
}

test('same-name notes retain root, replica, ID and exact sealed request through offline restart',async t=>{
  const f=await fixture(t),{notes,remote,files}=f;
  const personal=await notes.adopt('box','Notes/a.md'),companyNote=await notes.adopt(company,'Notes/a.md');
  assert.equal(personal.localPath,'box/Notes/a.md');assert.equal(companyNote.localPath,'boxes/company/Notes/a.md');
  remote.offline=true;
  await notes.save('box',personal.path,'personal edit',digest(Buffer.from(personal.text)));
  await notes.save(company,companyNote.path,'company edit',digest(Buffer.from(companyNote.text)));await notes.running;
  const op=JSON.parse(notes.db.prepare('SELECT record FROM box_operations').get().record),sealed=op.request;
  assert.equal(sealed.root,company);assert.equal(op.box_id,'company');assert.equal(sealed.box_id,undefined);
  assert.equal(notes.db.prepare('SELECT COUNT(*) AS n FROM operations').get().n,1);
  assert.equal(notes.activity(company).every(item=>item.root===company),true);
  assert.equal(notes.activitySummary(company).counts.queued,1);
  const restored=await f.restart();remote.offline=false;await restored.pump();
  assert.equal(restored.get(company,companyNote.path).id,companyNote.id);
  assert.equal(files.get(key('box',personal.path)).text,'personal edit');
  assert.equal(files.get(key(company,companyNote.path)).text,'company edit');
  assert.deepEqual(f.submitted.find(v=>v.root===company),sealed);
  assert.equal(await readFile(path.join(restored.root,companyNote.localPath),'utf8'),'company edit');
});

test('selected-folder coverage, watcher identities, absence and move-follow stay within the saved Box',async t=>{
  const {notes,selections,files,remote}=await fixture(t);
  selections.start({root:'box',path:'Notes'});await selections.running;
  const companySelection=selections.start({root:company,path:'Notes'});await selections.running;
  assert.equal(selections.list().length,2);assert.equal(selections.watchers.size,2);
  assert.equal(notes.db.prepare('SELECT COUNT(*) AS n FROM sync_selections').get().n,1);
  assert.equal(notes.db.prepare('SELECT COUNT(*) AS n FROM box_sync_selections').get().n,1);
  files.delete(key(company,'Notes/a.md'));selections.sourceChanged(['Notes'],false,company);await selections.running;
  assert.equal(notes.get(company,'Notes/a.md'),undefined);assert.equal(notes.get('box','Notes/a.md').text,'box');
  selections.followMove('Notes','Renamed',company);
  assert.equal(selections.list().find(s=>s.id===companySelection.id).path,'Renamed');
  assert.equal(selections.list().find(s=>s.root==='box').path,'Notes');await selections.running;
  remote.offline=true;
});

test('unregistered saved roots are held without blocking Personal replay or remapping files',async t=>{
  const f=await fixture(t);f.remote.offline=true;
  await f.notes.stageLocal('offline.md',{root:company,text:'company pending'});await f.notes.running;
  await f.notes.stageLocal('personal.md',{root:'box',text:'personal pending'});await f.notes.running;
  const restored=await f.restart(new BoxRoots());f.remote.offline=false;await restored.pump();
  assert.equal(restored.get(company,'offline.md').pending,true);
  assert.equal(restored.get('box','personal.md').pending,false);
  assert.equal(f.submitted.some(v=>v.root===company),false);
  assert.match(JSON.parse(restored.db.prepare('SELECT record FROM box_operations').get().record).reason,/unavailable/);
});

test('an absent refresh response from another root cannot retire a local Company projection',async t=>{
  const {notes,remote}=await fixture(t),r=await notes.adopt(company,'Notes/a.md');
  remote.json=async()=>({data:{root:'box',path:r.path,exists:false}});
  await assert.rejects(()=>notes.refresh(r.path,company),/unrelated refresh/);
  assert.equal(notes.get(company,r.path).id,r.id);assert.equal(await readFile(path.join(notes.root,r.localPath),'utf8'),company);
});

test('cross-Box note and tree moves fail before any remote request or local mutation',async t=>{
  const {notes,submitted}=await fixture(t);await notes.adopt('box','Notes/a.md');
  await assert.rejects(()=>notes.mutate({root:'box',target_root:company,path:'Notes/a.md',operation:'rename',target:'Notes/b.md'}),/Cross-Box/);
  await assert.rejects(()=>notes.trees.preview('Notes','Other',{root:'box',target_root:company}),/Cross-Box/);
  assert.equal(submitted.length,0);assert.equal(notes.get('box','Notes/a.md').text,'box');
});

test('same-root Company note rename and folder handover preserve Personal paths and IDs',async t=>{
  const {notes,files}=await fixture(t);notes.trees.moveDirectory=async(local,placement)=>rename(path.join(local.root,placement.from),path.join(local.root,placement.to));
  const personal=await notes.adopt('box','Notes/a.md'),companyNote=await notes.adopt(company,'Notes/a.md');
  await notes.mutate({root:company,path:companyNote.path,operation:'rename',target:'Notes/renamed.md'});await notes.running;
  assert.equal(notes.get(company,'Notes/renamed.md').id,companyNote.id);assert.equal(notes.get('box','Notes/a.md').id,personal.id);
  const review=await notes.trees.preview('Notes','Moved',{root:company});await notes.trees.confirm(review.operation_id);await notes.running;
  assert.equal(notes.get(company,'Moved/renamed.md').id,companyNote.id);assert.equal(companyNote.localPath,'boxes/company/Moved/renamed.md');
  assert.equal(files.has(key(company,'Moved/renamed.md')),true);assert.equal(files.has(key('box','Notes/a.md')),true);
  assert.equal(await readFile(path.join(notes.root,'box/Notes/a.md'),'utf8'),'box');
});

test('remote watcher requests and notifications carry separate saved roots for identical folders',async t=>{
  const {notes,remote}=await fixture(t),calls=[],events=[];
  await notes.adopt('box','Notes/a.md');await notes.adopt(company,'Notes/a.md');notes.emit=(type,value)=>events.push({type,...value});
  remote.request=async(_endpoint,q,o)=>{calls.push({q,body:o.body});return Readable.from(['data: '+JSON.stringify({type:'ready',root:o.body.root,paths:['Notes']})+'\n\n']);};
  await notes.openWatch();
  assert.deepEqual(calls.map(c=>c.body.root).sort(),['box',company].sort());
  assert.deepEqual(calls.map(c=>c.q.box_id).sort(),['company','personal']);
  assert.equal(events.filter(e=>e.type==='source-notification').every(e=>['box',company].includes(e.root)),true);
  assert.equal(notes.rootWatches.size,2);notes.stopWatches();assert.equal(notes.rootWatches.size,0);
});

test('uploads and folder children replay their saved root, never enter legacy upload storage',async t=>{
  const directory=await realpath(await mkdtemp(path.join(os.tmpdir(),'loom-box-uploads-'))),db=new DatabaseSync(':memory:');
  t.after(async()=>{db.close();await rm(directory,{recursive:true,force:true});});
  const source=path.join(directory,'folder');await mkdir(source);await writeFile(path.join(source,'same.md'),'bytes');
  const calls=[],folders=[],remote={async request(_endpoint,q,o){calls.push({q,...o});return Readable.from([JSON.stringify({data:{path:o.body?.path||'folder/same.md',offset:5,state:q.action==='complete'?'accepted':'receiving'}})]);}};
  const first=new UploadQueue(db,remote,{boxes:roots()});first.suspended=true;await first.enqueue([source],{root:company});
  assert.equal(db.prepare('SELECT COUNT(*) AS n FROM desktop_uploads').get().n,0);
  const resumed=new UploadQueue(db,remote,{boxes:roots(),ensureDirectory:async(p,_c,_id,binding)=>{folders.push(binding);return {path:p,state:'accepted'};}});
  await resumed.control({action:'resume_all'});await resumed.running;
  assert.equal(resumed.list().every(v=>v.root===company&&v.state==='accepted'),true);
  assert.equal(folders[0].root,company);assert.equal(calls[0].body.root,company);assert.equal(calls.every(c=>c.q.root===company),true);
});
