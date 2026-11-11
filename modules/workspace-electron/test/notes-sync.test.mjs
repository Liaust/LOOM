import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, rm, readFile, writeFile, lstat, mkdir, rename } from 'node:fs/promises';
import path from 'node:path';
import os from 'node:os';
import { NotesSync,watchDirectories } from '../src/notes-sync.mjs';
import { digest,LocalFiles } from '../src/local-files.mjs';
import { placeAcceptedPath } from '../src/local-placement.mjs';

function owner() {
  const files=new Map([['Notes/one.md',{text:'original',id:'file_one',version:1}]]),receipts=new Map();
  let offline=false,lost=false,submitted=[];
  const snapshot=(p,f)=>({root:'box',path:p,file:{file_id:f.id,relative_path:p,collection_id:'notes'},base_id:`base_${f.version}`,generation:'generation',hash:`sha256:${digest(Buffer.from(f.text))}`,exists:!f.deleted,text:f.text});
  return {
    files,receipts,submitted,
    set offline(value){offline=value},set lost(value){lost=value},
    async json(endpoint,query,options={}) {
      if(offline)throw Error('Network unavailable');
      if(endpoint==='entries')return {data:{entries:[...files].filter(([p,f])=>!f.deleted&&p.startsWith(query.path+'/')).slice(0,1).map(([p])=>({relative_path:p})),has_more:false}};
      assert.equal(endpoint,'note');
      if(options.method!=='POST'){
        const f=files.get(query.path)||{text:'',id:'new_file',version:0,deleted:true};return {data:snapshot(query.path,f)};
      }
      const r=options.body;submitted.push(structuredClone(r));
      if(receipts.has(r.operation_id))return {data:receipts.get(r.operation_id)};
      const f=files.get(r.path)||{text:'',id:'new_file',version:0,deleted:true};
      if(r.base_id!==`base_${f.version}`)return {data:{operation_id:r.operation_id,state:'conflict',reason:'Source changed'}};
      const destination=r.target||r.path;
      if(r.kind==='rename'&&files.has(destination))return {data:{operation_id:r.operation_id,state:'conflict',reason:'Destination exists'}};
      const next={...f,text:r.kind==='edit'?r.text:f.text,version:f.version+1,deleted:r.kind==='delete'};
      if(r.kind==='rename')files.delete(r.path);files.set(destination,next);
      const result={operation_id:r.operation_id,state:'accepted',note:snapshot(destination,next)};receipts.set(r.operation_id,result);
      if(lost){lost=false;throw Error('Connection lost after commit')};return {data:result};
    },
  };
}
test('root watch events use the same parent identity as root-level working files',()=>{
  assert.deepEqual(watchDirectories(['','Notes']),['.','Notes']);
});
test('missing source parents retire unchanged copies only after an independent Files absence',async t=>{
 const {client,remote}=await fixture(t);const record=await client.adopt('box','Notes/one.md'),json=remote.json.bind(remote);let sourceExists=false;
 remote.json=async(endpoint,query,options)=>{if(endpoint==='stat'){if(sourceExists)return {data:{kind:'file'}};throw Object.assign(Error('not found'),{status:404});}if(endpoint==='note'&&options?.method!=='POST')throw Object.assign(Error('parent missing'),{status:404});return json(endpoint,query,options);};
 sourceExists=true;await assert.rejects(()=>client.refresh(record.path),/parent missing/);assert.equal(record.deleted,undefined);
 sourceExists=false;await client.refresh(record.path);assert.equal(record.deleted,true);assert.equal(client.byID.get(record.id).text,'original');assert.equal(remote.submitted.length,0);
});
test('a concurrent local edit never resurrects a deleted Main file',async t=>{
 const {client,remote}=await fixture(t);const record=await client.adopt('box','Notes/one.md');for(const w of client.watchers.values())w.close();client.watchers.clear();await writeFile(path.join(client.root,record.localPath),'unsent local edit');remote.files.get(record.path).deleted=true;
 await client.refresh(record.path);assert.equal(record.held,true);assert.match(record.localRecoveryNeeded,/local copy has changed/);assert.equal(await readFile(path.join(client.root,record.localPath),'utf8'),'unsent local edit');assert.equal(remote.submitted.length,0);
});
async function fixture(t, options = {}) {
  const directory=await mkdtemp(path.join(os.tmpdir(),'loom-native-'));const remote=owner();const client=new NotesSync(directory,remote,options);
  await client.init();t.after(async()=>{await client.close();await rm(directory,{recursive:true,force:true})});return {directory,remote,client};
}
test('a capture-aborted active watch restarts even when its path signature is unchanged',async t=>{
  const {client,remote}=await fixture(t);await client.adopt('box','Notes/one.md');
  remote.request=async()=>{};client.watchActive=true;client.watchSignature='["Notes"]';client.watchActiveAbort=new AbortController();client.watchActiveAbort.abort();
  let restarted=false;client.openWatch=async()=>{restarted=true;};client.startWatch();await new Promise(r=>setTimeout(r,130));assert.equal(restarted,true);
});
test('explicit source extensions create, rename and save without an appended md suffix', async t => {
  const { client, remote } = await fixture(t);
  const created = await client.mutate({root:'box',path:'Notes',name:'script.py',operation:'create_note',text:'print(1)'}); await client.running;
  assert.equal(created.path, 'Notes/script.py');
  await client.mutate({root:'box',path:created.path,name:'script.ts',operation:'rename'}); await client.running;
  const r=client.get('box','Notes/script.ts'); await client.save('box',r.path,'console.log(2)',(await client.read(r)).base); await client.running;
  assert.equal(remote.files.get(r.path).text,'console.log(2)');
  assert.equal(remote.files.has('Notes/script.py.md'),false);
  assert.equal((await client.mutate({root:'box',path:'Notes',name:'Meeting',operation:'create_note'})).path,'Notes/Meeting.md'); await client.running;
});

