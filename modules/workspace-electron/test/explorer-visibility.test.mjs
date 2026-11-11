import test from 'node:test';
import assert from 'node:assert/strict';
import {visibleExplorerEntries,loadExplorerVisibility,saveExplorerVisibility} from '../renderer/explorer-visibility.js';

test('visibility preference defaults off, survives reopening and tolerates unavailable storage',()=>{
  const values=new Map(),storage={getItem:key=>values.get(key),setItem:(key,value)=>values.set(key,value)};
  assert.equal(loadExplorerVisibility(storage),false);
  saveExplorerVisibility(storage,true);assert.equal(loadExplorerVisibility(storage),true);
  saveExplorerVisibility(storage,false);assert.equal(loadExplorerVisibility(storage),false);
  assert.equal(loadExplorerVisibility({getItem(){throw Error('unavailable');}}),false);
});

test('directory projections keep canonical entries and pagination untouched',()=>{
  const entries=[{kind:'file',name:'README.md'},{kind:'file',name:'AGENTS.MD'},{kind:'file',name:'note.md'},{kind:'directory',name:'AGENTS.md'}];
  const page={entries,has_more:true,next_cursor:'next'};
  assert.deepEqual(visibleExplorerEntries(entries,true).map(entry=>entry.name),['note.md','AGENTS.md']);
  assert.equal(page.entries,entries);assert.equal(entries.length,4);assert.equal(page.next_cursor,'next');
  assert.equal(visibleExplorerEntries(entries,false),entries);
});
