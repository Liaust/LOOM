import test from 'node:test';
import assert from 'node:assert/strict';
import { DirectoryTree } from '../renderer/tree-model.js';

const folder = relative => ({ name: relative.split('/').at(-1), relative_path: relative, kind: 'directory' });

test('source hints refresh visible branches and invalidate collapsed ones without a corpus traversal',async()=>{
 const calls=[];const tree=new DirectoryTree(async input=>{calls.push(input.path);return {entries:[folder(input.path+'/new')],has_more:false};});
 tree.remember('box','Notes',{entries:[folder('Notes/old')],has_more:false});tree.remember('box','Other',{entries:[folder('Other/old')],has_more:false});tree.expanded.add(tree.key('box','Notes'));
 await tree.sourceChanged('box',['Notes','Other','Unknown']);assert.deepEqual(calls,['Notes']);assert.equal(tree.branch('box','Notes').page.entries[0].name,'new');assert.equal(tree.branch('box','Other').page,null);assert.equal(tree.branches.has(tree.key('box','Unknown')),false);
});

test('expansion loads one branch only and revalidates after an unwatched collapse', async () => {
  const calls = [];
  const tree = new DirectoryTree(async input => { calls.push(input); return { entries: [folder(`${input.path}/child`)], has_more: false }; });
  await tree.expand('box', 'Notes');
  assert.deepEqual(calls.map(call => call.path), ['Notes']);
  assert.equal(tree.branches.size, 1);
  await tree.expand('box', 'Projects');
  tree.collapse('box', 'Notes');
  await tree.expand('box', 'Notes');
  assert.equal(calls.length, 3);
  assert.ok(tree.isExpanded('box', 'Projects'));
  assert.ok(tree.isExpanded('box', 'Notes'));
});

test('requests share one in-flight page and pagination preserves earlier entries', async () => {
  let resolve;
  const gate = new Promise(done => { resolve = done; });
  const calls = [];
  const tree = new DirectoryTree(async input => { calls.push(input); await gate; return input.cursor ? { entries: [folder('two')], has_more: false } : { entries: [folder('one')], has_more: true, next_cursor: 'page2' }; });
  const first = tree.expand('box', '');
  const second = tree.load('box', '');
  resolve();
  await Promise.all([first, second]);
  assert.equal(calls.length, 1);
  await tree.load('box', '', { more: true });
  assert.equal(calls[1].cursor, 'page2');
  assert.deepEqual(tree.branch('box').page.entries.map(entry => entry.name), ['one', 'two']);
});

test('ordinary listings seed the tree and reveal only lists missing ancestors', async () => {
  const calls = [];
  const tree = new DirectoryTree(async input => { calls.push(input.path); return { entries: [], has_more: false }; });
  tree.remember('box', '', { entries: [folder('Notes')], has_more: false });
  tree.remember('box', 'Notes/Physics', { entries: [], has_more: false });
  await tree.reveal('box', 'Notes/Physics');
  assert.deepEqual(calls, ['Notes']);
  tree.collapseAll();
  assert.equal(tree.expanded.size, 0);
  assert.equal(calls.length, 1);
});

test('failed branches can retry and a changed local root rejects old in-flight data', async () => {
  let finish;
  const pending = new Promise(resolve => { finish = resolve; });
  const tree = new DirectoryTree(async () => pending);
  const old = tree.expand('local', 'Notes');
  tree.resetRoot('local');
  tree.remember('local', 'Notes', { entries: [folder('new-vault')], has_more: false });
  finish({ entries: [folder('old-vault')], has_more: false });
  await old;
  assert.equal(tree.branch('local', 'Notes').page.entries[0].name, 'new-vault');
  let attempts = 0;
  const retry = new DirectoryTree(async () => { if (++attempts === 1) throw new Error('offline'); return { entries: [], has_more: false }; });
  await assert.rejects(retry.expand('box'), /offline/);
  assert.equal(retry.branch('box').error, 'offline');
  await retry.load('box', '', { refresh: true });
  assert.equal(retry.branch('box').error, null);
});

test('accepted removal updates loaded membership and subtrees without touching siblings or reading Main',()=>{
  const tree=new DirectoryTree(()=>{throw Error('No scan is needed for an exact accepted removal.');});
  tree.remember('box','',{entries:[folder('Notes'),folder('Notes-copy')],has_more:false});
  tree.remember('box','Notes',{entries:[folder('Notes/child')],has_more:false});tree.expanded.add(tree.key('box','Notes'));
  tree.remember('storage','',{entries:[folder('Notes')],has_more:false});
  tree.remove('box','Notes');
  assert.deepEqual(tree.branch('box','').page.entries.map(e=>e.relative_path),['Notes-copy']);assert.equal(tree.branches.has(tree.key('box','Notes')),false);assert.equal(tree.isExpanded('box','Notes'),false);assert.equal(tree.branch('storage','').page.entries.length,1);
  tree.remember('box','',{entries:[folder('Notes')],has_more:false});assert.equal(tree.branch('box','').page.entries[0].relative_path,'Notes');
});

test('an accepted deletion cannot be undone by an older in-flight directory snapshot',async()=>{
  let release,calls=0;const gate=new Promise(resolve=>release=resolve);
  const tree=new DirectoryTree(async()=>{if(++calls===1){await gate;return{entries:[folder('gone'),folder('kept')],has_more:false};}return{entries:[folder('kept')],has_more:false};});
  tree.remember('box','',{entries:[folder('gone'),folder('kept')],has_more:false});
  const reading=tree.load('box','',{refresh:true});await Promise.resolve();tree.remove('box','gone');assert.deepEqual(tree.branch('box','').page.entries.map(e=>e.name),['kept']);release();await reading;
  assert.equal(calls,2);assert.deepEqual(tree.branch('box','').page.entries.map(e=>e.name),['kept']);
});

test('refresh requested during a pending read returns a fresh result, not the earlier snapshot',async()=>{
  let release,calls=0;const gate=new Promise(resolve=>release=resolve);
  const tree=new DirectoryTree(async()=>{if(++calls===1){await gate;return{entries:[folder('old')],has_more:false};}return{entries:[folder('new')],has_more:false};});
  const first=tree.expand('box','');await Promise.resolve();const refresh=tree.load('box','',{refresh:true});release();await Promise.all([first,refresh]);
  assert.equal(calls,2);assert.equal(tree.branch('box','').page.entries[0].name,'new');
});