async function treeFixture(t) {
  const moveDirectory=async(local,p)=>{const from=path.join(local.root,p.from),to=path.join(local.root,p.to);const source=await lstat(from).catch(()=>null);if(source){await mkdir(path.dirname(to),{recursive:true});await rename(from,to);}const result=await lstat(to);assert.equal(`${result.dev}:${result.ino}`,p.identity);};
  const value=await fixture(t,{moveDirectory}),{client,remote}=value;
  remote.files.set('Notes/folder/a.py',{text:'print(1)',id:'code',version:1});
  remote.files.set('Notes/folder/asset.pdf',{text:'opaque asset',id:'asset',version:1});
  const json=remote.json.bind(remote); const requests=[]; let lose=false, pause;
  remote.json=async(endpoint,query,options)=>{
    if(endpoint!=='tree-move')return json(endpoint,query,options);
    const body=options.body; requests.push(structuredClone(body));
    const receipt={operation_id:body.operation_id,from:body.from,to:body.to,plan_hash:'exact-plan',entries:3,bytes:21,state:body.confirm?'accepted':'preview'};
    if(body.confirm){
      if(pause)await pause;
      if(!remote.receipts.has(body.operation_id))for(const [name,file] of [...remote.files])if(name===body.from||name.startsWith(body.from+'/')){remote.files.delete(name);remote.files.set(body.to+name.slice(body.from.length),{...file,version:file.version+1});}
      remote.receipts.set(body.operation_id,receipt);if(lose){lose=false;throw Error('Lost tree receipt');}
    }
    return {data:receipt};
  };
  return {...value,requests,setLost:()=>{lose=true;},setPause:promise=>{pause=promise;},moveDirectory};
}

test('populated folder preview moves Main first and follows existing local files without downloading remote assets', async t=>{
  const {client,remote,requests}=await treeFixture(t);const r=await client.adopt('box','Notes/folder/a.py');
  const preview=await client.mutate({root:'box',path:'Notes/folder',operation:'rename',target:'Notes/moved',directory:true});
  assert.equal(preview.status,'preview');assert.equal(remote.files.has('Notes/folder/a.py'),true);
  await client.trees.confirm(preview.operation_id);await client.running;
  assert.equal(client.get('box','Notes/moved/a.py').id,r.id);assert.equal((await client.read(r)).text,'print(1)');
  assert.equal(remote.files.has('Notes/moved/asset.pdf'),true);await assert.rejects(()=>lstat(path.join(client.root,'box/Notes/moved/asset.pdf')),/ENOENT/);
  assert.equal(client.inspect(preview.operation_id).summary.includes('Local handover: complete'),true);
  assert.equal(requests.length,2);await client.save('box',r.path,'print(2)',(await client.read(r)).base);await client.running;
  assert.equal(remote.files.get(r.path).text,'print(2)');
});

