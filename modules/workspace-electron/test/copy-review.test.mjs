import test from 'node:test';
import assert from 'node:assert/strict';
import {mkdtemp,mkdir,writeFile,readFile,symlink,rm,realpath} from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import {Readable} from 'node:stream';
import {DatabaseSync} from 'node:sqlite';
import {CopyReviews,copySnapshot,guardCopyDestination,checkCopyDrafts} from '../src/copy-review.mjs';
import {UploadQueue} from '../src/uploads.mjs';
import {downloadFileCopy} from '../src/file-download.mjs';
test('copy confirmations expire and cannot be replayed',()=>{let now=1;const reviews=new CopyReviews({now:()=>now});const first=reviews.create({source:'a'});assert.equal(reviews.take(first).source,'a');assert.throws(()=>reviews.take(first),/already used/);const next=reviews.create({source:'b'});now+=300001;assert.throws(()=>reviews.take(next),/expired/);});
test('saved matching Mac drafts allow copies without erasing recovery variants',async()=>{const draft={text:'saved',binding_id:'one'},drafts=new Map([[JSON.stringify(['mac','Notes/A.md']),draft]]),read=async()=>({text:'saved',binding_id:'one'});await checkCopyDrafts(drafts,'mac','Notes',read);assert.equal(drafts.size,1);draft.variants=[{text:'unsaved',binding_id:'two'}];await assert.rejects(()=>checkCopyDrafts(drafts,'mac','Notes',read),/drafts/);assert.equal(draft.variants.length,1);});
test('changed reviewed sources queue no files and leave originals intact',async t=>{
 const dir=await realpath(await mkdtemp(path.join(os.tmpdir(),'loom-copy-')));t.after(()=>rm(dir,{recursive:true,force:true}));const file=path.join(dir,'note.md');await writeFile(file,'original');const snapshot=await copySnapshot(dir);await writeFile(file,'changed');const db=new DatabaseSync(':memory:');t.after(()=>db.close());const queue=new UploadQueue(db,{});queue.suspended=true;
 await assert.rejects(()=>queue.enqueue([dir],{root:'box',expected:snapshot.entries}),/changed/);assert.equal(queue.list().length,0);assert.equal(await readFile(file,'utf8'),'changed');
});
test('downloads refuse managed destinations including symlink aliases and never overwrite',async t=>{
 const dir=await mkdtemp(path.join(os.tmpdir(),'loom-download-'));t.after(()=>rm(dir,{recursive:true,force:true}));const managed=path.join(dir,'managed'),ordinary=path.join(dir,'ordinary');await mkdir(managed);await mkdir(ordinary);await symlink(managed,path.join(dir,'alias'));
 await assert.rejects(()=>guardCopyDestination(path.join(dir,'alias','a'),[managed]),/managed/);
 const output=path.join(ordinary,'note.md'),run=()=>downloadFileCopy({output,metadata:{size:4},load:()=>Readable.from([Buffer.from('note')]),verifyDestination:p=>guardCopyDestination(p,[managed])});assert.equal((await run()).bytes,4);await assert.rejects(run,/already exists/);assert.equal(await readFile(output,'utf8'),'note');
 let checks=0;await assert.rejects(()=>downloadFileCopy({output:path.join(ordinary,'held.md'),metadata:{size:4},load:()=>Readable.from([Buffer.from('note')]),verifyDestination:()=>{if(++checks===2)throw Error('destination changed');}}),/destination changed/);
});
