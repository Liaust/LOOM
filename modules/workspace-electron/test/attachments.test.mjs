import test from 'node:test';
import assert from 'node:assert/strict';
import {DatabaseSync} from 'node:sqlite';
import {mkdtemp,mkdir,writeFile,readFile,rm,realpath,symlink} from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import {createHash} from 'node:crypto';
import {Attachments} from '../src/attachments.mjs';
import {attachmentReference,attachmentLink} from '../src/attachment-reference.mjs';
import {tabWheelDelta} from '../renderer/tab-strip.js';
import {EditorState} from '@codemirror/state';
import {markdown} from '@codemirror/lang-markdown';
import {attachmentSpans} from '../renderer/attachment-spans.js';
import {directoryMutation,ensureAttachmentDirectory} from '../src/upload-directories.mjs';
import {clipboardImage} from '../src/attachment-clipboard.mjs';
import {interruptedAttachment} from '../src/uploads.mjs';

test('attachment references retain dimensions, PDF page/height, Unicode and escaped delimiters',()=>{
  const image=attachmentReference('![[Attachments/a%23b.png|420x260]]');assert.equal(image.target,'Attachments/a%23b.png');assert.equal(image.width,420);assert.equal(image.height,260);assert.equal(image.label,'a#b.png');
  const pdf=attachmentReference('book.pdf#page=3&height=360');assert.equal(pdf.kind,'pdf');assert.equal(pdf.page,3);assert.equal(pdf.height,360);
  assert.equal(attachmentReference('https://example.com/pixel.png').external,true);
  assert.equal(attachmentLink('Notes/a.md','Library/ação#1.pdf'),'../Library/ação%231.pdf');
  for(const value of ['javascript:evil.png','file:///private/a.png','x.png|0','x.png|32oops','x.pdf#page=-1','x%00.png','x%0A.png'])assert.throws(()=>attachmentReference(value),value);
});
test('clipboard uses the current async MIME API, reads image bytes only and bounds payloads',async()=>{
  const read=[];const api={read:async()=>[{types:['text/plain','image/png'],getType:async type=>{read.push(type);return new Blob([new Uint8Array([0,1,255])]);}}]};
  const image=await clipboardImage(api);assert.equal(image.name,'image.png');assert.deepEqual(image.bytes,Buffer.from([0,1,255]));assert.deepEqual(read,['image/png']);
  assert.equal(await clipboardImage({read:async()=>[{types:['text/plain']}]}),null);
  await assert.rejects(()=>clipboardImage({read:async()=>[{types:['image/png'],getType:async()=>({size:65*1024**2})}]}),/64 MiB/);
});
test('tab wheel maps line/page/vertical gestures but leaves zoom gestures untouched',()=>{
  assert.equal(tabWheelDelta({deltaX:0,deltaY:5,deltaMode:1},400),120);
  assert.equal(tabWheelDelta({deltaX:-90,deltaY:2,deltaMode:0},400),-90);
  assert.equal(tabWheelDelta({deltaX:0,deltaY:1,deltaMode:2},400),400);
  assert.equal(tabWheelDelta({deltaX:0,deltaY:12,ctrlKey:true},400),0);
});
test('reconnect resumes interrupted attachments only, never explicit pauses or old bulk transfers',()=>{
  assert.equal(Boolean(interruptedAttachment({state:'paused',resumeOnConnect:true})),false);
  assert.equal(Boolean(interruptedAttachment({attachmentID:'a',state:'paused',resumeOnConnect:false})),false);
  assert.equal(Boolean(interruptedAttachment({attachmentID:'a',state:'paused',resumeOnConnect:true})),true);
  assert.equal(Boolean(interruptedAttachment({attachmentID:'a',state:'held',error:'ECONNRESET'})),true);
  assert.equal(Boolean(interruptedAttachment({attachmentID:'a',state:'held',error:'Permission denied'})),false);
});
test('folder requests bind Main identities without leaking local replica fields into strict owner schemas',async()=>{
  const binding={root:'box:company',box_id:'company',box_key:'company'},snapshot={root:binding.root,path:'Notes/Attachments',exists:false,file:{file_id:'file-id'},base_id:'base-id',generation:'generation'};
  const body=directoryMutation(binding,snapshot,snapshot.path,'desktop_test-folder');assert.equal(body.box_key,undefined);assert.equal(body.box_id,'company');assert.equal(body.operation_id,'desktop_test-folder');
  assert.throws(()=>directoryMutation(binding,{...snapshot,root:'box'},snapshot.path,'desktop_test'),/unrelated/);
  let posts=0;await ensureAttachmentDirectory({json:async(_endpoint,q,options)=>{if(options){posts++;assert.deepEqual(Object.keys(options.body).sort(),['root','box_id','path','operation_id','file_id','base_id','generation','kind','text','directory'].sort());return{data:{state:'accepted'}};}return{data:snapshot};}},{id:'desktop_test',destination:'Notes/Attachments/test.pdf'},binding);assert.equal(posts,1);
});
test('attachment blocks exclude code/metadata/escapes without a sixteen-reference cutoff',()=>{
  const text='---\nimage: "![[metadata.png]]"\n---\n\n`![[code.png]]`\n\n```md\n![[fenced.png]]\n```\n\n\\![[escaped.png]]\n\n'+Array.from({length:20},(_,i)=>`![[Attachments/image-${i}.png|420]]`).join('\n\n');
  const state=EditorState.create({doc:text,extensions:[markdown()]});const spans=attachmentSpans(state);assert.equal(spans.length,20);assert.equal(spans.at(-1).reference,'Attachments/image-19.png|420');
  assert.equal(attachmentSpans(EditorState.create({doc:'---\n![[metadata.png]]',extensions:[markdown()]})).length,0);
});
async function fixture(t,root='box'){
  const directory=await realpath(await mkdtemp(path.join(os.tmpdir(),'loom-attachments-'))),db=new DatabaseSync(':memory:');
  t.after(async()=>{db.close();await rm(directory,{recursive:true,force:true});});
  const owner={root,path:'Notes/original.md',binding:'origin',editable:true},queued=[],events=[];
  const make=()=>new Attachments(db,path.join(directory,'staged'),{context:input=>{assert.equal(input.root,owner.root);assert.equal(input.binding,owner.binding);return owner;},enqueue:async v=>{queued.push(v);return{id:'upload-'+v.id,state:'queued'};},importLocal:async v=>{queued.push(v);},emit:(...args)=>events.push(args)});
  return {directory,db,owner,queued,events,make,manager:await make().init()};
}
test('binary import stages exact bytes, binds original document and retains pending payload across restart',async t=>{
  const {directory,owner,queued,make,manager}=await fixture(t);const source=path.join(directory,'source.pdf'),payload=Buffer.from([0,255,42,0,1]);await writeFile(source,payload);
  const result=await manager.stage({root:'box',path:owner.path,binding:'origin'},{source});
  assert.equal(result.state,'pending');assert.match(result.insert,/^!\[\[Attachments\/source-[\w-]+\.pdf\]\]$/);
  assert.equal(queued.length,1);assert.equal(queued[0].binding,'origin');assert.equal(queued[0].root,'box');assert.deepEqual(await readFile(queued[0].stagePath),payload);assert.deepEqual(await readFile(source),payload);
  assert.equal(queued[0].hash,'sha256:'+createHash('sha256').update(payload).digest('hex'));
  const restarted=await make().init();assert.equal(restarted.rows()[0].uploadID,'upload-'+result.id);
  owner.path='Elsewhere/renamed.md';const saved=restarted.list({root:'box',path:owner.path,binding:'origin'})[0];assert.match(saved.insert,/\.\.\/Notes\/Attachments\//);assert.equal(saved.original,result.insert);
  restarted.accepted({id:'upload-'+result.id,destination:'Notes/Attachments/accepted (2).pdf'});assert.equal(restarted.rows()[0].state,'accepted');assert.match(restarted.list({root:'box',path:owner.path,binding:'origin'})[0].insert,/accepted \(2\)/);
});
test('Mac clipboard import is local; read-only owners and symlink sources refuse',async t=>{
  const {directory,owner,manager,queued}=await fixture(t,'mac');const result=await manager.stage({root:'mac',binding:'origin'},{bytes:Buffer.from([1,2,0]),name:'image.png'});assert.equal(result.state,'accepted');assert.equal(queued[0].root,'mac');
  const original=path.join(directory,'real.png'),link=path.join(directory,'link.png');await writeFile(original,'real');await symlink(original,link);
  await assert.rejects(()=>manager.stage({root:'mac',binding:'origin'},{source:link}),/symbolic/);
  owner.editable=false;await assert.rejects(()=>manager.stage({root:'mac',binding:'origin'},{bytes:Buffer.from('no'),name:'x.png'}),/writable/);
});
