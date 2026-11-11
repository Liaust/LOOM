import test from 'node:test';
import assert from 'node:assert/strict';
import {FileTree} from '../renderer/file-tree.js';

function fixture(reveal){
  return {container:{querySelector:()=>null},model:{reveal,expand:async()=>{}},render(){}};
}

test('new-entry ownership is reserved before asynchronous folder discovery',async()=>{
  let release,reads=0;
  const tree=fixture(()=>{reads++;return new Promise(resolve=>{release=resolve;});});
  const first=FileTree.prototype.beginCreate.call(tree,'box','Notes',{placeholder:'Untitled.md'});
  await assert.rejects(FileTree.prototype.beginCreate.call(tree,'box','Notes',{placeholder:'Second.md'}),/Finish naming/);
  assert.equal(reads,1);release();await new Promise(resolve=>setImmediate(resolve));
  assert.equal(tree.creation.placeholder,'Untitled.md');assert.equal(tree.preparingCreation,false);
  tree.creation.resolve(null);assert.equal(await first,null);
});

test('failed folder discovery releases new-entry ownership for retry',async()=>{
  const tree=fixture(async()=>{throw Error('Offline');});
  await assert.rejects(FileTree.prototype.beginCreate.call(tree,'box','Notes',{}),/Offline/);
  assert.equal(tree.preparingCreation,false);assert.equal(tree.creation,undefined);
  tree.model.reveal=async()=>{};
  const next=FileTree.prototype.beginCreate.call(tree,'box','Notes',{});
  await new Promise(resolve=>setImmediate(resolve));tree.creation.resolve(null);assert.equal(await next,null);
});

test('cancelling empty preparation prevents a late naming row and does not release a newer owner',async()=>{
  const releases=[];
  const tree=fixture(()=>new Promise(resolve=>releases.push(resolve)));
  const first=FileTree.prototype.beginCreate.call(tree,'box','Notes',{});
  assert.equal(FileTree.prototype.cancelEmptyCreate.call(tree),true);
  const second=FileTree.prototype.beginCreate.call(tree,'box','Other',{}),owner=tree.preparingCreation;
  releases[0]();assert.equal(await first,null);assert.equal(tree.preparingCreation,owner);
  releases[1]();await new Promise(resolve=>setImmediate(resolve));
  assert.equal(tree.creation.path,'Other');tree.creation.resolve(null);await second;
});

test('empty naming cancellation resolves once without touching typed or busy names',()=>{
  let resolved=0;const tree=fixture(async()=>{});
  tree.creation={root:'box',path:'Notes',name:'   ',resolve:()=>resolved++};
  assert.equal(FileTree.prototype.cancelEmptyCreate.call(tree,'box','Other'),false);
  assert.equal(FileTree.prototype.cancelEmptyCreate.call(tree,'box','Notes'),true);
  assert.equal(resolved,1);assert.equal(tree.creation,null);
  assert.equal(FileTree.prototype.cancelEmptyCreate.call(tree),false);
  tree.creation={name:'Keep this name',resolve:()=>resolved++};assert.equal(FileTree.prototype.cancelEmptyCreate.call(tree),false);
  tree.creation={name:'',busy:true,resolve:()=>resolved++};assert.equal(FileTree.prototype.cancelEmptyCreate.call(tree),false);assert.equal(resolved,1);
});