test('lost folder receipt and restart preserve the same move request without recreating the old local path', async t=>{
  const {client,remote,directory,setLost,requests,moveDirectory}=await treeFixture(t);await client.adopt('box','Notes/folder/a.py');
  const preview=await client.mutate({root:'box',path:'Notes/folder',operation:'rename',target:'Notes/moved',directory:true});setLost();
  await client.trees.confirm(preview.operation_id);await client.running;await client.close();
  const restored=new NotesSync(directory,remote,{moveDirectory});await restored.init();t.after(()=>restored.close());await restored.pump();
  assert.deepEqual(requests[1],requests[2]);assert.equal(restored.get('box','Notes/moved/a.py').pending,false);
  await assert.rejects(()=>lstat(path.join(restored.root,'box/Notes/folder')),/ENOENT/);
});

test('folder moves refuse pending edits and keep a local destination collision after Main acceptance', async t=>{
  const {client,remote,setPause}=await treeFixture(t);const r=await client.adopt('box','Notes/folder/a.py');
  remote.offline=true;await client.save('box',r.path,'pending',(await client.read(r)).base);await client.running;
  await assert.rejects(()=>client.trees.preview('Notes/folder','Notes/moved'),/pending edits/);remote.offline=false;await client.pump();
  const preview=await client.trees.preview('Notes/folder','Notes/moved');let release;setPause(new Promise(resolve=>release=resolve));
  await client.trees.confirm(preview.operation_id);await mkdir(path.join(client.root,'box/Notes/moved'));await writeFile(path.join(client.root,'box/Notes/moved/unrelated.txt'),'preserve');
  release();await client.running;
  assert.equal(remote.files.has('Notes/moved/a.py'),true);
  // The production helper refuses this race; this fixture uses an occupied
  // destination to exercise the same OS-level refusal without deleting bytes.
  assert.equal(await readFile(path.join(client.root,'box/Notes/moved/unrelated.txt'),'utf8'),'preserve');
  assert.equal(client.activity()[0].status,'local_recovery');assert.equal(client.get('box','Notes/folder/a.py'),r);
});
test('native notes save with no Obsidian process or plugin and retain exact receipts',async t=>{
  const {client,remote}=await fixture(t);const r=await client.adopt('box','Notes/one.md');
  const before=await client.read(r);await client.save('box',r.path,'native edit',before.base);await client.running;
  assert.equal(remote.files.get(r.path).text,'native edit');assert.equal(r.pending,false);assert.equal(client.activity()[0].status,'accepted');
  assert.equal(await readFile(path.join(client.root,'box/Notes/one.md'),'utf8'),'native edit');
});
test('offline saves survive application restart and reconnect without rebase',async t=>{
  const {directory,remote,client}=await fixture(t);const r=await client.adopt('box','Notes/one.md');remote.offline=true;
  await client.save('box',r.path,'offline edit',(await client.read(r)).base);await client.running;await client.close();
  const restored=new NotesSync(directory,remote);await restored.init();t.after(()=>restored.close());
  assert.equal((await restored.read(restored.get('box',r.path))).text,'offline edit');assert.equal(restored.get('box',r.path).pending,true);
  remote.offline=false;await restored.pump();assert.equal(remote.files.get(r.path).text,'offline edit');assert.equal(restored.get('box',r.path).pending,false);
});
test('lost response replays immutable operation, not a second save',async t=>{
  const {client,remote}=await fixture(t);const r=await client.adopt('box','Notes/one.md');remote.lost=true;
  await client.save('box',r.path,'once',(await client.read(r)).base);await client.running;await client.pump();
  assert.equal(remote.receipts.size,1);assert.deepEqual(remote.submitted[0],remote.submitted[1]);assert.equal(r.pending,false);
});
test('source conflict preserves both source and native pending edit',async t=>{
  const {client,remote}=await fixture(t);const r=await client.adopt('box','Notes/one.md');remote.files.set(r.path,{text:'agent edit',id:'file_one',version:2});
  await client.save('box',r.path,'Mac edit',(await client.read(r)).base);await client.running;
  assert.equal(r.held,true);assert.equal(remote.files.get(r.path).text,'agent edit');assert.equal((await client.read(r)).text,'Mac edit');
  assert.equal(client.activity()[0].status,'conflict');const base=(await client.read(r)).base;await assert.rejects(()=>client.save('box',r.path,'overwrite',base),/conflict/);
});
test('ordered offline successors use accepted parent bases and preserve every edit',async t=>{
  const {client,remote}=await fixture(t);const r=await client.adopt('box','Notes/one.md');remote.offline=true;
  await client.save('box',r.path,'one',(await client.read(r)).base);await client.running;
  await client.save('box',r.path,'two',(await client.read(r)).base);await client.running;
  remote.offline=false;await client.pump();
  assert.equal(remote.files.get(r.path).text,'two');assert.equal(remote.submitted.length,2);
  assert.equal(remote.submitted[0].base_id,'base_1');assert.equal(remote.submitted[1].base_id,'base_2');assert.equal(r.pending,false);
});
test('external source refresh cannot be silently replaced using an older accepted base',async t=>{
  const {client,remote}=await fixture(t);const r=await client.adopt('box','Notes/one.md');
  await client.save('box',r.path,'first',(await client.read(r)).base);await client.running;
  remote.files.set(r.path,{text:'agent edit',id:'file_one',version:9});await client.refresh(r.path);
  await client.save('box',r.path,'after agent',(await client.read(r)).base);await client.running;
  assert.equal(remote.submitted.at(-1).base_id,'base_9');assert.equal(remote.files.get(r.path).text,'after agent');
});
test('create, rename and recoverable source delete retain local history',async t=>{
  const {client,remote}=await fixture(t);
  const created=await client.mutate({root:'box',path:'Notes',name:'new.md',operation:'create_note',text:'new'});await client.running;
  assert.equal(created.operation_id,client.activity()[0].id);
  await client.mutate({root:'box',path:created.path,name:'renamed.md',operation:'rename'});await client.running;
  assert.equal(remote.files.get('Notes/renamed.md').text,'new');assert.equal(client.get('box','Notes/new.md'),undefined);
  await client.mutate({root:'box',path:'Notes/renamed.md',operation:'trash'});await client.running;
  assert.equal(remote.files.get('Notes/renamed.md').deleted,true);assert.equal(client.get('box','Notes/renamed.md'),undefined);assert.equal(client.activity().length,3);
});
test('client detects externally edited working files without reading an Obsidian journal',async t=>{
  const {client,remote}=await fixture(t);const r=await client.adopt('box','Notes/one.md');
  await writeFile(path.join(client.root,r.localPath),'external local edit');
  await client.refresh(r.path);await client.serial;await client.running;
  assert.equal(remote.files.get(r.path).text,'external local edit');
});

