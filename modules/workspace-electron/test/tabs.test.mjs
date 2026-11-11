import test from 'node:test';
import assert from 'node:assert/strict';
import {DocumentTabs} from '../renderer/tabs.js';

const note=path=>({root:'box',path,entry:{name:path},text:path});
const order=tabs=>tabs.visible().map(slot=>slot.id);
const snapshot=tabs=>JSON.stringify({slots:[...tabs.slots],active:tabs.activeTab,closed:tabs.closed,serial:tabs.serial,items:[...tabs.items]});

test('foreground defaults reuse an empty tab; background creation never reuses or activates it',()=>{
  const tabs=new DocumentTabs(),first=tabs.activeTab;
  assert.equal(tabs.newTab(),first);
  const background=tabs.newTab({activate:false});
  assert.notEqual(background,first);assert.equal(tabs.activeTab,first);
  assert.equal(tabs.page().kind,'empty');assert.equal(tabs.page(background).kind,'empty');
  const fresh=tabs.newTab({reuseEmpty:false});
  assert.notEqual(fresh,first);assert.equal(tabs.activeTab,fresh);
  assert.deepEqual(tabs.visible().map(slot=>slot.pinned),[false,false,false]);
});

test('background opens preserve the active page, history and document even on a blank active tab',()=>{
  const tabs=new DocumentTabs(),first=tabs.activeTab,a=note('a.md');
  assert.equal(tabs.open(a,{background:true}),a);
  assert.equal(tabs.activeTab,first);assert.equal(tabs.page().kind,'empty');
  const second=order(tabs)[1];assert.equal(tabs.page(second).path,'a.md');
  tabs.open(note('active.md'));const history=tabs.slots.get(first).history.slice();
  tabs.open(note('b.md'),{newTab:true,background:true});
  assert.equal(tabs.activeTab,first);assert.deepEqual(tabs.slots.get(first).history,history);
  assert.equal(tabs.active,tabs.key('box','active.md'));
  tabs.open(note('another.md'),{background:true,tab:second});
  assert.equal(tabs.page(second).path,'a.md');assert.equal(tabs.page([...tabs.slots.keys()].at(-1)).path,'another.md');
  assert.equal(tabs.activeTab,first);
  assert.equal(tabs.get('box','a.md'),a);
});

test('explicit nonactivation works for navigation and new file tabs',()=>{
  const tabs=new DocumentTabs();tabs.open(note('a.md'));const first=tabs.activeTab;
  const second=tabs.newTab({activate:false});
  tabs.visit({kind:'knowledge',root:'box',path:'Notes',view:'graph'},{tab:second,activate:false});
  tabs.open(note('b.md'),{tab:second,activate:false});
  tabs.open(note('c.md'),{newTab:true,activate:false});
  assert.equal(tabs.activeTab,first);assert.equal(tabs.page().path,'a.md');
  assert.equal(tabs.page(second).path,'b.md');assert.equal(tabs.target(-1,second).kind,'knowledge');
});

test('background operations work without an active tab and respect capacity without mutation',()=>{
  const tabs=new DocumentTabs(1);tabs.close();
  tabs.open(note('a.md'),{background:true});assert.equal(tabs.activeTab,null);
  const before=snapshot(tabs);
  assert.throws(()=>tabs.newTab({activate:false}),/Close an open file/);
  assert.throws(()=>tabs.open(note('b.md'),{background:true}),/Close an open file/);
  assert.equal(snapshot(tabs),before);
  assert.equal(tabs.cycle(),order(tabs)[0]);
});

test('reordering retains active tab, slots, histories and documents; stale targets are harmless',()=>{
  const tabs=new DocumentTabs(),a=note('a.md');tabs.open(a);const first=tabs.activeTab;
  tabs.open(note('b.md'),{newTab:true});const second=tabs.activeTab;
  tabs.open(note('c.md'),{newTab:true});const third=tabs.activeTab,slot=tabs.slots.get(first);
  assert.equal(tabs.reorder(third,first),true);assert.deepEqual(order(tabs),[third,first,second]);
  assert.equal(tabs.reorder(first),true);assert.deepEqual(order(tabs),[third,second,first]);
  assert.equal(tabs.reorder(first,first),true);const before=snapshot(tabs);
  assert.equal(tabs.reorder('missing',first),false);assert.equal(tabs.reorder(first,'missing'),false);
  assert.equal(snapshot(tabs),before);assert.equal(tabs.activeTab,third);
  assert.equal(tabs.slots.get(first),slot);assert.equal(tabs.get('box','a.md'),a);
});

