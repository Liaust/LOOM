import test from 'node:test';
import assert from 'node:assert/strict';
import {PassThrough} from 'node:stream';
import {mkdtemp,mkdir,rename,writeFile,rm,realpath} from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import {DirectoryWatch} from '../src/directory-watch.mjs';

const until=async predicate=>{for(let n=0;n<150;n++){if(predicate())return;await new Promise(r=>setTimeout(r,20));}assert.fail('Notification did not arrive.');};
const frame=(stream,type,paths,root)=>stream.write(`data: ${JSON.stringify({type,paths,...(root?{root}:{})})}\n\n`);

test('visible folders subscribe separately from Notes and cancel obsolete Box scopes',async t=>{
  const calls=[],events=[];
  const controller=new DirectoryWatch({request:async(...args)=>{const stream=new PassThrough();calls.push({args,stream});args[2].signal.addEventListener('abort',()=>stream.destroy());return stream;}},{emit:(type,event)=>events.push({type,...event}),retryMs:10});
  t.after(()=>controller.close());
  controller.setScopes([{root:'box',path:'Projects'},{root:'box:company',path:'Projects'},{root:'box',path:'Projects'}]);
  await until(()=>calls.length===2);
  assert.equal(calls[0].args[2].lane,'explorer-watch');assert.deepEqual(calls[0].args[2].body.paths,['Projects']);
  frame(calls[0].stream,'ready',['Projects']);frame(calls[1].stream,'changed',['Projects']);
  await until(()=>events.filter(e=>e.type==='explorer-changed').length===2);
  controller.setScopes([{root:'box:company',path:'Projects'}]);
  assert.equal(calls[0].args[2].signal.aborted,true);assert.equal(calls[1].args[2].signal.aborted,false);
  controller.setScopes([{root:'box:company',path:'Projects'}]);assert.equal(calls.length,2);
  controller.reconnect();await until(()=>calls.length===3);frame(calls[2].stream,'ready',['Projects']);
  await until(()=>events.filter(e=>e.type==='explorer-changed').length===3);
  controller.setScopes([]);assert.equal(calls[2].args[2].signal.aborted,true);
});

test('notifications outside the bound root are refused and reconnect restores coverage',async t=>{
  const calls=[],events=[];
  const controller=new DirectoryWatch({request:async(...args)=>{const stream=new PassThrough();calls.push(stream);args[2].signal.addEventListener('abort',()=>stream.destroy());return stream;}},{emit:(type,event)=>events.push({type,...event}),retryMs:10});t.after(()=>controller.close());
  controller.setScopes([{root:'box:company',path:'Notes'}]);await until(()=>calls.length===1);
  frame(calls[0],'changed',['Notes'],'box');await until(()=>calls.length===2);
  assert.match(events[0].error,/changed root/);assert.equal(events.some(e=>e.type==='explorer-changed'),false);
  frame(calls[1],'ready',['Notes']);await until(()=>events.some(e=>e.type==='explorer-changed'));
  assert.throws(()=>controller.setScopes([{root:'box',path:'../escape'}]),/Invalid/);
});

test('Mac watches creation and directory replacement without recursively scanning content',async t=>{
  const root=await realpath(await mkdtemp(path.join(os.tmpdir(),'loom-explorer-'))),events=[];
  t.after(()=>rm(root,{recursive:true,force:true}));await mkdir(path.join(root,'Notes'));
  const controller=new DirectoryWatch(null,{localRoot:async(_root,relative)=>path.join(root,relative),emit:(type,event)=>events.push({type,...event}),retryMs:10});t.after(()=>controller.close());
  controller.setScopes([{root:'mac',path:'Notes'}]);await until(()=>events.some(e=>e.type==='explorer-changed'));events.length=0;
  await mkdir(path.join(root,'Notes','taxonomy'));await until(()=>events.some(e=>e.paths?.includes('Notes')));
  await rename(path.join(root,'Notes'),path.join(root,'old'));await mkdir(path.join(root,'Notes'));
  await new Promise(r=>setTimeout(r,300));events.length=0;
  await writeFile(path.join(root,'Notes','new.md'),'current');await until(()=>events.some(e=>e.paths?.includes('Notes')));
  controller.setScopes([]);await new Promise(r=>setTimeout(r,80));events.length=0;
  await writeFile(path.join(root,'Notes','quiet.md'),'quiet');await new Promise(r=>setTimeout(r,200));assert.equal(events.length,0);
});