test('restart materializes a durable edit after interruption without replacing it with older disk bytes',async t=>{
  const {directory,remote,client}=await fixture(t);const r=await client.adopt('box','Notes/one.md');
  client.queue(r,'edit','saved before interruption');await client.close();
  const restored=new NotesSync(directory,remote);await restored.init();
  assert.equal((await restored.read(restored.get('box',r.path))).text,'saved before interruption');
  await restored.pump();assert.equal(remote.files.get(r.path).text,'saved before interruption');await restored.close();
});

test('reviewed conflict resolution retains original receipts and checks the reviewed Main base',async t=>{
  const {client,remote}=await fixture(t);const r=await client.adopt('box','Notes/one.md');
  remote.files.set(r.path,{text:'agent edit',id:'file_one',version:2});
  await client.save('box',r.path,'local edit',(await client.read(r)).base);await client.running;
  const original=r.lastOperation,review=await client.review('box',r.path);
  assert.equal(review.source,'agent edit');assert.equal(review.local,'local edit');
  await client.resolve(review.token,'reviewed combination');await client.running;
  assert.equal(remote.files.get(r.path).text,'reviewed combination');assert.equal(r.held,false);
  const retained=JSON.parse(client.db.prepare('SELECT record FROM operations WHERE id=?').get(original).record);
  assert.equal(retained.state,'resolved');assert.equal(retained.receipt.state,'conflict');assert.equal(retained.text,'local edit');
  assert.equal(remote.submitted.at(-1).base_id,'base_2');
});