test('pinning and unpinning expose metadata without moving or activating tabs',()=>{
  const tabs=new DocumentTabs();tabs.open(note('a.md'));const first=tabs.activeTab;
  tabs.open(note('b.md'),{newTab:true});const second=tabs.activeTab;
  assert.equal(tabs.pin(first,true),true);assert.equal(tabs.visible()[0].pinned,true);
  assert.equal(tabs.pin(first,false),true);assert.equal(tabs.visible()[0].pinned,false);
  assert.equal(tabs.pin('missing',true),false);assert.deepEqual(order(tabs),[first,second]);
  assert.equal(tabs.activeTab,second);assert.throws(()=>tabs.pin(first,'yes'),/boolean/);
  assert.equal(tabs.visible()[0].pinned,false);
});

test('reopening restores identity, position, cursor, forward history and pin while retaining draft objects',()=>{
  const tabs=new DocumentTabs(),draft={...note('draft.md'),dirty:true,held:true,selection:[2,4,50]};
  tabs.open(note('first.md'));const first=tabs.activeTab;
  tabs.open(draft,{newTab:true});const second=tabs.activeTab;tabs.pin(second,true);
  tabs.open(note('forward.md'));tabs.open(draft,{step:-1});const slot=tabs.slots.get(second);
  tabs.open(note('last.md'),{newTab:true});const third=tabs.activeTab;
  tabs.close(second);assert.equal(tabs.activeTab,third);assert.equal(tabs.get('box','draft.md'),draft);
  assert.equal(tabs.reopenClosed(),second);assert.deepEqual(order(tabs),[first,second,third]);
  assert.equal(tabs.activeTab,second);assert.equal(tabs.slots.get(second),slot);
  assert.equal(tabs.page().path,'draft.md');assert.equal(tabs.target(1).path,'forward.md');
  assert.equal(tabs.visible()[1].pinned,true);assert.equal(tabs.visible()[1].value,draft);
  assert.deepEqual(draft.selection,[2,4,50]);assert.equal(draft.text,'draft.md');assert.equal(draft.held,true);
  assert.equal(tabs.reopenClosed(),null);
});

test('closed recovery is LIFO and bounded; failed reopen keeps history for retry',()=>{
  const tabs=new DocumentTabs(2,64,128,2),ids=[];
  for(const path of ['a.md','b.md','c.md']){tabs.open(note(path));ids.push(tabs.activeTab);tabs.close();}
  assert.equal(tabs.closed.length,2);assert.equal(tabs.reopenClosed(),ids[2]);assert.equal(tabs.reopenClosed(),ids[1]);
  assert.equal(tabs.reopenClosed(),null);assert.equal(tabs.get('box','a.md').path,'a.md');
  tabs.close(ids[2]);tabs.open(note('d.md'),{newTab:true});const before=snapshot(tabs);
  assert.throws(()=>tabs.reopenClosed(),/Close an open file/);assert.equal(snapshot(tabs),before);
  const current=tabs.activeTab;tabs.close();assert.equal(tabs.reopenClosed(),current);
});

test('invalid closes do not create recovery entries, including after the last tab closes',()=>{
  const tabs=new DocumentTabs(),first=tabs.activeTab;
  assert.equal(tabs.close('missing').kind,'empty');assert.equal(tabs.closed.length,0);
  assert.equal(tabs.close(),null);assert.equal(tabs.closed.length,1);
  assert.equal(tabs.close(),null);assert.equal(tabs.closed.length,1);
  assert.equal(tabs.reopenClosed(),first);assert.equal(tabs.page().kind,'empty');
  const disabled=new DocumentTabs(1,0,1,0);disabled.close();assert.equal(disabled.reopenClosed(),null);
});

test('closed navigation paths follow accepted moves without discarding cached dirty/held models',()=>{
  const tabs=new DocumentTabs(24,0),draft={...note('Notes/a.md'),dirty:true},held={...note('Notes/b.md'),held:true};
  tabs.open(draft);tabs.open(held);const id=tabs.activeTab;tabs.close();
  assert.equal(tabs.move('box','Notes','Moved','box/Moved'),true);
  assert.deepEqual(tabs.prune(),[]);assert.equal(tabs.reopenClosed(),id);
  assert.equal(tabs.page().path,'Moved/b.md');assert.equal(tabs.target(-1).path,'Moved/a.md');
  assert.equal(tabs.get('box','Moved/a.md'),draft);assert.equal(tabs.get('box','Moved/b.md'),held);
});

