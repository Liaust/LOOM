import test from 'node:test';
import assert from 'node:assert/strict';
import {DocumentTabs} from '../renderer/tabs.js';

const note=path=>({root:'box',path,entry:{name:path},text:path});
test('ordinary navigation reuses one tab and Back/Forward preserve cached drafts',()=>{
  const tabs=new DocumentTabs(),a={...note('a.md'),dirty:true,selection:[3,3,50],text:'unsaved'},b=note('b.md');
  tabs.open(a);tabs.open(b);assert.equal(tabs.visible().length,1);
  assert.deepEqual(tabs.target(-1),{kind:'file',root:'box',path:'a.md'});
  tabs.open(tabs.get('box','a.md'),{step:-1});assert.equal(tabs.active,tabs.key('box','a.md'));assert.equal(tabs.get('box','a.md'),a);
  assert.equal(a.text,'unsaved');assert.deepEqual(a.selection,[3,3,50]);
  tabs.open(b,{step:1});assert.equal(tabs.page().path,'b.md');assert.equal(tabs.target(1),undefined);
});
test('new branches discard only forward navigation, not documents or independent tab histories',()=>{
  const tabs=new DocumentTabs(),a=note('a.md'),b=note('b.md'),c=note('c.md');tabs.open(a);tabs.open(b);const first=tabs.activeTab;
  tabs.newTab();tabs.open(c);const second=tabs.activeTab;assert.equal(tabs.visible().length,2);
  tabs.open(b,{tab:first,record:false});tabs.open(a,{step:-1});tabs.open(note('d.md'));
  assert.equal(tabs.target(1),undefined);assert.equal(tabs.get('box','b.md'),b);
  tabs.open(c,{tab:second,record:false});assert.deepEqual(tabs.target(-1),{kind:'empty'});
  tabs.visit({kind:'empty'},{step:-1});assert.equal(tabs.target(1).path,'c.md');
});
test('folder history, explicit blank tabs and closing preserve separate slots',()=>{
  const tabs=new DocumentTabs();tabs.visit({kind:'directory',root:'box',path:'Notes'});tabs.open(note('Notes/a.md'));
  tabs.visit(tabs.target(-1),{step:-1});assert.equal(tabs.page().kind,'directory');assert.equal(tabs.target(1).path,'Notes/a.md');
  const first=tabs.activeTab;tabs.newTab();const second=tabs.activeTab;tabs.newTab();assert.equal(tabs.activeTab,second);assert.equal(tabs.visible().length,2);
  tabs.close(first);assert.equal(tabs.activeTab,second);tabs.close();assert.equal(tabs.page(),undefined);tabs.newTab();assert.equal(tabs.page().kind,'empty');
});
test('accepted moves retarget forward and evicted historical pages without duplicating history',()=>{
  const tabs=new DocumentTabs(24,1),a=note('Notes/a.md'),b=note('Notes/b.md');tabs.open(a);tabs.open(b);tabs.prune();assert.equal(tabs.get('box','Notes/a.md'),undefined);
  assert.equal(tabs.move('box','Notes','Moved','box/Moved'),true);assert.equal(tabs.page().path,'Moved/b.md');assert.equal(tabs.target(-1).path,'Moved/a.md');
  tabs.open(note('Moved/a.md'),{step:-1});assert.equal(tabs.target(1).path,'Moved/b.md');
});
test('clean hidden document eviction never loses dirty, visible or in-flight work',()=>{
  const tabs=new DocumentTabs(24,1),dirty={...note('dirty.md'),dirty:true},saving=note('saving.md'),clean=note('clean.md'),visible=note('visible.md');
  for(const document of [dirty,saving,clean,visible])tabs.open(document);
  assert.deepEqual(tabs.prune(value=>value===saving),[clean]);assert.equal(tabs.get('box','dirty.md'),dirty);assert.equal(tabs.get('box','saving.md'),saving);assert.equal(tabs.get('box','visible.md'),visible);
});
test('invalid/stale history steps do not advance a tab and trails remain bounded',()=>{
  const tabs=new DocumentTabs(24,64,3);tabs.open(note('a.md'));tabs.open(note('b.md'));
  assert.throws(()=>tabs.open(note('not-previous.md'),{step:-1}),/Navigation changed/);assert.equal(tabs.page().path,'b.md');
  for(let i=0;i<5;i++)tabs.open(note(i+'.md'));assert.equal(tabs.slots.get(tabs.activeTab).history.length,3);
  tabs.open(tabs.get('box','4.md'));assert.equal(tabs.slots.get(tabs.activeTab).history.length,3);
});