test('a stale source response cannot overwrite a newer accepted local save',async t=>{
  const {client,remote}=await fixture(t);const r=await client.adopt('box','Notes/one.md');
  const json=remote.json.bind(remote);let release,started;
  const start=new Promise(resolve=>started=resolve),wait=new Promise(resolve=>release=resolve);
  remote.json=async(endpoint,query,options)=>{
    const result=await json(endpoint,query,options);
    if(options?.method!=='POST'){started();await wait;}
    return result;
  };
  const refresh=client.refresh(r.path);await start;
  await client.save('box',r.path,'newer save',(await client.read(r)).base);await client.running;
  release();await refresh;
  assert.equal((await client.read(r)).text,'newer save');assert.equal(r.source.text,'newer save');
});

test('save during an upload is drained without another wake',async t=>{
  const {client,remote}=await fixture(t);const r=await client.adopt('box','Notes/one.md');
  const json=remote.json.bind(remote);let release,started;
  const start=new Promise(resolve=>started=resolve),wait=new Promise(resolve=>release=resolve);
  remote.json=async(endpoint,query,options)=>{
    if(options?.method==='POST'&&options.body.text==='first'){started();await wait;}
    return json(endpoint,query,options);
  };
  await client.save('box',r.path,'first',(await client.read(r)).base);await start;
  await client.save('box',r.path,'second',(await client.read(r)).base);
  release();await client.running;
  assert.equal(remote.files.get(r.path).text,'second');assert.equal(r.pending,false);
});

test('a reconnect drains more than one page without spinning on held work',async t=>{
  const {client,remote}=await fixture(t);const r=await client.adopt('box','Notes/one.md');
  await client.change(async()=>{for(let i=0;i<102;i++)client.queue(r,'edit',`edit ${i}`);await client.materialize(r);});
  await client.pump();
  assert.equal(remote.submitted.length,102);assert.equal(r.pending,false);
  assert.equal(remote.files.get(r.path).text,'edit 101');
});

test('resolution preserves the exact external local version reviewed',async t=>{
  const {client,remote}=await fixture(t);const r=await client.adopt('box','Notes/one.md');
  remote.files.set(r.path,{text:'agent edit',id:'file_one',version:2});
  await client.save('box',r.path,'local edit',(await client.read(r)).base);await client.running;
  await writeFile(path.join(client.root,r.localPath),'external after conflict');
  const review=await client.review('box',r.path);assert.equal(review.external,'external after conflict');
  await client.resolve(review.token,'reviewed merge');await client.running;
  const op=JSON.parse(client.db.prepare('SELECT record FROM operations WHERE id=?').get(r.lastOperation).record);
  assert.equal(op.resolution.externalLocal,'external after conflict');
  assert.equal(remote.files.get(r.path).text,'reviewed merge');
});

test('empty folders use directory receipts; nonempty folders are not moved',async t=>{
  const {client,remote}=await fixture(t);
  await client.mutate({root:'box',path:'Notes',name:'empty',operation:'create_folder'});await client.running;
  await client.mutate({root:'box',path:'Notes/empty',name:'renamed',operation:'rename',directory:true});await client.running;
  assert.equal(remote.submitted.at(-1).directory,true);assert.equal(remote.files.get('Notes/renamed').deleted,false);
  remote.files.set('Notes/renamed/child.md',{id:'child',version:1,text:'preserve'});
  await assert.rejects(()=>client.mutate({root:'box',path:'Notes/renamed',operation:'trash',directory:true}),/Nonempty folder/);
  assert.equal(remote.files.get('Notes/renamed/child.md').text,'preserve');
  remote.files.delete('Notes/renamed/child.md');
  await client.mutate({root:'box',path:'Notes/renamed',operation:'trash',directory:true});await client.running;
  assert.equal(remote.files.get('Notes/renamed').deleted,true);
});