test('cycling follows reordered slots, wraps both directions and includes pinned tabs',()=>{
  const tabs=new DocumentTabs();tabs.open(note('a.md'));const first=tabs.activeTab;
  tabs.open(note('b.md'),{newTab:true});const second=tabs.activeTab;
  tabs.open(note('c.md'),{newTab:true});const third=tabs.activeTab;tabs.pin(first,true);
  tabs.reorder(third,first);assert.equal(tabs.cycle(),first);assert.equal(tabs.cycle(-1),third);
  assert.equal(tabs.cycle(-1),second);assert.equal(tabs.cycle(2),first);
  assert.equal(tabs.cycle(0),first);assert.equal(tabs.cycle(3),first);
  assert.equal(tabs.cycle(Number.MAX_SAFE_INTEGER),second);
  const before=snapshot(tabs);for(const step of [NaN,Infinity,0.5,'1',null])assert.throws(()=>tabs.cycle(step),/safe integer/);
  assert.equal(snapshot(tabs),before);for(const id of order(tabs))tabs.close(id);
  assert.equal(tabs.cycle(),null);
});

test('restoration retains order and pins, defaults legacy pins to false, caps history and advances IDs',()=>{
  const history=['a.md','b.md','c.md','d.md'].map(path=>({kind:'file',root:'box',path}));
  const session={tabs:[{id:'tab_8',history,cursor:2,pinned:true},{id:'tab_3',history:[{kind:'empty'}],cursor:0}],active:'tab_8'};
  const tabs=new DocumentTabs(24,64,2);tabs.close();tabs.restore(session);
  assert.deepEqual(order(tabs),['tab_8','tab_3']);assert.equal(tabs.page().path,'c.md');
  assert.equal(tabs.target(-1).path,'b.md');assert.equal(tabs.slots.get('tab_8').history.length,2);
  assert.deepEqual(tabs.visible().map(slot=>slot.pinned),[true,false]);assert.equal(tabs.closed.length,0);
  tabs.page().path='changed.md';assert.equal(history[2].path,'c.md');assert.equal(tabs.items.size,0);
  assert.equal(tabs.newTab(),'tab_9');
});

test('invalid session restoration is atomic and cannot run over opened document models',()=>{
  const tabs=new DocumentTabs(2),slot={id:'tab_9',history:[{kind:'empty'}],cursor:0},before=snapshot(tabs);
  for(const session of [null,{tabs:[],active:null},{tabs:[slot],active:'missing'},{tabs:[slot,slot],active:slot.id},
    {tabs:[{...slot,id:'invalid'}],active:'invalid'},{tabs:[{...slot,cursor:2}],active:slot.id},
    {tabs:[{...slot,pinned:'yes'}],active:slot.id},{tabs:[{...slot,history:[{kind:'file'}]}],active:slot.id},
    {tabs:[{...slot,history:new Array(1)}],active:slot.id},
    {tabs:[slot,{...slot,id:'tab_10'},{...slot,id:'tab_11'}],active:slot.id}]){
    assert.throws(()=>tabs.restore(session));assert.equal(snapshot(tabs),before);
  }
  tabs.open(note('a.md'));assert.throws(()=>tabs.restore({tabs:[slot],active:slot.id}),/before opening documents/);
  assert.equal(tabs.page().path,'a.md');
});

test('invalid arguments never allocate tabs, advance history or change cached documents',()=>{
  const tabs=new DocumentTabs();tabs.open(note('a.md'));const before=snapshot(tabs);
  for(const options of [{activate:0},{reuseEmpty:'yes'}])assert.throws(()=>tabs.newTab(options),/boolean/);
  for(const options of [{newTab:'yes'},{background:'yes'},{background:true,activate:true},{newTab:true,step:-1},
    {newTab:true,record:1},{newTab:true,step:0.5},{tab:'missing',activate:false}])assert.throws(()=>tabs.open(note('b.md'),options));
  for(const value of [null,{}, {root:1,path:'a.md'},{root:'box',path:42}])assert.throws(()=>tabs.open(value,{newTab:true}));
  assert.throws(()=>tabs.visit(undefined));assert.throws(()=>tabs.visit({kind:'empty'},{step:NaN}));
  assert.equal(snapshot(tabs),before);
  for(const args of [[0],[-1],[NaN],[1,-1],[1,0,0],[1,0,1,-1],[1.5]])assert.throws(()=>new DocumentTabs(...args),/safe integer/);
});
