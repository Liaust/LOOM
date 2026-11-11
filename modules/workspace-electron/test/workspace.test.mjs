import test from 'node:test';
import assert from 'node:assert/strict';
import { DocumentTabs } from '../renderer/tabs.js';
import { retargetDrafts,storeDraft,draftFor,clearSavedDraft } from '../src/path-drafts.mjs';

test('document tabs preserve drafts and editing state without replacing siblings', () => {
  const tabs = new DocumentTabs(2);
  const one = { root: 'box', path: 'Notes/one.md', text: 'unsaved', dirty: true, editing: true };
  tabs.open(one); tabs.open({ root: 'storage', path: 'one.md' },{newTab:true});
  assert.equal(tabs.get('box', 'Notes/one.md'), one);
  assert.throws(() => tabs.open({ root: 'box', path: 'third.md' },{newTab:true}), /Close an open file/);
  tabs.close(); assert.equal(tabs.active, tabs.key('box', 'Notes/one.md'));
  assert.equal(tabs.get('box', 'Notes/one.md').text, 'unsaved');
});

test('accepted moves retarget active tabs without replacing unsaved editor state',()=>{
  const tabs=new DocumentTabs(),note={root:'box',path:'Notes/old.md',text:'draft',dirty:true,editing:true};tabs.open(note);
  assert.equal(tabs.move('box','Notes/old.md','Notes/new.md','box/Notes/new.md'),true);
  assert.equal(tabs.get('box','Notes/new.md'),note);assert.equal(tabs.active,tabs.key('box','Notes/new.md'));
  assert.equal(note.text,'draft');assert.equal(note.dirty,true);assert.equal(note.local_path,'box/Notes/new.md');
});
test('an open destination tab is retained instead of overwritten by a move',()=>{
  const tabs=new DocumentTabs();tabs.open({root:'box',path:'a.md',text:'one'});tabs.open({root:'box',path:'b.md',text:'two'});
  assert.equal(tabs.move('box','a.md','b.md','box/b.md'),false);assert.equal(tabs.items.size,2);assert.equal(tabs.get('box','b.md').text,'two');
});
test('draft handover preserves both colliding drafts and only moves exact path descendants',()=>{
  const key=p=>JSON.stringify(['box',p]),drafts=new Map([[key('Notes/A/one.md'),{text:'old'}],[key('Notes/AA/one.md'),{text:'sibling'}]]);
  assert.deepEqual(retargetDrafts(drafts,'box','Notes/A','Notes/B'),{moved:1,collisions:0});
  assert.equal(drafts.get(key('Notes/B/one.md')).text,'old');assert.equal(drafts.get(key('Notes/AA/one.md')).text,'sibling');
  drafts.set(key('Notes/C/one.md'),{text:'other'});
  assert.deepEqual(retargetDrafts(drafts,'box','Notes/B','Notes/C'),{moved:0,collisions:1});
  assert.equal(drafts.get(key('Notes/B/one.md')).text,'old');assert.equal(drafts.get(key('Notes/C/one.md')).text,'other');
});
test('subsequent autosave after a move collision cannot replace the independent destination draft',()=>{
  const key=p=>JSON.stringify(['box',p]),drafts=new Map([[key('old.md'),{text:'source draft'}],[key('new.md'),{text:'destination draft',binding_id:'other'}]]);
  retargetDrafts(drafts,'box','old.md','new.md','source');
  storeDraft(drafts,'box','new.md',{text:'later source draft',binding_id:'source'});
  assert.equal(drafts.get(key('new.md')).text,'destination draft');assert.equal(drafts.get(key('old.md')).text,'later source draft');
  assert.equal(draftFor(drafts,'box','new.md','source').text,'later source draft');
  clearSavedDraft(drafts,'box','source','later source draft');
  assert.equal(drafts.has(key('old.md')),false);assert.equal(drafts.get(key('new.md')).text,'destination draft');
});
test('draft variants and newer autosaves are retained when another version is saved',()=>{
  const drafts=new Map();storeDraft(drafts,'box','one.md',{text:'first',binding_id:'one'});
  storeDraft(drafts,'box','one.md',{text:'independent',binding_id:'two'});
  assert.equal(draftFor(drafts,'box','one.md','one').text,'first');assert.equal(draftFor(drafts,'box','one.md','two').text,'independent');
  storeDraft(drafts,'box','one.md',{text:'newer',binding_id:'one'});clearSavedDraft(drafts,'box','one','first');
  assert.equal(draftFor(drafts,'box','one.md','one').text,'newer');
  clearSavedDraft(drafts,'box','one','newer');assert.equal(draftFor(drafts,'box','one.md','two').text,'independent');
});