test('retained deleted child bytes do not prevent removing a now-empty source folder',async t=>{
  const {client}=await fixture(t);
  await client.mutate({root:'box',path:'Notes',name:'owned',operation:'create_folder'});await client.running;
  await client.mutate({root:'box',path:'Notes/owned',name:'child.md',operation:'create_note',text:'preserve'});await client.running;
  await client.mutate({root:'box',path:'Notes/owned/child.md',operation:'trash'});await client.running;
  await client.mutate({root:'box',path:'Notes/owned',operation:'trash',directory:true});await client.running;
  assert.equal(client.activity()[0].status,'accepted');
  const removal=JSON.parse(client.db.prepare('SELECT record FROM operations WHERE id=?').get(client.activity().find(op=>op.name==='Notes/owned/child.md').id).record);
  assert.equal(await readFile(removal.placement.retained,'utf8'),'preserve');
});

function gateRename(remote,kind='rename') {
  const json=remote.json.bind(remote);let release,started;
  const start=new Promise(resolve=>started=resolve),wait=new Promise(resolve=>release=resolve);
  remote.json=async(endpoint,query,options)=>{
    if(options?.body?.kind===kind){started();await wait;}
    return json(endpoint,query,options);
  };
  return {start,release};
}
test('online move changes Main before local paths, with no duplicate old working file',async t=>{
  const {client,remote}=await fixture(t);const r=await client.adopt('box','Notes/one.md');
  const original=await lstat(path.join(client.root,r.localPath));const gate=gateRename(remote);
  const result=await client.mutate({root:'box',path:r.path,target:'Notes/moved.md',operation:'rename'});await gate.start;
  assert.equal(result.status,'pending_main');assert.equal(result.path,'Notes/one.md');assert.equal(result.target,'Notes/moved.md');
  assert.equal(client.get('box','Notes/moved.md'),undefined);assert.equal(remote.files.has('Notes/one.md'),true);
  assert.equal(await readFile(path.join(client.root,r.localPath),'utf8'),'original');
  await assert.rejects(()=>lstat(path.join(client.root,'box/Notes/moved.md')),error=>error.code==='ENOENT');
  gate.release();await client.running;
  assert.equal(remote.files.has('Notes/one.md'),false);assert.equal(r.path,'Notes/moved.md');assert.equal(r.pending,false);
  await assert.rejects(()=>lstat(path.join(client.root,'box/Notes/one.md')),error=>error.code==='ENOENT');
  assert.equal((await lstat(path.join(client.root,r.localPath))).ino,original.ino);
});
test('edits while awaiting a move use its accepted new path and exact successor base',async t=>{
  const {client,remote}=await fixture(t);const r=await client.adopt('box','Notes/one.md');const gate=gateRename(remote);
  await client.mutate({root:'box',path:r.path,target:'Notes/moved.md',operation:'rename'});await gate.start;
  await client.save('box','Notes/one.md','edit during move',(await client.read(r)).base);
  gate.release();await client.running;
  assert.equal(remote.files.get('Notes/moved.md').text,'edit during move');assert.equal(r.pending,false);
  assert.equal(remote.submitted[1].path,'Notes/moved.md');assert.equal(remote.submitted[1].base_id,'base_2');
  assert.equal((await client.read(r)).text,'edit during move');
});
test('offline move and edit survive restart without inventing a successful local or Main move',async t=>{
  const {client,remote,directory}=await fixture(t);const r=await client.adopt('box','Notes/one.md');remote.offline=true;
  await client.mutate({root:'box',path:r.path,target:'Notes/offline.md',operation:'rename'});await client.running;
  await client.save('box',r.path,'offline successor',(await client.read(r)).base);await client.running;await client.close();
  const restored=new NotesSync(directory,remote);await restored.init();t.after(()=>restored.close());
  assert.equal((await restored.read(restored.get('box','Notes/one.md'))).text,'offline successor');
  assert.equal(restored.get('box','Notes/one.md').pathOperation.target,'Notes/offline.md');
  remote.offline=false;await restored.pump();
  assert.equal(remote.files.get('Notes/offline.md').text,'offline successor');assert.equal(restored.get('box','Notes/offline.md').pending,false);
});
test('lost Main move acknowledgement replays the original request and places the file once',async t=>{
  const {client,remote,directory}=await fixture(t);await client.adopt('box','Notes/one.md');remote.lost=true;
  await client.mutate({root:'box',path:'Notes/one.md',target:'Notes/once.md',operation:'rename'});await client.running;
  assert.ok(client.get('box','Notes/one.md'));assert.ok(remote.files.has('Notes/once.md'));await client.close();
  const restored=new NotesSync(directory,remote);await restored.init();t.after(()=>restored.close());await restored.pump();
  assert.deepEqual(remote.submitted[0],remote.submitted[1]);assert.equal(remote.receipts.size,1);
  assert.equal(restored.get('box','Notes/once.md').pending,false);
});
test('Main conflict leaves local path and contents unchanged',async t=>{
  const {client,remote}=await fixture(t);const r=await client.adopt('box','Notes/one.md');
  remote.files.set('Notes/occupied.md',{id:'other',version:1,text:'independent'});
  await client.mutate({root:'box',path:r.path,target:'Notes/occupied.md',operation:'rename'});await client.running;
  assert.equal(r.path,'Notes/one.md');assert.equal(r.held,true);assert.equal((await client.read(r)).text,'original');
  assert.equal(remote.files.get('Notes/occupied.md').text,'independent');assert.equal(client.activity()[0].status,'conflict');
});
test('accepted Main move with a local collision is not misreported as a source failure and recovers without replay',async t=>{
  const {client,remote,directory}=await fixture(t);const r=await client.adopt('box','Notes/one.md');const gate=gateRename(remote);
  await client.mutate({root:'box',path:r.path,target:'Notes/collision.md',operation:'rename'});await gate.start;
  await writeFile(path.join(client.root,'box/Notes/collision.md'),'external destination');gate.release();await client.running;
  assert.equal(r.path,'Notes/one.md');assert.equal(r.held,true);assert.equal(client.activity()[0].status,'local_recovery');
  assert.match(client.inspect(r.lastOperation).summary,/Source receipt: accepted/);
  assert.equal(await readFile(path.join(client.root,'box/Notes/collision.md'),'utf8'),'external destination');
  assert.equal(remote.files.get('Notes/collision.md').text,'original');await client.close();
  // Remove only the test-owned collision, not a user recovery policy.
  await rm(path.join(client.root,'box/Notes/collision.md'));
  const restored=new NotesSync(directory,remote);await restored.init();t.after(()=>restored.close());
  assert.equal(restored.get('box','Notes/collision.md').pending,false);assert.equal(remote.submitted.length,1);
  assert.equal(restored.activity()[0].status,'accepted');
});
test('a second path request is held until the first is accepted',async t=>{
  const {client,remote}=await fixture(t);await client.adopt('box','Notes/one.md');remote.offline=true;
  await client.mutate({root:'box',path:'Notes/one.md',target:'Notes/pending.md',operation:'rename'});await client.running;
  await assert.rejects(()=>client.mutate({root:'box',path:'Notes/one.md',operation:'trash'}),/already awaiting/);
});
test('legacy optimistic queued moves retain their immutable request and placement semantics',async t=>{
  const {client,remote}=await fixture(t);const r=await client.adopt('box','Notes/one.md');
  client.queue(r,'rename',r.text,'Notes/legacy.md');await client.materialize(r);assert.equal(r.path,'Notes/legacy.md');
  await client.pump();assert.equal(r.pending,false);assert.equal(remote.files.get('Notes/legacy.md').text,'original');
  assert.equal(remote.submitted[0].path,'Notes/one.md');
});
test('removal waits for Main and retains local bytes outside the active working tree',async t=>{
  const {client,remote}=await fixture(t);const r=await client.adopt('box','Notes/one.md'),gate=gateRename(remote,'delete');
  await client.mutate({root:'box',path:r.path,operation:'trash'});await gate.start;
  assert.equal(r.deleted,undefined);assert.equal(await readFile(path.join(client.root,r.localPath),'utf8'),'original');
  await assert.rejects(async()=>client.save('box',r.path,'late editor save',(await client.read(r)).base),/awaiting Main/);
  gate.release();await client.running;
  assert.equal(remote.files.get('Notes/one.md').deleted,true);assert.equal(client.get('box','Notes/one.md'),undefined);
  const op=JSON.parse(client.db.prepare('SELECT record FROM operations WHERE id=?').get(r.lastOperation).record);
  assert.equal(await readFile(op.placement.retained,'utf8'),'original');
  await assert.rejects(()=>lstat(path.join(client.root,r.localPath)),error=>error.code==='ENOENT');
});
test('a pending Main response remains replayable rather than stranded outside the send queue',async t=>{
  const {client,remote}=await fixture(t);await client.adopt('box','Notes/one.md');
  const json=remote.json.bind(remote);let pending=true,first;
  remote.json=async(endpoint,query,options)=>{
    if(pending&&options?.body?.kind==='rename'){pending=false;first=structuredClone(options.body);return {data:{operation_id:options.body.operation_id,state:'pending'}};}
    return json(endpoint,query,options);
  };
  await client.mutate({root:'box',path:'Notes/one.md',target:'Notes/later.md',operation:'rename'});await client.running;
  assert.equal(client.activity()[0].status,'pending');assert.ok(client.get('box','Notes/one.md'));
  await client.pump();assert.equal(client.get('box','Notes/later.md').pending,false);assert.deepEqual(remote.submitted[0],first);
});
test('an interrupted draft handover is replayed from its original path after local placement',async t=>{
  const {client,remote,directory}=await fixture(t);await client.adopt('box','Notes/one.md');
  client.emit=async(type,event)=>{if(event.previousPath)throw Error('Draft owner interrupted');};
  await client.mutate({root:'box',path:'Notes/one.md',target:'Notes/handed-over.md',operation:'rename'});await client.running;
  assert.equal(client.get('box','Notes/handed-over.md').pathOperation.kind,'rename');
  assert.match(client.inspect(client.activity()[0].id).summary,/Local handover: pending/);
  assert.equal(client.activity()[0].status,'local_recovery');await client.close();
  const events=[],restored=new NotesSync(directory,remote,{emit:(type,event)=>events.push(event)});await restored.init();t.after(()=>restored.close());
  assert.ok(events.some(event=>event.previousPath==='Notes/one.md'&&event.path==='Notes/handed-over.md'));
  assert.equal(restored.get('box','Notes/handed-over.md').pending,false);assert.equal(remote.submitted.length,1);
  assert.match(restored.inspect(restored.activity()[0].id).summary,/Local handover: complete/);
});
test('restart finishes a published local hardlink without another Main move or leaving the old path',async t=>{
  const {client,remote,directory}=await fixture(t);await client.adopt('box','Notes/one.md');const save=client.saveOp.bind(client);let fail=true;
  client.saveOp=op=>{if(fail&&op.placement?.stage==='published'){fail=false;throw Error('Local placement interrupted');}return save(op);};
  await client.mutate({root:'box',path:'Notes/one.md',target:'Notes/recovered.md',operation:'rename'});await client.running;
  assert.equal(client.activity()[0].status,'local_recovery');await client.close();
  const restored=new NotesSync(directory,remote);await restored.init();t.after(()=>restored.close());
  assert.equal(restored.get('box','Notes/recovered.md').pending,false);assert.equal(remote.submitted.length,1);
  await assert.rejects(()=>lstat(path.join(restored.root,'box/Notes/one.md')),error=>error.code==='ENOENT');
});
test('an empty local folder placeholder created before a crash can be reconciled without replacing entries',async t=>{
  const directory=await mkdtemp(path.join(os.tmpdir(),'loom-placement-'));t.after(()=>rm(directory,{recursive:true,force:true}));
  const local=new LocalFiles(path.join(directory,'working'));await mkdir(path.join(local.root,'box/Notes/from'),{recursive:true});
  const op={mainFirst:true,state:'accepted',kind:'rename',directory:true,placement:{from:'box/Notes/from',to:'box/Notes/to',stage:'waiting'}};let saved;
  await assert.rejects(()=>placeAcceptedPath(local,path.join(directory,'recovery'),op,async()=>{
    if(op.placement.destinationIdentity)throw Error('Stopped before identity checkpoint');saved=structuredClone(op);
  }),/Stopped/);
  assert.equal(saved.placement.stage,'creating_directory');assert.equal(saved.placement.destinationIdentity,undefined);
  await placeAcceptedPath(local,path.join(directory,'recovery'),saved,async()=>{});
  assert.equal(saved.placement.stage,'done');assert.equal((await lstat(path.join(local.root,'box/Notes/to'))).isDirectory(),true);
  assert.equal((await lstat(saved.placement.retained)).isDirectory(),true);
});
