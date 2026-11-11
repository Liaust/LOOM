import test from 'node:test';
import assert from 'node:assert/strict';
import {groupFileMenu} from '../renderer/file-menu-model.js';

const actions=ids=>ids.map(id=>({id,label:id,run:()=>id}));
const groups=values=>{
  const out=[[]];
  for(const value of values)value.separator?out.push([]):out.at(-1).push(value.id);
  assert.ok(out.every(group=>group.length),'no empty or doubled separator groups');
  return out;
};

test('Main folder menu puts creation first, focus second and rare operations last',()=>{
  const source=actions(['timeline','working-copy','project-lifecycle','export-project','copy','cut','upload','new-base','new-folder','rename','move','focus','reveal-tree','new-note','new-canvas','paste','properties','copy-path','find','offline','enrich','trash']);
  assert.deepEqual(groups(groupFileMenu(source)),[
    ['new-note','new-folder','new-canvas','new-base'],
    ['focus','find','reveal-tree'],
    ['rename','move','copy','cut','paste'],
    ['properties','copy-path'],
    ['offline','working-copy','upload'],
    ['timeline','enrich','export-project','project-lifecycle'],
    ['trash'],
  ]);
});
test('note and Mac menus retain only eligible actions, callbacks and recovery styling',()=>{
  const source=actions(['copy-main','copy-local-path','open-background','open-tab','copy','rename','duplicate','new-note','new-folder','reveal-finder','properties','trash']);
  source.at(-1).danger=true;source[0].disabled=true;
  const result=groupFileMenu(source),ids=result.filter(a=>!a.separator).map(a=>a.id);
  assert.deepEqual(ids,['new-note','new-folder','open-tab','open-background','rename','duplicate','copy','properties','copy-local-path','reveal-finder','copy-main','trash']);
  for(const action of source){assert.equal(result.find(a=>a.id===action.id),action);assert.equal(action.run(),action.id);}
  assert.equal(result.at(-1).danger,true);assert.equal(result.find(a=>a.id==='copy-main').disabled,true);
});
test('Storage and Application Data do not acquire writable actions or empty groups',()=>{
  assert.deepEqual(groups(groupFileMenu(actions(['project-lifecycle','working-copy','move-retained','copy-path','properties','find','focus','reveal-tree','offline']))),[
    ['focus','find','reveal-tree'],['properties','copy-path'],['offline','working-copy'],['move-retained','project-lifecycle'],
  ]);
  assert.deepEqual(groups(groupFileMenu(actions(['save-copy','copy-member','open','open-tab','copy-path','browse-attachments','open-background','reveal-tree','copy-local-path','reveal-finder']))),[
    ['open','open-tab','open-background','browse-attachments','reveal-tree'],['copy-path','copy-local-path','copy-member','reveal-finder'],['save-copy'],
  ]);
  assert.deepEqual(groupFileMenu([]),[]);
});
test('localized labels never change action order and duplicate or unclassified IDs are rejected',()=>{
  const source=[{id:'new-folder',label:'A'},{id:'focus',label:'0'},{id:'new-note',label:'Z'}];
  assert.deepEqual(groups(groupFileMenu(source)),[['new-note','new-folder'],['focus']]);
  assert.throws(()=>groupFileMenu(actions(['focus','focus'])),/Invalid file menu action/);
  assert.throws(()=>groupFileMenu(actions(['unknown'])),/Invalid file menu action/);
});
